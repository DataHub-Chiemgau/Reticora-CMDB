package composition

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// Regression test for WP-052 (E-26): migration 000056 used to delete
// composition and ci_relationship rows whose references cross tenants before
// adding the composite foreign keys. It now moves them into
// migration_quarantine, and its down migration moves them back.
//
// The test runs the real SQL of both migration files against inconsistent
// rows. It works inside one transaction that is always rolled back: it first
// undoes the constraints and the quarantine table of the migrated schema,
// seeds the inconsistent rows, then executes the migration sections.

const (
	quarOrgA   = "dddd4444-0000-4000-8000-000000000001"
	quarOrgB   = "dddd4444-0000-4000-8000-000000000002"
	quarTypeA  = "dddd4444-0000-4000-8000-0000000000e1"
	quarTypeB  = "dddd4444-0000-4000-8000-0000000000e2"
	quarCIA    = "dddd4444-0000-4000-8000-0000000000c1"
	quarCIB    = "dddd4444-0000-4000-8000-0000000000c2"
	quarAssetA = "dddd4444-0000-4000-8000-0000000000a1"
	quarAssetB = "dddd4444-0000-4000-8000-0000000000a2"
	quarCompOK = "dddd4444-0000-4000-8000-0000000000f1"
	quarComp   = "dddd4444-0000-4000-8000-0000000000f2"
	quarRelOK  = "dddd4444-0000-4000-8000-0000000000b1"
	quarRel    = "dddd4444-0000-4000-8000-0000000000b2"
)

// migrationSection returns the text of a migration file from the line that
// starts with begin up to, but excluding, the line that starts with end ("" =
// end of file).
func migrationSection(t *testing.T, file, begin, end string) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/" + file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	text := string(raw)
	i := strings.Index(text, "\n"+begin)
	if i < 0 {
		t.Fatalf("%s: section start %q not found", file, begin)
	}
	text = text[i+1:]
	if end != "" {
		j := strings.Index(text, "\n"+end)
		if j < 0 {
			t.Fatalf("%s: section end %q not found", file, end)
		}
		text = text[:j+1]
	}
	return text
}

func TestMigration56QuarantinesInconsistentRows(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	exec := func(step, sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", step, execErr)
		}
	}

	// Schema state just before the tenant-reference section of 000056.
	exec("undo 000056 references", `
		ALTER TABLE composition DROP CONSTRAINT composition_child_asset_tenant_fkey;
		ALTER TABLE composition DROP CONSTRAINT composition_child_ci_tenant_fkey;
		ALTER TABLE composition DROP CONSTRAINT composition_parent_tenant_fkey;
		ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_target_tenant_fkey;
		ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_source_tenant_fkey;
		ALTER TABLE asset DROP CONSTRAINT asset_id_organization_key;
		ALTER TABLE ci DROP CONSTRAINT ci_id_organization_key;
		DROP TABLE migration_quarantine;`)

	// Org A owns one consistent composition and relationship each, plus one
	// composition and one relationship that point into org B.
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Quarantine A', 'quarantine-a'), ($2, 'Quarantine B', 'quarantine-b')`, []any{quarOrgA, quarOrgB}},
		{`INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1, $2, 'quarantine-server', 'Server'), ($3, $4, 'quarantine-server', 'Server')`, []any{quarTypeA, quarOrgA, quarTypeB, quarOrgB}},
		{`INSERT INTO ci (id, organization_id, ci_type_id, name) VALUES ($1, $2, $3, 'ci-a'), ($4, $5, $6, 'ci-b')`, []any{quarCIA, quarOrgA, quarTypeA, quarCIB, quarOrgB, quarTypeB}},
		{`INSERT INTO asset (id, organization_id, asset_tag, name) VALUES ($1, $2, 'QUAR-A', 'asset-a'), ($3, $4, 'QUAR-B', 'asset-b')`, []any{quarAssetA, quarOrgA, quarAssetB, quarOrgB}},
		{`INSERT INTO composition (id, organization_id, parent_asset_id, child_ci_id, role) VALUES ($1, $2, $3, $4, 'ok'), ($5, $2, $3, $6, 'cross-tenant')`, []any{quarCompOK, quarOrgA, quarAssetA, quarCIA, quarComp, quarCIB}},
		{`INSERT INTO ci_relationship (id, organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, $3, 'runs_on'), ($4, $2, $3, $5, 'runs_on')`, []any{quarRelOK, quarOrgA, quarCIA, quarRel, quarCIB}},
	} {
		exec("seed", s.sql, s.args...)
	}

	exec("000056 up", migrationSection(t, "000056_rls_enforcement.up.sql",
		"-- ─── Tenant-consistent references", "-- ─── Stable CI type keys"))

	var quarantined []string
	rows, err := tx.Query(ctx, `
		SELECT source_table || ':' || (row_data->>'id') || ':' || organization_id
		FROM migration_quarantine WHERE migration = '000056' ORDER BY source_table`)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	quarantined, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect quarantine: %v", err)
	}
	want := []string{
		"ci_relationship:" + quarRel + ":" + quarOrgA,
		"composition:" + quarComp + ":" + quarOrgA,
	}
	if strings.Join(quarantined, " ") != strings.Join(want, " ") {
		t.Fatalf("quarantine = %v, want %v", quarantined, want)
	}

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM composition WHERE id IN ($1, $2)`, quarCompOK, quarComp); n != 1 {
		t.Fatalf("composition rows after 000056 = %d, want only the consistent one", n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id IN ($1, $2)`, quarRelOK, quarRel); n != 1 {
		t.Fatalf("relationship rows after 000056 = %d, want only the consistent one", n)
	}
	// The quarantined row keeps every column value.
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE row_data->>'role' = 'cross-tenant' AND row_data->>'parent_asset_id' = $1`, quarAssetA); n != 1 {
		t.Fatal("quarantined composition row lost its column values")
	}

	// Down: drop the composite references again and move the rows back.
	exec("000056 down", migrationSection(t, "000056_rls_enforcement.down.sql",
		"ALTER TABLE composition DROP CONSTRAINT IF EXISTS composition_child_asset_tenant_fkey;", "DROP TRIGGER IF EXISTS trg_audit_log_no_update"))
	if n := count(`SELECT count(*) FROM composition WHERE id = $1 AND role = 'cross-tenant' AND child_ci_id = $2`, quarComp, quarCIB); n != 1 {
		t.Fatal("down migration did not restore the quarantined composition row")
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id = $1 AND target_ci_id = $2`, quarRel, quarCIB); n != 1 {
		t.Fatal("down migration did not restore the quarantined relationship row")
	}
	if n := count(`SELECT count(*) FROM pg_tables WHERE tablename = 'migration_quarantine'`); n != 0 {
		t.Fatal("down migration left migration_quarantine behind")
	}
}
