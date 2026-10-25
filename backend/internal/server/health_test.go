package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// TestReadyzDetailNotSampledIsQuiet covers the review of WP-334: the ingest
// detail fails with discovery.ErrNoBacklogSample until the ingest worker has
// sampled the backlog. /readyz is unauthenticated and probed every 5 s, so
// that expected state must not write a WARN line per request; it is still
// reported as unavailable. Any other failure keeps its WARN.
func TestReadyzDetailNotSampledIsQuiet(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pending := ReadinessCheck{Name: "ingest", Detail: func(context.Context) (any, error) {
		return nil, fmt.Errorf("lane backlog: %w", discovery.ErrNoBacklogSample)
	}}
	for range 3 {
		code, body, raw := serveReadiness(t, ok("database"), pending)
		if code != http.StatusOK || body.Status != "ready" || !strings.Contains(raw, `"details":{"ingest":{"error":"unavailable"}}`) {
			t.Fatalf("detail not sampled yet: %d %s", code, raw)
		}
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a detail not sampled yet wrote a warning:\n%s", logs.String())
	}

	logs.Reset()
	broken := ReadinessCheck{Name: "ingest", Detail: func(context.Context) (any, error) {
		return nil, errors.New("nats: consumer info failed")
	}}
	serveReadiness(t, ok("database"), broken)
	if !strings.Contains(logs.String(), `level=WARN msg="readiness detail failed" check=ingest`) {
		t.Errorf("a failing detail wrote no warning:\n%s", logs.String())
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
// pass, a missing one is reported by name. The runtime login reads the
// enabled event trigger of the migrated database (WP-604).
func TestDatabaseReadiness(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := database.NewPool(ctx, scopetest.AppDSN(t))
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

// TestDatabaseReadinessDDLGuardAndExtensionVersion covers WP-604 (OPS-01) in
// a database of its own, so the event trigger of the shared test database
// stays enabled: the start check and /readyz report a disabled, replica-only
// or missing reticora_restrict_app_ddl as not ready, without naming it in the
// response; an extension below the default version of its library is a
// warning in the log of the first check only and does not change the result.
func TestDatabaseReadinessDDLGuardAndExtensionVersion(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	// The event trigger of migration 000078.
	const ddlGuardTrigger = "reticora_restrict_app_ddl"
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("reticora_wp604_readyz_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, dropErr := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop database %s: %v", name, dropErr)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// An extension created before an image update keeps its version; the
	// trigger body does not matter for the check.
	exec(`CREATE EXTENSION btree_gist VERSION '1.6'`)
	exec(`CREATE FUNCTION wp604_noop() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`)
	exec(`CREATE EVENT TRIGGER ` + ddlGuardTrigger + ` ON ddl_command_start EXECUTE FUNCTION wp604_noop()`)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const outdated = `level=WARN msg="PostgreSQL extension is not at the version of its installed library; ` +
		`update it with scripts/ops/timescaledb-update.sh (TimescaleDB) or ALTER EXTENSION <name> UPDATE" ` +
		`extension=btree_gist version=1.6 default_version=1.7`

	check := DatabaseReadiness(pool, []string{"btree_gist"})
	if err := check.Check(ctx); err != nil {
		t.Fatalf("enabled trigger: %v", err)
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 1 || !strings.Contains(logs.String(), outdated) {
		t.Errorf("start check with btree_gist 1.6: want exactly the warning\n%s\ngot\n%s", outdated, logs.String())
	}
	logs.Reset()
	if err := check.Check(ctx); err != nil || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("second check: err = %v, the warning must not repeat per probe:\n%s", err, logs.String())
	}

	for _, tc := range []struct {
		alter, want string
	}{
		{"ALTER EVENT TRIGGER " + ddlGuardTrigger + " DISABLE", "event trigger reticora_restrict_app_ddl is not enabled (evtenabled D)"},
		{"ALTER EVENT TRIGGER " + ddlGuardTrigger + " ENABLE REPLICA", "event trigger reticora_restrict_app_ddl is not enabled (evtenabled R)"},
		{"ALTER EVENT TRIGGER " + ddlGuardTrigger + " ENABLE ALWAYS", ""},
		{"ALTER EVENT TRIGGER " + ddlGuardTrigger + " ENABLE", ""},
		{"DROP EVENT TRIGGER " + ddlGuardTrigger, "event trigger reticora_restrict_app_ddl is missing"},
	} {
		exec(tc.alter)
		err := check.Check(ctx)
		if tc.want == "" {
			if err != nil {
				t.Errorf("after %s: %v", tc.alter, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("after %s: err = %v, want %q", tc.alter, err, tc.want)
		}
		code, body, raw := serveReadiness(t, check)
		if code != http.StatusServiceUnavailable || body.Status != "not_ready" || body.Checks["database"] != "unavailable" ||
			strings.Contains(raw, ddlGuardTrigger) {
			t.Errorf("/readyz after %s: %d %s", tc.alter, code, raw)
		}
	}

	// At the default version a new start check stays quiet.
	exec(`ALTER EXTENSION btree_gist UPDATE`)
	exec(`CREATE EVENT TRIGGER ` + ddlGuardTrigger + ` ON ddl_command_start EXECUTE FUNCTION wp604_noop()`)
	logs.Reset()
	if err := DatabaseReadiness(pool, []string{"btree_gist"}).Check(ctx); err != nil || strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("current extension: err = %v, log:\n%s", err, logs.String())
	}
}
