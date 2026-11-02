package rls_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestSitePoliciesRestrictSiteScopedPrincipals covers migration 000063
// (WP-027, CH25, TEN-04, TEN-05, E-09): a principal restricted to site 1
// sees and writes only rows of site 1 in every table with a site column,
// keeps reading rows without site and cannot write them. The catalog test
// checks that no table with site_id lacks the predicate.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSitePoliciesRestrictSiteScopedPrincipals(t *testing.T) {
	f := scopetest.Seed(t, "47")
	bg := context.Background()
	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "site "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	ciAt := func(name, site string) string {
		id := f.CI(t, f.OrgA, f.Client1, name)
		if site != "" {
			if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, id, site); err != nil {
				t.Fatalf("place ci %s: %v", name, err)
			}
		}
		return id
	}
	ci1, ci2, ci0 := ciAt("site-1", site1), ciAt("site-2", site2), ciAt("no-site", "")

	ids := map[string]string{}
	seed := func(key, sql string, args ...any) {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		ids[key] = id
	}
	seed("building1", `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'b1')`, f.OrgA, site1)
	seed("building2", `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'b2')`, f.OrgA, site2)
	seed("nic1", `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0')`, f.OrgA, ci1)
	seed("nic2", `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0')`, f.OrgA, ci2)
	seed("subnet1", `INSERT INTO subnet (organization_id, client_id, site_id, cidr) VALUES ($1, $2, $3, '10.47.1.0/24')`, f.OrgA, f.Client1, site1)
	seed("subnet2", `INSERT INTO subnet (organization_id, client_id, site_id, cidr) VALUES ($1, $2, $3, '10.47.2.0/24')`, f.OrgA, f.Client1, site2)
	seed("subnet0", `INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.47.0.0/24')`, f.OrgA, f.Client1)
	seed("edge12", `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`, f.OrgA, ci1, ci2)
	seed("edge10", `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`, f.OrgA, ci1, ci0)

	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Sites = database.ScopeIDs(site1)
	run := func(fn func(ctx context.Context, tx pgx.Tx) error) error {
		return database.WithTenant(bg, f.App, &scope, fn)
	}

	// Reads: rows of site 1 and rows without site.
	for _, c := range []struct {
		table, id string
		visible   bool
	}{
		{"site", site1, true},
		{"site", site2, false},
		{"location", site1, true},
		{"location", site2, false},
		{"building", ids["building1"], true},
		{"building", ids["building2"], false},
		{"ci", ci1, true},
		{"ci", ci2, false},
		{"ci", ci0, true},
		{"network_interface", ids["nic1"], true},
		{"network_interface", ids["nic2"], false},
		{"subnet", ids["subnet1"], true},
		{"subnet", ids["subnet2"], false},
		{"subnet", ids["subnet0"], true},
		{"ci_relationship", ids["edge12"], false},
		{"ci_relationship", ids["edge10"], true},
	} {
		var n int
		if err := run(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+c.table+` WHERE id = $1`, c.id).Scan(&n)
		}); err != nil {
			t.Fatalf("read %s: %v", c.table, err)
		}
		if (n == 1) != c.visible {
			t.Errorf("%s %s: visible=%v, want %v", c.table, c.id, n == 1, c.visible)
		}
	}

	// Writes: only site 1; rows without site are for org-wide principals
	// only (E-09); a row cannot be moved out of the scope.
	for _, c := range []struct {
		name string
		sql  string
		args []any
		ok   bool
	}{
		{"ci at site 1", `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status) SELECT $1, $2, $3, ci_type_id, 'new', 'active' FROM ci WHERE id = $4`, []any{f.OrgA, f.Client1, site1, ci1}, true},
		{"ci at site 2", `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status) SELECT $1, $2, $3, ci_type_id, 'new', 'active' FROM ci WHERE id = $4`, []any{f.OrgA, f.Client1, site2, ci1}, false},
		{"ci without site", `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status) SELECT $1, $2, ci_type_id, 'new', 'active' FROM ci WHERE id = $3`, []any{f.OrgA, f.Client1, ci1}, false},
		{"move ci to site 2", `UPDATE ci SET location_id = $2 WHERE id = $1`, []any{ci1, site2}, false},
		{"building at site 2", `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'x')`, []any{f.OrgA, site2}, false},
		{"subnet without site", `INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.47.9.0/24')`, []any{f.OrgA, f.Client1}, false},
		{"site", `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'new site')`, []any{f.OrgA, f.Client1}, false},
	} {
		err := run(func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, c.sql, c.args...)
			if err == nil && tag.RowsAffected() == 0 {
				return errors.New("no row written")
			}
			return err
		})
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}

	// An org-wide principal still sees every site and writes rows without
	// site.
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ci WHERE id = ANY($1::uuid[])`, []string{ci1, ci2, ci0}).Scan(&n); err != nil {
			return err
		}
		if n != 3 {
			t.Errorf("org-wide: %d of 3 CIs visible", n)
		}
		_, err := tx.Exec(ctx, `INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.47.8.0/24')`, f.OrgA, f.Client1)
		return err
	}); err != nil {
		t.Fatalf("org-wide: %v", err)
	}
}

