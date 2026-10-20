// Tests of the NAS plugin. TestAuditNASCredentialTransport (F-011, WP-152)
// moved here from nas_auditdefect_test.go (WP-379): Synology DSM is spoken
// only over HTTPS 5001 with certificate verification, the login is a POST
// and no credential follows a redirect. The fake DSM endpoints listen on a
// random 127.x.y.z loopback address on the fixed vendor ports so parallel
// runs do not collide.
package nas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	auditNASUser     = "audit-nas-admin"
	auditNASPassword = "S3cret-audit-nas"
	auditNASSID      = "audit-sid-7f3c9e"
	// plainDSMPort is the Synology DSM web API over plain HTTP; the plugin
	// never contacts it.
	plainDSMPort = 5000
)

// auditNASListen binds port on a random 127.x.y.z address (port 0: a free
// port).
func auditNASListen(t *testing.T, port int) (net.Listener, string) {
	t.Helper()
	var lastErr error
	for range 20 {
		ip := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(250), rand.IntN(250), 1+rand.IntN(250))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err == nil {
			return ln, ip
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback address on port %d: %v", port, lastErr)
	return nil, ""
}

// serveNAS serves handler on ln, over TLS with the self-signed httptest
// certificate when useTLS is set.
func serveNAS(t *testing.T, ln net.Listener, handler http.Handler, useTLS bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	// Rejected handshakes are expected; keep them out of the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// pinScope is the TLS scope of cidr that pins the SPKI of cert.
func pinScope(t *testing.T, cidr string, cert *x509.Certificate) plugins.TLSScope {
	t.Helper()
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return plugins.TLSScope{Network: network, PinsSHA256: [][]byte{pin[:]}}
}

// nasRequest is a request a fake NAS endpoint received.
type nasRequest struct {
	method      string
	path        string
	requestURI  string
	contentType string
	// form is the parsed POST body.
	form url.Values
	dump string
}

// nasRecorder records the requests of a fake NAS endpoint.
type nasRecorder struct {
	mu   sync.Mutex
	reqs []nasRequest
}

func (r *nasRecorder) add(req *http.Request) {
	raw, _ := httputil.DumpRequest(req, true)
	_ = req.ParseForm()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, nasRequest{method: req.Method, path: req.URL.Path, requestURI: req.RequestURI,
		contentType: req.Header.Get("Content-Type"), form: req.PostForm, dump: string(raw)})
}

func (r *nasRecorder) all() []nasRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nasRequest(nil), r.reqs...)
}

// leaked reports the requests that carry the password readable (plain,
// URL-encoded or as HTTP Basic credentials).
func (r *nasRecorder) leaked() []string {
	basic := base64.StdEncoding.EncodeToString([]byte(auditNASUser + ":" + auditNASPassword))
	var out []string
	for _, req := range r.all() {
		dump, _ := url.QueryUnescape(req.dump)
		if strings.Contains(dump, auditNASPassword) || strings.Contains(req.dump, basic) {
			first, _, _ := strings.Cut(req.dump, "\r\n")
			out = append(out, first)
		}
	}
	return out
}

