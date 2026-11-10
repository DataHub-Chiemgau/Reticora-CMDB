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
//
// Migration 000253 (WP-743) dropped composition; the tests create the table
// in the state of 000055 themselves (composition000055).
// TestMigration253DropSavesAndRestoresRows covers that drop: every row and
// every asset.parent_asset_id go to migration_quarantine, and its down
// migration brings table and rows back.

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

// composition000055 is the table composition as migration 000055 created it,
// the state before the composite foreign keys of 000056.
const composition000055 = `
	CREATE TABLE composition (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
		parent_asset_id UUID NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
		child_ci_id UUID REFERENCES ci(id) ON DELETE CASCADE,
		child_asset_id UUID REFERENCES asset(id) ON DELETE CASCADE,
		role TEXT,
		position TEXT,
		configuration_only BOOLEAN NOT NULL DEFAULT true,
		independently_serialized BOOLEAN NOT NULL DEFAULT false,
		independently_assignable BOOLEAN NOT NULL DEFAULT false,
		independently_locatable BOOLEAN NOT NULL DEFAULT false,
		independently_lifecycle_managed BOOLEAN NOT NULL DEFAULT false,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT composition_child_check CHECK (
			(child_ci_id IS NOT NULL AND child_asset_id IS NULL) OR
			(child_ci_id IS NULL AND child_asset_id IS NOT NULL)
		)
	);
	CREATE UNIQUE INDEX idx_composition_unique_child_ci ON composition(parent_asset_id, child_ci_id) WHERE child_ci_id IS NOT NULL;
	CREATE UNIQUE INDEX idx_composition_unique_child_asset ON composition(parent_asset_id, child_asset_id) WHERE child_asset_id IS NOT NULL;
	CREATE INDEX idx_composition_parent ON composition(parent_asset_id);`

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

