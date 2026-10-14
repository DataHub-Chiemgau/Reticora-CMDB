package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
// a successor link; other operations carry none.
func TestDeprecatedOperationsCarryLifecycleHeaders(t *testing.T) {
	h := OpenAPIValidation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/review-items/7b0c6e1a-0000-4000-8000-000000000001/resolve", nil))
	if got := w.Header().Get("Sunset"); got != "Wed, 13 Oct 2027 00:00:00 GMT" {
		t.Errorf("Sunset %q", got)
	}
	if got := w.Header().Get("Deprecation"); got != "@1791763200" {
		t.Errorf("Deprecation %q", got)
	}
	if got := w.Header().Get("Link"); got != `</api/v1/review-items/{id}/resolve>; rel="successor-version"` {
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
