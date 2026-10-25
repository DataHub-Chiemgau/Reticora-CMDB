package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredExtensions are the PostgreSQL extensions the migrations install and
// the server relies on (TEC-06).
var RequiredExtensions = []string{"pgcrypto", "btree_gist", "timescaledb"}

// readinessTimeout bounds every single check so a hanging dependency turns
// into "unavailable" instead of a hanging probe.
const readinessTimeout = 2 * time.Second

// readinessDetailTimeout bounds every detail. A detail is informational, so
// it must not hold up the answer: the probe of the orchestrator times out
// after one second by default, and a slow answer would take the instance out
// of the load balancer although every check passed.
const readinessDetailTimeout = 300 * time.Millisecond

// ReadinessCheck is one dependency that /readyz verifies (OPS-01). Check
// returns nil when the dependency is usable. Detail optionally reports
// operational figures under details.<Name> (the ingest backlog per lane,
// WP-334) within readinessDetailTimeout, or "unavailable"; a check with only
// a Detail is informational: it is not listed in checks and never changes
// the status, and a slow Detail never delays the answer by more than
// readinessDetailTimeout.
type ReadinessCheck struct {
	Name   string
	Check  func(ctx context.Context) error
	Detail func(ctx context.Context) (any, error)
}

type readinessResponse struct {
	Status  string            `json:"status"`
	Checks  map[string]string `json:"checks"`
	Details map[string]any    `json:"details,omitempty"`
}

// readinessHandler runs all checks in parallel and answers 200 when every
// check passes and 503 otherwise. The endpoint is unauthenticated, so the
// response only names the failing dependency; the error goes to the log.
// Details carry no tenant data.
func readinessHandler(checks []ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := readinessResponse{Status: "ready", Checks: make(map[string]string, len(checks))}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			if c.Detail != nil {
				wg.Add(1)
				go func(c ReadinessCheck) {
					defer wg.Done()
					detail := readinessDetail(r.Context(), c)
					mu.Lock()
					defer mu.Unlock()
					if resp.Details == nil {
						resp.Details = map[string]any{}
					}
					resp.Details[c.Name] = detail
				}(c)
			}
			if c.Check == nil {
				continue
			}
			wg.Add(1)
			go func(c ReadinessCheck) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
				defer cancel()
				err := c.Check(ctx)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					slog.Warn("readiness check failed", "check", c.Name, "error", err)
					resp.Checks[c.Name] = "unavailable"
					resp.Status = "not_ready"
					return
				}
				resp.Checks[c.Name] = "ok"
			}(c)
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if resp.Status != "ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// readinessDetail returns the detail of c, or "unavailable" when it fails or
// takes longer than readinessDetailTimeout; a Detail that outlasts its
// context finishes in the background. A failure is logged as a warning,
// except a detail that has no value yet (discovery.ErrNoBacklogSample: the
// ingest worker has not sampled since the start): that state is expected,
// and the endpoint is unauthenticated and probed every few seconds.
func readinessDetail(parent context.Context, c ReadinessCheck) any {
	ctx, cancel := context.WithTimeout(parent, readinessDetailTimeout)
	defer cancel()
	type result struct {
		detail any
		err    error
	}
	done := make(chan result, 1)
	go func() {
		detail, err := c.Detail(ctx)
		done <- result{detail, err}
	}()
	var err error
	select {
	case res := <-done:
		if res.err == nil {
			return res.detail
		}
		err = res.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	if errors.Is(err, discovery.ErrNoBacklogSample) {
		slog.Debug("readiness detail not available yet", "check", c.Name, "error", err)
	} else {
		slog.Warn("readiness detail failed", "check", c.Name, "error", err)
	}
	return map[string]string{"error": "unavailable"}
}

// DDLGuardEventTrigger is the event trigger of migration 000078 that
// restricts the application roles to index DDL (CH19). The update of the
// TimescaleDB extension disables it for the duration of the update
// (scripts/ops/timescaledb-update.sh, WP-604); an aborted update could leave
// it disabled.
const DDLGuardEventTrigger = "reticora_restrict_app_ddl"

// DatabaseReadiness checks that PostgreSQL answers, that the given
// extensions are installed and that DDLGuardEventTrigger exists and is
// enabled (OPS-01, WP-604). Both are catalog lookups. The first check — the
// start check of cmd/server — also logs a warning for every given extension
// that is not at the default version of its installed library: after an
// image update TimescaleDB keeps its version until
// scripts/ops/timescaledb-update.sh raises it. That comparison reads the
// extension control files of the server, so it is not repeated per probe,
// and the versions go to the log only, not to the unauthenticated /readyz.
func DatabaseReadiness(pool *pgxpool.Pool, extensions []string) ReadinessCheck {
	var versionsCompared atomic.Bool
	return ReadinessCheck{Name: "database", Check: func(ctx context.Context) error {
		var (
			missing []string
			trigger *string
		)
		err := pool.QueryRow(ctx, `
			SELECT ARRAY(SELECT e FROM unnest($1::text[]) AS e
			              WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = e)
			              ORDER BY e),
			       (SELECT evtenabled::text FROM pg_event_trigger WHERE evtname = $2)`,
			extensions, DDLGuardEventTrigger).Scan(&missing, &trigger)
		if err != nil {
			return fmt.Errorf("query database: %w", err)
		}
		if versionsCompared.CompareAndSwap(false, true) {
			warnOutdatedExtensions(ctx, pool, extensions)
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing PostgreSQL extensions: %s", strings.Join(missing, ", "))
		}
		switch {
		case trigger == nil:
			return fmt.Errorf("event trigger %s is missing; apply the migrations", DDLGuardEventTrigger)
		// O: enabled, A: always; D (disabled) and R (replica only) do not
		// fire in the sessions of the application.
		case *trigger != "O" && *trigger != "A":
			return fmt.Errorf("event trigger %s is not enabled (evtenabled %s); "+
				"run scripts/ops/timescaledb-update.sh or, as the bootstrap superuser, ALTER EVENT TRIGGER %s ENABLE",
				DDLGuardEventTrigger, *trigger, DDLGuardEventTrigger)
		}
		return nil
	}}
}

// warnOutdatedExtensions logs a warning for every extension of extensions
// whose installed version differs from the default version of the library
// the server ships.
func warnOutdatedExtensions(ctx context.Context, pool *pgxpool.Pool, extensions []string) {
	rows, err := pool.Query(ctx, `
		SELECT e.extname::text, e.extversion, a.default_version
		  FROM pg_extension e JOIN pg_available_extensions a ON a.name = e.extname
		 WHERE e.extname = ANY($1::text[]) AND e.extversion IS DISTINCT FROM a.default_version
		 ORDER BY e.extname`, extensions)
	if err != nil {
		slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, version, defaultVersion string
		if err := rows.Scan(&name, &version, &defaultVersion); err != nil {
			slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
			return
		}
		slog.Warn("PostgreSQL extension is not at the version of its installed library; "+
			"update it with scripts/ops/timescaledb-update.sh (TimescaleDB) or ALTER EXTENSION <name> UPDATE",
			"extension", name, "version", version, "default_version", defaultVersion)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("could not compare the PostgreSQL extension versions", "error", err)
	}
}

// TCPReadiness checks that a TCP connection to address (host:port) can be
// opened. It serves dependencies without a cheap protocol-level probe here
// (object storage endpoint, NATS).
func TCPReadiness(name, address string) ReadinessCheck {
	return ReadinessCheck{Name: name, Check: func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		return conn.Close()
	}}
}