// dropLaterReferences drops the foreign keys of later migrations that depend
// on the keys 000056 adds: ci_asset_id_fkey (000088), ci_parent_ci_fkey
// (000089), ci_merged_into_fkey (000108), ci_identity_mac_ci_fkey (000161)
// the references on ci and asset of migration 000194 (WP-838),
// ci_template_ci_fkey (000206), ci_tag_ci_fkey (000218), the CI and
// asset references of ticket_link (000230), the asset reference of the
// ledger stock_movement (000242), asset_tag_link_asset_fkey (000249),
// barcode_registry_asset_fkey (000255), stocktake_item_asset_fkey
// (000260) and the endpoint references of relationship_change (000261).
// The references on ci and asset that migrations 000287 (WP-876) and
// 000288 (WP-877) made composite get their simple form again.
const dropLaterReferences = `
	ALTER TABLE compliance_result DROP CONSTRAINT compliance_result_ci_id_fkey,
		ADD CONSTRAINT compliance_result_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE form_submission DROP CONSTRAINT form_submission_ci_id_fkey,
		ADD CONSTRAINT form_submission_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE SET NULL;
	ALTER TABLE maintenance_window_ci DROP CONSTRAINT maintenance_window_ci_ci_id_fkey,
		ADD CONSTRAINT maintenance_window_ci_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE security_finding DROP CONSTRAINT security_finding_ci_id_fkey,
		ADD CONSTRAINT security_finding_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE stock_scan DROP CONSTRAINT stock_scan_asset_id_fkey,
		ADD CONSTRAINT stock_scan_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id);
	ALTER TABLE stock_scan DROP CONSTRAINT stock_scan_ci_id_fkey,
		ADD CONSTRAINT stock_scan_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id);
	ALTER TABLE asset_location DROP CONSTRAINT asset_location_asset_id_fkey,
		ADD CONSTRAINT asset_location_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id) ON DELETE CASCADE;
	ALTER TABLE ci_change DROP CONSTRAINT ci_change_ci_id_fkey,
		ADD CONSTRAINT ci_change_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci_contact DROP CONSTRAINT ci_contact_ci_id_fkey,
		ADD CONSTRAINT ci_contact_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci_field_value DROP CONSTRAINT ci_field_value_ci_id_fkey,
		ADD CONSTRAINT ci_field_value_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci_instance_field_definition DROP CONSTRAINT ci_instance_field_definition_ci_id_fkey,
		ADD CONSTRAINT ci_instance_field_definition_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_source_ci_id_fkey,
		ADD CONSTRAINT ci_relationship_source_ci_id_fkey FOREIGN KEY (source_ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_target_ci_id_fkey,
		ADD CONSTRAINT ci_relationship_target_ci_id_fkey FOREIGN KEY (target_ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE discovery_result DROP CONSTRAINT discovery_result_matched_ci_id_fkey,
		ADD CONSTRAINT discovery_result_matched_ci_id_fkey FOREIGN KEY (matched_ci_id) REFERENCES ci (id);
	ALTER TABLE endpoint_agent DROP CONSTRAINT endpoint_agent_ci_id_fkey,
		ADD CONSTRAINT endpoint_agent_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE SET NULL;
	ALTER TABLE network_interface DROP CONSTRAINT network_interface_ci_id_fkey,
		ADD CONSTRAINT network_interface_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE rack_mount DROP CONSTRAINT rack_mount_ci_id_fkey,
		ADD CONSTRAINT rack_mount_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE relationship_suppression DROP CONSTRAINT relationship_suppression_source_ci_id_fkey,
		ADD CONSTRAINT relationship_suppression_source_ci_id_fkey FOREIGN KEY (source_ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE relationship_suppression DROP CONSTRAINT relationship_suppression_target_ci_id_fkey,
		ADD CONSTRAINT relationship_suppression_target_ci_id_fkey FOREIGN KEY (target_ci_id) REFERENCES ci (id) ON DELETE CASCADE;
	ALTER TABLE ci DROP CONSTRAINT ci_asset_id_fkey;
	ALTER TABLE ci DROP CONSTRAINT ci_parent_ci_fkey;
	ALTER TABLE ci DROP CONSTRAINT ci_merged_into_fkey;
	ALTER TABLE ci_identity_mac DROP CONSTRAINT ci_identity_mac_ci_fkey;
	ALTER TABLE asset DROP CONSTRAINT asset_ci_id_fkey;
	ALTER TABLE ticket DROP CONSTRAINT ticket_related_ci_id_fkey, DROP CONSTRAINT ticket_related_asset_id_fkey;
	ALTER TABLE reservation DROP CONSTRAINT reservation_asset_id_fkey;
	ALTER TABLE disposal_record DROP CONSTRAINT disposal_record_asset_id_fkey, DROP CONSTRAINT disposal_record_ci_id_fkey;
	ALTER TABLE internal_order_item DROP CONSTRAINT internal_order_item_asset_id_fkey;
	ALTER TABLE ci_template DROP CONSTRAINT ci_template_ci_fkey;
	ALTER TABLE ci_tag DROP CONSTRAINT ci_tag_ci_fkey;
	ALTER TABLE ticket_link DROP CONSTRAINT ticket_link_ci_fkey, DROP CONSTRAINT ticket_link_asset_fkey;
	ALTER TABLE stock_movement DROP CONSTRAINT stock_movement_asset_id_fkey;
	ALTER TABLE asset_tag_link DROP CONSTRAINT asset_tag_link_asset_fkey;
	ALTER TABLE barcode_registry DROP CONSTRAINT barcode_registry_asset_fkey;
	ALTER TABLE stocktake_item DROP CONSTRAINT stocktake_item_asset_fkey;
	ALTER TABLE relationship_change DROP CONSTRAINT relationship_change_source_tenant_fkey,
		DROP CONSTRAINT relationship_change_target_tenant_fkey;`

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

	// Schema state just before the tenant-reference section of 000056. The
	// edge scope trigger (000061, since 000142 refusing endpoints of another
	// organization) did not exist yet.
	exec("undo later references", dropLaterReferences)
	exec("undo 000056 references", `
		ALTER TABLE ci_relationship DISABLE TRIGGER ci_relationship_scope;
		ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_target_tenant_fkey;
		ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_source_tenant_fkey;
		ALTER TABLE asset DROP CONSTRAINT asset_id_organization_key;
		ALTER TABLE ci DROP CONSTRAINT ci_id_organization_key;
		DROP TABLE migration_quarantine;`)
	exec("composition of 000055", composition000055)

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

