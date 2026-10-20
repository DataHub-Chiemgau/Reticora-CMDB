//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests of the
// collector's certificate life cycle (must fail until the defects are
// fixed).
//
// WP-145 (COL-04).
//
// Findings encoded here:
//   - TestAuditCollectorRenewsCertificateAt60Percent – P1 "No automatic
//     certificate renewal; collectors lose connectivity 90 days after
//     enrollment": edgecore/enrollment.Renew exists but nothing in the
//     collector calls it, so a certificate at 55 of 90 days (past the 60 %
//     renewal point) is never renewed. COL-04: automatic renewal from 60 %
//     of the lifetime over the existing channel.
//   - TestAuditCollectorEnrollsFromEnvironment – P2 "Bootstrap via
//     RETICORA_ENROLL_TOKEN / RETICORA_CLOUD_URL not implemented": a first
//     start with these variables and no keystore must perform the CSR
//     enrollment and persist the identity (0600).
//
// Both tests start the real collector entry point Main() in a child process
// (TestAuditHelperCollectorMain, the helper-process pattern) and observe it
// from outside, so they do not depend on where a fix wires the logic in.
// The renewal check is expected at start-up (and then periodically); the
// observation window is 15 s. A renewal is recognised by a certificate
// signing request (PEM "CERTIFICATE REQUEST") that the collector sends over
// HTTP to the server URL or over its NATS channel.
//
// The renewal test runs the collector's mTLS NATS channel against a real
// nats-server (TEST_NATS_SERVER_BIN, v2.10+ for handshake_first). The
// shared PKI helpers are in natsserver_helpers_test.go.
//
// Retest:
//
//	cd collector && TEST_NATS_SERVER_BIN=<path to nats-server> go test -tags auditdefect -count=1 -v \
//	  -run 'TestAuditCollectorRenewsCertificateAt60Percent|TestAuditCollectorEnrollsFromEnvironment' ./collectorcmd/
package collectorcmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

const auditMainEnv = "AUDIT_COLLECTOR_MAIN"

// TestAuditHelperCollectorMain is not a test of its own: in the child
// process started by the tests below it runs the collector's Main().
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
// RETICORA_-free environment) until stop is called; it returns the
// collector's output.
func auditStartCollector(t *testing.T, env map[string]string) (stop func() string) {
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
	var once sync.Once
	stopFn := func() string {
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
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	t.Cleanup(func() { stopFn() })
	return stopFn
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

func TestAuditCollectorRenewsCertificateAt60Percent(t *testing.T) {
	bin := os.Getenv("TEST_NATS_SERVER_BIN")
	if bin == "" {
		t.Fatal("TEST_NATS_SERVER_BIN is required: the collector's mTLS NATS channel runs against a real nats-server")
	}
	dir := t.TempDir()
	ca := newAuditPKI(t)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	write("ca.pem", ca.pem)
	write("server.pem", ca.issue(t, &serverKey.PublicKey, pkix.Name{CommonName: "127.0.0.1"}, now.Add(-time.Hour), now.Add(24*time.Hour), true))
	write("server-key.pem", auditKeyPEM(t, serverKey))
	port := auditFreeTCPPort(t)
	write("nats.conf", fmt.Sprintf(`listen: "127.0.0.1:%d"
tls {
  cert_file: "server.pem"
  key_file: "server-key.pem"
  ca_file: "ca.pem"
  verify: true
  handshake_first: true
  timeout: 5
}
`, port))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "-c", "nats.conf")
	srv.Dir = dir
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = srv.Wait() }()

	// The enrolled collector: certificate issued 55 days ago for 90 days
	// (61 % of its lifetime).
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000b1", "6f0b6f0e-0000-4000-8000-0000000000b2"
	colKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notBefore := now.Add(-55 * 24 * time.Hour)
	colCert := ca.issue(t, &colKey.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, notBefore, notBefore.Add(90*24*time.Hour), false)
	credPath := filepath.Join(dir, "credentials.json")
	if err := (&keystore.FileStore{Path: credPath}).Save(context.Background(), keystore.Credentials{
		DeviceID: colID, ClientCertificatePEM: colCert, ClientPrivateKeyPEM: auditKeyPEM(t, colKey), CertificateAuthority: ca.pem,
	}); err != nil {
		t.Fatal(err)
	}

	sink := &auditCSRSink{}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sink.check("HTTP "+r.Method+" "+r.URL.Path, body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer httpSrv.Close()

	// A monitor client of the same CA sees every message on the server.
	monKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	monPair, err := tls.X509KeyPair([]byte(ca.issue(t, &monKey.PublicKey, pkix.Name{CommonName: "audit-monitor"}, now.Add(-time.Hour), now.Add(time.Hour), false)),
		[]byte(auditKeyPEM(t, monKey)))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	url := fmt.Sprintf("tls://127.0.0.1:%d", port)
	var mon *nats.Conn
	for range 50 {
		mon, err = nats.Connect(url, nats.Secure(&tls.Config{RootCAs: roots, Certificates: []tls.Certificate{monPair}, MinVersion: tls.VersionTLS12}),
			nats.TLSHandshakeFirst(), nats.Timeout(2*time.Second))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("nats-server with TLS did not come up: %v", err)
	}
	defer mon.Close()
	var subjects sync.Map
	if _, err = mon.Subscribe(">", func(m *nats.Msg) {
		subjects.Store(m.Subject, true)
		sink.check("NATS "+m.Subject, m.Data)
	}); err != nil {
		t.Fatal(err)
	}
	if err = mon.Flush(); err != nil {
		t.Fatal(err)
	}

	stop := auditStartCollector(t, map[string]string{
		"RETICORA_SERVER_URL":          httpSrv.URL,
		"RETICORA_NATS_URL":            url,
		"RETICORA_CREDENTIALS_PATH":    credPath,
		"RETICORA_SPOOL_DIR":           filepath.Join(dir, "spool"),
		"RETICORA_AGENT_RELAY_ADDR":    "127.0.0.1:0",
		"RETICORA_DISCOVERY_PROTOCOLS": "sweep",
		"RETICORA_ENVIRONMENT":         "development",
	})
	collectorSubjectSeen := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && len(sink.all()) == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	subjects.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), "collector."+orgID+"."+colID+".") {
			collectorSubjectSeen = true
		}
		return true
	})
	output := stop()
	if !collectorSubjectSeen {
		t.Fatalf("control: the collector did not use its NATS channel (no message on collector.%s.%s.*); output:\n%s", orgID, colID, auditTail(output, 15))
	}
	if len(sink.all()) == 0 {
		t.Errorf("COL-04 violated: the certificate is at 61 %% of its 90-day lifetime (renewal due from 60 %%), but within 15 s the collector sent no "+
			"certificate signing request over HTTP or NATS; at day 90 it loses its connection. Collector log tail:\n%s", auditTail(output, 8))
	}
}

func TestAuditCollectorEnrollsFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	ca := newAuditPKI(t)
	const token = "audit-enroll-token-7f3c"
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000b3", "6f0b6f0e-0000-4000-8000-0000000000b4"
	var mu sync.Mutex
	var enrolls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(token)) && !strings.Contains(r.Header.Get("Authorization"), token) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		mu.Lock()
		enrolls = append(enrolls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		// Answer like the server's enrollment endpoint: sign the CSR.
		var req struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(body, &req)
		block, _ := pem.Decode([]byte(req.CSR))
		if block == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cert := ca.issue(t, csr.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour), false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": colID, "certificate": map[string]any{
			"certificate_pem": cert, "ca_bundle_pem": ca.pem, "not_after": time.Now().Add(90 * 24 * time.Hour)}})
	}))
	defer srv.Close()

	credPath := filepath.Join(dir, "credentials.json")
	stop := auditStartCollector(t, map[string]string{
		"RETICORA_ENROLL_TOKEN":        token,
		"RETICORA_CLOUD_URL":           srv.URL,
		"RETICORA_SERVER_URL":          srv.URL,
		"RETICORA_NATS_URL":            "tls://127.0.0.1:1",
		"RETICORA_CREDENTIALS_PATH":    credPath,
		"RETICORA_SPOOL_DIR":           filepath.Join(dir, "spool"),
		"RETICORA_AGENT_RELAY_ADDR":    "127.0.0.1:0",
		"RETICORA_DISCOVERY_PROTOCOLS": "sweep",
		"RETICORA_ENVIRONMENT":         "development",
	})
	deadline := time.Now().Add(15 * time.Second)
	stored := func() (os.FileInfo, error) { return os.Stat(credPath) }
	for time.Now().Before(deadline) {
		if _, err := stored(); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	output := stop()
	mu.Lock()
	got := append([]string(nil), enrolls...)
	mu.Unlock()
	st, err := stored()
	switch {
	case len(got) == 0:
		t.Errorf("COL-04 violated: started with RETICORA_ENROLL_TOKEN and RETICORA_CLOUD_URL and without a keystore, the collector sent no "+
			"enrollment request carrying the token within 15 s (keystore %s: %v). Collector log tail:\n%s", credPath, err, auditTail(output, 8))
	case err != nil:
		t.Errorf("COL-04 violated: enrollment requests %v were answered, but no identity was stored at %s: %v", got, credPath, err)
	case st.Mode().Perm() != 0o600:
		t.Errorf("the enrolled identity %s has mode %o, want 0600", credPath, st.Mode().Perm())
	}
}
