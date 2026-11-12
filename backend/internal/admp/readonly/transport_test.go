package readonly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err := c.getJSON(ctx, "services/42", map[string][]string{"q": {"a b"}}, nil); err != nil {
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
// exactly the configured method on exactly the auth URL; another method,
// another path, query or origin is refused. The login method is POST by
// default, may be GET, and nothing else.
func TestAuthTransportAllowsOnlyTheLoginRequest(t *testing.T) {
	m := newMockADMP(t)
	c, _ := newTestClient(t, m, "acc-auth", 50, nil)
	if c.login.method != http.MethodPost {
		t.Fatalf("default login method %s", c.login.method)
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
	for _, tc := range []struct {
		method, url string
		want        error
	}{
		{http.MethodGet, m.srv.URL + "/auth/token", ErrMethodNotAllowed},
		{http.MethodPut, m.srv.URL + "/auth/token", ErrMethodNotAllowed},
		{http.MethodPatch, m.srv.URL + "/auth/token", ErrMethodNotAllowed},
		{http.MethodDelete, m.srv.URL + "/auth/token", ErrMethodNotAllowed},
		{http.MethodPost, m.srv.URL + "/api/v1/services/1", ErrDestinationNotAllowed},
		{http.MethodPost, m.srv.URL + "/auth/token?x=1", ErrDestinationNotAllowed},
		{http.MethodPost, m.srv.URL + "/auth/token/", ErrDestinationNotAllowed},
	} {
		if err := try(tc.method, tc.url); !errors.Is(err, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.url, err, tc.want)
		}
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the login reached another origin")
	}))
	defer other.Close()
	if err := try(http.MethodPost, other.URL+"/auth/token"); !errors.Is(err, ErrDestinationNotAllowed) {
		t.Errorf("POST to another origin: %v", err)
	}
	if n := m.total(""); n != 0 {
		t.Fatalf("refused logins reached the server: %v", m.hits)
	}
	if err := try(http.MethodPost, m.srv.URL+"/auth/token"); err != nil || m.count("POST /auth/token") != 1 {
		t.Fatalf("the login request: %v", err)
	}
	// The data transport refuses the login request: the two never share
	// a transport.
	if c.http.Transport == c.login.client.Transport {
		t.Fatal("client and login share a transport")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.srv.URL+"/auth/token", http.NoBody)
	if _, err := c.http.Transport.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("login over the data transport: %v", err)
	}

	// GET as login method; any other method is no valid configuration.
	g, err := New(Config{AccountID: "acc-auth-get", OrganizationID: "org-1", BaseURL: m.srv.URL + "/api/v1",
		AuthURL: m.srv.URL + "/auth/token", AuthMethod: "get"}, nil, nil, egress.Options{AllowPrivate: true})
	if err != nil || g.login.method != http.MethodGet {
		t.Fatalf("login method get: %v", err)
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, "TRACE"} {
		if _, err = New(Config{AccountID: "acc-auth-x", OrganizationID: "org-1", BaseURL: m.srv.URL,
			AuthURL: m.srv.URL + "/auth/token", AuthMethod: method}, nil, nil, egress.Options{AllowPrivate: true}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("login method %s accepted: %v", method, err)
		}
	}
	// Without an auth URL no login request is possible at all.
	none, err := New(Config{AccountID: "acc-auth-none", OrganizationID: "org-1", BaseURL: m.srv.URL + "/api/v1"},
		nil, nil, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = none.login.do(ctx, nil, nil); !errors.Is(err, ErrDestinationNotAllowed) {
		t.Fatalf("login without auth URL: %v", err)
	}
	// Invalid URLs are no configuration.
	for _, base := range []string{"", "ftp://x", "https://user:pw@x", "https://x/?q=1", "https://x/#f", "/relative"} {
		if _, err = New(Config{AccountID: "a", OrganizationID: "o", BaseURL: base}, nil, nil, egress.Options{}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("base URL %q accepted", base)
		}
	}
}
