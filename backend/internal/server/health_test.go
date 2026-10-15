package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/go-chi/chi/v5"
)

func ok(name string) ReadinessCheck {
	return ReadinessCheck{Name: name, Check: func(context.Context) error { return nil }}
}

func serveReadiness(t *testing.T, checks ...ReadinessCheck) (int, readinessResponse, string) {
	t.Helper()
	mux := chi.NewRouter()
	registerOperational(mux, "test", false, checks)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body, rec.Body.String()
}

// TestReadyzAllChecksPass: OPS-01 – 200 and every check reported as ok.
func TestReadyzAllChecksPass(t *testing.T) {
	code, body, _ := serveReadiness(t, ok("database"), ok("redis"))
	if code != http.StatusOK || body.Status != "ready" {
		t.Fatalf("got %d %q, want 200 ready", code, body.Status)
	}
	if body.Checks["database"] != "ok" || body.Checks["redis"] != "ok" {
		t.Fatalf("checks = %v", body.Checks)
	}
}

// TestReadyzFailingCheckIs503: one failing dependency makes the instance not
// ready; the response names it but does not leak the error text.
func TestReadyzFailingCheckIs503(t *testing.T) {
	failing := ReadinessCheck{Name: "redis", Check: func(context.Context) error {
		return errors.New("dial tcp 10.0.0.7:6379: secret detail")
	}}
	code, body, raw := serveReadiness(t, ok("database"), failing)
	if code != http.StatusServiceUnavailable || body.Status != "not_ready" {
		t.Fatalf("got %d %q, want 503 not_ready", code, body.Status)
	}
	if body.Checks["redis"] != "unavailable" || body.Checks["database"] != "ok" {
		t.Fatalf("checks = %v", body.Checks)
	}
	if strings.Contains(raw, "secret detail") || strings.Contains(raw, "10.0.0.7") {
		t.Fatalf("response leaks the error: %s", raw)
	}
}

// TestReadyzHangingCheckTimesOut: a dependency that never answers is
// reported as unavailable within the per-check timeout.
func TestReadyzHangingCheckTimesOut(t *testing.T) {
	hanging := ReadinessCheck{Name: "nats", Check: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	start := time.Now()
	code, body, _ := serveReadiness(t, hanging)
	if elapsed := time.Since(start); elapsed > readinessTimeout+time.Second {
		t.Fatalf("probe took %v, want about %v", elapsed, readinessTimeout)
	}
	if code != http.StatusServiceUnavailable || body.Checks["nats"] != "unavailable" {
		t.Fatalf("got %d %v", code, body.Checks)
	}
}

// TestReadyzWithoutChecks: --no-db development mode has no dependencies.
func TestReadyzWithoutChecks(t *testing.T) {
	code, body, _ := serveReadiness(t)
	if code != http.StatusOK || body.Status != "ready" || len(body.Checks) != 0 {
		t.Fatalf("got %d %+v", code, body)
	}
}

// TestReadyzDetails covers WP-334 (OPS-05): a check with a Detail reports
// it under details; a Detail-only check is not listed in checks and never
// changes the status, not even when it fails, and its error is not leaked.
func TestReadyzDetails(t *testing.T) {
	backlog := ReadinessCheck{Name: "ingest", Detail: func(context.Context) (any, error) {
		return map[string]any{"lanes": 4, "backlog": map[string]int{"0": 12, "1": 0, "legacy": 0}, "total": 12}, nil
	}}
	code, body, raw := serveReadiness(t, ok("database"), backlog)
	if code != http.StatusOK || body.Status != "ready" || len(body.Checks) != 1 || body.Checks["database"] != "ok" {
		t.Fatalf("got %d %+v", code, body)
	}
	if !strings.Contains(raw, `"details":{"ingest":{"backlog":{"0":12,"1":0,"legacy":0},"lanes":4,"total":12}}`) {
		t.Fatalf("details: %s", raw)
	}

	broken := ReadinessCheck{Name: "ingest", Detail: func(context.Context) (any, error) {
		return nil, errors.New("nats: secret detail")
	}}
	code, body, raw = serveReadiness(t, ok("database"), broken)
	if code != http.StatusOK || body.Status != "ready" || strings.Contains(raw, "secret detail") ||
		!strings.Contains(raw, `"details":{"ingest":{"error":"unavailable"}}`) {
		t.Fatalf("failing detail: %d %s", code, raw)
	}

	// Without a Detail the response has no details.
	if _, _, raw = serveReadiness(t, ok("database")); strings.Contains(raw, "details") {
		t.Fatalf("details without a Detail: %s", raw)
	}
}

// TestReadyzSlowDetail: an informational detail never holds up the probe
// (review finding, WP-334). The ingest backlog asks JetStream on every
// /readyz; a stalled JetStream used to keep the answer back for the whole
// readinessTimeout, past the orchestrator's default probe timeout of one
// second, so every instance left the load balancer although every check
// passed. A detail that waits for its context, or ignores it, is reported
// as unavailable within readinessDetailTimeout; the checks are unaffected.
func TestReadyzSlowDetail(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	for name, detail := range map[string]func(context.Context) (any, error){
		"until its context ends": func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		"ignoring its context": func(context.Context) (any, error) {
			<-stop
			return map[string]int{"total": 1}, nil
		},
	} {
		start := time.Now()
		code, body, raw := serveReadiness(t, ok("database"), ReadinessCheck{Name: "ingest", Detail: detail})
		if elapsed := time.Since(start); elapsed > readinessDetailTimeout+200*time.Millisecond {
			t.Errorf("detail %s: /readyz answered after %v, want within %v", name, elapsed, readinessDetailTimeout)
		}
		if code != http.StatusOK || body.Status != "ready" || body.Checks["database"] != "ok" ||
			!strings.Contains(raw, `"details":{"ingest":{"error":"unavailable"}}`) {
			t.Errorf("detail %s: %d %s", name, code, raw)
		}
	}
	if readinessDetailTimeout >= time.Second {
		t.Errorf("readinessDetailTimeout %v must stay below the default probe timeout of one second", readinessDetailTimeout)
	}
}

func TestHealthzStaysLiveness(t *testing.T) {
	mux := chi.NewRouter()
	failing := ReadinessCheck{Name: "database", Check: func(context.Context) error { return errors.New("down") }}
	registerOperational(mux, "test", false, []ReadinessCheck{failing})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 even when a dependency is down", rec.Code)
	}
}

func TestTCPReadiness(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if err := TCPReadiness("s", addr).Check(context.Background()); err != nil {
		t.Fatalf("open port: %v", err)
	}
	_ = ln.Close()
	if err := TCPReadiness("s", addr).Check(context.Background()); err == nil {
		t.Fatal("closed port reported as ready")
	}
}

// TestDatabaseReadiness runs against TEST_DATABASE_URL: installed extensions
// pass, a missing one is reported by name.
func TestDatabaseReadiness(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if okErr := DatabaseReadiness(pool, []string{"pgcrypto", "btree_gist"}).Check(ctx); okErr != nil {
		t.Fatalf("installed extensions: %v", okErr)
	}
	err = DatabaseReadiness(pool, []string{"pgcrypto", "reticora_missing_ext"}).Check(ctx)
	if err == nil || !strings.Contains(err.Error(), "reticora_missing_ext") || strings.Contains(err.Error(), "pgcrypto") {
		t.Fatalf("missing extension: err = %v", err)
	}
}
