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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// The tests of this file cover WP-033 (TEC-12): the OpenSearch path filters
// every query server-side on the principal's client and site scope, index
// documents carry client and site, and hits outside the scope never leave the
// backend even when the index returns them.

const (
	osOrg     = "11111111-1111-4111-8111-111111111111"
	osClient1 = "11111111-1111-4111-8111-0000000000c1"
	osClient2 = "11111111-1111-4111-8111-0000000000c2"
	osSite1   = "11111111-1111-4111-8111-0000000000a1"
)

func restricted(clients, sites []string) *database.TenantScope {
	scope := database.OrgWideScope(osOrg, "user")
	if clients != nil {
		scope.Clients = database.ScopeIDs(clients...)
	}
	if sites != nil {
		scope.Sites = database.ScopeIDs(sites...)
	}
	return &scope
}

func TestOpenSearchQueryFiltersOnScope(t *testing.T) {
	q := Query{OrganizationID: osOrg, Text: "router"}

	body, err := buildOpenSearchQuery(&q, restricted(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "client_id") || strings.Contains(string(raw), "site_id") {
		t.Errorf("org-wide principal got scope filters: %s", raw)
	}

	body, err = buildOpenSearchQuery(&q, restricted([]string{osClient1}, []string{osSite1}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(body)
	for _, want := range []string{
		`{"terms":{"client_id":["` + osClient1 + `"]}}`,
		`{"must_not":{"exists":{"field":"client_id"}}}`,
		`{"terms":{"site_id":["` + osSite1 + `"]}}`,
		`{"must_not":{"exists":{"field":"site_id"}}}`,
		`{"must_not":{"terms":{"entity_type":["document"]}}}`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("restricted query lacks %s: %s", want, raw)
		}
	}

	teams := restricted(nil, nil)
	teams.Teams = database.ScopeIDs("team-1")
	body, _ = buildOpenSearchQuery(&q, teams)
	raw, _ = json.Marshal(body)
	if !strings.Contains(string(raw), `"entity_type":["ticket"]`) {
		t.Errorf("team-restricted query does not exclude tickets: %s", raw)
	}

	if _, err := buildOpenSearchQuery(&q, nil); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("query without scope: %v, want ErrNoTenantScope", err)
	}
	other := database.OrgWideScope("22222222-2222-4222-8222-222222222222", "user")
	if _, err := buildOpenSearchQuery(&q, &other); !errors.Is(err, database.ErrTenantMismatch) {
		t.Errorf("query of another organization: %v, want ErrTenantMismatch", err)
	}
}

// TestOpenSearchDropsHitsOutsideScope serves hits of every client from a fake
// index, as if its mapping ignored the filters: the backend returns only those
// in scope.
func TestOpenSearchDropsHitsOutsideScope(t *testing.T) {
	var request []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, _ = io.ReadAll(r.Body)
		hit := func(id, entityType, client, site string) string {
			return `{"_score":1,"_source":{"organization_id":"` + osOrg + `","entity_type":"` + entityType +
				`","entity_id":"` + id + `","title":"x","url":"/","client_id":"` + client + `","site_id":"` + site + `"}}`
		}
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":6},"hits":[` + strings.Join([]string{
			hit("own", "ci", osClient1, osSite1),
			hit("org-wide", "ci", "", ""),
			hit("foreign-client", "ci", osClient2, ""),
			hit("foreign-site", "ci", osClient1, "11111111-1111-4111-8111-0000000000a2"),
			hit("document", "document", "", ""),
			`{"_score":1,"_source":{"organization_id":"22222222-2222-4222-8222-222222222222","entity_type":"ci","entity_id":"other-org","title":"x","url":"/"}}`,
		}, ",") + `]}}`))
	}))
	defer srv.Close()

	b := NewOpenSearchBackend(OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client())
	ctx := database.ContextWithTenantScope(context.Background(), restricted([]string{osClient1}, []string{osSite1}))
	res, err := b.Query(ctx, Query{OrganizationID: osOrg, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range res.Data {
		got[h.EntityID] = true
	}
	if len(got) != 2 || !got["own"] || !got["org-wide"] {
		t.Errorf("hits %v, want only own and org-wide", got)
	}
	if !strings.Contains(string(request), osClient1) {
		t.Errorf("request to the index lacks the client filter: %s", request)
	}

	if _, err := b.Query(context.Background(), Query{OrganizationID: osOrg, Text: "x"}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("query without tenant scope: %v, want ErrNoTenantScope", err)
	}
}

// TestCIIndexerCarriesScope shows that CI documents reach the remote index
// with their client and site.
func TestCIIndexerCarriesScope(t *testing.T) {
	var indexed Document
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&indexed)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	indexer := NewCIIndexer(NewOpenSearchBackend(OpenSearchConfig{URL: srv.URL, Index: "idx"}, srv.Client()))
	item := &ci.Item{ID: "ci-1", OrganizationID: osOrg, ClientID: osClient1, SiteID: osSite1, Name: "router"}
	if err := indexer.IndexDocument(context.Background(), ci.IndexDocumentFor(item)); err != nil {
		t.Fatal(err)
	}
	if indexed.ClientID != osClient1 || indexed.SiteID != osSite1 {
		t.Errorf("indexed document client %q site %q, want the CI's", indexed.ClientID, indexed.SiteID)
	}
}
