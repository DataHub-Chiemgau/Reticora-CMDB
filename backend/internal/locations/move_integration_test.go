package locations_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestLocationMoveRecordsChangeAndAudit covers WP-055 (LOC-11): moving a node
// re-parents its subtree, rewrites the paths and records location_change and
// an audit entry in the same transaction; a move through a specialist table
// is recorded as well.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationMoveRecordsChangeAndAudit(t *testing.T) {
	f := scopetest.Seed(t, "59")
	bg := context.Background()
	repo := locations.NewPGRepository(f.App)
	user := f.AppUser(t, f.OrgA, "mover")
	// The principal is the same for the session variable (location_change)
	// and the tenant info (audit), as in a request.
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	tr := buildTree(t, repo, ctx, f.OrgA, f.Client1, "mv")
	wh2 := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: tr.site.ID, Name: "mv-w2"})

	zone, err := repo.Move(ctx, f.OrgA, tr.zone.ID, wh2.ID)
	if err != nil {
		t.Fatalf("move zone: %v", err)
	}
	bin, err := repo.Get(ctx, f.OrgA, tr.bin.ID)
	if err != nil || bin.Path != zone.Path+"."+label(tr.shelf.ID)+"."+label(tr.bin.ID) {
		t.Errorf("bin path after the move: %q, %v", bin.Path, err)
	}

	type change struct{ oldParent, newParent, oldPath, newPath, by string }
	changes := func(id string) []change {
		t.Helper()
		rows, queryErr := f.Admin.Query(bg, `SELECT COALESCE(old_parent_id::text, ''), COALESCE(new_parent_id::text, ''), old_path, new_path, COALESCE(changed_by, '')
			FROM location_change WHERE location_id = $1 ORDER BY changed_at`, id)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		defer rows.Close()
		var out []change
		for rows.Next() {
			var c change
			if scanErr := rows.Scan(&c.oldParent, &c.newParent, &c.oldPath, &c.newPath, &c.by); scanErr != nil {
				t.Fatal(scanErr)
			}
			out = append(out, c)
		}
		return out
	}
	want := change{tr.warehouse.ID, wh2.ID, tr.zone.Path, zone.Path, user}
	if got := changes(tr.zone.ID); len(got) != 1 || got[0] != want {
		t.Errorf("location_change of the zone: %+v, want [%+v]", got, want)
	}
	if got := changes(tr.bin.ID); len(got) != 0 {
		t.Errorf("descendants keep their parent and get no change row: %+v", got)
	}
	var audited int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'location.moved'
		AND resource_type = 'location' AND resource_id = $2 AND actor_id = $3`, f.OrgA, tr.zone.ID, user).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("audit entries of the move: %d, %v; want 1", audited, err)
	}

	// A failing move leaves neither a change row nor an audit entry.
	if _, err = repo.Move(ctx, f.OrgA, tr.zone.ID, tr.room.ID); err == nil {
		t.Fatal("zone below a room: want an error")
	}
	if got := changes(tr.zone.ID); len(got) != 1 {
		t.Errorf("change rows after a refused move: %d, want 1", len(got))
	}

	// A room moved through its specialist table is recorded too.
	b2 := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.site.ID, Name: "mv-b2"})
	if _, err = f.Admin.Exec(bg, `UPDATE room SET building_id = $2 WHERE id = $1`, tr.room.ID, b2.ID); err != nil {
		t.Fatalf("move room through the room table: %v", err)
	}
	if got := changes(tr.room.ID); len(got) != 1 || got[0].newParent != b2.ID {
		t.Errorf("location_change of the room: %+v", got)
	}

	// location_change is a tenant table: organization B sees nothing.
	var visible int
	scopeB := database.OrgWideScope(f.OrgB, f.User)
	if err = database.WithTenant(bg, f.App, &scopeB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM location_change`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Errorf("organization B reads %d location_change rows, %v", visible, err)
	}
}

