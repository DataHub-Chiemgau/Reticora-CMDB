// Proof of the read-only guarantee against the operations of the ADMP
// documentation itself (ADM-02, E-248, E-255, WP-1077): every operation of
// the copy testdata/admp-api-9.14.0.json, which equals
// docs/plan/phase3/ADMP-API, is tried over the transports of the client;
// only the GETs of the allowlist and the login pass, nothing else reaches
// the mock.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestSpec|TestAllowlistMatchesPlan|TestAdmpSpecCopyMatchesDocs' ./internal/admp/readonly/
package readonly

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// docsSpec is the documentation as delivered (A-55).
const docsSpec = "../../../../docs/plan/phase3/ADMP-API"

// specOperation is one operation of the documentation.
type specOperation struct {
	method, path, id string
}

// specOperations lists the operations of the copy, sorted.
func specOperations(t *testing.T) []specOperation {
	t.Helper()
	doc := loadSpec(t)
	paths, _ := doc["paths"].(map[string]any)
	var out []specOperation
	for p, item := range paths {
		ops, _ := item.(map[string]any)
		for method, op := range ops {
			o, isOp := op.(map[string]any)
			if !isOp {
				continue
			}
			id, _ := o["operationId"].(string)
			out = append(out, specOperation{method: strings.ToUpper(method), path: p, id: id})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].method < out[j].method
	})
	return out
}

// specPaths lists the paths of the copy, sorted.
func specPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, op := range specOperations(t) {
		if !slices.Contains(out, op.path) {
			out = append(out, op.path)
		}
	}
	return out
}

// concrete fills the path parameters of a template: 1 for the ids, x for
// the name of a query.
func concrete(template string) string {
	segs := strings.Split(template, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			if s == "{name}" {
				segs[i] = "x"
			} else {
				segs[i] = "1"
			}
		}
	}
	return strings.Join(segs, "/")
}

