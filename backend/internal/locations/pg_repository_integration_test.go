package locations_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// label is the ltree label of a node id (location_label in migration 000062).
func label(id string) string { return strings.ReplaceAll(id, "-", "_") }

func mustCreate(t *testing.T, repo *locations.PGRepository, ctx context.Context, orgID string, req locations.CreateRequest) *locations.Location {
	t.Helper()
	loc, err := repo.Create(ctx, orgID, req)
	if err != nil {
		t.Fatalf("create %s %q: %v", req.Kind, req.Name, err)
	}
	return loc
}

// tree builds site > building > room > rack and site > warehouse > zone >
// shelf > bin for clientID.
type tree struct {
	site, building, room, rack, warehouse, zone, shelf, bin *locations.Location
}

func buildTree(t *testing.T, repo *locations.PGRepository, ctx context.Context, orgID, clientID, name string) tree {
	t.Helper()
	var tr tree
	tr.site = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindSite, ClientID: clientID, Name: name})
	tr.building = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.site.ID, Name: name + "-b"})
	tr.room = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindRoom, ParentID: tr.building.ID, Name: name + "-r"})
	tr.rack = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindRack, ParentID: tr.room.ID, Name: name + "-k"})
	tr.warehouse = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: tr.site.ID, Name: name + "-w"})
	tr.zone = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindZone, ParentID: tr.warehouse.ID, Name: name + "-z"})
	tr.shelf = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindShelf, ParentID: tr.zone.ID, Name: name + "-s"})
	tr.bin = mustCreate(t, repo, ctx, orgID, locations.CreateRequest{Kind: locations.KindBin, ParentID: tr.shelf.ID, Name: name + "-n"})
	return tr
}

func TestLocationTreeMatrixAndDerivation(t *testing.T) {
	f := scopetest.Seed(t, "43")
	repo := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	tr := buildTree(t, repo, ctx, f.OrgA, f.Client1, "s1")

	// Derivation: every node carries the site and client of its site and the
	// path of its parent plus its own label.
	nodes := []*locations.Location{tr.building, tr.room, tr.rack, tr.warehouse, tr.zone, tr.shelf, tr.bin}
	parents := []*locations.Location{tr.site, tr.building, tr.room, tr.site, tr.warehouse, tr.zone, tr.shelf}
	for i, n := range nodes {
		if n.SiteID != tr.site.ID || n.ClientID != f.Client1 {
			t.Errorf("%s: site %s client %s, want %s %s", n.Kind, n.SiteID, n.ClientID, tr.site.ID, f.Client1)
		}
		if want := parents[i].Path + "." + label(n.ID); n.Path != want {
			t.Errorf("%s: path %s, want %s", n.Kind, n.Path, want)
		}
	}
	if tr.site.Path != label(tr.site.ID) || tr.site.SiteID != tr.site.ID {
		t.Errorf("site: path %s site %s", tr.site.Path, tr.site.SiteID)
	}

	// Parent matrix through the repository.
	for _, c := range []struct {
		name string
		req  locations.CreateRequest
	}{
		{"building below building", locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.building.ID, Name: "x"}},
		{"zone below site", locations.CreateRequest{Kind: locations.KindZone, ParentID: tr.site.ID, Name: "x"}},
		{"rack below warehouse", locations.CreateRequest{Kind: locations.KindRack, ParentID: tr.warehouse.ID, Name: "x"}},
		{"bin below zone", locations.CreateRequest{Kind: locations.KindBin, ParentID: tr.zone.ID, Name: "x"}},
		{"warehouse without parent", locations.CreateRequest{Kind: locations.KindWarehouse, Name: "x"}},
		{"site with parent", locations.CreateRequest{Kind: locations.KindSite, ParentID: tr.site.ID, ClientID: f.Client1, Name: "x"}},
	} {
		if _, err := repo.Create(ctx, f.OrgA, c.req); !errors.Is(err, locations.ErrInvalidParent) {
			t.Errorf("%s: got %v, want ErrInvalidParent", c.name, err)
		}
	}

	// The database enforces matrix and derivation for every writer, here the
	// maintenance connection without RLS: a spoofed client and site are
	// overwritten, a wrong parent kind is rejected.
	bg := context.Background()
	spoofID := f.ID()
	var client, site, path string
	if err := f.Admin.QueryRow(bg, `
		INSERT INTO location (id, organization_id, client_id, site_id, kind, parent_id, name, path)
		VALUES ($1, $2, $3, $1, 'zone', $4, 'spoof', 'x')
		RETURNING client_id::text, site_id::text, path::text`,
		spoofID, f.OrgA, f.Client2, tr.warehouse.ID).Scan(&client, &site, &path); err != nil {
		t.Fatalf("insert zone with spoofed scope: %v", err)
	}
	if client != f.Client1 || site != tr.site.ID || path != tr.warehouse.Path+"."+label(spoofID) {
		t.Errorf("spoofed zone derived client %s site %s path %s", client, site, path)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'shelf', $2, 'x')`,
		f.OrgA, tr.warehouse.ID); err == nil || !strings.Contains(err.Error(), "location_parent_matrix") && !strings.Contains(err.Error(), "cannot be placed") {
		t.Errorf("shelf below warehouse: got %v, want matrix violation", err)
	}

	// Specialist kinds change through their table only and need a row there.
	if _, err := f.Admin.Exec(bg, `UPDATE location SET name = 'renamed' WHERE id = $1`, tr.building.ID); err == nil {
		t.Error("renaming a building through location: want error")
	}
	if _, err := f.Admin.Exec(bg, `UPDATE building SET name = 'renamed' WHERE id = $1`, tr.building.ID); err != nil {
		t.Fatalf("rename building: %v", err)
	}
	if got, err := repo.Get(ctx, f.OrgA, tr.building.ID); err != nil || got.Name != "renamed" {
		t.Errorf("building location after rename: %+v, %v", got, err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'room', $2, 'orphan')`,
		f.OrgA, tr.building.ID); err == nil {
		t.Error("room location without room row: want error at commit")
	}
}

