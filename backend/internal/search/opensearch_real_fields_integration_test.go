package search_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// TestOpenSearchFieldsAgainstRealService checks the parts of the remote
// document and query that TestOpenSearchScopeAgainstRealService does not
// reach, against the OpenSearch of deploy/ (SRC-02, WP-786/WP-787; Upd-02,
// OpenSearch 3.x): the dynamic template of the field rows (fields.<key> as
// text), the nested identifiers with keyword and ip mapping, the
// identifier conditions (address, CIDR range, MAC, MAC prefix) with their
// inner hits and docvalue_fields, and the highlight of the title and the
// field rows with the default and the custom tags. The answers of the
// server are what the own HTTP client of internal/search parses; a changed
// format of a major release fails here, not in production.
//
// It runs only with TEST_OPENSEARCH_URL (the DB job of
// .github/workflows/ci.yml starts the service); it needs no database.
func TestOpenSearchFieldsAgainstRealService(t *testing.T) {
	osURL := strings.TrimRight(os.Getenv("TEST_OPENSEARCH_URL"), "/")
	if osURL == "" {
		t.Skip("TEST_OPENSEARCH_URL not set; skipping OpenSearch integration test")
	}
	bg := context.Background()
	o := &realOpenSearch{url: osURL, index: fmt.Sprintf("reticora-upd02-%d", time.Now().UnixNano()), client: &http.Client{Timeout: 70 * time.Second}}
	var health struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
	}
	if code := o.do(t, http.MethodGet, "/_cluster/health?wait_for_status=yellow&timeout=60s", nil, &health); code != http.StatusOK || health.TimedOut {
		t.Fatalf("OpenSearch at %s is not ready: status %d, cluster %q", osURL, code, health.Status)
	}
	t.Cleanup(func() {
		o.do(t, http.MethodDelete, "/"+o.index, nil, nil)
		o.do(t, http.MethodDelete, "/_index_template/"+o.index, nil, nil)
	})

	remote := search.NewOpenSearchBackend(&search.OpenSearchConfig{URL: osURL, Index: o.index}, nil)
	if err := remote.EnsureIndexTemplate(bg); err != nil {
		t.Fatalf("index template: %v", err)
	}
	const org = "00000000-0000-4000-8000-0000000000a1"
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	router := &search.FullDocument{
		Document: search.Document{
			ID: "d1", OrganizationID: org, EntityType: "ci", EntityID: "ci-router",
			Title: "Edge Router Nord", URL: "/cmdb/ci-router", UpdatedAt: now,
			Fields: map[string]string{"attributes.standort": "Technikraum Nordflügel"},
		},
		Identifiers: []search.IdentifierRow{
			{Field: search.IdentifierManagementIP, Norm: "10.20.30.40", Addr: "10.20.30.40"},
			{Field: search.IdentifierPrimaryMAC, Norm: "001a2b3c4d5e"},
		},
	}
	other := &search.FullDocument{
		Document: search.Document{
			ID: "d2", OrganizationID: org, EntityType: "ci", EntityID: "ci-switch",
			Title: "Access Switch Süd", URL: "/cmdb/ci-switch", UpdatedAt: now,
			Fields: map[string]string{"attributes.standort": "Lager Süd"},
		},
		Identifiers: []search.IdentifierRow{
			{Field: search.IdentifierManagementIP, Norm: "192.168.7.9", Addr: "192.168.7.9"},
			{Field: search.IdentifierPrimaryMAC, Norm: "a0b1c2d3e4f5"},
		},
	}
	for _, doc := range []*search.FullDocument{router, other} {
		if err := remote.IndexFullDocument(bg, doc); err != nil {
			t.Fatalf("index %s: %v", doc.EntityID, err)
		}
	}
	o.refresh(t)

	// The mapping the server derived: field rows as text through the
	// dynamic template, identifiers nested with keyword and ip.
	var mapping map[string]struct {
		Mappings struct {
			Properties map[string]struct {
				Type       string `json:"type"`
				Properties map[string]struct {
					Type       string `json:"type"`
					Properties map[string]struct {
						Type string `json:"type"`
					} `json:"properties"`
				} `json:"properties"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if code := o.do(t, http.MethodGet, "/"+o.index+"/_mapping", nil, &mapping); code != http.StatusOK {
		t.Fatalf("mapping: status %d", code)
	}
	props := mapping[o.index].Mappings.Properties
	if got := props["fields"].Properties["attributes"].Properties["standort"].Type; got != "text" {
		t.Errorf("fields.attributes.standort is mapped as %q, want text (dynamic template)", got)
	}
	ids := props["identifiers"]
	if ids.Type != "nested" || ids.Properties["norm"].Type != "keyword" || ids.Properties["field"].Type != "keyword" || ids.Properties["addr"].Type != "ip" {
		t.Errorf("identifiers mapping = %+v, want nested with field/norm keyword and addr ip", ids)
	}

	scope := database.OrgWideScope(org, "user")
	ctx := database.ContextWithTenantScope(bg, &scope)
	find := func(text string) search.Result {
		t.Helper()
		res, err := remote.Query(ctx, &search.Query{OrganizationID: org, Text: text, Highlight: true, Limit: 10})
		if err != nil {
			t.Fatalf("query %q: %v", text, err)
		}
		return res
	}
	only := func(text string, res search.Result) search.Hit {
		t.Helper()
		if res.Total != 1 || len(res.Data) != 1 || res.Data[0].EntityID != router.EntityID {
			t.Fatalf("query %q: total %d, hits %+v, want only %s", text, res.Total, res.Data, router.EntityID)
		}
		return res.Data[0]
	}
	matched := func(h search.Hit, field, highlight string) bool {
		return slices.Contains(h.MatchedFields, search.MatchedField{Field: field, Highlight: highlight})
	}

	// A field row: highlighted with the custom tags, named by its key.
	h := only("Nordflügel", find("Nordflügel"))
	if !matched(h, "attributes.standort", "Technikraum <mark>Nordflügel</mark>") {
		t.Errorf("field row: matched_fields = %+v", h.MatchedFields)
	}
	if h.Fields != nil {
		t.Errorf("hit carries the field rows: %+v", h.Fields)
	}

	// The title: default tags in highlights, <mark> in matched_fields.
	h = only("Router", find("Router"))
	if !slices.Contains(h.Highlights, "Edge <em>Router</em> Nord") || !matched(h, "title", "Edge <mark>Router</mark> Nord") {
		t.Errorf("title: highlights %q, matched_fields %+v", h.Highlights, h.MatchedFields)
	}

	// The identifiers through the nested query and its inner hits.
	for _, c := range []struct{ text, field, highlight string }{
		{"10.20.30.40", search.IdentifierManagementIP, "10.20.30.40"},
		{"10.20.30.0/24", search.IdentifierManagementIP, "10.20.30.40"},
		{"00:1A:2B:3C:4D:5E", search.IdentifierPrimaryMAC, "00:1a:2b:3c:4d:5e"},
		{"00-1a-2b", search.IdentifierPrimaryMAC, "00:1a:2b:3c:4d:5e"},
	} {
		h := only(c.text, find(c.text))
		if !matched(h, c.field, c.highlight) {
			t.Errorf("identifier %q: matched_fields = %+v, want %s=%s", c.text, h.MatchedFields, c.field, c.highlight)
		}
	}

	// Another organization finds nothing of it.
	scopeB := database.OrgWideScope("00000000-0000-4000-8000-0000000000b2", "user")
	res, err := remote.Query(database.ContextWithTenantScope(bg, &scopeB), &search.Query{OrganizationID: scopeB.OrgID, Text: "Router"})
	if err != nil || res.Total != 0 || len(res.Data) != 0 {
		t.Fatalf("other organization: total %d, hits %+v, err %v", res.Total, res.Data, err)
	}
}
