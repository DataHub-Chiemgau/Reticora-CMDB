//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test of the
// collector's certificate life cycle (must fail until the defect is
// fixed).
//
// WP-145 (COL-04).
//
// Finding encoded here:
//   - TestAuditCollectorEnrollsFromEnvironment – P2 "Bootstrap via
//     RETICORA_ENROLL_TOKEN / RETICORA_CLOUD_URL not implemented": a first
//     start with these variables and no keystore must perform the CSR
//     enrollment and persist the identity (0600).
//
// The test starts the real collector entry point Main() in a child process
// (TestAuditHelperCollectorMain, the helper-process pattern, in
// lifecycle_helpers_test.go) and observes it from outside, so it does not
// depend on where a fix wires the logic in; the observation window is 15 s.
// The renewal test (F-026) is in lifecycle_test.go since WP-414.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditCollectorEnrollsFromEnvironment' ./collectorcmd/
package collectorcmd

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	_, stop := auditStartCollector(t, map[string]string{
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
