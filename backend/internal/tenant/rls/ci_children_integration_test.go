package rls_test

import (
	"context"
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
			`INSERT INTO ip_address (organization_id, interface_id, address, status) VALUES ($1, $2, '10.42.0.1', 'active')`,
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
