package admp_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// contractMock is an ADMP built from its documentation (WP-1077, E-255):
// the operations of readonly/testdata/admp-api-9.14.0.json (the copy of
// docs/plan/phase3/ADMP-API) answered with the fixtures of
// readonly/testdata/admp_fixtures.json by operationId. It serves as
// base_url; the login is POST /V1/user/authentication with {username,
// password} and sets Set-Cookie: AuthToken=<JWT with exp>. Every GET needs
// a valid, unexpired cookie (else 401). It counts requests per method and
// path, records the time of each GET and simulates on demand an expired
// token (401), 429 with Retry-After and 5xx.
type contractMock struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	ops      []mockOperation
	fixtures map[string]docFixture
	hits     map[string]int
	gets     []time.Time
	tokens   map[string]time.Time
	logins   int
	// username and password the login accepts.
	username, password string
	// expireNext makes the next GET find every token expired (401).
	expireNext bool
	// fail429 answers the next GETs with 429 and Retry-After retryAfter.
	fail429    int
	retryAfter string
	// fail5xx answers the next GETs with 503.
	fail5xx int
	// tokenTTL is the lifetime of a token (exp claim and Max-Age).
	tokenTTL time.Duration
}

// mockOperation is an operation of the documentation.
type mockOperation struct {
	method string
	segs   []string
	id     string
	path   string
}

// mockAllowlist is the allowlist of the plan (E-248, introduction of
// M3.14): the login and seven GET path templates.
var mockAllowlist = []string{
	"POST /V1/user/authentication",
	"GET /V1/user/authentication",
	"GET /V1/infrastructure/accessnodepicker",
	"GET /V1/infrastructure/port/{portId}/dienstIds",
	"GET /V1/dienst/{id}",
	"GET /V1/dienst/{id}/properties/public",
	"GET /V1/anschluss/{id}",
	"GET /V1/organization/byIds",
}

func newContractMock(t *testing.T, username, password string) *contractMock {
	t.Helper()
	m := &contractMock{t: t, fixtures: map[string]docFixture{}, hits: map[string]int{}, tokens: map[string]time.Time{},
		username: username, password: password, tokenTTL: 10 * time.Minute, retryAfter: "1"}
	raw, err := os.ReadFile("readonly/testdata/admp-api-9.14.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for p, ops := range doc.Paths {
		for method, op := range ops {
			m.ops = append(m.ops, mockOperation{method: strings.ToUpper(method), segs: strings.Split(strings.TrimPrefix(p, "/"), "/"),
				id: op.OperationID, path: p})
		}
	}
	raw, err = os.ReadFile("readonly/testdata/admp_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	for key, v := range all {
		if strings.HasPrefix(key, "_") {
			continue
		}
		var f docFixture
		if err = json.Unmarshal(v, &f); err != nil {
			t.Fatalf("fixture %s: %v", key, err)
		}
		m.fixtures[key] = f
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

// match returns the operation of method and path and the value of its
// first path parameter; known reports whether the path is one of the
// documentation with any method. The path template with the most literal
// segments wins (GET /V1/dienst/paginator is the paginator, not
// /V1/dienst/{id}), independent of the order of m.ops; a method the best
// template does not document is no operation, even when a template with
// a parameter in its place has it.
func (m *contractMock) match(method, path string) (op *mockOperation, param string, known bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	best, bestLiterals := "", -1
	values := map[string]string{}
	for i := range m.ops {
		o := &m.ops[i]
		if len(o.segs) != len(segs) {
			continue
		}
		value, fits, literals := "", true, 0
		for j, s := range o.segs {
			if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
				if segs[j] == "" {
					fits = false
					break
				}
				if value == "" {
					value = segs[j]
				}
				continue
			}
			if s != segs[j] {
				fits = false
				break
			}
			literals++
		}
		if !fits {
			continue
		}
		values[o.path] = value
		if literals > bestLiterals || (literals == bestLiterals && o.path < best) {
			best, bestLiterals = o.path, literals
		}
	}
	if bestLiterals < 0 {
		return nil, "", false
	}
	for i := range m.ops {
		if o := &m.ops[i]; o.path == best && o.method == method {
			return o, values[best], true
		}
	}
	return nil, "", true
}

// jwt builds a token with sub and exp (unsigned for the mock).
func mockJWT(n int, exp time.Time) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := enc.EncodeToString(fmt.Appendf(nil, `{"sub":"svc-%d","exp":%d}`, n, exp.Unix()))
	return header + "." + claims + "." + enc.EncodeToString(fmt.Appendf(nil, "mock-signature-%d", n))
}

