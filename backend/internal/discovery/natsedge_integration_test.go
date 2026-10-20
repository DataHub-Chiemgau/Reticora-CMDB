package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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
