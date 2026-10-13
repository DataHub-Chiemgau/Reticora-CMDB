package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

type edgeFixture struct {
	f     *scopetest.Fixture
	ca    *discovery.CA
	repo  *discovery.PGRepository
	codes int
}

// enroll registers a collector with a certificate and returns its id and
// TLS client identity.
func (e *edgeFixture) enroll(t *testing.T, name string) (string, tls.Certificate) {
	t.Helper()
	e.codes++
	raw := fmt.Sprintf("scopetest-a6-%s-%d", name, e.codes)
	code := &discovery.EnrollmentCode{OrganizationID: e.f.OrgA, Label: raw, ExpiresAt: time.Now().Add(time.Hour)}
	code.SetRawCode(raw)
	if err := e.repo.CreateEnrollmentCode(e.f.OrgCtx(e.f.OrgA), code); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr := csrFor(t, "", key)
	c := &discovery.Collector{Name: name}
	cert, err := e.repo.EnrollCollector(context.Background(), raw, c,
		func(ctx context.Context, org, id string) (*discovery.IssuedCert, error) {
			return e.ca.Sign(ctx, csr, org, id)
		})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID, tls.Certificate{Certificate: [][]byte{parseCert(t, cert.CertificatePEM).Raw}, PrivateKey: key}
}