// TestMigration56DownWithoutQuarantineTable covers WP-574 (F-224, E-72
// Option 1): installations that applied 000056 before E-26 have its
// composite keys but no migration_quarantine. The down migration used to
// read the table unconditionally and stopped with "relation does not exist"
// and a dirty version. It now skips moving rows back when the table is
// missing and drops everything else as before. The case with the table is
// TestMigration56QuarantinesInconsistentRows.
func TestMigration56DownWithoutQuarantineTable(t *testing.T) {
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

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return n
	}

	// Legacy schema state at version 56: the composite keys of 000056
	// exist (composition with its tenant keys, created here because 000253
	// dropped it), migration_quarantine does not, and the foreign keys of
	// later migrations that depend on those keys do not exist yet either.
	if _, err = tx.Exec(ctx, dropLaterReferences+`
		DROP TABLE migration_quarantine;`+composition000055+`
		ALTER TABLE composition
			ADD CONSTRAINT composition_parent_tenant_fkey
			FOREIGN KEY (parent_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE,
			ADD CONSTRAINT composition_child_ci_tenant_fkey
			FOREIGN KEY (child_ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
			ADD CONSTRAINT composition_child_asset_tenant_fkey
			FOREIGN KEY (child_asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;`); err != nil {
		t.Fatalf("legacy schema state: %v", err)
	}
	const keys000056 = `SELECT count(*) FROM pg_constraint WHERE conname IN (
		'composition_child_asset_tenant_fkey', 'composition_child_ci_tenant_fkey',
		'composition_parent_tenant_fkey', 'ci_relationship_target_tenant_fkey',
		'ci_relationship_source_tenant_fkey', 'asset_id_organization_key', 'ci_id_organization_key')`
	if n := count(keys000056); n != 7 {
		t.Fatalf("composite keys of 000056 before down = %d, want 7", n)
	}
	relationships := count(`SELECT count(*) FROM ci_relationship`)
	compositions := count(`SELECT count(*) FROM composition`)

	if _, err = tx.Exec(ctx, migrationSection(t, "000056_rls_enforcement.down.sql",
		"ALTER TABLE composition DROP CONSTRAINT IF EXISTS composition_child_asset_tenant_fkey;",
		"DROP TRIGGER IF EXISTS trg_audit_log_no_update")); err != nil {
		t.Fatalf("000056 down without migration_quarantine: %v", err)
	}
	if n := count(keys000056); n != 0 {
		t.Errorf("composite keys of 000056 after down = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM pg_tables WHERE tablename = 'migration_quarantine'`); n != 0 {
		t.Error("down migration created migration_quarantine")
	}
	if n := count(`SELECT count(*) FROM ci_relationship`); n != relationships {
		t.Errorf("ci_relationship rows after down = %d, want %d", n, relationships)
	}
	if n := count(`SELECT count(*) FROM composition`); n != compositions {
		t.Errorf("composition rows after down = %d, want %d", n, compositions)
	}
}

