package search_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// TestSearchRepositoryClientScope runs the PostgreSQL search with the
// principal's tenant scope (TEN-06, WP-015): a principal restricted to client
// 1 gets no hits for client-2 CIs, while a reindex it triggers still rebuilds
// the index of the whole organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "25")
	own := f.CI(t, f.OrgA, f.Client1, "scopetest-search-own")
	foreign := f.CI(t, f.OrgA, f.Client2, "scopetest-search-foreign")

	repo := search.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for id, title := range map[string]string{own: "scopetest-search-own", foreign: "scopetest-search-foreign"} {
		if err := repo.IndexDocument(orgCtx, search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, Title: title, URL: "/cmdb/" + id}); err != nil {
			t.Fatalf("index %s: %v", title, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	hits := func(c context.Context) map[string]bool {
		t.Helper()
		res, err := repo.Query(c, search.Query{OrganizationID: f.OrgA, Text: "scopetest", Limit: 50})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out := map[string]bool{}
		for _, h := range res.Data {
			out[h.EntityID] = true
		}
		return out
	}
	if got := hits(ctx); !got[own] || got[foreign] {
		t.Fatalf("client-1 hits: own=%v foreign=%v, want true/false", got[own], got[foreign])
	}
	if got := hits(orgCtx); !got[own] || !got[foreign] {
		t.Fatalf("org-wide hits: own=%v foreign=%v, want true/true", got[own], got[foreign])
	}

	// A reindex by the client-scoped principal keeps the client-2 document.
	if _, err := repo.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	var count int
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM search_document WHERE organization_id = $1 AND entity_id = $2`, f.OrgA, foreign).Scan(&count); err != nil {
		t.Fatalf("count foreign document: %v", err)
	}
	if count != 1 {
		t.Fatalf("index of client-2 CI after client-1 reindex: %d documents, want 1", count)
	}
	if _, err := repo.Query(context.Background(), search.Query{OrganizationID: f.OrgA, Text: "scopetest"}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("query without scope: got %v, want ErrNoTenantScope", err)
	}
}
