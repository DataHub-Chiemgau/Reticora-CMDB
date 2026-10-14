package history_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestUnifiedCIHistory covers CI-06 and AUD-03 (WP-171) against
// PostgreSQL: the history of a CI unites its ci_change rows (with the site
// at the time), its entity_change rows and its audit entries in one trail,
// newest first, paginated over all three; another organization sees none
// of it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUnifiedCIHistory(t *testing.T) {
	f := scopetest.Seed(t, "b1")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	site, err := locations.NewPGRepository(f.App).Create(ctx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "history-site"})
	if err != nil {
		t.Fatal(err)
	}
	siteID := site.ID
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, LocationID: siteID, CITypeID: typeID, Name: "history-unified", Status: "active"}
	if err = cis.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	renamed := "history-unified-2"
	if _, err = cis.Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	repo := history.NewPGRepository(f.App)
	if err = repo.Record(ctx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: item.ID, ChangeType: "lifecycle_transition", Comment: "commissioned"}); err != nil {
		t.Fatal(err)
	}

	all, total, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]int{}
	for i, c := range all {
		sources[c.Source]++
		if c.Source == history.SourceCIChange && c.SiteID != siteID {
			t.Errorf("ci_change %s without the site at the time: %q", c.ChangeType, c.SiteID)
		}
		if i > 0 && c.CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("not newest first at %d", i)
		}
	}
	if total != len(all) || sources[history.SourceCIChange] < 2 || sources[history.SourceAudit] < 2 || sources[history.SourceEntityChange] != 1 {
		t.Fatalf("total %d, sources %v", total, sources)
	}
	page, pageTotal, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 2, Offset: 1})
	if err != nil || pageTotal != total || len(page) != 2 || page[0].ID != all[1].ID {
		t.Errorf("second page: %d/%d %v", len(page), pageTotal, err)
	}
	other, otherTotal, err := repo.List(f.OrgCtx(f.OrgB), f.OrgB, "ci", item.ID, api.PaginationParams{Limit: 100})
	if err != nil || otherTotal != 0 || len(other) != 0 {
		t.Errorf("another organization reads %d entries (%v)", otherTotal, err)
	}
}
