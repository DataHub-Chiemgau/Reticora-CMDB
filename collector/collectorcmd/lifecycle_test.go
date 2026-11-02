// Certificate life cycle of the collector against a real nats-server
// (TEST_NATS_SERVER_BIN, v2.10+ for handshake_first and the authorization
// callout), WP-414 (COL-04, F-026), and its unattended enrollment from
// RETICORA_ENROLL_TOKEN and RETICORA_CLOUD_URL, WP-493 (COL-04, F-101; no
// nats-server needed).
//
// The test starts the real collector entry point Main() in a child process
// (lifecycle_helpers_test.go) and plays the platform: the endpoint runs
// like nats-edge (mTLS, account COLLECTORS, authorization callout); the
// callout admits the collector's certificate, and after the first
// connection with a renewed certificate it refuses its predecessors (like
// the server, natsedge.go). A platform client answers the collector's
// subjects: the renewal checked as the server does (WP-375), the credential
// bundle sealed for the key of the presented certificate, the edge ingest.
//
// Retest:
//
//	cd collector && TEST_NATS_SERVER_BIN=<path to nats-server> go test -count=1 -v \
//	  -run 'TestAuditCollectorRenewsCertificateAt60Percent' ./collectorcmd/
//	cd collector && go test -count=1 -v -run 'TestAuditCollectorEnrollsFromEnvironment|TestCollectorEnrollFromEnvironment' ./collectorcmd/
package collectorcmd

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// sealTestBundle seals content for the collector key pub like the server
// (backend/internal/credential.SealBundle).
func sealTestBundle(pub *ecdsa.PublicKey, collectorID string, content *bundleContent) (*sealedBundle, error) {
	recipient, err := pub.ECDH()
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, eph.PublicKey().Bytes(), "reticora/credential-bundle/v1|"+collectorID, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return &sealedBundle{Version: 1, CollectorID: collectorID, EphemeralKey: eph.PublicKey().Bytes(), Nonce: nonce,
		Ciphertext: gcm.Seal(nil, nonce, plain, []byte("reticora-credential-bundle|"+collectorID))}, nil
}

// lifecyclePlatform is the platform side of the test: the authorization
// callout of the endpoint and the services on the collector's subjects.
type lifecyclePlatform struct {
	t            *testing.T
	ca           *auditPKI
	orgID, colID string
	sink         *auditCSRSink

	mu sync.Mutex
	// admitted are the serials of the collector certificates the callout
	// admitted, in order; refusedOld counts refused predecessors.
	admitted   []string
	refusedOld int
	// issued is the serial of the renewed certificate; renewedIn is set
	// once the callout admitted it.
	issued    string
	renewedIn bool
	// bundles are the serials of the certificates the bundle requests
	// carried after renewedIn, bundlesBefore those before.
	bundles, bundlesBefore []string
	// ingests counts the edge ingests accepted after renewedIn.
	ingests  int
	subjects map[string]bool
}

const platformCN = "audit-platform"

// authorize answers one request of the authorization callout.
func (p *lifecyclePlatform) authorize(issuer nkeys.KeyPair, m *nats.Msg) {
	req, err := jwt.DecodeAuthorizationRequestClaims(string(m.Data))
	if err != nil {
		p.t.Errorf("authorization request: %v", err)
		return
	}
	resp := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	resp.Audience = req.Server.ID
	var certs []string
	if req.TLS != nil {
		certs = req.TLS.Certs
		if len(certs) == 0 && len(req.TLS.VerifiedChains) > 0 {
			certs = req.TLS.VerifiedChains[0]
		}
	}
	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Audience = "COLLECTORS"
	uc.Expires = time.Now().Add(time.Hour).Unix()
	switch p.admit(certs) {
	case "platform":
		uc.Pub.Allow.Add(">")
		uc.Sub.Allow.Add(">")
	case "collector":
		// The collector's subject tree and its own inbox (natsedge.go).
		tree := "collector." + p.orgID + "." + p.colID + ".>"
		uc.Pub.Allow.Add(tree)
		uc.Sub.Allow.Add(tree, "_INBOX."+p.colID+".>")
	default:
		resp.Error = "not authorized"
	}
	if resp.Error == "" {
		if resp.Jwt, err = uc.Encode(issuer); err != nil {
			p.t.Errorf("user JWT: %v", err)
			return
		}
	}
	token, err := resp.Encode(issuer)
	if err != nil {
		p.t.Errorf("authorization response: %v", err)
		return
	}
	_ = m.Respond([]byte(token))
}