// shape is a path template with anonymous parameters.
func shape(template string) string {
	segs := strings.Split(template, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// allowedShapes are the GET templates of the allowlist, anonymous.
func allowedShapes() []string {
	out := make([]string, 0, len(allowedGets))
	for _, r := range allowedGets {
		out = append(out, shape(r.path))
	}
	sort.Strings(out)
	return out
}

// otherMethods are the methods besides GET a request may name.
var otherMethods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead,
	http.MethodOptions, http.MethodTrace, http.MethodConnect}

// TestAdmpSpecCopyMatchesDocs: the copy in testdata is the documentation
// unchanged (SHA-256).
func TestAdmpSpecCopyMatchesDocs(t *testing.T) {
	sum := func(path string) [32]byte {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(raw)
	}
	if a, b := sum(specCopy), sum(docsSpec); a != b {
		t.Fatalf("%s (%x) differs from %s (%x)", specCopy, a, docsSpec, b)
	}
	ops := specOperations(t)
	counts := map[string]int{}
	for _, op := range ops {
		counts[op.method]++
	}
	if len(ops) != 46 || counts[http.MethodGet] != 23 || counts[http.MethodPost] != 10 || counts[http.MethodPatch] != 8 ||
		counts[http.MethodDelete] != 5 {
		t.Errorf("operations of the documentation: %d %v, want 46 (23 GET, 10 POST, 8 PATCH, 5 DELETE)", len(ops), counts)
	}
}

// TestSpecNonGetOperationsAreUnreachable: for every path of the
// documentation, the login path included, and every method of POST, PUT,
// PATCH, DELETE, HEAD, OPTIONS, TRACE and CONNECT the request is refused
// by the inner transport of the client and by the login transport before
// the network; only POST /V1/user/authentication over the login transport
// is left out. The mock sees no request. That covers the 22 non-GET
// operations of the documentation besides the login, DELETE
// /V1/user/authentication and POST /V1/query/execute/{name} in
// particular, and PUT, which the documentation does not use.
func TestSpecNonGetOperationsAreUnreachable(t *testing.T) {
	m := newMockADMP(t)
	c := proofClient(t, m, "acc-spec-write", nil)
	ctx := context.Background()
	base := m.srv.URL + "/api/v1"
	if c.login.client == nil {
		t.Fatal("the client has no login transport")
	}
	inner, login := c.http.Transport, c.login.client.Transport
	covered := map[string]bool{}
	for _, p := range specPaths(t) {
		target := base + concrete(p)
		for _, method := range otherMethods {
			req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(`{"x":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = inner.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
				t.Errorf("%s %s over the inner transport: %v, want ErrMethodNotAllowed", method, p, err)
			}
			covered[method+" "+p] = true
			if method == authMethod && p == authPath {
				continue // the login, the one request of the login transport
			}
			req, err = http.NewRequestWithContext(ctx, method, target, strings.NewReader(`{"x":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = login.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) && !errors.Is(err, ErrDestinationNotAllowed) {
				t.Errorf("%s %s over the login transport: %v, want a refusal", method, p, err)
			}
		}
	}
	// Every non-GET operation of the documentation is among the tried
	// pairs; there are 22 besides the login.
	var forbidden []string
	for _, op := range specOperations(t) {
		if op.method == http.MethodGet {
			continue
		}
		if !covered[op.method+" "+op.path] {
			t.Errorf("%s %s (%s) was not tried", op.method, op.path, op.id)
		}
		if op.method != authMethod || op.path != authPath {
			forbidden = append(forbidden, op.method+" "+op.path)
		}
	}
	if len(forbidden) != 22 || !slices.Contains(forbidden, "DELETE "+authPath) || !slices.Contains(forbidden, "POST /V1/query/execute/{name}") {
		t.Errorf("forbidden non-GET operations %v, want 22 with the logout and the named query", forbidden)
	}
	// The login over the inner transport is refused as well.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+authPath, strings.NewReader(`{}`))
	if _, err := inner.RoundTrip(req); !errors.Is(err, ErrMethodNotAllowed) {
		t.Errorf("POST %s over the inner transport: %v, want ErrMethodNotAllowed", authPath, err)
	}
	if n := m.total(""); n != 0 {
		t.Errorf("the mock saw %d requests: %v", n, m.hits)
	}
}

// TestSpecGetOutsideAllowlistIsRejected: the 16 GET operations of the
// documentation outside the allowlist end with ErrPathNotAllowed before
// the network, through getJSON and through the inner transport.
func TestSpecGetOutsideAllowlistIsRejected(t *testing.T) {
	m := newMockADMP(t)
	c := proofClient(t, m, "acc-spec-get", nil)
	ctx := context.Background()
	allowed := allowedShapes()
	var outside []string
	for _, op := range specOperations(t) {
		if op.method != http.MethodGet || slices.Contains(allowed, shape(op.path)) {
			continue
		}
		outside = append(outside, op.path)
		if err := c.getJSON(ctx, concrete(op.path), nil, nil); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("GET %s (%s): %v, want ErrPathNotAllowed", op.path, op.id, err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.srv.URL+"/api/v1"+concrete(op.path), http.NoBody)
		if _, err := c.http.Transport.RoundTrip(req); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("GET %s over the inner transport: %v, want ErrPathNotAllowed", op.path, err)
		}
	}
	if len(outside) != 16 {
		t.Errorf("GETs outside the allowlist: %d %v, want 16", len(outside), outside)
	}
	if n := m.total(""); n != 0 {
		t.Errorf("the mock saw %d requests: %v", n, m.hits)
	}
}

// planAllowlist is the allowlist of the introduction of M3.14 (E-248):
// the login and seven GET paths.
var planAllowlist = []string{
	"POST /V1/user/authentication",
	"GET /V1/user/authentication",
	"GET /V1/infrastructure/accessnodepicker",
	"GET /V1/infrastructure/port/{}/dienstIds",
	"GET /V1/dienst/{}",
	"GET /V1/dienst/{}/properties/public",
	"GET /V1/anschluss/{}",
	"GET /V1/organization/byIds",
}

// TestAllowlistMatchesPlan: the allowlist of the client is exactly the set
// of the plan, and each of its templates is an operation of the
// documentation with that method.
func TestAllowlistMatchesPlan(t *testing.T) {
	got := []string{authMethod + " " + authPath}
	for _, s := range allowedShapes() {
		got = append(got, http.MethodGet+" "+s)
	}
	sort.Strings(got)
	want := append([]string(nil), planAllowlist...)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("allowlist %v, want %v", got, want)
	}
	documented := map[string]bool{}
	for _, op := range specOperations(t) {
		documented[op.method+" "+shape(op.path)] = true
	}
	for _, entry := range got {
		if !documented[entry] {
			t.Errorf("%s is no operation of the documentation", entry)
		}
	}
	// The query parameter of byIds is the documented id.
	for _, r := range allowedGets {
		if r.path == organizationsPath && !slices.Equal(r.query, []string{"id"}) {
			t.Errorf("query of %s: %v, want [id]", r.path, r.query)
		}
	}
}
