package readonly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The login of the documentation (E-249, E-250, WP-1073) against an
// httptest ADMP that answers with the fixtures of
// testdata/admp_fixtures.json (E-255).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestAuthenticator|TestList|TestGet|TestUndocumented' ./internal/admp/readonly/

// fixtureJWT is the token of the login fixture (exp 2031-01-01).
const fixtureJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIiwiZXhwIjoxOTI0OTkyMDAwfQ.c2lnbmF0dXJlLWNhbmFyeQ"

// fixture is one answer of admp_fixtures.json.
type fixture struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func loadFixtures(t *testing.T) map[string]fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/admp_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	out := map[string]fixture{}
	for op, v := range all {
		if strings.HasPrefix(op, "_") {
			continue
		}
		var f fixture
		if err = json.Unmarshal(v, &f); err != nil {
			t.Fatalf("fixture %s: %v", op, err)
		}
		out[op] = f
	}
	return out
}

// fixtureADMP serves the fixtures below /admp (the base URL) and counts
// requests per method and path. A GET without the session cookie of the
// last login is 401.
type fixtureADMP struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	fixtures map[string]fixture
	hits     map[string]int
	logins   []loginRecord
	// get, when set, answers a GET before the fixtures (true: answered).
	get func(w http.ResponseWriter, r *http.Request) bool
	// token is the cookie value the next login sets ("" keeps the
	// fixture's).
	tokens int
}

// loginRecord is a login request as the mock saw it.
type loginRecord struct {
	contentType string
	body        []byte
}

const fixtureBase = "/admp"