// TestMigration253DropSavesAndRestoresRows covers WP-743 (AST-05, E-26):
// migration 000253 saves every composition row, role and position included,
// and every set asset.parent_asset_id in migration_quarantine before it
// drops the table, the cycle function and the column; its down migration
// rebuilds them and restores the rows unchanged. The test brings the table
// back with the down migration, seeds rows, then runs up and down again,
// all in one transaction that is rolled back.
func TestMigration253DropSavesAndRestoresRows(t *testing.T) {
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
	text := func(sql string, args ...any) string {
		t.Helper()
		var v *string
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&v); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		if v == nil {
			return ""
		}
		return *v
	}
	migration := func(file string) string {
		t.Helper()
		raw, readErr := os.ReadFile("../../migrations/" + file)
		if readErr != nil {
			t.Fatalf("read %s: %v", file, readErr)
		}
		return string(raw)
	}
	up, down := migration("000253_drop_composition.up.sql"), migration("000253_drop_composition.down.sql")

	const (
		org     = "dddd4444-0000-4000-8000-000000000253"
		ciType  = "dddd4444-0000-4000-8000-0000000253e1"
		childCI = "dddd4444-0000-4000-8000-0000000253c1"
		parent  = "dddd4444-0000-4000-8000-0000000253a1"
		child   = "dddd4444-0000-4000-8000-0000000253a2"
		compCI  = "dddd4444-0000-4000-8000-0000000253f1"
		compAst = "dddd4444-0000-4000-8000-0000000253f2"
	)
	const (
		compositions = `SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id)::text FROM composition c WHERE c.organization_id = $1`
		parents      = `SELECT jsonb_agg(jsonb_build_object('id', a.id, 'parent_asset_id', a.parent_asset_id,
			'version', a.version, 'updated_at', a.updated_at) ORDER BY a.id)::text FROM asset a WHERE a.organization_id = $1`
		quarantined = `SELECT jsonb_agg(q.row_data ORDER BY q.row_data->>'id')::text FROM migration_quarantine q
			WHERE q.migration = '000253' AND q.source_table = $2 AND q.organization_id = $1`
	)

	exec("000253 down (composition back)", down)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Drop 253', 'quarantine-253')`, []any{org}},
		{`INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1, $2, 'quarantine-253', 'Server')`, []any{ciType, org}},
		{`INSERT INTO ci (id, organization_id, ci_type_id, name) VALUES ($1, $2, $3, 'child-ci')`, []any{childCI, org, ciType}},
		{`INSERT INTO asset (id, organization_id, asset_tag, name) VALUES ($1, $3, 'Q253-P', 'parent'), ($2, $3, 'Q253-C', 'child')`, []any{parent, child, org}},
		{`UPDATE asset SET parent_asset_id = $1 WHERE id = $2`, []any{parent, child}},
		{`INSERT INTO composition (id, organization_id, parent_asset_id, child_ci_id, role, position)
			VALUES ($1, $2, $3, $4, 'psu', 'slot 2')`, []any{compCI, org, parent, childCI}},
		{`INSERT INTO composition (id, organization_id, parent_asset_id, child_asset_id, configuration_only, independently_locatable)
			VALUES ($1, $2, $3, $4, false, true)`, []any{compAst, org, parent, child}},
	} {
		exec("seed", s.sql, s.args...)
	}
	compsBefore, parentsBefore := text(compositions, org), text(parents, org)
	if compsBefore == "" || parentsBefore == "" {
		t.Fatal("seed rows missing")
	}

	exec("000253 up", up)
	if got := text(quarantined, org, "composition"); got != compsBefore {
		t.Errorf("quarantined composition rows = %s, want %s", got, compsBefore)
	}
	wantAsset := text(`SELECT jsonb_build_array(jsonb_build_object('id', $1::uuid, 'parent_asset_id', $2::uuid))::text`, child, parent)
	if got := text(quarantined, org, "asset"); got != wantAsset {
		t.Errorf("quarantined asset values = %s, want %s", got, wantAsset)
	}
	if got := text(`SELECT concat_ws(',', to_regclass('composition')::text,
		to_regprocedure('reject_composition_cycle()')::text,
		(SELECT attname::text FROM pg_attribute WHERE attrelid = 'asset'::regclass AND attname = 'parent_asset_id' AND NOT attisdropped))`); got != "" {
		t.Errorf("objects left after 000253 up: %s", got)
	}

	exec("000253 down", down)
	if got := text(compositions, org); got != compsBefore {
		t.Errorf("composition rows after down = %s, want %s", got, compsBefore)
	}
	if got := text(parents, org); got != parentsBefore {
		t.Errorf("asset rows after down = %s, want %s", got, parentsBefore)
	}
	if got := text(`SELECT count(*)::text FROM migration_quarantine WHERE migration = '000253'`); got != "0" {
		t.Errorf("quarantine entries of 000253 after down = %s, want 0", got)
	}
	// The rebuilt cycle trigger refuses a cycle again.
	exec("savepoint", `SAVEPOINT cycle`)
	if _, err = tx.Exec(ctx, `INSERT INTO composition (organization_id, parent_asset_id, child_asset_id) VALUES ($1, $2, $3)`,
		org, child, parent); err == nil {
		t.Error("cycle parent -> child -> parent accepted after down")
	}
	exec("rollback to savepoint", `ROLLBACK TO SAVEPOINT cycle`)
}
