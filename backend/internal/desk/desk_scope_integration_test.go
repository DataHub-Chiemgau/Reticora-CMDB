package desk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
)

// TestDeskRepositoryClientScope runs the desk repository with the
// principal's tenant scope (TEN-06, WP-019): a principal restricted to client
// 1 neither sees nor books a desk on a client-2 site. A principal restricted
// to the client-1 site, alone or together with client 1, does not see or book
// the desk on the client-2 site either (WP-340).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeskRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "35")
	ownRoom := f.Room(t, f.Client1, "desk-room-c1")
	foreignRoom := f.Room(t, f.Client2, "desk-room-c2")
	user := f.AppUser(t, f.OrgA, "booker")

	repo := desk.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: ownRoom, Name: "desk-c1", Status: "available", Attributes: map[string]any{}}
	foreignDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "desk-c2", Status: "available", Attributes: map[string]any{}}
	for _, d := range []*desk.Desk{ownDesk, foreignDesk} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide desk %s: %v", d.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != ownDesk.ID {
		t.Fatalf("client-1 desks: %+v err=%v, want only the own one", list, err)
	}
	start := time.Now().UTC().Add(time.Hour)
	booking := func(deskID string) *desk.Booking {
		return &desk.Booking{OrganizationID: f.OrgA, DeskID: deskID, UserID: user, StartsAt: start, EndsAt: start.Add(time.Hour)}
	}
	if _, err = repo.Book(ctx, booking(foreignDesk.ID)); err == nil {
		t.Fatal("client-1 principal booked a desk on a client-2 site")
	}
	if _, err = repo.Book(ctx, booking(ownDesk.ID)); err != nil {
		t.Fatalf("client-1 booking of own desk: %v", err)
	}
	if err = repo.Create(ctx, &desk.Desk{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "intruder", Status: "available", Attributes: map[string]any{}}); err == nil {
		t.Fatal("client-1 principal placed a desk on a client-2 site")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignDesk.ID); err == nil {
		t.Fatal("client-1 principal deleted a desk on a client-2 site")
	}

	// Site-scoped principals.
	var ownSite string
	if err = f.Admin.QueryRow(context.Background(), `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, ownRoom).Scan(&ownSite); err != nil {
		t.Fatalf("site of the client-1 room: %v", err)
	}
	scoped := func(clients database.ScopeSet) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Clients, scope.Sites = clients, database.ScopeIDs(ownSite)
		return database.ContextWithTenantScope(context.Background(), &scope)
	}
	for name, sctx := range map[string]context.Context{
		"site(s1)":           scoped(database.AllScopes()),
		"client+site(c1,s1)": scoped(database.ScopeIDs(f.Client1)),
	} {
		list, _, err = repo.List(sctx, f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 100})
		if err != nil || len(list) != 1 || list[0].ID != ownDesk.ID {
			t.Fatalf("%s desks: %+v err=%v, want only the site-1 desk", name, list, err)
		}
		if _, err = repo.GetByID(sctx, f.OrgA, foreignDesk.ID); err == nil {
			t.Fatalf("%s principal read the desk on site 2", name)
		}
		if _, err = repo.Book(sctx, booking(foreignDesk.ID)); err == nil {
			t.Fatalf("%s principal booked the desk on site 2", name)
		}
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk WHERE room_id = $1`, foreignRoom).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 desks: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
