package savedview_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
)

// TestSavedViewScope runs saved views and their query engine with the
// principal's tenant scope (TEN-06, WP-015): a view executed by a principal
// restricted to client 1 returns no client-2 CI, and views of another
// organization stay invisible.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSavedViewScope(t *testing.T) {
	f := scopetest.Seed(t, "26")
	own := f.CI(t, f.OrgA, f.Client1, "view-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "view-c2")

	ctx := f.ClientCtx(f.Client1)
	engine := savedview.NewPGQueryEngine(f.App)
	results, total, err := engine.Query(ctx, f.OrgA, savedview.FilterSpec{EntityKind: "ci"}, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
	}
	if !seen[own] || seen[foreign] || total != len(results) {
		t.Fatalf("client-1 view result: own=%v foreign=%v total=%d, want true/false/%d", seen[own], seen[foreign], total, len(results))
	}

	repo := savedview.NewPGRepository(f.App)
	viewB := &savedview.View{OrganizationID: f.OrgB, OwnerID: f.User, Name: "org-b", EntityKind: "ci", FilterSpec: map[string]any{}}
	if err = repo.Create(f.OrgCtx(f.OrgB), viewB); err != nil {
		t.Fatalf("create organization B view: %v", err)
	}
	viewA := &savedview.View{OrganizationID: f.OrgA, OwnerID: f.User, Name: "org-a", EntityKind: "ci", FilterSpec: map[string]any{}}
	if err = repo.Create(ctx, viewA); err != nil {
		t.Fatalf("create organization A view: %v", err)
	}
	views, _, err := repo.List(ctx, f.OrgA, f.User, api.PaginationParams{Limit: 100})
	if err != nil || len(views) != 1 || views[0].ID != viewA.ID {
		t.Fatalf("organization A views: %+v err=%v, want only the A view", views, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgB, viewB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read organization B view: got %v, want ErrTenantMismatch", err)
	}
	if _, _, err = engine.Query(context.Background(), f.OrgA, savedview.FilterSpec{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("query without scope: got %v, want ErrNoTenantScope", err)
	}
}
