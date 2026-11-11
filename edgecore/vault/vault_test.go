package vault

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// kv2 is a KV v2 test server: an AppRole (role-1/secret-1), a token
// (root-token) and the entries of the mount secret by path and version.
type kv2 struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	headers  []http.Header
	lease    int
	entries  map[string]map[int]map[string]any // path → version → data
	deny     map[string]bool                   // paths the token may not read
	revoked  map[string]bool                   // tokens the vault no longer accepts
}

func newKV2(t *testing.T) (*kv2, *httptest.Server) {
	t.Helper()
	k := &kv2{t: t, entries: map[string]map[int]map[string]any{
		"network/sw1": {
			1: {"snmp_ro": "community-v1"},
			2: {"snmp_ro": "community-v2", "number": 7},
		},
	}, deny: map[string]bool{"network/secret": true}, revoked: map[string]bool{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(k.serve))
	t.Cleanup(srv.Close)
	return k, srv
}

func (k *kv2) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	k.requests = append(k.requests, r.Method+" "+r.URL.RequestURI())
	k.headers = append(k.headers, r.Header.Clone())
	lease := k.lease
	token := r.Header.Get("X-Vault-Token")
	revoked := k.revoked[token]
	// After a revocation the AppRole login issues a new token.
	issued := "approle-token"
	if k.revoked[issued] {
		issued = "approle-token-2"
	}
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if revoked {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role_id"] != "role-1" || body["secret_id"] != "secret-1" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":["invalid role or secret ID"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"auth":{"client_token":"` + issued + `","lease_duration":3600,"renewable":true}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/token/lookup-self":
		if token != "root-token" && !strings.HasPrefix(token, "approle-token") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ttl":0,"renewable":false}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/token/renew-self":
		_, _ = w.Write([]byte(`{"auth":{"client_token":"` + token + `","lease_duration":7200,"renewable":true}}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		if !strings.HasPrefix(token, "approle-token") && token != "root-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		if k.deny[path] {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		versions := k.entries[path]
		version := 0
		for v := range versions {
			version = max(version, v)
		}
		if q := r.URL.Query().Get("version"); q != "" {
			version = int(q[0] - '0')
		}
		data, ok := versions[version]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"lease_duration": lease,
			"data": map[string]any{"data": data, "metadata": map[string]any{"version": version}}})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (k *kv2) seen() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.requests...)
}

func caPEM(srv *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

// otherCA is a self-signed certificate that did not issue the test
// server's.
func otherCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "other CA"}, IsCA: true,
		BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func newClient(t *testing.T, srv *httptest.Server, namespace string) *Client {
	t.Helper()
	c, err := New(Config{Address: srv.URL, Namespace: namespace, CAPEM: caPEM(srv)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestLoginAppRole(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "")
	ctx := context.Background()
	if err := c.Login(ctx, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "wrong"}); Class(err) != ClassDenied {
		t.Fatalf("login with a wrong secret id = %v, want %s", err, ClassDenied)
	} else if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("the error names the secret id: %v", err)
	}
	if err := c.Login(ctx, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}); err != nil {
		t.Fatal(err)
	}
	s, err := c.Read(ctx, "secret", "network/sw1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Data["snmp_ro"] != "community-v2" || s.Version != 2 {
		t.Fatalf("read = %+v", s)
	}
	if _, ok := s.Data["number"]; ok {
		t.Fatal("a non-string field was returned")
	}
	if err = c.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1/auth/approle/login", "POST /v1/auth/approle/login", "GET /v1/secret/data/network/sw1",
		"POST /v1/auth/token/renew-self"}
	if got := k.seen(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	// The CA of the connection is the only trust anchor: another one fails.
	strange, err := New(Config{Address: srv.URL, CAPEM: otherCA(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err = strange.Login(ctx, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}); Class(err) != ClassUnreachable {
		t.Fatalf("login against an untrusted certificate = %v, want %s", err, ClassUnreachable)
	}
	if _, err = New(Config{Address: srv.URL, CAPEM: "no pem"}); err == nil {
		t.Fatal("an invalid CA was accepted")
	}
	if _, err = New(Config{Address: "http://vault.example.com"}); err == nil {
		t.Fatal("an http address was accepted")
	}
}

func TestLoginToken(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "")
	ctx := context.Background()
	if err := c.Login(ctx, Auth{Method: AuthToken, Token: "bad"}); Class(err) != ClassDenied {
		t.Fatalf("login with a bad token = %v", err)
	}
	if err := c.Login(ctx, Auth{Method: AuthToken, Token: "root-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(ctx, "secret", "network/sw1", 1); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	last := k.headers[len(k.headers)-1]
	k.mu.Unlock()
	if last.Get("X-Vault-Token") != "root-token" {
		t.Fatal("the read did not carry the token")
	}
	for _, r := range k.seen() {
		if strings.HasPrefix(r, "POST") {
			t.Fatalf("a token login posted: %v", k.seen())
		}
	}
}

func TestReadVersion(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "")
	ctx := context.Background()
	if err := c.Login(ctx, Auth{Method: AuthToken, Token: "root-token"}); err != nil {
		t.Fatal(err)
	}
	s, err := c.Read(ctx, "/secret/", "/network/sw1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Data["snmp_ro"] != "community-v1" || s.Version != 1 {
		t.Fatalf("version 1 = %+v", s)
	}
	if got := k.seen(); got[len(got)-1] != "GET /v1/secret/data/network/sw1?version=1" {
		t.Fatalf("request = %s", got[len(got)-1])
	}
	if _, err = c.Read(ctx, "secret", "network/sw1", 9); Class(err) != ClassNotFound {
		t.Fatalf("missing version = %v", err)
	}
	if _, err = c.Read(ctx, "secret", "network/none", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing entry = %v", err)
	}
	mapped, err := MapFields(s.Data, map[string]string{"community": "snmp_ro"})
	if err != nil || mapped["community"] != "community-v1" {
		t.Fatalf("map = %v, %v", mapped, err)
	}
	if _, err = MapFields(s.Data, map[string]string{"community": "snmp_rw"}); Class(err) != ClassFieldMissing {
		t.Fatalf("missing field = %v", err)
	} else if strings.Contains(err.Error(), "community-v1") {
		t.Fatal("the error carries a value")
	}
}

func TestCacheTTLAndLease(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := NewCache(0, clock)
	k := CacheKey{Connection: "conn", Path: "network/sw1"}
	c.Put(k, &Secret{Data: map[string]string{"snmp_ro": "v"}, Version: 3})
	now = now.Add(DefaultTTL - time.Second)
	if s, ok := c.Get(k); !ok || s.Data["snmp_ro"] != "v" || s.Version != 3 {
		t.Fatalf("fresh entry = %+v, %v", s, ok)
	}
	now = now.Add(time.Second)
	if _, ok := c.Get(k); ok {
		t.Fatal("the entry outlived the default TTL of 300 s")
	}
	// A shorter lease caps the TTL; a longer one does not extend it.
	c.Put(k, &Secret{Data: map[string]string{"snmp_ro": "v"}, LeaseDuration: 30 * time.Second})
	now = now.Add(30 * time.Second)
	if _, ok := c.Get(k); ok {
		t.Fatal("the entry outlived its lease")
	}
	c.Put(k, &Secret{Data: map[string]string{"snmp_ro": "v"}, LeaseDuration: time.Hour})
	if exp, _ := c.Expires(k); exp.Sub(now) != DefaultTTL {
		t.Fatalf("expiry with a long lease = %v, want %v", exp.Sub(now), DefaultTTL)
	}
	// Versions are separate entries; Close overwrites the values.
	c.Put(CacheKey{Connection: "conn", Path: "network/sw1", Version: 1}, &Secret{Data: map[string]string{"snmp_ro": "old"}})
	if c.Len() != 2 {
		t.Fatalf("entries = %d", c.Len())
	}
	c.mu.Lock()
	held := c.entries[k].fields["snmp_ro"]
	c.mu.Unlock()
	c.Close()
	if c.Len() != 0 || string(held) != "\x00" {
		t.Fatalf("Close left %d entries, value %q", c.Len(), held)
	}
	// Sweep overwrites expired entries nobody asks for again; DeleteFunc
	// drops the entries of a reference that left the bundle.
	gone, kept := CacheKey{Connection: "conn", Path: "gone"}, CacheKey{Connection: "conn", Path: "kept"}
	c.Put(gone, &Secret{Data: map[string]string{"v": "x"}, LeaseDuration: time.Second})
	c.Put(kept, &Secret{Data: map[string]string{"v": "y"}})
	c.mu.Lock()
	held = c.entries[gone].fields["v"]
	c.mu.Unlock()
	now = now.Add(time.Second)
	c.Sweep()
	if _, ok := c.Get(kept); !ok || c.Len() != 1 || string(held) != "\x00" {
		t.Fatalf("Sweep: %d entries, expired value %q", c.Len(), held)
	}
	c.DeleteFunc(func(k CacheKey) bool { return k.Path == "kept" })
	if c.Len() != 0 {
		t.Fatalf("DeleteFunc left %d entries", c.Len())
	}
	// Another login or mount is another entry.
	if (CacheKey{Path: "p", Source: Fingerprint("https://a", "kv", "role")}) == (CacheKey{Path: "p", Source: Fingerprint("https://a", "kv2", "role")}) ||
		Fingerprint("a", "bc") == Fingerprint("ab", "c") {
		t.Fatal("the fingerprint does not tell the sources apart")
	}

	// The client's answers carry the lease the cache honors.
	kv, srv := newKV2(t)
	kv.lease = 60
	cl := newClient(t, srv, "")
	if err := cl.Login(context.Background(), Auth{Method: AuthToken, Token: "root-token"}); err != nil {
		t.Fatal(err)
	}
	s, err := cl.Read(context.Background(), "secret", "network/sw1", 0)
	if err != nil || s.LeaseDuration != time.Minute {
		t.Fatalf("lease = %v, %v", s, err)
	}
}

func TestDenied403(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "")
	ctx := context.Background()
	if err := c.Login(ctx, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Read(ctx, "secret", "network/secret", 0)
	if Class(err) != ClassDenied || !errors.Is(err, ErrDenied) {
		t.Fatalf("read of a denied path = %v", err)
	}
	if strings.Contains(err.Error(), "approle-token") || strings.Contains(err.Error(), "secret-1") {
		t.Fatalf("the error carries the token or secret id: %v", err)
	}
	// The token is valid, only the path is denied: no new login (it would
	// issue a token per read and use up the secret id).
	logins := func() int {
		n := 0
		for _, r := range k.seen() {
			if r == "POST /v1/auth/approle/login" {
				n++
			}
		}
		return n
	}
	for range 3 {
		_, _ = c.Read(ctx, "secret", "network/secret", 0)
	}
	if n := logins(); n != 1 {
		t.Fatalf("AppRole logins after denied reads = %d, want 1", n)
	}
	// A revoked token: one new login, then the read succeeds.
	k.mu.Lock()
	k.revoked["approle-token"] = true
	k.mu.Unlock()
	if s, rerr := c.Read(ctx, "secret", "network/sw1", 0); rerr != nil || s.Version != 2 {
		t.Fatalf("read after the revocation = %v, %v", s, rerr)
	}
	if n := logins(); n != 2 {
		t.Fatalf("AppRole logins after the revocation = %d, want 2", n)
	}
	// Unreachable: nobody listens.
	gone := httptest.NewTLSServer(http.NotFoundHandler())
	ca := caPEM(gone)
	addr := gone.URL
	gone.Close()
	dead, err := New(Config{Address: addr, CAPEM: ca, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = dead.Login(ctx, Auth{Method: AuthToken, Token: "root-token"}); Class(err) != ClassUnreachable {
		t.Fatalf("unreachable vault = %v", err)
	}
}

func TestNamespaceHeader(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "team-a")
	if err := c.Login(context.Background(), Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), "secret", "network/sw1", 0); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	headers, requests := k.headers, k.requests
	k.headers, k.requests = nil, nil
	k.mu.Unlock()
	for i, h := range headers {
		if h.Get("X-Vault-Namespace") != "team-a" {
			t.Fatalf("request %s without the namespace header", requests[i])
		}
	}
	plain := newClient(t, srv, "")
	if err := plain.Login(context.Background(), Auth{Method: AuthToken, Token: "root-token"}); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, set := k.headers[len(k.headers)-1]["X-Vault-Namespace"]; set {
		t.Fatal("the namespace header was sent without a namespace")
	}
}

func TestTransportRejectsWriteMethods(t *testing.T) {
	k, srv := newKV2(t)
	c := newClient(t, srv, "")
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodPost} {
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+"/v1/secret/data/x", strings.NewReader(`{"data":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.http.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrMethodNotAllowed) {
			t.Fatalf("%s /v1/secret/data/x = %v, want ErrMethodNotAllowed", method, err)
		}
	}
	if got := k.seen(); len(got) != 0 {
		t.Fatalf("writes reached the server: %v", got)
	}
	// The two login paths and reads pass.
	for _, ok := range [][2]string{{http.MethodPost, "/v1/auth/approle/login"}, {http.MethodPost, "/v1/auth/token/renew-self"},
		{http.MethodGet, "/v1/secret/data/x"}} {
		if !allowedRequest(ok[0], ok[1]) {
			t.Fatalf("%s %s refused", ok[0], ok[1])
		}
	}
}

// TestAddressWithPathPrefix: a vault behind a reverse proxy
// (https://host/vault) logs in, renews and reads below the prefix; the
// login paths are allowed only there, writes stay refused.
func TestAddressWithPathPrefix(t *testing.T) {
	k, _ := newKV2(t)
	proxy := httptest.NewTLSServer(http.StripPrefix("/vault", http.HandlerFunc(k.serve)))
	t.Cleanup(proxy.Close)
	c, err := New(Config{Address: proxy.URL + "/vault/", CAPEM: caPEM(proxy)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := context.Background()
	if err = c.Login(ctx, Auth{Method: AuthAppRole, RoleID: "role-1", SecretID: "secret-1"}); err != nil {
		t.Fatalf("AppRole login below the prefix: %v", err)
	}
	if err = c.Renew(ctx); err != nil {
		t.Fatalf("renewal below the prefix: %v", err)
	}
	if s, rerr := c.Read(ctx, "secret", "network/sw1", 0); rerr != nil || s.Version != 2 {
		t.Fatalf("read below the prefix = %v, %v", s, rerr)
	}
	before := len(k.seen())
	for _, p := range []string{"/vault/v1/secret/data/x", "/v1/auth/approle/login", "/other/v1/auth/approle/login"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+p, strings.NewReader(`{}`))
		resp, derr := c.http.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(derr, ErrMethodNotAllowed) {
			t.Fatalf("POST %s = %v, want ErrMethodNotAllowed", p, derr)
		}
	}
	if got := k.seen(); len(got) != before {
		t.Fatalf("refused requests reached the vault: %v", got[before:])
	}
}
