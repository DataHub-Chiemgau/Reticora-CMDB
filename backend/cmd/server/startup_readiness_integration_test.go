package main

// Phase-1 acceptance audit, group T5-install-ops-gate: former DEFECT tests,
// regression tests since WP-442 (F-126, F-127). Helpers:
// startup_failclosed_integration_test.go.
//
// WP-097 (OPS-01) P2 "/readyz does not check SMTP although SMTP is
// configured": main.go registered readiness checks only for redis, database,
// object_storage and nats; with RETICORA_SMTP_HOST set an unreachable SMTP
// server left the instance "ready". Test gap high "No SMTP readiness test".
//
// WP-097 (OPS-01) P2 "object_storage readiness dials the raw
// RETICORA_S3_ENDPOINT, so URL-form or portless endpoints accepted by the S3
// client keep /readyz at 503 forever": main.go passed cfg.S3Endpoint
// unchanged to server.TCPReadiness ("dial tcp: address http://h:p: too many
// colons in address"), while blob.NewS3Store accepts http(s)://host[:port].
//
// Requirement (OPS-01, WP-097 AC1): /readyz reports every configured
// dependency of the instance and answers 200 exactly when they are usable;
// a check that turns unavailable is logged once with its name and address,
// without credentials (WP-442). The real server binary is started with a
// production configuration (test PostgreSQL and Redis, fake S3, local SMTP
// listener on 127.0.0.1).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_08?sslmode=disable \
//     TEST_REDIS_URL=redis://localhost:56379/0 \
//     go test -count=1 -v -run 'TestAuditT5ReadinessCoversSMTP|TestAuditT5ReadinessObjectStorageURLEndpoint' ./cmd/server/

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

