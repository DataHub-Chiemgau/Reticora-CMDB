//go:build auditdefect

package locations_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-055 (LOC-11: "Locations mit zugeordneten CIs, Assets, Mounts oder
// Kindern können nicht gelöscht werden (409 conflict)") P2 "DELETE /sites,
// /buildings, /rooms with child locations returns 204 and cascades the
// subtree": tenantapi/pg_repository.go calls locations.CheckDeletable(...,
// subtree=true), which never reports the 'location' kind, and
// location_parent_id_fkey cascades. /api/v1/locations/{id} refuses the same
// delete with 409 (control). Note: move_integration_test.go currently asserts
// the cascade of an empty room as intended; a fix has to update it.
//
// WP-055 P2 (data loss) "Site delete silently cascades VLANs/enrollment
// tokens and nulls subnets": dependencyQuery checks only location, ci, asset,
// quantity_item, asset_movement, rack_mount and role_assignment;
// vlan_site_id_fkey cascades and subnet.site_id is SET NULL. Requirement (per
// the confirmed finding): a site that still carries VLANs or subnets is
// refused with 409 and those rows stay unchanged (or the cascade is a
// recorded decision, which does not exist).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3LocationDeleteRefusesChildren|TestAuditT3SiteDeleteKeepsNetworkObjects' ./internal/locations/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
)

type auditT3Locations struct {
	t    *testing.T
	f    *scopetest.Fixture
	repo *locations.PGRepository
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
	return &auditT3Locations{t: t, f: f, repo: repo, ctx: ctx, mux: mux}
}

func (l *auditT3Locations) del(path string) *httptest.ResponseRecorder {
	l.t.Helper()
	w := httptest.NewRecorder()
	l.mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil).WithContext(l.ctx))
	return w
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
			w := l.del(tc.path(tr))
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "location") {
				t.Errorf("DELETE %s of a node with child locations = %d %s, want 409 with dependency 'location' (LOC-11)",
					tc.path(tr), w.Code, strings.TrimSpace(w.Body.String()))
			}
			if n := l.nodes(ids...); n != len(ids) {
				t.Errorf("subtree after the delete: %d of %d nodes left, want all (no silent cascade)", n, len(ids))
			}
		})
	}
	// Control: an empty building is deleted through the specialist route.
	tr := buildTree(t, l.repo, l.ctx, l.f.OrgA, l.f.Client1, "audit-t3-del-empty")
	empty := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindBuilding, ParentID: tr.site.ID, Name: "audit-t3-empty-b"})
	if w := l.del("/api/v1/buildings/" + empty.ID); w.Code != http.StatusNoContent {
		t.Errorf("control: DELETE /api/v1/buildings/{empty building} = %d, want 204", w.Code)
	}
}

func TestAuditT3SiteDeleteKeepsNetworkObjects(t *testing.T) {
	l := newAuditT3Locations(t, "fa")
	bg := context.Background()
	site := mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: l.f.Client1, Name: "audit-t3-net-site"})
	if _, err := l.f.Admin.Exec(bg, `INSERT INTO vlan (organization_id, client_id, site_id, vlan_id, name) VALUES ($1, $2, $3, 100, 'audit-t3-vlan')`,
		l.f.OrgA, l.f.Client1, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.f.Admin.Exec(bg, `INSERT INTO subnet (organization_id, client_id, site_id, cidr, name) VALUES ($1, $2, $3, '10.93.0.0/24', 'audit-t3-net')`,
		l.f.OrgA, l.f.Client1, site.ID); err != nil {
		t.Fatal(err)
	}
	w := l.del("/api/v1/sites/" + site.ID)
	vlans := l.count(`SELECT count(*) FROM vlan WHERE organization_id = $1 AND name = 'audit-t3-vlan'`, l.f.OrgA)
	subnets := l.count(`SELECT count(*) FROM subnet WHERE organization_id = $1 AND name = 'audit-t3-net' AND site_id = $2`, l.f.OrgA, site.ID)
	if w.Code != http.StatusConflict || vlans != 1 || subnets != 1 {
		t.Errorf("DELETE /api/v1/sites/{site with VLAN and subnet} = %d; VLAN rows left=%d, subnets still at the site=%d; want 409 and both kept (silent loss of IPAM data)",
			w.Code, vlans, subnets)
	}
}
