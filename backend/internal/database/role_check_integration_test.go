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

// TestRoleContractStartupCheck covers WP-064 and WP-352 (TEN-03, F-057): the
// migrated schema with the runtime login satisfies the role contract, and
// each deviation of the effective rights, inherited ones included, is
// reported so that NewPool refuses to start. The superuser login of
// TEST_DATABASE_URL is such a deviation even after SET ROLE reticora_app: a
// RESET ROLE hands the session every right back.
func TestRoleContractStartupCheck(t *testing.T) {
	f := scopetest.Seed(t, "60")
	ctx := context.Background()
	if err := database.VerifyRoleContract(ctx, f.App, database.DefaultOwnerRole); err != nil {
		t.Fatalf("migrated schema with the runtime login: %v", err)
	}

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var login string
	var loginSuper bool
	if err = tx.QueryRow(ctx, `SELECT session_user::text, rolsuper FROM pg_roles WHERE rolname = session_user`).Scan(&login, &loginSuper); err != nil {
		t.Fatal(err)
	}
	if !loginSuper {
		t.Fatalf("setup: the login of TEST_DATABASE_URL (%s) is not a superuser", login)
	}
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
	if contractErr.Role != database.DefaultAppRole || contractErr.SessionUser != login {
		t.Errorf("role %q, session user %q; want %q, %q", contractErr.Role, contractErr.SessionUser, database.DefaultAppRole, login)
	}
	for _, want := range []string{
		"session user " + login + " is SUPERUSER", // the superuser login is refused, not accepted
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

// TestOwnerCannotTruncateTenantTables covers WP-352 (TEN-03, CH19, DB-04 in
// part; F-192, probe truncate_probe.sql): TRUNCATE ignores row level
// security, and reticora_owner owns the tenant tables. The guard trigger of
// migration 000144 refuses TRUNCATE to reticora_app and to reticora_owner
// reached by SET ROLE, also on the hypertable metric_sample and the
// continuous aggregate metric_sample_1h; the owner cannot drop or disable the
// guard; VerifyRoleContract reports a tenant table without it and a login
// that may truncate past it.
func TestOwnerCannotTruncateTenantTables(t *testing.T) {
	f := scopetest.Seed(t, "60")
	ctx := context.Background()
	if _, err := f.Admin.Exec(ctx, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'B', 'scopetest-60-tb1')`, f.ID(), f.OrgB); err != nil {
		t.Fatal(err)
	}
	clients := func() (a, b int) {
		t.Helper()
		if err := f.Admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE organization_id = $1), count(*) FILTER (WHERE organization_id = $2) FROM client`,
			f.OrgA, f.OrgB).Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	beforeA, beforeB := clients()
	if beforeA == 0 || beforeB == 0 {
		t.Fatalf("setup: %d clients of org A, %d of org B", beforeA, beforeB)
	}

	// Probe truncate_probe.sql on the runtime login: as reticora_app TRUNCATE
	// lacks the privilege; after SET ROLE reticora_owner the owner may read
	// and delete only the rows of its organization, and TRUNCATE is refused
	// by the guard.
	conn, err := f.App.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `TRUNCATE client`); sqlState(err) != "42501" {
		t.Errorf("TRUNCATE as %s: %v, want insufficient privilege", database.DefaultAppRole, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"SET LOCAL ROLE " + database.DefaultOwnerRole,
		"SELECT set_config('app.org_id', '" + f.OrgA + "', true)",
	} {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var visibleB int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM client WHERE organization_id = $1`, f.OrgB).Scan(&visibleB); err != nil || visibleB != 0 {
		t.Errorf("owner sees %d clients of another organization (%v), want 0", visibleB, err)
	}
	_, err = tx.Exec(ctx, `TRUNCATE client CASCADE`)
	if sqlState(err) != "42501" || !strings.Contains(err.Error(), "may not truncate tenant table") {
		t.Errorf("TRUNCATE as %s: %v, want the guard's insufficient privilege", database.DefaultOwnerRole, err)
	}
	_ = tx.Rollback(ctx)
	if a, b := clients(); a != beforeA || b != beforeB {
		t.Errorf("after the refused TRUNCATE %d/%d clients of org A/B remain, want %d/%d", a, b, beforeA, beforeB)
	}

	// The owner cannot remove or switch off the guard.
	for _, stmt := range []string{
		`DROP TRIGGER ` + database.TruncateGuardTrigger + ` ON client`,
		`ALTER TABLE client DISABLE TRIGGER ` + database.TruncateGuardTrigger,
		`ALTER TABLE client DISABLE TRIGGER ALL`,
		`CREATE OR REPLACE FUNCTION ` + database.TruncateGuardTrigger + `() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NULL; END$$`,
	} {
		ownerTx := asOwner(t, f, f.OrgA)
		if _, err = ownerTx.Exec(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("%s as owner: %v, want insufficient privilege", stmt, err)
		}
		_ = ownerTx.Rollback(ctx) // frees the connection and the table lock at once
	}

	// TimescaleDB accepts the statement-level trigger on the hypertable and
	// on the materialization hypertable of the continuous aggregate; neither
	// application role holds a privilege on them (view barrier, WP-040), so
	// the guard is shown with a TRUNCATE privilege granted for the test.
	for _, table := range []string{"metric_sample", "metric_sample_1h"} {
		var mat string
		if err = f.Admin.QueryRow(ctx, `
			SELECT COALESCE((SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
			                   FROM timescaledb_information.continuous_aggregates
			                  WHERE view_schema = 'public' AND view_name = $1), $1)`, table).Scan(&mat); err != nil {
			t.Fatal(err)
		}
		ownerTx := asOwner(t, f, f.OrgA, `GRANT TRUNCATE ON `+table+`, `+mat+` TO `+database.DefaultOwnerRole)
		_, err = ownerTx.Exec(ctx, `TRUNCATE `+table)
		if sqlState(err) != "42501" || !strings.Contains(err.Error(), "may not truncate tenant table") {
			t.Errorf("TRUNCATE %s as owner with TRUNCATE privilege: %v, want the guard's insufficient privilege", table, err)
		}
		_ = ownerTx.Rollback(ctx)
	}

	// A tenant table without the guard or with the guard disabled, and a
	// login that may truncate past the guard, are contract deviations.
	tx, err = f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`DROP TRIGGER ` + database.TruncateGuardTrigger + ` ON ticket`,
		`ALTER TABLE asset DISABLE TRIGGER ` + database.TruncateGuardTrigger,
		`SET LOCAL ROLE ` + database.DefaultAppRole,
	} {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = database.VerifyRoleContract(ctx, tx, database.DefaultOwnerRole)
	var contractErr *database.RoleContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("missing guards not reported: %v", err)
	}
	for _, want := range []string{
		"tenant table ticket has no enabled statement-level BEFORE TRUNCATE trigger " + database.TruncateGuardTrigger,
		"tenant table asset has no enabled statement-level BEFORE TRUNCATE trigger " + database.TruncateGuardTrigger,
	} {
		if !slices.Contains(contractErr.Violations, want) {
			t.Errorf("violation %q not reported; got:\n%v", want, contractErr.Violations)
		}
	}
	for _, v := range contractErr.Violations {
		if strings.Contains(v, "TRUNCATE trigger") && !strings.HasPrefix(v, "tenant table ticket ") && !strings.HasPrefix(v, "tenant table asset ") {
			t.Errorf("guarded table reported: %s", v)
		}
	}
	_ = tx.Rollback(ctx)

	dsn := t1LoginRole(t, f, "audit_t1_wp352_truncate", "NOBYPASSRLS")
	if _, err = f.Admin.Exec(ctx, `GRANT TRUNCATE ON client TO audit_t1_wp352_truncate`); err != nil {
		t.Fatal(err)
	}
	// Runs before the drop of the login, which the grant would block.
	t.Cleanup(func() { _, _ = f.Admin.Exec(ctx, `REVOKE TRUNCATE ON client FROM audit_t1_wp352_truncate`) })
	pool, err := database.NewPool(ctx, dsn)
	if err == nil {
		pool.Close()
		t.Fatal("NewPool accepted a login with TRUNCATE on a tenant table")
	}
	if !errors.As(err, &contractErr) || !slices.Contains(contractErr.Violations,
		"session user audit_t1_wp352_truncate may TRUNCATE tenant table client; the guard only stops reticora_app and reticora_owner") {
		t.Errorf("login with TRUNCATE on client: %v", err)
	}
}
