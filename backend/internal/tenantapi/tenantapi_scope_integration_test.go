package tenantapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// TestTenantRepositoryClientScope runs the client/site/building/room
// repository with the principal's tenant scope (TEN-06, WP-014): a principal
// restricted to client 1 neither sees nor changes client 2, its sites or the
// buildings and rooms on them, and cannot place a building on a client-2
// site.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTenantRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "21")
	repo := tenantapi.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownSite := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "site-c1"}
	foreignSite := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "site-c2"}
	for _, s := range []*tenantapi.Site{ownSite, foreignSite} {
		if err := repo.CreateSite(orgCtx, s); err != nil {
			t.Fatalf("org-wide site %s: %v", s.Name, err)
		}
	}
	foreignBuilding := &tenantapi.Building{OrganizationID: f.OrgA, SiteID: foreignSite.ID, Name: "building-c2"}
	if err := repo.CreateBuilding(orgCtx, foreignBuilding); err != nil {
		t.Fatalf("org-wide building: %v", err)
	}
	foreignRoom := &tenantapi.Room{OrganizationID: f.OrgA, BuildingID: foreignBuilding.ID, Name: "room-c2"}
	if err := repo.CreateRoom(orgCtx, foreignRoom); err != nil {
		t.Fatalf("org-wide room: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	clients, _, err := repo.ListClients(ctx, f.OrgA, page)
	if err != nil || len(clients) != 1 || clients[0].ID != f.Client1 {
		t.Fatalf("client-1 clients: %+v err=%v, want only client 1", clients, err)
	}
	sites, _, err := repo.ListSites(ctx, f.OrgA, "", page)
	if err != nil || len(sites) != 1 || sites[0].ID != ownSite.ID {
		t.Fatalf("client-1 sites: %+v err=%v, want only the client-1 site", sites, err)
	}
	buildings, total, err := repo.ListBuildings(ctx, f.OrgA, "", page)
	if err != nil || total != 0 || len(buildings) != 0 {
		t.Fatalf("client-1 buildings: total=%d %+v err=%v, want none", total, buildings, err)
	}
	rooms, total, err := repo.ListRooms(ctx, f.OrgA, "", page)
	if err != nil || total != 0 || len(rooms) != 0 {
		t.Fatalf("client-1 rooms: total=%d %+v err=%v, want none", total, rooms, err)
	}
	if _, err = repo.GetRoom(ctx, f.OrgA, foreignRoom.ID); err == nil {
		t.Fatal("client-1 principal read a room on a client-2 site")
	}
	name := "renamed"
	if _, err = repo.UpdateBuilding(ctx, f.OrgA, foreignBuilding.ID, tenantapi.UpdateBuildingRequest{Name: &name}); err == nil {
		t.Fatal("client-1 principal renamed a building on a client-2 site")
	}
	if err = repo.DeleteRoom(ctx, f.OrgA, foreignRoom.ID); err == nil {
		t.Fatal("client-1 principal deleted a room on a client-2 site")
	}
	if err = repo.DeleteClient(ctx, f.OrgA, f.Client2); err == nil {
		t.Fatal("client-1 principal deleted client 2")
	}
	if err = repo.CreateBuilding(ctx, &tenantapi.Building{OrganizationID: f.OrgA, SiteID: foreignSite.ID, Name: "intruder"}); err == nil {
		t.Fatal("client-1 principal placed a building on a client-2 site")
	}
	if err = repo.CreateBuilding(ctx, &tenantapi.Building{OrganizationID: f.OrgA, SiteID: ownSite.ID, Name: "building-c1"}); err != nil {
		t.Fatalf("client-1 building on own site: %v", err)
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM room WHERE id = $1) + (SELECT count(*) FROM building WHERE id = $2 AND name = 'building-c2')`,
		foreignRoom.ID, foreignBuilding.ID).Scan(&count); err != nil {
		t.Fatalf("count client-2 rows: %v", err)
	}
	if count != 2 {
		t.Fatal("client-2 room or building was changed")
	}
	if _, _, err = repo.ListSites(context.Background(), f.OrgA, "", page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
