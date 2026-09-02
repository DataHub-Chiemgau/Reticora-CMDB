package rls_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// These tests run against a migrated PostgreSQL instance (see the migrations CI
// job). They are skipped unless TEST_DATABASE_URL is set.
//
// They are the regression tests for the tenant-isolation defect fixed by
// migration 000056: RLS policies existed but were inert, because the
// application connected as the schema owner (a superuser without FORCE ROW
// LEVEL SECURITY), so every policy was bypassed. The tests therefore assert the
// two halves of the fix separately:
//
//  1. the connection pool refuses to hand out an RLS-bypassing role, and
//  2. with a correctly scoped connection, no cross-tenant read or write of any
//     kind succeeds.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

const (
	orgA    = "aaaa1111-0000-4000-8000-000000000001"
	orgB    = "bbbb2222-0000-4000-8000-000000000002"
	typeA   = "aaaa1111-0000-4000-8000-00000000000a"
	typeB   = "bbbb2222-0000-4000-8000-00000000000b"
	ciA     = "aaaa1111-0000-4000-8000-0000000000c1"
	ciB     = "bbbb2222-0000-4000-8000-0000000000c2"
	assetA  = "aaaa1111-0000-4000-8000-0000000000d1"
	assetB  = "bbbb2222-0000-4000-8000-0000000000d2"
	relTypA = "aaaa1111-0000-4000-8000-0000000000e1"
)

// seed creates two organizations with one CI and one asset each, using a
// maintenance connection so the fixture itself is not subject to RLS.
func seed(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	// Registered before the row cleanup so it runs last (t.Cleanup is LIFO).
	t.Cleanup(admin.Close)

	// asset.organization_id is NO ACTION rather than ON DELETE CASCADE, so the
	// fixture tears its rows down explicitly, child tables first.
	// The append-only guards on audit_log and asset_movement deliberately block
	// deletion. Teardown runs under session_replication_role = replica, which
	// skips triggers for this session only and therefore cannot race with other
	// packages sharing the database.
	cleanup := func() {
		conn, err := admin.Acquire(ctx)
		if err != nil {
			t.Errorf("acquire cleanup connection: %v", err)
			return
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
			t.Errorf("lift append-only guards: %v", err)
			return
		}
		defer conn.Exec(ctx, `SET session_replication_role = origin`)
		for _, table := range []string{
			"asset_movement", "audit_log", "ci_relationship", "composition",
			"asset", "ci", "ci_type", "relationship_type", "organization",
		} {
			column := "organization_id"
			if table == "organization" {
				column = "id"
			}
			if _, err := conn.Exec(ctx,
				`DELETE FROM `+table+` WHERE `+column+` IN ($1::uuid, $2::uuid)`, orgA, orgB); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	}
	cleanup()

	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1,'RLS Isolation Org A','rls-isolation-org-a')`, []any{orgA}},
		{`INSERT INTO organization (id, name, slug) VALUES ($1,'RLS Isolation Org B','rls-isolation-org-b')`, []any{orgB}},
		{`INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1,$2,'rls-isolation-server','Server')`, []any{typeA, orgA}},
		{`INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1,$2,'rls-isolation-server','Server')`, []any{typeB, orgB}},
		{`INSERT INTO ci (id, organization_id, ci_type_id, name) VALUES ($1,$2,$3,'ci-a')`, []any{ciA, orgA, typeA}},
		{`INSERT INTO ci (id, organization_id, ci_type_id, name) VALUES ($1,$2,$3,'ci-b')`, []any{ciB, orgB, typeB}},
		{`INSERT INTO asset (id, organization_id, asset_tag, name) VALUES ($1,$2,'RLS-ISO-A','asset-a')`, []any{assetA, orgA}},
		{`INSERT INTO asset (id, organization_id, asset_tag, name) VALUES ($1,$2,'RLS-ISO-B','asset-b')`, []any{assetB, orgB}},
		{`INSERT INTO relationship_type (id, organization_id, key, forward_label, reverse_label) VALUES ($1,$2,'rls-isolation-runs-on','runs on','hosts')`, []any{relTypA, orgA}},
	}
	for _, s := range stmts {
		if _, err := admin.Exec(ctx, s.sql, s.args...); err != nil {
			cleanup()
			t.Fatalf("seed %q: %v", s.sql, err)
		}
	}
	t.Cleanup(cleanup)
}

// scoped opens a pool through the production constructor (which switches into
// the restricted application role and verifies RLS is enforced) and pins it to
// the given organization.
func scoped(t *testing.T, ctx context.Context, dsn, orgID string) *pgxpool.Pool {
	t.Helper()
	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("pool for org %s: %v", orgID, err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "SELECT set_config('app.org_id', $1, false)", orgID); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	return pool
}

// TestPoolRefusesRLSBypassingRole asserts the startup guard: a pool must not be
// handed out when the effective role can bypass row level security. Setting
// RETICORA_DB_APP_ROLE to an empty value disables the SET ROLE, which leaves
// the (superuser) login role in place in CI.
func TestPoolRefusesRLSBypassingRole(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()

	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	defer admin.Close()
	var super, bypass bool
	if err := admin.QueryRow(ctx,
		`SELECT COALESCE(rolsuper,false), COALESCE(rolbypassrls,false) FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass); err != nil {
		t.Fatalf("inspect login role: %v", err)
	}
	if !super && !bypass {
		t.Skip("login role already restricted; the bypass guard cannot be exercised")
	}

	t.Setenv(database.AppRoleEnv, "")
	pool, err := database.NewPool(ctx, dsn)
	if err == nil {
		pool.Close()
		t.Fatal("expected NewPool to refuse an RLS-bypassing role")
	}
}