// TestLocationDeleteRefusedWithDependencies covers WP-055 (LOC-11): deleting
// a location that CIs, assets, stock, movements, rack mounts or role
// assignments reference is refused with 409 and the dependency kinds,
// through the location, site/building/room and rack APIs alike; nothing is
// silently cleared.
func TestLocationDeleteRefusedWithDependencies(t *testing.T) {
	f := scopetest.Seed(t, "5a")
	bg := context.Background()
	repo := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	tr := buildTree(t, repo, ctx, f.OrgA, f.Client1, "del")

	mux := chi.NewRouter()
	locations.NewHandler(repo).RegisterRoutes(mux)
	tenantapi.NewHandler(tenantapi.NewPGRepository(f.App)).RegisterRoutes(mux)
	rack.NewHandler(rack.NewPGRepository(f.App)).RegisterRoutes(mux)
	del := func(path string) (int, []string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA})))
		var body struct {
			Dependencies []string `json:"dependencies"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Dependencies
	}
	expect := func(path string, status int, kinds ...string) {
		t.Helper()
		code, deps := del(path)
		if code != status || !slices.Equal(deps, kinds) {
			t.Errorf("DELETE %s: %d %v, want %d %v", path, code, deps, status, kinds)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	ciID := f.CI(t, f.OrgA, f.Client1, "del-ci")
	exec(`UPDATE ci SET location_id = $2 WHERE id = $1`, ciID, tr.bin.ID)
	assetID := f.Asset(t, f.OrgA, f.Client1, "del-asset")
	exec(`UPDATE asset SET location_id = $2 WHERE id = $1`, assetID, tr.shelf.ID)
	exec(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u) VALUES ($1, $2, $3, 1)`, f.OrgA, tr.rack.ID, ciID)

	// Location API: a node with children or references.
	expect("/api/v1/locations/"+tr.bin.ID, http.StatusConflict, "ci")
	expect("/api/v1/locations/"+tr.shelf.ID, http.StatusConflict, "asset", "location")
	// Site, building, room and rack rows cascade to their subtree: every
	// reference below counts.
	expect("/api/v1/racks/"+tr.rack.ID, http.StatusConflict, "rack_mount")
	expect("/api/v1/rooms/"+tr.room.ID, http.StatusConflict, "rack_mount")
	expect("/api/v1/sites/"+tr.site.ID, http.StatusConflict, "asset", "ci", "rack_mount")

	// Stock, movements and role assignments hold their location as well.
	stock := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindZone, ParentID: tr.warehouse.ID, Name: "del-stock"})
	exec(`INSERT INTO quantity_item (organization_id, client_id, name, location_id) VALUES ($1, $2, 'del', $3)`, f.OrgA, f.Client1, stock.ID)
	expect("/api/v1/locations/"+stock.ID, http.StatusConflict, "quantity_item")
	moved := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindZone, ParentID: tr.warehouse.ID, Name: "del-moved"})
	exec(`INSERT INTO asset_movement (organization_id, item_kind, asset_id, movement_type, to_location_id) VALUES ($1, 'asset', $2, 'transfer', $3)`,
		f.OrgA, assetID, moved.ID)
	expect("/api/v1/locations/"+moved.ID, http.StatusConflict, "asset_movement")
	scoped := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "del-scoped"})
	roleID := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'del-role', '[]')`, roleID, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id) VALUES ($1, $2, $3, $4, $5)`,
		f.OrgA, f.AppUser(t, f.OrgA, "del-user"), roleID, f.Client1, scoped.ID)
	expect("/api/v1/sites/"+scoped.ID, http.StatusConflict, "role_assignment")

	// The references are untouched.
	var location string
	if err := f.Admin.QueryRow(bg, `SELECT location_id::text FROM ci WHERE id = $1`, ciID).Scan(&location); err != nil || location != tr.bin.ID {
		t.Errorf("CI location after the refused deletes: %q, %v", location, err)
	}
	var mounts int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM rack_mount WHERE rack_id = $1`, tr.rack.ID).Scan(&mounts); err != nil || mounts != 1 {
		t.Errorf("rack mounts after the refused deletes: %d, %v", mounts, err)
	}

	// The foreign keys are the backstop for references the check misses.
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	_, err = tx.Exec(bg, `DELETE FROM location WHERE id = $1`, tr.bin.ID)
	var de *locations.DependencyError
	if !errors.As(locations.DeleteError(err), &de) || !slices.Equal(de.Kinds, []string{"ci"}) {
		t.Errorf("direct delete of a referenced location: %v, want a ci dependency", err)
	}
	_ = tx.Rollback(bg)

	// Without references the subtree goes: the room once its rack is empty.
	exec(`DELETE FROM rack_mount WHERE rack_id = $1`, tr.rack.ID)
	expect("/api/v1/rooms/"+tr.room.ID, http.StatusNoContent)
}
