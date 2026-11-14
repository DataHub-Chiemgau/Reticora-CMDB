package readonly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The guarding transports (ADM-02, WP-926) against httptest servers with
// egress.Options{AllowPrivate: true}: nothing but GET reaches the base
// URL, nothing but the login request the auth URL; refusals happen before
// the network (the servers count no hit).

// TestReadOnlyTransportAllowsOnlyGet: POST, PUT, PATCH, DELETE, HEAD and
// OPTIONS, and a GET with a body, end with ErrMethodNotAllowed; the
// server counts no hit. A plain GET passes.
func TestReadOnlyTransportAllowsOnlyGet(t *testing.T) {
	m := newMockADMP(t)
	c, _ := newTestClient(t, m, "acc-transport", 50, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodHead, http.MethodOptions, "PROPFIND"} {
		req, err := http.NewRequestWithContext(context.Background(), method, m.srv.URL+"/api/v1/services/1", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.http.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s: %v, want ErrMethodNotAllowed", method, err)
		}
	}
	withBody, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, m.srv.URL+"/api/v1/services/1", strings.NewReader("x"))
	if resp, err := c.http.Do(withBody); !errors.Is(err, ErrMethodNotAllowed) {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Errorf("GET with a body: %v", err)
	}
	if n := m.total(""); n != 0 {
		t.Fatalf("the server saw %d requests, want 0", n)
	}
	// The transport alone refuses as well (no client in between).
	rt := c.http.Transport
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req, _ := http.NewRequestWithContext(context.Background(), method, m.srv.URL+"/api/v1/x", http.NoBody)
		if _, err := rt.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("RoundTrip %s: %v", method, err)
		}
	}
	get, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, m.srv.URL+"/api/v1/services/1", http.NoBody)
	resp, err := c.http.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if m.total("") != 1 || m.count("GET /api/v1/services/1") != 1 {
		t.Fatalf("hits after one GET: %v", m.hits)
	}
}

// TestReadOnlyTransportRejectsForeignHostAndRedirect: a GET to another
// origin (another port of the same host as well) and a redirect hop to it
// end with ErrDestinationNotAllowed; the foreign server counts no hit.
func TestReadOnlyTransportRejectsForeignHostAndRedirect(t *testing.T) {
	m := newMockADMP(t)
	foreign := newMockADMP(t)
	m.read = func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/api/v1/away":
			http.Redirect(w, r, foreign.srv.URL+"/api/v1/x", http.StatusFound)
		case "/api/v1/outside":
			http.Redirect(w, r, "/admin", http.StatusFound)
		case "/api/v1/inside":
			http.Redirect(w, r, "/api/v1/target", http.StatusFound)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}
	c, _ := newTestClient(t, m, "acc-foreign", 50, nil)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, foreign.srv.URL+"/api/v1/x", http.NoBody)
	if resp, err := c.http.Do(req); !errors.Is(err, ErrDestinationNotAllowed) {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("GET to another origin: %v", err)
	}
	for _, p := range []string{"away", "outside"} {
		err := c.getJSON(context.Background(), p, nil, nil)
		if !errors.Is(err, ErrDestinationNotAllowed) || ClassOf(err) != ClassMethodNotAllowed {
			t.Errorf("redirect %s: %v", p, err)
		}
	}
	if n := foreign.total(""); n != 0 {
		t.Fatalf("the foreign server saw %d requests", n)
	}
	if m.count("GET /admin") != 0 {
		t.Fatal("a redirect left the prefix of the base URL")
	}
	// A redirect below the prefix is followed.
	if err := c.getJSON(context.Background(), "inside", nil, nil); err != nil || m.count("GET /api/v1/target") != 1 {
		t.Fatalf("redirect below the prefix: %v", err)
	}
}

