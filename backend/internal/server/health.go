package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredExtensions are the PostgreSQL extensions the migrations install and
// the server relies on (TEC-06).
var RequiredExtensions = []string{"pgcrypto", "btree_gist", "timescaledb"}

// readinessTimeout bounds every single check so a hanging dependency turns
// into "unavailable" instead of a hanging probe.
const readinessTimeout = 2 * time.Second

// ReadinessCheck is one dependency that /readyz verifies (OPS-01). Check
// returns nil when the dependency is usable.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

type readinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// readinessHandler runs all checks in parallel and answers 200 when every
// check passes and 503 otherwise. The endpoint is unauthenticated, so the
// response only names the failing dependency; the error goes to the log.
func readinessHandler(checks []ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := readinessResponse{Status: "ready", Checks: make(map[string]string, len(checks))}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
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
