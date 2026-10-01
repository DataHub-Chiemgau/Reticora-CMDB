package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids of this file; distinct from the other integration tests that
// share the database.
const (
	scopeOrgA    = "c0c0a000-0000-4000-8000-000000000001"
	scopeOrgB    = "c0c0b000-0000-4000-8000-000000000002"
	scopeClient1 = "c0c0a000-0000-4000-8000-0000000000c1"
	scopeClient2 = "c0c0a000-0000-4000-8000-0000000000c2"
	scopeUser    = "c0c0a000-0000-4000-8000-0000000000f1"
)

var scopeGUCs = []string{
	database.OrgGUC, database.UserGUC, database.ClientScopeGUC, database.SiteScopeGUC, database.TeamScopeGUC,
}

// seedScopeFixture creates organization A with two clients and an empty
// organization B through a maintenance connection, which is not subject to
// RLS, and returns a pool through the production constructor.
func seedScopeFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := testDatabaseURL(t)
	ctx := context.Background()

	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cleanup := func() {
		// client rows go with their organization (ON DELETE CASCADE).
		if _, delErr := admin.Exec(ctx, `DELETE FROM organization WHERE id IN ($1::uuid, $2::uuid)`, scopeOrgA, scopeOrgB); delErr != nil {
			t.Errorf("cleanup: %v", delErr)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Scope Org A', 'tenant-scope-org-a')`, []any{scopeOrgA}},
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Scope Org B', 'tenant-scope-org-b')`, []any{scopeOrgB}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 1', 'scope-client-1')`, []any{scopeClient1, scopeOrgA}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 2', 'scope-client-2')`, []any{scopeClient2, scopeOrgA}},
	} {
		if _, seedErr := admin.Exec(ctx, s.sql, s.args...); seedErr != nil {
			t.Fatalf("seed %q: %v", s.sql, seedErr)
		}
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func readGUCs(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (map[string]string, error) {
	values := make(map[string]string, len(scopeGUCs))
	for _, name := range scopeGUCs {
		var v *string
		if err := q.QueryRow(ctx, `SELECT current_setting($1, true)`, name).Scan(&v); err != nil {
			return nil, err
		}
		if v != nil {
			values[name] = *v
		}
	}
	return values, nil
}

// TestWithTenantSetsAllGUCs: every TEN-04 variable is set inside the
// transaction, with the restricted lists and the no-access marker.
func TestWithTenantSetsAllGUCs(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	scope := database.TenantScope{
		OrgID:   scopeOrgA,
		UserID:  scopeUser,
		Clients: database.ScopeIDs(scopeClient1, scopeClient2),
		Sites:   database.ScopeIDs(),
		Teams:   database.AllScopes(),
	}
	var got map[string]string
	err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = readGUCs(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	want := map[string]string{
		database.OrgGUC:         scopeOrgA,
		database.UserGUC:        scopeUser,
		database.ClientScopeGUC: scopeClient1 + "," + scopeClient2,
		database.SiteScopeGUC:   "00000000-0000-0000-0000-000000000000",
		database.TeamScopeGUC:   "",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
}

// TestWithTenantResetsGUCsOnPooledConnections: after commit and after a
// rolled-back transaction no tenant variable survives on any idle connection
// of the pool, i.e. on the connection that served the transaction.
func TestWithTenantResetsGUCsOnPooledConnections(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	scope := database.OrgWideScope(scopeOrgA, scopeUser)
	scope.Clients = database.ScopeIDs(scopeClient1)
	errAbort := errors.New("abort")

	for _, tc := range []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
		want error
	}{
		{"commit", func(context.Context, pgx.Tx) error { return nil }, nil},
		{"rollback", func(context.Context, pgx.Tx) error { return errAbort }, errAbort},
	} {
		if err := database.WithTenant(ctx, pool, &scope, tc.fn); !errors.Is(err, tc.want) {
			t.Fatalf("%s: WithTenant() = %v, want %v", tc.name, err, tc.want)
		}
		conns := pool.AcquireAllIdle(ctx)
		if len(conns) == 0 {
			t.Fatalf("%s: no idle connection to inspect", tc.name)
		}
		for _, c := range conns {
			values, err := readGUCs(ctx, c)
			c.Release()
			if err != nil {
				t.Fatalf("%s: read GUCs: %v", tc.name, err)
			}
			for name, v := range values {
				if v != "" {
					t.Errorf("%s: %s = %q survived the transaction", tc.name, name, v)
				}
			}
		}
	}
}

// TestWithTenantScopesRows checks the scope against the real client policy:
// an empty client scope sees nothing (E-08), a restricted scope sees its
// clients, the org-wide marker sees all, and another org sees 0 rows (TEN-06).
func TestWithTenantScopesRows(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	count := func(scope database.TenantScope) int {
		t.Helper()
		var n int
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM client`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("WithTenant: %v", err)
		}
		return n
	}
	withClients := func(org string, clients database.ScopeSet) database.TenantScope {
		s := database.OrgWideScope(org, scopeUser)
		s.Clients = clients
		return s
	}

	for _, tc := range []struct {
		name  string
		scope database.TenantScope
		want  int
	}{
		{"org-wide", database.OrgWideScope(scopeOrgA, scopeUser), 2},
		{"one client", withClients(scopeOrgA, database.ScopeIDs(scopeClient1)), 1},
		{"empty client scope fails closed", withClients(scopeOrgA, database.ScopeIDs()), 0},
		{"wrong org", database.OrgWideScope(scopeOrgB, scopeUser), 0},
		{"client of another org", withClients(scopeOrgB, database.ScopeIDs(scopeClient1)), 0},
	} {
		if got := count(tc.scope); got != tc.want {
			t.Errorf("%s: %d clients visible, want %d", tc.name, got, tc.want)
		}
	}
}

// TestWithTenantRejectsIncompleteScope: fn never runs without a complete scope.
func TestWithTenantRejectsIncompleteScope(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	err := database.WithTenant(ctx, pool, &database.TenantScope{OrgID: scopeOrgA}, func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run with an incomplete scope")
		return nil
	})
	if !errors.Is(err, database.ErrIncompleteScope) {
		t.Fatalf("WithTenant() = %v, want ErrIncompleteScope", err)
	}
}
