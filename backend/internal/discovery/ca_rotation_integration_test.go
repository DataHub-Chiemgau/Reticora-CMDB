package discovery_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// storedCA is a row of collector_ca as stored.
type storedCA struct {
	id, state, certPEM string
	keyCT, keyDEK      []byte
}

func storedCAs(t *testing.T, f *scopetest.Fixture) []storedCA {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT id::text, state, cert_pem, key_ciphertext, key_dek
		FROM collector_ca ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedCA
	for rows.Next() {
		var c storedCA
		if err = rows.Scan(&c.id, &c.state, &c.certPEM, &c.keyCT, &c.keyDEK); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// openCAKey decrypts the key of a stored CA with enc and checks that it is
// the key of the CA certificate.
func openCAKey(t *testing.T, c storedCA, enc *crypto.EnvelopeEncryptor) error {
	t.Helper()
	dek, err := enc.UnwrapDEK(c.keyDEK)
	if err != nil {
		return err
	}
	der, err := crypto.Decrypt(dek, c.keyCT)
	if err != nil {
		return err
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return err
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || !ecKey.PublicKey.Equal(parseCert(t, c.certPEM).PublicKey) {
		return errors.New("the decrypted key is not the key of the CA certificate")
	}
	return nil
}

// TestCollectorCARewrap covers the rewrap of the collector CA keys (COL-04)
// by the master-key rotation (SEC-06, WP-141, WP-547) against PostgreSQL:
// with an active and a previous CA (after a roll-over) and the new master
// key current, the old one previous, the run rotation.master_key wraps the
// key of every trusted CA with the new key; certificate and key ciphertext
// stay the same, only the wrap changes, and a second run changes nothing.
// Afterwards the old key is no longer needed: a CA instance with the new
// key alone (the restarted server) decrypts both keys, keeps the same trust
// bundle, signs collector certificates that verify against it next to
// the ones issued before the rotation, and issues the edge server
// certificate from the oldest CA. The old key alone opens no CA key.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCollectorCARewrap' ./internal/discovery/
func TestCollectorCARewrap(t *testing.T) {
	f := scopetest.Seed(t, "a3")
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	cleanDerivationRoot(t, f)
	oldKey, _ := crypto.GenerateMasterKey()
	newKey, _ := crypto.GenerateMasterKey()
	oldEnc, err := crypto.NewEnvelopeEncryptor(oldKey)
	if err != nil {
		t.Fatal(err)
	}

	// Before the rotation: a certificate of the first CA, a roll-over and a
	// certificate of the new active CA.
	const collectorID = "a3a3a3a3-0000-4000-8000-000000000547"
	collectorKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := discovery.NewCA(f.App, oldEnc)
	first, err := ca.Sign(bg, csrFor(t, collectorID, collectorKey), f.OrgA, collectorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ca.Rollover(bg, discovery.CAChangeOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := ca.Sign(bg, csrFor(t, collectorID, collectorKey), f.OrgA, collectorID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ca.TrustBundle(bg)
	if err != nil || strings.Count(bundle, "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("trust bundle before the rotation: %d anchors (%v)", strings.Count(bundle, "BEGIN CERTIFICATE"), err)
	}
	before := storedCAs(t, f)
	if len(before) != 2 || before[0].state != "previous" || before[1].state != "active" {
		t.Fatalf("collector_ca before the rotation: %+v", before)
	}
	for _, c := range before {
		if !oldEnc.IsCurrent(c.keyDEK) {
			t.Fatalf("CA %s is not wrapped with the old key", c.id)
		}
	}

	// The rotation: the new key current, the old one previous; the rewrap run.
	rotating, err := crypto.NewEnvelopeEncryptor(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	rotation := jobs.NewRotation(f.App, rotating, rotationDEKs{repo: credential.NewPGRepository(f.App)}, nil)
	n, err := rotation.RewrapMasterKey(bg)
	if err != nil || n < len(before) {
		t.Fatalf("rewrap run: %d rewrapped (want at least the %d CA keys), %v", n, len(before), err)
	}
	newOnly, _ := crypto.NewEnvelopeEncryptor(newKey)
	oldOnly, _ := crypto.NewEnvelopeEncryptor(oldKey)
	after := storedCAs(t, f)
	if len(after) != len(before) {
		t.Fatalf("collector_ca after the rewrap: %d rows, want %d", len(after), len(before))
	}
	for i, c := range after {
		b := before[i]
		if c.id != b.id || c.state != b.state || c.certPEM != b.certPEM || !bytes.Equal(c.keyCT, b.keyCT) {
			t.Errorf("CA %s: the rewrap changed more than the wrapped data key", b.id)
		}
		if bytes.Equal(c.keyDEK, b.keyDEK) || !newOnly.IsCurrent(c.keyDEK) {
			t.Errorf("CA %s (%s): the data key is not wrapped with the new master key", c.id, c.state)
		}
		if oerr := openCAKey(t, c, newOnly); oerr != nil {
			t.Errorf("CA %s (%s): the new master key alone does not open the CA key: %v", c.id, c.state, oerr)
		}
		if oerr := openCAKey(t, c, oldOnly); oerr == nil {
			t.Errorf("CA %s (%s): the old master key still opens the CA key", c.id, c.state)
		}
	}
	if _, err = rotation.RewrapMasterKey(bg); err != nil {
		t.Fatalf("second rewrap run: %v", err)
	}
	for i, c := range storedCAs(t, f) {
		if !bytes.Equal(c.keyDEK, after[i].keyDEK) {
			t.Errorf("CA %s: the second run rewrapped it again", c.id)
		}
	}

	// The restarted server with the new key alone.
	restarted := discovery.NewCA(f.App, newOnly)
	got, err := restarted.TrustBundle(bg)
	if err != nil || got != bundle {
		t.Fatalf("trust bundle with the new key alone: changed %t (%v)", got != bundle, err)
	}
	fresh, err := restarted.Sign(bg, csrFor(t, collectorID, collectorKey), f.OrgA, collectorID)
	if err != nil {
		t.Fatalf("signing with the new key alone: %v", err)
	}
	freshCert := parseCert(t, fresh.CertificatePEM)
	if !verifies(freshCert, bundle) || fresh.CABundlePEM != bundle ||
		!bytes.Equal(freshCert.AuthorityKeyId, parseCert(t, second.CertificatePEM).AuthorityKeyId) {
		t.Error("the certificate signed after the rotation does not chain to the active CA of the trust bundle")
	}
	for name, issued := range map[string]*discovery.IssuedCert{"first CA": first, "active CA": second} {
		if !verifies(parseCert(t, issued.CertificatePEM), got) {
			t.Errorf("the certificate of the %s issued before the rotation is no longer trusted", name)
		}
	}
	serverPEM, _, err := restarted.IssueServer(bg, []string{"nats-edge"}, time.Hour)
	if err != nil {
		t.Fatalf("edge server certificate with the new key alone: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(bundle))
	serverCert := parseCert(t, serverPEM)
	if _, verr := serverCert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "nats-edge",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); verr != nil {
		t.Errorf("edge server certificate: %v", verr)
	}
	if !bytes.Equal(serverCert.AuthorityKeyId, parseCert(t, first.CertificatePEM).AuthorityKeyId) {
		t.Error("the edge server certificate is not issued by the oldest trusted CA")
	}
	// The old key alone opens no CA key any more.
	if _, serr := discovery.NewCA(f.App, oldOnly).Sign(bg, csrFor(t, collectorID, collectorKey), f.OrgA, collectorID); serr == nil {
		t.Error("a CA with the retired master key alone still signs")
	}
}