// TestRowLevelSecurityBlocksCrossTenantAccess is the core isolation test: with
// app.org_id pinned to organization A, no read or write may reach organization
// B through any path.
func TestRowLevelSecurityBlocksCrossTenantAccess(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	seed(t, ctx, dsn)
	pool := scoped(t, ctx, dsn, orgA)

	t.Run("list is filtered", func(t *testing.T) {
		for _, table := range []string{"ci", "asset", "ci_type", "relationship_type"} {
			var foreign int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FROM `+table+` WHERE organization_id = $1`, orgB).Scan(&foreign); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if foreign != 0 {
				t.Errorf("%s: leaked %d rows of the other tenant", table, foreign)
			}
		}
	})

	t.Run("direct id lookup finds nothing", func(t *testing.T) {
		for _, tc := range []struct{ table, id string }{
			{"ci", ciB}, {"asset", assetB}, {"ci_type", typeB},
		} {
			var n int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FROM `+tc.table+` WHERE id = $1`, tc.id).Scan(&n); err != nil {
				t.Fatalf("lookup %s: %v", tc.table, err)
			}
			if n != 0 {
				t.Errorf("%s: direct id lookup exposed a foreign row", tc.table)
			}
		}
	})

	t.Run("cross tenant update affects no rows", func(t *testing.T) {
		tag, err := pool.Exec(ctx, `UPDATE ci SET name = 'hijacked' WHERE id = $1`, ciB)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("updated %d foreign rows", tag.RowsAffected())
		}
	})

	t.Run("cross tenant delete affects no rows", func(t *testing.T) {
		tag, err := pool.Exec(ctx, `DELETE FROM asset WHERE id = $1`, assetB)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("deleted %d foreign rows", tag.RowsAffected())
		}
	})

	t.Run("insert into another tenant is rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO ci (organization_id, ci_type_id, name) VALUES ($1,$2,'smuggled')`, orgB, typeB)
		if err == nil {
			t.Fatal("expected the WITH CHECK clause to reject a foreign-tenant insert")
		}
	})

	t.Run("relationship cannot span tenants", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			 VALUES ($1,$2,$3,'rls-isolation-runs-on')`, orgA, ciA, ciB)
		if err == nil {
			t.Fatal("expected a relationship pointing at a foreign CI to be rejected")
		}
	})

	t.Run("own tenant remains writable", func(t *testing.T) {
		tag, err := pool.Exec(ctx, `UPDATE ci SET name = 'ci-a-renamed' WHERE id = $1`, ciA)
		if err != nil {
			t.Fatalf("update own row: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("expected to update the own row, affected %d", tag.RowsAffected())
		}
	})
}

// TestAuditLogIsAppendOnly asserts that the application role cannot rewrite or
// erase audit history; the hash chain makes tampering detectable, these
// triggers make it impossible.
func TestAuditLogIsAppendOnly(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	seed(t, ctx, dsn)
	pool := scoped(t, ctx, dsn, orgA)

	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_log (organization_id, action, resource_type, resource_id)
		 VALUES ($1,'ci.created','ci',$2)`, orgA, ciA); err != nil {
		t.Fatalf("append audit entry: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'tampered' WHERE organization_id = $1`, orgA); err == nil {
		t.Fatal("expected audit_log UPDATE to be rejected")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE organization_id = $1`, orgA); err == nil {
		t.Fatal("expected audit_log DELETE to be rejected")
	}
}

// TestAssetMovementIsAppendOnly asserts the movement ledger cannot be rewritten
// so historical movements never disappear when the current location changes.
func TestAssetMovementIsAppendOnly(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	seed(t, ctx, dsn)
	pool := scoped(t, ctx, dsn, orgA)

	if _, err := pool.Exec(ctx,
		`INSERT INTO asset_movement (organization_id, asset_id, movement_type)
		 VALUES ($1,$2,'receipt')`, orgA, assetA); err != nil {
		t.Fatalf("record movement: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE asset_movement SET movement_type = 'disposal' WHERE organization_id = $1`, orgA); err == nil {
		t.Fatal("expected asset_movement UPDATE to be rejected")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM asset_movement WHERE organization_id = $1`, orgA); err == nil {
		t.Fatal("expected asset_movement DELETE to be rejected")
	}
}
