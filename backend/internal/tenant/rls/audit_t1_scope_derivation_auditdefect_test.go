//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-025 (TEN-02, TEN-05, IMP-07; AC2/AC3) P2 "Derived edge scope goes stale
// when a client- or site-scoped principal moves a CI": propagate_ci_scope
// (000061) runs as SECURITY INVOKER under the mover's RLS and skips the edge
// whose far endpoint the mover cannot see, so the edge keeps the old
// source_client_id/source_site_id and observers see an edge naming a CI
// they may not see.
//
// WP-027 (TEN-02, TEN-05; CH25) P2 "Assets, rooms and racks have a location
// but no site_id; site-scoped principals see them in all sites": TEN-02
// requires a denormalised site_id on every table with a location reference.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v \
//     -run 'TestAuditT1EdgeScopeFollowsScopedMove|TestAuditT1SiteScopeOfLocatedTables' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestAuditT1EdgeScopeFollowsScopedMove(t *testing.T) {
	w := newT1World(t, "dc")
	f := w.f
	bg := context.Background()
	client3 := w.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'Client 3', 'scopetest-dc-c3') RETURNING id::text`, f.OrgA)
	inTenant := func(scope *database.TenantScope, query string, args ...any) (int, error) {
		var n int
		err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&n)
		})
		return n, err
	}
	scoped := func(clients, sites []string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, w.userA)
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return &s
	}

	t.Run("client move by a client-1/client-2 principal", func(t *testing.T) {
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-x")
		y := f.CI(t, f.OrgA, client3, "t1-edge-y")
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		mover := w.as("clients-1-2", only(f.Client1, f.Client2), all())
		if code, body := w.do(mover, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
			t.Fatalf("move X to client 2 as clients-1-2: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != f.Client2 {
			t.Errorf("TEN-02/AC2: edge source_client_id after the move = %s, want client 2 (%s)", got, f.Client2)
		}
		observer := scoped([]string{f.Client1, client3}, nil)
		if n, err := inTenant(observer, `SELECT count(*) FROM ci WHERE id = $1`, x); err != nil || n != 0 {
			t.Fatalf("setup: observer clients 1+3 sees CI X: %d %v", n, err)
		}
		if n, err := inTenant(observer, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1`, x); err != nil || n != 0 {
			t.Errorf("TEN-05/AC3: observer clients 1+3 sees %d edges naming the invisible CI X (err %v), want 0", n, err)
		}
	})

	t.Run("site move by a site-1/site-2 principal", func(t *testing.T) {
		room1, room2, room3 := f.Room(t, f.Client1, "t1-edge-s1"), f.Room(t, f.Client1, "t1-edge-s2"), f.Room(t, f.Client1, "t1-edge-s3")
		s1, s2, s3 := w.siteOfRoom(room1), w.siteOfRoom(room2), w.siteOfRoom(room3)
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-sx")
		y := f.CI(t, f.OrgA, f.Client1, "t1-edge-sy")
		for ci, site := range map[string]string{x: s1, y: s3} {
			if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+ci, map[string]any{"location_id": site}); code != http.StatusOK {
				t.Fatalf("place CI: %d %s", code, trim(body))
			}
		}
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		mover := w.as("sites-1-2", all(), only(s1, s2))
		if code, body := w.do(mover, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"location_id": s2}); code != http.StatusOK {
			t.Fatalf("move X to site 2 as sites-1-2: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_site_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != s2 {
			t.Errorf("TEN-02/AC2: edge source_site_id after the move = %s, want site 2 (%s)", got, s2)
		}
		observer := scoped(nil, []string{s1, s3})
		if n, err := inTenant(observer, `SELECT count(*) FROM ci WHERE id = $1`, x); err != nil || n != 0 {
			t.Fatalf("setup: observer sites 1+3 sees CI X: %d %v", n, err)
		}
		if n, err := inTenant(observer, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1`, x); err != nil || n != 0 {
			t.Errorf("TEN-05/AC3: observer sites 1+3 sees %d edges naming the invisible CI X (err %v), want 0", n, err)
		}
	})

	t.Run("positive control: org-wide move re-derives the edge", func(t *testing.T) {
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-cx")
		y := f.CI(t, f.OrgA, client3, "t1-edge-cy")
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
			t.Fatalf("org-wide move: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != f.Client2 {
			t.Errorf("edge source_client_id after the org-wide move = %s, want client 2", got)
		}
	})
}

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