// admit decides on the presented certificates: the platform, the
// collector, or "" (refused).
func (p *lifecyclePlatform) admit(certs []string) string {
	if len(certs) == 0 {
		return ""
	}
	leaf, err := leafCertificate(certs[0])
	if err != nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case leaf.Subject.CommonName == platformCN:
		return "platform"
	case leaf.Subject.CommonName == p.colID && len(leaf.Subject.Organization) == 1 && leaf.Subject.Organization[0] == p.orgID:
		serial := leaf.SerialNumber.Text(16)
		if p.issued != "" && serial == p.issued {
			p.renewedIn = true
		}
		// The first connection with the renewed certificate revokes its
		// predecessors.
		if p.renewedIn && serial != p.issued {
			p.refusedOld++
			return ""
		}
		p.admitted = append(p.admitted, serial)
		return "collector"
	}
	return ""
}

// serve answers the collector's requests below its subject tree.
func (p *lifecyclePlatform) serve(m *nats.Msg) {
	p.sink.check("NATS "+m.Subject, m.Data)
	p.mu.Lock()
	p.subjects[m.Subject] = true
	p.mu.Unlock()
	tree := "collector." + p.orgID + "." + p.colID
	var reply any
	switch strings.TrimPrefix(m.Subject, tree) {
	case ".certificate.renew":
		reply = p.renew(m.Subject, m.Data)
	case ".credentials.request":
		reply = p.bundle(m.Data)
	case ".ingest":
		reply = p.ingest()
	case ".heartbeat":
		reply = map[string]any{"status": http.StatusOK}
	case ".subnets.report":
		reply = map[string]any{"suggested": 0}
	default:
		reply = map[string]string{"error": "not served by this test"}
	}
	if m.Reply == "" {
		return
	}
	raw, err := json.Marshal(reply)
	if err == nil {
		err = m.Respond(raw)
	}
	if err != nil {
		p.t.Errorf("reply on %s: %v", m.Subject, err)
	}
}

// renew issues the successor like the server: 90 days, CN and O of the
// collector, answer with certificate_pem and ca_bundle_pem.
func (p *lifecyclePlatform) renew(subject string, data []byte) any {
	now := time.Now()
	csr, _, code := checkRenewalRequest(p.ca, subject, data, now)
	if code != "" {
		p.t.Errorf("renewal refused: %s", code)
		return map[string]string{"error": code}
	}
	cert := p.ca.issue(p.t, csr.PublicKey, pkix.Name{CommonName: p.colID, Organization: []string{p.orgID}}, now.Add(-time.Minute), now.Add(90*24*time.Hour), false)
	leaf, err := leafCertificate(cert)
	if err != nil {
		p.t.Errorf("renewed certificate: %v", err)
		return map[string]string{"error": "renewal_unavailable"}
	}
	p.mu.Lock()
	p.issued = leaf.SerialNumber.Text(16)
	p.mu.Unlock()
	return map[string]any{"certificate_pem": cert, "ca_bundle_pem": p.ca.pem, "serial": leaf.SerialNumber.Text(16), "not_after": leaf.NotAfter}
}

