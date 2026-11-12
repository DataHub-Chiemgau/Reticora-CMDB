package readonly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// Operation of the client (ADM-03, WP-926): token bucket, retries,
// circuit breaker, token cache and errors without secrets, against an
// httptest ADMP with a fake clock and a fake authenticator.

const (
	testPassword = "admp-password-canary-6b1e"
	testUser     = "reticora"
)

// fakeClock advances on every sleep and records the waits.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d > 0 {
		f.sleeps = append(f.sleeps, d)
		f.t = f.t.Add(d)
	}
	return ctx.Err()
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func (f *fakeClock) waits() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sleeps
	f.sleeps = nil
	return out
}

func (f *fakeClock) clock() clock { return clock{now: f.now, sleep: f.sleep} }

// fakeSecrets is the SecretSource of the tests.
type fakeSecrets struct{ fail bool }

func (s fakeSecrets) Decrypt(_ context.Context, orgID, id string) (map[string]any, error) {
	if s.fail {
		return nil, fmt.Errorf("decrypt %s/%s failed with password %s", orgID, id, testPassword)
	}
	return map[string]any{"username": testUser, "password": testPassword}, nil
}

// fakeAuth logs in with the bound login request: it sends the secret to
// the auth URL and reads {"token","expires_in"}.
type fakeAuth struct {
	l      *login
	mu     sync.Mutex
	logins int
}

func (a *fakeAuth) bindLogin(l *login) { a.l = l }

func (a *fakeAuth) Token(ctx context.Context) (string, time.Time, error) {
	secret, err := a.l.secret(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	body, _ := json.Marshal(map[string]any{"user": secret["username"], "password": secret["password"]})
	status, raw, err := a.l.do(ctx, http.Header{"Content-Type": {"application/json"}}, body)
	if err != nil {
		return "", time.Time{}, err
	}
	if status != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("login answered %d: %s", status, raw)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return "", time.Time{}, err
	}
	a.mu.Lock()
	a.logins++
	a.mu.Unlock()
	return out.Token, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

func (a *fakeAuth) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.logins
}

// mockADMP is an httptest ADMP: the login at /auth/token and the reads
// below /api/v1, counting requests by method and path.
type mockADMP struct {
	mu     sync.Mutex
	hits   map[string]int
	tokens int
	// read answers a read; nil answers {"ok":true}.
	read func(w http.ResponseWriter, r *http.Request, n int)
	srv  *httptest.Server
	// tokenTTL is the expires_in of a login (seconds).
	tokenTTL int
}

func newMockADMP(t *testing.T) *mockADMP {
	t.Helper()
	m := &mockADMP{hits: map[string]int{}, tokenTTL: 600}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockADMP) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	key := r.Method + " " + r.URL.Path
	m.hits[key]++
	n := m.hits[key]
	m.mu.Unlock()
	if r.URL.Path == "/auth/token" {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != testPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		m.mu.Lock()
		m.tokens++
		tok := fmt.Sprintf("tok-%d-canary-%s", m.tokens, strings.Repeat("x", 8))
		ttl := m.tokenTTL
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": ttl})
		return
	}
	if m.read != nil {
		m.read(w, r, n)
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (m *mockADMP) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[key]
}

// total returns the requests with method other than "" (all for "").
func (m *mockADMP) total(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, v := range m.hits {
		if method == "" || strings.HasPrefix(k, method+" ") {
			n += v
		}
	}
	return n
}

