// Helpers of the collector life-cycle tests (WP-414): they start the real
// collector entry point Main() in a child process
// (TestAuditHelperCollectorMain, the helper-process pattern) and observe it
// from outside; lifecycle_test.go uses them (WP-414, WP-493). The PKI helpers
// (auditPKI, newAuditPKI, auditKeyPEM, auditFreeTCPPort) are in
// natsserver_helpers_test.go.
package collectorcmd

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const auditMainEnv = "AUDIT_COLLECTOR_MAIN"

// TestAuditHelperCollectorMain is not a test of its own: in the child
// process started by auditStartCollector it runs the collector's Main().
func TestAuditHelperCollectorMain(t *testing.T) {
	if os.Getenv(auditMainEnv) != "1" {
		return
	}
	os.Args = os.Args[:1]
	Main()
}

// auditCSRSink records the requests that carry a certificate signing
// request.
type auditCSRSink struct {
	mu   sync.Mutex
	seen []string
}

func (s *auditCSRSink) check(where string, data []byte) {
	if bytes.Contains(data, []byte("CERTIFICATE REQUEST")) {
		s.mu.Lock()
		s.seen = append(s.seen, where)
		s.mu.Unlock()
	}
}

func (s *auditCSRSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// auditStartCollector runs Main() in a child process with env (on top of a
// RETICORA_-free environment) until stop is called; stop returns the
// collector's output, output what it has written so far.
func auditStartCollector(t *testing.T, env map[string]string) (output, stop func() string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuditHelperCollectorMain$", "-test.count=1")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RETICORA_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, auditMainEnv+"=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	var mu sync.Mutex
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) })
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	output = func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	var once sync.Once
	stop = func() string {
		once.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
		return output()
	}
	t.Cleanup(func() { stop() })
	return output, stop
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func auditTail(s string, lines int) string {
	parts := strings.Split(strings.TrimSpace(s), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
