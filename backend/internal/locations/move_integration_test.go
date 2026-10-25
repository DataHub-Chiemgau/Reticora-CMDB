package locations_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gopkg.in/yaml.v3"
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

	// The change row carries the name of the moved location, and it outlives
	// the location (WP-411, F-227): location_id is a reference without a
	// foreign key.
	var name string
	if err = f.Admin.QueryRow(bg, `SELECT location_name FROM location_change WHERE location_id = $1`, tr.zone.ID).Scan(&name); err != nil || name != tr.zone.Name {
		t.Errorf("location_name of the zone's change row: %q, %v; want %q", name, err, tr.zone.Name)
	}
	gone := mustCreate(t, repo, ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindZone, ParentID: tr.warehouse.ID, Name: "mv-gone"})
	if _, err = repo.Move(ctx, f.OrgA, gone.ID, wh2.ID); err != nil {
		t.Fatalf("move the zone to delete: %v", err)
	}
	if err = repo.Delete(ctx, f.OrgA, gone.ID); err != nil {
		t.Fatalf("delete the moved empty zone: %v", err)
	}
	if got := changes(gone.ID); len(got) != 1 || got[0].oldParent != tr.warehouse.ID || got[0].newParent != wh2.ID || got[0].by != user {
		t.Errorf("location_change of the deleted zone: %+v, want the move by %s", got, user)
	}
	if err = f.Admin.QueryRow(bg, `SELECT location_name FROM location_change WHERE location_id = $1`, gone.ID).Scan(&name); err != nil || name != "mv-gone" {
		t.Errorf("location_name of the deleted zone's change row: %q, %v; want mv-gone", name, err)
	}
}

// TestLocationDeleteRefusedWithDependencies covers WP-055 (LOC-11): deleting
// a location that child locations, CIs, assets, stock, movements, rack
// mounts or role assignments reference is refused with 409 and the
// dependency kinds, through the location, site/building/room and rack APIs
// alike; nothing is silently cleared and nothing cascades (WP-411).
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
	// Site, building and room rows keep their children like the location API
	// (WP-411, F-023): the child locations are the dependency; the references
	// below refuse the delete of the child.
	expect("/api/v1/racks/"+tr.rack.ID, http.StatusConflict, "rack_mount")
	expect("/api/v1/rooms/"+tr.room.ID, http.StatusConflict, "location")
	expect("/api/v1/buildings/"+tr.building.ID, http.StatusConflict, "location")
	expect("/api/v1/sites/"+tr.site.ID, http.StatusConflict, "location")

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
	// A site assignment names the site only (WP-391): its client is the site's.
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_site_id) VALUES ($1, $2, $3, $4)`,
		f.OrgA, f.AppUser(t, f.OrgA, "del-user"), roleID, scoped.ID)
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

	// Nothing cascades (WP-411): the room keeps its empty rack and goes once
	// the rack is deleted; the building stays.
	exec(`DELETE FROM rack_mount WHERE rack_id = $1`, tr.rack.ID)
	expect("/api/v1/rooms/"+tr.room.ID, http.StatusConflict, "location")
	var left int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM location WHERE id = ANY($1::uuid[])`, []string{tr.room.ID, tr.rack.ID}).Scan(&left); err != nil || left != 2 {
		t.Errorf("room and rack after the refused room delete: %d, %v; want 2", left, err)
	}
	expect("/api/v1/racks/"+tr.rack.ID, http.StatusNoContent)
	expect("/api/v1/rooms/"+tr.room.ID, http.StatusNoContent)
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM location WHERE id = ANY($1::uuid[])`, []string{tr.building.ID, tr.room.ID, tr.rack.ID}).Scan(&left); err != nil || left != 1 {
		t.Errorf("building, room and rack after deleting rack and room: %d, %v; want the building only", left, err)
	}
}

// auditT3Locations serves the location, site/building/room and rack routes
// to one principal of organization A (Phase-1 acceptance audit, group
// T3-data-ingest, WP-055).
type auditT3Locations struct {
	t    *testing.T
	f    *scopetest.Fixture
	repo *locations.PGRepository
	user string
	ctx  context.Context
	mux  *chi.Mux
}

func newAuditT3Locations(t *testing.T, tag string) *auditT3Locations {
	t.Helper()
	f := scopetest.Seed(t, tag)
	user := f.AppUser(t, f.OrgA, "audit-t3-loc")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := locations.NewPGRepository(f.App)
	mux := chi.NewRouter()
	locations.NewHandler(repo).RegisterRoutes(mux)
	tenantapi.NewHandler(tenantapi.NewPGRepository(f.App)).RegisterRoutes(mux)
	rack.NewHandler(rack.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &auditT3Locations{t: t, f: f, repo: repo, user: user, ctx: ctx, mux: mux}
}

// del sends DELETE path and returns the status and the dependency kinds of
// a 409.
func (l *auditT3Locations) del(path string) (int, []string) {
	l.t.Helper()
	w := httptest.NewRecorder()
	l.mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil).WithContext(l.ctx))
	var body struct {
		Dependencies []string `json:"dependencies"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Dependencies
}

