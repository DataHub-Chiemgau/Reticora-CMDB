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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

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
// CA is retired, which is refused while the collector still holds a
// certificate of it unless forced (WP-430); the CA is kept in the database,
// encrypted under the master key.
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
	if _, err = ca.Rollover(bg, discovery.CAChangeOptions{}); err != nil {
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
	// The collector still holds certificates of the previous CA (enrolled and
	// renewed before the roll-over): the retirement is refused with their
	// number and changes nothing (WP-430); forced, it retires the CA.
	var inUse *discovery.PreviousCAInUseError
	if _, err = ca.RetirePrevious(bg, discovery.CAChangeOptions{}); !errors.As(err, &inUse) || inUse.Collectors != 1 {
		t.Fatalf("retirement with a collector on the previous CA: %v", err)
	}
	if again, _ := ca.TrustBundle(bg); again != bundle {
		t.Fatal("a refused retirement changed the trust anchors")
	}
	retired, err := ca.RetirePrevious(bg, discovery.CAChangeOptions{Force: true})
	if err != nil || retired.Overridden != 1 || len(retired.Retired) != 1 || len(retired.Previous) != 0 {
		t.Fatalf("forced retirement: %+v (%v)", retired, err)
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

// TestCollectorCARetireRefusedWhileInUse covers the lock of the retirement
// (WP-430, COL-04, F-102): after a roll-over RetirePrevious refuses with the
// number of active collectors that hold a certificate of the previous CA
// (their bundle lacks the new CA until they renew, WP-414) and changes
// nothing. Counted are a collector enrolled before and one renewed after the
// roll-over whose predecessor is not revoked yet; not counted are a disabled
// collector (its certificates are revoked) and the renewed one once its
// predecessor is revoked. The query of docs/betrieb/collector-ca.md lists
// exactly the counted collectors. Without any left the retirement succeeds
// without force; a second one finds no previous CA.
func TestCollectorCARetireRefusedWhileInUse(t *testing.T) {
	e, enc := auditEdge(t, "c1")
	f := e.f
	bg := context.Background()
	stays, _, _ := auditEnroll(t, e, "retire-stays")
	renews, renewKey, renewCert := auditEnrollWith(t, e, agedCA(e, enc, 55), f.OrgA, "retire-renews")
	disabledID, _, _ := auditEnroll(t, e, "retire-disabled")
	// A collector of another organization counts as well: the CA spans them.
	otherID, _, _ := auditEnrollIn(t, e, f.OrgB, "retire-other-org")
	if _, err := e.ca.Rollover(bg, discovery.CAChangeOptions{}); err != nil {
		t.Fatal(err)
	}
	doc, rerr := os.ReadFile("../../../docs/betrieb/collector-ca.md")
	if rerr != nil {
		t.Fatal(rerr)
	}
	listQuery := regexp.MustCompile("(?s)<!-- query: previous-ca-collectors -->\n```sql\n(.*?)```").FindSubmatch(doc)
	if listQuery == nil {
		t.Fatal("docs/betrieb/collector-ca.md: no query previous-ca-collectors")
	}
	onPrevious := func(after string, ids ...string) {
		t.Helper()
		want := len(ids)
		rows, err := f.Admin.Query(bg, `SELECT collector_id::text FROM (`+strings.TrimSuffix(strings.TrimSpace(string(listQuery[1])), ";")+`) q`)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(listed)
		slices.Sort(ids)
		if fmt.Sprint(listed) != fmt.Sprint(ids) {
			t.Fatalf("%s: the query of the runbook lists %v, want %v", after, listed, ids)
		}
		st, err := e.ca.Status(bg)
		if err != nil {
			t.Fatal(err)
		}
		if st.Collectors != want || len(st.Previous) != 1 {
			t.Fatalf("%s: %d collectors on the previous CA, %d previous CAs; want %d and 1", after, st.Collectors, len(st.Previous), want)
		}
		var inUse *discovery.PreviousCAInUseError
		if _, err = e.ca.RetirePrevious(bg, discovery.CAChangeOptions{}); !errors.As(err, &inUse) || inUse.Collectors != want ||
			!strings.Contains(err.Error(), strconv.Itoa(want)+" active collectors") {
			t.Fatalf("%s: retirement = %v, want the refusal with %d collectors", after, err, want)
		}
		var previous int
		if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM collector_ca WHERE state = 'previous'`).Scan(&previous); err != nil || previous != 1 {
			t.Fatalf("%s: a refused retirement changed collector_ca (%d previous, %v)", after, previous, err)
		}
	}
	onPrevious("after the roll-over", stays, renews, disabledID, otherID)

	disabled := discovery.CollectorDisabled
	for _, c := range []struct{ org, id string }{{f.OrgA, disabledID}, {f.OrgB, otherID}} {
		if _, err := e.repo.UpdateCollector(f.OrgCtx(c.org), c.org, c.id, &discovery.CollectorPatch{Status: &disabled}); err != nil {
			t.Fatal(err)
		}
	}
	onPrevious("after the disables", stays, renews)

	// The renewal issues a certificate of the new CA; the predecessor stays
	// valid until the collector connects with the successor.
	subject := discovery.CollectorSubject(f.OrgA, renews) + discovery.CertificateRenewSuffix
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	resp := discovery.NewCertificateRenewal(f.App, e.ca).Renew(bg, subject,
		renewalRequest(t, subject, subjectCSR(t, renews, f.OrgA, newKey), renewCert.CertificatePEM, time.Now(), renewKey))
	if resp.Error != "" || resp.IssuedCert == nil || strings.Count(resp.CABundlePEM, "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("renewal after the roll-over: %+v", resp)
	}
	if verifies(parseCert(t, resp.CertificatePEM), renewCert.CABundlePEM) {
		t.Fatal("the renewal after the roll-over was signed by the previous CA")
	}
	onPrevious("after the renewal, before the predecessor is revoked", stays, renews)
	if _, err := discovery.RevokeSupersededCertificates(bg, f.App, 0); err != nil {
		t.Fatal(err)
	}
	if revoked, err := e.ca.IsRevoked(bg, renewCert.Serial); err != nil || !revoked {
		t.Fatalf("control: the predecessor of the renewed certificate is not revoked (%v)", err)
	}
	onPrevious("after the predecessor was revoked", stays)

	if _, err := e.repo.UpdateCollector(f.OrgCtx(f.OrgA), f.OrgA, stays, &discovery.CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	ch, err := e.ca.RetirePrevious(bg, discovery.CAChangeOptions{})
	if err != nil || ch.Overridden != 0 || len(ch.Retired) != 1 || len(ch.Previous) != 0 || ch.EdgeIssuer != ch.Active {
		t.Fatalf("retirement without collectors on the previous CA: %+v (%v)", ch, err)
	}
	if _, err = e.ca.RetirePrevious(bg, discovery.CAChangeOptions{Force: true}); !errors.Is(err, discovery.ErrNoPreviousCA) {
		t.Fatalf("second retirement: %v, want %v", err, discovery.ErrNoPreviousCA)
	}
}

// TestCollectorCAReloadsAfterVersionChange covers the fallback of the reload
// (WP-430, COL-04): replica B loaded the CA; replica A rolls over and no
// announcement reaches B. B still holds the former state in memory, but its
// next signature comes from the new CA, its trust bundle and its callout
// know both anchors and it issues the edge server certificate by the
// previous CA (version check of collector_ca). After A retired the previous
// CA, B no longer admits a certificate of it and issues the edge server
// certificate by the new CA.
func TestCollectorCAReloadsAfterVersionChange(t *testing.T) {
	e, enc := auditEdge(t, "c2")
	f := e.f
	bg := context.Background()
	replicaA, replicaB := e.ca, discovery.NewCA(f.App, enc)
	oldID, oldKey, oldCert := auditEnrollWith(t, e, replicaB, f.OrgA, "reload-old")
	first := parseCert(t, oldCert.CABundlePEM)
	loadedBefore := discovery.LoadedCAFingerprint(replicaB)
	if loadedBefore == "" {
		t.Fatal("control: replica B has not loaded the CA")
	}
	ch, err := replicaA.Rollover(bg, discovery.CAChangeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := discovery.LoadedCAFingerprint(replicaB); got != loadedBefore {
		t.Fatal("control: replica B reloaded without a use")
	}
	signed, err := replicaB.Sign(bg, csrFor(t, oldID, oldKey), f.OrgA, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if parseCert(t, signed.CertificatePEM).CheckSignatureFrom(first) == nil || discovery.LoadedCAFingerprint(replicaB) != ch.Active.Fingerprint {
		t.Fatal("replica B signed by the previous CA after the roll-over of replica A")
	}
	if bundle, berr := replicaB.TrustBundle(bg); berr != nil || strings.Count(bundle, "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("trust bundle of replica B after the roll-over: %v", berr)
	}
	authz := discovery.NewEdgeAuthorizer(f.App, replicaB)
	for name, certPEM := range map[string]string{"previous CA": oldCert.CertificatePEM, "new CA": signed.CertificatePEM} {
		if _, aerr := authz.Authorize(bg, []string{certPEM}); aerr != nil {
			t.Fatalf("callout of replica B during the roll-over, certificate of the %s: %v", name, aerr)
		}
	}
	serverPEM, _, err := replicaB.IssueServer(bg, []string{"localhost"}, time.Hour)
	if err != nil || parseCert(t, serverPEM).CheckSignatureFrom(first) != nil {
		t.Fatalf("edge server certificate of replica B during the roll-over not by the previous CA (%v)", err)
	}

	if _, err = replicaA.RetirePrevious(bg, discovery.CAChangeOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = authz.Authorize(bg, []string{oldCert.CertificatePEM}); err == nil {
		t.Fatal("replica B admits a certificate of the retired CA")
	}
	if _, err = authz.Authorize(bg, []string{signed.CertificatePEM}); err != nil {
		t.Fatalf("replica B refuses a certificate of the active CA: %v", err)
	}
	// The first anchor of a bundle issued during the roll-over is the new CA.
	if serverPEM, _, err = replicaB.IssueServer(bg, []string{"localhost"}, time.Hour); err != nil ||
		parseCert(t, serverPEM).CheckSignatureFrom(parseCert(t, signed.CABundlePEM)) != nil {
		t.Fatalf("edge server certificate of replica B after the retirement not by the new CA (%v)", err)
	}
}

// TestCollectorCAChangeLock covers the CA lock (WP-430): a roll-over waits
// while a certificate is being issued (another session holds the lock
// shared), and an enrollment waits while a change holds it exclusively; each
// finishes once the lock is released.
func TestCollectorCAChangeLock(t *testing.T) {
	e, _ := auditEdge(t, "c3")
	f := e.f
	bg := context.Background()
	if _, err := e.ca.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	holder, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	waits := func(what, lock, unlock string, run func() error) {
		t.Helper()
		if _, lerr := holder.Exec(bg, lock, discovery.CALockClass); lerr != nil {
			t.Fatal(lerr)
		}
		done := make(chan error, 1)
		go func() { done <- run() }()
		select {
		case rerr := <-done:
			_, _ = holder.Exec(bg, unlock, discovery.CALockClass)
			t.Fatalf("%s did not wait for the CA lock (%v)", what, rerr)
		case <-time.After(time.Second):
		}
		if _, lerr := holder.Exec(bg, unlock, discovery.CALockClass); lerr != nil {
			t.Fatal(lerr)
		}
		select {
		case rerr := <-done:
			if rerr != nil {
				t.Fatalf("%s: %v", what, rerr)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not finish after the CA lock was released", what)
		}
	}
	waits("a roll-over during an issuance", `SELECT pg_advisory_lock_shared($1, 0)`, `SELECT pg_advisory_unlock_shared($1, 0)`, func() error {
		_, rerr := e.ca.Rollover(bg, discovery.CAChangeOptions{})
		return rerr
	})
	raw := "scopetest-c3-lock"
	code := &discovery.EnrollmentCode{OrganizationID: f.OrgA, Label: raw, ExpiresAt: time.Now().Add(time.Hour)}
	code.SetRawCode(raw)
	if err = e.repo.CreateEnrollmentCode(f.OrgCtx(f.OrgA), code); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr := csrFor(t, "", key)
	waits("an enrollment during a change", `SELECT pg_advisory_lock($1, 0)`, `SELECT pg_advisory_unlock($1, 0)`, func() error {
		_, rerr := e.repo.EnrollCollector(bg, raw, &discovery.Collector{Name: "lock"}, func(ctx context.Context, org, id string) (*discovery.IssuedCert, error) {
			return e.ca.Sign(ctx, csr, org, id)
		})
		return rerr
	})
}

// TestCollectorCAChangeAnnouncementAuthenticated covers the announcement of
// a change of the CA (WP-430): the internal NATS authenticates nobody, so a
// receiver accepts only an announcement with the MAC of the key derived from
// the derivation root of the installation (WP-459), at most 5 minutes old.
// A changed version, a MAC of another installation's root, an old timestamp
// and garbage are refused.
func TestCollectorCAChangeAnnouncementAuthenticated(t *testing.T) {
	newEnc := func() *crypto.EnvelopeEncryptor {
		t.Helper()
		key, err := crypto.GenerateMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		enc, err := crypto.NewEnvelopeEncryptor(key)
		if err != nil {
			t.Fatal(err)
		}
		if err = enc.UseEphemeralDerivationRoot(); err != nil {
			t.Fatal(err)
		}
		return enc
	}
	ca, foreign := discovery.NewCA(nil, newEnc()), discovery.NewCA(nil, newEnc())
	now := time.Now()
	msg, err := discovery.CAChangeMessage(ca, "v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = discovery.VerifyCAChange(ca, msg, now.Add(time.Minute)); err != nil {
		t.Fatalf("authentic announcement refused: %v", err)
	}
	other, _ := discovery.CAChangeMessage(foreign, "v1", now)
	stale, _ := discovery.CAChangeMessage(ca, "v1", now.Add(-6*time.Minute))
	for name, data := range map[string][]byte{
		"changed version":     []byte(strings.Replace(string(msg), `"v1"`, `"v2"`, 1)),
		"another root":        other,
		"older than 5 min":    stale,
		"no MAC":              []byte(`{"version":"v1","at":"` + now.UTC().Format(time.RFC3339Nano) + `"}`),
		"no JSON":             []byte("reload"),
		"empty":               nil,
		"MAC of empty fields": []byte(`{"mac":""}`),
	} {
		if verr := discovery.VerifyCAChange(ca, data, now); verr == nil {
			t.Errorf("%s: announcement accepted", name)
		}
	}
}
