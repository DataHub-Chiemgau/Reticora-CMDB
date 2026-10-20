// Certificate renewal over the collector's NATS channel on PostgreSQL and a
// real nats-server (WP-375, COL-04, COL-02, finding F-012).
//
//   - TestAuditCertificateRenewalBoundToCollector (moved from
//     collector_pki_auditdefect_test.go): only the collector itself renews.
//     The HTTP route is gone (404/405 for a session and an API key with
//     discovery:ingest, nothing issued); on the NATS subject a certificate
//     of another collector, a foreign signature, a stale timestamp, a CSR
//     for another collector and a certificate at 50 % are refused; at 61 %
//     the collector gets a 90-day certificate for its CSR key with the CA
//     bundle; a replay of that request issues nothing.
//   - TestCertificateRenewalRevokesPredecessor: the predecessor is revoked
//     when the collector first connects with the successor, not before.
//   - TestRunEdgeRevokesSupersededCertificates: RunEdge revokes the
//     predecessor at the latest 7 days after the successor was issued.
//   - TestCollectorCAUpgradeDetection: the detection queries of
//     docs/betrieb/collector-ca.md find a second issuance made before the
//     upgrade, not the enrollment certificate.
//
// The NATS tests run an internal nats-server with a leaf node port and
// deploy/docker-compose/nats-edge.conf bridged to it (TEST_NATS_SERVER_BIN,
// WP-374; skipped without it, failing in the CI).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<path to nats-server 2.10+> go test -count=1 -v -p 1 \
//	  -run 'TestAuditCertificateRenewalBoundToCollector|TestCertificateRenewalRevokesPredecessor|TestRunEdgeRevokesSupersededCertificates|TestCollectorCAUpgradeDetection' ./internal/discovery/
package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// agedCA is the collector CA of the fixture with a clock days in the past:
// its certificates are days into their 90-day lifetime.
func agedCA(e *edgeFixture, enc *crypto.EnvelopeEncryptor, days int) *discovery.CA {
	return discovery.NewCA(e.f.App, enc).WithClock(func() time.Time {
		return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	})
}

// subjectCSR is a CSR of key naming the collector cn and, unless empty, the
// organization org.
func subjectCSR(t *testing.T, cn, org string, key any) string {
	t.Helper()
	subject := pkix.Name{CommonName: cn}
	if org != "" {
		subject.Organization = []string{org}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: subject}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// renewalRequest is a renewal request on subject for csr with the current
// certificate certPEM and the timestamp at, signed by signer.
func renewalRequest(t *testing.T, subject, csr, certPEM string, at time.Time, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	digest := sha256.Sum256(discovery.RenewalSigningInput(subject, ts, csr))
	sig, err := ecdsa.SignASN1(rand.Reader, signer, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(discovery.RenewalRequest{CSR: csr, CertificatePEM: certPEM, Timestamp: ts, Signature: sig})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// renewStack is the renewal path end to end: an internal nats-server with a
// leaf node port, nats-edge.conf bridged to it (account COLLECTORS), the
// authorization callout (RunEdge) and the renewal service on the internal
// server.
type renewStack struct {
	// core is a plain connection to the internal NATS, which authenticates
	// nobody: the server's side and anyone else on that network.
	core    *nats.Conn
	edgeURL string
	roots   *x509.CertPool
}

func startRenewStack(t *testing.T, e *edgeFixture, enc *crypto.EnvelopeEncryptor) *renewStack {
	t.Helper()
	bin := edgeNATSServerBin(t)
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, filepath.Join(dir, "pki"), []string{"localhost", "127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile("../../../deploy/docker-compose/nats-edge.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "nats-edge.conf"), conf, 0o644); err != nil {
		t.Fatal(err)
	}
	corePort, leafPort, edgePort := auditFreePort(t), auditFreePort(t), auditFreePort(t)
	if err = os.WriteFile(filepath.Join(dir, "core.conf"), fmt.Appendf(nil, "port: %d\nleafnodes { port: %d }\n", corePort, leafPort), 0o644); err != nil {
		t.Fatal(err)
	}
	startNATSServer(t, bin, dir, nil, "-c", "core.conf")
	startNATSServer(t, bin, dir, []string{fmt.Sprintf("RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:%d", leafPort)},
		"-c", "nats-edge.conf", "-p", fmt.Sprint(edgePort))

	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	s := &renewStack{edgeURL: fmt.Sprintf("tls://127.0.0.1:%d", edgePort)}
	if s.roots, err = e.ca.TrustPool(bg); err != nil {
		t.Fatal(err)
	}
	go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: e.f.App, CA: e.ca, Secrets: secrets, URL: s.edgeURL})
	for range 50 {
		if s.core, err = nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", corePort), nats.NoReconnect()); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("internal NATS: %v", err)
	}
	t.Cleanup(s.core.Close)
	svc := discovery.NewCertificateRenewal(e.f.App, e.ca)
	go func() { _ = svc.Serve(ctx, s.core) }()
	return s
}