func (m *contractMock) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.hits[r.Method+" "+r.URL.Path]++
	m.mu.Unlock()
	op, param, known := m.match(r.Method, r.URL.Path)
	switch {
	case op == nil && known:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	case op == nil:
		w.WriteHeader(http.StatusNotFound)
		return
	case op.id == "postV1UserAuthentication":
		m.login(w, r)
		return
	case r.Method != http.MethodGet:
		// A write of the documentation: the client never sends one; the
		// test counts it.
		w.WriteHeader(http.StatusForbidden)
		return
	}
	m.mu.Lock()
	m.gets = append(m.gets, time.Now())
	if m.expireNext {
		m.expireNext = false
		m.tokens = map[string]time.Time{}
	}
	cookie, err := r.Cookie("AuthToken")
	valid := err == nil && m.tokens[cookie.Value].After(time.Now())
	fail429, fail5xx := m.fail429 > 0, m.fail5xx > 0
	if valid && fail429 {
		m.fail429--
	} else if valid && fail5xx {
		m.fail5xx--
	}
	f, found := m.fixtures[op.id+"#"+param]
	if !found {
		f, found = m.fixtures[op.id]
	}
	retryAfter := m.retryAfter
	m.mu.Unlock()
	switch {
	case !valid:
		w.WriteHeader(http.StatusUnauthorized)
		return
	case fail429:
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		return
	case fail5xx:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case !found:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"name":"NotFoundError","message":"not found"}`))
		return
	}
	for k, v := range f.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}

// login answers POST /V1/user/authentication: the body is exactly
// {username, password}; a valid one sets a new token.
func (m *contractMock) login(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 2 || body["username"] != m.username ||
		body["password"] != m.password || r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	m.mu.Lock()
	m.logins++
	exp := time.Now().Add(m.tokenTTL)
	token := mockJWT(m.logins, exp)
	m.tokens[token] = exp
	f := m.fixtures["postV1UserAuthentication"]
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "AuthToken", Value: token, Path: "/", HttpOnly: true, MaxAge: int(m.tokenTTL.Seconds())})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.Body)
}

// count returns the requests of "METHOD /path".
func (m *contractMock) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[key]
}

// snapshot returns the requests per method and path.
func (m *contractMock) snapshot() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.hits))
	for k, v := range m.hits {
		out[k] = v
	}
	return out
}

// nonGet returns the requests other than GET per method and path.
func (m *contractMock) nonGet() map[string]int {
	out := map[string]int{}
	for k, v := range m.snapshot() {
		if !strings.HasPrefix(k, http.MethodGet+" ") {
			out[k] = v
		}
	}
	return out
}

// getsOutsideAllowlist lists the GETs whose path is no template of the
// allowlist.
func (m *contractMock) getsOutsideAllowlist() []string {
	var out []string
	for k := range m.snapshot() {
		method, path, _ := strings.Cut(k, " ")
		if method != http.MethodGet {
			continue
		}
		op, _, _ := m.match(method, path)
		if op == nil || !slices.Contains(mockAllowlist, method+" "+op.path) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// getTimes returns the times of the GETs since index from.
func (m *contractMock) getTimes() []time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Time(nil), m.gets...)
}

// set changes the simulation under the lock.
func (m *contractMock) set(fn func(m *contractMock)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

// TestContractMockPrefersLiteralSegments: the mock assigns a literal path
// of the documentation to its own operation, not to a template with a
// parameter in its place, whatever the order of the operations; so a GET
// of a paginator counts as a GET outside the allowlist.
func TestContractMockPrefersLiteralSegments(t *testing.T) {
	m := newContractMock(t, "svc", "pw")
	for range 20 {
		rand.Shuffle(len(m.ops), func(i, j int) { m.ops[i], m.ops[j] = m.ops[j], m.ops[i] })
		for path, want := range map[string]string{"/V1/dienst/paginator": "/V1/dienst/paginator",
			"/V1/anschluss/paginator": "/V1/anschluss/paginator", "/V1/dienst/815": "/V1/dienst/{id}"} {
			op, _, known := m.match(http.MethodGet, path)
			if !known || op == nil || op.path != want {
				t.Fatalf("GET %s: %+v known %v, want the operation %s", path, op, known, want)
			}
		}
		if op, _, known := m.match(http.MethodDelete, "/V1/dienst/paginator"); op != nil || !known {
			t.Fatalf("DELETE /V1/dienst/paginator: %+v known %v, want a known path without operation", op, known)
		}
	}
}