func TestLocationTreeMoveAndCycle(t *testing.T) {
	f := scopetest.Seed(t, "44")
	repo := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	a := buildTree(t, repo, ctx, f.OrgA, f.Client1, "a")
	b := buildTree(t, repo, ctx, f.OrgA, f.Client2, "b")

	// Cycle: a warehouse cannot move below its own shelf.
	if _, err := repo.Move(ctx, f.OrgA, a.zone.ID, a.shelf.ID); !errors.Is(err, locations.ErrCycle) {
		t.Errorf("zone below own shelf: got %v, want ErrCycle", err)
	}
	// Matrix on move.
	if _, err := repo.Move(ctx, f.OrgA, a.zone.ID, b.site.ID); !errors.Is(err, locations.ErrInvalidParent) {
		t.Errorf("zone below site: got %v, want ErrInvalidParent", err)
	}
	if _, err := repo.Move(ctx, f.OrgA, a.site.ID, b.site.ID); !errors.Is(err, locations.ErrInvalidParent) {
		t.Errorf("site below site: got %v, want ErrInvalidParent", err)
	}

	// Moving a building (specialist table) and a warehouse (location only) to
	// the site of client 2 re-derives the whole subtree.
	if _, err := repo.Move(ctx, f.OrgA, a.building.ID, b.site.ID); err != nil {
		t.Fatalf("move building: %v", err)
	}
	if _, err := repo.Move(ctx, f.OrgA, a.warehouse.ID, b.site.ID); err != nil {
		t.Fatalf("move warehouse: %v", err)
	}
	for root, want := range map[*locations.Location]int{a.building: 3, a.warehouse: 4} {
		sub, err := repo.Subtree(ctx, f.OrgA, root.ID)
		if err != nil {
			t.Fatalf("subtree %s: %v", root.Kind, err)
		}
		if len(sub) != want {
			t.Fatalf("subtree %s: %d nodes, want %d", root.Kind, len(sub), want)
		}
		for _, n := range sub {
			if n.SiteID != b.site.ID || n.ClientID != f.Client2 || !strings.HasPrefix(n.Path, b.site.Path+".") {
				t.Errorf("%s %s after move: site %s client %s path %s", n.Kind, n.Name, n.SiteID, n.ClientID, n.Path)
			}
		}
	}
	var siteOfBuilding string
	if err := f.Admin.QueryRow(context.Background(), `SELECT site_id::text FROM building WHERE id = $1`, a.building.ID).Scan(&siteOfBuilding); err != nil || siteOfBuilding != b.site.ID {
		t.Errorf("building.site_id after move: %s, %v", siteOfBuilding, err)
	}

	// A site changing its client takes its subtree along.
	if _, err := f.Admin.Exec(context.Background(), `UPDATE site SET client_id = $2 WHERE id = $1`, b.site.ID, f.Client1); err != nil {
		t.Fatalf("move site b to client 1: %v", err)
	}
	for _, id := range []string{b.site.ID, a.rack.ID, a.zone.ID, b.bin.ID} {
		if got, err := repo.Get(ctx, f.OrgA, id); err != nil || got.ClientID != f.Client1 {
			t.Errorf("node %s after site client change: %+v, %v", id, got, err)
		}
	}

	// Deleting a site removes its tree, specialist rows included.
	if _, err := f.Admin.Exec(context.Background(), `DELETE FROM site WHERE id = $1`, b.site.ID); err != nil {
		t.Fatalf("delete site: %v", err)
	}
	var left int
	if err := f.Admin.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM location WHERE site_id = $1) + (SELECT count(*) FROM room WHERE id = $2)`,
		b.site.ID, a.room.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows left after site delete: %d, %v", left, err)
	}
}

func TestLocationTreeScope(t *testing.T) {
	f := scopetest.Seed(t, "45")
	repo := locations.NewPGRepository(f.App)
	a := buildTree(t, repo, f.OrgCtx(f.OrgA), f.OrgA, f.Client1, "a")
	b := buildTree(t, repo, f.OrgCtx(f.OrgA), f.OrgA, f.Client2, "b")

	// Client scope: client 1 sees its tree only and cannot attach below the
	// tree of client 2.
	c1 := f.ClientCtx(f.Client1)
	if sub, err := repo.Subtree(c1, f.OrgA, a.site.ID); err != nil || len(sub) != 8 {
		t.Errorf("client 1 own subtree: %d nodes, %v", len(sub), err)
	}
	if _, err := repo.Subtree(c1, f.OrgA, b.site.ID); !errors.Is(err, locations.ErrNotFound) {
		t.Errorf("client 1 foreign subtree: got %v, want ErrNotFound", err)
	}
	if _, err := repo.Create(c1, f.OrgA, locations.CreateRequest{Kind: locations.KindZone, ParentID: b.warehouse.ID, Name: "x"}); !errors.Is(err, locations.ErrInvalidParent) {
		t.Errorf("client 1 zone below foreign warehouse: got %v, want ErrInvalidParent", err)
	}
	if _, err := repo.Create(c1, f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: b.site.ID, Name: "x"}); err == nil {
		t.Error("client 1 building below foreign site: want error")
	}
	if _, err := repo.Create(c1, f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client2, Name: "x"}); err == nil {
		t.Error("client 1 site for client 2: want error")
	}
	if _, err := repo.Move(c1, f.OrgA, a.zone.ID, b.warehouse.ID); err == nil {
		t.Error("client 1 moves zone below foreign warehouse: want error")
	}
	if list, err := repo.List(c1, f.OrgA, locations.Filter{}); err != nil || len(list) != 8 {
		t.Errorf("client 1 list: %d nodes, %v; want its 8", len(list), err)
	} else {
		for _, n := range list {
			if n.ClientID != f.Client1 {
				t.Errorf("client 1 lists foreign node %s", n.ID)
			}
		}
	}
	if err := repo.Delete(c1, f.OrgA, b.bin.ID); !errors.Is(err, locations.ErrNotFound) {
		t.Errorf("client 1 deletes foreign bin: got %v, want ErrNotFound", err)
	}
	if _, err := repo.Get(f.OrgCtx(f.OrgA), f.OrgA, b.bin.ID); err != nil {
		t.Errorf("foreign bin after delete attempt: %v", err)
	}
	if _, err := repo.List(context.Background(), f.OrgA, locations.Filter{}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("list without scope: got %v, want ErrNoTenantScope", err)
	}

	// Site scope: a principal restricted to site a sees nothing of site b,
	// even within its own client.
	other := mustCreate(t, repo, f.OrgCtx(f.OrgA), f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "a2"})
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Sites = database.ScopeIDs(a.site.ID)
	siteCtx := database.ContextWithTenantScope(context.Background(), &scope)
	if _, err := repo.Get(siteCtx, f.OrgA, a.bin.ID); err != nil {
		t.Errorf("site scope own bin: %v", err)
	}
	if _, err := repo.Get(siteCtx, f.OrgA, other.ID); !errors.Is(err, locations.ErrNotFound) {
		t.Errorf("site scope other site: got %v, want ErrNotFound", err)
	}

	// Organization B sees nothing of organization A.
	if _, err := repo.Get(f.OrgCtx(f.OrgB), f.OrgB, a.site.ID); !errors.Is(err, locations.ErrNotFound) {
		t.Errorf("org B: got %v, want ErrNotFound", err)
	}
}

// TestLocationTreeUpdateAndDelete covers WP-053: rename through the
// specialist table, rename and move in one call, field errors of the parent
// matrix, and delete of leaves only.
func TestLocationTreeUpdateAndDelete(t *testing.T) {
	f := scopetest.Seed(t, "56")
	repo := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	tr := buildTree(t, repo, ctx, f.OrgA, f.Client1, "u")

	name := "u-room-renamed"
	room, err := repo.Update(ctx, f.OrgA, tr.room.ID, locations.UpdateRequest{Name: &name})
	if err != nil || room.Name != name {
		t.Fatalf("rename room: %+v, %v", room, err)
	}
	var specialist string
	if err = f.Admin.QueryRow(context.Background(), `SELECT name FROM room WHERE id = $1`, tr.room.ID).Scan(&specialist); err != nil || specialist != name {
		t.Errorf("room table name %q, %v; want %q", specialist, err, name)
	}

	b2 := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.site.ID, Name: "u-b2"})
	name = "u-room-moved"
	room, err = repo.Update(ctx, f.OrgA, tr.room.ID, locations.UpdateRequest{Name: &name, ParentID: &b2.ID})
	if err != nil || room.Name != name || room.ParentID != b2.ID || room.Path != b2.Path+"."+label(room.ID) {
		t.Fatalf("rename and move room: %+v, %v", room, err)
	}

	var fe *locations.FieldError
	for _, c := range []struct {
		req   locations.CreateRequest
		field string
	}{
		{locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.warehouse.ID, Name: "x"}, "parent_id"},
		{locations.CreateRequest{Kind: locations.KindZone, ParentID: f.ID(), Name: "x"}, "parent_id"},
		{locations.CreateRequest{Kind: locations.KindSite, ClientID: f.ID(), Name: "x"}, "client_id"},
	} {
		if _, err = repo.Create(ctx, f.OrgA, c.req); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("create %s below %s: %v, want field %s", c.req.Kind, c.req.ParentID, err, c.field)
		}
	}
	if _, err = repo.Update(ctx, f.OrgA, tr.warehouse.ID, locations.UpdateRequest{ParentID: &tr.bin.ID}); !errors.As(err, &fe) || fe.Field != "parent_id" {
		t.Errorf("move warehouse below its bin: %v, want parent_id field error", err)
	}

	if err = repo.Delete(ctx, f.OrgA, tr.shelf.ID); !errors.Is(err, locations.ErrHasChildren) {
		t.Errorf("delete shelf with bin: %v, want ErrHasChildren", err)
	}
	if err = repo.Delete(ctx, f.OrgA, tr.rack.ID); err != nil {
		t.Errorf("delete rack: %v", err)
	}
	var racks int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM rack WHERE id = $1`, tr.rack.ID).Scan(&racks); err != nil || racks != 0 {
		t.Errorf("rack row after delete: %d, %v", racks, err)
	}
	if err = repo.Delete(ctx, f.OrgA, "not-a-uuid"); !errors.Is(err, locations.ErrNotFound) {
		t.Errorf("delete malformed id: %v, want ErrNotFound", err)
	}
	if list, listErr := repo.List(ctx, f.OrgA, locations.Filter{ParentID: tr.site.ID}); listErr != nil || len(list) != 3 {
		t.Errorf("children of the site: %d, %v; want 3", len(list), listErr)
	}
	if list, listErr := repo.List(ctx, f.OrgA, locations.Filter{Kind: locations.KindBin, Search: "U-N"}); listErr != nil || len(list) != 1 {
		t.Errorf("bins matching u-n: %d, %v; want 1", len(list), listErr)
	}
}
