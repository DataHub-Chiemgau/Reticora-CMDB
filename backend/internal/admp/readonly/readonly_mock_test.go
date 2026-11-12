// Proof of the read-only guarantee, layer (d): a mock ADMP counts no write
// (ADM-02, E-224, E-248, WP-927, extended by WP-1073).
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

// allowedGetPath reports whether a path the mock saw (below /api/v1) is a
// route of the allowlist.
func allowedGetPath(path string) bool {
	rel, ok := strings.CutPrefix(path, "/api/v1")
	if !ok {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	for i := range allowedGets {
		if allowedGets[i].matches(segs) {
			return true
		}
	}
	return false
}

// TestMockServerSeesNoWriteRequests: a mock ADMP serves as base URL
// (/api/v1) with the documented login at /api/v1/V1/user/authentication
// and counts requests per pair of method and path. The test builds a client
// with the documented login (no fake authenticator) and the allowlist
// alone, calls every exported method of *Client with zero values, reads
// once, then tries POST, PUT, PATCH and DELETE against the base URL and
// the login URL over the client's transport, every method but POST on the
// login URL and POST elsewhere over the login transport, and manipulated
// paths and GETs outside the allowlist over getJSON. The set of non-GET
// pairs the mock saw is exactly {POST /V1/user/authentication} with one
// request; DELETE /V1/user/authentication (the logout) and every other
// non-GET pair have none; every GET the mock saw is on the allowlist.
func TestMockServerSeesNoWriteRequests(t *testing.T) {
	m := newMockADMP(t)
	foreign := newMockADMP(t)
	c := proofClient(t, m, "acc-mock", nil)
	ctx := context.Background()
	baseURL, loginURL := m.srv.URL+"/api/v1", m.srv.URL+mockLogin

	called := callExported(t, c)
	t.Logf("exported methods called: %v", called)
	for _, want := range []string{"GetSession", "ListAccessPorts", "ListPortServiceIDs", "GetService", "ListServiceProperties",
		"GetConnection", "GetOrganization"} {
		found := false
		for _, n := range called {
			found = found || n == want
		}
		if !found {
			t.Errorf("the reflection did not call %s", want)
		}
	}
	if _, err := c.GetService(ctx, 815); err != nil && ClassOf(err) != ClassDecode {
		t.Fatalf("read: %v", err)
	}
	if _, err := c.ListServiceProperties(ctx, 815); err != nil && ClassOf(err) != ClassDecode {
		t.Fatalf("read of the properties: %v", err)
	}
	if _, err := c.GetConnection(ctx, 4711); err != nil && ClassOf(err) != ClassDecode {
		t.Fatalf("read of the connection: %v", err)
	}
	if _, err := c.GetOrganization(ctx, 9); err != nil && ClassOf(err) != ClassDecode {
		t.Fatalf("read of the carrier: %v", err)
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
		for _, target := range []string{baseURL + "/V1/dienst/1", baseURL + "/V1/anschluss", baseURL + "/V1/query/execute/x",
			baseURL + "/V1/dienst/1/properties", baseURL + "/V1/dienst/1/properties/public", baseURL + "/V1/anschluss/1",
			baseURL + "/V1/organization/byIds?id=1", baseURL, loginURL} {
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
	for _, target := range []string{baseURL + "/V1/dienst/1", baseURL, baseURL + "/V1/query/execute/x", m.srv.URL + authPath} {
		if err := try(c.login.client, http.MethodPost, target); !errors.Is(err, ErrDestinationNotAllowed) {
			t.Errorf("POST %s over the login transport: %v", target, err)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions} {
		if err := try(c.login.client, method, loginURL); !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s on the login URL over the login transport: %v", method, err)
		}
	}
	// Manipulated paths never leave the base URL; GETs outside the
	// allowlist never leave the client.
	for _, p := range []string{"..", "../V1/user/authentication", "%2e%2e/x", loginURL, foreign.srv.URL + "/api/v1/V1/dienst/1",
		"//" + strings.TrimPrefix(foreign.srv.URL, "http://") + "/x"} {
		if err := c.getJSON(ctx, p, nil, nil); err == nil {
			t.Errorf("getJSON %q went through", p)
		}
	}
	for _, p := range unlistedGets {
		if err := c.getJSON(ctx, p, nil, nil); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("GET %s: %v, want ErrPathNotAllowed", p, err)
		}
	}

	m.mu.Lock()
	hits := make(map[string]int, len(m.hits))
	for k, v := range m.hits {
		hits[k] = v
	}
	m.mu.Unlock()
	nonGet := map[string]int{}
	for key, n := range hits {
		method, path, _ := strings.Cut(key, " ")
		switch {
		case method != http.MethodGet:
			nonGet[key] += n
		case !allowedGetPath(path):
			t.Errorf("the mock saw %d × %s outside the allowlist", n, key)
		}
	}
	if len(nonGet) != 1 || nonGet["POST "+mockLogin] != 1 {
		t.Errorf("non-GET requests at the mock: %v, want exactly POST %s once", nonGet, mockLogin)
	}
	if n := hits["DELETE "+mockLogin]; n != 0 {
		t.Errorf("the mock saw %d logouts", n)
	}
	if hits["GET /api/v1/V1/dienst/815"] != 1 || hits["GET /api/v1/V1/dienst/815/properties/public"] != 1 ||
		hits["GET /api/v1"+sessionPath] != 1 || hits["GET /api/v1"+accessNodePickerPath] != 1 ||
		hits["GET /api/v1/V1/anschluss/4711"] != 1 || hits["GET /api/v1"+organizationsPath] != 1 {
		t.Errorf("reads at the base URL = %v", hits)
	}
	if n := foreign.total(""); n != 0 {
		t.Errorf("the foreign host saw %d requests", n)
	}
}