// fakeDSM answers like the Synology DSM web API: the vendor probe, the
// login and SYNO.DSM.Info. Login and info read their parameters only from
// the POST body, so a request with them in the URL fails.
func fakeDSM(rec *nasRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		form := r.PostForm
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/webapi/query.cgi":
			_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/webapi/auth.cgi" && form.Get("api") == "SYNO.API.Auth" && form.Get("method") == "login":
			if form.Get("account") != auditNASUser || form.Get("passwd") != auditNASPassword {
				_, _ = io.WriteString(w, `{"success":false,"error":{"code":400}}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"sid":"`+auditNASSID+`"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/webapi/entry.cgi" && form.Get("api") == "SYNO.DSM.Info" && form.Get("method") == "getinfo":
			if form.Get("_sid") != auditNASSID {
				_, _ = io.WriteString(w, `{"success":false,"error":{"code":119}}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"model":"DS920+","serial":"20A1B2C3","firmware_ver":"DSM 7.2-64570","hostname":"nas01"}}`)
		default:
			http.NotFound(w, r)
		}
	}
}

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.client == nil {
		t.Fatal("expected http client to be initialized")
	}
	// COL-02: the default client verifies certificates (system roots) and
	// follows no redirects.
	if tr, ok := p.client.Transport.(*http.Transport); !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("the default NAS client must verify certificates")
	}
	if p.client.CheckRedirect == nil {
		t.Error("the default NAS client must not follow redirects")
	}
	for _, probe := range nasPorts {
		if probe.port == plainDSMPort {
			t.Errorf("the vendor probe contacts port %d (plain-HTTP DSM)", plainDSMPort)
		}
	}
	if p.Name() != "nas" {
		t.Errorf("Name() = %q, want nas", p.Name())
	}
}

// TestAuditNASCredentialTransport encodes COL-02 for the NAS plugin (F-011):
// the Synology account is never readable on the wire and never in a URL;
// nothing is sent to the plain-HTTP DSM on 5000, nothing to an HTTPS
// endpoint whose certificate is not trusted (the collection fails with
// tls_untrusted), and no credential follows a redirect to plain HTTP or
// another host. With the SPKI pin of the target's TLS scope the login is a
// POST whose body carries account and password; the session ID travels only
// in POST bodies. Neither password nor session ID appears in a log or an
// error.
func TestAuditNASCredentialTransport(t *testing.T) {
	wrongPin := sha256.Sum256([]byte("other key"))
	pinned := func(cidr func(ip string) string) func(*testing.T, string, *x509.Certificate) []plugins.TLSScope {
		return func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope {
			return []plugins.TLSScope{pinScope(t, cidr(ip), cert)}
		}
	}
	host := func(ip string) string { return ip + "/32" }
	cases := []struct {
		name string
		// https serves the DSM over TLS (self-signed) on 5001 next to the
		// plain-HTTP DSM on 5000 of the same address.
		https bool
		// scopes are the TLS scopes of the plugin for the endpoint.
		scopes  func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope
		trusted bool
		// redirectPath is answered by the trusted endpoint with a 307 to
		// the same path on the plain DSM (5000) or, with toOther, on the
		// DSM of another trusted address.
		redirectPath string
		toOther      bool
	}{
		{name: "production default New(): Synology DSM over http://target:5000 only"},
		{name: "untrusted self-signed certificate on 5001 without TLS scope", https: true},
		{name: "self-signed certificate on 5001 with a wrong pin", https: true,
			scopes: func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope {
				scope := pinScope(t, ip+"/32", cert)
				scope.PinsSHA256 = [][]byte{wrongPin[:]}
				return []plugins.TLSScope{scope}
			}},
		{name: "matching pin of another network", https: true,
			scopes: pinned(func(string) string { return "10.0.0.1/32" })},
		{name: "self-signed certificate on 5001 with the matching pin: login per POST", https: true, trusted: true,
			scopes: pinned(host)},
		{name: "trusted endpoint redirects the login to plain HTTP on 5000", https: true, trusted: true,
			scopes: pinned(host), redirectPath: "/webapi/auth.cgi"},
		{name: "trusted endpoint redirects the session to plain HTTP on 5000", https: true, trusted: true,
			scopes: pinned(host), redirectPath: "/webapi/entry.cgi"},
		{name: "trusted endpoint redirects the login to another trusted host", https: true, trusted: true,
			scopes: pinned(func(string) string { return "127.0.0.0/8" }), redirectPath: "/webapi/auth.cgi", toOther: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, ip := auditNASListen(t, plainDSMPort)
			plain := &nasRecorder{}
			serveNAS(t, ln, fakeDSM(plain), false)
			otherLn, otherIP := auditNASListen(t, synologyHTTPSPort)
			other := &nasRecorder{}
			serveNAS(t, otherLn, fakeDSM(other), true)

			secure := &nasRecorder{}
			var opts []Option
			if tc.https {
				tlsLn, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(synologyHTTPSPort)))
				if err != nil {
					t.Fatal(err)
				}
				dsm := fakeDSM(secure)
				srv := serveNAS(t, tlsLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.redirectPath != "" && r.URL.Path == tc.redirectPath {
						secure.add(r)
						to := "http://" + net.JoinHostPort(ip, strconv.Itoa(plainDSMPort))
						if tc.toOther {
							to = "https://" + net.JoinHostPort(otherIP, strconv.Itoa(synologyHTTPSPort))
						}
						http.Redirect(w, r, to+r.URL.Path, http.StatusTemporaryRedirect)
						return
					}
					dsm(w, r)
				}), true)
				if tc.scopes != nil {
					opts = append(opts, WithTLSScopes(tc.scopes(t, ip, srv.Certificate())...))
				}
			}

			logs := captureLogs(t)
			p := New(opts...)
			p.Timeout = 2 * time.Second
			rec, err := p.Collect(context.Background(), ip, map[string]string{"username": auditNASUser, "password": auditNASPassword})

			if n := len(plain.all()); n > 0 {
				t.Errorf("COL-02 violated: the plain-HTTP DSM %s received %d request(s), %d with the password readable, e.g. %q",
					net.JoinHostPort(ip, strconv.Itoa(plainDSMPort)), n, len(plain.leaked()), plain.leaked())
			}
			if n := len(other.all()); n > 0 {
				t.Errorf("COL-02 violated: the DSM of another host %s received %d request(s), %d with the password",
					otherIP, n, len(other.leaked()))
			}
			for _, secret := range []string{auditNASPassword, auditNASSID} {
				if strings.Contains(logs.String(), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
					t.Errorf("the secret %q appears in the log %q or the error %v", secret, logs.String(), err)
				}
			}
			for _, req := range secure.all() {
				if dump, _ := url.QueryUnescape(req.requestURI); strings.Contains(dump, auditNASPassword) ||
					strings.Contains(dump, auditNASUser) || strings.Contains(dump, auditNASSID) {
					t.Errorf("COL-02 violated: account, password or session ID in the URL %q (access logs)", req.requestURI)
				}
			}
			switch {
			case !tc.https:
				if err == nil {
					t.Error("a DSM reachable only over plain HTTP must not be collected")
				}
			case !tc.trusted:
				if n := len(secure.all()); n > 0 {
					t.Errorf("COL-02 violated: the DSM %s with an untrusted certificate received %d request(s), %d with the password",
						net.JoinHostPort(ip, strconv.Itoa(synologyHTTPSPort)), n, len(secure.leaked()))
				}
				if !errors.Is(err, plugins.ErrTLSUntrusted) || !strings.Contains(err.Error(), "tls_untrusted") {
					t.Errorf("Collect error = %v, want tls_untrusted", err)
				}
			case tc.redirectPath != "":
				var redirected bool
				for _, req := range secure.all() {
					redirected = redirected || req.path == tc.redirectPath
				}
				if !redirected {
					t.Errorf("the trusted endpoint received no request for %s", tc.redirectPath)
				}
				if err != nil || rec == nil || rec.Classification.Model != "" {
					t.Errorf("redirected collection: err=%v, record=%+v, want the result without DSM info", err, rec)
				}
			default:
				reqs := secure.all()
				if err != nil || rec == nil || rec.Classification.Model != "DS920+" || len(reqs) != 3 {
					t.Fatalf("trusted DSM: err=%v, record=%+v, requests=%d (want probe, login, info)", err, rec, len(reqs))
				}
				login, info := reqs[1], reqs[2]
				if login.method != http.MethodPost || login.path != "/webapi/auth.cgi" || login.requestURI != "/webapi/auth.cgi" ||
					login.contentType != "application/x-www-form-urlencoded" ||
					login.form.Get("account") != auditNASUser || login.form.Get("passwd") != auditNASPassword {
					t.Errorf("login = %s %s (%s), body %v; want POST /webapi/auth.cgi with account and password only in the body",
						login.method, login.requestURI, login.contentType, login.form)
				}
				if info.method != http.MethodPost || info.requestURI != "/webapi/entry.cgi" || info.form.Get("_sid") != auditNASSID {
					t.Errorf("info = %s %s, body %v; want POST /webapi/entry.cgi with the session ID only in the body",
						info.method, info.requestURI, info.form)
				}
				if leaked := secure.leaked(); len(leaked) != 1 || !strings.HasPrefix(leaked[0], "POST /webapi/auth.cgi ") {
					t.Errorf("requests with the password: %q, want only the login body", leaked)
				}
			}
		})
	}
}

// TestTrueNASCredentialsOnlyOverVerifiedTLS: the TrueNAS Basic credentials
// reach only an endpoint whose certificate is trusted, and not a plain-HTTP
// redirect target (COL-02).
func TestTrueNASCredentialsOnlyOverVerifiedTLS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pinned   bool
		redirect bool
	}{
		{name: "untrusted self-signed certificate"},
		{name: "matching pin", pinned: true},
		{name: "trusted endpoint redirects to plain HTTP", pinned: true, redirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plainLn, ip := auditNASListen(t, 0)
			plain := &nasRecorder{}
			serveNAS(t, plainLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plain.add(r) }), false)
			tlsLn, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
			if err != nil {
				t.Fatal(err)
			}
			secure := &nasRecorder{}
			srv := serveNAS(t, tlsLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secure.add(r)
				if tc.redirect {
					http.Redirect(w, r, "http://"+plainLn.Addr().String()+r.URL.Path, http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, `{"hostname":"truenas01","version":"TrueNAS-SCALE-23.10.1"}`)
			}), true)
			var opts []Option
			if tc.pinned {
				opts = append(opts, WithTLSScopes(pinScope(t, ip+"/32", srv.Certificate())))
			}
			p := New(opts...)
			port := tlsLn.Addr().(*net.TCPAddr).Port

			res, err := p.collectTrueNAS(context.Background(), ip, port, auditNASUser, auditNASPassword)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(plain.all()); n > 0 {
				t.Errorf("COL-02 violated: the plain-HTTP endpoint received %d request(s), %d with the credentials", n, len(plain.leaked()))
			}
			switch {
			case !tc.pinned:
				if n := len(secure.all()); n > 0 || res.Name != ip {
					t.Errorf("untrusted endpoint received %d request(s), name %q", n, res.Name)
				}
			case tc.redirect:
				if len(secure.leaked()) != 1 {
					t.Errorf("the trusted endpoint received %d request(s) with the credentials, want 1", len(secure.leaked()))
				}
			default:
				if len(secure.leaked()) != 1 || res.Name != "truenas01" {
					t.Errorf("trusted endpoint: requests with credentials=%d, name %q", len(secure.leaked()), res.Name)
				}
			}
		})
	}
}

// newTestPlugin rewrites all plugin HTTP requests to the test server, which
// lets the response parsing be exercised without the fixed vendor ports.
func newTestPlugin(t *testing.T, srv *httptest.Server) *Plugin {
	t.Helper()
	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	p := New()
	base := http.DefaultTransport
	p.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			r := req.Clone(req.Context())
			r.URL.Scheme = srvURL.Scheme
			r.URL.Host = srvURL.Host
			r.Host = srvURL.Host
			return base.RoundTrip(r)
		}),
	}
	return p
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// trustedDSM serves the fake DSM over TLS on 5001 of a random loopback
// address and returns the address and a plugin that pins its certificate.
func trustedDSM(t *testing.T, handler http.Handler) (string, *Plugin) {
	t.Helper()
	ln, ip := auditNASListen(t, synologyHTTPSPort)
	srv := serveNAS(t, ln, handler, true)
	p := New(WithTLSScopes(pinScope(t, ip+"/32", srv.Certificate())))
	p.Timeout = 2 * time.Second
	return ip, p
}

func TestVerifyVendorStatusRules(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		vendor     string
		want       bool
	}{
		{"synology endpoint 200", http.StatusOK, "synology", true},
		{"synology endpoint 403 still counts", http.StatusForbidden, "synology", true},
		{"qnap endpoint 200", http.StatusOK, "qnap", true},
		{"truenas endpoint 401 still counts", http.StatusUnauthorized, "truenas", true},
		{"server error rejects", http.StatusInternalServerError, "synology", false},
		{"unknown vendor rejected", http.StatusOK, "buffalo", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.statusCode)
			}))
			defer srv.Close()

			p := newTestPlugin(t, srv)
			if got, err := p.verifyVendor(context.Background(), p.client, srv.URL, c.vendor); got != c.want || err != nil {
				t.Errorf("verifyVendor(status=%d, vendor=%q) = %v, %v, want %v", c.statusCode, c.vendor, got, err, c.want)
			}
		})
	}
}

func TestVerifyVendorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately unreachable
	p := newTestPlugin(t, srv)
	if ok, err := p.verifyVendor(context.Background(), p.client, "http://127.0.0.1:1", "synology"); ok || err != nil {
		t.Errorf("verifyVendor = %v, %v, want false without a certificate error for an unreachable endpoint", ok, err)
	}
}

func TestDetectNASVendorSynology(t *testing.T) {
	ip, p := trustedDSM(t, fakeDSM(&nasRecorder{}))
	vendor, port, err := p.detectNASVendor(context.Background(), ip)
	if vendor != "synology" || port != synologyHTTPSPort || err != nil {
		t.Errorf("detectNASVendor = (%q, %d, %v), want (synology, %d, nil)", vendor, port, err, synologyHTTPSPort)
	}
}

func TestDetectNASVendorNone(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond}
	vendor, port, err := p.detectNASVendor(context.Background(), "192.0.2.1")
	if vendor != "" || port != 0 || err != nil {
		t.Errorf("detectNASVendor = (%q, %d, %v), want (\"\", 0, nil)", vendor, port, err)
	}
}

func TestSynologyLoginParsesSID(t *testing.T) {
	srv := httptest.NewServer(fakeDSM(&nasRecorder{}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	sid, err := p.synologyLogin(context.Background(), p.client, srv.URL, auditNASUser, auditNASPassword)
	if sid != auditNASSID || err != nil {
		t.Errorf("synologyLogin = %q, %v, want %s", sid, err, auditNASSID)
	}
}

func TestSynologyLoginFailureReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(fakeDSM(&nasRecorder{}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	if sid, err := p.synologyLogin(context.Background(), p.client, srv.URL, auditNASUser, "wrong"); sid != "" || err != nil {
		t.Errorf("synologyLogin = %q, %v, want empty on failure", sid, err)
	}
}

func TestSynologySystemInfoParsesData(t *testing.T) {
	srv := httptest.NewServer(fakeDSM(&nasRecorder{}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	info := p.synologySystemInfo(context.Background(), p.client, srv.URL, auditNASSID)
	if info == nil {
		t.Fatal("synologySystemInfo returned nil")
	}
	if info["model"] != "DS920+" || info["hostname"] != "nas01" {
		t.Errorf("unexpected info: %v", info)
	}
	if info := p.synologySystemInfo(context.Background(), p.client, srv.URL, "other-sid"); info != nil {
		t.Errorf("info with a foreign session ID = %v, want nil", info)
	}
}

// TestCollectSynologyEndToEnd: detection, login and SYNO.DSM.Info over the
// verified TLS of 5001 (pin), login and info per POST.
func TestCollectSynologyEndToEnd(t *testing.T) {
	ip, p := trustedDSM(t, fakeDSM(&nasRecorder{}))
	res, err := p.collect(context.Background(), ip, map[string]string{"username": auditNASUser, "password": auditNASPassword})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if res.Manufacturer != "Synology" {
		t.Errorf("Manufacturer = %q, want Synology", res.Manufacturer)
	}
	if res.Model != "DS920+" || res.Name != "DS920+" {
		t.Errorf("Model/Name = %q/%q, want DS920+", res.Model, res.Name)
	}
	if res.Serial != "20A1B2C3" {
		t.Errorf("Serial = %q", res.Serial)
	}
	if res.Firmware != "DSM 7.2-64570" {
		t.Errorf("Firmware = %q", res.Firmware)
	}
	if res.Attributes["dsmVersion"] != "DSM 7.2-64570" || res.Attributes["hostname"] != "nas01" {
		t.Errorf("dsmVersion/hostname = %v/%v", res.Attributes["dsmVersion"], res.Attributes["hostname"])
	}
	if res.Attributes["vendor"] != "synology" || res.Attributes["apiPort"] != synologyHTTPSPort {
		t.Errorf("vendor/apiPort = %v/%v, want synology/%d", res.Attributes["vendor"], res.Attributes["apiPort"], synologyHTTPSPort)
	}
}

func TestCollectSynologyWithoutCredentials(t *testing.T) {
	p := New()
	res, err := p.collectSynology(context.Background(), "192.0.2.31", "", "")
	if err != nil {
		t.Fatalf("collectSynology: %v", err)
	}
	if res.Model != "" || res.Serial != "" {
		t.Errorf("expected unauthenticated result without hardware fields, got %+v", res)
	}
	if res.CIType != "nas" || res.Attributes["vendor"] != "synology" || res.Attributes["apiPort"] != synologyHTTPSPort {
		t.Errorf("unexpected base result: %+v", res)
	}
}

func TestCollectTrueNASEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2.0/system/info" {
			fmt.Fprint(w, `{"hostname":"truenas01","version":"TrueNAS-SCALE-23.10.1","system_product":"M50","system_serial":"TRU123","system_manufacturer":"iXsystems"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	res, err := p.collectTrueNAS(context.Background(), "192.0.2.32", 443, "root", "pw")
	if err != nil {
		t.Fatalf("collectTrueNAS: %v", err)
	}
	if res.Name != "truenas01" {
		t.Errorf("Name = %q, want truenas01", res.Name)
	}
	if res.Firmware != "TrueNAS-SCALE-23.10.1" {
		t.Errorf("Firmware = %q", res.Firmware)
	}
	if res.Model != "M50" || res.Serial != "TRU123" {
		t.Errorf("Model/Serial = %q/%q", res.Model, res.Serial)
	}
	if res.Manufacturer != "iXsystems" {
		t.Errorf("Manufacturer = %q, want iXsystems", res.Manufacturer)
	}
}

func TestCollectQNAPBaseResult(t *testing.T) {
	p := New()
	res, err := p.collectQNAP(context.Background(), "192.0.2.33", 8080, "", "")
	if err != nil {
		t.Fatalf("collectQNAP: %v", err)
	}
	if res.Manufacturer != "QNAP" {
		t.Errorf("Manufacturer = %q, want QNAP", res.Manufacturer)
	}
	if res.Attributes["vendor"] != "qnap" || res.Attributes["apiPort"] != 8080 {
		t.Errorf("unexpected attributes: %v", res.Attributes)
	}
}

func TestCollectDetectsVendor(t *testing.T) {
	ip, p := trustedDSM(t, fakeDSM(&nasRecorder{}))
	res, err := p.collect(context.Background(), ip, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Attributes["vendor"] != "synology" {
		t.Errorf("vendor = %v, want synology", res.Attributes["vendor"])
	}
}

func TestCollectNoVendorDetected(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond}
	_, err := p.collect(context.Background(), "192.0.2.1", nil)
	if err == nil || !strings.Contains(err.Error(), "no NAS vendor detected") {
		t.Errorf("expected vendor detection error, got %v", err)
	}
}

func TestDiscoverFindsNAS(t *testing.T) {
	ip, p := trustedDSM(t, fakeDSM(&nasRecorder{}))
	results, err := p.Probe(context.Background(), []string{ip}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "nas" {
		t.Errorf("CIType = %q, want nas", r.CIType)
	}
	if r.Attributes["vendor"] != "synology" || r.Attributes["apiPort"] != synologyHTTPSPort {
		t.Errorf("vendor/apiPort = %v/%v, want synology/%d", r.Attributes["vendor"], r.Attributes["apiPort"], synologyHTTPSPort)
	}
	if r.Attributes["nasDevice"] != true {
		t.Errorf("nasDevice = %v, want true", r.Attributes["nasDevice"])
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Probe(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", results)
	}
}

func TestDiscoverCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Plugin{Timeout: 100 * time.Millisecond}
	results, err := p.Probe(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}
