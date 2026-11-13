package probe

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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The probe type http (PRT-04, WP-971) against httptest servers: plain HTTP
// and TLS servers trusted through a TLS scope of 127.0.0.0/8.

// httpSpec is an http probe of url with extra params (JSON members).
func httpSpec(url, extra string) probewire.ProbeSpec {
	params := `{"url":"` + url + `"`
	if extra != "" {
		params += "," + extra
	}
	return probewire.ProbeSpec{ID: "probe-http", Type: TypeHTTP, Target: "127.0.0.1", IntervalMS: 60_000, TimeoutMS: 10_000,
		DownCount: 3, UpCount: 1, Params: json.RawMessage(params + "}")}
}

// trusting is an executor that trusts the certificate of srv.
func trusting(srv *httptest.Server) *HTTPExecutor {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	return &HTTPExecutor{TLSScopes: []plugins.TLSScope{{Network: loopback, RootCAs: pool}}}
}

func runHTTP(t *testing.T, e *HTTPExecutor, spec probewire.ProbeSpec, creds map[string]string) probewire.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.Execute(ctx, spec, creds)
}

func expectResult(t *testing.T, name string, r probewire.Result, status probewire.Status, code string) {
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Errorf("%s: %s/%s, want %s/%s (labels %v)", name, r.Status, r.Code, status, code, r.Labels)
	}
}

func TestHTTPStatusAndRegex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/moved":
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("status: healthy\n"))
	}))
	defer srv.Close()
	e := &HTTPExecutor{}
	r := runHTTP(t, e, httpSpec(srv.URL+"/", `"body_regex":"status: (healthy|ok)"`), nil)
	expectResult(t, "200 with matching body", r, probewire.StatusOK, "")
	if r.Values[ValueHTTPStatusCode] != 200 || r.RTTMS == nil || r.Values[ValueHTTPDNS] != 0 {
		t.Errorf("values %v rtt %v", r.Values, r.RTTMS)
	}
	for _, key := range []string{ValueHTTPDNS, ValueHTTPConnect, ValueHTTPTLS, ValueHTTPTTFB} {
		if _, ok := r.Values[key]; !ok {
			t.Errorf("value %s missing", key)
		}
	}
	if _, ok := r.Values[ValueHTTPCertExpiry]; ok {
		t.Error("cert_expiry_s on plain http")
	}
	expectResult(t, "body without match", runHTTP(t, e, httpSpec(srv.URL+"/", `"body_regex":"degraded"`), nil),
		probewire.StatusFail, probewire.ReasonBodyMismatch)
	expectResult(t, "404 outside the default 200-399", runHTTP(t, e, httpSpec(srv.URL+"/missing", ""), nil),
		probewire.StatusFail, probewire.ReasonStatusMismatch)
	expectResult(t, "404 expected", runHTTP(t, e, httpSpec(srv.URL+"/missing", `"expected_status":["404"]`), nil),
		probewire.StatusOK, "")
	expectResult(t, "201 in a range", runHTTP(t, e, httpSpec(srv.URL+"/created", `"expected_status":["200","201-204"]`), nil),
		probewire.StatusOK, "")
	expectResult(t, "201 outside the list", runHTTP(t, e, httpSpec(srv.URL+"/created", `"expected_status":["200"]`), nil),
		probewire.StatusFail, probewire.ReasonStatusMismatch)
	// Without follow_redirects the redirect is the answer (302 in 200-399).
	expectResult(t, "redirect not followed", runHTTP(t, e, httpSpec(srv.URL+"/moved", ""), nil), probewire.StatusOK, "")
	expectResult(t, "HEAD", runHTTP(t, e, httpSpec(srv.URL+"/", `"method":"HEAD"`), nil), probewire.StatusOK, "")
	// A closed port is refused.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	expectResult(t, "closed port", runHTTP(t, e, httpSpec(url+"/", ""), nil), probewire.StatusFail, probewire.ReasonRefused)
}

