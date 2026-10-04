package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

func TestPostgresQueryBuilderUsesParameters(t *testing.T) {
	sql, args, err := buildPostgresQuery(Query{OrganizationID: "org", Text: "x' | delete", EntityTypes: []string{"ci"}, Limit: 10, Highlight: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 5 || args[1] != "x' | delete" {
		t.Fatalf("query text not parameterized: %#v", args)
	}
	if contains(sql, "x' | delete") {
		t.Fatalf("query interpolated user input: %s", sql)
	}
}

func TestOpenSearchQueryBuilderStructured(t *testing.T) {
	scope := database.OrgWideScope("org", "user")
	body, err := buildOpenSearchQuery(&Query{OrganizationID: "org", Text: "name:*)", EntityTypes: []string{"ticket"}, Limit: 5}, &scope)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !contains(string(raw), "multi_match") || contains(string(raw), "query_string") {
		t.Fatalf("unsafe query body: %s", raw)
	}
}

func TestOpenSearchRejectsOversizedQuery(t *testing.T) {
	scope := database.OrgWideScope("org", "user")
	_, err := buildOpenSearchQuery(&Query{OrganizationID: "org", Text: string(make([]rune, MaxQueryLength+1))}, &scope)
	if err == nil {
		t.Fatal("expected oversized query error")
	}
}

func TestOpenSearchHTTPQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/idx/_search" {
			w.Write([]byte(`{"hits":{"total":{"value":1},"hits":[{"_score":2,"_source":{"id":"1","organization_id":"org","entity_type":"ci","entity_id":"e","title":"Router","url":"/cmdb/e","updated_at":"2026-01-01T00:00:00Z"}}]}}`))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	res, err := b.Query(orgWideContext("org"), Query{OrganizationID: "org", Text: "router"})
	if err != nil || len(res.Data) != 1 {
		t.Fatalf("res=%#v err=%v", res, err)
	}
}
// orgWideContext carries an org-wide tenant scope, as the auth middleware
// attaches it to every request.
func orgWideContext(orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "user")
	return database.ContextWithTenantScope(context.Background(), &scope)
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || contains(s[1:], sub) || s[:len(sub)] == sub))
}

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestOpenSearchPingInvalidURL(t *testing.T) {
	b := NewOpenSearchBackend(OpenSearchConfig{URL: "://bad-url"}, nil)
	if err := b.Ping(context.Background()); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestOpenSearchPingUnreachable(t *testing.T) {
	b := NewOpenSearchBackend(OpenSearchConfig{URL: "http://opensearch.local"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	if err := b.Ping(context.Background()); err == nil {
		t.Fatal("expected error for unreachable backend")
	}
}

func TestOpenSearchIndexDocumentMarshalError(t *testing.T) {
	b := NewOpenSearchBackend(OpenSearchConfig{URL: "http://opensearch.local"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	doc := Document{OrganizationID: "org", EntityType: "ci", EntityID: "1", Metadata: map[string]string{"bad": "\xff\xfe"}}
	if err := b.IndexDocument(context.Background(), doc); err == nil {
		t.Fatal("expected marshal error for invalid UTF-8 metadata")
	}
}

func TestOpenSearchQueryRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":1},"hits":[{"_score":1.5,"_source":{"id":"d1","organization_id":"org","entity_type":"ci","entity_id":"1","title":"switch"},"highlight":{"title":["<em>switch</em>"]}}]}}`))
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(OpenSearchConfig{URL: srv.URL}, srv.Client())
	if err := b.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := b.Query(orgWideContext("org"), Query{OrganizationID: "org", Text: "switch", Limit: 10, Highlight: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Data) != 1 || res.Data[0].Title != "switch" || len(res.Data[0].Highlights) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

var errTest = errors.New("transport failure")

func TestIndexTemplateBodyStructure(t *testing.T) {
	body := indexTemplateBody("reticora-search")
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"reticora-search", "organization_id", "entity_type", "entity_id", "title", "summary", "metadata", "updated_at", "keyword", "date"} {
		if !strings.Contains(s, want) {
			t.Fatalf("index template missing %q: %s", want, s)
		}
	}
}

func TestEnsureIndexTemplateIdempotent(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/_index_template/idx" {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	if err := b.EnsureIndexTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second call must succeed too (upsert semantics, safe on every startup).
	if err := b.EnsureIndexTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 PUT calls, got %d", calls)
	}
}

func TestEnsureIndexTemplateError(t *testing.T) {
	b := NewOpenSearchBackend(OpenSearchConfig{URL: "http://opensearch.local", Index: "idx"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	if err := b.EnsureIndexTemplate(context.Background()); err == nil {
		t.Fatal("expected error for unreachable backend")
	}
}
