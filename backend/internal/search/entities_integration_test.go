package search_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
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

// TestLocationAndContactHitsLeadToTheirEntity covers SRC-01 (WP-594, F-334)
// against PostgreSQL: a location hit links to its node in the tree
// (/locations?node=<id>) instead of the root, a contact hit has no url (there
// is no contact page) and carries name, e-mail, phone and role for the
// contact card of the command palette, with and without the PII vault. The
// index triggers, the next write of a row with an old url and the reindex
// give the same urls; a principal of another client gets no contact hit.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationAndContactHitsLeadToTheirEntity(t *testing.T) {
	f := scopetest.Seed(t, "bb")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	other := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client2})
	idx := search.NewPGRepository(f.App)
	hit := func(ctx context.Context, text, entityType, id string) *search.Hit {
		t.Helper()
		res, err := idx.Query(ctx, search.Query{OrganizationID: f.OrgA, UserID: f.User, Text: text, EntityTypes: []string{entityType}, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for i := range res.Data {
			if res.Data[i].EntityID == id {
				return &res.Data[i]
			}
		}
		return nil
	}

	locs := locations.NewPGRepository(f.App)
	site, err := locs.Create(ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "Traunstein Ostwerk"})
	if err != nil {
		t.Fatal(err)
	}
	building, err := locs.Create(ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site.ID, Name: "Ostwerk Halle"})
	if err != nil {
		t.Fatal(err)
	}
	wantLocation := func(stage string) {
		t.Helper()
		h := hit(ctx, "Halle", "location", building.ID)
		if h == nil || h.URL != "/locations?node="+building.ID {
			t.Errorf("%s: location hit = %+v, want url /locations?node=%s", stage, h, building.ID)
		}
		if h := hit(other, "Halle", "location", building.ID); h != nil {
			t.Errorf("%s: location visible to another client: %+v", stage, h)
		}
	}
	wantLocation("trigger")

	contacts := contact.NewPGRepository(f.App)
	wantContact := func(stage, text string, c *contact.Contact) {
		t.Helper()
		h := hit(ctx, text, "contact", c.ID)
		if h == nil {
			t.Errorf("%s: no contact hit for %q", stage, text)
			return
		}
		want := map[string]string{"email": c.Email, "phone": c.Phone, "role": c.Role}
		if h.URL != "" || h.Title != c.DisplayName || len(h.Metadata) != len(want) {
			t.Errorf("%s: contact hit = %+v, want empty url, title %q and metadata %v", stage, h, c.DisplayName, want)
		}
		for k, v := range want {
			if h.Metadata[k] != v {
				t.Errorf("%s: contact hit metadata[%s] = %q, want %q", stage, k, h.Metadata[k], v)
			}
		}
		if h := hit(other, text, "contact", c.ID); h != nil {
			t.Errorf("%s: contact visible to another client: %+v", stage, h)
		}
	}
	plain := &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client1, DisplayName: "Gerda Kabelzug",
		Email: "gerda@example.org", Phone: "+49 861 4711", Role: "Netzbetrieb"}
	if err = contacts.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	wantContact("trigger", "Kabelzug", plain)

	// A row that still has the url of an older version is corrected by the
	// next write of its entity.
	if _, err = f.Admin.Exec(bg, `UPDATE search_document SET url = CASE entity_type WHEN 'location' THEN '/locations' ELSE '/contacts' END
		WHERE organization_id = $1 AND entity_id IN ($2, $3)`, f.OrgA, building.ID, plain.ID); err != nil {
		t.Fatal(err)
	}
	renamed := "Ostwerk Halle Nord"
	if _, err = locs.Update(ctx, f.OrgA, building.ID, locations.UpdateRequest{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	notes := "Rufbereitschaft"
	if _, err = contacts.Update(ctx, f.OrgA, plain.ID, contact.UpdateContactRequest{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	wantLocation("update")
	wantContact("update", "Kabelzug", plain)

	// The reindex writes the same urls.
	if _, err = idx.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	wantLocation("reindex")
	wantContact("reindex", "Kabelzug", plain)

	// With the PII vault, name, e-mail and phone come from the vault entry.
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	vaulted := &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client1, DisplayName: "Hubert Patchfeld",
		Email: "hubert@example.org", Phone: "+49 861 815", Role: "Leitstelle"}
	if err = contacts.Create(ctx, vaulted); err != nil {
		t.Fatal(err)
	}
	wantContact("vault", "Patchfeld", vaulted)
}