// auditSMTPListener is a minimal SMTP greeter on 127.0.0.1. Closing the
// returned listener stops the server for new connections.
func auditSMTPListener(t *testing.T) (l net.Listener, host string, port int) {
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
	return l, "127.0.0.1", addr.Port
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

// auditReadinessFailures returns the log lines "readiness check failed" of
// check in the JSON log output of the server.
func auditReadinessFailures(output, check string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, `"msg":"readiness check failed"`) && strings.Contains(line, `"check":"`+check+`"`) {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestAuditT5ReadinessCoversSMTP(t *testing.T) {
	// Credentials of the SMTP server; the readiness check does not use
	// them, and no log line may carry them.
	const smtpUser, smtpPassword = "readyz-smtp-user", "readyz-smtp-password-7f3c"
	smtpEnv := func(env map[string]string, host string, port int) {
		env["RETICORA_SMTP_HOST"] = host
		env["RETICORA_SMTP_PORT"] = strconv.Itoa(port)
		env["RETICORA_SMTP_TLS"] = "none"
		env["RETICORA_SMTP_FROM"] = "Reticora <noreply@audit.example>"
		env["RETICORA_SMTP_USERNAME"] = smtpUser
		env["RETICORA_SMTP_PASSWORD"] = smtpPassword
	}
	noCredentials := func(t *testing.T, output string) {
		t.Helper()
		for _, secret := range []string{smtpUser, smtpPassword} {
			if strings.Contains(output, secret) {
				t.Errorf("server log contains the SMTP credential %q", secret)
			}
		}
	}

	t.Run("reachable SMTP is reported ok, an outage makes the instance not ready", func(t *testing.T) {
		env := auditProductionEnv(t)
		listener, host, port := auditSMTPListener(t)
		smtpEnv(env, host, port)
		address := net.JoinHostPort(host, strconv.Itoa(port))
		var stoppedCodes []int
		var stoppedChecks []map[string]string
		run := auditStartServerThen(t, env, 90*time.Second, func(readyz func() (int, string)) {
			// The mail server goes down while the instance runs. The result
			// of a probe answers the probes of the next second
			// (server.smtpReadinessReuse), so every probe waits past it; the
			// second one must not log the unchanged failure again.
			_ = listener.Close()
			for range 2 {
				time.Sleep(1500 * time.Millisecond)
				code, body := readyz()
				t.Logf("SMTP %s stopped: /readyz -> %d %s", address, code, body)
				stoppedCodes = append(stoppedCodes, code)
				stoppedChecks = append(stoppedChecks, auditReadyChecks(t, body))
			}
		})
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		t.Logf("SMTP %s listening: /readyz -> %d %s", address, run.readyCode, run.readyBody)
		checks := auditReadyChecks(t, run.readyBody)
		if got, ok := checks["smtp"]; !ok || got != "ok" {
			t.Errorf("/readyz checks.smtp = %q (present %v), want \"ok\" with RETICORA_SMTP_HOST configured; checks: %v", got, ok, checks)
		}
		if run.readyCode != http.StatusOK {
			t.Errorf("/readyz = %d, want 200", run.readyCode)
		}
		for i, code := range stoppedCodes {
			if code != http.StatusServiceUnavailable || stoppedChecks[i]["smtp"] != "unavailable" {
				t.Errorf("probe %d after the SMTP outage: /readyz = %d %v, want 503 with checks.smtp \"unavailable\"", i+1, code, stoppedChecks[i])
			}
		}
		failures := auditReadinessFailures(run.output, "smtp")
		if len(failures) != 1 || !strings.Contains(failures[0], `"address":"`+address+`"`) {
			t.Errorf("want exactly one 'readiness check failed' line for smtp with address %s, got %d:\n%s",
				address, len(failures), strings.Join(failures, "\n"))
		}
		noCredentials(t, run.output)
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
		failures := auditReadinessFailures(run.output, "smtp")
		if len(failures) != 1 || !strings.Contains(failures[0], `"address":"`+closed+`"`) {
			t.Errorf("want one 'readiness check failed' line for smtp with address %s, got:\n%s", closed, auditTail(run.output, 8))
		}
		noCredentials(t, run.output)
	})

	t.Run("without SMTP configuration there is no smtp check", func(t *testing.T) {
		run := auditStartServer(t, auditProductionEnv(t), true, 90*time.Second)
		if run.readyError != nil {
			t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
		}
		if got, ok := auditReadyChecks(t, run.readyBody)["smtp"]; ok || run.readyCode != http.StatusOK {
			t.Errorf("/readyz = %d with checks.smtp = %q (present %v), want 200 without an smtp key", run.readyCode, got, ok)
		}
	})
}

func TestAuditT5ReadinessObjectStorageURLEndpoint(t *testing.T) {
	// S3 keys; no log line may carry them.
	const accessKey, secretKey = "AKIAREADYZT5TEST", "readyz-s3-secret-9d1e"
	for _, tc := range []struct {
		name     string
		endpoint func(hostPort string) string
	}{
		{"host:port (control)", func(hostPort string) string { return hostPort }},
		{"URL form http://host:port", func(hostPort string) string { return "http://" + hostPort }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s3 := auditFakeS3(t)
			hostPort := strings.TrimPrefix(s3.URL, "http://")
			env := auditProductionEnv(t)
			env["RETICORA_S3_ENDPOINT"] = tc.endpoint(hostPort)
			env["RETICORA_S3_ACCESS_KEY"] = accessKey
			env["RETICORA_S3_SECRET_KEY"] = secretKey
			var stoppedCode int
			var stoppedBody string
			run := auditStartServerThen(t, env, 90*time.Second, func(readyz func() (int, string)) {
				s3.Close()
				stoppedCode, stoppedBody = readyz()
			})
			if run.readyError != nil {
				t.Fatalf("%v (exit %d)\n%s", run.readyError, run.exitCode, auditTail(run.output, 8))
			}
			t.Logf("RETICORA_S3_ENDPOINT=%s: /readyz -> %d %s; S3 stopped: %d %s",
				env["RETICORA_S3_ENDPOINT"], run.readyCode, run.readyBody, stoppedCode, stoppedBody)
			if run.readyCode != http.StatusOK {
				t.Errorf("/readyz = %d, want 200: the S3 store accepted endpoint %q and is usable", run.readyCode, env["RETICORA_S3_ENDPOINT"])
			}
			if got := auditReadyChecks(t, run.readyBody)["object_storage"]; got != "ok" {
				t.Errorf("/readyz checks.object_storage = %q, want \"ok\" for endpoint %q", got, env["RETICORA_S3_ENDPOINT"])
			}
			if stoppedCode != http.StatusServiceUnavailable || auditReadyChecks(t, stoppedBody)["object_storage"] != "unavailable" {
				t.Errorf("S3 stopped: /readyz = %d %s, want 503 with checks.object_storage \"unavailable\"", stoppedCode, stoppedBody)
			}
			failures := auditReadinessFailures(run.output, "object_storage")
			if len(failures) != 1 || !strings.Contains(failures[0], `"address":"`+hostPort+`"`) {
				t.Errorf("want one 'readiness check failed' line for object_storage with address %s, got:\n%s",
					hostPort, strings.Join(failures, "\n"))
			}
			for _, secret := range []string{accessKey, secretKey} {
				if strings.Contains(run.output, secret) {
					t.Errorf("server log contains the S3 key %q", secret)
				}
			}
		})
	}
}
