package consumable_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// Retest (WP-758, STK-03, E-40):
//
//	cd backend && go test -count=1 -p 1 -run 'TestQuantityItemDrop' ./internal/consumable/

const dropMigration = "../../migrations/000269_quantity_item_drop"

// TestQuantityItemDropCatalog covers the migrated schema after 000269
// (WP-758): quantity_item is gone, and no function, policy, constraint,
// trigger or view names it any more. A site change of a location runs
// location_refresh_asset_site() without error.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestQuantityItemDropCatalog(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	var gone bool
	if err := f.Admin.QueryRow(bg, `SELECT to_regclass('quantity_item') IS NULL`).Scan(&gone); err != nil || !gone {
		t.Fatalf("quantity_item after 000269: dropped %v, %v; want dropped", gone, err)
	}
	// names reports whether the text names quantity_item; the alias
	// quantity_item_id (E-38) is no reference to the table.
	const names = `regexp_replace(%s, 'quantity_item_id', '', 'g') ILIKE '%%quantity\_item%%'`
	for name, query := range map[string]string{
		"functions": `SELECT p.oid::regprocedure::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public' AND ` + fmt.Sprintf(names, "p.prosrc"),
		"policies": `SELECT tablename || '.' || policyname FROM pg_policies
			WHERE ` + fmt.Sprintf(names, "policyname || ' ' || COALESCE(qual, '') || ' ' || COALESCE(with_check, '')"),
		"constraints": `SELECT conrelid::regclass::text || '.' || conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = 'public' AND ` + fmt.Sprintf(names, "conname || ' ' || pg_get_constraintdef(c.oid)"),
		"triggers": `SELECT tgrelid::regclass::text || '.' || tgname FROM pg_trigger
			WHERE NOT tgisinternal AND ` + fmt.Sprintf(names, "pg_get_triggerdef(oid)"),
		"views":   `SELECT viewname FROM pg_views WHERE schemaname = 'public' AND ` + fmt.Sprintf(names, "definition"),
		"indexes": `SELECT indexrelid::regclass::text FROM pg_index WHERE ` + fmt.Sprintf(names, "pg_get_indexdef(indexrelid)"),
	} {
		rows, err := f.Admin.Query(bg, query)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(found) > 0 {
			t.Errorf("%s still name quantity_item after 000269: %v", name, found)
		}
	}

	// A site change (the shelf moves to the zone of another site) re-derives
	// the assets through location_refresh_asset_site without quantity_item.
	from := binTree(t, f, f.OrgA, f.Client1, "qidrop-from")
	to := binTree(t, f, f.OrgA, f.Client1, "qidrop-to")
	assetID := f.Asset(t, f.OrgA, f.Client1, "qidrop-asset")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, assetID, from.bin); err != nil {
		t.Fatal(err)
	}
	if _, err := locations.NewPGRepository(f.App).Move(f.OrgCtx(f.OrgA), f.OrgA, from.shelf, to.zone); err != nil {
		t.Fatalf("site change of a location after 000269: %v", err)
	}
	var site string
	if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM asset WHERE id = $1`, assetID).Scan(&site); err != nil || site != to.site {
		t.Errorf("site of the asset after the move: %q, %v; want %s", site, err, to.site)
	}
}

// TestQuantityItemDropMigration covers the data of 000269 (WP-758, E-26) in
// one transaction that is always rolled back: the down part rebuilds
// quantity_item (with its TRUNCATE guard and RLS), legacy rows with and
// without a consumable of the same id are seeded; the up part keeps them in
// migration_quarantine (quantity_item_archived, quantity_item_not_merged)
// and drops the table; the down part brings them back unchanged and
// removes the entries. An up with a foreign key left on quantity_item
// aborts and changes nothing.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestQuantityItemDropMigration(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	readPart := func(part string) string {
		t.Helper()
		raw, err := os.ReadFile(dropMigration + "." + part + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	up, down := readPart("up"), readPart("down")
	bin := binTree(t, f, f.OrgA, f.Client1, "qidrop-mig").bin

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), execErr)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(bg, sql, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return n
	}

	exec(down)
	if n := count(`SELECT count(*) FROM pg_class WHERE oid = 'quantity_item'::regclass AND relrowsecurity AND relforcerowsecurity`); n != 1 {
		t.Error("quantity_item after down: RLS not enabled and forced")
	}
	// The start check TruncateGuardTrigger (database.VerifyRoleContract)
	// finds an enabled statement-level BEFORE TRUNCATE guard.
	if n := count(`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'quantity_item'::regclass
		AND tgname = $1 AND tgfoid = to_regprocedure('public.reticora_truncate_guard()')
		AND tgtype & 1 = 0 AND tgtype & 2 = 2 AND tgtype & 32 = 32 AND tgenabled IN ('O', 'A')`, database.TruncateGuardTrigger); n != 1 {
		t.Error("quantity_item after down: no enabled TRUNCATE guard")
	}
	if n := count(`SELECT count(*) FROM pg_policy WHERE polrelid = 'quantity_item'::regclass
		AND polname IN ('quantity_item_isolation', 'quantity_item_scoped_update', 'quantity_item_scoped_delete')`); n != 3 {
		t.Errorf("quantity_item after down: %d of the 3 policies", n)
	}

	merged, legacy := f.ID(), f.ID()
	exec(`INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'qidrop-merged')`, merged, f.OrgA, f.Client1)
	exec(`INSERT INTO quantity_item (id, organization_id, client_id, sku, name, stock_level, location_id, attributes)
		VALUES ($1, $2, $3, 'QIDROP-1', 'qidrop-merged', 4, $4, '{"color": "red"}')`, merged, f.OrgA, f.Client1, bin)
	exec(`INSERT INTO quantity_item (id, organization_id, client_id, name, stock_level) VALUES ($1, $2, $3, 'qidrop-legacy', 2)`,
		legacy, f.OrgA, f.Client1)
	var site string
	if err = tx.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM quantity_item WHERE id = $1`, merged).Scan(&site); err != nil || site == "" {
		t.Fatalf("site of the legacy row in a bin: %q, %v", site, err)
	}
	exec(`CREATE TEMPORARY TABLE qidrop_before ON COMMIT DROP AS SELECT id, to_jsonb(q) AS row_data FROM quantity_item q`)

	exec(up)
	if n := count(`SELECT count(*) FROM pg_class WHERE relname = 'quantity_item' AND relnamespace = 'public'::regnamespace`); n != 0 {
		t.Error("quantity_item exists after up")
	}
	reason := func(id string) string {
		t.Helper()
		var r string
		if scanErr := tx.QueryRow(bg, `SELECT q.reason FROM migration_quarantine q JOIN qidrop_before b ON b.row_data = q.row_data
			WHERE q.migration = '000269' AND q.source_table = 'quantity_item' AND q.organization_id = $1 AND b.id = $2`, f.OrgA, id).Scan(&r); scanErr != nil {
			t.Fatalf("quarantine entry of %s: %v", id, scanErr)
		}
		return r
	}
	if r := reason(merged); r != "quantity_item_archived" {
		t.Errorf("row with a consumable of its id: reason %q, want quantity_item_archived", r)
	}
	if r := reason(legacy); r != "quantity_item_not_merged" {
		t.Errorf("row without consumable: reason %q, want quantity_item_not_merged", r)
	}

	exec(down)
	if n := count(`SELECT count(*) FROM qidrop_before b JOIN quantity_item q ON q.id = b.id WHERE to_jsonb(q) = b.row_data`); n != 2 {
		t.Errorf("%d of the 2 rows back unchanged after down", n)
	}
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000269'`); n != 0 {
		t.Errorf("quarantine entries of 000269 after down: %d", n)
	}

	// A foreign key left on quantity_item aborts the up part.
	exec(`CREATE TABLE qidrop_ref (item_id UUID REFERENCES quantity_item (id))`)
	sp, err := tx.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	_, upErr := sp.Exec(bg, up)
	_ = sp.Rollback(bg)
	var pgErr *pgconn.PgError
	if !errors.As(upErr, &pgErr) || pgErr.Code != "P0001" ||
		pgErr.Message != "quantity_item (000269): foreign keys still reference the table: qidrop_ref.qidrop_ref_item_id_fkey" {
		t.Errorf("up with a foreign key on quantity_item: %v, want the abort naming qidrop_ref.qidrop_ref_item_id_fkey", upErr)
	}
	if n := count(`SELECT count(*) FROM quantity_item WHERE organization_id = $1`, f.OrgA); n != 2 {
		t.Errorf("rows after the aborted up: %d, want 2", n)
	}
	exec(`DROP TABLE qidrop_ref`)
	exec(up)
}
