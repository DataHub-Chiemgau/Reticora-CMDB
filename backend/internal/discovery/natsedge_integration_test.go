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
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type edgeFixture struct {
	f     *scopetest.Fixture
	ca    *discovery.CA
	repo  *discovery.PGRepository
	codes int
}

// enroll registers a collector of organization A with a certificate and
// returns its id and TLS client identity.
func (e *edgeFixture) enroll(t *testing.T, name string) (string, tls.Certificate) {
	t.Helper()
	id, key, issued := auditEnroll(t, e, name)
	return id, clientIdentity(t, key, issued)
}

// clientIdentity is the TLS client identity of an enrolled collector.
func clientIdentity(t *testing.T, key *ecdsa.PrivateKey, issued *discovery.IssuedCert) tls.Certificate {
	t.Helper()
	return tls.Certificate{Certificate: [][]byte{parseCert(t, issued.CertificatePEM).Raw}, PrivateKey: key}
}

// TestCollectorNATSEndpointAuthorization covers WP-146 (COL-08, PRI-07)
// against PostgreSQL: the callout admits a certificate of the collector CA
// for a collector that is not disabled and confines it to its own subject
// tree and inbox in the account COLLECTORS; a revoked, foreign or missing
// certificate and a disabled collector are refused; the user JWTs live at
// most 15 minutes (WP-381); the endpoint's PKI files are written, edge.conf
// with the edge worker's login in the system account, and its server
// certificate kept until renewal is due.
// With TEST_NATS_SERVER_BIN it runs deploy/docker-compose/nats-edge.conf
// end to end: no client certificate, no access; a collector reaches only
// its own subjects, neither another collector's nor, publishing or
// subscribing, the subject tree of a collector of another organization
// (WP-374). Without the binary the test is skipped, in the CI it fails.
func TestCollectorNATSEndpointAuthorization(t *testing.T) {
	e, enc := auditEdge(t, "a6")
	f := e.f
	bg := context.Background()
	authz := discovery.NewEdgeAuthorizer(f.App, e.ca)

	colID, colCert := e.enroll(t, "edge-a")
	certPEM := string(pemOf(colCert.Certificate[0]))
	id, err := authz.Authorize(bg, []string{certPEM})
	if err != nil || id.CollectorID != colID || id.OrganizationID != f.OrgA {
		t.Fatalf("authorize = %+v, %v", id, err)
	}
	issuedAt := time.Now()
	uc := id.UserClaims("UABC", issuedAt)
	tree := discovery.CollectorSubject(f.OrgA, colID) + ".>"
	if uc.Audience != discovery.EdgeAccount || !uc.Pub.Allow.Contains(tree) || len(uc.Pub.Allow) != 1 ||
		!uc.Sub.Allow.Contains(discovery.CollectorInbox(colID)+".>") || uc.Sub.Allow.Contains("_INBOX.>") {
		t.Fatalf("user claims %+v", uc)
	}
	// A connection the cut of a disable missed passes the callout again
	// after at most 15 minutes (WP-381, COL-04).
	if uc.Expires <= issuedAt.Unix() || uc.Expires > issuedAt.Add(discovery.EdgeUserValidity).Unix() || discovery.EdgeUserValidity > 15*time.Minute {
		t.Fatalf("user JWT expires at %d, issued at %d: at most %v", uc.Expires, issuedAt.Unix(), discovery.EdgeUserValidity)
	}

	// Credential bundles (WP-147, COL-02): only for the collector's own
	// certificate; the delivery is audited.
	gate := discovery.NewBundleGate(f.App, e.ca)
	tctx, target, err := gate.VerifyBundleTarget(bg, f.OrgA, colID, certPEM)
	if err != nil || target.CollectorID != colID || target.PublicKey == nil {
		t.Fatalf("bundle target = %+v, %v", target, err)
	}
	if target.CredentialIDs == nil || len(target.CredentialIDs) != 0 {
		t.Fatalf("without scopes the bundle is empty (E-67), got %#v", target.CredentialIDs)
	}
	// With discovery scopes the bundle holds their effective credentials
	// (DIS-09): those of the scope with credential_ids and the
	// organization-wide one for the scope without (the same one here).
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
	if again, _ := discovery.DeriveEdgeSecrets(enc); again.IssuerSeed != secrets.IssuerSeed || again.Password != secrets.Password ||
		again.SysPassword != secrets.SysPassword {
		t.Fatal("the endpoint keys are not stable")
	}
	if len(secrets.SysPassword) != 64 || secrets.SysPassword == secrets.Password {
		t.Fatal("the system login needs a password of its own")
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
	// The edge worker's login in the system account bypasses the callout
	// (WP-381).
	sysUser := fmt.Sprintf(`SYS { users: [ { user: %q, password: %q } ] }`, discovery.EdgeSysUser, secrets.SysPassword)
	authUsers := fmt.Sprintf(`auth_users: [ %q, %q ]`, discovery.EdgeAuthUser, discovery.EdgeSysUser)
	if !strings.Contains(string(conf), sysUser) || !strings.Contains(string(conf), authUsers) {
		t.Fatalf("edge.conf without the system login: %s", conf)
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

	runEdgeServer(t, edgeNATSServerBin(t), confDir, secrets, f, e, colID, colCert, disabledCert)
}

func runEdgeServer(t *testing.T, bin, confDir string, secrets *discovery.EdgeSecrets, f *scopetest.Fixture, e *edgeFixture,
	colID string, colCert, disabledCert tls.Certificate) {
	t.Helper()
	port := auditFreePort(t)
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
	violation := make(chan error, 8)
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { violation <- err })
	// refused waits for the server's permissions violation of op
	// ("publish" or "subscription") on subject.
	refused := func(op, subject string) {
		t.Helper()
		want := fmt.Sprintf("permissions violation for %s to %q", op, subject)
		select {
		case verr := <-violation:
			if !strings.Contains(strings.ToLower(verr.Error()), want) {
				t.Fatalf("%s on %s: %v", op, subject, verr)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s on %s was not refused", op, subject)
		}
	}
	foreign := discovery.CollectorSubject(f.OrgA, "00000000-0000-4000-8000-000000000000") + ".heartbeat"
	_ = nc.Publish(foreign, []byte("x"))
	_ = nc.Flush()
	refused("publish", foreign)

	// Another organization (PRI-07): collector B of organization B on the
	// same endpoint. A can neither publish nor subscribe on B's tree, nor on
	// its own id under organization B; B's own tree works.
	otherID, otherKey, otherIssued := auditEnrollIn(t, e, f.OrgB, "edge-org-b")
	otherCert := clientIdentity(t, otherKey, otherIssued)
	nb, err := connect(&otherCert, discovery.CollectorInbox(otherID))
	if err != nil {
		t.Fatalf("collector of organization B: %v", err)
	}
	defer nb.Close()
	otherTree := discovery.CollectorSubject(f.OrgB, otherID)
	otherSub, err := nb.SubscribeSync(otherTree + ".>")
	if err != nil {
		t.Fatal(err)
	}
	receives := func(s *nats.Subscription, want string) {
		t.Helper()
		if msg, nerr := s.NextMsg(3 * time.Second); nerr != nil || string(msg.Data) != want {
			t.Fatalf("control: %q on %s not received: %v", want, s.Subject, nerr)
		}
	}
	receivesNothing := func(s *nats.Subscription) {
		t.Helper()
		if msg, nerr := s.NextMsg(time.Second); nerr == nil {
			t.Fatalf("%q on %s crossed the organizations (subscription %s)", msg.Data, msg.Subject, s.Subject)
		}
	}
	if err = nb.Publish(otherTree+".heartbeat", []byte("b-own")); err != nil {
		t.Fatal(err)
	}
	receives(otherSub, "b-own")
	crossOrg := []string{otherTree, discovery.CollectorSubject(f.OrgB, colID)}
	for _, tree := range crossOrg {
		_ = nc.Publish(tree+".heartbeat", []byte("from-org-a"))
		_ = nc.Flush()
		refused("publish", tree+".heartbeat")
	}
	receivesNothing(otherSub)
	crossSubs := make([]*nats.Subscription, 0, len(crossOrg))
	for _, tree := range crossOrg {
		cs, serr := nc.SubscribeSync(tree + ".>")
		if serr != nil {
			t.Fatal(serr)
		}
		_ = nc.Flush()
		refused("subscription", tree+".>")
		crossSubs = append(crossSubs, cs)
	}
	if err = nb.Publish(otherTree+".heartbeat", []byte("b-after")); err != nil {
		t.Fatal(err)
	}
	receives(otherSub, "b-after")
	for _, cs := range crossSubs {
		receivesNothing(cs)
	}
	// Control: A's own tree still works after the refusals.
	if err = nc.Publish(own, []byte("a-after")); err != nil {
		t.Fatal(err)
	}
	receives(sub, "a-after")

	if conn, cerr := connect(nil, ""); cerr == nil {
		conn.Close()
		t.Fatal("connected without a client certificate")
	}
	if conn, cerr := connect(&disabledCert, ""); cerr == nil {
		conn.Close()
		t.Fatal("a disabled collector connected")
	}
}

// kickStack is the endpoint as deployed, with the cut of disabled
// collectors (WP-381): an internal nats-server with a leaf node port,
// nats-edge.conf bridged to it (account COLLECTORS), the edge worker
// (RunEdge: callout and cut) on its own connection to the internal NATS, and
// PATCH /collectors/{id} on a repository that announces disables over
// another connection (another replica).
type kickStack struct {
	// core is a plain connection to the internal NATS: the server's side.
	core    *nats.Conn
	edgeURL string
	roots   *x509.CertPool
	mux     *chi.Mux
}

// startKickStack starts the stack. rewrite, when set, changes the generated
// edge.conf before the endpoint starts; validity, when set, is the lifetime
// of the callout's user JWTs.
func startKickStack(t *testing.T, e *edgeFixture, enc *crypto.EnvelopeEncryptor, rewrite func(string) string, validity time.Duration) *kickStack {
	t.Helper()
	bin := edgeNATSServerBin(t)
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, pki, []string{"localhost", "127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rewrite != nil {
		generated, rerr := os.ReadFile(filepath.Join(pki, "edge.conf"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if err = os.WriteFile(filepath.Join(pki, "edge.conf"), []byte(rewrite(string(generated))), 0o644); err != nil {
			t.Fatal(err)
		}
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
	dialCore := func() *nats.Conn {
		t.Helper()
		var nc *nats.Conn
		var cerr error
		for range 50 {
			if nc, cerr = nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", corePort), nats.NoReconnect()); cerr == nil {
				t.Cleanup(nc.Close)
				return nc
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("internal NATS: %v", cerr)
		return nil
	}
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	s := &kickStack{core: dialCore(), edgeURL: fmt.Sprintf("tls://127.0.0.1:%d", edgePort)}
	if s.roots, err = e.ca.TrustPool(bg); err != nil {
		t.Fatal(err)
	}
	e.repo.WithDisableAnnouncer(dialCore())
	go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: e.f.App, CA: e.ca, Secrets: secrets, URL: s.edgeURL,
		Bus: dialCore(), UserValidity: validity})
	s.mux = chi.NewRouter()
	discovery.NewHandler(e.repo, nil).RegisterRoutes(s.mux)
	return s
}

// edgeCollector is a connection like the collector binary keeps one: it
// reconnects forever (here without delay); its disconnects, reconnects and
// the errors the endpoint sent are recorded.
type edgeCollector struct {
	nc *nats.Conn
	// down receives the error of every disconnect, up every successful
	// reconnect, errs the errors of the endpoint (e.g. nats.ErrAuthExpired).
	down chan error
	up   chan struct{}
	errs chan error
}

// drain empties ch and returns what it held.
func drain[T any](ch chan T) []T {
	var out []T
	for {
		select {
		case v := <-ch:
			out = append(out, v)
		default:
			return out
		}
	}
}

// collector connects collector id, retried while the endpoint and its
// callout start.
func (s *kickStack) collector(t *testing.T, id string, cert tls.Certificate) *edgeCollector {
	t.Helper()
	c := &edgeCollector{down: make(chan error, 64), up: make(chan struct{}, 64), errs: make(chan error, 64)}
	opts := []nats.Option{nats.Secure(&tls.Config{RootCAs: s.roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}),
		nats.TLSHandshakeFirst(), nats.Timeout(3 * time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(id)),
		nats.MaxReconnects(-1), nats.ReconnectWait(100 * time.Millisecond), nats.ReconnectJitter(0, 0),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			select {
			case c.down <- err:
			default:
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			select {
			case c.up <- struct{}{}:
			default:
			}
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case c.errs <- err:
			default:
			}
		})}
	var err error
	for range 50 {
		if c.nc, err = nats.Connect(s.edgeURL, opts...); err == nil {
			t.Cleanup(c.nc.Close)
			return c
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("collector %s cannot connect to the endpoint: %v", id, err)
	return nil
}

// dial is one connection attempt of collector id without reconnects.
func (s *kickStack) dial(id string, cert tls.Certificate) (*nats.Conn, error) {
	return nats.Connect(s.edgeURL, nats.Secure(&tls.Config{RootCAs: s.roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}),
		nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(id)), nats.NoReconnect())
}

// disable sends PATCH /collectors/{id} {"status":"disabled"} as an
// administrator of the organization.
func (s *kickStack) disable(t *testing.T, f *scopetest.Fixture, orgID, id string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/api/v1/collectors/"+id, strings.NewReader(`{"status":"disabled"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User}))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	var c discovery.Collector
	if err := json.Unmarshal(w.Body.Bytes(), &c); w.Code != http.StatusOK || err != nil || c.Status != discovery.CollectorDisabled {
		t.Fatalf("PATCH /collectors/%s = %d %s", id, w.Code, w.Body.String())
	}
}

// delivered publishes data on subject from nc until the internal NATS
// receives it on sub (the leaf node propagates the interest first).
func delivered(t *testing.T, nc *nats.Conn, sub *nats.Subscription, subject, data string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := nc.Publish(subject, []byte(data)); err != nil {
			t.Fatalf("publish %s: %v", subject, err)
		}
		_ = nc.Flush()
		for {
			msg, err := sub.NextMsg(200 * time.Millisecond)
			if err != nil {
				break
			}
			if string(msg.Data) == data {
				return
			}
		}
	}
	t.Fatalf("control: %q on %s did not reach the internal NATS", data, subject)
}

// waitClosed waits until the reconnects of c are given up: the endpoint
// refused them (authorization violation twice).
func waitClosed(t *testing.T, c *edgeCollector, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !c.nc.IsClosed() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !c.nc.IsClosed() {
		t.Fatalf("%s: the collector's reconnects were not refused within %v (status %v, last error %v)", what, within, c.nc.Status(), c.nc.LastError())
	}
	if !errors.Is(c.nc.LastError(), nats.ErrAuthorization) {
		t.Fatalf("%s: the connection closed with %v, want the callout's refusal (%v)", what, c.nc.LastError(), nats.ErrAuthorization)
	}
}

// kickEvents returns the log records of event for the collector.
func kickEvents(t *testing.T, logs *logCapture, event, collectorID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range logs.events(t, event) {
		if rec["collector_id"] == collectorID {
			out = append(out, rec)
		}
	}
	return out
}

// TestAuditDisabledCollectorConnectionCut covers F-025 (connection part,
// COL-04, WP-381) on the deployed endpoint: PATCH /collectors/{id} to
// disabled on one replica cuts the collector's open connection within 5 s
// (the edge worker of another one receives the announcement over the
// internal NATS, finds the connection over CONNZ of the system account and
// KICKs it); its reconnects are refused, nothing it publishes reaches the
// internal NATS any more. Another collector of the organization stays
// connected and delivers.
func TestAuditDisabledCollectorConnectionCut(t *testing.T) {
	e, enc := auditEdge(t, "d3")
	f := e.f
	logs := captureLogs(t)
	s := startKickStack(t, e, enc, nil, 0)
	colID, colCert := e.enroll(t, "audit-disable")
	otherID, otherCert := e.enroll(t, "audit-disable-control")
	col, other := s.collector(t, colID, colCert), s.collector(t, otherID, otherCert)

	own := discovery.CollectorSubject(f.OrgA, colID) + ".ingest"
	ownSub, err := s.core.SubscribeSync(discovery.CollectorSubject(f.OrgA, colID) + ".>")
	if err != nil {
		t.Fatal(err)
	}
	otherSubject := discovery.CollectorSubject(f.OrgA, otherID) + ".ingest"
	otherSub, err := s.core.SubscribeSync(discovery.CollectorSubject(f.OrgA, otherID) + ".>")
	if err != nil {
		t.Fatal(err)
	}
	delivered(t, col.nc, ownSub, own, "before-disable")
	delivered(t, other.nc, otherSub, otherSubject, "control-before")

	disabledAt := time.Now()
	s.disable(t, f, f.OrgA, colID)
	select {
	case <-col.down:
	case <-time.After(5*time.Second - time.Since(disabledAt)):
		t.Fatalf("COL-04 violated: 5 s after disable the collector is still connected (status %v)", col.nc.Status())
	}
	t.Logf("connection cut %v after the PATCH", time.Since(disabledAt).Round(time.Millisecond))
	// The collector tries to reconnect; the callout refuses it.
	waitClosed(t, col, 10*time.Second, "disabled collector")
	if n := len(drain(col.up)); n != 0 {
		t.Fatalf("the disabled collector reconnected %d times", n)
	}
	for _, aerr := range drain(col.errs) {
		if !errors.Is(aerr, nats.ErrAuthorization) {
			t.Fatalf("the connection did not end by the cut: %v", aerr)
		}
	}
	_ = col.nc.Publish(own, []byte("after-disable"))
	if msg, nerr := ownSub.NextMsg(time.Second); nerr == nil {
		t.Fatalf("COL-04 violated: %q of the disabled collector reached the internal NATS", msg.Data)
	}
	if conn, derr := s.dial(colID, colCert); derr == nil {
		conn.Close()
		t.Fatal("the disabled collector connected again")
	}
	kicked := kickEvents(t, logs, "discovery.collector.kicked", colID)
	if len(kicked) != 1 || kicked[0]["connections"] != float64(1) {
		t.Fatalf("discovery.collector.kicked for the collector: %v", kicked)
	}
	if failed := kickEvents(t, logs, "discovery.collector.kick_failed", colID); len(failed) != 0 {
		t.Fatalf("kick failed: %v", failed)
	}

	// Control: the other collector was not cut and still delivers.
	select {
	case derr := <-other.down:
		t.Fatalf("the active collector was disconnected too: %v", derr)
	default:
	}
	if n := len(drain(other.up)); !other.nc.IsConnected() || n != 0 {
		t.Fatalf("the active collector lost its connection: %v, %d reconnects", other.nc.Status(), n)
	}
	delivered(t, other.nc, otherSub, otherSubject, "control-after")
}

// TestDisabledCollectorCutFallsBackToJWTExpiry covers the fallback of
// WP-381 (COL-04): the endpoint's edge.conf lacks the system login (an
// endpoint not yet recreated after the upgrade), so the cut fails, is logged
// as discovery.collector.kick_failed and repeated; with user JWTs of 2 s
// (UserValidity) the endpoint ends the disabled collector's connection when
// its JWT expires and the callout refuses the reconnect. Control: an active
// collector stays connected across the expiries without losing data: it
// reconnects, and like the spool it repeats a request until it is
// acknowledged; every one reaches the internal NATS.
func TestDisabledCollectorCutFallsBackToJWTExpiry(t *testing.T) {
	const validity = 2 * time.Second
	e, enc := auditEdge(t, "d4")
	f := e.f
	logs := captureLogs(t)
	// The edge.conf of the releases before WP-381.
	withoutSysLogin := func(conf string) string {
		old := regexp.MustCompile(`SYS \{ users: .* \}\n`).ReplaceAllString(conf, "SYS {}\n")
		old = strings.Replace(old, fmt.Sprintf(`, %q ]`, discovery.EdgeSysUser), ` ]`, 1)
		if strings.Contains(old, discovery.EdgeSysUser) || !strings.Contains(old, "SYS {}\n") {
			t.Fatalf("edge.conf still has the system login: %s", old)
		}
		return old
	}
	s := startKickStack(t, e, enc, withoutSysLogin, validity)
	colID, colCert := e.enroll(t, "fallback-disable")
	activeID, activeCert := e.enroll(t, "fallback-active")
	col, active := s.collector(t, colID, colCert), s.collector(t, activeID, activeCert)

	// The internal side acknowledges every request of the active collector.
	activeSubject := discovery.CollectorSubject(f.OrgA, activeID) + ".ingest"
	var mu sync.Mutex
	received := map[int]bool{}
	if _, err := s.core.Subscribe(activeSubject, func(m *nats.Msg) {
		n, _ := strconv.Atoi(string(m.Data))
		mu.Lock()
		received[n] = true
		mu.Unlock()
		_ = m.Respond(m.Data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.core.Flush(); err != nil {
		t.Fatal(err)
	}

	drain(col.errs)
	reconnectsBefore := len(drain(active.up))
	disabledAt := time.Now()
	s.disable(t, f, f.OrgA, colID)

	// Meanwhile the active collector delivers over three JWT lifetimes.
	sent := 0
	for time.Since(disabledAt) < 3*validity {
		sent++
		data := strconv.Itoa(sent)
		deadline := time.Now().Add(15 * time.Second)
		for {
			msg, rerr := active.nc.Request(activeSubject, []byte(data), time.Second)
			if rerr == nil && string(msg.Data) == data {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("request %d of the active collector not acknowledged: %v", sent, rerr)
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The disabled collector: its JWT expired, the reconnect was refused.
	waitClosed(t, col, 2*validity+5*time.Second-time.Since(disabledAt), "disabled collector after the JWT expiry")
	errs := drain(col.errs)
	if !slices.ContainsFunc(errs, func(err error) bool { return errors.Is(err, nats.ErrAuthExpired) }) {
		t.Fatalf("the disabled collector's connection did not end with the expiry of its JWT: %v", errs)
	}
	if conn, derr := s.dial(colID, colCert); derr == nil {
		conn.Close()
		t.Fatal("the disabled collector connected again")
	}
	failed := kickEvents(t, logs, "discovery.collector.kick_failed", colID)
	if len(failed) < 2 || failed[0]["retry"] != true {
		t.Fatalf("the failed cut was not logged as discovery.collector.kick_failed and repeated: %v", failed)
	}
	if kicked := kickEvents(t, logs, "discovery.collector.kicked", colID); len(kicked) != 0 {
		t.Fatalf("cut without the system login: %v", kicked)
	}

	// Control: the active collector reconnected after its JWTs expired and
	// lost nothing.
	n := reconnectsBefore + len(drain(active.up))
	if !active.nc.IsConnected() || n < 2 {
		t.Fatalf("active collector: %v after %d reconnects, want connected after at least 2", active.nc.Status(), n)
	}
	t.Logf("active collector: %d requests acknowledged across %d reconnects; %d failed cuts logged", sent, n, len(failed))
	mu.Lock()
	defer mu.Unlock()
	for n := 1; n <= sent; n++ {
		if !received[n] {
			t.Fatalf("request %d of %d of the active collector is missing on the internal NATS", n, sent)
		}
	}
}

// edgePKIFiles are the files ExportEdgePKI writes; nothing else, no
// temporary file, stays in the directory.
var edgePKIFiles = []string{"ca.pem", "edge.conf", "server-key.pem", "server.pem"}

// checkEdgePKIDir reports what is wrong with an exported PKI directory: a
// file other than edgePKIFiles, a server key pair nats-edge cannot load, a
// server certificate the collector CA bundle does not verify for localhost,
// or a key or edge.conf readable beyond 0640.
func checkEdgePKIDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, edgePKIFiles) {
		return fmt.Errorf("files %v, want %v", names, edgePKIFiles)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	if err != nil {
		return fmt.Errorf("key pair: %w", err)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(bundle)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("server certificate: %w", err)
	}
	for _, name := range []string{"server-key.pem", "edge.conf"} {
		st, serr := os.Stat(filepath.Join(dir, name))
		if serr != nil {
			return serr
		}
		if perm := st.Mode().Perm(); perm&^0o640 != 0 {
			return fmt.Errorf("%s has mode %o, want at most 0640", name, perm)
		}
	}
	return nil
}

// TestAuditConcurrentEdgePKIExportConsistent covers F-104 (WP-146, WP-429,
// COL-08): three server replicas sharing the database and the PKI volume
// (k8s prod overlay: 3 replicas, ReadWriteMany) export at the same time,
// 60 times. Every export succeeds and leaves exactly the four files: a
// server key pair nats-edge can load, from the collector CA, the key and
// edge.conf at most 0640, no temporary file.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditConcurrentEdgePKIExportConsistent' ./internal/discovery/
func TestAuditConcurrentEdgePKIExportConsistent(t *testing.T) {
	e, enc := auditEdge(t, "d2")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	// Three server replicas sharing the database and the PKI volume.
	replicas := []*discovery.CA{e.ca, discovery.NewCA(e.f.App, enc), discovery.NewCA(e.f.App, enc)}
	for _, ca := range replicas {
		if _, err = ca.TrustBundle(bg); err != nil {
			t.Fatal(err)
		}
	}
	const runs = 60
	var inconsistent, errored int
	var firstInconsistency, firstError string
	for i := range runs {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("pki-%d", i))
		var wg sync.WaitGroup
		errs := make([]error, len(replicas))
		for j, ca := range replicas {
			wg.Go(func() {
				errs[j] = discovery.ExportEdgePKI(bg, ca, secrets, dir, []string{"localhost", "127.0.0.1"}, time.Now())
			})
		}
		wg.Wait()
		if jerr := errors.Join(errs...); jerr != nil {
			errored++
			if firstError == "" {
				firstError = jerr.Error()
			}
		}
		if cerr := checkEdgePKIDir(dir); cerr != nil {
			inconsistent++
			if firstInconsistency == "" {
				firstInconsistency = cerr.Error()
			}
		}
	}
	if inconsistent > 0 {
		t.Errorf("%d of %d concurrent three-replica exports left a PKI directory nats-edge cannot use as is (first: %s)", inconsistent, runs, firstInconsistency)
	}
	if errored > 0 {
		t.Errorf("%d of %d concurrent three-replica exports failed (first: %s)", errored, runs, firstError)
	}
}

// TestEdgePKIExportKeepsOnlyMatchingPair covers the reuse of an existing
// server key pair (WP-429, F-104): a matching pair of the collector CA that
// is not due stays unchanged, a key an earlier version left readable by
// others is narrowed to 0640; a key of another issuance, a missing key, a
// pair of a foreign CA and a pair due for renewal are replaced by a new
// pair of the collector CA.
func TestEdgePKIExportKeepsOnlyMatchingPair(t *testing.T) {
	e, enc := auditEdge(t, "d2")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	hosts := []string{"localhost", "127.0.0.1"}
	dir := filepath.Join(t.TempDir(), "pki")
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	export := func(now time.Time) {
		t.Helper()
		if eerr := discovery.ExportEdgePKI(bg, e.ca, secrets, dir, hosts, now); eerr != nil {
			t.Fatal(eerr)
		}
		if cerr := checkEdgePKIDir(dir); cerr != nil {
			t.Fatal(cerr)
		}
	}
	read := func(path string) string {
		t.Helper()
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return string(data)
	}
	write := func(path, data string) {
		t.Helper()
		if werr := os.WriteFile(path, []byte(data), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	export(time.Now())
	cert, key := read(certPath), read(keyPath)

	// Positive: the matching pair of the CA, not due, stays as it is; the
	// files an earlier version wrote with 0644 are no longer readable by
	// others.
	for _, name := range []string{"server-key.pem", "edge.conf"} {
		if err = os.Chmod(filepath.Join(dir, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	export(time.Now())
	if read(certPath) != cert || read(keyPath) != key {
		t.Fatal("a matching server key pair of the collector CA that is not due was replaced")
	}

	// Negative: the key of another issuance next to the certificate.
	_, otherKey, err := e.ca.IssueServer(bg, hosts, discovery.EdgeServerCertValidity)
	if err != nil {
		t.Fatal(err)
	}
	write(keyPath, otherKey)
	export(time.Now())
	if read(certPath) == cert || read(keyPath) == otherKey || read(keyPath) == key {
		t.Fatal("a certificate with the key of another issuance was kept instead of replaced")
	}

	// Negative: no key.
	cert = read(certPath)
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	export(time.Now())
	if read(certPath) == cert {
		t.Fatal("a certificate without its key was kept")
	}

	// Negative: a matching pair of a foreign CA.
	foreignKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	foreignTmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "reticora-collector-nats"},
		DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(discovery.EdgeServerCertValidity),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	foreignDER, err := x509.CreateCertificate(rand.Reader, foreignTmpl, foreignTmpl, &foreignKey.PublicKey, foreignKey)
	if err != nil {
		t.Fatal(err)
	}
	foreignKeyDER, err := x509.MarshalPKCS8PrivateKey(foreignKey)
	if err != nil {
		t.Fatal(err)
	}
	foreignCert := string(pemOf(foreignDER))
	write(certPath, foreignCert)
	write(keyPath, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: foreignKeyDER})))
	if _, err = tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("control: the foreign pair matches: %v", err)
	}
	export(time.Now())
	if read(certPath) == foreignCert {
		t.Fatal("a server certificate of a foreign CA was kept")
	}

	// Negative: the pair of the CA is due for renewal (60 % of its
	// lifetime).
	cert = read(certPath)
	export(time.Now().Add(time.Duration(float64(discovery.EdgeServerCertValidity) * discovery.RenewAfter)))
	if read(certPath) == cert {
		t.Fatal("a server certificate due for renewal was kept")
	}
}

// edgePKILockClass is the class of the export's advisory lock
// (natsedge.go); TestEdgePKIExportLock proves the export takes it.
const edgePKILockClass int32 = 0x52504b49

// TestEdgePKIExportLock covers the lock of the export (WP-429, F-104): an
// export waits, holding one connection of the pool and writing nothing,
// while another replica holds the lock, and finishes once it is released.
// Afterwards, also after an export that failed or panicked, the lock is free
// and the export holds no connection of the pool.
func TestEdgePKIExportLock(t *testing.T) {
	e, enc := auditEdge(t, "d2")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.ca.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	hosts := []string{"localhost", "127.0.0.1"}
	released := func(after string) {
		t.Helper()
		var held int
		if qerr := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid::bigint = $1
			AND objid = 0 AND objsubid = 2`, int64(uint32(edgePKILockClass))).Scan(&held); qerr != nil {
			t.Fatal(qerr)
		}
		if held != 0 {
			t.Fatalf("%s: the lock of the export is still held", after)
		}
		for i := 0; e.f.App.Stat().AcquiredConns() != 0; i++ {
			if i == 50 {
				t.Fatalf("%s: the export still holds %d connections of the pool", after, e.f.App.Stat().AcquiredConns())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Another replica holds the lock.
	holder, err := e.f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err = holder.Exec(bg, `SELECT pg_advisory_lock($1, 0)`, edgePKILockClass); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pki")
	done := make(chan error, 1)
	go func() { done <- discovery.ExportEdgePKI(bg, e.ca, secrets, dir, hosts, time.Now()) }()
	select {
	case err = <-done:
		_, _ = holder.Exec(bg, `SELECT pg_advisory_unlock($1, 0)`, edgePKILockClass)
		t.Fatalf("the export did not wait for the lock another replica holds (%v)", err)
	case <-time.After(time.Second):
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the export wrote %s while another replica held the lock (%v)", dir, serr)
	}
	if n := e.f.App.Stat().AcquiredConns(); n != 1 {
		t.Errorf("the waiting export holds %d connections of the pool, want 1", n)
	}
	if _, err = holder.Exec(bg, `SELECT pg_advisory_unlock($1, 0)`, edgePKILockClass); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the export did not finish after the lock was released")
	}
	if err = checkEdgePKIDir(dir); err != nil {
		t.Fatal(err)
	}
	released("after an export")

	// An export that fails: its directory cannot be created below a file.
	file := filepath.Join(t.TempDir(), "file")
	if err = os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, filepath.Join(file, "pki"), hosts, time.Now()); err == nil {
		t.Fatal("control: an export below a file succeeded")
	}
	released("after a failed export")

	// An export that panics: without secrets edge.conf cannot be written.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("control: the export without secrets did not panic")
			}
		}()
		_ = discovery.ExportEdgePKI(bg, e.ca, nil, filepath.Join(t.TempDir(), "pki"), hosts, time.Now())
	}()
	released("after a panic in the export")
}

// TestAuditCARolloverKeepsEnrolledCollectorTrust covers F-102 (WP-145,
// WP-430, COL-04): after a CA roll-over the export keeps issuing the edge
// server certificate from the previous CA, so a collector enrolled before
// (its bundle holds only that CA) and one enrolled after it (both anchors)
// trust the endpoint; ca.pem holds both anchors, the certificate of the
// collector enrolled after it comes from the new CA. After the retirement
// the server certificate comes from the new CA and ca.pem holds only it.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditCARolloverKeepsEnrolledCollectorTrust' ./internal/discovery/
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
	anchors := func() int {
		raw, rerr := os.ReadFile(filepath.Join(dir, "ca.pem"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		return strings.Count(string(raw), "BEGIN CERTIFICATE")
	}
	if verr := verifyServer(enrolled.CABundlePEM); verr != nil {
		t.Fatalf("control before roll-over: %v", verr)
	}
	if _, err = e.ca.Rollover(bg, discovery.CAChangeOptions{}); err != nil {
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
	if n := anchors(); n != 2 {
		t.Errorf("ca.pem holds %d trust anchors during the roll-over, want 2", n)
	}
	// The certificate issued after the roll-over comes from the new CA: the
	// bundle the collector enrolled before holds does not verify it.
	if verifies(parseCert(t, fresh.CertificatePEM), enrolled.CABundlePEM) || !verifies(parseCert(t, fresh.CertificatePEM), fresh.CABundlePEM) {
		t.Error("the collector certificate issued after the roll-over is not from the new CA")
	}

	// After the retirement (forced: the collector enrolled before still holds
	// a certificate of the previous CA) the server certificate comes from the
	// new CA.
	if _, err = e.ca.RetirePrevious(bg, discovery.CAChangeOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if verr := verifyServer(fresh.CABundlePEM); verr != nil {
		t.Errorf("after the retirement the collector enrolled during the roll-over does not trust the edge: %v", verr)
	}
	if verr := verifyServer(string(pemOf(parseCert(t, fresh.CABundlePEM).Raw))); verr != nil {
		t.Errorf("after the retirement the server certificate is not from the new CA: %v", verr)
	}
	if n := anchors(); n != 1 {
		t.Errorf("ca.pem holds %d trust anchors after the retirement, want 1", n)
	}
}

// collectorCAServerBinary builds backend/cmd/server for the command
// server collector-ca.
func collectorCAServerBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "server")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/server: %v\n%s", err, out)
	}
	return bin
}

// TestCollectorCARolloverAcrossReplicas covers F-102 end to end (WP-430,
// COL-04): two server instances, each with its own CA and edge worker on the
// same database and PKI volume, serve the deployed endpoint (real
// nats-server) and the renewal; server collector-ca runs as a third process.
// Nothing is restarted.
//   - rollover: both instances reload on the announcement, before any use,
//     and sign by the new CA; ca.pem holds both anchors, the server
//     certificate stays the one of the previous CA. A collector enrolled
//     before (its bundle holds only the previous CA) connects anew; one
//     renewed after the roll-over connects with its certificate of the new
//     CA, which the endpoint trusts since the command had it reload ca.pem.
//   - retire: refused with the number of collectors on the previous CA,
//     collector_ca unchanged; with --force it retires the previous CA,
//     audited with that number. The endpoint, reloaded, serves a server
//     certificate of the new CA and trusts only it: the renewed collector
//     connects anew with its bundle, the one enrolled before no longer.
//   - A collector connected like the collector binary (reconnecting) is
//     connected again after each reload and delivers to the internal NATS;
//     the reload re-reads the accounts of nats-edge, which closes the
//     connections the callout admitted.
//   - A collector cannot publish the announcement (callout permissions).
//
// It needs TEST_NATS_SERVER_BIN and builds cmd/server.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<path to nats-server 2.10+> go test -count=1 -v -p 1 \
//	  -run 'TestCollectorCARolloverAcrossReplicas' ./internal/discovery/
func TestCollectorCARolloverAcrossReplicas(t *testing.T) {
	// The database first: the unit job (no TEST_DATABASE_URL, no
	// nats-server) skips here instead of failing on the missing server.
	f := scopetest.Seed(t, "c5")
	natsBin := edgeNATSServerBin(t)
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	serverBin := collectorCAServerBinary(t)
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	instA, instB := discovery.NewCA(f.App, enc), discovery.NewCA(f.App, enc)
	e := &edgeFixture{f: f, ca: instA, repo: discovery.NewPGRepository(f.App)}
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}

	// The deployed endpoint with the PKI volume of both instances.
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	hosts := []string{"localhost", "127.0.0.1"}
	if err = discovery.ExportEdgePKI(bg, instA, secrets, pki, hosts, time.Now()); err != nil {
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
	startNATSServer(t, natsBin, dir, nil, "-c", "core.conf")
	startNATSServer(t, natsBin, dir, []string{fmt.Sprintf("RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:%d", leafPort)},
		"-c", "nats-edge.conf", "-p", fmt.Sprint(edgePort))
	coreURL, edgeURL := fmt.Sprintf("nats://127.0.0.1:%d", corePort), fmt.Sprintf("tls://127.0.0.1:%d", edgePort)
	dialCore := func() *nats.Conn {
		t.Helper()
		var nc *nats.Conn
		var cerr error
		for range 50 {
			if nc, cerr = nats.Connect(coreURL, nats.NoReconnect()); cerr == nil {
				t.Cleanup(nc.Close)
				return nc
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("internal NATS: %v", cerr)
		return nil
	}
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	for _, inst := range []*discovery.CA{instA, instB} {
		go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: f.App, CA: inst, Secrets: secrets, PKIDir: pki, Hosts: hosts,
			URL: edgeURL, Bus: dialCore()})
	}
	renewal := discovery.NewCertificateRenewal(f.App, instB)
	go func() { _ = renewal.Serve(ctx, dialCore()) }()
	core := dialCore()
	announced, err := core.SubscribeSync(discovery.CollectorCAChangedSubject)
	if err != nil {
		t.Fatal(err)
	}

	// A collector reaches the endpoint with its certificate and the CA bundle
	// it received, as the collector binary does.
	dial := func(id string, cert tls.Certificate, bundle string) (*nats.Conn, error) {
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(bundle))
		return nats.Connect(edgeURL, nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}),
			nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(id)), nats.NoReconnect())
	}
	// stay keeps a connection like the collector binary: it reconnects
	// forever (the reload of the endpoint re-reads its accounts and closes
	// the connections the callout admitted; they connect again at once).
	stay := func(id string, cert tls.Certificate, bundle string) *edgeCollector {
		t.Helper()
		c := &edgeCollector{down: make(chan error, 64), up: make(chan struct{}, 64), errs: make(chan error, 64)}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(bundle))
		var cerr error
		for range 50 {
			c.nc, cerr = nats.Connect(edgeURL, nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}),
				nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(id)),
				nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond), nats.ReconnectJitter(0, 0), nats.IgnoreAuthErrorAbort(),
				nats.DisconnectErrHandler(func(_ *nats.Conn, derr error) {
					select {
					case c.down <- derr:
					default:
					}
				}),
				nats.ReconnectHandler(func(*nats.Conn) {
					select {
					case c.up <- struct{}{}:
					default:
					}
				}),
				nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, herr error) {
					select {
					case c.errs <- herr:
					default:
					}
				}))
			if cerr == nil {
				t.Cleanup(c.nc.Close)
				return c
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("collector %s cannot connect to the endpoint: %v", id, cerr)
		return nil
	}
	oldID, oldKey, oldCert := auditEnroll(t, e, "rollover-old")
	renewID, renewKey, renewCert := auditEnrollWith(t, e, agedCA(e, enc, 55), f.OrgA, "rollover-renew")
	staying := stay(renewID, clientIdentity(t, renewKey, renewCert), renewCert.CABundlePEM)
	ncRenew, violations := staying.nc, staying.errs
	ownSubject := discovery.CollectorSubject(f.OrgA, renewID) + ".ingest"
	ownSub, err := core.SubscribeSync(discovery.CollectorSubject(f.OrgA, renewID) + ".>")
	if err != nil {
		t.Fatal(err)
	}
	delivered(t, ncRenew, ownSub, ownSubject, "before-rollover")
	servedBefore, err := os.ReadFile(filepath.Join(pki, "server.pem"))
	if err != nil {
		t.Fatal(err)
	}

	// A collector cannot announce a change of the CA.
	forged, err := discovery.CAChangeMessage(instA, "forged", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = ncRenew.Publish(discovery.CollectorCAChangedSubject, forged)
	_ = ncRenew.Flush()
	select {
	case verr := <-violations:
		if !strings.Contains(strings.ToLower(verr.Error()), "permissions violation for publish") {
			t.Fatalf("announcement by a collector: %v", verr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a collector published on " + discovery.CollectorCAChangedSubject)
	}
	if msg, nerr := announced.NextMsg(time.Second); nerr == nil {
		t.Fatalf("the announcement of a collector reached the internal NATS: %s", msg.Data)
	}

	// The third process: server collector-ca with the server's configuration.
	env := []string{
		"RETICORA_DATABASE_URL=" + scopetest.AppDSN(t),
		"RETICORA_MASTER_KEY=" + masterKey,
		"RETICORA_PKI_DIR=" + pki,
		"RETICORA_NATS_EDGE_URL=" + edgeURL,
		"RETICORA_NATS_EDGE_HOSTS=" + strings.Join(hosts, ","),
		"RETICORA_NATS_URL=" + coreURL,
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RETICORA_") {
			env = append(env, kv)
		}
	}
	cli := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(serverBin, append([]string{"collector-ca"}, args...)...)
		cmd.Env = env
		out, cerr := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		switch {
		case cerr == nil:
			return 0, string(out)
		case errors.As(cerr, &exitErr):
			return exitErr.ExitCode(), string(out)
		}
		t.Fatalf("server collector-ca %v: %v", args, cerr)
		return 0, ""
	}
	audits := func(action, active string) []map[string]any {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT operator_kind, details FROM operator_audit WHERE action = $1 AND details->>'active' = $2 ORDER BY id`, action, active)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var kind string
			var details map[string]any
			if serr := rows.Scan(&kind, &details); serr != nil {
				t.Fatal(serr)
			}
			details["operator_kind"] = kind
			out = append(out, details)
		}
		return out
	}
	caCert := func(state string) []*x509.Certificate {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT cert_pem FROM collector_ca WHERE state = $1 ORDER BY created_at`, state)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []*x509.Certificate
		for rows.Next() {
			var p string
			if serr := rows.Scan(&p); serr != nil {
				t.Fatal(serr)
			}
			out = append(out, parseCert(t, p))
		}
		return out
	}
	fingerprint := func(c *x509.Certificate) string {
		sum := sha256.Sum256(c.Raw)
		return hex.EncodeToString(sum[:])
	}
	anchors := func() int {
		t.Helper()
		raw, rerr := os.ReadFile(filepath.Join(pki, "ca.pem"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		return strings.Count(string(raw), "BEGIN CERTIFICATE")
	}
	previousCA := parseCert(t, oldCert.CABundlePEM)

	// Roll-over.
	if code, out := cli("rollover"); code != 0 || !strings.Contains(out, "reloaded") {
		t.Fatalf("server collector-ca rollover: exit %d\n%s", code, out)
	}
	active := caCert("active")
	if len(active) != 1 || len(caCert("previous")) != 1 || fingerprint(active[0]) == fingerprint(previousCA) {
		t.Fatal("collector_ca after the roll-over: want a new active and the former one as previous")
	}
	newCA, newFP := active[0], fingerprint(active[0])
	if rolled := audits("collector_ca.rolled_over", newFP); len(rolled) != 1 || rolled[0]["edge_reloaded"] != true ||
		rolled[0]["operator_kind"] != "break_glass" {
		t.Fatalf("audit of the roll-over: %v", rolled)
	}
	if msg, nerr := announced.NextMsg(5 * time.Second); nerr != nil || discovery.VerifyCAChange(instA, msg.Data, time.Now()) != nil {
		t.Fatalf("the roll-over was not announced: %v", nerr)
	}
	// Both instances reload on the announcement, before any signature or
	// callout would check the version.
	deadline := time.Now().Add(10 * time.Second)
	for discovery.LoadedCAFingerprint(instA) != newFP || discovery.LoadedCAFingerprint(instB) != newFP {
		if time.Now().After(deadline) {
			t.Fatal("the instances did not reload the CA state on the announcement")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for name, inst := range map[string]*discovery.CA{"A": instA, "B": instB} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		issued, serr := inst.Sign(bg, csrFor(t, oldID, key), f.OrgA, oldID)
		if serr != nil || parseCert(t, issued.CertificatePEM).CheckSignatureFrom(newCA) != nil {
			t.Fatalf("instance %s does not sign by the new CA after the roll-over (%v)", name, serr)
		}
	}
	served, err := os.ReadFile(filepath.Join(pki, "server.pem"))
	if err != nil || string(served) != string(servedBefore) || anchors() != 2 {
		t.Fatalf("during the roll-over ca.pem holds %d anchors (want 2) and the server certificate changed: %v", anchors(), string(served) != string(servedBefore))
	}

	// The collector enrolled before connects anew with its bundle.
	nc, err := dial(oldID, clientIdentity(t, oldKey, oldCert), oldCert.CABundlePEM)
	if err != nil {
		t.Fatalf("COL-04: a collector enrolled before the roll-over cannot connect: %v", err)
	}
	nc.Close()
	// The connected collector is connected again after the reload and
	// delivers.
	delivered(t, ncRenew, ownSub, ownSubject, "after-rollover")
	t.Logf("connected collector across the reload of the roll-over: disconnects %v, reconnects %d",
		drain(staying.down), len(drain(staying.up)))
	// It renews: a certificate of the new CA with both anchors. Like the
	// collector binary it starts a new session with it.
	subject := renewalSubject(f.OrgA, renewID)
	renewedKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	resp := askRenewal(t, ncRenew, subject, renewalRequest(t, subject, subjectCSR(t, renewID, f.OrgA, renewedKey), renewCert.CertificatePEM, time.Now(), renewKey))
	if resp.Error != "" || resp.IssuedCert == nil || parseCert(t, resp.CertificatePEM).CheckSignatureFrom(newCA) != nil ||
		strings.Count(resp.CABundlePEM, "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("renewal after the roll-over: %+v", resp)
	}
	renewed := clientIdentity(t, renewedKey, resp.IssuedCert)
	staying.nc.Close()
	renewedSession := stay(renewID, renewed, resp.CABundlePEM)
	delivered(t, renewedSession.nc, ownSub, ownSubject, "renewed")

	// Retirement: refused while the collector enrolled before is on the
	// previous CA, then forced.
	if code, out := cli("retire"); code != 1 || !strings.Contains(out, "1 active collectors") {
		t.Fatalf("server collector-ca retire with a collector on the previous CA: exit %d\n%s", code, out)
	}
	if len(caCert("previous")) != 1 || len(audits("collector_ca.retired", newFP)) != 0 {
		t.Fatal("a refused retirement changed collector_ca or was audited")
	}
	if code, out := cli("retire", "--force"); code != 0 || !strings.Contains(out, "reloaded") {
		t.Fatalf("server collector-ca retire --force: exit %d\n%s", code, out)
	}
	retired := audits("collector_ca.retired", newFP)
	if len(retired) != 1 || retired[0]["force"] != true || retired[0]["overridden_collectors"] != float64(1) ||
		fmt.Sprint(retired[0]["retired"]) != fmt.Sprint([]any{fingerprint(previousCA)}) || retired[0]["edge_reloaded"] != true {
		t.Fatalf("audit of the forced retirement: %v", retired)
	}
	if len(caCert("previous")) != 0 || anchors() != 1 {
		t.Fatalf("after the retirement: %d previous CAs, ca.pem with %d anchors", len(caCert("previous")), anchors())
	}
	if msg, nerr := announced.NextMsg(5 * time.Second); nerr != nil || discovery.VerifyCAChange(instB, msg.Data, time.Now()) != nil {
		t.Fatalf("the retirement was not announced: %v", nerr)
	}
	if served, err = os.ReadFile(filepath.Join(pki, "server.pem")); err != nil || parseCert(t, string(served)).CheckSignatureFrom(newCA) != nil {
		t.Fatalf("after the retirement the server certificate is not by the new CA (%v)", err)
	}
	// The endpoint, reloaded and not restarted, serves the new server
	// certificate: the renewed collector's session is connected again and
	// delivers, a new connection verifies the server certificate of the new
	// CA with its bundle.
	delivered(t, renewedSession.nc, ownSub, ownSubject, "after-retire")
	t.Logf("renewed collector across the reload of the retirement: disconnects %v, reconnects %d",
		drain(renewedSession.down), len(drain(renewedSession.up)))
	if nc, err = dial(renewID, renewed, resp.CABundlePEM); err != nil {
		t.Fatalf("COL-04: after the retirement the renewed collector cannot connect: %v", err)
	}
	state, err := nc.TLSConnectionState()
	if err != nil || state.PeerCertificates[0].CheckSignatureFrom(newCA) != nil {
		t.Fatalf("the endpoint serves no server certificate of the new CA after the retirement (%v)", err)
	}
	nc.Close()
	if nc, err = dial(oldID, clientIdentity(t, oldKey, oldCert), oldCert.CABundlePEM); err == nil {
		nc.Close()
		t.Fatal("after the forced retirement a collector on the retired CA still connects")
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
