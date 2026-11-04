package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

func TestPostgresQueryBuilderUsesParameters(t *testing.T) {
	sql, args, err := buildPostgresQuery(&Query{OrganizationID: "org", Text: "x' | delete", EntityTypes: []string{"ci"}, Limit: 10, Highlight: true})
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
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	res, err := b.Query(orgWideContext("org"), &Query{OrganizationID: "org", Text: "router"})
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
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: "://bad-url"}, nil)
	if err := b.Ping(context.Background()); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestOpenSearchPingUnreachable(t *testing.T) {
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: "http://opensearch.local"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	if err := b.Ping(context.Background()); err == nil {
		t.Fatal("expected error for unreachable backend")
	}
}

func TestOpenSearchIndexDocumentMarshalError(t *testing.T) {
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: "http://opensearch.local"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	doc := Document{OrganizationID: "org", EntityType: "ci", EntityID: "1", Metadata: map[string]string{"bad": "\xff\xfe"}}
	if err := b.IndexDocument(context.Background(), &doc); err == nil {
		t.Fatal("expected marshal error for invalid UTF-8 metadata")
	}
}

func TestOpenSearchQueryRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":1},"hits":[{"_score":1.5,"_source":{"id":"d1","organization_id":"org","entity_type":"ci","entity_id":"1","title":"switch"},"highlight":{"title":["<em>switch</em>"]}}]}}`))
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: srv.URL}, srv.Client())
	if err := b.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := b.Query(orgWideContext("org"), &Query{OrganizationID: "org", Text: "switch", Limit: 10, Highlight: true})
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
	// WP-787: the field rows as text under fields, the identifiers nested.
	for _, want := range []string{
		`"fields":{"dynamic":true,"type":"object"}`,
		`"identifiers":{"properties":{"addr":{"type":"ip"},"field":{"type":"keyword"},"norm":{"type":"keyword"}},"type":"nested"}`,
		`"path_match":"fields.*"`,
		`"mapping":{"type":"text"}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("index template lacks %s: %s", want, s)
		}
	}
}

func TestEnsureIndexTemplateIdempotent(t *testing.T) {
	var calls, mappings int
	indexExists := false
	var mapping []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/_index_template/idx" {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/idx/_mapping" {
			mappings++
			mapping, _ = io.ReadAll(r.Body)
			if !indexExists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	// Without the index the template alone takes effect at its creation.
	if err := b.EnsureIndexTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second call must succeed too (upsert semantics, safe on every startup);
	// an existing index gets the new fields additively (WP-787).
	indexExists = true
	if err := b.EnsureIndexTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || mappings != 2 {
		t.Fatalf("expected 2 template and 2 mapping PUTs, got %d and %d", calls, mappings)
	}
	var body struct {
		DynamicTemplates []map[string]any          `json:"dynamic_templates"`
		Properties       map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(mapping, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Properties) != 2 || body.Properties["fields"]["type"] != "object" || body.Properties["identifiers"]["type"] != "nested" ||
		len(body.DynamicTemplates) != 1 {
		t.Errorf("mapping of the existing index is not the additive one of fields and identifiers: %s", mapping)
	}
}

func TestEnsureIndexTemplateError(t *testing.T) {
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: "http://opensearch.local", Index: "idx"}, &http.Client{Transport: failingRoundTripper{err: errTest}})
	if err := b.EnsureIndexTemplate(context.Background()); err == nil {
		t.Fatal("expected error for unreachable backend")
	}
}

// TestLocationsAndContactsAreSearchable covers SRC-01 (WP-186): locations
// and contacts are hits with their read permission and their own
// visibility check.
func TestLocationsAndContactsAreSearchable(t *testing.T) {
	for kind, perm := range map[string]string{"location": "location:read", "contact": "contact:read"} {
		if ReadPermission[kind] != perm {
			t.Errorf("%s: read permission %q, want %q", kind, ReadPermission[kind], perm)
		}
		if !strings.Contains(hitVisible, "WHEN '"+kind+"' THEN EXISTS") {
			t.Errorf("%s: no visibility check of its own", kind)
		}
	}
}

// TestOpenSearchQuerySearchesFieldsAndIdentifiers covers WP-787 (SRC-02):
// the text matches the field rows (fields.*), the identifier conditions of
// search.ParseIdentifiers become nested queries (term on norm or addr,
// range on addr, prefix on norm), network.* only with NetworkReadable, and
// no hit carries fields or identifiers.
func TestOpenSearchQuerySearchesFieldsAndIdentifiers(t *testing.T) {
	scope := database.OrgWideScope("org", "user")
	build := func(q *Query) string {
		t.Helper()
		q.OrganizationID = "org"
		body, err := buildOpenSearchQuery(q, &scope)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(body)
		return string(raw)
	}
	for _, c := range []struct {
		name, text string
		network    bool
		want, not  []string
	}{
		{"words", "rack server", true,
			[]string{`"fields.*"`, `"_source":{"excludes":["fields","identifiers"]}`},
			[]string{`"nested"`}},
		{"mac", "00:1A:2b:3c:4d:5e", true,
			[]string{`"nested":{"path":"identifiers"`, `{"term":{"identifiers.norm":"001a2b3c4d5e"}}`, `"network.mac"`}, nil},
		{"ip", "10.0.0.7", true,
			[]string{`{"term":{"identifiers.addr":"10.0.0.7"}}`, `"network.ip"`, `"core.management_ip"`}, nil},
		{"cidr", "10.0.0.0/24", false,
			[]string{`{"range":{"identifiers.addr":{"gte":"10.0.0.0","lte":"10.0.0.255"}}}`, `"core.management_ip"`},
			[]string{`network.`}},
		{"prefix", "INV-00", false,
			[]string{`{"prefix":{"identifiers.norm":"inv-00"}}`, `"must_not":[{"terms":{"identifiers.field":["core.primary_mac","network.mac"]}}]`,
				`"core.inventory_no"`, `"asset.inventory_no"`},
			[]string{`"network.ip"`}},
		{"mac prefix", "00:1a:2b", true,
			[]string{`{"prefix":{"identifiers.norm":"00:1a:2b"}}`, `{"prefix":{"identifiers.norm":"001a2b"}}`}, nil},
	} {
		raw := build(&Query{Text: c.text, NetworkReadable: c.network})
		for _, w := range c.want {
			if !strings.Contains(raw, w) {
				t.Errorf("%s: query lacks %s: %s", c.name, w, raw)
			}
		}
		for _, n := range c.not {
			if strings.Contains(raw, n) {
				t.Errorf("%s: query has %s: %s", c.name, n, raw)
			}
		}
	}
	// Only the asset fields count for a query of assets.
	raw := build(&Query{Text: "10.0.0.7", EntityTypes: []string{"asset"}, NetworkReadable: true})
	if strings.Contains(raw, "network.ip") || strings.Contains(raw, "core.management_ip") {
		t.Errorf("asset query counts CI identifiers: %s", raw)
	}
	// Highlighting covers the field rows and returns the matched
	// identifiers as inner hits.
	raw = build(&Query{Text: "10.0.0.7", Highlight: true})
	for _, w := range []string{`"fields.*":{"number_of_fragments":2,"post_tags":["\u003c/mark\u003e"],"pre_tags":["\u003cmark\u003e"]}`,
		`"inner_hits":{"_source":false,"docvalue_fields":["identifiers.field","identifiers.norm"]`} {
		if !strings.Contains(raw, w) {
			t.Errorf("highlighting query lacks %s: %s", w, raw)
		}
	}
}

// TestOpenSearchMatchedFields covers WP-787: the highlights of the title
// and the field rows and the identifiers of the inner hits become
// matched_fields like in the PostgreSQL index (WP-786); highlights keep the
// title and summary fragments; a summary counts only for a type without
// field rows; network.* only with NetworkReadable.
func TestOpenSearchMatchedFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":2},"hits":[
			{"_score":2,"_source":{"organization_id":"org","entity_type":"ci","entity_id":"c1","title":"web 01","fields":{"core.hostname":"web01"},"identifiers":[{"field":"network.ip","norm":"10.0.0.7"}]},
			 "highlight":{"title":["<em>web</em> 01"],"summary":["<em>web</em>01"],"fields.core.hostname":["<mark>web</mark>01"],"fields.attributes.role":["<mark>web</mark> frontend"]},
			 "inner_hits":{"identifiers":{"hits":{"hits":[
				{"fields":{"identifiers.field":["network.mac"],"identifiers.norm":["001a2b3c4d5e"]}},
				{"fields":{"identifiers.field":["core.primary_mac"],"identifiers.norm":["001a2b3c4d5e"]}},
				{"fields":{"identifiers.field":["core.inventory_no"],"identifiers.norm":["inv-1"]}}]}}}},
			{"_score":1,"_source":{"organization_id":"org","entity_type":"location","entity_id":"l1","title":"Hall"},
			 "highlight":{"summary":["<em>web</em> room"]}}]}}`))
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	for _, network := range []bool{false, true} {
		res, err := b.Query(orgWideContext("org"), &Query{OrganizationID: "org", Text: "web", Highlight: true, NetworkReadable: network})
		if err != nil || len(res.Data) != 2 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		ci := res.Data[0]
		if ci.Fields != nil {
			t.Errorf("hit carries the field rows: %v", ci.Fields)
		}
		if strings.Join(ci.Highlights, "|") != "<em>web</em> 01|<em>web</em>01" {
			t.Errorf("highlights %q, want the title and summary fragments", ci.Highlights)
		}
		want := "title=<mark>web</mark> 01;core.hostname=<mark>web</mark>01;attributes.role=<mark>web</mark> frontend;" +
			"core.primary_mac=00:1a:2b:3c:4d:5e;"
		if network {
			want += "network.mac=00:1a:2b:3c:4d:5e;"
		}
		got := ""
		for _, m := range ci.MatchedFields {
			got += m.Field + "=" + m.Highlight + ";"
		}
		if got != want {
			t.Errorf("network %v: matched fields %s, want %s", network, got, want)
		}
		if loc := res.Data[1].MatchedFields; len(loc) != 1 || loc[0].Field != "summary" || loc[0].Highlight != "<mark>web</mark> room" {
			t.Errorf("location matched fields %+v, want its summary", loc)
		}
	}
	res, err := b.Query(orgWideContext("org"), &Query{OrganizationID: "org", Text: "web"})
	if err != nil || len(res.Data) != 2 || res.Data[0].MatchedFields != nil {
		t.Errorf("without highlighting: %+v, %v; want no matched fields", res, err)
	}
}

