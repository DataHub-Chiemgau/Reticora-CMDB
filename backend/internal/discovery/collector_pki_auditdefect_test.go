//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests of the
// collector PKI and the edge on PostgreSQL (must fail until the defects are
// fixed).
//
// WP-145 (COL-04), WP-146 (COL-08).
//
// Finding encoded here:
//   - TestAuditCARolloverKeepsEnrolledCollectorTrust – WP-145 P2 "after a
//     roll-over enrolled collectors no longer trust the edge server
//     certificate". COL-04: roll-over with two trust anchors and no outage.
//
// The shared helpers (auditEdge, auditEnroll, auditFreePort) are in
// edge_helpers_integration_test.go without a build tag (WP-374).
// TestAuditCertificateRenewalBoundToCollector (F-012) is fixed and lives in
// renew_edge_integration_test.go (WP-375), TestAuditBundleCredentialSelection
// (F-027) in bundle_selection_integration_test.go (WP-376),
// TestAuditDisabledCollectorConnectionCut (F-025) in
// natsedge_integration_test.go (WP-381),
// TestAuditConcurrentEdgePKIExportConsistent (F-104) in
// natsedge_integration_test.go (WP-429).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 \
//	  -run 'TestAuditCARolloverKeepsEnrolledCollectorTrust' ./internal/discovery/
package discovery_test

import (
	"context"
	"crypto/x509"
	"os"
	"path/filepath"
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