// TestReadOnlyPathStaysBelowBaseURL: getJSON resolves a path relative to
// the base URL; absolute URLs, dot segments (also encoded) and paths that
// leave the prefix are refused before the network.
func TestReadOnlyPathStaysBelowBaseURL(t *testing.T) {
	m := newMockADMP(t)
	c, _ := newTestClient(t, m, "acc-path", 50, nil)
	ctx := context.Background()
	for _, p := range []string{"../admin", "/../admin", "a/../../admin", "%2e%2e/admin", "a/%2E%2E/%2e%2e/admin",
		"./x", "http://evil.example/x", "//evil.example/x", "https:x", `a\..\..\x`, "x?redirect=http://evil", "x#frag"} {
		err := c.getJSON(ctx, p, nil, nil)
		if !errors.Is(err, ErrInvalidPath) || ClassOf(err) != ClassMethodNotAllowed {
			t.Errorf("path %q: %v, want ErrInvalidPath", p, err)
		}
	}
	if n := m.total(""); n != 0 {
		t.Fatalf("refused paths reached the server: %v", m.hits)
	}
	if err := c.getJSON(ctx, "services/42", map[string][]string{"q": {"7"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.getJSON(ctx, "/services/43", nil, nil); err != nil {
		t.Fatal(err)
	}
	if m.count("GET /api/v1/services/42") != 1 || m.count("GET /api/v1/services/43") != 1 {
		t.Fatalf("hits = %v", m.hits)
	}
	// The transport checks the prefix on its own, also against a sibling
	// with the same leading characters.
	for _, raw := range []string{m.srv.URL + "/api/v10/x", m.srv.URL + "/api", m.srv.URL + "/api/v1/%2e%2e/admin"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, http.NoBody)
		if _, err := c.http.Transport.RoundTrip(req); !errors.Is(err, ErrDestinationNotAllowed) {
			t.Errorf("RoundTrip %s: %v", raw, err)
		}
	}
	if n := m.total(http.MethodGet); n != 2 {
		t.Fatalf("hits = %v", m.hits)
	}
}

// TestAuthTransportAllowsOnlyTheLoginRequest: the login transport passes
// exactly POST on exactly base URL + /V1/user/authentication; another
// method, another path, query or origin is refused. A configured auth URL
// other than that one leaves the login without a URL.
func TestAuthTransportAllowsOnlyTheLoginRequest(t *testing.T) {
	m := newMockADMP(t)
	c, _ := newTestClient(t, m, "acc-auth", 50, nil)
	if c.login.method != http.MethodPost || c.login.url.String() != m.srv.URL+mockLogin {
		t.Fatalf("login %s %v", c.login.method, c.login.url)
	}
	ctx := context.Background()
	try := func(method, raw string) error {
		req, _ := http.NewRequestWithContext(ctx, method, raw, strings.NewReader(`{"password":"`+testPassword+`"}`))
		resp, err := c.login.client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	login := m.srv.URL + mockLogin
	for _, tc := range []struct {
		method, url string
		want        error
	}{
		{http.MethodPost, m.srv.URL + "/api/v1/services/1", ErrDestinationNotAllowed},
		{http.MethodPost, login + "?x=1", ErrDestinationNotAllowed},
		{http.MethodPost, login + "/", ErrDestinationNotAllowed},
		{http.MethodPost, m.srv.URL + authPath, ErrDestinationNotAllowed},
	} {
		if err := try(tc.method, tc.url); !errors.Is(err, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.url, err, tc.want)
		}
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the login reached another origin")
	}))
	defer other.Close()
	if err := try(http.MethodPost, other.URL+mockLogin); !errors.Is(err, ErrDestinationNotAllowed) {
		t.Errorf("POST to another origin: %v", err)
	}
	if n := m.total(""); n != 0 {
		t.Fatalf("refused logins reached the server: %v", m.hits)
	}
	if err := try(http.MethodPost, login); err != nil || m.count("POST "+mockLogin) != 1 {
		t.Fatalf("the login request: %v", err)
	}
	// The data transport refuses the login request: the two never share
	// a transport.
	if c.http.Transport == c.login.client.Transport {
		t.Fatal("client and login share a transport")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, login, http.NoBody)
	if _, err := c.http.Transport.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("login over the data transport: %v", err)
	}
	// A configured auth URL is used only when it is exactly the login URL.
	same, err := New(Config{AccountID: "acc-auth-same", OrganizationID: "org-1", BaseURL: m.srv.URL + "/api/v1/",
		AuthURL: login}, nil, nil, egress.Options{AllowPrivate: true})
	if err != nil || same.login.client == nil || same.login.url.String() != login {
		t.Fatalf("auth URL equal to the login URL: %v", err)
	}
	foreign, err := New(Config{AccountID: "acc-auth-foreign", OrganizationID: "org-1", BaseURL: m.srv.URL + "/api/v1",
		AuthURL: other.URL + mockLogin}, nil, nil, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = foreign.login.do(ctx, nil, nil); !errors.Is(err, ErrDestinationNotAllowed) || ClassOf(err) != ClassAuth {
		t.Fatalf("login with a foreign auth URL: %v", err)
	}
	// Invalid URLs are no configuration.
	for _, base := range []string{"", "ftp://x", "https://user:pw@x", "https://x/?q=1", "https://x/#f", "/relative"} {
		if _, err = New(Config{AccountID: "a", OrganizationID: "o", BaseURL: base}, nil, nil, egress.Options{}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("base URL %q accepted", base)
		}
	}
}

// TestAuthTransportRejectsNonPostOnAuthPath: DELETE (the logout of the
// documentation), PATCH, PUT, GET, HEAD and OPTIONS on
// /V1/user/authentication over the login transport are refused before the
// network; the mock counts no hit.
func TestAuthTransportRejectsNonPostOnAuthPath(t *testing.T) {
	m := newMockADMP(t)
	c, _ := newTestClient(t, m, "acc-auth-methods", 50, nil)
	ctx := context.Background()
	for _, method := range []string{http.MethodDelete, http.MethodPatch, http.MethodPut, http.MethodGet, http.MethodHead,
		http.MethodOptions} {
		req, _ := http.NewRequestWithContext(ctx, method, m.srv.URL+mockLogin, http.NoBody)
		resp, err := c.login.client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s on the login path: %v, want ErrMethodNotAllowed", method, err)
		}
		inner, _ := http.NewRequestWithContext(ctx, method, m.srv.URL+mockLogin, http.NoBody)
		if _, err = c.login.client.Transport.RoundTrip(inner); !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s on the login path over the inner transport: %v", method, err)
		}
	}
	if n := m.total(""); n != 0 {
		t.Fatalf("the mock saw %v", m.hits)
	}
}

