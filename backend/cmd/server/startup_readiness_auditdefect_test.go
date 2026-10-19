//go:build auditdefect

package main

// Phase-1 acceptance audit, group T5-install-ops-gate: DEFECT tests (fail
// until the product is fixed). Helpers: startup_failclosed_integration_test.go.
//
// WP-097 (OPS-01) P2 "/readyz does not check SMTP although SMTP is
// configured": main.go registers readiness checks only for redis, database,
// object_storage and nats; with RETICORA_SMTP_HOST set an unreachable SMTP
// server leaves the instance "ready". Test gap high "No SMTP readiness test".
//
// WP-097 (OPS-01) P2 "object_storage readiness dials the raw
// RETICORA_S3_ENDPOINT, so URL-form or portless endpoints accepted by the S3
// client keep /readyz at 503 forever": main.go passes cfg.S3Endpoint
// unchanged to server.TCPReadiness ("dial tcp: address http://h:p: too many
// colons in address"), while blob.NewS3Store accepts http(s)://host[:port].
//
// Requirement (OPS-01, WP-097 AC1): /readyz reports every configured
// dependency of the instance and answers 200 exactly when they are usable.
// The real server binary is started with a production configuration
// (test PostgreSQL and Redis, fake S3, local SMTP listener).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_08?sslmode=disable \
//     TEST_REDIS_URL=redis://localhost:56379/0 \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT5ReadinessCoversSMTP|TestAuditT5ReadinessObjectStorageURLEndpoint' ./cmd/server/

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// auditSMTPListener is a minimal SMTP greeter on 127.0.0.1.
func auditSMTPListener(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				_, _ = c.Write([]byte("220 audit-t5 ESMTP ready\r\n"))
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "QUIT"):
						_, _ = c.Write([]byte("221 bye\r\n"))
						return
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						_, _ = c.Write([]byte("250 audit-t5\r\n"))
					default:
						_, _ = c.Write([]byte("250 ok\r\n"))
					}
				}
			}(c)
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func auditReadyChecks(t *testing.T, body string) map[string]string {
	t.Helper()
	var resp struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("/readyz body is not JSON: %s", body)
	}
	return resp.Checks
}

func TestAuditT5ReadinessCoversSMTP(t *testing.T) {
	smtpEnv := func(env map[string]string, host string, port int) {
		env["RETICORA_SMTP_HOST"] = host
		env["RETICORA_SMTP_PORT"] = strconv.Itoa(port)
		env["RETICORA_SMTP_TLS"] = "none"
		env["RETICORA_SMTP_FROM"] = "Reticora <noreply@audit.example>"
	}

	t.Run("reachable SMTP is reported ok", func(t *testing.T) {
		env := auditProductionEnv(t)
		host, port := auditSMTPListener(t)
		smtpEnv(env, host, port)
		run := auditStartServer(t, env, true, 90*time.Second)
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		t.Logf("SMTP %s:%d listening: /readyz -> %d %s", host, port, run.readyCode, run.readyBody)
		checks := auditReadyChecks(t, run.readyBody)
		if got, ok := checks["smtp"]; !ok || got != "ok" {
			t.Errorf("/readyz checks.smtp = %q (present %v), want \"ok\" with RETICORA_SMTP_HOST configured; checks: %v", got, ok, checks)
		}
		if run.readyCode != http.StatusOK {
			t.Errorf("/readyz = %d, want 200", run.readyCode)
		}
	})

	t.Run("unreachable SMTP makes the instance not ready", func(t *testing.T) {
		env := auditProductionEnv(t)
		closed := auditClosedAddress(t)
		port, _ := strconv.Atoi(closed[strings.LastIndex(closed, ":")+1:])
		smtpEnv(env, "127.0.0.1", port)
		run := auditStartServer(t, env, true, 90*time.Second)
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		t.Logf("SMTP %s closed: /readyz -> %d %s", closed, run.readyCode, run.readyBody)
		if run.readyCode != http.StatusServiceUnavailable {
			t.Errorf("/readyz = %d, want 503 while the configured SMTP server %s is unreachable", run.readyCode, closed)
		}
		if got := auditReadyChecks(t, run.readyBody)["smtp"]; got != "unavailable" {
			t.Errorf("/readyz checks.smtp = %q, want \"unavailable\"", got)
		}
	})
}

func TestAuditT5ReadinessObjectStorageURLEndpoint(t *testing.T) {
	s3 := auditFakeS3(t)
	hostPort := strings.TrimPrefix(s3.URL, "http://")
	for _, tc := range []struct {
		name     string
		endpoint string
	}{
		{"host:port (control)", hostPort},
		{"URL form http://host:port", "http://" + hostPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := auditProductionEnv(t)
			env["RETICORA_S3_ENDPOINT"] = tc.endpoint
			env["RETICORA_S3_ACCESS_KEY"] = "audit"
			env["RETICORA_S3_SECRET_KEY"] = "audit-secret"
			run := auditStartServer(t, env, true, 90*time.Second)
			if run.readyError != nil {
				t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
			}
			t.Logf("RETICORA_S3_ENDPOINT=%s: /readyz -> %d %s", tc.endpoint, run.readyCode, run.readyBody)
			if run.readyCode != http.StatusOK {
				t.Errorf("/readyz = %d, want 200: the S3 store accepted endpoint %q and is usable", run.readyCode, tc.endpoint)
			}
			if got := auditReadyChecks(t, run.readyBody)["object_storage"]; got != "ok" {
				t.Errorf("/readyz checks.object_storage = %q, want \"ok\" for endpoint %q", got, tc.endpoint)
			}
		})
	}
}
