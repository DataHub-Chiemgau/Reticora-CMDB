package rack_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
)

// TestRackRepositoryClientScope runs the rack repository with the
// principal's tenant scope (TEN-06, WP-014): a principal restricted to client
// 1 neither sees nor changes a rack on a client-2 site or its mounts, cannot
// mount a client-2 CI, and the occupancy check still sees every mount.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "22")
	bg := context.Background()
	room := func(clientID, name string) string {
		t.Helper()
		var roomID string
		if err := f.Admin.QueryRow(bg, `
			WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id),
			     b AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, $3 FROM s RETURNING id)
			INSERT INTO room (organization_id, building_id, name) SELECT $1, id, $3 FROM b RETURNING id::text`,
			f.OrgA, clientID, name).Scan(&roomID); err != nil {
			t.Fatalf("seed room %s: %v", name, err)
		}
		return roomID
	}
	ownRoom := room(f.Client1, "rack-room-c1")
	foreignRoom := room(f.Client2, "rack-room-c2")
	ownCI := f.CI(t, f.OrgA, f.Client1, "rack-ci-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "rack-ci-c2")

	repo := rack.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownRack := &rack.Rack{OrganizationID: f.OrgA, RoomID: ownRoom, Name: "rack-c1", HeightU: 42}
	foreignRack := &rack.Rack{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "rack-c2", HeightU: 42}
	for _, rk := range []*rack.Rack{ownRack, foreignRack} {
		if err := repo.CreateRack(orgCtx, rk); err != nil {
			t.Fatalf("org-wide rack %s: %v", rk.Name, err)
		}
	}
	// A client-2 CI mounted in the client-1 rack (shared rack).
	sharedMount := &rack.RackMount{OrganizationID: f.OrgA, RackID: ownRack.ID, CIID: foreignCI, PositionU: 1, HeightU: 2, Face: "front"}
	if err := repo.CreateMount(orgCtx, sharedMount); err != nil {
		t.Fatalf("org-wide mount: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	racks, _, err := repo.ListRacks(ctx, f.OrgA, "", page)
	if err != nil || len(racks) != 1 || racks[0].ID != ownRack.ID {
		t.Fatalf("client-1 racks: %+v err=%v, want only the client-1 rack", racks, err)
	}
	if _, err = repo.GetRack(ctx, f.OrgA, foreignRack.ID); err == nil {
		t.Fatal("client-1 principal read a rack on a client-2 site")
	}
	if err = repo.DeleteRack(ctx, f.OrgA, foreignRack.ID); err == nil {
		t.Fatal("client-1 principal deleted a rack on a client-2 site")
	}
	if err = repo.CreateRack(ctx, &rack.Rack{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "intruder", HeightU: 42}); err == nil {
		t.Fatal("client-1 principal created a rack on a client-2 site")
	}

	mounts, total, err := repo.ListMounts(ctx, f.OrgA, ownRack.ID, page)
	if err != nil || total != 0 || len(mounts) != 0 {
		t.Fatalf("client-1 mounts: total=%d %+v err=%v, want none (the CI belongs to client 2)", total, mounts, err)
	}
	if err = repo.DeleteMount(ctx, f.OrgA, sharedMount.ID); err == nil {
		t.Fatal("client-1 principal removed the mount of a client-2 CI")
	}
	if err = repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: ownRack.ID, CIID: foreignCI, PositionU: 10, HeightU: 1, Face: "front"}); err == nil {
		t.Fatal("client-1 principal mounted a client-2 CI")
	}
	// The occupied units stay occupied although the mount is invisible.
	if err = repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: ownRack.ID, CIID: ownCI, PositionU: 2, HeightU: 1, Face: "front"}); !errors.Is(err, rack.ErrValidation) {
		t.Fatalf("client-1 mount over an invisible mount: got %v, want ErrValidation", err)
	}
	if err = repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: ownRack.ID, CIID: ownCI, PositionU: 5, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("client-1 mount of own CI: %v", err)
	}

	var count int
	if err = f.Admin.QueryRow(bg,
		`SELECT (SELECT count(*) FROM rack WHERE id = $1) + (SELECT count(*) FROM rack_mount WHERE id = $2)`,
		foreignRack.ID, sharedMount.ID).Scan(&count); err != nil {
		t.Fatalf("count client-2 rows: %v", err)
	}
	if count != 2 {
		t.Fatal("client-2 rack or mount was deleted")
	}
	if _, _, err = repo.ListRacks(bg, f.OrgA, "", page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
