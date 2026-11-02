package rls_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestSiteScopeOfLocatedTables covers WP-393 (TEN-02, TEN-05; CH25, F-053):
// assets, rooms and racks carry the site of their location (asset: WP-392,
// migration 000151; room and rack: migration 000152), and a principal
// restricted to site 1 neither lists, reads, changes nor deletes those of
// site 2, through the API or with a direct SELECT; a site-2 principal sees
// them (positive control).
//   - Probe probe_site_scope_nosite_tables.sql: with app.site_scope = site 1
//     the application role reads 0/0/0 rooms, racks and assets of site 2.
//   - room and rack take client and site from their location row; a written
//     value is overwritten, and they follow when the location tree moves
//     (building to another site, site to another client, room to another
//     building).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSiteScopeOfLocatedTables(t *testing.T) {
	w := newT1World(t, "00")
	f := w.f
	bg := context.Background()
	room1, room2 := f.Room(t, f.Client1, "t1-sl-s1"), f.Room(t, f.Client1, "t1-sl-s2")
	site1, site2 := w.siteOfRoom(room1), w.siteOfRoom(room2)
	rack1 := w.scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-sl-rack1') RETURNING id::text`, f.OrgA, room1)
	rack2 := w.scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-sl-rack2') RETURNING id::text`, f.OrgA, room2)
	asset1 := w.scalar(`INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, 't1-sl-a1', 't1-sl-asset-site1', $3) RETURNING id::text`,
		f.OrgA, f.Client1, site1)
	asset2 := w.scalar(`INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, 't1-sl-a2', 't1-sl-asset-site2', $3) RETURNING id::text`,
		f.OrgA, f.Client1, rack2)
	site1P, site2P := w.as("site1", all(), only(site1)), w.as("site2", all(), only(site2))
	siteScope := func(site string) *database.TenantScope {
		scope := database.OrgWideScope(f.OrgA, w.userA)
		scope.Sites = database.ScopeIDs(site)
		return &scope
	}
	// The helpers take the (sub)test they report to.
	scalar := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return v
	}
	count := func(t *testing.T, scope *database.TenantScope, table string, ids ...string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = ANY($1::uuid[])`, ids).Scan(&n)
		}); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	t.Run("API: assets, rooms and racks of another site", func(t *testing.T) {
		for _, c := range []struct {
			path      string
			own, away string
		}{{"/api/v1/assets", asset1, asset2}, {"/api/v1/rooms", room1, room2}, {"/api/v1/racks", rack1, rack2}} {
			for _, p := range []struct {
				principal *channelPrincipal
				own, away string
			}{{site1P, c.own, c.away}, {site2P, c.away, c.own}} {
				code, body := w.do(p.principal, http.MethodGet, c.path+"?limit=200", nil)
				if code != http.StatusOK || !strings.Contains(body, p.own) {
					t.Fatalf("GET %s as %s: %d, own row listed %v", c.path, p.principal.kind, code, strings.Contains(body, p.own))
				}
				if strings.Contains(body, p.away) {
					t.Errorf("CH25/TEN-05: GET %s as %s lists the row of the other site", c.path, p.principal.kind)
				}
				if code, _ := w.do(p.principal, http.MethodGet, c.path+"/"+p.own, nil); code != http.StatusOK {
					t.Errorf("GET %s/{own} as %s: %d, want 200", c.path, p.principal.kind, code)
				}
				if code, _ := w.do(p.principal, http.MethodGet, c.path+"/"+p.away, nil); code != http.StatusNotFound {
					t.Errorf("GET %s/{other site} as %s: %d, want 404", c.path, p.principal.kind, code)
				}
			}
		}
	})

	t.Run("DB: room, rack and asset rows of another site", func(t *testing.T) {
		for _, c := range []struct{ table, id1, id2 string }{{"room", room1, room2}, {"rack", rack1, rack2}, {"asset", asset1, asset2}} {
			if n := count(t, siteScope(site1), c.table, c.id2); n != 0 {
				t.Errorf("TEN-02/TEN-05: site-1 principal selects the %s of site 2 (%d rows), want 0", c.table, n)
			}
			if n := count(t, siteScope(site1), c.table, c.id1); n != 1 {
				t.Errorf("site-1 principal selects %d rows of its own %s, want 1", n, c.table)
			}
			if n := count(t, siteScope(site2), c.table, c.id1, c.id2); n != 1 {
				t.Errorf("site-2 principal selects %d of the two %s rows, want 1 (its own)", n, c.table)
			}
		}
	})

	// probe_site_scope_nosite_tables.sql: every room, rack and asset at
	// site 2 (per location tree) under app.site_scope = site 1 alone.
	t.Run("probe: rows of site 2 under the site-1 scope", func(t *testing.T) {
		probes := []struct{ table, atSite2 string }{
			{"building", `SELECT b.id::text FROM building b JOIN location l ON l.id = b.id WHERE l.site_id = $1`},
			{"location", `SELECT l.id::text FROM location l WHERE l.site_id = $1`},
			{"room", `SELECT r.id::text FROM room r JOIN location l ON l.id = r.id WHERE l.site_id = $1`},
			{"rack", `SELECT r.id::text FROM rack r JOIN location l ON l.id = r.id WHERE l.site_id = $1`},
			{"asset", `SELECT a.id::text FROM asset a JOIN location l ON l.id = a.location_id WHERE l.site_id = $1`},
		}
		var got []string
		for _, p := range probes {
			rows, err := f.Admin.Query(bg, p.atSite2, site2)
			if err != nil {
				t.Fatalf("%s at site 2: %v", p.table, err)
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil || len(ids) == 0 {
				t.Fatalf("%s at site 2: %v rows %v, want the seeded ones", p.table, ids, err)
			}
			got = append(got, fmt.Sprint(count(t, siteScope(site1), p.table, ids...)))
		}
		if want := "0/0/0/0/0"; strings.Join(got, "/") != want {
			t.Errorf("site-1 scope reads building/location/room/rack/asset rows of site 2: %s, want %s", strings.Join(got, "/"), want)
		}
	})

	t.Run("DB: writes of the site-1 principal", func(t *testing.T) {
		building1 := scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room1)
		building2 := scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room2)
		const (
			written = "written"
			refused = "refused"
			noRow   = "no row"
		)
		insertRoom := `INSERT INTO room (organization_id, building_id, name) VALUES ($1, $2, $3)`
		insertRack := `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, $3)`
		for _, c := range []struct {
			name string
			sql  string
			args []any
			want string
		}{
			{"room in a building of site 1", insertRoom, []any{f.OrgA, building1, "t1-sl-new"}, written},
			{"room in a building of site 2", insertRoom, []any{f.OrgA, building2, "t1-sl-new"}, refused},
			{"rack in a room of site 1", insertRack, []any{f.OrgA, room1, "t1-sl-new"}, written},
			{"rack in a room of site 2", insertRack, []any{f.OrgA, room2, "t1-sl-new"}, refused},
			{"move own room to site 2", `UPDATE room SET building_id = $2 WHERE id = $1`, []any{room1, building2}, refused},
			{"move own rack to site 2", `UPDATE rack SET room_id = $2 WHERE id = $1`, []any{rack1, room2}, refused},
			{"change room of site 2", `UPDATE room SET floor = 3 WHERE id = $1`, []any{room2}, noRow},
			{"rename room of site 2", `UPDATE room SET name = 't1-sl-renamed' WHERE id = $1`, []any{room2}, noRow},
			{"relabel room of site 2 as site 1", `UPDATE room SET site_id = $2 WHERE id = $1`, []any{room2, site1}, noRow},
			{"delete room of site 2", `DELETE FROM room WHERE id = $1`, []any{room2}, noRow},
			{"change rack of site 2", `UPDATE rack SET notes = 't1-sl' WHERE id = $1`, []any{rack2}, noRow},
			{"delete rack of site 2", `DELETE FROM rack WHERE id = $1`, []any{rack2}, noRow},
			{"change own room", `UPDATE room SET floor = 3 WHERE id = $1`, []any{room1}, written},
			{"change own rack", `UPDATE rack SET notes = 't1-sl' WHERE id = $1`, []any{rack1}, written},
		} {
			var rows int64
			err := database.WithTenant(bg, f.App, siteScope(site1), func(ctx context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, c.sql, c.args...)
				rows = tag.RowsAffected()
				// Roll back what was written, so the cases do not depend on
				// each other.
				if err == nil {
					err = errRollback
				}
				return err
			})
			if errors.Is(err, errRollback) {
				err = nil
			}
			var pgErr *pgconn.PgError
			var got string
			switch {
			case err == nil && rows >= 1:
				got = written
			case err == nil:
				got = noRow
			case errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "23503"):
				// The location of site 2 is invisible (parent not found) or
				// the location row fails WITH CHECK.
				got = refused
			default:
				got = fmt.Sprintf("error %v", err)
			}
			if got != c.want {
				t.Errorf("site 1: %s: %s, want %s", c.name, got, c.want)
			}
		}
	})

	scopeOf := func(t *testing.T, table, id string) string {
		t.Helper()
		return scalar(t, `SELECT client_id::text || '/' || site_id::text FROM `+table+` WHERE id = $1`, id)
	}

	t.Run("derivation: client and site of the location row", func(t *testing.T) {
		building1 := scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room1)
		claimed := scalar(t, `INSERT INTO room (organization_id, building_id, name, client_id, site_id) VALUES ($1, $2, 't1-sl-claim', $3, $4) RETURNING id::text`,
			f.OrgA, building1, f.Client2, site2)
		if got, want := scopeOf(t, "room", claimed), f.Client1+"/"+site1; got != want {
			t.Errorf("room inserted with client 2 and site 2 in a building of site 1: %s, want %s", got, want)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE rack SET client_id = $2, site_id = $3 WHERE id = $1`, rack1, f.Client2, site2); err != nil {
			t.Fatalf("write scope of rack 1: %v", err)
		}
		for _, c := range []struct{ table, id, want string }{
			{"room", room1, f.Client1 + "/" + site1}, {"rack", rack1, f.Client1 + "/" + site1},
			{"room", room2, f.Client1 + "/" + site2}, {"rack", rack2, f.Client1 + "/" + site2},
		} {
			if got := scopeOf(t, c.table, c.id); got != c.want {
				t.Errorf("%s %s: client/site %s, want %s", c.table, c.id, got, c.want)
			}
		}
		// Backfill and triggers: no room or rack differs from its location.
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM room r JOIN location l ON l.id = r.id WHERE (r.client_id, r.site_id) IS DISTINCT FROM (l.client_id, l.site_id))
		  + (SELECT count(*) FROM rack r JOIN location l ON l.id = r.id WHERE (r.client_id, r.site_id) IS DISTINCT FROM (l.client_id, l.site_id))`).Scan(&n); err != nil || n != 0 {
			t.Errorf("rooms and racks whose client or site differs from their location: %d (%v), want 0", n, err)
		}
	})

	t.Run("propagation: moves in the location tree", func(t *testing.T) {
		room3 := f.Room(t, f.Client1, "t1-sl-s3")
		site3 := scalar(t, `SELECT site_id::text FROM location WHERE id = $1`, room3)
		rack3 := scalar(t, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-sl-rack3') RETURNING id::text`, f.OrgA, room3)
		building1 := scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room1)
		building2 := scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room2)
		clientScope := database.OrgWideScope(f.OrgA, w.userA)
		clientScope.Clients = database.ScopeIDs(f.Client1)
		orgWide := database.OrgWideScope(f.OrgA, w.userA)
		for _, step := range []struct {
			name  string
			scope *database.TenantScope
			sql   string
			args  []any
			want  map[string]string
		}{
			{"a client-1 principal moves the building of room 2 to site 1", &clientScope,
				`UPDATE building SET site_id = $2 WHERE id = $1`, []any{building2, site1},
				map[string]string{room2: f.Client1 + "/" + site1, rack2: f.Client1 + "/" + site1}},
			{"site 3 moves to client 2", &orgWide,
				`UPDATE site SET client_id = $2 WHERE id = $1`, []any{site3, f.Client2},
				map[string]string{room3: f.Client2 + "/" + site3, rack3: f.Client2 + "/" + site3}},
			{"room 3 moves into the building of room 1", &orgWide,
				`UPDATE room SET building_id = $2 WHERE id = $1`, []any{room3, building1},
				map[string]string{room3: f.Client1 + "/" + site1, rack3: f.Client1 + "/" + site1}},
		} {
			if err := database.WithTenant(bg, f.App, step.scope, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, step.sql, step.args...)
				return err
			}); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			for id, want := range step.want {
				table := "room"
				if id == rack2 || id == rack3 {
					table = "rack"
				}
				if got := scopeOf(t, table, id); got != want {
					t.Errorf("%s: %s %s has client/site %s, want %s", step.name, table, id, got, want)
				}
			}
		}
		if n := count(t, siteScope(site1), "rack", rack2, rack3); n != 2 {
			t.Errorf("after the moves the site-1 principal sees %d of racks 2 and 3, want 2", n)
		}
		if n := count(t, siteScope(site2), "room", room2); n != 0 {
			t.Errorf("after the move of its building the site-2 principal still sees room 2")
		}
	})
}
