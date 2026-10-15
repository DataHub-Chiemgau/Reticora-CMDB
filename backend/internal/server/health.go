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

// DatabaseReadiness checks that PostgreSQL answers and that the given
// extensions are installed.
func DatabaseReadiness(pool *pgxpool.Pool, extensions []string) ReadinessCheck {
	return ReadinessCheck{Name: "database", Check: func(ctx context.Context) error {
		var missing []string
		err := pool.QueryRow(ctx, `
			SELECT COALESCE(array_agg(e ORDER BY e), '{}')
			FROM unnest($1::text[]) AS e
			WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = e)`, extensions).Scan(&missing)
		if err != nil {
			return fmt.Errorf("query database: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing PostgreSQL extensions: %s", strings.Join(missing, ", "))
		}
		return nil
	}}
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