func TestHTTPRedirectLimit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n := len(r.URL.Path) - 1 // "/aaa" is three hops before the target
		if n > 0 {
			http.Redirect(w, r, r.URL.Path[:n], http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	e := &HTTPExecutor{}
	expectResult(t, "3 redirects, 3 allowed", runHTTP(t, e, httpSpec(srv.URL+"/aaa", `"follow_redirects":3,"expected_status":["200"]`), nil),
		probewire.StatusOK, "")
	hits.Store(0)
	expectResult(t, "3 redirects, 2 allowed", runHTTP(t, e, httpSpec(srv.URL+"/aaa", `"follow_redirects":2`), nil),
		probewire.StatusFail, probewire.ReasonRedirectLimit)
	if n := hits.Load(); n != 3 {
		t.Errorf("%d requests with 2 redirects allowed, want 3", n)
	}
}

func TestHTTPRedirectDropsAuthorizationOnOriginChange(t *testing.T) {
	var otherAuth atomic.Value
	otherAuth.Store("")
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer other.Close()
	var firstAuth atomic.Value
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, other.URL+"/landing", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	e := trusting(first)
	e.TLSScopes[0].RootCAs.AddCert(other.Certificate())
	creds := map[string]string{"token": "s3cret"}
	r := runHTTP(t, e, httpSpec(first.URL+"/", `"follow_redirects":1`), creds)
	expectResult(t, "redirect to another origin", r, probewire.StatusOK, "")
	if got, _ := firstAuth.Load().(string); got != "Bearer s3cret" {
		t.Errorf("first origin got Authorization %q", got)
	}
	if got, _ := otherAuth.Load().(string); got != "" {
		t.Errorf("other origin (other port) got Authorization %q, want none", got)
	}
	// https → http with a credential is refused before the request.
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { plainHits.Add(1) }))
	defer plain.Close()
	downgrade := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/", http.StatusFound)
	}))
	defer downgrade.Close()
	r = runHTTP(t, trusting(downgrade), httpSpec(downgrade.URL+"/", `"follow_redirects":3`), creds)
	expectResult(t, "https to http with credential", r, probewire.StatusFail, probewire.ReasonRedirectLimit)
	if plainHits.Load() != 0 {
		t.Error("the plain http server was requested after a downgrade with credential")
	}
	// A credential at an http:// URL is refused before anything is sent.
	r = runHTTP(t, &HTTPExecutor{}, httpSpec(plain.URL+"/", ""), creds)
	expectResult(t, "credential at http://", r, probewire.StatusError, probewire.ReasonConfig)
	if plainHits.Load() != 0 || r.Labels[LabelField] != "params.url" {
		t.Errorf("credential at http://: hits %d, labels %v", plainHits.Load(), r.Labels)
	}
}

func TestHTTPBodyCap(t *testing.T) {
	const size = 2 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("a", 32<<10))
		for written := 0; written < size-3; {
			n, err := w.Write(chunk)
			written += n
			if err != nil {
				return
			}
		}
		_, _ = w.Write([]byte("END"))
	}))
	defer srv.Close()
	e := &HTTPExecutor{}
	// The marker at the end of 2 MiB lies beyond max_body: cut off without
	// an error, so the expression does not match.
	expectResult(t, "marker beyond max_body", runHTTP(t, e, httpSpec(srv.URL+"/", `"body_regex":"END"`), nil),
		probewire.StatusFail, probewire.ReasonBodyMismatch)
	expectResult(t, "match within max_body", runHTTP(t, e, httpSpec(srv.URL+"/", `"body_regex":"^a{1000}","max_body":1048576`), nil),
		probewire.StatusOK, "")
	expectResult(t, "1 MiB cap", runHTTP(t, e, httpSpec(srv.URL+"/", `"body_regex":"END","max_body":1048576`), nil),
		probewire.StatusFail, probewire.ReasonBodyMismatch)
}

