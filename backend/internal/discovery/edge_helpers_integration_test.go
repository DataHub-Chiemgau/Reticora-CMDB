// Shared helpers of the collector edge tests on PostgreSQL (WP-374):
// TestCollectorNATSEndpointAuthorization and the edge tests moved out of
// collector_pki_auditdefect_test.go use them without a build tag, the defect
// tests still in that file with the tag auditdefect.
package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// auditEdge seeds the fixture and a collector CA (a private CA per test).
func auditEdge(t *testing.T, tag string) (*edgeFixture, *crypto.EnvelopeEncryptor) {
	t.Helper()
	f := scopetest.Seed(t, tag)
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	return &edgeFixture{f: f, ca: discovery.NewCA(f.App, enc), repo: discovery.NewPGRepository(f.App)}, enc
}

// auditEnroll enrolls a collector of organization A and returns its id, key
// and issued certificate (as the collector stores them).
func auditEnroll(t *testing.T, e *edgeFixture, name string) (string, *ecdsa.PrivateKey, *discovery.IssuedCert) {
	t.Helper()
	return auditEnrollIn(t, e, e.f.OrgA, name)
}

// auditEnrollIn is auditEnroll for a collector of the given organization.
func auditEnrollIn(t *testing.T, e *edgeFixture, orgID, name string) (string, *ecdsa.PrivateKey, *discovery.IssuedCert) {
	t.Helper()
	e.codes++
	raw := fmt.Sprintf("scopetest-audit-%s-%d", name, e.codes)
	code := &discovery.EnrollmentCode{OrganizationID: orgID, Label: raw, ExpiresAt: time.Now().Add(time.Hour)}
	code.SetRawCode(raw)
	if err := e.repo.CreateEnrollmentCode(e.f.OrgCtx(orgID), code); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &discovery.Collector{Name: name}
	issued, err := e.repo.EnrollCollector(context.Background(), raw, c, func(ctx context.Context, org, id string) (*discovery.IssuedCert, error) {
		return e.ca.Sign(ctx, csrFor(t, "", key), org, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID, key, issued
}

func auditFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// edgeNATSServerBin returns the nats-server for the end-to-end edge tests
// (TEST_NATS_SERVER_BIN). Without it the test is skipped; in the CI (CI=true)
// it fails, so the edge tests cannot pass there without a real server
// (COL-08, PRI-07).
func edgeNATSServerBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TEST_NATS_SERVER_BIN")
	if bin != "" {
		return bin
	}
	if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
		t.Fatal("TEST_NATS_SERVER_BIN not set in the CI: the edge tests run deploy/docker-compose/nats-edge.conf with a real nats-server")
	}
	t.Skip("TEST_NATS_SERVER_BIN not set; skipping the end-to-end run of nats-edge.conf")
	return ""
}
