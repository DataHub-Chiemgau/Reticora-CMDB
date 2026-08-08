package database_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// The RLS policies only apply to roles without SUPERUSER/BYPASSRLS. The
// CI database superuser would silently bypass them, so every assertion runs
// inside a transaction executed as a dedicated unprivileged role.
const rlsTestRole = "reticora_rls_test"

// setupRLSTestRole creates (idempotently) an unprivileged role with DML
// grants on the migrated schema so row-level security is actually enforced,
// and allows the migration owner to assume it via SET ROLE.
func setupRLSTestRole(t *testing.T, dsn string) {
	t.Helper()
	db := openAdmin(t, dsn)
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+rlsTestRole+`') THEN
				CREATE ROLE `+rlsTestRole+` NOLOGIN NOSUPERUSER NOBYPASSRLS;
			END IF;
		END
		$$`); err != nil {
		t.Fatalf("create test role: %v", err)
	}
	// The DSN user (migration owner) must be able to assume the test role.
	// quote_ident protects against usernames containing SQL metacharacters.
	var grantStmt string
	if err := db.QueryRowContext(ctx,
		"SELECT format('GRANT %s TO %s', quote_ident($1), quote_ident(current_user))", rlsTestRole,
	).Scan(&grantStmt); err != nil {
		t.Fatalf("build grant statement: %v", err)
	}
	if _, err := db.ExecContext(ctx, grantStmt); err != nil {
		t.Fatalf("grant test role: %v", err)
	}
	for _, stmt := range []string{
		"GRANT USAGE ON SCHEMA public TO " + rlsTestRole,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + rlsTestRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + rlsTestRole,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("grant privileges: %v", err)
		}
	}
}

// withRLS runs fn inside a transaction as the unprivileged test role with the
// tenant GUCs set, then rolls back so tests stay hermetic.
func withRLS(t *testing.T, dsn, orgID, clientScope string, fn func(tx *sql.Tx)) {
	t.Helper()
	db := openAdmin(t, dsn)
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+rlsTestRole); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	if clientScope != "" {
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.client_scope', $1, true)", clientScope); err != nil {
			t.Fatalf("set app.client_scope: %v", err)
		}
	}
	fn(tx)
}

func openAdmin(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return db
}

// TestTenantIsolationRLS asserts that rows of one organization are invisible
// and unwritable for another organization under RLS, for representative
// tables (ci, subnet/ipam, ticket).
func TestTenantIsolationRLS(t *testing.T) {
	dsn := testDatabaseURL(t)
	setupRLSTestRole(t, dsn)

	var (
		orgA    = "a0000000-0000-4000-8000-000000000001"
		orgB    = "b0000000-0000-4000-8000-000000000002"
		ciTypeA = "c1000000-0000-4000-8000-000000000001"
	)

	// Seed data as the (superuser) migration owner so the inserts are not
	// subject to RLS; the assertions below read it back under RLS.
	seed := openAdmin(t, dsn)
	defer seed.Close()
	ctx := context.Background()

	cleanupOrg := func() {
		c := context.Background()
		// ticket.organization_id lacks ON DELETE CASCADE, so remove
		// dependent rows explicitly before dropping the organizations.
		_, _ = seed.ExecContext(c, `DELETE FROM ticket WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM organization WHERE id IN ($1, $2)`, orgA, orgB)
	}
	// Idempotent re-runs: drop leftovers from a previous failed run first.
	cleanupOrg()
	t.Cleanup(cleanupOrg)

	if _, err := seed.ExecContext(ctx, `INSERT INTO organization (id, name, slug) VALUES
		($1, 'RLS Org A', 'rls-org-a'), ($2, 'RLS Org B', 'rls-org-b')`, orgA, orgB); err != nil {
		t.Fatalf("seed orgs: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci_type (id, organization_id, name) VALUES
		($1, $2, 'Server') ON CONFLICT (id) DO NOTHING`, ciTypeA, orgA); err != nil {
		t.Fatalf("seed ci_type: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci (organization_id, ci_type_id, name)
		VALUES ($1, $2, 'rls-ci-a')`, orgA, ciTypeA); err != nil {
		t.Fatalf("seed ci: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO subnet (organization_id, cidr, name)
		VALUES ($1, '10.250.0.0/24', 'rls-subnet-a')`, orgA); err != nil {
		t.Fatalf("seed subnet: %v", err)
	}
	var userID string
	if err := seed.QueryRowContext(ctx, `INSERT INTO app_user (organization_id, email, display_name)
		VALUES ($1, 'rls-a@example.com', 'RLS A') RETURNING id::text`, orgA).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ticket (organization_id, title, reporter_id)
		VALUES ($1, 'rls-ticket-a', $2)`, orgA, userID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	// Wrong org sees zero rows on every representative table.
	withRLS(t, dsn, orgB, "", func(tx *sql.Tx) {
		for _, table := range []string{"ci", "subnet", "ticket"} {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
				t.Fatalf("count %s as org B: %v", table, err)
			}
			if n != 0 {
				t.Fatalf("org B must not see org A rows in %s, got %d", table, n)
			}
		}
	})

	// Right org sees its own rows.
	withRLS(t, dsn, orgA, "", func(tx *sql.Tx) {
		for _, table := range []string{"ci", "subnet", "ticket"} {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
				t.Fatalf("count %s as org A: %v", table, err)
			}
			if n != 1 {
				t.Fatalf("org A must see exactly its own row in %s, got %d", table, n)
			}
		}
	})

	// Cross-tenant writes are rejected by the WITH CHECK clause.
	withRLS(t, dsn, orgB, "", func(tx *sql.Tx) {
		_, err := tx.Exec(`INSERT INTO ci (organization_id, ci_type_id, name) VALUES ($1, $2, 'rls-cross')`, orgA, ciTypeA)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Fatalf("expected RLS violation inserting into org A as org B, got: %v", err)
		}
	})
}