// bundle seals the collector's credentials for the key of the presented
// certificate (COL-02).
func (p *lifecyclePlatform) bundle(data []byte) any {
	var req struct {
		CertificatePEM string `json:"certificate_pem"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return map[string]string{"error": "invalid_request"}
	}
	leaf, err := leafCertificate(req.CertificatePEM)
	if err != nil {
		return map[string]string{"error": "not_authorized"}
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return map[string]string{"error": "not_authorized"}
	}
	p.mu.Lock()
	if p.renewedIn {
		p.bundles = append(p.bundles, leaf.SerialNumber.Text(16))
	} else {
		p.bundlesBefore = append(p.bundlesBefore, leaf.SerialNumber.Text(16))
	}
	p.mu.Unlock()
	sealed, err := sealTestBundle(pub, p.colID, &bundleContent{Credentials: []bundleCredential{
		{ID: "6f0b6f0e-0000-4000-8000-0000000000b9", Name: "lab", Kind: "snmp_v2c", Secret: map[string]any{"community": "lifecycle"}},
	}})
	if err != nil {
		p.t.Errorf("seal bundle: %v", err)
		return map[string]string{"error": "unavailable"}
	}
	return map[string]any{"bundle": sealed}
}

// ingest accepts the edge ingests once the collector is connected with its
// renewed certificate; before, it answers 503 and the collector spools
// them, so a delivery proves the edge upload of the new session.
func (p *lifecyclePlatform) ingest() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.renewedIn {
		return map[string]any{"status": http.StatusServiceUnavailable, "error": "accepted after the renewal only (test)"}
	}
	p.ingests++
	return map[string]any{"status": http.StatusAccepted}
}

func (p *lifecyclePlatform) state() (renewedIn bool, bundles, ingests int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renewedIn, len(p.bundles), p.ingests
}

// TestAuditCollectorRenewsCertificateAt60Percent covers WP-414 (COL-04,
// F-026): the enrolled collector holds a certificate issued 55 days ago for
// 90 days (61 % of its lifetime). At start it renews it over its mTLS NATS
// channel (not over HTTP), stores the new pair in the keystore (0600) and
// restarts its NATS session with it: the callout sees the serial of the
// renewed certificate, the next bundle request carries it and the bundle
// opens with the new key ("credential bundle updated"), and an ingest is
// delivered over the edge of the new session. Without the session restart
// the test is red.
func TestAuditCollectorRenewsCertificateAt60Percent(t *testing.T) {
	bin := natsServerBin(t)
	dir := t.TempDir()
	ca := newAuditPKI(t)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	write("ca.pem", ca.pem)
	write("server.pem", ca.issue(t, &serverKey.PublicKey, pkix.Name{CommonName: "127.0.0.1"}, now.Add(-time.Hour), now.Add(24*time.Hour), true))
	write("server-key.pem", auditKeyPEM(t, serverKey))
	issuer, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	issuerKey, _ := issuer.PublicKey()
	port := auditFreeTCPPort(t)
	// The endpoint as nats-edge.conf and edge.conf configure it
	// (backend/internal/discovery/natsedge.go: edgeConf).
	write("nats.conf", fmt.Sprintf(`listen: "127.0.0.1:%d"
tls {
  cert_file: "server.pem"
  key_file: "server-key.pem"
  ca_file: "ca.pem"
  verify: true
  handshake_first: true
  timeout: 5
}
accounts {
  AUTH { users: [ { user: "audit-auth", password: "audit-auth" } ] }
  COLLECTORS {}
}
authorization {
  auth_callout {
    issuer: %q
    auth_users: [ "audit-auth" ]
    account: AUTH
  }
}
`, port, issuerKey))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "-c", "nats.conf")
	srv.Dir = dir
	if err = srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = srv.Wait() }()

	// The enrolled collector: certificate issued 55 days ago for 90 days
	// (61 % of its lifetime).
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000b1", "6f0b6f0e-0000-4000-8000-0000000000b2"
	colKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notBefore := now.Add(-55 * 24 * time.Hour)
	colCert := ca.issue(t, &colKey.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, notBefore, notBefore.Add(90*24*time.Hour), false)
	oldLeaf, err := leafCertificate(colCert)
	if err != nil {
		t.Fatal(err)
	}
	oldSerial := oldLeaf.SerialNumber.Text(16)
	credPath := filepath.Join(dir, "credentials.json")
	if err = (&keystore.FileStore{Path: credPath}).Save(context.Background(), keystore.Credentials{
		DeviceID: colID, ClientCertificatePEM: colCert, ClientPrivateKeyPEM: auditKeyPEM(t, colKey), CertificateAuthority: ca.pem,
	}); err != nil {
		t.Fatal(err)
	}

	sink := &auditCSRSink{}
	// The HTTP API: no renewal there (F-012); ingests and heartbeats are
	// refused so that the records stay in the spool until the edge takes
	// them.
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sink.check("HTTP "+r.Method+" "+r.URL.Path, body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer httpSrv.Close()

	platform := &lifecyclePlatform{t: t, ca: ca, orgID: orgID, colID: colID, sink: sink, subjects: map[string]bool{}}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	url := fmt.Sprintf("tls://127.0.0.1:%d", port)
	connect := func(cn string, opts ...nats.Option) *nats.Conn {
		t.Helper()
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		pair, perr := tls.X509KeyPair([]byte(ca.issue(t, &key.PublicKey, pkix.Name{CommonName: cn}, now.Add(-time.Hour), now.Add(time.Hour), false)),
			[]byte(auditKeyPEM(t, key)))
		if perr != nil {
			t.Fatal(perr)
		}
		opts = append([]nats.Option{nats.Secure(&tls.Config{RootCAs: roots, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}),
			nats.TLSHandshakeFirst(), nats.Timeout(2 * time.Second)}, opts...)
		var nc *nats.Conn
		var cerr error
		for range 50 {
			if nc, cerr = nats.Connect(url, opts...); cerr == nil {
				return nc
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s did not connect to the nats-server: %v", cn, cerr)
		return nil
	}
	// The authorization callout, then the platform (admitted by it).
	auth := connect("audit-auth", nats.UserInfo("audit-auth", "audit-auth"))
	defer auth.Close()
	if _, err = auth.Subscribe("$SYS.REQ.USER.AUTH", func(m *nats.Msg) { platform.authorize(issuer, m) }); err != nil {
		t.Fatal(err)
	}
	if err = auth.Flush(); err != nil {
		t.Fatal(err)
	}
	plat := connect(platformCN)
	defer plat.Close()
	if _, err = plat.Subscribe("collector.>", platform.serve); err != nil {
		t.Fatal(err)
	}
	if err = plat.Flush(); err != nil {
		t.Fatal(err)
	}

	running, stop := auditStartCollector(t, map[string]string{
		"RETICORA_SERVER_URL":           httpSrv.URL,
		"RETICORA_NATS_URL":             url,
		"RETICORA_CREDENTIALS_PATH":     credPath,
		"RETICORA_SPOOL_DIR":            filepath.Join(dir, "spool"),
		"RETICORA_SPOOL_FLUSH_INTERVAL": "1s",
		"RETICORA_AGENT_RELAY_ADDR":     "127.0.0.1:0",
		"RETICORA_SIMULATE":             "true",
		"RETICORA_ENVIRONMENT":          "development",
	})
	// newSession is the collector's log after the restart of its NATS
	// session; a reply can reach the collector there only over the new
	// connection.
	newSession := func(out string) string {
		_, after, _ := strings.Cut(out, `"msg":"collector NATS session restarts with the renewed certificate"`)
		return after
	}
	// delivered: the edge of the new session took records, from the spool
	// or of a discovery run (the HTTP API refuses them).
	delivered := func(out string) bool {
		session := newSession(out)
		return strings.Contains(session, `"msg":"spooled discovery results delivered"`) || strings.Contains(session, `"msg":"discovery records uploaded"`)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if renewedIn, bundles, ingests := platform.state(); renewedIn && bundles > 0 && ingests > 0 && delivered(running()) {
			break
		}
	}
	output := stop()

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.admitted) == 0 || platform.admitted[0] != oldSerial {
		t.Fatalf("control: the collector did not connect with its enrolled certificate (admitted serials %v, want %s first); output:\n%s",
			platform.admitted, oldSerial, auditTail(output, 15))
	}
	csrs := sink.all()
	renewSubject := "NATS collector." + orgID + "." + colID + ".certificate.renew"
	overNATS := false
	for _, where := range csrs {
		if where == renewSubject {
			overNATS = true
		}
		if strings.HasPrefix(where, "HTTP ") {
			t.Errorf("the collector sent a certificate signing request over the API (%s); renewals go over the NATS channel (F-012)", where)
		}
	}
	if !overNATS || platform.issued == "" {
		t.Fatalf("COL-04 violated: the certificate is at 61 %% of its 90-day lifetime (renewal due from 60 %%), but within 30 s the collector sent no "+
			"renewal on %s (certificate signing requests seen: %v); at day 90 it loses its connection. Collector log tail:\n%s",
			strings.TrimPrefix(renewSubject, "NATS "), csrs, auditTail(output, 15))
	}

	// The keystore holds the renewed certificate with a new key (0600).
	creds, err := (&keystore.FileStore{Path: credPath}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := leafCertificate(creds.ClientCertificatePEM)
	if err != nil || stored.SerialNumber.Text(16) != platform.issued {
		t.Fatalf("the keystore does not hold the renewed certificate %s (%v)", platform.issued, err)
	}
	if _, err = tls.X509KeyPair([]byte(creds.ClientCertificatePEM), []byte(creds.ClientPrivateKeyPEM)); err != nil ||
		creds.ClientPrivateKeyPEM == auditKeyPEM(t, colKey) || creds.CertificateAuthority != ca.pem {
		t.Fatalf("the keystore does not pair the renewed certificate with a new key and the CA bundle (%v)", err)
	}
	if st, serr := os.Stat(credPath); serr != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("keystore mode %v, %v; want 0600", st.Mode().Perm(), serr)
	}

	// The session restarted with the renewed certificate.
	if !platform.renewedIn {
		t.Fatalf("the collector did not restart its NATS session with the renewed certificate: the callout never saw serial %s "+
			"(admitted %v); the session keeps the old certificate until it is superseded or expires. Collector log tail:\n%s",
			platform.issued, platform.admitted, auditTail(output, 15))
	}
	if len(platform.bundles) == 0 {
		t.Errorf("no bundle request after the session restart (before: %v)", platform.bundlesBefore)
	}
	for _, serial := range platform.bundles {
		if serial != platform.issued {
			t.Errorf("a bundle request of the new session carried the certificate %s, want the renewed %s", serial, platform.issued)
		}
	}
	if !strings.Contains(newSession(output), `"msg":"credential bundle updated"`) {
		t.Errorf("the bundle of the new session did not open with the new key (no \"credential bundle updated\" after the restart). Collector log tail:\n%s",
			auditTail(output, 15))
	}
	// The platform accepts ingests only after the renewed certificate was
	// admitted; the records reach it over the new session.
	if platform.ingests == 0 || !delivered(output) {
		t.Errorf("no ingest delivered over the edge of the new session (%d accepted). Collector log tail:\n%s", platform.ingests, auditTail(output, 15))
	}
}

// TestAuditCollectorEnrollsFromEnvironment covers WP-493 (COL-04, F-101):
// a first start with RETICORA_ENROLL_TOKEN and RETICORA_CLOUD_URL and
// without a keystore performs the CSR enrollment, persists the identity
// (0600) and runs on; a second start finds the keystore and sends no
// enrollment request. The token never appears in the collector log.
func TestAuditCollectorEnrollsFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	ca := newAuditPKI(t)
	const token = "audit-enroll-token-7f3c"
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000b3", "6f0b6f0e-0000-4000-8000-0000000000b4"
	var mu sync.Mutex
	var enrolls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(token)) && !strings.Contains(r.Header.Get("Authorization"), token) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		mu.Lock()
		enrolls = append(enrolls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		// Answer like the server's enrollment endpoint: sign the CSR.
		var req struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(body, &req)
		block, _ := pem.Decode([]byte(req.CSR))
		if block == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cert := ca.issue(t, csr.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour), false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": colID, "certificate": map[string]any{
			"certificate_pem": cert, "ca_bundle_pem": ca.pem, "not_after": time.Now().Add(90 * 24 * time.Hour)}})
	}))
	defer srv.Close()
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), enrolls...)
	}

	credPath := filepath.Join(dir, "credentials.json")
	env := map[string]string{
		"RETICORA_ENROLL_TOKEN":        token,
		"RETICORA_CLOUD_URL":           srv.URL,
		"RETICORA_SERVER_URL":          srv.URL,
		"RETICORA_NATS_URL":            "tls://127.0.0.1:1",
		"RETICORA_CREDENTIALS_PATH":    credPath,
		"RETICORA_SPOOL_DIR":           filepath.Join(dir, "spool"),
		"RETICORA_AGENT_RELAY_ADDR":    "127.0.0.1:0",
		"RETICORA_DISCOVERY_PROTOCOLS": "sweep",
		"RETICORA_ENVIRONMENT":         "development",
	}
	// The collector runs on after the enrollment: it logs its normal start.
	running := func(output func() string) bool {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(output(), "collector configured") {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}

	output, stop := auditStartCollector(t, env)
	started := running(output)
	first := stop()
	got := requests()
	st, err := os.Stat(credPath)
	switch {
	case len(got) == 0:
		t.Fatalf("COL-04 violated: started with RETICORA_ENROLL_TOKEN and RETICORA_CLOUD_URL and without a keystore, the collector sent no "+
			"enrollment request carrying the token within 15 s (keystore %s: %v). Collector log tail:\n%s", credPath, err, auditTail(first, 8))
	case err != nil:
		t.Fatalf("COL-04 violated: enrollment requests %v were answered, but no identity was stored at %s: %v", got, credPath, err)
	case st.Mode().Perm() != 0o600:
		t.Errorf("the enrolled identity %s has mode %o, want 0600", credPath, st.Mode().Perm())
	}
	if len(got) != 1 || got[0] != "POST /api/v1/collectors/enroll" {
		t.Errorf("enrollment requests = %v, want exactly one POST /api/v1/collectors/enroll", got)
	}
	if !started {
		t.Errorf("after the enrollment the collector did not start normally within 15 s. Log tail:\n%s", auditTail(first, 8))
	}
	creds, err := (&keystore.FileStore{Path: credPath}).Load(context.Background())
	if err != nil || creds.DeviceID != colID || creds.ClientPrivateKeyPEM == "" || creds.Metadata["server_url"] != srv.URL {
		t.Fatalf("stored identity: %+v, %v", creds, err)
	}
	if strings.Contains(first, token) {
		t.Errorf("the enrollment token appears in the collector log:\n%s", first)
	}
	keyBefore := creds.ClientPrivateKeyPEM

	// Second start with the same environment: the keystore wins, no
	// enrollment request (Retest F-101).
	output, stop = auditStartCollector(t, env)
	started = running(output)
	second := stop()
	if !started {
		t.Errorf("second start: the collector did not start within 15 s. Log tail:\n%s", auditTail(second, 8))
	}
	if again := requests(); len(again) != 1 {
		t.Errorf("second start with a keystore sent enrollment requests again: %v", again[1:])
	}
	if creds, err = (&keystore.FileStore{Path: credPath}).Load(context.Background()); err != nil || creds.ClientPrivateKeyPEM != keyBefore {
		t.Errorf("second start replaced the stored identity (%v)", err)
	}
	if strings.Contains(second, token) {
		t.Errorf("the enrollment token appears in the collector log of the second start:\n%s", second)
	}
}

// TestCollectorEnrollFromEnvironmentFailureExits covers WP-493 (COL-04): a
// refused unattended enrollment ends the start with exit code != 0 and the
// cause in the log, without the token and without a stored identity.
func TestCollectorEnrollFromEnvironmentFailureExits(t *testing.T) {
	dir := t.TempDir()
	const token = "audit-enroll-token-refused-91d2"
	srv := fakeEnrollServer(t, "SOME-OTHER-CODE", "6f0b6f0e-0000-4000-8000-0000000000c1", "6f0b6f0e-0000-4000-8000-0000000000c2")
	credPath := filepath.Join(dir, "credentials.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuditHelperCollectorMain$", "-test.count=1")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RETICORA_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, auditMainEnv+"=1",
		"RETICORA_ENROLL_TOKEN="+token,
		"RETICORA_CLOUD_URL="+srv.URL,
		"RETICORA_CREDENTIALS_PATH="+credPath,
		"RETICORA_SPOOL_DIR="+filepath.Join(dir, "spool"),
		"RETICORA_AGENT_RELAY_ADDR=127.0.0.1:0",
		"RETICORA_ENVIRONMENT=production")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("the collector did not exit after a refused enrollment:\n%s", auditTail(out.String(), 8))
	}
	log := out.String()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Errorf("refused enrollment: exit %v, want an exit code != 0. Output:\n%s", err, log)
	}
	if !strings.Contains(log, "collector.enroll.failed") || !strings.Contains(log, "invalid or expired") {
		t.Errorf("the log does not name the cause:\n%s", log)
	}
	if strings.Contains(log, token) {
		t.Errorf("the enrollment token appears in the collector log:\n%s", log)
	}
	if _, serr := os.Stat(credPath); !os.IsNotExist(serr) {
		t.Errorf("a refused enrollment stored an identity at %s: %v", credPath, serr)
	}
}