func newFixtureADMP(t *testing.T) *fixtureADMP {
	t.Helper()
	m := &fixtureADMP{t: t, fixtures: loadFixtures(t), hits: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

// operation maps a request to the operationId of the documentation.
func operation(method, rel string) string {
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	switch {
	case method == http.MethodPost && rel == authPath:
		return "postV1UserAuthentication"
	case method == http.MethodGet && rel == sessionPath:
		return "getV1UserAuthentication"
	case method == http.MethodGet && rel == accessNodePickerPath:
		return "getV1InfrastructureAccessnodepicker"
	case method == http.MethodGet && len(segs) == 5 && segs[1] == "infrastructure" && segs[2] == "port" && segs[4] == "dienstIds":
		return "getV1InfrastructurePort:portIdDienstIds"
	case method == http.MethodGet && len(segs) == 3 && segs[1] == "dienst":
		return "getV1Dienst:id"
	case method == http.MethodGet && len(segs) == 5 && segs[1] == "dienst" && segs[3] == "properties" && segs[4] == "public":
		return "getV1Dienst:idPropertiesPublic"
	}
	return ""
}

func (m *fixtureADMP) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.hits[r.Method+" "+r.URL.Path]++
	m.mu.Unlock()
	rel, ok := strings.CutPrefix(r.URL.Path, fixtureBase)
	op := operation(r.Method, rel)
	if !ok || op == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if op == "postV1UserAuthentication" {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.logins = append(m.logins, loginRecord{contentType: r.Header.Get("Content-Type"), body: body})
		m.tokens++
		m.mu.Unlock()
	} else {
		if m.get != nil && m.get(w, r) {
			return
		}
		if c, err := r.Cookie(authCookie); err != nil || c.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	m.mu.Lock()
	f, found := m.fixtures[op]
	m.mu.Unlock()
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	for k, v := range f.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}

// set replaces the fixture of an operation.
func (m *fixtureADMP) set(op string, f fixture) {
	m.mu.Lock()
	m.fixtures[op] = f
	m.mu.Unlock()
}

func (m *fixtureADMP) fixture(op string) fixture {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fixtures[op]
}

func (m *fixtureADMP) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[key]
}

func (m *fixtureADMP) total(method string) int {
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

// docSecrets is the api_token credential of E-249: username and token
// (the password).
type docSecrets struct{ fields map[string]any }

func (s docSecrets) Decrypt(context.Context, string, string) (map[string]any, error) {
	return s.fields, nil
}

var validLogin = map[string]any{"username": testUser, "token": testPassword}

// docClient builds a client with the documented login on the fixture
// mock; authURL "" is the default.
func docClient(t *testing.T, m *fixtureADMP, authURL string, secret map[string]any, fc *fakeClock) *Client {
	t.Helper()
	c, err := New(Config{AccountID: "acc-doc", OrganizationID: "org-1", CredentialID: "cred-1", BaseURL: m.srv.URL + fixtureBase,
		AuthURL: authURL, RateLimitRPS: 50, TimeoutMS: 5000}, docSecrets{fields: secret}, nil, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if fc != nil {
		c.clock = fc.clock()
	}
	c.jitter = func(time.Duration) time.Duration { return 0 }
	return c
}

// TestAuthenticatorPostsDocBody: the login is exactly POST
// /V1/user/authentication with Content-Type application/json and the body
// {username, password} (no other member); the following GET carries the
// cookie AuthToken.
func TestAuthenticatorPostsDocBody(t *testing.T) {
	m := newFixtureADMP(t)
	var cookie string
	m.get = func(_ http.ResponseWriter, r *http.Request) bool {
		cookie = r.Header.Get("Cookie")
		if r.Header.Get("Authorization") != "" {
			t.Error("a GET carries an Authorization header")
		}
		return false
	}
	c := docClient(t, m, "", validLogin, nil)
	if _, err := c.GetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := m.count("POST " + fixtureBase + authPath); n != 1 || len(m.logins) != 1 {
		t.Fatalf("logins %d, hits %v", n, m.hits)
	}
	l := m.logins[0]
	if l.contentType != "application/json" {
		t.Errorf("Content-Type %q", l.contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(l.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["username"] != testUser || body["password"] != testPassword {
		t.Errorf("login body %s, want exactly {username, password}", l.body)
	}
	if cookie != authCookie+"="+fixtureJWT {
		t.Errorf("GET cookie %q", cookie)
	}
}

// TestAuthenticatorReadsAuthTokenCookie: the expiry is Max-Age, else
// Expires, else the exp claim of the JWT, else 10 minutes; a cookie that
// deletes itself is no token. The client caches the token until its expiry
// minus 60 s.
func TestAuthenticatorReadsAuthTokenCookie(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	header := func(line string) http.Header { return http.Header{"Set-Cookie": {"Other=x; Path=/", line}} }
	for _, tc := range []struct {
		name, line string
		want       time.Time
		ok         bool
	}{
		{"max-age", "AuthToken=" + fixtureJWT + "; Max-Age=3600; Path=/", now.Add(time.Hour), true},
		{"expires", "AuthToken=abc; Expires=" + now.Add(2*time.Hour).Format(http.TimeFormat), now.Add(2 * time.Hour), true},
		{"jwt exp", "AuthToken=" + fixtureJWT + "; Path=/; HttpOnly", time.Unix(1924992000, 0), true},
		{"default", "AuthToken=opaque-token; Path=/", now.Add(10 * time.Minute), true},
		{"deleted", "AuthToken=x; Max-Age=0", time.Time{}, false},
		{"expired", "AuthToken=x; Expires=" + now.Add(-time.Hour).Format(http.TimeFormat), time.Time{}, false},
		{"empty", "AuthToken=; Path=/", time.Time{}, false},
	} {
		token, exp, ok := sessionCookie(header(tc.line), now)
		if ok != tc.ok || !exp.Equal(tc.want) || (ok && token == "") {
			t.Errorf("%s: %q %v %v, want %v %v", tc.name, token, exp, ok, tc.want, tc.ok)
		}
	}
	// Through the client: the fixture's JWT expires in 2031, a cookie with
	// Max-Age 120 is renewed after 60 s.
	m := newFixtureADMP(t)
	fc := newFakeClock()
	c := docClient(t, m, "", validLogin, fc)
	ctx := context.Background()
	login := m.fixture("postV1UserAuthentication")
	login.Headers = map[string]string{"Set-Cookie": "AuthToken=short-token; Max-Age=120; Path=/"}
	m.set("postV1UserAuthentication", login)
	for range 2 {
		if _, err := c.GetSession(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(m.logins); n != 1 {
		t.Fatalf("logins %d, want 1", n)
	}
	fc.advance(61 * time.Second)
	if _, err := c.GetSession(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(m.logins); n != 2 {
		t.Fatalf("logins after 61 s of a 120 s token: %d, want 2", n)
	}
}

// TestAuthenticatorRenewsOn401: a 401 on a GET drops the token, logs in
// exactly once more and repeats the GET once; a second 401 is the class
// auth.
func TestAuthenticatorRenewsOn401(t *testing.T) {
	m := newFixtureADMP(t)
	rejectFirst := true
	m.get = func(w http.ResponseWriter, _ *http.Request) bool {
		if rejectFirst {
			rejectFirst = false
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		return false
	}
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	if _, err := c.GetSession(ctx); err != nil {
		t.Fatalf("read after a 401: %v", err)
	}
	if len(m.logins) != 2 || m.count("GET "+fixtureBase+sessionPath) != 2 {
		t.Fatalf("logins %d, hits %v; want 2 logins and 2 GETs", len(m.logins), m.hits)
	}
	m.get = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	_, err := c.GetSession(ctx)
	if ClassOf(err) != ClassAuth || len(m.logins) != 3 || m.count("GET "+fixtureBase+sessionPath) != 4 {
		t.Fatalf("always 401: %v, logins %d, hits %v; want auth after one renewal", err, len(m.logins), m.hits)
	}
}

// TestAuthenticator403IsAuthWithoutRenew: a 403 is the class auth at once,
// without a second login.
func TestAuthenticator403IsAuthWithoutRenew(t *testing.T) {
	m := newFixtureADMP(t)
	m.get = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	c := docClient(t, m, "", validLogin, nil)
	_, err := c.GetSession(context.Background())
	var e *Error
	if ClassOf(err) != ClassAuth || !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("403: %v, want auth", err)
	}
	if len(m.logins) != 1 || m.count("GET "+fixtureBase+sessionPath) != 1 {
		t.Fatalf("logins %d, hits %v; want one login and one GET", len(m.logins), m.hits)
	}
}

// TestAuthenticatorRejectsForeignAuthOrigin: an auth URL with another
// host, another port, http instead of https, an additional prefix or
// another path ends the login with the class auth before any request
// (also the credential is not sent); no mock sees a request.
func TestAuthenticatorRejectsForeignAuthOrigin(t *testing.T) {
	m := newFixtureADMP(t)
	other := newFixtureADMP(t)
	httpsBase := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the TLS server saw a request")
	}))
	defer httpsBase.Close()
	host := strings.TrimPrefix(m.srv.URL, "http://")
	for name, authURL := range map[string]string{
		"foreign host":     "http://admp-other.invalid" + fixtureBase + authPath,
		"foreign port":     other.srv.URL + fixtureBase + authPath,
		"https not http":   "https://" + host + fixtureBase + authPath,
		"extra prefix":     m.srv.URL + fixtureBase + "/x" + authPath,
		"foreign path":     m.srv.URL + fixtureBase + "/V1/user/login",
		"no base prefix":   m.srv.URL + authPath,
		"trailing segment": m.srv.URL + fixtureBase + authPath + "/x",
	} {
		c := docClient(t, m, authURL, validLogin, nil)
		if _, err := c.GetSession(context.Background()); ClassOf(err) != ClassAuth {
			t.Errorf("%s: %v, want auth", name, err)
		}
	}
	// The other direction: base https, auth http on the same host.
	c, err := New(Config{AccountID: "acc-tls", OrganizationID: "org-1", CredentialID: "c", BaseURL: httpsBase.URL + fixtureBase,
		AuthURL: "http://" + strings.TrimPrefix(httpsBase.URL, "https://") + fixtureBase + authPath},
		docSecrets{fields: validLogin}, nil, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetSession(context.Background()); ClassOf(err) != ClassAuth {
		t.Errorf("http auth URL for an https base: %v", err)
	}
	if n := m.total("") + other.total(""); n != 0 {
		t.Fatalf("the mocks saw %v and %v", m.hits, other.hits)
	}
}

// TestAuthenticatorMissingCookieIsAuth: a 200 without the cookie AuthToken
// (or with another cookie only), and a 401 of the login, are the class
// auth; a credential without username or token is credential.
func TestAuthenticatorMissingCookieIsAuth(t *testing.T) {
	m := newFixtureADMP(t)
	ctx := context.Background()
	for name, f := range map[string]fixture{
		"no cookie":    {Status: http.StatusOK, Body: json.RawMessage(`{"message":"ok"}`)},
		"other cookie": {Status: http.StatusOK, Headers: map[string]string{"Set-Cookie": "Session=x"}, Body: json.RawMessage(`{}`)},
		"401":          {Status: http.StatusUnauthorized, Body: json.RawMessage(`{"message":"wrong"}`)},
	} {
		m.set("postV1UserAuthentication", f)
		c := docClient(t, m, "", validLogin, nil)
		if _, err := c.GetSession(ctx); ClassOf(err) != ClassAuth {
			t.Errorf("%s: %v, want auth", name, err)
		}
	}
	if n := m.total(http.MethodGet); n != 0 {
		t.Errorf("reads without a session: %v", m.hits)
	}
	before := m.total("")
	for name, secret := range map[string]map[string]any{
		"no username": {"token": testPassword},
		"no token":    {"username": testUser},
		"password":    {"username": testUser, "password": testPassword},
	} {
		c := docClient(t, m, "", secret, nil)
		if _, err := c.GetSession(ctx); ClassOf(err) != ClassCredential {
			t.Errorf("%s: %v, want credential", name, err)
		}
	}
	if m.total("") != before {
		t.Errorf("a login without complete credential reached the mock: %v", m.hits)
	}
}

// TestAuthenticatorErrorCarriesNoToken: no error of a login or a read
// names the token, the password, the user or the answer.
func TestAuthenticatorErrorCarriesNoToken(t *testing.T) {
	const marker = "login-answer-canary-5e2a"
	m := newFixtureADMP(t)
	ctx := context.Background()
	var texts []string
	m.set("postV1UserAuthentication", fixture{Status: http.StatusUnauthorized, Body: json.RawMessage(`{"message":"` + marker + `"}`)})
	_, err := docClient(t, m, "", validLogin, nil).GetSession(ctx)
	texts = append(texts, err.Error(), fmt.Sprintf("%+v", err))
	m.set("postV1UserAuthentication", fixture{Status: http.StatusOK,
		Headers: map[string]string{"Set-Cookie": "AuthToken=" + fixtureJWT + "; Path=/"}, Body: json.RawMessage(`{}`)})
	m.get = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"echo":%q,"marker":%q}`, r.Header.Get("Cookie"), marker)
		return true
	}
	_, err = docClient(t, m, "", validLogin, nil).GetSession(ctx)
	texts = append(texts, err.Error(), fmt.Sprintf("%+v", err))
	if ClassOf(err) != ClassAuth {
		t.Fatalf("401: %v", err)
	}
	for _, text := range texts {
		for _, secret := range []string{fixtureJWT, testPassword, testUser, marker, authCookie} {
			if strings.Contains(text, secret) {
				t.Errorf("%q carries %q", text, secret)
			}
		}
	}
}

// TestAuthenticatorNeverLogsOut: logins, renewals, failures and a new
// client of the same account never send DELETE /V1/user/authentication
// (or any other method than POST there).
func TestAuthenticatorNeverLogsOut(t *testing.T) {
	m := newFixtureADMP(t)
	fc := newFakeClock()
	ctx := context.Background()
	c := docClient(t, m, "", validLogin, fc)
	for range 3 {
		_, _ = c.GetSession(ctx)
		_, _ = c.ListAccessPorts(ctx)
		fc.advance(time.Hour)
	}
	m.get = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	_, _ = c.GetSession(ctx)
	_, _ = docClient(t, m, "", validLogin, fc).GetSession(ctx)
	for key, n := range m.hits {
		method, path, _ := strings.Cut(key, " ")
		if method != http.MethodGet && (method != http.MethodPost || path != fixtureBase+authPath) {
			t.Errorf("the mock saw %d × %s", n, key)
		}
	}
	if n := m.count("DELETE " + fixtureBase + authPath); n != 0 {
		t.Errorf("%d logouts", n)
	}
	if len(m.logins) < 2 {
		t.Errorf("logins %d, the test did not renew", len(m.logins))
	}
}
