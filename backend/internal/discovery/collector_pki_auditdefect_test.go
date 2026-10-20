//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests of the
// collector PKI and the edge on PostgreSQL (must fail until the defects are
// fixed).
//
// WP-145 (COL-04), WP-146 (COL-08).
//
// Findings encoded here:
//   - TestAuditCARolloverKeepsEnrolledCollectorTrust – WP-145 P2 "after a
//     roll-over enrolled collectors no longer trust the edge server
//     certificate". COL-04: roll-over with two trust anchors and no outage.
//   - TestAuditConcurrentEdgePKIExportConsistent – WP-146 P2 "Concurrent
//     edge-PKI export by several server replicas leaves a mismatched or
//     missing server key/certificate" (k8s prod overlay: 3 replicas, shared
//     PVC).
//
// The shared helpers (auditEdge, auditEnroll, auditFreePort) are in
// edge_helpers_integration_test.go without a build tag (WP-374).
// TestAuditCertificateRenewalBoundToCollector (F-012) is fixed and lives in
// renew_edge_integration_test.go (WP-375), TestAuditBundleCredentialSelection
// (F-027) in bundle_selection_integration_test.go (WP-376),
// TestAuditDisabledCollectorConnectionCut (F-025) in
// natsedge_integration_test.go (WP-381).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 \
//	  -run 'TestAuditCARolloverKeepsEnrolledCollectorTrust|TestAuditConcurrentEdgePKIExportConsistent' ./internal/discovery/
package discovery_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

func TestAuditCARolloverKeepsEnrolledCollectorTrust(t *testing.T) {
	e, enc := auditEdge(t, "d1")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, _, enrolled := auditEnroll(t, e, "audit-roll-old")
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	verifyServer := func(bundlePEM string) error {
		raw, rerr := os.ReadFile(filepath.Join(dir, "server.pem"))
		if rerr != nil {
			return rerr
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(bundlePEM))
		_, verr := parseCert(t, string(raw)).Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost",
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return verr
	}
	if verr := verifyServer(enrolled.CABundlePEM); verr != nil {
		t.Fatalf("control before roll-over: %v", verr)
	}
	if err = e.ca.Rollover(bg); err != nil {
		t.Fatal(err)
	}
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _, fresh := auditEnroll(t, e, "audit-roll-new")
	if verr := verifyServer(fresh.CABundlePEM); verr != nil {
		t.Errorf("a collector enrolled after the roll-over does not trust the edge: %v", verr)
	}
	if verr := verifyServer(enrolled.CABundlePEM); verr != nil {
		t.Errorf("COL-04 violated: after the CA roll-over a collector enrolled before it no longer trusts the edge server certificate (outage at the next nats-edge restart): %v", verr)
	}
}

func TestAuditConcurrentEdgePKIExportConsistent(t *testing.T) {
	e, enc := auditEdge(t, "d2")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	// Two server replicas sharing the database and the PKI volume.
	replicaA, replicaB := e.ca, discovery.NewCA(e.f.App, enc)
	if _, err = replicaA.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = replicaB.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	const runs = 60
	var mismatched, errored int
	var firstMismatch, firstError string
	var widestMode os.FileMode
	for i := range runs {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("pki-%d", i))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, ca := range []*discovery.CA{replicaA, replicaB} {
			wg.Go(func() {
				errs[j] = discovery.ExportEdgePKI(bg, ca, secrets, dir, []string{"localhost", "127.0.0.1"}, time.Now())
			})
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			errored++
			if firstError == "" {
				firstError = fmt.Sprint(errs)
			}
		}
		if _, lerr := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")); lerr != nil {
			mismatched++
			if firstMismatch == "" {
				firstMismatch = lerr.Error()
			}
		}
		if st, serr := os.Stat(filepath.Join(dir, "server-key.pem")); serr == nil && st.Mode().Perm() > widestMode {
			widestMode = st.Mode().Perm()
		}
	}
	if mismatched > 0 {
		t.Errorf("%d of %d concurrent two-replica exports left a server key/certificate pair nats-edge cannot load (first: %s)", mismatched, runs, firstMismatch)
	}
	if errored > 0 {
		t.Errorf("%d of %d concurrent two-replica exports failed (first: %s)", errored, runs, firstError)
	}
	if widestMode&0o007 != 0 {
		t.Errorf("server-key.pem is readable by others: mode %o (want at most 0640)", widestMode)
	}
}