// TestCollectorNATSEndpointAuthorization covers WP-146 (COL-08, PRI-07)
// against PostgreSQL: the callout admits a certificate of the collector CA
// for a collector that is not disabled and confines it to its own subject
// tree and inbox in the account COLLECTORS; a revoked, foreign or missing
// certificate and a disabled collector are refused; the endpoint's PKI
// files are written and its server certificate kept until renewal is due.
// With TEST_NATS_SERVER_BIN it runs deploy/docker-compose/nats-edge.conf
// end to end: no client certificate, no access; a collector reaches only
// its own subjects.
func TestCollectorNATSEndpointAuthorization(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	e := &edgeFixture{f: f, ca: discovery.NewCA(f.App, enc), repo: discovery.NewPGRepository(f.App)}
	authz := discovery.NewEdgeAuthorizer(f.App, e.ca)

	colID, colCert := e.enroll(t, "edge-a")
	certPEM := string(pemOf(colCert.Certificate[0]))
	id, err := authz.Authorize(bg, []string{certPEM})
	if err != nil || id.CollectorID != colID || id.OrganizationID != f.OrgA {
		t.Fatalf("authorize = %+v, %v", id, err)
	}
	uc := id.UserClaims("UABC", time.Now())
	tree := discovery.CollectorSubject(f.OrgA, colID) + ".>"
	if uc.Audience != discovery.EdgeAccount || !uc.Pub.Allow.Contains(tree) || len(uc.Pub.Allow) != 1 ||
		!uc.Sub.Allow.Contains(discovery.CollectorInbox(colID)+".>") || uc.Sub.Allow.Contains("_INBOX.>") {
		t.Fatalf("user claims %+v", uc)
	}

	// Credential bundles (WP-147, COL-02): only for the collector's own
	// certificate; the delivery is audited.
	gate := discovery.NewBundleGate(f.App, e.ca)
	tctx, target, err := gate.VerifyBundleTarget(bg, f.OrgA, colID, certPEM)
	if err != nil || target.CollectorID != colID || target.PublicKey == nil {
		t.Fatalf("bundle target = %+v, %v", target, err)
	}
	if target.CredentialIDs != nil {
		t.Fatalf("without scopes the bundle selects by client, got %v", target.CredentialIDs)
	}
	// With discovery scopes the bundle holds exactly their credentials (DIS-09).
	scopeCred := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'scope-cred', 'snmp_v3', '\x00')`, scopeCred, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, credential_ids)
		VALUES ($1, $2, 'net', 'cidr', '10.9.0.0/24', ARRAY[$3::uuid]), ($1, $2, 'host', 'host', '10.9.1.1', '{}')`, f.OrgA, colID, scopeCred); err != nil {
		t.Fatal(err)
	}
	if _, scoped, serr := gate.VerifyBundleTarget(bg, f.OrgA, colID, certPEM); serr != nil || len(scoped.CredentialIDs) != 1 || scoped.CredentialIDs[0] != scopeCred {
		t.Fatalf("bundle target with scopes = %+v, %v", scoped, serr)
	}
	if _, _, err = gate.VerifyBundleTarget(bg, f.OrgA, "00000000-0000-4000-8000-000000000000", certPEM); err == nil {
		t.Fatal("a certificate obtained a bundle for another collector")
	}
	if err = gate.BundleDelivered(tctx, target, []string{"cred-1"}); err != nil {
		t.Fatal(err)
	}
	var audited int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'credential.bundle_delivered'
		AND resource_id = $2`, f.OrgA, colID).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("%d audited deliveries (%v)", audited, err)
	}

	// Refused: no certificate, a certificate of another CA, a revoked one.
	if _, err = authz.Authorize(bg, nil); !errors.Is(err, discovery.ErrNoClientCertificate) {
		t.Fatalf("no certificate: %v", err)
	}
	selfKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	selfSigned := selfSignedPEM(t, selfKey, colID, f.OrgA)
	if _, err = authz.Authorize(bg, []string{selfSigned}); err == nil {
		t.Fatal("a self-signed certificate was admitted")
	}

	// The callout service answers with a signed user JWT or a refusal; its
	// keys come from the master key, the same for every replica.
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := discovery.DeriveEdgeSecrets(enc); again.IssuerSeed != secrets.IssuerSeed || again.Password != secrets.Password {
		t.Fatal("the endpoint keys are not stable")
	}
	issuerPub := secrets.IssuerKey
	svc, err := discovery.NewEdgeAuthService(authz, secrets.IssuerSeed)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, _ := nkeys.CreateServer()
	serverPub, _ := serverKey.PublicKey()
	userKey, _ := nkeys.CreateUser()
	userPub, _ := userKey.PublicKey()
	ask := func(certs ...string) *jwt.AuthorizationResponseClaims {
		t.Helper()
		req := jwt.NewAuthorizationRequestClaims(serverPub)
		req.UserNkey = userPub
		req.Server.ID = serverPub
		req.TLS = &jwt.ClientTLS{Certs: certs}
		token, eerr := req.Encode(serverKey)
		if eerr != nil {
			t.Fatal(eerr)
		}
		answer, rerr := svc.Respond(bg, []byte(token))
		if rerr != nil {
			t.Fatal(rerr)
		}
		resp, derr := jwt.DecodeAuthorizationResponseClaims(string(answer))
		if derr != nil || resp.Issuer != issuerPub || resp.Subject != userPub {
			t.Fatalf("response %+v, %v", resp, derr)
		}
		return resp
	}
	if resp := ask(certPEM); resp.Error != "" || resp.Jwt == "" {
		t.Fatalf("admitted collector: %+v", resp)
	} else if user, derr := jwt.DecodeUserClaims(resp.Jwt); derr != nil || user.Audience != discovery.EdgeAccount || user.Name != colID {
		t.Fatalf("user JWT %+v, %v", user, derr)
	}
	if resp := ask(); resp.Error == "" || resp.Jwt != "" {
		t.Fatalf("request without certificate: %+v", resp)
	}

	// The endpoint's files.
	confDir := t.TempDir()
	dir := filepath.Join(confDir, "pki")
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost", "127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "server.pem"))
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost", "127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "edge.conf"))
	if !strings.Contains(string(conf), secrets.IssuerKey) || !strings.Contains(string(conf), "verify: true") ||
		strings.Contains(string(conf), secrets.IssuerSeed) {
		t.Fatalf("edge.conf: %s", conf)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "server.pem"))
	server := parseCert(t, string(first))
	if string(first) != string(second) || server.VerifyHostname("localhost") != nil || server.VerifyHostname("127.0.0.1") != nil {
		t.Fatal("server certificate not kept or without the host names")
	}

	disabledID, disabledCert := e.enroll(t, "edge-disabled")
	disabled := discovery.CollectorDisabled
	if _, err = e.repo.UpdateCollector(f.OrgCtx(f.OrgA), f.OrgA, disabledID, &discovery.CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err = authz.Authorize(bg, []string{string(pemOf(disabledCert.Certificate[0]))}); err == nil {
		t.Fatal("a disabled collector was admitted")
	}

	bin := os.Getenv("TEST_NATS_SERVER_BIN")
	if bin == "" {
		t.Log("TEST_NATS_SERVER_BIN not set; skipping the end-to-end run of nats-edge.conf")
		return
	}
	runEdgeServer(t, bin, confDir, secrets, f, e, colID, colCert, disabledCert)
}

func runEdgeServer(t *testing.T, bin, confDir string, secrets *discovery.EdgeSecrets, f *scopetest.Fixture, e *edgeFixture,
	colID string, colCert, disabledCert tls.Certificate) {
	t.Helper()
	const port = 14229
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The deployed configuration, run like the container: from its
	// directory with the PKI volume as pki/.
	conf, err := os.ReadFile("../../../deploy/docker-compose/nats-edge.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(confDir, "nats-edge.conf"), conf, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, "-c", "nats-edge.conf", "-p", fmt.Sprint(port))
	cmd.Dir = confDir
	cmd.Env = append(os.Environ(), "RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:1")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if serr := cmd.Start(); serr != nil {
		t.Fatal(serr)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(out.String())
		}
	}()
	url := fmt.Sprintf("tls://127.0.0.1:%d", port)
	go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: f.App, CA: e.ca, Secrets: secrets, URL: url})

	roots, _ := e.ca.TrustPool(context.Background())
	connect := func(cert *tls.Certificate, inbox string) (*nats.Conn, error) {
		cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		opts := []nats.Option{nats.Secure(cfg), nats.TLSHandshakeFirst(), nats.Timeout(3 * time.Second)}
		if inbox != "" {
			opts = append(opts, nats.CustomInboxPrefix(inbox))
		}
		return nats.Connect(url, opts...)
	}
	var nc *nats.Conn
	for i := 0; i < 50; i++ {
		if nc, err = connect(&colCert, discovery.CollectorInbox(colID)); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("collector with its certificate: %v", err)
	}
	defer nc.Close()

	// Own subjects work, foreign ones are refused.
	own := discovery.CollectorSubject(f.OrgA, colID) + ".heartbeat"
	sub, err := nc.SubscribeSync(own)
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.Publish(own, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err = sub.NextMsg(3 * time.Second); err != nil {
		t.Fatalf("own subject: %v", err)
	}
	violation := make(chan error, 4)
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { violation <- err })
	_ = nc.Publish(discovery.CollectorSubject(f.OrgA, "00000000-0000-4000-8000-000000000000")+".heartbeat", []byte("x"))
	_ = nc.Flush()
	select {
	case verr := <-violation:
		if !strings.Contains(strings.ToLower(verr.Error()), "permissions violation") {
			t.Fatalf("foreign subject: %v", verr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publishing to another collector's subject was not refused")
	}

	if conn, cerr := connect(nil, ""); cerr == nil {
		conn.Close()
		t.Fatal("connected without a client certificate")
	}
	if conn, cerr := connect(&disabledCert, ""); cerr == nil {
		conn.Close()
		t.Fatal("a disabled collector connected")
	}
}

func pemOf(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// selfSignedPEM is a client certificate naming the collector that no CA
// of Reticora signed.
func selfSignedPEM(t *testing.T, key *ecdsa.PrivateKey, cn, org string) string {
	t.Helper()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pemOf(der))
}