// newTestClient builds a client of account on the mock with the fake
// clock; jitter is off.
func newTestClient(t *testing.T, m *mockADMP, account string, rps float64, fc *fakeClock) (*Client, *fakeAuth) {
	t.Helper()
	auth := &fakeAuth{}
	c, err := New(Config{AccountID: account, OrganizationID: "org-1", CredentialID: "cred-1",
		BaseURL: m.srv.URL + "/api/v1", AuthURL: m.srv.URL + "/auth/token", RateLimitRPS: rps, TimeoutMS: 5000},
		fakeSecrets{}, auth, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if fc != nil {
		c.clock = fc.clock()
	}
	c.jitter = func(time.Duration) time.Duration { return 0 }
	return c, auth
}

// TestClientRateLimitPerAccount: the token bucket of an account lets one
// read through every 1/rate_limit_rps (burst 1); another account has a
// bucket of its own.
func TestClientRateLimitPerAccount(t *testing.T) {
	m := newMockADMP(t)
	fc := newFakeClock()
	a, _ := newTestClient(t, m, "acc-rate-a", 2, fc)
	b, _ := newTestClient(t, m, "acc-rate-b", 2, fc)
	ctx := context.Background()
	start := fc.now()
	for i := range 5 {
		if err := a.getJSON(ctx, "services/1", nil, nil); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if elapsed := fc.now().Sub(start); elapsed != 2*time.Second {
		t.Fatalf("5 reads at 2/s took %v, want 2s (burst 1)", elapsed)
	}
	fc.waits()
	// The other account waits for nothing: its bucket is full.
	if err := b.getJSON(ctx, "services/1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if w := fc.waits(); len(w) != 0 {
		t.Fatalf("account b waited %v for account a's bucket", w)
	}
	if n := m.count("GET /api/v1/services/1"); n != 6 {
		t.Fatalf("reads at the mock = %d, want 6", n)
	}
	// Bounds: a rate out of bounds takes the default 2/s.
	c, err := New(Config{AccountID: "acc-rate-c", OrganizationID: "org-1", BaseURL: m.srv.URL, RateLimitRPS: 500},
		nil, nil, egress.Options{AllowPrivate: true})
	if err != nil || c.bucket.interval != 500*time.Millisecond {
		t.Fatalf("rate 500 = %v, %v; want the default interval 500ms", c.bucket.interval, err)
	}
}

// TestClientRetriesOnlyOn429And5xx: a GET is repeated after 429 (at least
// its Retry-After) and 5xx with exponential backoff, at most three times;
// 4xx and decode errors are not repeated; nothing but GET is retryable.
func TestClientRetriesOnlyOn429And5xx(t *testing.T) {
	m := newMockADMP(t)
	m.read = func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Path {
		case "/api/v1/flaky":
			if n <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/v1/down":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/v1/limited":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/api/v1/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/bad":
			w.WriteHeader(http.StatusBadRequest)
		case "/api/v1/garbage":
			_, _ = w.Write([]byte(`{not json`))
		}
	}
	fc := newFakeClock()
	c, _ := newTestClient(t, m, "acc-retry", 50, fc)
	ctx := context.Background()
	backoffs := func() []time.Duration {
		var out []time.Duration
		for _, w := range fc.waits() {
			if w >= retryBase {
				out = append(out, w)
			}
		}
		return out
	}

	var out struct{ OK bool }
	if err := c.getJSON(ctx, "flaky", nil, &out); err != nil || !out.OK {
		t.Fatalf("flaky = %v %+v", err, out)
	}
	if n := m.count("GET /api/v1/flaky"); n != 3 {
		t.Fatalf("flaky hits = %d, want 3", n)
	}
	if got := backoffs(); len(got) != 2 || got[0] != 250*time.Millisecond || got[1] != 500*time.Millisecond {
		t.Fatalf("backoffs = %v, want 250ms, 500ms", got)
	}

	cases := []struct {
		path  string
		class string
		hits  int
	}{
		{"down", ClassServer, 4},
		{"limited", ClassRateLimited, 4},
		{"missing", ClassClient, 1},
		{"bad", ClassClient, 1},
		{"garbage", ClassDecode, 1},
	}
	for _, tc := range cases {
		err := c.getJSON(ctx, tc.path, nil, &out)
		if ClassOf(err) != tc.class {
			t.Errorf("%s: class %q, want %s", tc.path, ClassOf(err), tc.class)
		}
		if n := m.count("GET /api/v1/" + tc.path); n != tc.hits {
			t.Errorf("%s: hits %d, want %d", tc.path, n, tc.hits)
		}
		got := backoffs()
		switch tc.path {
		case "down":
			if len(got) != 3 || got[2] != time.Second {
				t.Errorf("down: backoffs %v, want 250ms, 500ms, 1s", got)
			}
		case "limited":
			if len(got) != 3 || got[0] != 7*time.Second || got[1] != 7*time.Second || got[2] != 7*time.Second {
				t.Errorf("limited: backoffs %v, want Retry-After 7s each", got)
			}
		default:
			if len(got) != 0 {
				t.Errorf("%s: retried with backoffs %v", tc.path, got)
			}
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if retryable(method, http.StatusServiceUnavailable) || retryable(method, http.StatusTooManyRequests) {
			t.Errorf("%s is retryable", method)
		}
	}
	if !retryable(http.MethodGet, 500) || retryable(http.MethodGet, 404) || retryable(http.MethodGet, 501-100) {
		t.Error("retryable(GET) does not follow 429/5xx")
	}
}

// TestClientCircuitBreaker: five failed reads in a row open the breaker
// for 60 s (reads fail with circuit_open without a request); then one
// probe goes out: a failed probe opens it again, a successful one closes
// it.
func TestClientCircuitBreaker(t *testing.T) {
	m := newMockADMP(t)
	var healthy bool
	var mu sync.Mutex
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) {
		mu.Lock()
		defer mu.Unlock()
		if !healthy {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}
	setHealthy := func(v bool) { mu.Lock(); healthy = v; mu.Unlock() }
	fc := newFakeClock()
	c, _ := newTestClient(t, m, "acc-breaker", 50, fc)
	ctx := context.Background()
	gauge := func() float64 { return testutil.ToFloat64(circuitOpen.WithLabelValues("acc-breaker")) }

	for i := range breakerThreshold {
		if IsCircuitOpen(c) {
			t.Fatalf("open after %d failures", i)
		}
		if err := c.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassServer {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if !IsCircuitOpen(c) || gauge() != 1 {
		t.Fatalf("after 5 failures: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}
	before := m.total(http.MethodGet)
	err := c.getJSON(ctx, "x", nil, nil)
	if ClassOf(err) != ClassCircuitOpen || !errors.Is(err, ErrCircuitOpen) || m.total(http.MethodGet) != before {
		t.Fatalf("read while open: %v, %d new requests", err, m.total(http.MethodGet)-before)
	}

	// Half open after 60 s: the failed probe opens it again at once.
	fc.advance(breakerOpenFor)
	if IsCircuitOpen(c) {
		t.Fatal("still open after 60 s")
	}
	if err = c.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassServer {
		t.Fatalf("probe: %v", err)
	}
	if !IsCircuitOpen(c) {
		t.Fatal("a failed probe left the breaker closed")
	}
	if err = c.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassCircuitOpen {
		t.Fatalf("after the failed probe: %v", err)
	}

	// The next probe succeeds: closed, the gauge 0.
	fc.advance(breakerOpenFor)
	setHealthy(true)
	if err = c.getJSON(ctx, "x", nil, nil); err != nil {
		t.Fatalf("successful probe: %v", err)
	}
	if IsCircuitOpen(c) || gauge() != 0 {
		t.Fatalf("after the successful probe: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}
	// A 4xx is no failure of ADMP: it does not count.
	setHealthy(false)
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(http.StatusNotFound) }
	for range 2 * breakerThreshold {
		_ = c.getJSON(ctx, "x", nil, nil)
	}
	if IsCircuitOpen(c) {
		t.Fatal("404 answers opened the breaker")
	}

	// A half-open probe answered with a 404 closes the breaker, and the
	// gauge follows at once.
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(http.StatusInternalServerError) }
	for range breakerThreshold {
		_ = c.getJSON(ctx, "x", nil, nil)
	}
	if !IsCircuitOpen(c) || gauge() != 1 {
		t.Fatalf("reopened: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}
	fc.advance(breakerOpenFor)
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(http.StatusNotFound) }
	if err = c.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassClient {
		t.Fatalf("404 probe: %v", err)
	}
	if IsCircuitOpen(c) || gauge() != 0 {
		t.Fatalf("after the 404 probe: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}
}

// TestClientCanceledReadIsNoBreakerFailure: a read the caller ends (a
// canceled context before the bucket or during the request) is the class
// canceled and does not count against the circuit breaker; a canceled
// half-open probe leaves the next probe free.
func TestClientCanceledReadIsNoBreakerFailure(t *testing.T) {
	m := newMockADMP(t)
	fc := newFakeClock()
	c, _ := newTestClient(t, m, "acc-cancel", 50, fc)
	gauge := func() float64 { return testutil.ToFloat64(circuitOpen.WithLabelValues("acc-cancel")) }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 2 * breakerThreshold {
		if err := c.getJSON(canceled, "x", nil, nil); ClassOf(err) != ClassCanceled {
			t.Fatalf("canceled read %d: %v", i, err)
		}
	}
	if IsCircuitOpen(c) || gauge() != 0 {
		t.Fatalf("canceled reads opened the breaker: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}

	// Canceled while ADMP is answering: canceled as well, no failure.
	var (
		inFlight   sync.Mutex
		cancelRead context.CancelFunc
	)
	exited := make(chan struct{}, 1)
	m.read = func(_ http.ResponseWriter, r *http.Request, _ int) {
		inFlight.Lock()
		cancelRead()
		inFlight.Unlock()
		<-r.Context().Done()
		exited <- struct{}{}
	}
	for i := range breakerThreshold {
		ctx, cancelFn := context.WithCancel(context.Background())
		inFlight.Lock()
		cancelRead = cancelFn
		inFlight.Unlock()
		err := c.getJSON(ctx, "x", nil, nil)
		cancelFn()
		<-exited // the handler is done before the next read
		if ClassOf(err) != ClassCanceled {
			t.Fatalf("read %d canceled in flight: %v", i, err)
		}
	}
	if IsCircuitOpen(c) {
		t.Fatal("reads canceled in flight opened the breaker")
	}

	// A canceled half-open probe: no outcome, the next read is the probe.
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(http.StatusInternalServerError) }
	for range breakerThreshold {
		_ = c.getJSON(context.Background(), "x", nil, nil)
	}
	fc.advance(breakerOpenFor)
	if err := c.getJSON(canceled, "x", nil, nil); ClassOf(err) != ClassCanceled {
		t.Fatalf("canceled probe: %v", err)
	}
	m.read = func(w http.ResponseWriter, _ *http.Request, _ int) { _, _ = w.Write([]byte(`{}`)) }
	if err := c.getJSON(context.Background(), "x", nil, nil); err != nil {
		t.Fatalf("probe after the canceled one: %v", err)
	}
	if IsCircuitOpen(c) || gauge() != 0 {
		t.Fatalf("after the probe: open %v, gauge %v", IsCircuitOpen(c), gauge())
	}
}

// TestClientTokenCacheAndRenewOn401: the token is cached until its expiry
// minus 60 s; a 401 drops it and logs in once more; a second 401 is the
// class auth without a further login.
func TestClientTokenCacheAndRenewOn401(t *testing.T) {
	m := newMockADMP(t)
	var reject string
	var mu sync.Mutex
	m.read = func(w http.ResponseWriter, r *http.Request, _ int) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer tok-") || (reject != "" && strings.Contains(auth, reject)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}
	setReject := func(v string) { mu.Lock(); reject = v; mu.Unlock() }
	c, auth := newTestClient(t, m, "acc-token", 50, nil)
	// The cache runs on the wall clock of the fake authenticator.
	ctx := context.Background()
	for range 3 {
		if err := c.getJSON(ctx, "x", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if auth.count() != 1 || m.count("POST /auth/token") != 1 {
		t.Fatalf("logins for three reads = %d, want 1", auth.count())
	}
	// Within 60 s of the expiry the token is renewed.
	c.tokens.mu.Lock()
	c.tokens.expires = time.Now().Add(59 * time.Second)
	c.tokens.mu.Unlock()
	if err := c.getJSON(ctx, "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if auth.count() != 2 {
		t.Fatalf("logins after the margin = %d, want 2", auth.count())
	}
	// A 401 on the current token: one more login, then success.
	setReject("tok-2-")
	if err := c.getJSON(ctx, "x", nil, nil); err != nil {
		t.Fatalf("read after a 401: %v", err)
	}
	if auth.count() != 3 {
		t.Fatalf("logins after a 401 = %d, want 3", auth.count())
	}
	// Every token refused: one renewal, then auth.
	setReject("tok-")
	err := c.getJSON(ctx, "x", nil, nil)
	if ClassOf(err) != ClassAuth || auth.count() != 4 {
		t.Fatalf("always 401: %v after %d logins, want auth after 4", err, auth.count())
	}
	// A failing login is the class auth, a failing secret credential.
	failing, err := New(Config{AccountID: "acc-token-2", OrganizationID: "org-1", CredentialID: "c",
		BaseURL: m.srv.URL + "/api/v1", AuthURL: m.srv.URL + "/auth/token"}, fakeSecrets{fail: true}, &fakeAuth{}, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = failing.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassCredential {
		t.Fatalf("failing secret: %v", err)
	}
	noAuth, err := New(Config{AccountID: "acc-token-3", OrganizationID: "org-1", BaseURL: m.srv.URL + "/api/v1"},
		nil, nil, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = noAuth.getJSON(ctx, "x", nil, nil); ClassOf(err) != ClassAuth || !errors.Is(err, ErrNoAuthenticator) {
		t.Fatalf("without authenticator: %v", err)
	}
}

// TestClientErrorsCarryNoSecret: error texts and metric labels name no
// token, password or answer, whatever ADMP answers.
func TestClientErrorsCarryNoSecret(t *testing.T) {
	const marker = "answer-body-canary-91c4"
	m := newMockADMP(t)
	m.read = func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/api/v1/server":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/client":
			w.WriteHeader(http.StatusBadRequest)
		case "/api/v1/redirect":
			http.Redirect(w, r, "http://other.invalid/"+marker, http.StatusFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"error":"%s","echo":"%s"} trailing`, marker, r.Header.Get("Authorization"))
	}
	fc := newFakeClock()
	c, _ := newTestClient(t, m, "acc-secret", 50, fc)
	failing, err := New(Config{AccountID: "acc-secret-2", OrganizationID: "org-1", CredentialID: "c",
		BaseURL: m.srv.URL + "/api/v1", AuthURL: m.srv.URL + "/auth/token"}, fakeSecrets{fail: true}, &fakeAuth{}, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var texts []string
	for _, p := range []string{"server", "client", "decode", "redirect", "../" + marker, "http://x/" + testPassword} {
		var out struct{ OK bool }
		if err = c.getJSON(ctx, p, nil, &out); err == nil {
			t.Fatalf("%s: no error", p)
		}
		texts = append(texts, err.Error(), fmt.Sprintf("%+v", err))
	}
	if err = failing.getJSON(ctx, "x", nil, nil); err == nil {
		t.Fatal("failing secret: no error")
	}
	texts = append(texts, err.Error())

	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				texts = append(texts, lp.GetName()+"="+lp.GetValue())
			}
		}
	}
	for _, text := range texts {
		for _, secret := range []string{testPassword, "tok-", marker, "Bearer", m.srv.URL} {
			if strings.Contains(text, secret) {
				t.Errorf("%q carries %q", text, secret)
			}
		}
	}
}
