package contact_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestContactRepositoryClientScope runs the contact repository with the
// principal's tenant scope (TEN-06, WP-011): a principal restricted to client
// 1 neither sees nor changes a contact of client 2, and CI-contact links are
// visible only when CI and contact are both visible.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestContactRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "14")
	ownCI := f.CI(t, f.OrgA, f.Client1, "contact-ci-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "contact-ci-c2")

	repo := contact.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client1, DisplayName: "Own"}
	foreign := &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client2, DisplayName: "Foreign"}
	for _, c := range []*contact.Contact{own, foreign} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide create %s: %v", c.DisplayName, err)
		}
	}
	crossLink := &contact.CIContact{OrganizationID: f.OrgA, CIID: ownCI, ContactID: foreign.ID, RelationshipType: "owner"}
	if err := repo.Link(orgCtx, crossLink); err != nil {
		t.Fatalf("org-wide link: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 list: got %+v, want only the client-1 contact", list)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a contact of client 2")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal deleted a contact of client 2")
	}
	if err = repo.Create(ctx, &contact.Contact{OrganizationID: f.OrgA, ClientID: f.Client2, DisplayName: "Intruder"}); err == nil {
		t.Fatal("client-1 principal created a contact for client 2")
	}

	links, _, err := repo.ListForCI(ctx, f.OrgA, ownCI, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("client-1 principal sees %d links to a client-2 contact", len(links))
	}
	if err = repo.Link(ctx, &contact.CIContact{OrganizationID: f.OrgA, CIID: foreignCI, ContactID: own.ID, RelationshipType: "owner"}); err == nil {
		t.Fatal("client-1 principal linked a contact to a client-2 CI")
	}
	if err = repo.Unlink(ctx, f.OrgA, crossLink.ID); err == nil {
		t.Fatal("client-1 principal removed a link to a client-2 contact")
	}
	if err = repo.Link(ctx, &contact.CIContact{OrganizationID: f.OrgA, CIID: ownCI, ContactID: own.ID, RelationshipType: "responsible"}); err != nil {
		t.Fatalf("client-1 link within its client: %v", err)
	}

	var remaining int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM contact WHERE id = $1) + (SELECT count(*) FROM ci_contact WHERE id = $2)`,
		foreign.ID, crossLink.ID).Scan(&remaining); err != nil {
		t.Fatalf("count foreign rows: %v", err)
	}
	if remaining != 2 {
		t.Fatal("client-2 contact or its link was deleted")
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