// proofClient builds a client on the mock with the allowlist alone (no
// test routes) and the given authenticator (nil: the documented login).
func proofClient(t *testing.T, m *mockADMP, account string, auth Authenticator) *Client {
	t.Helper()
	c, err := New(Config{AccountID: account, OrganizationID: "org-1", CredentialID: "cred-1", BaseURL: m.srv.URL + "/api/v1",
		RateLimitRPS: 50, TimeoutMS: 5000}, fakeSecrets{}, auth, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	c.jitter = func(time.Duration) time.Duration { return 0 }
	return c
}

// unlistedGets are the GETs of the documentation outside the allowlist
// (E-248, introduction of M3.14).
var unlistedGets = []string{
	"/V1/admin/logs/paginator", "/V1/user/authorization", "/V1/user/authorization/groups", "/V1/user/authorization/nodes",
	"/V1/anschluss/paginator", "/V1/anschluss/inhaber/paginator", "/V1/anschluss/7/actions", "/V1/dienst/paginator",
	"/V1/dienst/7/properties", "/V1/dienst/7/actions", "/V1/dienst/template/picker", "/V1/action/all",
	"/V1/processor/procedure/schema", "/V1/processor/process/paginator", "/V1/processor/definition/step/paginator",
	"/V1/changelog/paginator",
}

// TestReadOnlyTransportAllowsOnlyListedPaths: the 16 GETs of the
// documentation outside the allowlist, an unknown path, a query the
// allowlist does not name (also on the picker) and the paths of the
// allowlist with a suffix end with ErrPathNotAllowed (class
// path_not_allowed) before the network; the listed paths pass.
func TestReadOnlyTransportAllowsOnlyListedPaths(t *testing.T) {
	m := newMockADMP(t)
	auth := &fakeAuth{}
	c := proofClient(t, m, "acc-allowlist", auth)
	ctx := context.Background()
	refused := append([]string{"/V1/unknown", "/V1/dienst/7/properties/public/x", "/V1/dienst", "/V1/infrastructure/port/7",
		"/V1/user/authentication/x", "/V1"}, unlistedGets...)
	for _, p := range refused {
		err := c.getJSON(ctx, p, nil, nil)
		if !errors.Is(err, ErrPathNotAllowed) || ClassOf(err) != ClassPathNotAllowed {
			t.Errorf("GET %s: %v, want ErrPathNotAllowed", p, err)
		}
	}
	for _, q := range []map[string][]string{{"technology": {"DSL"}}, {"organizationId": {"1"}}} {
		if err := c.getJSON(ctx, accessNodePickerPath, q, nil); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("picker with query %v: %v", q, err)
		}
	}
	for _, p := range []string{sessionPath, accessNodePickerPath, "/V1/infrastructure/port/12/dienstIds", "/V1/dienst/815"} {
		if err := c.getJSON(ctx, p, nil, nil); err != nil {
			t.Errorf("GET %s: %v", p, err)
		}
	}
	for key, n := range m.hits {
		method, path, _ := strings.Cut(key, " ")
		if method == http.MethodGet && !strings.HasPrefix(path, "/api/v1/V1/user/authentication") &&
			!strings.HasPrefix(path, "/api/v1/V1/infrastructure/") && !strings.HasPrefix(path, "/api/v1/V1/dienst/815") {
			t.Errorf("the mock saw %d × %s", n, key)
		}
	}
	if m.total(http.MethodGet) != 4 {
		t.Errorf("GETs at the mock: %v, want the 4 listed", m.hits)
	}
}

// TestReadOnlyTransportRejectsNonNumericIDs: a path id must be a decimal
// id of 1 to 16 digits without a leading zero; anything else (also
// encoded) ends with ErrPathNotAllowed before the network.
func TestReadOnlyTransportRejectsNonNumericIDs(t *testing.T) {
	m := newMockADMP(t)
	c := proofClient(t, m, "acc-ids", &fakeAuth{})
	ctx := context.Background()
	for _, id := range []string{"abc", "0", "007", "-1", "1.5", "1e3", "12345678901234567", "%31", "1%2F2", "1;x", " 1"} {
		err := c.getJSON(ctx, "/V1/dienst/"+id, nil, nil)
		if err == nil || (!errors.Is(err, ErrPathNotAllowed) && !errors.Is(err, ErrInvalidPath)) {
			t.Errorf("id %q: %v, want a refusal", id, err)
		}
	}
	if n := m.total(http.MethodGet); n != 0 {
		t.Fatalf("refused ids reached the mock: %v", m.hits)
	}
	if err := c.getJSON(ctx, "/V1/dienst/1234567890123456", nil, nil); err != nil {
		t.Fatalf("16 digits: %v", err)
	}
	if _, err := c.GetService(ctx, 0); err == nil || m.total(http.MethodGet) != 1 {
		t.Fatalf("GetService(0): %v, %v", err, m.hits)
	}
}
