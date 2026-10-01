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
