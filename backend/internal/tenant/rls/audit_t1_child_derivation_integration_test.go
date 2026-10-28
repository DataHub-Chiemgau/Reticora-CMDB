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
// WP-533 (TEN-02) adds the tables left out above, each with insert and move
// of the parent:
//   - rack_mount and the matched discovery_result run through (a) to (d); the
//     matched result's job belongs to a collector of client 2, so the CI and
//     not the collector decides;
//   - an unmatched discovery_result takes the client of its job's collector
//     and no site; matched to the CI it follows the CI (and its move),
//     unmatched again it returns to the collector's client;
//   - relationship_suppression carries the scope of both endpoints: claims
//     are overridden, a client-1 principal cannot suppress towards a client-2
//     CI, and when the source moves to another site and the target to client
//     2, source_site_id and target_client_id/target_site_id follow and the
//     client-1 principal no longer sees the row.
//
// WP-636 (TEN-02; F-363) moves TestAuditCollectorBindingMovesUnmatchedResults
// here: the unmatched results of a collector follow every change of its
// client (migration 000176), also the ones written before the change.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditCollectorBindingMovesUnmatchedResults|TestAuditT1CIChildTableDerivation' ./internal/tenant/rls/

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
	// Discovery results run in a job of a client-2 collector.
	collector2 := scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 't1-child-collector-c2', 'active') RETURNING id::text`, f.OrgA, f.Client2)
	job2 := scalar(`INSERT INTO discovery_job (organization_id, collector_id, job_type) VALUES ($1, $2, 'sweep') RETURNING id::text`, f.OrgA, collector2)
	rack := scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-child-rack') RETURNING id::text`, f.OrgA, f.Room(t, f.Client1, "t1-child-rack"))

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
	exec := func(s *database.TenantScope, query string, args ...any) {
		t.Helper()
		if err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	scopeOf := func(table, id string) string {
		t.Helper()
		return scalar(`SELECT COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '') FROM `+table+` WHERE id = $1`, id)
	}

	// ciColumn is the column naming the CI ("" = ci_id).
	tables := []struct{ table, insert, ciColumn string }{
		{"ci_change", `INSERT INTO ci_change (organization_id, ci_id, change_type, client_id, comment) VALUES ($1, $2, 'update', $3, $4) RETURNING id::text`, ""},
		{"ci_field_value", `INSERT INTO ci_field_value (organization_id, ci_id, field_name, client_id, override_reason) VALUES ($1, $2, 't1_field', $3, $4) RETURNING id::text`, ""},
		{"ci_instance_field_definition", `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type, client_id, label) VALUES ($1, $2, 't1_def', 'string', $3, $4) RETURNING id::text`, ""},
		{"security_finding", `INSERT INTO security_finding (organization_id, ci_id, kind, title, client_id, detail) VALUES ($1, $2, 'vulnerability', 't1-finding', $3, $4) RETURNING id::text`, ""},
		{"compliance_result", `INSERT INTO compliance_result (organization_id, ci_id, status, client_id, rule_id) VALUES ($1, $2, 'pass', $3, $4::uuid) RETURNING id::text`, ""},
		{"ci_contact", `INSERT INTO ci_contact (organization_id, ci_id, client_id, contact_id) VALUES ($1, $2, $3, $4::uuid) RETURNING id::text`, ""},
		{"rack_mount", `INSERT INTO rack_mount (organization_id, ci_id, client_id, rack_id, position_u) VALUES ($1, $2, $3, $4::uuid, 1) RETURNING id::text`, ""},
		{"discovery_result", `INSERT INTO discovery_result (organization_id, matched_ci_id, client_id, job_id, raw_data) VALUES ($1, $2, $3, $4::uuid, '{}') RETURNING id::text`, "matched_ci_id"},
	}
	extra := map[string]string{"compliance_result": rule, "ci_contact": contact, "rack_mount": rack, "discovery_result": job2}
	rows := map[string]string{}
	for _, tb := range tables {
		t.Run(tb.table, func(t *testing.T) {
			ex := extra[tb.table]
			if ex == "" {
				ex = "t1"
			}
			ciColumn := tb.ciColumn
			if ciColumn == "" {
				ciColumn = "ci_id"
			}
			// (a) the writer's client claim is overridden by the CI's scope.
			id, err := insert(scope(), tb.insert, ci1, f.Client2, ex)
			if err != nil {
				t.Fatalf("org-wide insert: %v", err)
			}
			rows[tb.table] = id
			if got := scopeOf(tb.table, id); got != f.Client1+"|"+site1 {
				t.Errorf("(a) stored client|site = %s, want the CI's %s|%s", got, f.Client1, site1)
			}
			// (b) a client-1 principal cannot write a row of a client-2 CI.
			if _, err := insert(scope(f.Client1), tb.insert, ci2, f.Client1, ex); err == nil {
				t.Errorf("(b) a client-1 principal inserted a %s row for the client-2 CI", tb.table)
			}
			if n := scalar(`SELECT count(*)::text FROM `+tb.table+` WHERE `+ciColumn+` = $1`, ci2); n != "0" {
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

	// An unmatched discovery result belongs to the client of its job's
	// collector and has no site; matching it to the client-1 CI moves it to
	// the CI's scope (it then follows the CI's move in (d)).
	unmatchedInsert := `INSERT INTO discovery_result (organization_id, job_id, client_id, site_id, raw_data) VALUES ($1, $2, $3, $4, '{}') RETURNING id::text`
	var unmatched string
	t.Run("discovery_result unmatched", func(t *testing.T) {
		var err error
		if err = database.WithTenant(bg, f.App, scope(), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, unmatchedInsert, f.OrgA, job2, f.Client1, site1).Scan(&unmatched)
		}); err != nil {
			t.Fatalf("org-wide insert: %v", err)
		}
		if got := scopeOf("discovery_result", unmatched); got != f.Client2+"|" {
			t.Errorf("stored client|site = %s, want the collector's %s|", got, f.Client2)
		}
		if err := database.WithTenant(bg, f.App, scope(f.Client1), func(ctx context.Context, tx pgx.Tx) error {
			var id string
			return tx.QueryRow(ctx, unmatchedInsert, f.OrgA, job2, f.Client1, nil).Scan(&id)
		}); err == nil {
			t.Errorf("a client-1 principal inserted an unmatched result of the client-2 collector")
		}
		if n := count(scope(f.Client1), "discovery_result", unmatched); n != 0 {
			t.Errorf("a client-1 principal selects %d unmatched results of the client-2 collector, want 0", n)
		}
		if n := count(scope(f.Client2), "discovery_result", unmatched); n != 1 {
			t.Errorf("a client-2 principal selects %d unmatched results of its collector, want 1", n)
		}
		exec(scope(), `UPDATE discovery_result SET matched_ci_id = $2 WHERE id = $1`, unmatched, ci1)
		if got := scopeOf("discovery_result", unmatched); got != f.Client1+"|"+site1 {
			t.Errorf("matched to the client-1 CI: client|site = %s, want the CI's %s|%s", got, f.Client1, site1)
		}
		rows["discovery_result (matched later)"] = unmatched
	})

	// relationship_suppression between ci1 (client 1, site 1) and ciT (client
	// 1, site 1t); both endpoints' scope is derived.
	siteT := siteOf(f.Room(t, f.Client1, "t1-child-s1t"))
	site2 := siteOf(f.Room(t, f.Client2, "t1-child-s2"))
	ciT := f.CI(t, f.OrgA, f.Client1, "t1-child-c1t")
	if got := scalar(`UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`, ciT, siteT); got != siteT {
		t.Fatalf("setup: target CI site %q, want site 1t", got)
	}
	relType := scalar(`SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`)
	suppressionInsert := `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id, source_client_id, source_site_id, target_client_id, target_site_id)
		VALUES ($1, $2, $3, $4, $5, $6, $5, $6) RETURNING id::text`
	edgeScope := func(id string) string {
		t.Helper()
		return scalar(`SELECT COALESCE(source_client_id::text, '') || '|' || COALESCE(source_site_id::text, '') || '>' ||
			COALESCE(target_client_id::text, '') || '|' || COALESCE(target_site_id::text, '') FROM relationship_suppression WHERE id = $1`, id)
	}
	var suppression string
	t.Run("relationship_suppression", func(t *testing.T) {
		// (a) claims of client 2 / site 2 for both endpoints are overridden.
		if err := database.WithTenant(bg, f.App, scope(), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, suppressionInsert, f.OrgA, ci1, ciT, relType, f.Client2, site2).Scan(&suppression)
		}); err != nil {
			t.Fatalf("org-wide insert: %v", err)
		}
		if got, want := edgeScope(suppression), f.Client1+"|"+site1+">"+f.Client1+"|"+siteT; got != want {
			t.Errorf("(a) stored source>target client|site = %s, want %s", got, want)
		}
		// (b) a client-1 principal cannot suppress towards the client-2 CI.
		if err := database.WithTenant(bg, f.App, scope(f.Client1), func(ctx context.Context, tx pgx.Tx) error {
			var id string
			return tx.QueryRow(ctx, suppressionInsert, f.OrgA, ci1, ci2, relType, f.Client1, site1).Scan(&id)
		}); err == nil {
			t.Errorf("(b) a client-1 principal inserted a suppression towards the client-2 CI")
		}
		if n := scalar(`SELECT count(*)::text FROM relationship_suppression WHERE source_ci_id = $1 AND target_ci_id = $2`, ci1, ci2); n != "0" {
			t.Errorf("(b) suppressions towards the client-2 CI: %s, want 0", n)
		}
		// (c) visibility follows both endpoints.
		if n := count(scope(f.Client2), "relationship_suppression", suppression); n != 0 {
			t.Errorf("(c) a client-2 principal selects %d suppressions between client-1 CIs, want 0", n)
		}
		if n := count(scope(f.Client1), "relationship_suppression", suppression); n != 1 {
			t.Errorf("(c) a client-1 principal selects %d suppressions between its CIs, want 1", n)
		}
	})

	// (d) the CI moves to another site of client 1; every child follows.
	if got := scalar(`UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`, ci1, site1b); got != site1b {
		t.Fatalf("move: CI site %q, want site 1b", got)
	}
	for table, id := range rows {
		if table == "discovery_result (matched later)" {
			table = "discovery_result"
		}
		if got := scalar(`SELECT COALESCE(site_id::text, '') FROM `+table+` WHERE id = $1`, id); got != site1b {
			t.Errorf("(d) %s site_id after the CI moved = %s, want %s", table, got, site1b)
		}
	}
	if unmatched != "" {
		// Unmatched again, the result returns to the collector's client.
		exec(scope(), `UPDATE discovery_result SET matched_ci_id = NULL WHERE id = $1`, unmatched)
		if got := scopeOf("discovery_result", unmatched); got != f.Client2+"|" {
			t.Errorf("unmatched again: client|site = %s, want the collector's %s|", got, f.Client2)
		}
	}
	if suppression != "" {
		// The source moved to site 1b above; the target moves to client 2.
		if got := scalar(`UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1 RETURNING COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '')`,
			ciT, f.Client2, site2); got != f.Client2+"|"+site2 {
			t.Fatalf("move: target CI client|site %q, want client 2/site 2", got)
		}
		if got, want := edgeScope(suppression), f.Client1+"|"+site1b+">"+f.Client2+"|"+site2; got != want {
			t.Errorf("(d) suppression source>target client|site after both endpoints moved = %s, want %s", got, want)
		}
		if n := count(scope(f.Client1), "relationship_suppression", suppression); n != 0 {
			t.Errorf("(d) a client-1 principal selects %d suppressions towards the moved client-2 CI, want 0", n)
		}
		if n := count(scope(f.Client1, f.Client2), "relationship_suppression", suppression); n != 1 {
			t.Errorf("(d) a client-1+2 principal selects %d suppressions between its CIs, want 1", n)
		}
	}
}

