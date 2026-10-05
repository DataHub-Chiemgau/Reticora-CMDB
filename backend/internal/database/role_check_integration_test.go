package database_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// asOwner opens a transaction on the maintenance pool, runs prepare as the
// (superuser) login role, switches to the application role and from there to
// the owner role as the runtime DDL path does, and sets the org-wide tenant
// context of org. The transaction is rolled back at the end of the test.
func asOwner(t *testing.T, f *scopetest.Fixture, org string, prepare ...string) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	for _, stmt := range append(prepare, "SET LOCAL ROLE "+database.DefaultAppRole, "SET LOCAL ROLE "+database.DefaultOwnerRole) {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
		set_config('app.client_scope', '', true), set_config('app.site_scope', '', true), set_config('app.team_scope', '', true)`, org); err != nil {
		t.Fatal(err)
	}
	return tx
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestForceRowLevelSecurityBindsOwner covers WP-064 (TEN-03): the tables
// belong to reticora_owner, and FORCE ROW LEVEL SECURITY applies the tenant
// policies to it as to the application role. Without FORCE the owner would
// see every organization, which the second case shows as the contrast.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestForceRowLevelSecurityBindsOwner(t *testing.T) {
	f := scopetest.Seed(t, "60")
	ctx := context.Background()
	if _, err := f.Admin.Exec(ctx, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'B', 'scopetest-60-b1')`, f.ID(), f.OrgB); err != nil {
		t.Fatal(err)
	}
	count := func(tx pgx.Tx, org string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM client WHERE organization_id = $1`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	tx := asOwner(t, f, f.OrgA)
	var owner, current string
	if err := tx.QueryRow(ctx, `SELECT pg_get_userbyid(relowner), current_user FROM pg_class WHERE oid = 'client'::regclass`).Scan(&owner, &current); err != nil {
		t.Fatal(err)
	}
	if owner != database.DefaultOwnerRole || current != database.DefaultOwnerRole {
		t.Fatalf("client owned by %q, running as %q; want both %q", owner, current, database.DefaultOwnerRole)
	}
	if a, b := count(tx, f.OrgA), count(tx, f.OrgB); a != 2 || b != 0 {
		t.Errorf("owner under FORCE sees %d clients of its organization and %d of another, want 2 and 0", a, b)
	}
	_, err := tx.Exec(ctx, `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'x', 'scopetest-60-x')`, f.OrgB)
	if sqlState(err) != "42501" {
		t.Errorf("owner insert into another organization: %v, want a row level security violation", err)
	}

	// Contrast: the same owner without FORCE bypasses the policies.
	tx = asOwner(t, f, f.OrgA, `ALTER TABLE client NO FORCE ROW LEVEL SECURITY`)
	if b := count(tx, f.OrgB); b != 1 {
		t.Errorf("owner without FORCE sees %d clients of another organization, want 1 (no policy applies)", b)
	}
}

// TestRuntimeIndexDDL covers WP-064 (CH19, DB-04 in part): an application
// connection creates an index concurrently, outside a transaction, after an
// explicit SET ROLE to the owner role; without it, it has no DDL rights, and
// the owner role may only run index DDL.
func TestRuntimeIndexDDL(t *testing.T) {
	f := scopetest.Seed(t, "60")
	ctx := context.Background()
	t.Cleanup(func() { _, _ = f.Admin.Exec(ctx, `DROP INDEX IF EXISTS idx_attr_wp064_probe`) })

	conn, err := f.App.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	const create = `CREATE INDEX CONCURRENTLY idx_attr_wp064_probe ON ci ((attributes->>'wp064_probe'))`
	if _, err = conn.Exec(ctx, create); sqlState(err) != "42501" {
		t.Errorf("index DDL without SET ROLE: %v, want insufficient privilege", err)
	}
	if _, err = conn.Exec(ctx, "SET ROLE "+database.DefaultOwnerRole); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, resetErr := conn.Exec(ctx, "SET ROLE "+database.DefaultAppRole); resetErr != nil {
			t.Errorf("reset role: %v", resetErr)
			conn.Conn().Close(ctx)
		}
	}()
	if _, err = conn.Exec(ctx, create); err != nil {
		t.Fatalf("index DDL as owner: %v", err)
	}
	if _, err = conn.Exec(ctx, `DROP INDEX CONCURRENTLY idx_attr_wp064_probe`); err != nil {
		t.Fatalf("drop index as owner: %v", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE ci NO FORCE ROW LEVEL SECURITY`,
		`ALTER TABLE ci DISABLE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS ci_isolation ON ci`,
		`CREATE TABLE public.wp064_probe (id int)`,
		`GRANT SELECT ON ci TO PUBLIC`,
	} {
		if _, err = conn.Exec(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("%s as owner: %v, want insufficient privilege", stmt, err)
		}
	}
}

// TestRoleContractStartupCheck covers WP-064 (TEN-03): the migrated schema
// satisfies the role contract, and each deviation of the effective rights,
// inherited ones included, is reported so that NewPool refuses to start.
func TestRoleContractStartupCheck(t *testing.T) {
	f := scopetest.Seed(t, "60")
	ctx := context.Background()
	if err := database.VerifyRoleContract(ctx, f.App, database.DefaultOwnerRole); err != nil {
		t.Fatalf("migrated schema: %v", err)
	}

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`ALTER TABLE ci NO FORCE ROW LEVEL SECURITY`,
		`ALTER TABLE asset DISABLE ROW LEVEL SECURITY`,
		`ALTER TABLE ticket OWNER TO CURRENT_USER`,
		`GRANT reticora_owner TO reticora_app WITH INHERIT TRUE`,
		`GRANT CREATE ON SCHEMA public TO reticora_app`,
		`CREATE ROLE reticora_wp064_su NOLOGIN SUPERUSER`,
		`GRANT reticora_wp064_su TO reticora_owner WITH INHERIT FALSE`,
		`SET LOCAL ROLE reticora_app`,
	} {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = database.VerifyRoleContract(ctx, tx, database.DefaultOwnerRole)
	var contractErr *database.RoleContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("deviations not reported: %v", err)
	}
	if contractErr.Role != database.DefaultAppRole {
		t.Errorf("role %q, want %q", contractErr.Role, database.DefaultAppRole)
	}
	for _, want := range []string{
		"role reticora_wp064_su (reachable from reticora_app via SET ROLE) is SUPERUSER",
		"reticora_app inherits the privileges of reticora_owner; DDL rights must need an explicit SET ROLE",
		"reticora_app has CREATE on schema public outside of reticora_owner",
		"tenant table ci has no FORCE ROW LEVEL SECURITY",
		"tenant table asset has no ENABLE ROW LEVEL SECURITY",
		"tenant table ticket is owned by ", // the login role
	} {
		if !slices.ContainsFunc(contractErr.Violations, func(v string) bool { return strings.HasPrefix(v, want) }) {
			t.Errorf("violation %q not reported; got:\n%v", want, contractErr.Violations)
		}
	}

	// A missing owner role (schema before migration 000078) is a deviation.
	if err = database.VerifyRoleContract(ctx, tx, "reticora_wp064_missing"); !errors.As(err, &contractErr) ||
		!slices.Contains(contractErr.Violations, "owner role reticora_wp064_missing does not exist") {
		t.Errorf("missing owner role: %v", err)
	}
}
