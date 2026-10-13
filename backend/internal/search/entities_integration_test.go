package search_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLocationsAndContactsAreHitsAgainstPostgres covers SRC-01 (WP-186)
// against PostgreSQL: a location and a contact are search hits as soon as
// they are written (no reindex), changes and deletions follow, and a
// principal of another client does not see the client's contact.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationsAndContactsAreHitsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bb")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	idx := search.NewPGRepository(f.App)
	find := func(ctx context.Context, text, entityType string) []search.Hit {
		t.Helper()
		res, err := idx.Query(ctx, search.Query{OrganizationID: f.OrgA, UserID: f.User, Text: text, EntityTypes: []string{entityType}, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return res.Data
	}

	site, err := locations.NewPGRepository(f.App).Create(ctx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "Rosenheim Nordwerk"})
	if err != nil {
		t.Fatal(err)
	}
	if hits := find(ctx, "Nordwerk", "location"); len(hits) != 1 || hits[0].EntityID != site.ID {
		t.Errorf("location hit: %+v", hits)
	}

	contacts := contact.NewPGRepository(f.App)
	c := &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client1, DisplayName: "Ingrid Netzwart", Role: "Leitstelle"}
	if err = contacts.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if hits := find(ctx, "Leitstelle", "contact"); len(hits) != 1 || hits[0].EntityID != c.ID {
		t.Errorf("contact hit: %+v", hits)
	}
	// Another client's principal sees neither.
	other := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client2})
	if hits := find(other, "Leitstelle", "contact"); len(hits) != 0 {
		t.Errorf("contact visible to another client: %+v", hits)
	}
	if hits := find(other, "Nordwerk", "location"); len(hits) != 0 {
		t.Errorf("location visible to another client: %+v", hits)
	}

	// A rename is found under the new name, a deletion is gone.
	if _, err = f.Admin.Exec(context.Background(), `UPDATE site SET name = 'Rosenheim Suedwerk' WHERE id = $1`, site.ID); err != nil {
		t.Fatal(err)
	}
	if hits := find(ctx, "Suedwerk", "location"); len(hits) != 1 {
		t.Errorf("renamed location: %+v", hits)
	}
	if err = contacts.Delete(ctx, f.OrgA, c.ID); err != nil {
		t.Fatal(err)
	}
	if hits := find(ctx, "Leitstelle", "contact"); len(hits) != 0 {
		t.Errorf("deleted contact still a hit: %+v", hits)
	}
}