// expiringServer is a TLS server whose self-signed certificate for
// 127.0.0.1 expires after lifetime, and the pool that trusts it.
func expiringServer(t *testing.T, lifetime time.Duration, h http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(971), Subject: pkix.Name{CommonName: "wp971"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(lifetime),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return srv, pool
}

func scopeOf(pool *x509.CertPool) []plugins.TLSScope {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	return []plugins.TLSScope{{Network: loopback, RootCAs: pool}}
}

func TestHTTPCertExpiry(t *testing.T) {
	srv, pool := expiringServer(t, 10*24*time.Hour, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	e := &HTTPExecutor{TLSScopes: scopeOf(pool)}
	r := runHTTP(t, e, httpSpec(srv.URL+"/", ""), nil)
	expectResult(t, "10 days left, warn at 21", r, probewire.StatusOK, probewire.WarnCertExpiring)
	if left := r.Values[ValueHTTPCertExpiry]; left < 9*86400 || left > 10*86400 {
		t.Errorf("cert_expiry_s %v, want about 10 days", left)
	}
	if _, ok := r.Values[ValueHTTPTLS]; !ok {
		t.Error("tls_ms missing")
	}
	expectResult(t, "10 days left, warn at 7", runHTTP(t, e, httpSpec(srv.URL+"/", `"cert_warn_days":7`), nil), probewire.StatusOK, "")
}

func TestHTTPUntrustedCertificateSendsNoCredential(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	// No TLS scope: the system roots do not know the test certificate.
	r := runHTTP(t, &HTTPExecutor{}, httpSpec(srv.URL+"/", ""), map[string]string{"username": "u", "password": "p"})
	expectResult(t, "untrusted certificate", r, probewire.StatusError, probewire.ReasonTLSUntrusted)
	if hits.Load() != 0 {
		t.Errorf("the server saw %d requests, want none", hits.Load())
	}
	// A pin that does not match is untrusted as well.
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	pinned := &HTTPExecutor{TLSScopes: []plugins.TLSScope{{Network: loopback, PinsSHA256: [][]byte{make([]byte, 32)}}}}
	expectResult(t, "pin mismatch", runHTTP(t, pinned, httpSpec(srv.URL+"/", ""), nil), probewire.StatusError, probewire.ReasonTLSUntrusted)
	if hits.Load() != 0 {
		t.Errorf("the server saw %d requests after a pin mismatch", hits.Load())
	}
}

func TestHTTPBasicAndBearer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, basic := r.BasicAuth()
		switch {
		case basic && user == "probe" && pass == "pw":
		case r.Header.Get("Authorization") == "Bearer tok":
		case r.URL.Path == "/public" && r.Header.Get("Authorization") == "":
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	e := trusting(srv)
	expectResult(t, "basic", runHTTP(t, e, httpSpec(srv.URL+"/", ""), map[string]string{"username": "probe", "password": "pw"}),
		probewire.StatusOK, "")
	expectResult(t, "bearer", runHTTP(t, e, httpSpec(srv.URL+"/", ""), map[string]string{"token": "tok"}), probewire.StatusOK, "")
	expectResult(t, "anonymous", runHTTP(t, e, httpSpec(srv.URL+"/public", ""), nil), probewire.StatusOK, "")
	r := runHTTP(t, e, httpSpec(srv.URL+"/", ""), map[string]string{"username": "probe"})
	expectResult(t, "username without password", r, probewire.StatusError, probewire.ReasonConfig)
	if strings.Contains(r.Labels[LabelMessage], "probe") {
		t.Errorf("the result names the credential: %v", r.Labels)
	}
}

func TestHTTPAuthFailedIsUnknown(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forbidden" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	e := trusting(srv)
	expectResult(t, "401", runHTTP(t, e, httpSpec(srv.URL+"/", ""), map[string]string{"token": "wrong"}),
		probewire.StatusError, probewire.ReasonAuthFailed)
	expectResult(t, "403", runHTTP(t, e, httpSpec(srv.URL+"/forbidden", ""), nil), probewire.StatusError, probewire.ReasonAuthFailed)
	expectResult(t, "401 expected", runHTTP(t, e, httpSpec(srv.URL+"/", `"expected_status":["401"]`), nil), probewire.StatusOK, "")
	// error/auth_failed leads to unknown in the state machine, never down.
	m := openMachine(t, t.TempDir())
	spec := httpSpec(srv.URL+"/", "")
	states := feed(t, m, &spec, repeat(errResult(probewire.ReasonAuthFailed), 5)...)
	if got := states[len(states)-1]; got != probewire.StateUnknown {
		t.Errorf("state after auth_failed %s, want unknown", got)
	}
}

func TestHTTPOnlyGetHead(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { methods = append(methods, r.Method) }))
	defer srv.Close()
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		spec := httpSpec(srv.URL+"/", `"method":"`+m+`"`)
		if errs := probewire.Validate(&spec); len(errs) == 0 || errs[0].Field != "params.method" {
			t.Errorf("Validate accepted method %s: %v", m, errs)
		}
		expectResult(t, "Execute "+m, runHTTP(t, &HTTPExecutor{}, spec, nil), probewire.StatusError, probewire.ReasonConfig)
		r := Run(context.Background(), &spec, nil)
		expectResult(t, "Run "+m, r, probewire.StatusError, probewire.ReasonConfig)
	}
	for _, m := range []string{"GET", "HEAD"} {
		spec := httpSpec(srv.URL+"/", `"method":"`+m+`"`)
		expectResult(t, "Run "+m, Run(context.Background(), &spec, nil), probewire.StatusOK, "")
	}
	if strings.Join(methods, ",") != "GET,HEAD" {
		t.Errorf("server saw %v, want GET and HEAD only", methods)
	}
}

func TestHTTPCertExpiringIsDegraded(t *testing.T) {
	srv, pool := expiringServer(t, 5*24*time.Hour, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	spec := httpSpec(srv.URL+"/", "")
	r := runHTTP(t, &HTTPExecutor{TLSScopes: scopeOf(pool)}, spec, nil)
	expectResult(t, "5 days left", r, probewire.StatusOK, probewire.WarnCertExpiring)
	m := openMachine(t, t.TempDir())
	if _, _, err := m.Observe(&spec, r); err != nil {
		t.Fatal(err)
	}
	c, _ := m.Counters(spec.ID)
	if c.State != probewire.StateDegraded || c.Reason != probewire.WarnCertExpiring {
		t.Errorf("state %s/%s, want degraded/cert_expiring", c.State, c.Reason)
	}
}

// TestHTTPConfigureTLSScopesReachesRun: the TLS scopes of ConfigureHTTP
// reach the registered executor that probe.Run uses; a target pinned in a
// scope is ok there, without it tls_untrusted.
func TestHTTPConfigureTLSScopesReachesRun(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	t.Cleanup(func() { ConfigureHTTP(nil) })
	spec := httpSpec(srv.URL+"/", "")
	ConfigureHTTP(nil)
	expectResult(t, "without scope", Run(context.Background(), &spec, nil), probewire.StatusError, probewire.ReasonTLSUntrusted)
	pin := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	ConfigureHTTP([]plugins.TLSScope{{Network: loopback, PinsSHA256: [][]byte{pin[:]}}})
	expectResult(t, "pinned scope", Run(context.Background(), &spec, nil), probewire.StatusOK, "")
}