func (l *auditT3Locations) exec(sql string, args ...any) {
	l.t.Helper()
	if _, err := l.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		l.t.Fatalf("%s: %v", sql, err)
	}
}

func (l *auditT3Locations) count(sql string, args ...any) int {
	l.t.Helper()
	var n int
	if err := l.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		l.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (l *auditT3Locations) nodes(ids ...string) int {
	return l.count(`SELECT count(*) FROM location WHERE id::text = ANY($1)`, ids)
}

// paths returns the path of every node of ids that exists.
func (l *auditT3Locations) paths(ids ...string) map[string]string {
	l.t.Helper()
	rows, err := l.f.Admin.Query(context.Background(), `SELECT id::text, path::text FROM location WHERE id::text = ANY($1)`, ids)
	if err != nil {
		l.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		return p, row.Scan(&p[0], &p[1])
	})
	if err != nil {
		l.t.Fatal(err)
	}
	m := map[string]string{}
	for _, p := range out {
		m[p[0]] = p[1]
	}
	return m
}

func (l *auditT3Locations) audits() int {
	return l.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1`, l.f.OrgA)
}

// deleted returns the location.deleted audit entries of id by the
// principal.
func (l *auditT3Locations) deleted(id string) int {
	return l.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'location.deleted'
		AND resource_type = 'location' AND resource_id = $2 AND actor_id = $3`, l.f.OrgA, id, l.user)
}

// TestAuditT3LocationDeleteRefusesChildren covers WP-411 (LOC-11, F-023):
// DELETE /sites, /buildings or /rooms of a node with child locations is
// refused with 409 'location' like DELETE /locations; the subtree, its
// paths and the audit log stay unchanged. An empty building or room is
// deleted through its route (204) and audited.
func TestAuditT3LocationDeleteRefusesChildren(t *testing.T) {
	l := newAuditT3Locations(t, "f9")
	cases := []struct {
		name  string
		path  func(tr tree) string
		nodes func(tr tree) []string
	}{
		{"locations route (control)", func(tr tree) string { return "/api/v1/locations/" + tr.building.ID },
			func(tr tree) []string { return []string{tr.building.ID, tr.room.ID, tr.rack.ID} }},
		{"sites route", func(tr tree) string { return "/api/v1/sites/" + tr.site.ID },
			func(tr tree) []string {
				return []string{tr.site.ID, tr.building.ID, tr.room.ID, tr.rack.ID, tr.warehouse.ID, tr.zone.ID, tr.shelf.ID, tr.bin.ID}
			}},
		{"buildings route", func(tr tree) string { return "/api/v1/buildings/" + tr.building.ID },
			func(tr tree) []string { return []string{tr.building.ID, tr.room.ID, tr.rack.ID} }},
		{"rooms route", func(tr tree) string { return "/api/v1/rooms/" + tr.room.ID },
			func(tr tree) []string { return []string{tr.room.ID, tr.rack.ID} }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := buildTree(t, l.repo, l.ctx, l.f.OrgA, l.f.Client1, "audit-t3-del-"+string(rune('a'+i)))
			ids := tc.nodes(tr)
			paths, audits := l.paths(ids...), l.audits()
			if code, deps := l.del(tc.path(tr)); code != http.StatusConflict || !slices.Equal(deps, []string{locations.DependencyLocation}) {
				t.Errorf("DELETE %s of a node with child locations = %d %v, want 409 [location] (LOC-11)", tc.path(tr), code, deps)
			}
			if got := l.paths(ids...); !maps.Equal(got, paths) {
				t.Errorf("subtree after the delete: %v, want %v (no silent cascade)", got, paths)
			}
			if n := l.audits(); n != audits {
				t.Errorf("audit entries after the refused delete: %d, want %d", n, audits)
			}
		})
	}
	// An empty building and an empty room are deleted through their route
	// and audited.
	tr := buildTree(t, l.repo, l.ctx, l.f.OrgA, l.f.Client1, "audit-t3-del-empty")
	building := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.site.ID, Name: "audit-t3-empty-b"})
	room := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindRoom, ParentID: tr.building.ID, Name: "audit-t3-empty-r"})
	for _, c := range []struct {
		route, table string
		node         *locations.Location
	}{
		{"/api/v1/buildings/", "building", building},
		{"/api/v1/rooms/", "room", room},
	} {
		if code, deps := l.del(c.route + c.node.ID); code != http.StatusNoContent {
			t.Errorf("DELETE %s{empty %s} = %d %v, want 204", c.route, c.table, code, deps)
		}
		if n := l.nodes(c.node.ID) + l.count(`SELECT count(*) FROM `+c.table+` WHERE id = $1`, c.node.ID); n != 0 {
			t.Errorf("empty %s after the delete: %d rows left", c.table, n)
		}
		if n := l.deleted(c.node.ID); n != 1 {
			t.Errorf("location.deleted audit entries of the empty %s: %d, want 1", c.table, n)
		}
	}
	if n := l.nodes(tr.site.ID, tr.building.ID); n != 2 {
		t.Errorf("site and building after deleting the empty nodes: %d, want 2", n)
	}
}

