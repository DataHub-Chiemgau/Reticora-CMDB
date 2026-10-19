package rls_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestCIChildTablesCarryClientScope covers migration 000061 (WP-025, TEN-02,
// TEN-05, IMP-07, SRC-01): the child rows of a CI derive client_id and
// site_id from it whatever the writer passes, follow the CI when it moves to
// another client, and the policies reject rows of a foreign client. A
// relationship is visible only when both endpoints are.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIChildTablesCarryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "42")
	bg := context.Background()
	own := f.CI(t, f.OrgA, f.Client1, "children-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "children-c2")

	orgWide := database.OrgWideScope(f.OrgA, "")
	var nicID, edgeID string
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		// The writer claims client 2; the trigger derives client 1 from the CI.
		if err := tx.QueryRow(ctx,
			`INSERT INTO network_interface (organization_id, ci_id, name, client_id) VALUES ($1, $2, 'eth0', $3) RETURNING id::text`,
			f.OrgA, own, f.Client2).Scan(&nicID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ip_address (organization_id, interface_id, address, type) VALUES ($1, $2, '10.42.0.1', 'static')`,
			f.OrgA, nicID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on') RETURNING id::text`,
			f.OrgA, own, foreign).Scan(&edgeID)
	}); err != nil {
		t.Fatalf("org-wide writes: %v", err)
	}

	var nicClient, ipClient, srcClient, dstClient string
	if err := f.Admin.QueryRow(bg, `
		SELECT (SELECT client_id::text FROM network_interface WHERE id = $1),
		       (SELECT client_id::text FROM ip_address WHERE interface_id = $1),
		       (SELECT source_client_id::text FROM ci_relationship WHERE id = $2),
		       (SELECT target_client_id::text FROM ci_relationship WHERE id = $2)`,
		nicID, edgeID).Scan(&nicClient, &ipClient, &srcClient, &dstClient); err != nil {
		t.Fatalf("read derived scope: %v", err)
	}
	if nicClient != f.Client1 || ipClient != f.Client1 || srcClient != f.Client1 || dstClient != f.Client2 {
		t.Fatalf("derived clients: interface=%s ip=%s edge=%s->%s, want client 1, client 1, client 1->client 2", nicClient, ipClient, srcClient, dstClient)
	}

	client1 := database.OrgWideScope(f.OrgA, "")
	client1.Clients = database.ScopeIDs(f.Client1)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&n)
		}); err != nil {
			t.Fatalf("client-1 query %q: %v", query, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM network_interface WHERE ci_id = $1`, own); n != 1 {
		t.Fatalf("client-1 interfaces of own CI: %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, edgeID); n != 0 {
		t.Fatal("client-1 principal sees an edge to a client-2 CI")
	}
	if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth1')`, f.OrgA, foreign)
		return err
	}); err == nil {
		t.Fatal("client-1 principal created an interface on a client-2 CI")
	}

	// Moving the CI to client 2 moves its interface, address and edge.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, own, f.Client2); err != nil {
		t.Fatalf("move CI: %v", err)
	}
	if err := f.Admin.QueryRow(bg, `
		SELECT (SELECT client_id::text FROM network_interface WHERE id = $1),
		       (SELECT client_id::text FROM ip_address WHERE interface_id = $1),
		       (SELECT source_client_id::text FROM ci_relationship WHERE id = $2)`,
		nicID, edgeID).Scan(&nicClient, &ipClient, &srcClient); err != nil {
		t.Fatalf("read moved scope: %v", err)
	}
	if nicClient != f.Client2 || ipClient != f.Client2 || srcClient != f.Client2 {
		t.Fatalf("after move: interface=%s ip=%s edge source=%s, want client 2", nicClient, ipClient, srcClient)
	}
	if n := count(`SELECT count(*) FROM network_interface WHERE id = $1`, nicID); n != 0 {
		t.Fatal("client-1 principal still sees the interface of the moved CI")
	}
}

// TestCIChildEdgeToOrgWideCI pins decision E-62, option 1 (transition rule
// until approval; WP-345, F-182, probe_edge_orgwide.sql): a client-scoped
// principal sees an org-wide CI (client_id NULL) but creates neither an
// edge or suppression between it and an own CI nor an interface on it. The
// WITH CHECK of ci_relationship, relationship_suppression and
// network_interface requires every endpoint client in the scope (E-09); the
// API refuses the edge with a generic 403/404. Org-wide principals create
// the edge, and the client-scoped principal reads it (USING keeps NULL
// endpoints readable).
func TestCIChildEdgeToOrgWideCI(t *testing.T) {
	w := newT1World(t, "05")
	f := w.f
	bg := context.Background()
	own := f.CI(t, f.OrgA, f.Client1, "e62-own")
	orgWideCI := f.CI(t, f.OrgA, "", "e62-org-wide")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatalf("relationship type: %v", err)
	}

	client1 := database.OrgWideScope(f.OrgA, w.userA)
	client1.Clients = database.ScopeIDs(f.Client1)
	inClient1 := func(query string, args ...any) error {
		return database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		})
	}
	var seen int
	if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ci WHERE id = $1`, orgWideCI).Scan(&seen)
	}); err != nil || seen != 1 {
		t.Fatalf("setup: client-1 principal sees the org-wide CI %d times (err %v), want 1", seen, err)
	}

	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{"edge own -> org-wide", `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
			[]any{f.OrgA, own, orgWideCI}},
		{"edge org-wide -> own", `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
			[]any{f.OrgA, orgWideCI, own}},
		{"suppression own -> org-wide", `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id) VALUES ($1, $2, $3, $4)`,
			[]any{f.OrgA, own, orgWideCI, typeID}},
		{"interface on the org-wide CI", `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'e62-eth0')`,
			[]any{f.OrgA, orgWideCI}},
	} {
		if err := inClient1(tc.query, tc.args...); !rlsDenied(err) {
			t.Errorf("E-62 option 1: client-1 principal, %s: err %v, want 42501 row-level security", tc.name, err)
		}
	}

	scoped := w.as("client1", only(f.Client1), all())
	for _, body := range []map[string]any{
		{"source_ci_id": own, "target_ci_id": orgWideCI, "rel_type": "depends_on"},
		{"source_ci_id": orgWideCI, "target_ci_id": own, "rel_type": "depends_on"},
	} {
		if code, resp := w.do(scoped, http.MethodPost, "/api/v1/relationships", body); !refused(code) {
			t.Errorf("E-62 option 1: POST /api/v1/relationships %v as client1: %d %s, want 403/404", body, code, trim(resp))
		}
	}
	if n := w.scalar(`SELECT count(*)::text FROM ci_relationship WHERE source_ci_id IN ($1, $2) OR target_ci_id IN ($1, $2)`, own, orgWideCI); n != "0" {
		t.Fatalf("refused writes left %s edges", n)
	}

	// Control: the org-wide principal creates the edge; it carries NULL as
	// target client and the client-1 principal reads it.
	edge := w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": own, "target_ci_id": orgWideCI, "rel_type": "depends_on"})
	if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') || '>' || COALESCE(target_client_id::text, '') FROM ci_relationship WHERE id = $1`, edge); got != f.Client1+">" {
		t.Errorf("org-wide edge scope = %s, want client 1 > NULL", got)
	}
	if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ci_relationship WHERE id = $1`, edge).Scan(&seen)
	}); err != nil || seen != 1 {
		t.Errorf("client-1 principal sees the edge to the org-wide CI %d times (err %v), want 1", seen, err)
	}
}
