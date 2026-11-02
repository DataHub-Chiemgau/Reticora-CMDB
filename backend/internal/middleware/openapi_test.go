package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"gopkg.in/yaml.v3"
)

func TestOpenAPIValidationRejectsMissingContentType(t *testing.T) {
	handler := OpenAPIValidation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"name":"router"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("expected problem response, got %q", ct)
	}
}

func TestOpenAPIValidationRejectsInvalidJSONBody(t *testing.T) {
	handler := OpenAPIValidation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"name":`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestOpenAPIValidationAcceptsValidJSONBody(t *testing.T) {
	handler := OpenAPIValidation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"name":"router","ci_type_id":"type-1"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestOpenAPIValidatorRejectsMissingRequiredQueryParam(t *testing.T) {
	validator := &OpenAPIValidator{routes: map[string]RouteSpec{
		routeKey(http.MethodGet, "/api/v1/test"): {RequiredQueryParams: []string{"name"}},
	}}
	handler := validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestOpenAPIValidationLeavesCompressedBodiesToTheHandler covers WP-083
// (DIS-05): collectors send the ingest body gzip-compressed; the handler
// decompresses and validates it.
func TestOpenAPIValidationLeavesCompressedBodiesToTheHandler(t *testing.T) {
	handler := OpenAPIValidation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", strings.NewReader("\x1f\x8b compressed"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

// TestDeprecatedOperationsCarryLifecycleHeaders covers API-01: an operation
// the specification marks deprecated answers with Deprecation, Sunset and
// a successor link to the same item (F-219); other operations carry none.
func TestDeprecatedOperationsCarryLifecycleHeaders(t *testing.T) {
	h := DeprecationHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/review-items/7b0c6e1a-0000-4000-8000-000000000001/resolve", nil))
	if got := w.Header().Get("Sunset"); got != "Wed, 13 Oct 2027 00:00:00 GMT" {
		t.Errorf("Sunset %q", got)
	}
	if got := w.Header().Get("Deprecation"); got != "@1791763200" {
		t.Errorf("Deprecation %q", got)
	}
	if got := w.Header().Get("Link"); got != `</api/v1/review-items/7b0c6e1a-0000-4000-8000-000000000001/resolve>; rel="successor-version"` {
		t.Errorf("Link %q", got)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/review-items", nil))
	if w.Header().Get("Sunset") != "" || w.Header().Get("Deprecation") != "" {
		t.Errorf("a current operation is marked deprecated: %v", w.Header())
	}
}

// TestDeprecationsNeedTwelveMonthsNotice: the shipped specification is
// valid, and a deprecation without sunset or with less than 12 months of
// notice is rejected.
func TestDeprecationsNeedTwelveMonthsNotice(t *testing.T) {
	spec, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if deps, perr := ParseDeprecations(spec); perr != nil || len(deps) < 2 {
		t.Fatalf("shipped spec: %d deprecations, %v", len(deps), perr)
	}
	for name, doc := range map[string]string{
		"no sunset":        "paths:\n  /a:\n    get:\n      deprecated: true\n      x-deprecated-at: '2026-01-01'\n",
		"too short":        "paths:\n  /a:\n    get:\n      deprecated: true\n      x-deprecated-at: '2026-01-01'\n      x-sunset: '2026-06-01'\n",
		"no deprecated-at": "paths:\n  /a:\n    get:\n      deprecated: true\n      x-sunset: '2028-01-01'\n",
	} {
		if _, perr := ParseDeprecations([]byte(doc)); perr == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestOpenAPISpecRequiredInProduction covers WP-568 (API-01, F-218): a
// production server whose OpenAPI document cannot be loaded (missing file,
// unparseable document) refuses to start instead of silently serving
// deprecated operations without Deprecation and Sunset; the shipped
// document loads.
func TestOpenAPISpecRequiredInProduction(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("paths: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing file":         filepath.Join(dir, "missing.yaml"),
		"unparseable document": broken,
	} {
		t.Setenv("RETICORA_OPENAPI_SPEC_PATH", path)
		loaded, err := NewOpenAPIValidator().CheckSpec(true)
		if err == nil || loaded || !strings.Contains(err.Error(), path) {
			t.Errorf("production, %s: loaded %v, error %v; want a start error naming %s", name, loaded, err, path)
		}
	}
	shipped, err := filepath.Abs("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETICORA_OPENAPI_SPEC_PATH", shipped)
	if loaded, err := NewOpenAPIValidator().CheckSpec(true); err != nil || !loaded {
		t.Errorf("production, shipped document: loaded %v, error %v; want it loaded", loaded, err)
	}
}

// TestOpenAPISpecMissingOutsideProductionWarns covers WP-568 (F-218):
// outside production a server without the OpenAPI document starts, logs a
// warning and reports "not loaded" (the /readyz detail openapi, see
// cmd/server).
func TestOpenAPISpecMissingOutsideProductionWarns(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	t.Setenv("RETICORA_OPENAPI_SPEC_PATH", missing)
	v := NewOpenAPIValidator()
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	loaded, err := v.CheckSpec(false)
	if err != nil || loaded {
		t.Fatalf("outside production: loaded %v, error %v; want a start without the document", loaded, err)
	}
	if out := logged.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "openapi: specification not loaded") || !strings.Contains(out, missing) {
		t.Errorf("outside production: log %q; want a warning naming %s", out, missing)
	}
	if OpenAPISpecNotLoaded != "not loaded" {
		t.Errorf("readiness detail %q, want %q", OpenAPISpecNotLoaded, "not loaded")
	}
}

// TestSuccessorLinkExpandsPathParameters covers WP-568 (API-01, F-219): the
// successor-version link names a URI with the path parameters of the
// request filled in (/x/42 -> </y/42>), escaped as in the request; a
// successor with a placeholder the request does not fill gets no link,
// while Deprecation and Sunset stay.
func TestSuccessorLinkExpandsPathParameters(t *testing.T) {
	life := func(successor string) Deprecation {
		return Deprecation{DeprecatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Sunset: time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), Successor: successor}
	}
	v := &OpenAPIValidator{deprecated: map[string]Deprecation{
		"GET /x/{id}":       life("/y/{id}"),
		"GET /x/{id}/z":     life("/y/{ci_id}"),
		"GET /x":            life("/y/{id}"),
		"GET /p/{a}/q/{b}":  life("/r/{b}/s/{a}"),
		"POST /plain/{id}":  life("/plain2"),
		"DELETE /none/{id}": life(""),
	}}
	for _, tc := range []struct{ method, path, link string }{
		{http.MethodGet, "/x/42", `</y/42>; rel="successor-version"`},
		{http.MethodGet, "/x/a%3Eb", `</y/a%3Eb>; rel="successor-version"`},
		{http.MethodGet, "/p/1/q/2", `</r/2/s/1>; rel="successor-version"`},
		{http.MethodPost, "/plain/7", `</plain2>; rel="successor-version"`},
		{http.MethodGet, "/x/42/z", ""},
		{http.MethodGet, "/x", ""},
		{http.MethodDelete, "/none/7", ""},
	} {
		w := httptest.NewRecorder()
		v.writeDeprecation(w, httptest.NewRequest(tc.method, tc.path, nil))
		if got := w.Header().Get("Link"); got != tc.link {
			t.Errorf("%s %s: Link %q, want %q", tc.method, tc.path, got, tc.link)
		}
		if w.Header().Get("Deprecation") == "" || w.Header().Get("Sunset") == "" {
			t.Errorf("%s %s: lifecycle headers %v, want Deprecation and Sunset", tc.method, tc.path, w.Header())
		}
	}
}

// TestShippedSuccessorLinksExpand: every deprecated operation of the
// shipped document links a URI without placeholders that names a path of
// the document; only the mount of a CI into a rack, whose successor needs
// the CI ID from the body, has no link (F-219).
func TestShippedSuccessorLinksExpand(t *testing.T) {
	var doc struct {
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(defaultOpenAPIValidator.specBytes, &doc); err != nil || len(doc.Paths) == 0 {
		t.Fatalf("shipped document: %d paths, %v", len(doc.Paths), err)
	}
	var unlinked []string
	for key := range defaultOpenAPIValidator.deprecated {
		method, template, _ := strings.Cut(key, " ")
		segments := strings.Split(template, "/")
		for i, s := range segments {
			if isPathPlaceholder(s) {
				segments[i] = "v-" + strings.Trim(s, "{}")
			}
		}
		w := httptest.NewRecorder()
		defaultOpenAPIValidator.writeDeprecation(w, httptest.NewRequest(method, strings.Join(segments, "/"), nil))
		link := w.Header().Get("Link")
		if link == "" {
			unlinked = append(unlinked, key)
			continue
		}
		target := strings.TrimSuffix(strings.TrimPrefix(link, "<"), `>; rel="successor-version"`)
		known := false
		for path := range doc.Paths {
			known = known || routePatternMatches(path, target)
		}
		if strings.ContainsAny(target, "{}") || !known {
			t.Errorf("%s: Link %q names no path of the document", key, link)
		}
	}
	if want := []string{"POST /api/v1/racks/{id}/mounts"}; !slices.Equal(unlinked, want) {
		t.Errorf("deprecated operations without successor link: %v, want %v", unlinked, want)
	}
}

// TestAuditT2RefreshPassesOpenAPIValidation covers WP-364 (AUT-02, F-018):
// the SPA renews the access token with a POST that carries only the
// HttpOnly refresh cookie, no body and no Content-Type
// (frontend/src/auth/session.ts:22-25, api/openapi.yaml operation
// refreshSession without requestBody). The validation stage must hand it to
// the refresh handler, with the rules of the OpenAPI document and with the
// fallback registry of a server without it.
func TestAuditT2RefreshPassesOpenAPIValidation(t *testing.T) {
	fallback := &OpenAPIValidator{}
	fallback.registerKnownRoutes()
	validators := map[string]*OpenAPIValidator{"OpenAPI document": defaultOpenAPIValidator, "fallback registry": fallback}
	for _, tc := range []struct {
		name, validator, contentType, body string
	}{
		{name: "SPA refresh: cookie only, no Content-Type, no body", validator: "OpenAPI document"},
		{name: "cookie only, Content-Type application/json, empty body", validator: "OpenAPI document", contentType: "application/json"},
		{name: "fallback registry: cookie only, no Content-Type, no body", validator: "fallback registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := validators[tc.validator].Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: "opaque-refresh-token"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if !reached || w.Code != http.StatusOK {
				t.Errorf("POST /api/v1/auth/refresh (%s): status %d, handler reached %v, body %s; want the request to reach the refresh handler",
					tc.name, w.Code, reached, strings.TrimSpace(w.Body.String()))
			}
		})
	}
}

// specOperation is the part of an OpenAPI operation the registry check needs.
type specOperation struct {
	RequestBody *struct {
		Required bool `yaml:"required"`
	} `yaml:"requestBody"`
}

// TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies: the lightweight
// validator must not demand more than the API contract (API-04 validation
// against the OpenAPI document). For every registered write route that the
// OpenAPI document describes:
//   - RequiresBody only where the operation declares a required requestBody;
//   - a Content-Type check only where the operation has a requestBody.
//
// This holds for the rules derived from the document and for the fallback
// registry of a server without the document (WP-364). A positive control
// shows the validator still refuses a bodiless POST /api/v1/cis
// (documented required body).
func TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies(t *testing.T) {
	v := NewOpenAPIValidator()
	if len(v.specBytes) == 0 {
		t.Fatalf("OpenAPI document not found (spec path %q)", v.specPath)
	}
	fallback := &OpenAPIValidator{}
	fallback.registerKnownRoutes()
	for name, routes := range map[string]map[string]RouteSpec{"rules from the OpenAPI document": v.routes, "fallback registry": fallback.routes} {
		if mismatches := registryMismatches(t, v.specBytes, routes); len(mismatches) > 0 {
			t.Errorf("%s demands more than the OpenAPI contract (%d routes):\n  %s", name, len(mismatches), strings.Join(mismatches, "\n  "))
		}
	}

	// Positive control: a documented required body is still enforced, with
	// and without Content-Type.
	for _, contentType := range []string{"application/json", ""} {
		reached := false
		h := v.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", nil)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if reached || w.Code != http.StatusBadRequest {
			t.Errorf("control: POST /api/v1/cis without body (Content-Type %q) -> %d (handler reached %v), want 400", contentType, w.Code, reached)
		}
	}
}

// registryMismatches lists the write routes of routes that demand more than
// the operations of the OpenAPI document spec.
func registryMismatches(t *testing.T, spec []byte, routes map[string]RouteSpec) []string {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("parse OpenAPI document: %v", err)
	}
	// The registry and the document may name path parameters differently
	// ({id} vs {teamId}); compare with anonymous parameters.
	normalize := func(path string) string {
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
				parts[i] = "{}"
			}
		}
		return strings.Join(parts, "/")
	}
	ops := map[string]specOperation{}
	for path, items := range doc.Paths {
		for method, node := range items {
			if !slices.Contains([]string{"post", "put", "patch"}, method) {
				continue
			}
			var op specOperation
			if err := node.Decode(&op); err != nil {
				t.Fatalf("decode %s %s: %v", method, path, err)
			}
			ops[strings.ToUpper(method)+" "+normalize(path)] = op
		}
	}

	var mismatches []string
	for key, spec := range routes {
		parts := strings.SplitN(key, " ", 2)
		if !shouldValidateContentType(parts[0]) {
			continue
		}
		op, documented := ops[parts[0]+" "+normalize(parts[1])]
		if !documented {
			continue
		}
		hasBody := op.RequestBody != nil
		if spec.RequiresBody && (!hasBody || !op.RequestBody.Required) {
			mismatches = append(mismatches, key+": validator requires a JSON body, OpenAPI declares no required requestBody")
		}
		if !hasBody {
			mismatches = append(mismatches, key+": validator requires a JSON Content-Type, OpenAPI declares no requestBody")
		}
	}
	sort.Strings(mismatches)
	return mismatches
}

// TestOpenAPIValidationRulesComeFromTheDocument covers WP-364 (API-04): the
// rules are derived from requestBody and parameters of the OpenAPI
// document. A required body and its media type, a required query parameter
// and an optional body are enforced as documented; a Content-Type is
// checked only for a body that is sent or required.
func TestOpenAPIValidationRulesComeFromTheDocument(t *testing.T) {
	v := NewOpenAPIValidator()
	if len(v.specBytes) == 0 {
		t.Fatalf("OpenAPI document not found (spec path %q)", v.specPath)
	}
	if got := v.routes[routeKey(http.MethodPost, "/api/v1/auth/refresh")]; got.RequiresBody || len(got.AllowedContentTypes) > 0 {
		t.Errorf("refreshSession has no requestBody, rule %+v", got)
	}
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name, method, path, contentType, body string
		want                                  int
	}{
		{"required body missing", http.MethodPost, "/api/v1/cis", "application/json", "", http.StatusBadRequest},
		{"required body without Content-Type", http.MethodPost, "/api/v1/cis", "", `{"name":"r"}`, http.StatusBadRequest},
		{"required body as text", http.MethodPost, "/api/v1/cis", "text/plain", `{"name":"r"}`, http.StatusBadRequest},
		{"required body as JSON", http.MethodPost, "/api/v1/cis", "application/json", `{"name":"r"}`, http.StatusNoContent},
		{"rule of a route the old registry did not know", http.MethodPost, "/api/v1/api-keys", "application/json", "", http.StatusBadRequest},
		{"required query parameter missing", http.MethodGet, "/api/v1/mounts", "", "", http.StatusBadRequest},
		{"required query parameter present", http.MethodGet, "/api/v1/mounts?rack_id=r-1", "", "", http.StatusNoContent},
		{"optional body omitted", http.MethodPost, "/api/v1/collectors/c-1/heartbeat", "", "", http.StatusNoContent},
		{"optional body as JSON", http.MethodPost, "/api/v1/collectors/c-1/heartbeat", "application/json", `{}`, http.StatusNoContent},
		{"optional body as text", http.MethodPost, "/api/v1/collectors/c-1/heartbeat", "text/plain", `x`, http.StatusBadRequest},
		{"bodiless operation without body", http.MethodPost, "/api/v1/audit/verify", "", "", http.StatusNoContent},
		{"bodiless operation without body", http.MethodPost, "/api/v1/webhooks/w-1/test", "", "", http.StatusNoContent},
		{"binary body of any media type", http.MethodPut, "/api/v1/documents/d-1/content", "application/pdf", "%PDF-1.7", http.StatusNoContent},
		{"binary body missing", http.MethodPut, "/api/v1/documents/d-1/content", "application/pdf", "", http.StatusBadRequest},
		{"+json media type", http.MethodPost, "/scim/v2/Users", "application/scim+json", `{"userName":"a"}`, http.StatusNoContent},
		{"+json media type with invalid JSON", http.MethodPost, "/scim/v2/Users", "application/scim+json", `{"userName":`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: "x"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: %s %s (Content-Type %q, body %q) -> %d %s, want %d", tc.name, tc.method, tc.path, tc.contentType, tc.body, w.Code, strings.TrimSpace(w.Body.String()), tc.want)
		}
	}
}

// TestParseRouteSpecsResolvesReferencesAndOverrides covers WP-364: path
// item parameters apply to every operation, an operation parameter of the
// same name and location overrides them, references are resolved, and an
// operation without rule is not registered.
func TestParseRouteSpecsResolvesReferencesAndOverrides(t *testing.T) {
	doc := `paths:
  /a/{id}:
    parameters:
      - $ref: '#/components/parameters/Site'
      - {name: q, in: query, required: true}
    get:
      parameters:
        - {name: q, in: query, required: false}
    post:
      requestBody:
        $ref: '#/components/requestBodies/Thing'
    delete: {}
  /b:
    post:
      parameters:
        - {name: id, in: path, required: true}
components:
  parameters:
    Site: {name: site_id, in: query, required: true}
  requestBodies:
    Thing:
      required: true
      content:
        application/json: {}
`
	routes, err := ParseRouteSpecs([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]RouteSpec{
		"GET /a/{id}":    {RequiredQueryParams: []string{"site_id"}},
		"POST /a/{id}":   {RequiredQueryParams: []string{"q", "site_id"}, RequiresBody: true, AllowedContentTypes: []string{"application/json"}},
		"DELETE /a/{id}": {RequiredQueryParams: []string{"q", "site_id"}},
	}
	if len(routes) != len(want) {
		t.Errorf("routes %v, want %v", routes, want)
	}
	for key, w := range want {
		got := routes[key]
		if !slices.Equal(got.RequiredQueryParams, w.RequiredQueryParams) || got.RequiresBody != w.RequiresBody || !slices.Equal(got.AllowedContentTypes, w.AllowedContentTypes) {
			t.Errorf("%s: %+v, want %+v", key, got, w)
		}
	}
	if _, err := ParseRouteSpecs([]byte("paths:\n  /a:\n    get:\n      parameters:\n        - $ref: '#/components/parameters/Missing'\n")); err == nil {
		t.Error("an unresolved reference was accepted")
	}
}

// TestOpenAPIValidatorFallsBackToTheRegistryWithoutDocument covers WP-364:
// without the OpenAPI document the built-in registry applies; it still
// enforces a required body and lets the cookie-only refresh through.
func TestOpenAPIValidatorFallsBackToTheRegistryWithoutDocument(t *testing.T) {
	t.Setenv("RETICORA_OPENAPI_SPEC_PATH", "/nonexistent/openapi.yaml")
	v := NewOpenAPIValidator()
	if len(v.specBytes) != 0 || len(v.routes) == 0 {
		t.Fatalf("spec %d bytes, %d routes; want the fallback registry", len(v.specBytes), len(v.routes))
	}
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	refresh.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: "x"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, refresh)
	if w.Code != http.StatusNoContent {
		t.Errorf("cookie-only refresh with the fallback registry: %d %s", w.Code, w.Body.String())
	}
	create := httptest.NewRequest(http.MethodPost, "/api/v1/cis", nil)
	create.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, create)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/v1/cis without body with the fallback registry: %d, want 400", w.Code)
	}
}