// startNATSServer runs nats-server in dir until the test ends; its output is
// logged when the test failed.
func startNATSServer(t *testing.T, bin, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("nats-server %v:\n%s", args, out.String())
		}
	})
}

// dial connects collector id to the endpoint with its client identity.
func (s *renewStack) dial(id string, cert tls.Certificate) (*nats.Conn, error) {
	return nats.Connect(s.edgeURL, nats.Secure(&tls.Config{RootCAs: s.roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}),
		nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(id)), nats.NoReconnect())
}

// connect is dial, retried while the endpoint and its callout start.
func (s *renewStack) connect(t *testing.T, id string, cert tls.Certificate) *nats.Conn {
	t.Helper()
	var err error
	for range 50 {
		var nc *nats.Conn
		if nc, err = s.dial(id, cert); err == nil {
			t.Cleanup(nc.Close)
			return nc
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("collector %s cannot connect to the endpoint: %v", id, err)
	return nil
}

// askRenewal sends a renewal request over nc and decodes the answer; it
// waits while the request finds no responder yet (leaf node and
// subscription starting).
func askRenewal(t *testing.T, nc *nats.Conn, subject string, data []byte) discovery.RenewalResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		msg, err := nc.Request(subject, data, 5*time.Second)
		if errors.Is(err, nats.ErrNoResponders) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("renewal request on %s: %v", subject, err)
		}
		var resp discovery.RenewalResponse
		if err = json.Unmarshal(msg.Data, &resp); err != nil {
			t.Fatalf("renewal answer %s: %v", msg.Data, err)
		}
		return resp
	}
}

func renewalSubject(orgID, collectorID string) string {
	return discovery.CollectorSubject(orgID, collectorID) + discovery.CertificateRenewSuffix
}

