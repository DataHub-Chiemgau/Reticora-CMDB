//go:build auditdefect

package rls_test

// Defect test found by WP-533 (t1-ev-child-derivation, TEN-02, TEN-09;
// F-363): an unmatched discovery_result takes the client of its job's
// collector (derive_discovery_result_scope, migration 000061) only when it
// is written. When an organization-wide collector is bound to a site of
// client 2 (PATCH /api/v1/collectors/{id} {"site_id"}; trigger
// collector_site_binding, migration 000127, sets its client), the unmatched
// results it produced before keep client_id NULL: a principal restricted to
// client 1 still reads them, while the results of the next run belong to
// client 2. A CI move propagates to its child rows (migration 000141); for
// the collector there is no such propagation.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^TestAuditCollectorBindingMovesUnmatchedResults$' ./internal/tenant/rls/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestAuditCollectorBindingMovesUnmatchedResults: after an organization-wide
// collector is bound to a site of client 2, its earlier unmatched results
// carry client 2 like the later ones, a client-1 principal reads none of
// them and a client-2 principal reads both; a matched result keeps the scope
// of its CI.
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
	before := insert(nil)
	matched := insert(ci1)
	if got := scalar(`SELECT COALESCE(client_id::text, '') FROM discovery_result WHERE id = $1`, before); got != "" {
		t.Fatalf("setup: unmatched result of the organization-wide collector has client %q, want none", got)
	}

	// The organization-wide collector is bound to a site of client 2.
	if err := database.WithTenant(bg, f.App, scope(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE collector SET site_id = $2 WHERE id = $1`, collector, site2)
		return err
	}); err != nil {
		t.Fatalf("bind the collector to the client-2 site: %v", err)
	}
	if got := scalar(`SELECT COALESCE(client_id::text, '') FROM collector WHERE id = $1`, collector); got != f.Client2 {
		t.Fatalf("setup: collector client after the binding %q, want client 2", got)
	}
	after := insert(nil)

	for name, id := range map[string]string{"result before the binding": before, "result after the binding": after} {
		if got := scalar(`SELECT COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '') FROM discovery_result WHERE id = $1`, id); got != f.Client2+"|" {
			t.Errorf("unmatched %s: client|site = %q, want the collector's %s|", name, got, f.Client2)
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
	if n := count(scope(f.Client1), before, after); n != 0 {
		t.Errorf("a client-1 principal reads %d unmatched results of the client-2 collector, want 0", n)
	}
	if n := count(scope(f.Client2), before, after); n != 2 {
		t.Errorf("a client-2 principal reads %d unmatched results of its collector, want 2", n)
	}
	// Control: the matched result stays with its CI.
	if got := scalar(`SELECT COALESCE(client_id::text, '') FROM discovery_result WHERE id = $1`, matched); got != f.Client1 {
		t.Errorf("matched result after the binding: client %q, want the CI's client 1", got)
	}
}
