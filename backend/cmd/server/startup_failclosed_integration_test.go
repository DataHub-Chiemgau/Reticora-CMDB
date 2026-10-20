package main

// Phase-1 acceptance audit, group T5-install-ops-gate: REGRESSION test (must
// pass).
//
// WP-097 (OPS-01) test gap high "The fail-closed startup in main.go has no
// repository test; only audit probes": the real server binary is built and
// started with a production configuration against the test PostgreSQL and
// Redis. The server logs in with the runtime login of the tests
// (scopetest.AppDSN, no SUPERUSER/BYPASSRLS, TEN-03, WP-351), as in the
// packaged installation; TEST_DATABASE_URL is only the admin login AppDSN
// derives it from. Every missing or unusable mandatory dependency must stop the start
// with exit code 1 and a specific message (fail-closed); a complete
// configuration must become ready (/readyz 200).
//
// Not covered here: a fresh database without TimescaleDB ("database is not
// ready") — it needs a second, unmigrated database on the test server.
//
// The helpers below (binary build, environment, fake OIDC/S3 endpoints) are
// shared with startup_readiness_auditdefect_test.go.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_08?sslmode=disable \
//     TEST_REDIS_URL=redis://localhost:56379/0 \
//     go test -count=1 -v -run 'TestAuditT5ServerStartupFailClosed' ./cmd/server/

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

var (
	auditServerOnce sync.Once
	auditServerDir  string
	auditServerBin  string
	auditServerErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if auditServerDir != "" {
		_ = os.RemoveAll(auditServerDir)
	}
	os.Exit(code)
}

// auditServerBinary builds this package's server binary once per test run.
func auditServerBinary(t *testing.T) string {
	t.Helper()
	auditServerOnce.Do(func() {
		auditServerDir, auditServerErr = os.MkdirTemp("", "reticora-server-audit-*")
		if auditServerErr != nil {
			return
		}
		auditServerBin = filepath.Join(auditServerDir, "server")
		cmd := exec.Command("go", "build", "-o", auditServerBin, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			auditServerErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if auditServerErr != nil {
		t.Fatal(auditServerErr)
	}
	return auditServerBin
}

func auditFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port
}

// auditClosedAddress is a 127.0.0.1 port nothing listens on.
func auditClosedAddress(t *testing.T) string {
	t.Helper()
	return "127.0.0.1:" + auditFreePort(t)
}

// auditFakeOIDC serves a discovery document for <url>/realms/reticora.
func auditFakeOIDC(t *testing.T) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := srv.URL + "/realms/reticora"
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
			issuer, issuer+"/protocol/openid-connect/auth", issuer+"/protocol/openid-connect/token", issuer+"/protocol/openid-connect/certs")
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/realms/reticora"
}

// auditFakeS3 answers every S3 call of the startup (bucket exists, enable
// versioning, location) with success.
func auditFakeS3(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["location"]; ok {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// auditProductionEnv is a complete production configuration against the
// test database (runtime login, scopetest.AppDSN) and Redis, without NATS (the shared NATS server is not
// isolated per test) and with every worker disabled.
func auditProductionEnv(t *testing.T) map[string]string {
	t.Helper()
	redis := os.Getenv("TEST_REDIS_URL")
	if os.Getenv("TEST_DATABASE_URL") == "" || redis == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for the server startup test")
	}
	db := scopetest.AppDSN(t)
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"RETICORA_ENVIRONMENT":      "production",
		"RETICORA_PORT":             auditFreePort(t),
		"RETICORA_DATABASE_URL":     db,
		"RETICORA_REDIS_URL":        redis,
		"RETICORA_MASTER_KEY":       key,
		"RETICORA_OIDC_ISSUER_URL":  auditFakeOIDC(t),
		"RETICORA_SESSION_KEY_PATH": testSessionKeyPath(t),
	}
	for _, name := range config.WorkerNames {
		env[config.WorkerEnvKey(name)] = "false"
	}
	return env
}

type auditServerRun struct {
	exitCode   int // -1 while running / killed by the test
	output     string
	readyCode  int
	readyBody  string
	readyError error
}