// TestAuditT3SiteDeleteKeepsNetworkObjects covers WP-411 (LOC-11, F-024)
// under the transitional rule of E-71 (option 1): a site that VLANs,
// subnets, collectors, discovery scopes, live enrollment tokens or merge
// tombstones are bound to is not deleted, through /sites and /locations
// alike: 409 with the kinds, the objects keep their site. Expired tokens and
// tombstones past tombstone_until do not count and are removed with the
// site (204, audited); a refused delete keeps them. A site without children
// and network objects is deleted although its location row references
// itself.
func TestAuditT3SiteDeleteKeepsNetworkObjects(t *testing.T) {
	l := newAuditT3Locations(t, "fa")
	site := func(name string) string {
		t.Helper()
		return mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: l.f.Client1, Name: name}).ID
	}
	vlan := func(siteID string) {
		l.exec(`INSERT INTO vlan (organization_id, client_id, site_id, vlan_id, name) VALUES ($1, $2, $3, 100, 'audit-t3-vlan')`, l.f.OrgA, l.f.Client1, siteID)
	}
	cidr := 0
	subnet := func(siteID string) {
		cidr++
		l.exec(`INSERT INTO subnet (organization_id, client_id, site_id, cidr, name) VALUES ($1, $2, $3, $4, 'audit-t3-net')`,
			l.f.OrgA, l.f.Client1, siteID, fmt.Sprintf("10.93.%d.0/24", cidr))
	}
	collector := func(siteID any) string {
		t.Helper()
		var id string
		if err := l.f.Admin.QueryRow(context.Background(), `INSERT INTO collector (organization_id, client_id, site_id, name) VALUES ($1, $2, $3, 'audit-t3-collector') RETURNING id::text`,
			l.f.OrgA, l.f.Client1, siteID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	token := func(siteID, ttl string) {
		l.exec(`INSERT INTO agent_enrollment_token (organization_id, client_id, site_id, token_hash, expires_at) VALUES ($1, $2, $3, $4, now() + $5::interval)`,
			l.f.OrgA, l.f.Client1, siteID, "audit-t3-"+l.f.ID(), ttl)
	}
	merge := func(siteID, ttl string) {
		l.exec(`INSERT INTO ci_merge (organization_id, client_id, site_id, survivor_id, victim_id, snapshot, tombstone_until)
			VALUES ($1, $2, $3, gen_random_uuid(), gen_random_uuid(), '{}', now() + $4::interval)`, l.f.OrgA, l.f.Client1, siteID, ttl)
	}
	lapsed := func(siteID string) int {
		return l.count(`SELECT (SELECT count(*) FROM agent_enrollment_token WHERE site_id = $1) + (SELECT count(*) FROM ci_merge WHERE site_id = $1)`, siteID)
	}

	// The finding: a site with a VLAN and a subnet.
	netSite := site("audit-t3-net-site")
	vlan(netSite)
	subnet(netSite)
	code, deps := l.del("/api/v1/sites/" + netSite)
	vlans := l.count(`SELECT count(*) FROM vlan WHERE organization_id = $1 AND name = 'audit-t3-vlan'`, l.f.OrgA)
	subnets := l.count(`SELECT count(*) FROM subnet WHERE organization_id = $1 AND name = 'audit-t3-net' AND site_id = $2`, l.f.OrgA, netSite)
	if code != http.StatusConflict || !slices.Equal(deps, []string{"subnet", "vlan"}) || vlans != 1 || subnets != 1 {
		t.Errorf("DELETE /api/v1/sites/{site with VLAN and subnet} = %d %v; VLAN rows left=%d, subnets still at the site=%d; want 409 [subnet vlan] and both kept (silent loss of IPAM data)",
			code, deps, vlans, subnets)
	}

	// Each kind on its own, through both delete paths.
	for _, c := range []struct {
		kind string
		bind func(siteID string)
	}{
		{"vlan", vlan},
		{"subnet", subnet},
		{"collector", func(s string) { collector(s) }},
		{"discovery_scope", func(s string) {
			l.exec(`INSERT INTO discovery_scope (organization_id, collector_id, site_id, name, kind, target) VALUES ($1, $2, $3, 'audit-t3-scope', 'cidr', '10.94.0.0/24')`,
				l.f.OrgA, collector(nil), s)
		}},
		{"agent_enrollment_token", func(s string) { token(s, "1 day") }},
		{"ci_merge", func(s string) { merge(s, "1 day") }},
	} {
		id := site("audit-t3-" + c.kind)
		c.bind(id)
		for _, route := range []string{"/api/v1/sites/", "/api/v1/locations/"} {
			if code, deps := l.del(route + id); code != http.StatusConflict || !slices.Equal(deps, []string{c.kind}) {
				t.Errorf("DELETE %s{site with a %s} = %d %v, want 409 [%s]", route, c.kind, code, deps, c.kind)
			}
		}
		if n := l.count(`SELECT count(*) FROM `+c.kind+` WHERE site_id = $1`, id); n != 1 || l.nodes(id) != 1 {
			t.Errorf("%s rows at the site after the refused deletes: %d, site rows %d; want 1 and 1", c.kind, n, l.nodes(id))
		}
	}

	// A site without children and network objects is deleted: its own
	// location row (site_id = id) does not block it.
	empty := site("audit-t3-empty-site")
	if n := l.count(`SELECT count(*) FROM location WHERE id = $1 AND site_id = id`, empty); n != 1 {
		t.Fatalf("precondition: the site's location row references itself: %d", n)
	}
	if code, deps := l.del("/api/v1/sites/" + empty); code != http.StatusNoContent {
		t.Errorf("DELETE /api/v1/sites/{empty site} = %d %v, want 204", code, deps)
	}
	if n := l.nodes(empty) + l.count(`SELECT count(*) FROM site WHERE id = $1`, empty); n != 0 || l.deleted(empty) != 1 {
		t.Errorf("empty site after the delete: %d rows left, %d audit entries; want 0 and 1", n, l.deleted(empty))
	}

	// Expired enrollment tokens and tombstones past tombstone_until go with
	// the site, on both delete paths, and the audit entry counts them.
	for _, route := range []string{"/api/v1/locations/", "/api/v1/sites/"} {
		id := site("audit-t3-lapsed" + strings.ReplaceAll(route, "/", "-"))
		token(id, "-1 hour")
		merge(id, "-1 day")
		if code, deps := l.del(route + id); code != http.StatusNoContent {
			t.Errorf("DELETE %s{site with an expired token and tombstone} = %d %v, want 204", route, code, deps)
		}
		if n := lapsed(id); n != 0 || l.nodes(id) != 0 {
			t.Errorf("%s: expired token and tombstone left: %d, site rows %d; want both removed", route, n, l.nodes(id))
		}
		if n := l.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'location.deleted' AND resource_id = $2
			AND changes->'removed' = '{"agent_enrollment_token": 1, "ci_merge": 1}'::jsonb`, l.f.OrgA, id); n != 1 {
			t.Errorf("%s: location.deleted entries naming the removed token and tombstone: %d, want 1", route, n)
		}
	}
	// Only an expired token: the case of the acceptance criterion.
	tokenOnly := site("audit-t3-expired-token")
	token(tokenOnly, "-1 minute")
	if code, deps := l.del("/api/v1/locations/" + tokenOnly); code != http.StatusNoContent || lapsed(tokenOnly) != 0 {
		t.Errorf("DELETE /api/v1/locations/{site with an expired token} = %d %v, tokens left %d; want 204 and none", code, deps, lapsed(tokenOnly))
	}
	// A refused delete keeps the lapsed rows: removal and delete are one
	// transaction.
	kept := site("audit-t3-lapsed-kept")
	token(kept, "-1 hour")
	merge(kept, "-1 day")
	vlan(kept)
	if code, deps := l.del("/api/v1/locations/" + kept); code != http.StatusConflict || !slices.Equal(deps, []string{"vlan"}) || lapsed(kept) != 2 {
		t.Errorf("DELETE /api/v1/locations/{site with a VLAN and lapsed rows} = %d %v, lapsed rows %d; want 409 [vlan] and 2", code, deps, lapsed(kept))
	}
}

// TestLocationDeleteKeepsChildrenTheCallerCannotSee covers WP-411 (LOC-11):
// the foreign keys of the tree are RESTRICT, so a child the deleting
// principal cannot see refuses the delete as well. A building and its rooms
// (made invisible to a principal of client 1 by the fixture) keep the site:
// DELETE /sites and /locations answer 409 [location], the nodes stay.
func TestLocationDeleteKeepsChildrenTheCallerCannotSee(t *testing.T) {
	l := newAuditT3Locations(t, "5b")
	bg := context.Background()
	site := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: l.f.Client1, Name: "hidden-site"})
	building := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site.ID, Name: "hidden-b"})
	room1 := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindRoom, ParentID: building.ID, Name: "hidden-r1"})
	room2 := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindRoom, ParentID: building.ID, Name: "hidden-r2"})
	hidden := []string{building.ID, room1.ID, room2.ID}

	// Fixture: building and rooms carry client 2, past the derivation
	// triggers (replica role on one connection).
	conn, err := l.f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET session_replication_role = replica`,
		`UPDATE location SET client_id = $1 WHERE id::text = ANY($2)`,
		`UPDATE room SET client_id = $1 WHERE id::text = ANY($2)`,
		`SET session_replication_role = origin`,
	} {
		var args []any
		if strings.HasPrefix(sql, "UPDATE") {
			args = []any{l.f.Client2, hidden}
		}
		if _, err = conn.Exec(bg, sql, args...); err != nil {
			conn.Release()
			t.Fatalf("%s: %v", sql, err)
		}
	}
	conn.Release()

	user := l.f.AppUser(t, l.f.OrgA, "hidden-client1")
	scope := database.OrgWideScope(l.f.OrgA, user)
	scope.Clients = database.ScopeIDs(l.f.Client1)
	l.ctx = tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: l.f.OrgA, UserID: user})
	if _, err = l.repo.Get(l.ctx, l.f.OrgA, site.ID); err != nil {
		t.Fatalf("precondition: the client 1 principal sees the site: %v", err)
	}
	if _, err = l.repo.Get(l.ctx, l.f.OrgA, building.ID); !errors.Is(err, locations.ErrNotFound) {
		t.Fatalf("precondition: the building is invisible to the client 1 principal: %v", err)
	}

	paths, audits := l.paths(append(hidden, site.ID)...), l.audits()
	for _, route := range []string{"/api/v1/sites/", "/api/v1/locations/"} {
		if code, deps := l.del(route + site.ID); code != http.StatusConflict || !slices.Equal(deps, []string{locations.DependencyLocation}) {
			t.Errorf("DELETE %s{site with an invisible building} = %d %v, want 409 [location]", route, code, deps)
		}
	}
	if got := l.paths(append(hidden, site.ID)...); !maps.Equal(got, paths) {
		t.Errorf("site, building and rooms after the refused deletes: %v, want %v", got, paths)
	}
	if n := l.count(`SELECT (SELECT count(*) FROM building WHERE id::text = ANY($1)) + (SELECT count(*) FROM room WHERE id::text = ANY($1))`, hidden); n != 3 {
		t.Errorf("building and room rows after the refused deletes: %d, want 3", n)
	}
	if n := l.audits(); n != audits {
		t.Errorf("audit entries after the refused deletes: %d, want %d", n, audits)
	}
}

