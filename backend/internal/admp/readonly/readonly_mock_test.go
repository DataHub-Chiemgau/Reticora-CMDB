// Proof of the read-only guarantee, layer (d): a mock ADMP counts no write
// (ADM-02, E-224, WP-927).
//
// The endpoint work packages (WP-1073 to WP-1076) extend this test with
// their endpoints (E-224: it stays a standing test); no change may loosen
// it (DoD in .github/pull_request_template.md).
package readonly

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// callExported calls every exported method of c with zero values (a
// background context for a context parameter) and returns their names.
func callExported(t *testing.T, c *Client) []string {
	t.Helper()
	v := reflect.ValueOf(c)
	ctxType := reflect.TypeFor[context.Context]()
	var names []string
	for i := range v.NumMethod() {
		m := v.Type().Method(i)
		args := make([]reflect.Value, 0, m.Type.NumIn()-1)
		for j := 1; j < m.Type.NumIn(); j++ {
			in := m.Type.In(j)
			if in == ctxType {
				args = append(args, reflect.ValueOf(context.Background()))
				continue
			}
			args = append(args, reflect.Zero(in))
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s panics on zero values: %v", m.Name, r)
				}
			}()
			v.Method(i).Call(args)
		}()
		names = append(names, m.Name)
	}
	return names
}

// TestMockServerSeesNoWriteRequests: a mock ADMP serves as base URL
// (/api/v1) and auth URL (/auth/token) and counts requests by method and
// path. The test calls every exported method of *Client, reads once, then
// tries POST, PUT, PATCH and DELETE against both URLs over the client's
// transport, POST against the base URL over the login transport, and
// manipulated paths over getJSON. The mock sees no request at the base
// URL but GET, at the auth URL only the logins with the configured method,
// nothing else.
func TestMockServerSeesNoWriteRequests(t *testing.T) {
	m := newMockADMP(t)
	foreign := newMockADMP(t)
	c, auth := newTestClient(t, m, "acc-mock", 50, nil)
	ctx := context.Background()
	baseURL, authURL := m.srv.URL+"/api/v1", m.srv.URL+"/auth/token"

	called := callExported(t, c)
	t.Logf("exported methods called: %v", called)
	if err := c.getJSON(ctx, "services/1", nil, nil); err != nil {
		t.Fatalf("read: %v", err)
	}

	writes := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	try := func(hc *http.Client, method, target string) error {
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(`{"x":1}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hc.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	for _, method := range writes {
		for _, target := range []string{baseURL + "/services/1", baseURL, authURL} {
			if err := try(c.http, method, target); err == nil {
				t.Errorf("%s %s over the client went through", method, target)
			}
			req, _ := http.NewRequestWithContext(ctx, method, target, http.NoBody)
			if _, err := c.http.Transport.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
				t.Errorf("%s %s over the inner transport: %v", method, target, err)
			}
		}
	}
	// The login transport takes its one request and nothing else.
	for _, target := range []string{baseURL + "/services/1", baseURL} {
		if err := try(c.login.client, http.MethodPost, target); !errors.Is(err, ErrDestinationNotAllowed) {
			t.Errorf("POST %s over the login transport: %v", target, err)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodGet} {
		if err := try(c.login.client, method, authURL); !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s on the auth URL over the login transport: %v", method, err)
		}
	}
	// Manipulated paths never leave the base URL.
	for _, p := range []string{"..", "../auth/token", "%2e%2e/auth/token", authURL, foreign.srv.URL + "/api/v1/x", "//" + strings.TrimPrefix(foreign.srv.URL, "http://") + "/x"} {
		if err := c.getJSON(ctx, p, nil, nil); err == nil {
			t.Errorf("getJSON %q went through", p)
		}
	}

	m.mu.Lock()
	hits := make(map[string]int, len(m.hits))
	for k, v := range m.hits {
		hits[k] = v
	}
	m.mu.Unlock()
	logins := 0
	for key, n := range hits {
		method, path, _ := strings.Cut(key, " ")
		switch {
		case path == "/auth/token" && method == c.login.method:
			logins += n
		case strings.HasPrefix(path, "/api/v1/") && method == http.MethodGet:
		default:
			t.Errorf("the mock saw %d × %s", n, key)
		}
	}
	if logins != auth.count() || logins != 1 {
		t.Errorf("logins at the auth URL = %d, authenticator logged in %d times, want 1", logins, auth.count())
	}
	if hits["GET /api/v1/services/1"] != 1 {
		t.Errorf("reads at the base URL = %v", hits)
	}
	if n := foreign.total(""); n != 0 {
		t.Errorf("the foreign host saw %d requests", n)
	}

	// The same with GET as configured login method: the login is a GET
	// at the auth URL, a POST there is refused.
	getAuth := &fakeAuth{}
	g, err := New(Config{AccountID: "acc-mock-get", OrganizationID: "org-1", CredentialID: "c",
		BaseURL: baseURL, AuthURL: authURL, AuthMethod: http.MethodGet}, fakeSecrets{}, getAuth, egress.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = try(g.login.client, http.MethodPost, authURL); !errors.Is(err, ErrMethodNotAllowed) {
		t.Errorf("POST on the auth URL with login method GET: %v", err)
	}
	if n := m.count("POST /auth/token"); n != 1 {
		t.Errorf("POST logins = %d after the GET client, want still 1", n)
	}
}
