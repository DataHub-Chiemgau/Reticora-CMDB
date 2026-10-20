package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestNeedsRenewal: renewal is due from 60 % of the lifetime (COL-04).
func TestNeedsRenewal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(discovery.CertValidity)
	for _, tc := range []struct {
		days int
		want bool
	}{{0, false}, {53, false}, {54, true}, {89, true}, {120, true}} {
		if got := discovery.NeedsRenewal(start, end, start.Add(time.Duration(tc.days)*24*time.Hour)); got != tc.want {
			t.Errorf("day %d: NeedsRenewal = %v, want %v", tc.days, got, tc.want)
		}
	}
}

func csrFor(t *testing.T, cn string, key any) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func parseCert(t *testing.T, p string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		t.Fatalf("no certificate in %q", p)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func verifies(cert *x509.Certificate, bundle string) bool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(bundle))
	_, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err == nil
}

// TestCollectorPKI covers WP-145 (COL-04) against PostgreSQL: enrollment
// with a CSR issues a 90-day client certificate from the internal CA with
// CN = collector id; the private key never reaches the server; a CSR for
// another collector or with a weak key is refused; renewal over the NATS
// service (CertificateRenewal, signed with the current key; there is no
// HTTP renewal, WP-375) is refused before 60 % of the lifetime and works
// afterwards; disabling the collector revokes its certificates (signed
// revocation list) and refuses renewal; a CA roll-over keeps both trust
// anchors (also in the CA bundle of new certificates) until the previous
// CA is retired; the CA is kept in the database, encrypted under the master
// key.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCollectorPKI(t *testing.T) {
	f := scopetest.Seed(t, "a5")
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	key, _ := crypto.GenerateMasterKey()
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	ca := discovery.NewCA(f.App, enc)
	repo := discovery.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).WithCA(ca).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	jsonBody := func(v map[string]any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	collectorKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	// Enrollment with a CSR.
	code := &discovery.EnrollmentCode{OrganizationID: f.OrgA, Label: "wp145", ExpiresAt: time.Now().Add(time.Hour)}
	code.SetRawCode("scopetest-a5-code")
	if err = repo.CreateEnrollmentCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	w := call(bg, http.MethodPost, "/api/v1/collectors/enroll",
		jsonBody(map[string]any{"code": "scopetest-a5-code", "name": "wp145", "csr": csrFor(t, "", collectorKey)}))
	if w.Code != http.StatusCreated {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("the enrollment response carries a private key")
	}
	var enrolled struct {
		discovery.Collector
		Certificate discovery.IssuedCert `json:"certificate"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &enrolled); err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, enrolled.Certificate.CertificatePEM)
	life := cert.NotAfter.Sub(cert.NotBefore)
	if cert.Subject.CommonName != enrolled.ID || life < discovery.CertValidity || life > discovery.CertValidity+10*time.Minute ||
		len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		!verifies(cert, enrolled.Certificate.CABundlePEM) || enrolled.CertFingerprint != enrolled.Certificate.Fingerprint {
		t.Fatalf("certificate CN %s, life %s, enrolled %+v", cert.Subject.CommonName, life, enrolled.Collector)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&collectorKey.PublicKey) {
		t.Fatal("the certificate is not for the collector's own key")
	}
	renewal := discovery.NewCertificateRenewal(f.App, ca)
	subject := discovery.CollectorSubject(f.OrgA, enrolled.ID) + discovery.CertificateRenewSuffix
	// renew asks the renewal service as the collector at now: a CSR of
	// newKey, the current certificate certPEM, signed with its key.
	renew := func(now time.Time, certPEM string, key, newKey *ecdsa.PrivateKey) discovery.RenewalResponse {
		renewal.WithClock(func() time.Time { return now })
		return renewal.Renew(bg, subject, renewalRequest(t, subject, subjectCSR(t, enrolled.ID, f.OrgA, newKey), certPEM, now, key))
	}
	certificates := func() int {
		var n int
		if cerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM collector_certificate WHERE collector_id = $1`, enrolled.ID).Scan(&n); cerr != nil {
			t.Fatal(cerr)
		}
		return n
	}

	// Refused CSRs: another collector, a weak key.
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	for name, csr := range map[string]string{
		"other collector": csrFor(t, "00000000-0000-4000-8000-000000000001", collectorKey),
		"weak key":        csrFor(t, enrolled.ID, weak),
		"garbage":         "not a csr",
	} {
		if _, serr := ca.Sign(bg, csr, f.OrgA, enrolled.ID); serr == nil {
			t.Errorf("%s: signed", name)
		}
	}

	// Renewal over the NATS service: not before 60 % of the lifetime, at
	// day 55 of 90 a new certificate for the new key.
	renewedKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if resp := renew(time.Now().UTC(), enrolled.Certificate.CertificatePEM, collectorKey, renewedKey); resp.Error != discovery.RenewalNotDue || resp.IssuedCert != nil {
		t.Fatalf("early renewal: %+v", resp)
	}
	resp := renew(time.Now().UTC().Add(55*24*time.Hour), enrolled.Certificate.CertificatePEM, collectorKey, renewedKey)
	if resp.Error != "" || resp.IssuedCert == nil || resp.Serial == enrolled.Certificate.Serial {
		t.Fatalf("renewal: %+v", resp)
	}
	renewed := *resp.IssuedCert
	if rc := parseCert(t, renewed.CertificatePEM); rc.Subject.CommonName != enrolled.ID || !verifies(rc, renewed.CABundlePEM) ||
		!rc.PublicKey.(*ecdsa.PublicKey).Equal(&renewedKey.PublicKey) {
		t.Fatalf("renewed certificate CN %s", rc.Subject.CommonName)
	}

	// CA roll-over: both anchors, then only the new one.
	if err = ca.Rollover(bg); err != nil {
		t.Fatal(err)
	}
	bundle, err := ca.TrustBundle(bg)
	if err != nil || strings.Count(bundle, "BEGIN CERTIFICATE") != 2 || !verifies(cert, bundle) {
		t.Fatalf("bundle after roll-over: %d anchors, old certificate trusted %v (%v)",
			strings.Count(bundle, "BEGIN CERTIFICATE"), verifies(cert, bundle), err)
	}
	fresh, err := ca.Sign(bg, csrFor(t, enrolled.ID, collectorKey), f.OrgA, enrolled.ID)
	if err != nil || string(parseCert(t, fresh.CertificatePEM).AuthorityKeyId) == string(cert.AuthorityKeyId) {
		t.Fatalf("certificate after roll-over from the old CA (%v)", err)
	}
	if fresh.CABundlePEM != bundle {
		t.Fatal("the CA bundle of a certificate issued during the roll-over lacks a trust anchor")
	}
	// Another replica uses the same CA from the database.
	if other, berr := discovery.NewCA(f.App, enc).TrustBundle(bg); berr != nil || other != bundle {
		t.Fatalf("second instance bundle differs (%v)", berr)
	}
	if err = ca.RetirePrevious(bg); err != nil {
		t.Fatal(err)
	}
	if bundle, _ = ca.TrustBundle(bg); strings.Count(bundle, "BEGIN CERTIFICATE") != 1 || verifies(cert, bundle) {
		t.Fatal("the retired CA is still trusted")
	}

	// Disabling revokes: CRL, IsRevoked, no renewal.
	disabled := discovery.CollectorDisabled
	if _, err = repo.UpdateCollector(ctx, f.OrgA, enrolled.ID, &discovery.CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if revoked, rerr := ca.IsRevoked(bg, renewed.Serial); rerr != nil || !revoked {
		t.Fatalf("renewed certificate revoked = %v (%v)", revoked, rerr)
	}
	w = call(ctx, http.MethodGet, "/api/v1/collectors/pki", "")
	var pki map[string]string
	if err = json.Unmarshal(w.Body.Bytes(), &pki); err != nil || w.Code != http.StatusOK {
		t.Fatalf("pki: %d %s", w.Code, w.Body.String())
	}
	block, _ := pem.Decode([]byte(pki["crl_pem"]))
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = crl.CheckSignatureFrom(parseCert(t, pki["ca_bundle_pem"])); err != nil {
		t.Fatalf("CRL signature: %v", err)
	}
	listed := map[string]bool{}
	for _, e := range crl.RevokedCertificateEntries {
		listed[e.SerialNumber.Text(16)] = true
	}
	if !listed[renewed.Serial] || !listed[enrolled.Certificate.Serial] {
		t.Fatalf("CRL lists %v", listed)
	}
	before := certificates()
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if resp = renew(time.Now().UTC().Add(55*24*time.Hour), renewed.CertificatePEM, renewedKey, otherKey); resp.Error != discovery.RenewalNotAuthorized || certificates() != before {
		t.Fatalf("renewal of a disabled collector: %+v", resp)
	}

	// The CA key is stored encrypted.
	var keyCT []byte
	if err = f.Admin.QueryRow(bg, `SELECT key_ciphertext FROM collector_ca WHERE state = 'active'`).Scan(&keyCT); err != nil {
		t.Fatal(err)
	}
	if _, perr := x509.ParsePKCS8PrivateKey(keyCT); perr == nil {
		t.Fatal("the CA key is stored in plaintext")
	}
}
