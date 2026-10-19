package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-025 (TEN-02, TEN-05, IMP-07; AC1 "Kindtabellen tragen client_id/
// site_id", AC2 "Trigger leiten den Scope aus dem CI ab, Schreiber können
// ihn nicht überschreiben") gap "Trigger derivation, spoof override and
// foreign-client rejection for most child tables (the matrix runs with
// triggers off)": TestCIChildTablesCarryClientScope covers network_interface,
// ip_address and ci_relationship only. This test runs, per CI child table,
// with triggers on and through WithTenant:
//   (a) an org-wide writer claims client 2 for a row of a client-1 CI: the
//       stored client_id/site_id are the CI's;
//   (b) a client-1 principal cannot insert a row for a client-2 CI;
//   (c) a client-2 principal selects 0 rows of the client-1 CI, a client-1
//       principal sees them;
//   (d) when the CI moves to another site, the rows' site_id follows.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1CIChildTableDerivation' ./internal/tenant/rls/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

func TestAuditT1CIChildTableDerivation(t *testing.T) {
	f := scopetest.Seed(t, "fc")
	bg := context.Background()
	scalar := func(query string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return v
	}
	siteOf := func(room string) string {
		return scalar(`SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room)
	}
	site1, site1b := siteOf(f.Room(t, f.Client1, "t1-child-s1")), siteOf(f.Room(t, f.Client1, "t1-child-s1b"))
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "t1-child-c1"), f.CI(t, f.OrgA, f.Client2, "t1-child-c2")
	if got := scalar(`UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`, ci1, site1); got != site1 {
		t.Fatalf("setup: CI site %q, want site 1", got)
	}
	rule := scalar(`INSERT INTO compliance_rule (organization_id, name, severity, category) VALUES ($1, 't1-child-rule', 'low', 'hygiene') RETURNING id::text`, f.OrgA)
	contact := scalar(`INSERT INTO contact (organization_id, client_id, display_name) VALUES ($1, $2, 't1-child-contact') RETURNING id::text`, f.OrgA, f.Client1)

	scope := func(clients ...string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, "")
		if len(clients) > 0 {
			s.Clients = database.ScopeIDs(clients...)
		}
		return &s
	}
	// insert runs the table's INSERT with $1 org, $2 CI, $3 the claimed
	// client and $4 the extra reference; it returns the new id.
	insert := func(s *database.TenantScope, query, ciID, claimed, extra string) (string, error) {
		var id string
		err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, f.OrgA, ciID, claimed, extra).Scan(&id)
		})
		return id, err
	}
	count := func(s *database.TenantScope, table, id string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&n)
		}); err != nil {
			t.Fatalf("select %s: %v", table, err)
		}
		return n
	}

	tables := []struct{ table, insert string }{
		{"ci_change", `INSERT INTO ci_change (organization_id, ci_id, change_type, client_id, comment) VALUES ($1, $2, 'update', $3, $4) RETURNING id::text`},
		{"ci_field_value", `INSERT INTO ci_field_value (organization_id, ci_id, field_name, client_id, override_reason) VALUES ($1, $2, 't1_field', $3, $4) RETURNING id::text`},
		{"ci_instance_field_definition", `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type, client_id, label) VALUES ($1, $2, 't1_def', 'string', $3, $4) RETURNING id::text`},
		{"security_finding", `INSERT INTO security_finding (organization_id, ci_id, kind, title, client_id, detail) VALUES ($1, $2, 'vulnerability', 't1-finding', $3, $4) RETURNING id::text`},
		{"compliance_result", `INSERT INTO compliance_result (organization_id, ci_id, status, client_id, rule_id) VALUES ($1, $2, 'pass', $3, $4::uuid) RETURNING id::text`},
		{"ci_contact", `INSERT INTO ci_contact (organization_id, ci_id, client_id, contact_id) VALUES ($1, $2, $3, $4::uuid) RETURNING id::text`},
	}
	extra := map[string]string{"compliance_result": rule, "ci_contact": contact}
	rows := map[string]string{}
	for _, tb := range tables {
		t.Run(tb.table, func(t *testing.T) {
			ex := extra[tb.table]
			if ex == "" {
				ex = "t1"
			}
			// (a) the writer's client claim is overridden by the CI's scope.
			id, err := insert(scope(), tb.insert, ci1, f.Client2, ex)
			if err != nil {
				t.Fatalf("org-wide insert: %v", err)
			}
			rows[tb.table] = id
			if got := scalar(`SELECT COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '') FROM `+tb.table+` WHERE id = $1`, id); got != f.Client1+"|"+site1 {
				t.Errorf("(a) stored client|site = %s, want the CI's %s|%s", got, f.Client1, site1)
			}
			// (b) a client-1 principal cannot write a row of a client-2 CI.
			if _, err := insert(scope(f.Client1), tb.insert, ci2, f.Client1, ex); err == nil {
				t.Errorf("(b) a client-1 principal inserted a %s row for the client-2 CI", tb.table)
			}
			if n := scalar(`SELECT count(*)::text FROM `+tb.table+` WHERE ci_id = $1`, ci2); n != "0" {
				t.Errorf("(b) %s rows of the client-2 CI: %s, want 0", tb.table, n)
			}
			// (c) visibility follows the CI's client.
			if n := count(scope(f.Client2), tb.table, id); n != 0 {
				t.Errorf("(c) a client-2 principal selects %d rows of the client-1 CI, want 0", n)
			}
			if n := count(scope(f.Client1), tb.table, id); n != 1 {
				t.Errorf("(c) a client-1 principal selects %d rows of its CI, want 1", n)
			}
		})
	}
	// (d) the CI moves to another site of client 1; every child follows.
	if got := scalar(`UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`, ci1, site1b); got != site1b {
		t.Fatalf("move: CI site %q, want site 1b", got)
	}
	for table, id := range rows {
		if got := scalar(`SELECT COALESCE(site_id::text, '') FROM `+table+` WHERE id = $1`, id); got != site1b {
			t.Errorf("(d) %s site_id after the CI moved = %s, want %s", table, got, site1b)
		}
	}
}