// auditStartServer starts the binary with exactly env (plus PATH/HOME). With
// waitReady it polls /readyz until it answers and then stops the server;
// otherwise it waits for the process to exit. timeout bounds both.
func auditStartServer(t *testing.T, env map[string]string, waitReady bool, timeout time.Duration) auditServerRun {
	t.Helper()
	bin := auditServerBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	run := auditServerRun{exitCode: -1}
	exitCode := func(err error) int {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err == nil {
			return 0
		}
		return -1
	}
	if !waitReady {
		select {
		case err := <-done:
			run.exitCode = exitCode(err)
		case <-ctx.Done():
			run.exitCode = -1
			<-done
		}
		run.output = out.String()
		return run
	}
	url := "http://127.0.0.1:" + env["RETICORA_PORT"] + "/readyz"
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(timeout - 5*time.Second)
	for {
		select {
		case err := <-done:
			run.exitCode = exitCode(err)
			run.output = out.String()
			run.readyError = fmt.Errorf("server exited before /readyz answered")
			return run
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			run.readyCode, run.readyBody = resp.StatusCode, strings.TrimSpace(string(body))
			break
		}
		if time.Now().After(deadline) {
			run.readyError = fmt.Errorf("/readyz did not answer within %s: %v", timeout, err)
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	run.output = out.String()
	return run
}

// auditRequireRuntimeLogin fails the test when the login of dsn is a
// superuser or has BYPASSRLS: the start would then not prove that the server
// runs with the runtime login of the packaged installation.
func auditRequireRuntimeLogin(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("runtime login: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var login string
	var super, bypass bool
	if err := conn.QueryRow(ctx, `SELECT rolname, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = session_user`).
		Scan(&login, &super, &bypass); err != nil {
		t.Fatalf("runtime login: %v", err)
	}
	if super || bypass {
		t.Fatalf("server login %s is superuser=%t bypassrls=%t; the startup test must use the runtime login (scopetest.AppDSN)", login, super, bypass)
	}
}

func auditTail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestAuditT5ServerStartupFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, env map[string]string)
		message string
	}{
		{"missing OIDC issuer", func(_ *testing.T, env map[string]string) { delete(env, "RETICORA_OIDC_ISSUER_URL") },
			"requires RETICORA_OIDC_ISSUER_URL"},
		{"missing master key", func(_ *testing.T, env map[string]string) { delete(env, "RETICORA_MASTER_KEY") },
			"requires RETICORA_MASTER_KEY"},
		{"invalid master key", func(_ *testing.T, env map[string]string) { env["RETICORA_MASTER_KEY"] = "not-a-base64-key" },
			"failed to init envelope encryptor"},
		{"missing session key", func(t *testing.T, env map[string]string) {
			env["RETICORA_SESSION_KEY_PATH"] = filepath.Join(t.TempDir(), "absent.pem")
		}, "session token setup failed"},
		{"redis unreachable", func(t *testing.T, env map[string]string) {
			env["RETICORA_REDIS_URL"] = "redis://" + auditClosedAddress(t) + "/0"
		}, "failed to connect to Redis"},
		{"database unreachable", func(t *testing.T, env map[string]string) {
			env["RETICORA_DATABASE_URL"] = "postgres://reticora:reticora@" + auditClosedAddress(t) + "/x?sslmode=disable"
		}, "failed to connect to database"},
		{"object storage unreachable", func(t *testing.T, env map[string]string) {
			env["RETICORA_S3_ENDPOINT"] = auditClosedAddress(t)
			env["RETICORA_S3_ACCESS_KEY"] = "audit"
			env["RETICORA_S3_SECRET_KEY"] = "audit-secret"
		}, "S3 blob storage unavailable"},
		{"partial object storage configuration", func(t *testing.T, env map[string]string) {
			env["RETICORA_S3_ENDPOINT"] = auditClosedAddress(t)
		}, "requires RETICORA_S3_ACCESS_KEY, RETICORA_S3_SECRET_KEY"},
		{"NATS unreachable", func(t *testing.T, env map[string]string) {
			env["RETICORA_NATS_URL"] = "nats://" + auditClosedAddress(t)
		}, "failed to set up the ingest queue on NATS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := auditProductionEnv(t)
			tc.mutate(t, env)
			run := auditStartServer(t, env, false, 90*time.Second)
			if run.exitCode != 1 {
				t.Errorf("exit code = %d, want 1 (fail-closed start)\n%s", run.exitCode, auditTail(run.output, 5))
			}
			if !strings.Contains(run.output, tc.message) {
				t.Errorf("start output lacks %q\n%s", tc.message, auditTail(run.output, 5))
			}
		})
	}

	t.Run("complete configuration becomes ready", func(t *testing.T) {
		env := auditProductionEnv(t)
		auditRequireRuntimeLogin(t, env["RETICORA_DATABASE_URL"])
		run := auditStartServer(t, env, true, 90*time.Second)
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		t.Logf("/readyz -> %d %s", run.readyCode, run.readyBody)
		if run.readyCode != http.StatusOK {
			t.Errorf("/readyz = %d %s, want 200", run.readyCode, run.readyBody)
		}
		for _, check := range []string{`"database":"ok"`, `"redis":"ok"`} {
			if !strings.Contains(run.readyBody, check) {
				t.Errorf("/readyz body %s lacks %s", run.readyBody, check)
			}
		}
	})

	t.Run("object storage endpoint host:port is probed", func(t *testing.T) {
		env := auditProductionEnv(t)
		s3 := auditFakeS3(t)
		env["RETICORA_S3_ENDPOINT"] = strings.TrimPrefix(s3.URL, "http://")
		env["RETICORA_S3_ACCESS_KEY"] = "audit"
		env["RETICORA_S3_SECRET_KEY"] = "audit-secret"
		run := auditStartServer(t, env, true, 90*time.Second)
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		t.Logf("RETICORA_S3_ENDPOINT=%s: /readyz -> %d %s", env["RETICORA_S3_ENDPOINT"], run.readyCode, run.readyBody)
		if run.readyCode != http.StatusOK || !strings.Contains(run.readyBody, `"object_storage":"ok"`) {
			t.Errorf("/readyz = %d %s, want 200 with object_storage ok", run.readyCode, run.readyBody)
		}
	})
}
