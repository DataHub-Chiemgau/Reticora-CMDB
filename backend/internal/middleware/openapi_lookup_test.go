package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// TestLookupPrefersTheMostSpecificTemplate pins the template matching of
// the validator: the request path is cleaned, a template matches segment
// by segment, and of several matching templates the first segment where
// one is literal and the other a placeholder decides; templates that never
// differ there are ordered by their text. Every lookup is repeated so that
// the result cannot depend on the iteration order of the rule map.
func TestLookupPrefersTheMostSpecificTemplate(t *testing.T) {
	v := &OpenAPIValidator{}
	for _, route := range []struct{ method, template string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/cis"},
		{http.MethodGet, "/cis/{id}"},
		{http.MethodGet, "/cis/search"},
		{http.MethodGet, "/cis/{id}/rel"},
		{http.MethodGet, "/cis/{id}/rel/{rid}"},
		{http.MethodGet, "/cis/{id}/rel/latest"},
		{http.MethodGet, "/a/{x}/c/d"},
		{http.MethodGet, "/a/b/{y}/{z}"},
		{http.MethodGet, "/t/{b}"},
		{http.MethodGet, "/t/{a}"},
		{http.MethodGet, "/u/{id}/v"},
		{http.MethodGet, "/u/w/{id}"},
		{http.MethodPost, "/cis/{id}"},
	} {
		v.register(route.method, route.template, RouteSpec{RequiredQueryParams: []string{route.method + " " + route.template}})
	}

	const id = "0b0c6b38-5d1b-4d8e-9a2a-111111111111"
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/", "GET /"},
		{http.MethodGet, "", "GET /"},
		{http.MethodGet, "//", "GET /"},
		{http.MethodGet, "/cis/..", "GET /"},
		{http.MethodGet, "/cis", "GET /cis"},
		{http.MethodGet, "/cis/", "GET /cis"},
		{http.MethodGet, "/cis/" + id, "GET /cis/{id}"},
		{http.MethodGet, "/cis/" + id + "/", "GET /cis/{id}"},
		{http.MethodGet, "/cis/%2F", "GET /cis/{id}"},
		{http.MethodGet, "/cis/{id}", "GET /cis/{id}"},
		{http.MethodGet, "/cis/search", "GET /cis/search"},
		{http.MethodGet, "/cis/search/", "GET /cis/search"},
		{http.MethodGet, "//cis//search", "GET /cis/search"},
		{http.MethodGet, "/cis/" + id + "/../search", "GET /cis/search"},
		{http.MethodGet, "/cis/" + id + "/rel", "GET /cis/{id}/rel"},
		{http.MethodGet, "/cis/search/rel", "GET /cis/{id}/rel"},
		{http.MethodGet, "/cis/" + id + "/rel/latest", "GET /cis/{id}/rel/latest"},
		{http.MethodGet, "/cis/search/rel/latest", "GET /cis/{id}/rel/latest"},
		{http.MethodGet, "/cis/" + id + "/rel/" + id, "GET /cis/{id}/rel/{rid}"},
		{http.MethodGet, "/a/b/c/d", "GET /a/b/{y}/{z}"},
		{http.MethodGet, "/a/q/c/d", "GET /a/{x}/c/d"},
		{http.MethodGet, "/t/1", "GET /t/{a}"},
		{http.MethodGet, "/u/w/v", "GET /u/w/{id}"},
		{http.MethodGet, "/u/x/v", "GET /u/{id}/v"},
		{http.MethodPost, "/cis/" + id, "POST /cis/{id}"},
		{http.MethodPost, "/cis/search", "POST /cis/{id}"},
		{http.MethodDelete, "/cis/" + id, ""},
		{http.MethodGet, "/cis/a/b", ""},
		{http.MethodGet, "/cis/" + id + "/rel/" + id + "/more", ""},
		{http.MethodGet, "/nope", ""},
		{http.MethodGet, "/api/v1/cis", ""},
	} {
		for range 20 {
			spec, ok := v.lookup(tc.method, tc.path)
			if tc.want == "" {
				if ok {
					t.Fatalf("%s %q: matched %v, want no rule", tc.method, tc.path, spec.RequiredQueryParams)
				}
				continue
			}
			if !ok || !reflect.DeepEqual(spec.RequiredQueryParams, []string{tc.want}) {
				t.Fatalf("%s %q: got %v (found %v), want %s", tc.method, tc.path, spec.RequiredQueryParams, ok, tc.want)
			}
		}
	}
}

