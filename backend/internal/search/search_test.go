package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	body, err := buildOpenSearchQuery(Query{OrganizationID: "org", Text: "name:*)", EntityTypes: []string{"ticket"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !contains(string(raw), "multi_match") || contains(string(raw), "query_string") {
		t.Fatalf("unsafe query body: %s", raw)
	}
}

func TestOpenSearchRejectsOversizedQuery(t *testing.T) {
	_, err := buildOpenSearchQuery(Query{Text: string(make([]rune, MaxQueryLength+1))})
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
	res, err := b.Query(Query{OrganizationID: "org", Text: "router"})
	if err != nil || len(res.Data) != 1 {
		t.Fatalf("res=%#v err=%v", res, err)
	}
}
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || contains(s[1:], sub) || s[:len(sub)] == sub))
}
