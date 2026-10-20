//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-027 (TEN-02, TEN-05; CH25) P2 "Assets, rooms and racks have a location
// but no site_id; site-scoped principals see them in all sites": TEN-02
// requires a denormalised site_id on every table with a location reference.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v \
//     -run 'TestAuditT1SiteScopeOfLocatedTables' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestAuditT1SiteScopeOfLocatedTables(t *testing.T) {
	w := newT1World(t, "dd")
	f := w.f
	bg := context.Background()
	room1, room2 := f.Room(t, f.Client1, "t1-sl-s1"), f.Room(t, f.Client1, "t1-sl-s2")
	site1, site2 := w.siteOfRoom(room1), w.siteOfRoom(room2)
	rack2 := w.scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-sl-rack2') RETURNING id::text`, f.OrgA, room2)
	asset1 := w.scalar(`INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, 't1-sl-a1', 't1-sl-asset-site1', $3) RETURNING id::text`,
		f.OrgA, f.Client1, site1)
	asset2 := w.scalar(`INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, 't1-sl-a2', 't1-sl-asset-site2', $3) RETURNING id::text`,
		f.OrgA, f.Client1, site2)
	site1P := w.as("site1", all(), only(site1))

	t.Run("API: assets of another site", func(t *testing.T) {
		code, body := w.do(site1P, http.MethodGet, "/api/v1/assets?limit=200", nil)
		if code != http.StatusOK || !strings.Contains(body, asset1) {
			t.Fatalf("GET /assets as site1: %d, own asset listed %v", code, strings.Contains(body, asset1))
		}
		if strings.Contains(body, asset2) {
			t.Errorf("CH25/TEN-05: GET /assets as site1 lists the asset located in site 2")
		}
		if code, _ := w.do(site1P, http.MethodGet, "/api/v1/assets/"+asset2, nil); code != http.StatusNotFound {
			t.Errorf("GET /assets/{site-2 asset} as site1: %d, want 404", code)
		}
	})
	t.Run("DB: room, rack and asset rows of another site", func(t *testing.T) {
		scope := database.OrgWideScope(f.OrgA, w.userA)
		scope.Sites = database.ScopeIDs(site1)
		for _, c := range []struct{ table, id string }{{"room", room2}, {"rack", rack2}, {"asset", asset2}} {
			var n int
			if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM `+c.table+` WHERE id = $1`, c.id).Scan(&n)
			}); err != nil {
				t.Fatalf("%s: %v", c.table, err)
			}
			if n != 0 {
				t.Errorf("TEN-02/TEN-05: site-1 principal selects the %s of site 2 (%d rows), want 0", c.table, n)
			}
		}
	})
}