// TestAuditCollectorBindingMovesUnmatchedResults (WP-636, F-363): after an
// organization-wide collector is bound to a site of client 2, its earlier
// unmatched results carry client 2 like the later ones, a client-1
// principal reads none of them and a client-2 principal reads both; a
// matched result keeps the scope of its CI. Releasing the binding (site_id
// NULL) keeps the collector's client (collector_site_binding, 000127) and
// so the results' client; clearing the client returns the results to the
// organization.
func TestAuditCollectorBindingMovesUnmatchedResults(t *testing.T) {
	f := scopetest.Seed(t, "fc")
	bg := context.Background()
	scalar := func(q string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	scope := func(clients ...string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, "")
		if len(clients) > 0 {
			s.Clients = database.ScopeIDs(clients...)
		}
		return &s
	}
	site2 := scalar(`SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, f.Room(t, f.Client2, "k5-binding-s2"))
	ci1 := f.CI(t, f.OrgA, f.Client1, "k5-binding-c1")
	collector := scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'k5-binding-collector', 'active') RETURNING id::text`, f.OrgA)
	job := scalar(`INSERT INTO discovery_job (organization_id, collector_id, job_type) VALUES ($1, $2, 'sweep') RETURNING id::text`, f.OrgA, collector)
	insert := func(matched any) string {
		t.Helper()
		var id string
		if err := database.WithTenant(bg, f.App, scope(), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO discovery_result (organization_id, job_id, matched_ci_id, raw_data)
				VALUES ($1, $2, $3, '{"ip":"10.64.0.9"}') RETURNING id::text`, f.OrgA, job, matched).Scan(&id)
		}); err != nil {
			t.Fatalf("insert discovery result: %v", err)
		}
		return id
	}
	// updateCollector changes the collector as an organization-wide app
	// principal.
	updateCollector := func(name, set string, args ...any) {
		t.Helper()
		if err := database.WithTenant(bg, f.App, scope(), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE collector SET `+set+` WHERE id = $1`, append([]any{collector}, args...)...)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	before := insert(nil)
	matched := insert(ci1)
	if got := scalar(`SELECT COALESCE(client_id::text, '') FROM discovery_result WHERE id = $1`, before); got != "" {
		t.Fatalf("setup: unmatched result of the organization-wide collector has client %q, want none", got)
	}

	// The organization-wide collector is bound to a site of client 2.
	updateCollector("bind the collector to the client-2 site", `site_id = $2`, site2)
	if got := scalar(`SELECT COALESCE(client_id::text, '') FROM collector WHERE id = $1`, collector); got != f.Client2 {
		t.Fatalf("setup: collector client after the binding %q, want client 2", got)
	}
	after := insert(nil)

	unmatchedScope := func(stage, want string) {
		t.Helper()
		for name, id := range map[string]string{"result before the binding": before, "result after the binding": after} {
			if got := scalar(`SELECT COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '') FROM discovery_result WHERE id = $1`, id); got != want {
				t.Errorf("%s: unmatched %s: client|site = %q, want the collector's %s", stage, name, got, want)
			}
		}
	}
	count := func(s *database.TenantScope, ids ...string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM discovery_result WHERE id = ANY($1::uuid[])`, ids).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Control: the matched result stays with its CI.
	matchedStays := func(stage string) {
		t.Helper()
		if got := scalar(`SELECT COALESCE(client_id::text, '') FROM discovery_result WHERE id = $1`, matched); got != f.Client1 {
			t.Errorf("%s: matched result: client %q, want the CI's client 1", stage, got)
		}
	}
	unmatchedScope("binding", f.Client2+"|")
	if n := count(scope(f.Client1), before, after); n != 0 {
		t.Errorf("binding: a client-1 principal reads %d unmatched results of the client-2 collector, want 0", n)
	}
	if n := count(scope(f.Client2), before, after); n != 2 {
		t.Errorf("binding: a client-2 principal reads %d unmatched results of its collector, want 2", n)
	}
	matchedStays("binding")

	// Releasing the binding (PATCH {"site_id": ""}) keeps the collector's
	// client, and so the results' client.
	updateCollector("release the site binding", `site_id = NULL`)
	if got := scalar(`SELECT COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '') FROM collector WHERE id = $1`, collector); got != f.Client2+"|" {
		t.Fatalf("collector client|site after releasing the binding %q, want %s|", got, f.Client2)
	}
	unmatchedScope("release", f.Client2+"|")
	if n := count(scope(f.Client1), before, after); n != 0 {
		t.Errorf("release: a client-1 principal reads %d unmatched results of the client-2 collector, want 0", n)
	}
	if n := count(scope(f.Client2), before, after); n != 2 {
		t.Errorf("release: a client-2 principal reads %d unmatched results of its collector, want 2", n)
	}
	matchedStays("release")

	// Clearing the client (any other path) returns the unmatched results to
	// the organization in the same statement.
	updateCollector("clear the collector client", `client_id = NULL`)
	unmatchedScope("client cleared", "|")
	if n := count(scope(f.Client1), before, after); n != 2 {
		t.Errorf("client cleared: a client-1 principal reads %d unmatched results of the organization-wide collector, want 2", n)
	}
	matchedStays("client cleared")
}