// TestOpenSearchIndexesFullDocument covers WP-787: a full document reaches
// the index with its field rows and identifiers, a document alone (an
// OpenSearchBackend without PostgreSQL) with the fields of phase 1 only.
func TestOpenSearchIndexesFullDocument(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	b := NewOpenSearchBackend(&OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	doc := Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "web", Fields: map[string]string{"core.hostname": "web01"}}
	if err := b.IndexDocument(context.Background(), &doc); err != nil {
		t.Fatal(err)
	}
	full := FullDocument{Document: doc, Identifiers: []IdentifierRow{{Field: "network.ip", Norm: "10.0.0.7", Addr: "10.0.0.7"}, {Field: "asset.inventory_no", Norm: "inv-1"}}}
	if err := b.IndexFullDocument(context.Background(), &full); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d PUTs, want 2", len(bodies))
	}
	if _, ok := bodies[0]["fields"]; ok || bodies[0]["identifiers"] != nil {
		t.Errorf("document alone carries fields or identifiers: %v", bodies[0])
	}
	raw, _ := json.Marshal(bodies[1])
	for _, want := range []string{`"fields":{"core.hostname":"web01"}`, `{"addr":"10.0.0.7","field":"network.ip","norm":"10.0.0.7"}`,
		`{"field":"asset.inventory_no","norm":"inv-1"}`, `"title":"web"`, `"entity_id":"c1"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("full document lacks %s: %s", want, raw)
		}
	}
}