// TestSitePoliciesConfineAssetsAndQuantityItems covers migration 000151
// (WP-392, TEN-02, TEN-05, E-09, F-141): asset and quantity_item carry the
// site of their location, derived by a trigger whatever the writer passes,
// and their policies apply the site predicate of ci (000063).
//   - A principal of site 1 of client 1 (the scope a role at site 1 resolves
//     to, WP-390) reads the rows of site 1 and rows without site, not those
//     at site 1b of client 1 or at site 2 of client 2.
//   - It writes rows at site 1 only: an INSERT or a move to another site,
//     and a row without site (E-09), fail at WITH CHECK; rows of other sites
//     are neither changed nor deleted.
//   - An org-wide principal sees all and writes rows without site; a
//     location of another organization is not found.
//   - A written site_id is overwritten; when a building moves to another
//     site the rows in its rooms follow, also those the mover cannot see.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSitePoliciesConfineAssetsAndQuantityItems(t *testing.T) {
	f := scopetest.Seed(t, "ff")
	bg := context.Background()
	room1 := f.Room(t, f.Client1, "ff-c1-s1")
	room1b := f.Room(t, f.Client1, "ff-c1-s1b")
	room2 := f.Room(t, f.Client2, "ff-c2-s2")
	siteOf := func(location string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, location).Scan(&site); err != nil {
			t.Fatalf("site of location %s: %v", location, err)
		}
		return site
	}
	site1, site1b := siteOf(room1), siteOf(room1b)
	var building1b string
	if err := f.Admin.QueryRow(bg, `SELECT parent_id::text FROM location WHERE id = $1`, room1b).Scan(&building1b); err != nil {
		t.Fatalf("building of room 1b: %v", err)
	}
	clientB, siteB := f.ID(), ""
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', 'scopetest-ff-cb')`, clientB, f.OrgB); err != nil {
		t.Fatalf("seed client of organization B: %v", err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'ff-site-b') RETURNING id::text`, f.OrgB, clientB).Scan(&siteB); err != nil {
		t.Fatalf("seed site of organization B: %v", err)
	}

	// Rows are seeded through the maintenance pool with the triggers active.
	seed := func(table, client, name, location string) string {
		t.Helper()
		var loc any
		if location != "" {
			loc = location
		}
		sql := `INSERT INTO quantity_item (organization_id, client_id, name, location_id) VALUES ($1, $2, $3, $4) RETURNING id::text`
		if table == "asset" {
			sql = `INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, $3, $3, $4) RETURNING id::text`
		}
		var id string
		if err := f.Admin.QueryRow(bg, sql, f.OrgA, client, name, loc).Scan(&id); err != nil {
			t.Fatalf("seed %s %s: %v", table, name, err)
		}
		return id
	}
	ids := map[string]string{}
	for _, table := range []string{"asset", "quantity_item"} {
		ids[table+"1"] = seed(table, f.Client1, "ff-"+table+"-c1-s1", room1)
		ids[table+"1b"] = seed(table, f.Client1, "ff-"+table+"-c1-s1b", room1b)
		ids[table+"2"] = seed(table, f.Client2, "ff-"+table+"-c2-s2", room2)
		ids[table+"0"] = seed(table, f.Client1, "ff-"+table+"-c1-unplaced", "")
		// Client 2 at site 1b of client 1: invisible to a mover restricted
		// to client 1, it must follow the building all the same.
		ids[table+"x"] = seed(table, f.Client2, "ff-"+table+"-c2-s1b", room1b)
	}

	site1Scope := database.OrgWideScope(f.OrgA, f.User)
	site1Scope.Clients = database.ScopeIDs(f.Client1)
	site1Scope.Sites = database.ScopeIDs(site1)
	asSite1 := func(fn func(ctx context.Context, tx pgx.Tx) error) error {
		return database.WithTenant(bg, f.App, &site1Scope, fn)
	}

	// Reads: site 1 and no site; neither site 1b nor client 2.
	for _, table := range []string{"asset", "quantity_item"} {
		for _, c := range []struct {
			key     string
			visible bool
		}{{"1", true}, {"0", true}, {"1b", false}, {"2", false}, {"x", false}} {
			var n int
			if err := asSite1(func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, ids[table+c.key]).Scan(&n)
			}); err != nil {
				t.Fatalf("read %s: %v", table, err)
			}
			if (n == 1) != c.visible {
				t.Errorf("site 1: %s %s visible=%v, want %v", table, c.key, n == 1, c.visible)
			}
		}
	}

	// Writes of the site-1 principal.
	const (
		written   = "written"
		withCheck = "refused by WITH CHECK"
		noRow     = "no row"
	)
	insertAsset := `INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, $3, $3, $4)`
	insertItem := `INSERT INTO quantity_item (organization_id, client_id, name, location_id) VALUES ($1, $2, $3, $4)`
	for _, c := range []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{"asset at site 1", insertAsset, []any{f.OrgA, f.Client1, "ff-new-s1", room1}, written},
		{"asset at site 1b", insertAsset, []any{f.OrgA, f.Client1, "ff-new-s1b", room1b}, withCheck},
		{"asset of client 1 at site 2 of client 2", insertAsset, []any{f.OrgA, f.Client1, "ff-new-s2", room2}, withCheck},
		{"asset without location (E-09)", insertAsset, []any{f.OrgA, f.Client1, "ff-new-none", nil}, withCheck},
		{"asset at site 1b claiming site 1", `INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id, site_id) VALUES ($1, $2, $3, $3, $4, $5)`,
			[]any{f.OrgA, f.Client1, "ff-new-claim", room1b, site1}, withCheck},
		{"move asset to site 1b", `UPDATE asset SET location_id = $2 WHERE id = $1`, []any{ids["asset1"], room1b}, withCheck},
		{"unplace asset (E-09)", `UPDATE asset SET location_id = NULL WHERE id = $1`, []any{ids["asset1"]}, withCheck},
		{"relabel asset of site 1b as site 1", `UPDATE asset SET site_id = $2 WHERE id = $1`, []any{ids["asset1b"], site1}, noRow},
		{"rename asset of site 1b", `UPDATE asset SET name = 'ff-renamed' WHERE id = $1`, []any{ids["asset1b"]}, noRow},
		{"delete asset of site 1b", `DELETE FROM asset WHERE id = $1`, []any{ids["asset1b"]}, noRow},
		{"rename asset of site 1", `UPDATE asset SET name = 'ff-renamed' WHERE id = $1`, []any{ids["asset1"]}, written},
		{"quantity item at site 1", insertItem, []any{f.OrgA, f.Client1, "ff-new-item-s1", room1}, written},
		{"quantity item at site 1b", insertItem, []any{f.OrgA, f.Client1, "ff-new-item-s1b", room1b}, withCheck},
		{"quantity item without location (E-09)", insertItem, []any{f.OrgA, f.Client1, "ff-new-item-none", nil}, withCheck},
		{"move quantity item to site 1b", `UPDATE quantity_item SET location_id = $2 WHERE id = $1`, []any{ids["quantity_item1"], room1b}, withCheck},
		{"change stock of quantity item of site 1b", `UPDATE quantity_item SET stock_level = stock_level + 1 WHERE id = $1`, []any{ids["quantity_item1b"]}, noRow},
		{"delete quantity item of site 1b", `DELETE FROM quantity_item WHERE id = $1`, []any{ids["quantity_item1b"]}, noRow},
	} {
		var rows int64
		err := asSite1(func(ctx context.Context, tx pgx.Tx) error {
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
		var got string
		switch {
		case err == nil && rows == 1:
			got = written
		case err == nil && rows == 0:
			got = noRow
		case rlsDenied(err):
			got = withCheck
		default:
			got = fmt.Sprintf("error %v", err)
		}
		if got != c.want {
			t.Errorf("site 1: %s: %s, want %s", c.name, got, c.want)
		}
	}

	// An org-wide principal sees every row and writes rows without site; a
	// location of organization B is not found.
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"asset", "quantity_item"} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = ANY($1::uuid[])`,
				[]string{ids[table+"1"], ids[table+"1b"], ids[table+"2"], ids[table+"0"], ids[table+"x"]}).Scan(&n); err != nil {
				return err
			}
			if n != 5 {
				t.Errorf("org-wide: %d of 5 %s rows visible", n, table)
			}
		}
		if _, err := tx.Exec(ctx, insertAsset, f.OrgA, nil, "ff-orgwide-none", nil); err != nil {
			t.Errorf("org-wide: asset without location: %v", err)
		}
		if _, err := tx.Exec(ctx, insertItem, f.OrgA, nil, "ff-orgwide-item-none", nil); err != nil {
			t.Errorf("org-wide: quantity item without location: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("org-wide: %v", err)
	}
	for _, c := range []struct{ table, sql string }{{"asset", insertAsset}, {"quantity_item", insertItem}} {
		err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, c.sql, f.OrgA, f.Client1, "ff-foreign-location-"+c.table, siteB)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Errorf("org-wide: %s at a location of organization B: err %v, want 23503 (location not found)", c.table, err)
		}
	}

	// Derivation: the site of the location, a written value is overwritten.
	siteIDOf := func(table, id string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM `+table+` WHERE id = $1`, id).Scan(&site); err != nil {
			t.Errorf("site_id of %s %s: %v", table, id, err)
		}
		return site
	}
	for _, table := range []string{"asset", "quantity_item"} {
		if _, err := f.Admin.Exec(bg, `UPDATE `+table+` SET site_id = $2 WHERE id = $1`, ids[table+"1"], site1b); err != nil {
			t.Errorf("write site_id of %s: %v", table, err)
		}
		for key, want := range map[string]string{"1": site1, "1b": site1b, "2": siteOf(room2), "0": "", "x": site1b} {
			if got := siteIDOf(table, ids[table+key]); got != want {
				t.Errorf("%s %s: site_id %q, want %q (site of its location)", table, key, got, want)
			}
		}
	}

	// Propagation: a principal restricted to client 1 moves the building of
	// room 1b to site 1; the rows in the room follow, also those of client
	// 2 it cannot see.
	clientScope := database.OrgWideScope(f.OrgA, f.User)
	clientScope.Clients = database.ScopeIDs(f.Client1)
	if err := database.WithTenant(bg, f.App, &clientScope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE building SET site_id = $2 WHERE id = $1`, building1b, site1)
		return err
	}); err != nil {
		t.Fatalf("move building of room 1b to site 1: %v", err)
	}
	for _, table := range []string{"asset", "quantity_item"} {
		for _, key := range []string{"1b", "x"} {
			if got := siteIDOf(table, ids[table+key]); got != site1 {
				t.Errorf("after the move: %s %s: site_id %q, want site 1 %q", table, key, got, site1)
			}
		}
	}
	var n int
	if err := asSite1(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM asset WHERE id = $1`, ids["asset1b"]).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("after the move: site 1 sees the asset of room 1b: %d (%v), want 1", n, err)
	}
}