func TestAuditCertificateRenewalBoundToCollector(t *testing.T) {
	e, enc := auditEdge(t, "d0")
	f := e.f
	bg := context.Background()
	stack := startRenewStack(t, e, enc)

	// Y: registered by an administrator, never enrolled, no certificate.
	y := &discovery.Collector{OrganizationID: f.OrgA, Name: "audit-renew-y"}
	if err := e.repo.RegisterCollector(f.OrgCtx(f.OrgA), y); err != nil {
		t.Fatal(err)
	}
	// X and V: enrolled with their own keys, certificates issued 55 of 90
	// days ago (61 %, renewal due); W: 45 of 90 days (50 %).
	xID, xKey, xCert := auditEnrollWith(t, e, agedCA(e, enc, 55), f.OrgA, "audit-renew-x")
	vID, vKey, vCert := auditEnrollWith(t, e, agedCA(e, enc, 55), f.OrgA, "audit-renew-v")
	wID, wKey, wCert := auditEnrollWith(t, e, agedCA(e, enc, 45), f.OrgA, "audit-renew-w")
	xSubject := renewalSubject(f.OrgA, xID)
	attackerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	issued := func(t *testing.T) (certs, audits int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM collector_certificate WHERE organization_id = $1),
			(SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'collector.certificate_issued')`, f.OrgA).Scan(&certs, &audits); err != nil {
			t.Fatal(err)
		}
		return certs, audits
	}
	certs0, audits0 := issued(t)
	// nothingIssued: no certificate row, no audit entry, and X's legitimate
	// certificate stays valid.
	nothingIssued := func(t *testing.T) {
		t.Helper()
		if c, a := issued(t); c != certs0 || a != audits0 {
			t.Errorf("F-012: issued without proof of the collector's key: %d certificate rows (want %d), %d entries collector.certificate_issued (want %d)",
				c, certs0, a, audits0)
		}
		if revoked, err := e.ca.IsRevoked(bg, xCert.Serial); err != nil || revoked {
			t.Errorf("control: X's legitimate certificate revoked=%v err=%v", revoked, err)
		}
	}
	refused := func(t *testing.T, resp discovery.RenewalResponse, want string) {
		t.Helper()
		if resp.Error != want || resp.IssuedCert != nil {
			cn := ""
			if resp.IssuedCert != nil && resp.CertificatePEM != "" {
				cn = parseCert(t, resp.CertificatePEM).Subject.CommonName
			}
			t.Errorf("answer error=%q certificate CN=%q, want error %q and no certificate", resp.Error, cn, want)
		}
	}

	// (a) The HTTP renewal is gone: a session and an API key with
	// discovery:ingest get 404/405, for a never enrolled and an enrolled
	// collector.
	t.Run("HTTP renewal", func(t *testing.T) {
		mux := chi.NewRouter()
		discovery.NewHandler(e.repo, nil).WithCA(e.ca).RegisterRoutes(mux)
		ingest := []identity.Permission{identity.PermDiscoveryIngest}
		principals := []identity.Principal{
			{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Permissions: ingest},
			{Subject: f.ID(), OrganizationID: f.OrgA, Type: identity.PrincipalTypeAPIKey, Permissions: ingest},
		}
		for _, p := range principals {
			ctx := identity.WithPrincipal(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}), p)
			for _, target := range []struct{ name, id string }{{"never enrolled collector", y.ID}, {"enrolled collector past 60 %", xID}} {
				body, _ := json.Marshal(map[string]string{"csr": csrFor(t, target.id, attackerKey)})
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/collectors/"+target.id+"/certificate",
					strings.NewReader(string(body))).WithContext(ctx))
				if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
					t.Errorf("COL-04/F-012: %s principal with [discovery:ingest], %s: POST /api/v1/collectors/{id}/certificate → %d %s, want 404/405",
						p.Type, target.name, w.Code, w.Body.String())
				}
			}
		}
		nothingIssued(t)
	})

	// (b) On X's subject with the certificate of collector V, signed with
	// V's key (on the internal NATS; the endpoint does not even let V
	// publish there).
	t.Run("certificate of another collector", func(t *testing.T) {
		refused(t, askRenewal(t, stack.core, xSubject, renewalRequest(t, xSubject, subjectCSR(t, xID, f.OrgA, attackerKey), vCert.CertificatePEM, time.Now(), vKey)),
			discovery.RenewalNotAuthorized)
		nothingIssued(t)
	})

	// (d) X's public certificate with a signature by a foreign key, or none.
	t.Run("foreign signature", func(t *testing.T) {
		refused(t, askRenewal(t, stack.core, xSubject, renewalRequest(t, xSubject, subjectCSR(t, xID, f.OrgA, attackerKey), xCert.CertificatePEM, time.Now(), attackerKey)),
			discovery.RenewalNotAuthorized)
		unsigned, _ := json.Marshal(discovery.RenewalRequest{CSR: subjectCSR(t, xID, f.OrgA, attackerKey), CertificatePEM: xCert.CertificatePEM,
			Timestamp: time.Now().UTC().Format(time.RFC3339)})
		refused(t, askRenewal(t, stack.core, xSubject, unsigned), discovery.RenewalInvalidRequest)
		// A signature by X's key over another subject does not carry over.
		other := renewalSubject(f.OrgA, vID)
		ts := time.Now().UTC().Format(time.RFC3339)
		csr := subjectCSR(t, xID, f.OrgA, attackerKey)
		digest := sha256.Sum256(discovery.RenewalSigningInput(other, ts, csr))
		sig, _ := ecdsa.SignASN1(rand.Reader, xKey, digest[:])
		moved, _ := json.Marshal(discovery.RenewalRequest{CSR: csr, CertificatePEM: xCert.CertificatePEM, Timestamp: ts, Signature: sig})
		refused(t, askRenewal(t, stack.core, xSubject, moved), discovery.RenewalNotAuthorized)
		nothingIssued(t)
	})

	xc := stack.connect(t, xID, clientIdentity(t, xKey, xCert))

	// (f) X itself over the endpoint, but with a timestamp more than 5 min
	// off.
	t.Run("timestamp outside the window", func(t *testing.T) {
		newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		for _, at := range []time.Time{time.Now().Add(-6 * time.Minute), time.Now().Add(6 * time.Minute)} {
			refused(t, askRenewal(t, xc, xSubject, renewalRequest(t, xSubject, subjectCSR(t, xID, f.OrgA, newKey), xCert.CertificatePEM, at, xKey)),
				discovery.RenewalStaleTimestamp)
		}
		nothingIssued(t)
	})

	// The CSR must name X (CN) and its organization (O).
	t.Run("CSR for another collector or organization", func(t *testing.T) {
		newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		for _, csr := range []string{subjectCSR(t, vID, f.OrgA, newKey), subjectCSR(t, xID, f.OrgB, newKey), subjectCSR(t, xID, "", newKey)} {
			refused(t, askRenewal(t, xc, xSubject, renewalRequest(t, xSubject, csr, xCert.CertificatePEM, time.Now(), xKey)), discovery.RenewalInvalidCSR)
		}
		nothingIssued(t)
	})

	// (e) W at 50 % of its lifetime.
	t.Run("renewal at 50 %", func(t *testing.T) {
		wc := stack.connect(t, wID, clientIdentity(t, wKey, wCert))
		newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		wSubject := renewalSubject(f.OrgA, wID)
		refused(t, askRenewal(t, wc, wSubject, renewalRequest(t, wSubject, subjectCSR(t, wID, f.OrgA, newKey), wCert.CertificatePEM, time.Now(), wKey)),
			discovery.RenewalNotDue)
		nothingIssued(t)
	})

	// (c) X at 61 % with its own certificate and signature: a 90-day
	// certificate CN = X for the CSR key, with the CA bundle; audited with
	// the predecessor.
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	positive := renewalRequest(t, xSubject, subjectCSR(t, xID, f.OrgA, newKey), xCert.CertificatePEM, time.Now(), xKey)
	resp := askRenewal(t, xc, xSubject, positive)
	if resp.Error != "" || resp.IssuedCert == nil {
		t.Fatalf("COL-04: X's renewal at 61 %% refused: %+v", resp)
	}
	cert := parseCert(t, resp.CertificatePEM)
	life := cert.NotAfter.Sub(cert.NotBefore)
	if cert.Subject.CommonName != xID || !slices.Equal(cert.Subject.Organization, []string{f.OrgA}) ||
		life < discovery.CertValidity || life > discovery.CertValidity+10*time.Minute || cert.SerialNumber.Text(16) != resp.Serial ||
		resp.Serial == xCert.Serial || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("renewed certificate: subject %v, life %s, serial %s (predecessor %s)", cert.Subject, life, resp.Serial, xCert.Serial)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&newKey.PublicKey) {
		t.Fatal("the renewed certificate is not for the CSR key")
	}
	bundle, err := e.ca.TrustBundle(bg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CABundlePEM == "" || resp.CABundlePEM != bundle || !verifies(cert, resp.CABundlePEM) {
		t.Fatalf("ca_bundle_pem of the answer: %q", resp.CABundlePEM)
	}
	if c, a := issued(t); c != certs0+1 || a != audits0+1 {
		t.Fatalf("after the renewal %d certificate rows (want %d), %d issuance entries (want %d)", c, certs0+1, a, audits0+1)
	}
	var serial, predecessor string
	if err = f.Admin.QueryRow(bg, `SELECT changes->>'serial', changes->>'predecessor_serial' FROM audit_log
		WHERE organization_id = $1 AND action = 'collector.certificate_issued' AND resource_id = $2`, f.OrgA, xID).Scan(&serial, &predecessor); err != nil ||
		serial != resp.Serial || predecessor != xCert.Serial {
		t.Fatalf("audit collector.certificate_issued: serial %q, predecessor %q (%v)", serial, predecessor, err)
	}
	// X has not connected with the successor yet: the predecessor stays
	// valid (TestCertificateRenewalRevokesPredecessor).
	if revoked, rerr := e.ca.IsRevoked(bg, xCert.Serial); rerr != nil || revoked {
		t.Fatalf("predecessor revoked=%v (%v) before X connected with the successor", revoked, rerr)
	}

	// A replay of the request (captured on the internal NATS) issues no
	// second certificate.
	refused(t, askRenewal(t, stack.core, xSubject, positive), discovery.RenewalNotDue)
	if c, a := issued(t); c != certs0+1 || a != audits0+1 {
		t.Errorf("the replayed request issued a certificate: %d certificate rows, %d issuance entries", c, a)
	}
}

func TestCertificateRenewalRevokesPredecessor(t *testing.T) {
	e, enc := auditEdge(t, "d7")
	f := e.f
	bg := context.Background()
	stack := startRenewStack(t, e, enc)
	revoked := func(serial string) bool {
		t.Helper()
		r, err := e.ca.IsRevoked(bg, serial)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	id, key, old := auditEnrollWith(t, e, agedCA(e, enc, 55), f.OrgA, "renew-predecessor")
	subject := renewalSubject(f.OrgA, id)
	nc := stack.connect(t, id, clientIdentity(t, key, old))
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	resp := askRenewal(t, nc, subject, renewalRequest(t, subject, subjectCSR(t, id, f.OrgA, newKey), old.CertificatePEM, time.Now(), key))
	if resp.Error != "" || resp.IssuedCert == nil {
		t.Fatalf("renewal: %+v", resp)
	}
	successor := resp.IssuedCert

	// Before the first connection with the successor the predecessor stays
	// valid: a further connection with it is admitted.
	if revoked(old.Serial) {
		t.Fatal("the predecessor was revoked at issuance")
	}
	stack.connect(t, id, clientIdentity(t, key, old)).Close()
	if revoked(old.Serial) {
		t.Fatal("a connection with the predecessor revoked it")
	}

	// The first connection with the successor revokes the predecessor.
	stack.connect(t, id, clientIdentity(t, newKey, successor))
	if !revoked(old.Serial) {
		t.Fatal("COL-04: after the first connection with the successor the predecessor is not revoked")
	}
	if revoked(successor.Serial) {
		t.Fatal("the successor was revoked")
	}
	var reason string
	if err := f.Admin.QueryRow(bg, `SELECT revocation_reason FROM collector_certificate WHERE serial = $1`, old.Serial).Scan(&reason); err != nil || reason != "superseded" {
		t.Fatalf("revocation_reason %q (%v)", reason, err)
	}
	crlPEM, err := e.ca.CRL(bg)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(crlPEM)
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range crl.RevokedCertificateEntries {
		listed[entry.SerialNumber.Text(16)] = true
	}
	if !listed[old.Serial] || listed[successor.Serial] {
		t.Fatalf("CRL lists %v (predecessor %s, successor %s)", listed, old.Serial, successor.Serial)
	}

	// The predecessor is refused at the endpoint and for renewals.
	if conn, derr := stack.dial(id, clientIdentity(t, key, old)); derr == nil {
		conn.Close()
		t.Error("the revoked predecessor still connects")
	}
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if again := askRenewal(t, stack.core, subject, renewalRequest(t, subject, subjectCSR(t, id, f.OrgA, otherKey), old.CertificatePEM, time.Now(), key)); again.Error != discovery.RenewalNotAuthorized {
		t.Errorf("renewal with the revoked predecessor: %+v", again)
	}
}

func TestRunEdgeRevokesSupersededCertificates(t *testing.T) {
	e, enc := auditEdge(t, "d8")
	f := e.f
	bg := context.Background()
	// renewed enrolls a collector, issues a successor through the
	// repository and dates the successor back by age.
	renewed := func(name, age string) (predecessor, successor string) {
		t.Helper()
		id, _, enrolled := auditEnroll(t, e, name)
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		next, err := e.repo.RenewEdgeCertificate(f.OrgCtx(f.OrgA), f.OrgA, id, enrolled.Serial, func(ctx context.Context, orgID, collectorID string) (*discovery.IssuedCert, error) {
			return e.ca.Sign(ctx, subjectCSR(t, collectorID, orgID, key), orgID, collectorID)
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Admin.Exec(bg, `UPDATE collector_certificate SET created_at = CASE serial WHEN $1 THEN now() - interval '60 days'
			ELSE now() - $3::interval END WHERE serial IN ($1, $2)`, enrolled.Serial, next.Serial, age); err != nil {
			t.Fatal(err)
		}
		return enrolled.Serial, next.Serial
	}
	staleOld, staleNew := renewed("superseded-8d", "8 days")
	freshOld, freshNew := renewed("superseded-6d", "6 days")
	revoked := func(serial string) bool {
		t.Helper()
		r, err := e.ca.IsRevoked(bg, serial)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if revoked(staleOld) || revoked(freshOld) {
		t.Fatal("control: a predecessor is revoked before RunEdge")
	}
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: f.App, CA: e.ca, Secrets: secrets})
	deadline := time.Now().Add(10 * time.Second)
	for !revoked(staleOld) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !revoked(staleOld) {
		t.Fatal("COL-04: RunEdge did not revoke the predecessor of a successor issued 8 days ago")
	}
	for serial, name := range map[string]string{staleNew: "successor issued 8 days ago", freshOld: "predecessor of a successor issued 6 days ago", freshNew: "successor issued 6 days ago"} {
		if revoked(serial) {
			t.Errorf("RunEdge revoked the %s", name)
		}
	}
}

// docQuery returns the SQL block after the marker <!-- query: name --> of
// docs/betrieb/collector-ca.md with the psql variable :'upgrade_at' as $1.
func docQuery(t *testing.T, name string) string {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/betrieb/collector-ca.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)<!-- query: " + regexp.QuoteMeta(name) + " -->\n```sql\n(.*?)```").FindSubmatch(doc)
	if m == nil || !strings.Contains(string(m[1]), ":'upgrade_at'") {
		t.Fatalf("docs/betrieb/collector-ca.md: no query %q with :'upgrade_at'", name)
	}
	return strings.ReplaceAll(string(m[1]), ":'upgrade_at'", "$1")
}

func TestCollectorCAUpgradeDetection(t *testing.T) {
	e, _ := auditEdge(t, "da")
	f := e.f
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	sign := func(key *ecdsa.PrivateKey) discovery.CertIssuer {
		return func(ctx context.Context, orgID, collectorID string) (*discovery.IssuedCert, error) {
			return e.ca.Sign(ctx, subjectCSR(t, collectorID, orgID, key), orgID, collectorID)
		}
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	// Before the upgrade: X enrolled (registration certificate) and got a
	// second certificate through the repository; Y, registered by an
	// administrator without enrollment, got one over the former route (row
	// and route audit entry as the route wrote them).
	xID, _, enrolled := auditEnroll(t, e, "upgrade-x")
	second, err := e.repo.RenewEdgeCertificate(org, f.OrgA, xID, enrolled.Serial, sign(key))
	if err != nil {
		t.Fatal(err)
	}
	y := &discovery.Collector{OrganizationID: f.OrgA, Name: "upgrade-y"}
	if err = e.repo.RegisterCollector(org, y); err != nil {
		t.Fatal(err)
	}
	httpIssued, err := e.ca.Sign(bg, subjectCSR(t, y.ID, f.OrgA, key), f.OrgA, y.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO collector_certificate (organization_id, collector_id, serial, fingerprint, not_before, not_after)
		VALUES ($1, $2, $3, $4, $5, $6)`, f.OrgA, y.ID, httpIssued.Serial, httpIssued.Fingerprint, httpIssued.NotBefore, httpIssued.NotAfter); err != nil {
		t.Fatal(err)
	}
	route := "/api/v1/collectors/{id}/certificate"
	resourceType, action := middleware.RouteAction(http.MethodPost, route)
	scope := database.OrgWideScope(f.OrgA, f.User)
	if err = database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, ActorID: f.User, ActorType: "user",
			Action: action, ResourceType: resourceType, ResourceID: y.ID, Source: audit.SourceCollector,
			Changes: map[string]interface{}{"status": http.StatusCreated, "route": http.MethodPost + " " + route}})
		return rerr
	}); err != nil {
		t.Fatal(err)
	}

	var upgradeAt time.Time
	if err = f.Admin.QueryRow(bg, `SELECT now()`).Scan(&upgradeAt); err != nil {
		t.Fatal(err)
	}
	// After the upgrade: a renewal over the NATS channel is no finding.
	third, err := e.repo.RenewEdgeCertificate(org, f.OrgA, xID, second.Serial, sign(key))
	if err != nil {
		t.Fatal(err)
	}

	rows, err := f.Admin.Query(bg, docQuery(t, "certificates"), upgradeAt)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var orgID, collectorID, name, serial string
		var issuedAt, notAfter time.Time
		var revokedAt *time.Time
		if err = rows.Scan(&orgID, &collectorID, &name, &serial, &issuedAt, &revokedAt, &notAfter); err != nil {
			t.Fatal(err)
		}
		if orgID == f.OrgA {
			found[serial] = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found[second.Serial] || !found[httpIssued.Serial] {
		t.Errorf("the detection query misses an issuance before the upgrade: second issuance of X %v, certificate of the never enrolled Y %v", found[second.Serial], found[httpIssued.Serial])
	}
	if found[enrolled.Serial] || found[third.Serial] || len(found) != 2 {
		t.Errorf("the detection query reports the registration certificate (%v), the renewal after the upgrade (%v) or more (%d rows)",
			found[enrolled.Serial], found[third.Serial], len(found))
	}

	var auditRows []string
	rows, err = f.Admin.Query(bg, docQuery(t, "audit"), upgradeAt)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var orgID, actorType string
		var actorID, collectorID, status *string
		var at time.Time
		if err = rows.Scan(&orgID, &at, &actorType, &actorID, &collectorID, &status); err != nil {
			t.Fatal(err)
		}
		if orgID == f.OrgA && collectorID != nil && actorID != nil && status != nil {
			auditRows = append(auditRows, *collectorID+" "+actorType+" "+*actorID+" "+*status)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := y.ID + " user " + f.User + " 201"; len(auditRows) != 1 || auditRows[0] != want {
		t.Errorf("the audit query found %v, want [%s]", auditRows, want)
	}
}