// TestLocationDeleteReferencesMapToSpecKinds covers WP-411 (LOC-11): every
// foreign key that refuses the delete of a location, site, building, room or
// rack reports a dependency kind of DependencyProblem in api/openapi.yaml
// (DeleteError), the nodes of the tree as 'location'. The tree and the
// objects bound to a site are RESTRICT (000157); only the specialist row of
// a node and the listed SET NULL references follow a delete.
func TestLocationDeleteReferencesMapToSpecKinds(t *testing.T) {
	f := scopetest.Seed(t, "5c")
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		AllOf      []schema          `yaml:"allOf"`
		Properties map[string]schema `yaml:"properties"`
		Items      *schema           `yaml:"items"`
		Enum       []string          `yaml:"enum"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]schema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var spec []string
	for _, part := range doc.Components.Schemas["DependencyProblem"].AllOf {
		if deps, ok := part.Properties["dependencies"]; ok && deps.Items != nil {
			spec = deps.Items.Enum
		}
	}
	kinds := []string{
		locations.DependencyLocation, locations.DependencyCI, locations.DependencyAsset, locations.DependencyQuantityItem,
		locations.DependencyAssetMovement, locations.DependencyRackMount, locations.DependencyRoleAssignment,
		"vlan", "subnet", "collector", "discovery_scope", "agent_enrollment_token", "ci_merge",
	}
	if !slices.Equal(slices.Sorted(slices.Values(spec)), slices.Sorted(slices.Values(kinds))) {
		t.Errorf("DependencyProblem.dependencies enum %v, want the kinds %v", spec, kinds)
	}

	rows, err := f.Admin.Query(context.Background(), `
		SELECT c.conname, c.conrelid::regclass::text, c.confdeltype::text FROM pg_constraint c
		 WHERE c.contype = 'f' AND c.confrelid IN ('location'::regclass, 'site'::regclass, 'building'::regclass, 'room'::regclass, 'rack'::regclass)
		 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([3]string, error) {
		var k [3]string
		return k, row.Scan(&k[0], &k[1], &k[2])
	})
	if err != nil {
		t.Fatal(err)
	}
	// The delete rules of 000157.
	restrict := map[string]bool{
		"location_site_id_fkey": true, "location_parent_id_fkey": true, "building_site_id_fkey": true,
		"room_building_id_fkey": true, "room_site_id_fkey": true, "rack_room_id_fkey": true, "rack_site_id_fkey": true,
		"vlan_site_id_fkey": true, "subnet_site_id_fkey": true, "collector_site_id_fkey": true,
		"discovery_scope_site_id_fkey": true, "agent_enrollment_token_site_id_fkey": true, "ci_merge_site_id_fkey": true,
	}
	// Foreign keys that follow a delete: the specialist row goes with its
	// location row; a desk loses its room and a review item its site.
	follow := map[string]string{
		"site_location_fkey": "c", "building_location_fkey": "c", "room_location_fkey": "c", "rack_location_fkey": "c",
		"desk_room_id_fkey": "n", "review_item_site_id_fkey": "n",
	}
	tree := []string{"location", "building", "room", "rack"}
	seen := 0
	for _, k := range keys {
		name, table, action := k[0], k[1], k[2]
		if restrict[name] {
			seen++
			if action != "r" {
				t.Errorf("%s: ON DELETE %s, want RESTRICT", name, action)
			}
		}
		if action != "r" && action != "a" {
			if follow[name] != action {
				t.Errorf("%s on %s: ON DELETE %s follows the delete of a location silently", name, table, action)
			}
			continue
		}
		var de *locations.DependencyError
		if !errors.As(locations.DeleteError(&pgconn.PgError{Code: "23503", TableName: table, ConstraintName: name}), &de) || len(de.Kinds) != 1 {
			t.Errorf("%s on %s: DeleteError reports no dependency kind", name, table)
			continue
		}
		if !slices.Contains(spec, de.Kinds[0]) || (slices.Contains(tree, table) && de.Kinds[0] != locations.DependencyLocation) {
			t.Errorf("%s on %s: DeleteError reports %q, want a kind of the spec ('location' for the tree)", name, table, de.Kinds[0])
		}
	}
	if seen != len(restrict) {
		t.Errorf("found %d of the %d foreign keys of 000157", seen, len(restrict))
	}
	if err = locations.DeleteError(&pgconn.PgError{Code: "23503", TableName: "unknown_table"}); errors.As(err, new(*locations.DependencyError)) {
		t.Errorf("a violation of an unknown table is reported as %v", err)
	}
}