// TestClientScopeRLS asserts app.client_scope restricts client-owned rows:
// a scoped principal sees only its own client's rows plus shared (NULL)
// rows, while an unscoped org-wide principal sees everything.
func TestClientScopeRLS(t *testing.T) {
	dsn := testDatabaseURL(t)
	setupRLSTestRole(t, dsn)

	var (
		orgID   = "a0000000-0000-4000-8000-0000000000c1"
		clientA = "a0000000-0000-4000-8000-00000000c1a1"
		clientB = "a0000000-0000-4000-8000-00000000c1b1"
		ciType  = "c1000000-0000-4000-8000-0000000000c1"
	)

	seed := openAdmin(t, dsn)
	defer seed.Close()
	ctx := context.Background()

	cleanupOrg := func() {
		_, _ = seed.ExecContext(context.Background(), `DELETE FROM organization WHERE id = $1`, orgID)
	}
	// Idempotent re-runs: drop leftovers from a previous failed run first.
	cleanupOrg()
	t.Cleanup(cleanupOrg)

	if _, err := seed.ExecContext(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'RLS Scope Org', 'rls-scope-org')`, orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO client (id, organization_id, name, slug) VALUES
		($1, $3, 'Scope Client A', 'scope-client-a'), ($2, $3, 'Scope Client B', 'scope-client-b')
		ON CONFLICT (id) DO NOTHING`, clientA, clientB, orgID); err != nil {
		t.Fatalf("seed clients: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci_type (id, organization_id, name) VALUES ($1, $2, 'ScopeType')
		ON CONFLICT (id) DO NOTHING`, ciType, orgID); err != nil {
		t.Fatalf("seed ci_type: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci (organization_id, client_id, ci_type_id, name) VALUES
		($1, $2, $4, 'scope-ci-a'), ($1, $3, $4, 'scope-ci-b'), ($1, NULL, $4, 'scope-ci-shared')`,
		orgID, clientA, clientB, ciType); err != nil {
		t.Fatalf("seed cis: %v", err)
	}

	// Org-wide (no client scope): all three rows.
	withRLS(t, dsn, orgID, "", func(tx *sql.Tx) {
		assertCINames(t, tx, []string{"scope-ci-a", "scope-ci-b", "scope-ci-shared"})
	})

	// Scoped to client A: own row + shared row only.
	withRLS(t, dsn, orgID, clientA, func(tx *sql.Tx) {
		assertCINames(t, tx, []string{"scope-ci-a", "scope-ci-shared"})
	})

	// Scoped to client B: its row + shared row only.
	withRLS(t, dsn, orgID, clientB, func(tx *sql.Tx) {
		assertCINames(t, tx, []string{"scope-ci-b", "scope-ci-shared"})
	})

	// The client table itself is scoped by its own id.
	withRLS(t, dsn, orgID, clientA, func(tx *sql.Tx) {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM client").Scan(&n); err != nil {
			t.Fatalf("count client: %v", err)
		}
		if n != 1 {
			t.Fatalf("client A scope must see exactly its own client row, got %d", n)
		}
	})

	// Cross-client writes are rejected by the WITH CHECK clause.
	withRLS(t, dsn, orgID, clientA, func(tx *sql.Tx) {
		_, err := tx.Exec(`INSERT INTO ci (organization_id, client_id, ci_type_id, name) VALUES ($1, $2, $3, 'scope-hack')`,
			orgID, clientB, ciType)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Fatalf("expected RLS violation inserting client B row as client A scope, got: %v", err)
		}
	})
}

func assertCINames(t *testing.T, tx *sql.Tx, want []string) {
	t.Helper()
	rows, err := tx.Query("SELECT name FROM ci ORDER BY name")
	if err != nil {
		t.Fatalf("query ci: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan ci: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ci: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}
