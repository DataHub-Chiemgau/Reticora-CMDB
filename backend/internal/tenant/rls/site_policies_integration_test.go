package rls_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
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