// TestLookupOnTheShippedDocument pins representative lookups against the
// rules of the shipped OpenAPI document: paths with IDs find the rule of
// their template, an operation without a rule finds none.
func TestLookupOnTheShippedDocument(t *testing.T) {
	v := defaultOpenAPIValidator
	if v.loadErr != nil || len(v.routes) == 0 {
		t.Fatalf("shipped document not loaded: %v", v.loadErr)
	}
	const id = "0b0c6b38-5d1b-4d8e-9a2a-111111111111"
	for _, tc := range []struct{ method, path, key string }{
		{http.MethodPatch, "/api/v1/cis/" + id, "PATCH /api/v1/cis/{id}"},
		{http.MethodPatch, "/api/v1/cis/" + id + "/", "PATCH /api/v1/cis/{id}"},
		{http.MethodPatch, "//api/v1//cis/" + id, "PATCH /api/v1/cis/{id}"},
		{http.MethodPost, "/api/v1/cis/bulk", "POST /api/v1/cis/bulk"},
		{http.MethodPost, "/api/v1/cis/" + id + "/merge", "POST /api/v1/cis/{id}/merge"},
		{http.MethodPost, "/api/v1/iga/access-reviews/" + id + "/items/" + id + "/decision", "POST /api/v1/iga/access-reviews/{id}/items/{itemId}/decision"},
		{http.MethodGet, "/api/v1/impact/", "GET /api/v1/impact"},
		{http.MethodPost, "/api/v1/cis/x/../../cis", "POST /api/v1/cis"},
		{http.MethodGet, "/api/v1/cis", ""},
		{http.MethodGet, "/api/v1/cis/" + id, ""},
		{http.MethodPost, "/api/v1/cis/" + id + "/merge/extra", ""},
		{http.MethodGet, "/", ""},
	} {
		spec, ok := v.lookup(tc.method, tc.path)
		if tc.key == "" {
			if ok {
				t.Errorf("%s %q: found %+v, want no rule", tc.method, tc.path, spec)
			}
			continue
		}
		want, exists := v.routes[tc.key]
		if !exists {
			t.Fatalf("shipped document has no rule %q", tc.key)
		}
		if !ok || !reflect.DeepEqual(spec, want) {
			t.Errorf("%s %q: got %+v (found %v), want the rule of %s %+v", tc.method, tc.path, spec, ok, tc.key, want)
		}
	}
}

// TestDeprecationMatchesTheCleanedPath pins the template matching of the
// lifecycle headers: the request path is cleaned before it is matched, and
// a path matching no deprecated template gets no headers.
func TestDeprecationMatchesTheCleanedPath(t *testing.T) {
	life := Deprecation{DeprecatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Sunset: time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), Successor: "/y/{id}"}
	v := &OpenAPIValidator{deprecated: map[string]Deprecation{
		"GET /x/{id}":       life,
		"GET /x/{id}/z/{n}": life,
		"GET /":             {DeprecatedAt: life.DeprecatedAt, Sunset: life.Sunset},
	}}
	for _, tc := range []struct {
		method, path string
		deprecated   bool
		link         string
	}{
		{http.MethodGet, "/x/42", true, `</y/42>; rel="successor-version"`},
		{http.MethodGet, "/x/42/", true, `</y/42>; rel="successor-version"`},
		{http.MethodGet, "//x//42", true, `</y/42>; rel="successor-version"`},
		{http.MethodGet, "/x/7/../42", true, `</y/42>; rel="successor-version"`},
		{http.MethodGet, "/x/42/z/1", true, `</y/42>; rel="successor-version"`},
		{http.MethodGet, "/", true, ""},
		{http.MethodGet, "/x/..", true, ""},
		{http.MethodGet, "/x", false, ""},
		{http.MethodGet, "/x/42/z", false, ""},
		{http.MethodGet, "/x/42/z/1/2", false, ""},
		{http.MethodPost, "/x/42", false, ""},
	} {
		w := httptest.NewRecorder()
		v.writeDeprecation(w, httptest.NewRequest(tc.method, tc.path, nil))
		if got := w.Header().Get("Deprecation") != ""; got != tc.deprecated {
			t.Errorf("%s %s: deprecated %v, want %v (%v)", tc.method, tc.path, got, tc.deprecated, w.Header())
		}
		if got := w.Header().Get("Link"); got != tc.link {
			t.Errorf("%s %s: Link %q, want %q", tc.method, tc.path, got, tc.link)
		}
	}
}
