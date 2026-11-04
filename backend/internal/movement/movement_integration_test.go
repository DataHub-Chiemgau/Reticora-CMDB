package movement_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ledgerMigration = "../../migrations/000242_stock_movement_canonical"
	dropMigration   = "../../migrations/000246_drop_asset_movement"
)

// readMigration returns the up and the down part of the migration path.
func readMigration(t *testing.T, path string) (up, down string) {
	t.Helper()
	rawUp, err := os.ReadFile(path + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	rawDown, err := os.ReadFile(path + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(rawUp), string(rawDown)
}

// siteCtx returns a request context of a principal of organization A that is
// restricted to siteID (clients and teams unrestricted).
func siteCtx(f *scopetest.Fixture, siteID string) context.Context {
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Sites = database.ScopeIDs(siteID)
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// stockBin creates a site of clientID in organization A with warehouse,
// zone, shelf and bin through the maintenance pool and returns the bin, the
// place of the stock of a consumable (stock_level, WP-283).
func stockBin(t *testing.T, f *scopetest.Fixture, clientID, name string) string {
	t.Helper()
	ctx := context.Background()
	var parent string
	if err := f.Admin.QueryRow(ctx, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		f.OrgA, clientID, name).Scan(&parent); err != nil {
		t.Fatalf("seed site %s: %v", name, err)
	}
	for _, kind := range []string{"warehouse", "zone", "shelf", "bin"} {
		if err := f.Admin.QueryRow(ctx, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, kind, parent, name+"-"+kind).Scan(&parent); err != nil {
			t.Fatalf("seed %s %s: %v", kind, name, err)
		}
	}
	return parent
}

// siteOf returns the site of the location id.
func siteOf(t *testing.T, f *scopetest.Fixture, id string) string {
	t.Helper()
	var site string
	if err := f.Admin.QueryRow(context.Background(), `SELECT site_id::text FROM location WHERE id = $1`, id).Scan(&site); err != nil {
		t.Fatalf("site of location %s: %v", id, err)
	}
	return site
}

// TestStockMovementCanonicalMigration covers the data migration of 000242
// (STK-04, AST-07, WP-744) in a rolled-back transaction: after the down part
// of 000246 (which restores asset_movement, WP-842) and of 000242 the legacy
// ledgers asset_movement, the former stock_movement and
// assignment get rows; the up part maps the types, keeps the ids, clears
// asset quantities and second object columns, takes the assignee from the
// notes, quarantines invalid rows, imports the assignment history without
// the movements asset_movement already holds and leaves asset_movement and
// assignment unchanged. Down and up again bring every row back, also an
// asset movement booked after the up part.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockMovementCanonicalMigration(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	ctx := context.Background()
	up, down := readMigration(t, ledgerMigration)
	_, dropDown := readMigration(t, dropMigration)
	asset1 := f.Asset(t, f.OrgA, f.Client1, "mig242-a1")
	asset2 := f.Asset(t, f.OrgA, f.Client1, "mig242-a2")
	foreign := f.Asset(t, f.OrgB, "", "mig242-foreign")
	user := f.AppUser(t, f.OrgA, "mig242-user")
	ciWithAsset := f.CI(t, f.OrgA, f.Client1, "mig242-ci-asset")
	ciAlone := f.CI(t, f.OrgA, f.Client1, "mig242-ci-alone")

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), execErr)
		}
	}
	exec(dropDown)
	exec(down)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, ciWithAsset, asset2)

	item, cons := f.ID(), f.ID()
	exec(`INSERT INTO quantity_item (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'mig242')`, item, f.OrgA, f.Client1)
	exec(`INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'mig242')`, cons, f.OrgA, f.Client1)

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	type legacy struct {
		id, kind, asset, item, typ, reason, notes string
		quantity                                  *float64
		created                                   time.Time
	}
	three, two := 3.0, 2.0
	mWarehouse, mBin, mRelease, mRepair, mUnknown, mQuantity, mBoth, mNoQuantity, mAssigned, mStranger, mReturned, mForeign :=
		f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	stranger := "0b0b0b0b-0000-4000-8000-000000000001"
	for _, m := range []legacy{
		{mWarehouse, "asset", asset1, "", "warehouse_transfer", "", "", nil, at(0)},
		{mBin, "asset", asset1, "", "bin_transfer", "", "", nil, at(1)},
		{mRelease, "asset", asset1, "", "reservation_release", "", "", nil, at(2)},
		{mRepair, "asset", asset1, "", "repair_transfer", "", "", nil, at(3)},
		{mUnknown, "asset", asset1, "", "teleport", "beamed", "", nil, at(4)},
		{mQuantity, "asset", asset1, "", "receipt", "", "", &three, at(5)},
		{mBoth, "quantity_item", asset1, item, "deployment", "", "", &two, at(6)},
		{mNoQuantity, "quantity_item", "", item, "receipt", "", "", nil, at(7)},
		{mAssigned, "asset", asset1, "", "assignment", "", "assigned to " + user, nil, at(10)},
		{mStranger, "asset", asset1, "", "assignment", "", "transferred to " + stranger, nil, at(11)},
		{mReturned, "asset", asset1, "", "return", "", "assignment returned", nil, at(20)},
		{mForeign, "asset", foreign, "", "receipt", "", "", nil, at(21)},
	} {
		exec(`INSERT INTO asset_movement (id, organization_id, item_kind, asset_id, quantity_item_id, movement_type, quantity, reason, notes, created_at)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10)`,
			m.id, f.OrgA, m.kind, m.asset, m.item, m.typ, m.quantity, m.reason, m.notes, m.created)
	}
	stockIn, stockOut := f.ID(), f.ID()
	exec(`INSERT INTO stock_movement (id, organization_id, consumable_id, direction, quantity, reason, reference, created_at)
		VALUES ($1, $2, $3, 'in', 5, 'delivery', 'PO-1', $4), ($5, $2, $3, 'out', 1, 'used', NULL, $6)`,
		stockIn, f.OrgA, cons, at(30), stockOut, at(31))
	// Assignment history: the first one has its movements in asset_movement
	// (within 5 s), the second none, the third only a CI with an asset, the
	// fourth a CI without one.
	aKnown, aNew, aCI, aAlone := f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO assignment (id, organization_id, asset_id, ci_id, assigned_to, assigned_by, assigned_at, returned_at, return_condition, status)
		VALUES ($1, $5, $6, NULL, $7, $7, $8, $9, NULL, 'returned'),
		       ($2, $5, $6, NULL, $7, $7, $10, $11, 'good', 'returned'),
		       ($3, $5, NULL, $12, $7, $7, $13, NULL, NULL, 'active'),
		       ($4, $5, NULL, $14, $7, $7, $13, NULL, NULL, 'active')`,
		aKnown, aNew, aCI, aAlone, f.OrgA, asset1, user,
		at(10).Add(3*time.Second), at(20).Add(-4*time.Second), at(40), at(41), ciWithAsset, at(42), ciAlone)

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return n
	}
	legacyBefore := count(`SELECT count(*) FROM asset_movement WHERE organization_id = $1`, f.OrgA)

	exec(up)

	type row struct {
		kind, asset, item, typ, reason, notes, assignee, client string
		quantity                                                *float64
	}
	read := func(id string) row {
		t.Helper()
		var r row
		if scanErr := tx.QueryRow(ctx, `SELECT item_kind, COALESCE(asset_id::text, ''), COALESCE(quantity_item_id::text, ''), movement_type,
			COALESCE(reason, ''), COALESCE(notes, ''), COALESCE(assignee_user_id::text, ''), COALESCE(client_id::text, ''), quantity
			FROM stock_movement WHERE id = $1`, id).
			Scan(&r.kind, &r.asset, &r.item, &r.typ, &r.reason, &r.notes, &r.assignee, &r.client, &r.quantity); scanErr != nil {
			t.Fatalf("movement %s: %v", id, scanErr)
		}
		return r
	}
	for _, c := range []struct {
		name, id string
		want     row
	}{
		{"warehouse_transfer", mWarehouse, row{kind: "asset", asset: asset1, typ: "transfer", client: f.Client1}},
		{"bin_transfer", mBin, row{kind: "asset", asset: asset1, typ: "transfer", client: f.Client1}},
		{"reservation_release", mRelease, row{kind: "asset", asset: asset1, typ: "release", client: f.Client1}},
		{"repair_transfer", mRepair, row{kind: "asset", asset: asset1, typ: "repair", client: f.Client1}},
		{"unknown type", mUnknown, row{kind: "asset", asset: asset1, typ: "correction", reason: "beamed; movement_type teleport", client: f.Client1}},
		{"asset with quantity", mQuantity, row{kind: "asset", asset: asset1, typ: "receipt", reason: "quantity 3.00", client: f.Client1}},
		{"asset and quantity item", mBoth, row{kind: "quantity_item", item: item, typ: "deployment", client: f.Client1, quantity: &two}},
		{"assigned to a user", mAssigned, row{kind: "asset", asset: asset1, typ: "assignment", notes: "assigned to " + user, assignee: user, client: f.Client1}},
		{"transferred to a stranger", mStranger, row{kind: "asset", asset: asset1, typ: "assignment", notes: "transferred to " + stranger, client: f.Client1}},
		{"returned", mReturned, row{kind: "asset", asset: asset1, typ: "return", notes: "assignment returned", client: f.Client1}},
	} {
		got := read(c.id)
		gotQuantity, wantQuantity := got.quantity, c.want.quantity
		got.quantity, c.want.quantity = nil, nil
		if got != c.want || (gotQuantity == nil) != (wantQuantity == nil) || (gotQuantity != nil && *gotQuantity != *wantQuantity) {
			t.Errorf("%s: %+v (quantity %v), want %+v (quantity %v)", c.name, got, gotQuantity, c.want, wantQuantity)
		}
	}
	for _, c := range []struct{ id, reason string }{{mNoQuantity, "movement_invalid_quantity"}, {mForeign, "movement_foreign_reference"}} {
		if n := count(`SELECT count(*) FROM stock_movement WHERE id = $1`, c.id); n != 0 {
			t.Errorf("quarantined movement %s is in the ledger", c.id)
		}
		if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000242' AND source_table = 'asset_movement'
			AND row_data ->> 'id' = $1 AND reason = $2`, c.id, c.reason); n != 1 {
			t.Errorf("quarantine of %s with %s: %d rows, want 1", c.id, c.reason, n)
		}
	}
	for _, c := range []struct{ id, typ, notes, reason string }{{stockIn, "receipt", "PO-1", "delivery"}, {stockOut, "retrieval", "", "used"}} {
		var kind, typ, notes, reason, consumableID string
		if err = tx.QueryRow(ctx, `SELECT item_kind, movement_type, COALESCE(notes, ''), COALESCE(reason, ''), consumable_id::text FROM stock_movement WHERE id = $1`, c.id).
			Scan(&kind, &typ, &notes, &reason, &consumableID); err != nil || kind != "consumable" || typ != c.typ || notes != c.notes || reason != c.reason || consumableID != cons {
			t.Errorf("former stock movement %s: %s %s %q %q %s, %v; want consumable %s %q %q", c.id, kind, typ, notes, reason, consumableID, err, c.typ, c.notes, c.reason)
		}
	}
	if n := count(`SELECT count(*) FROM pg_class WHERE relname = 'stock_movement_legacy'`); n != 0 {
		t.Error("stock_movement_legacy survives the up migration")
	}

	// Assignment history.
	if n := count(`SELECT count(*) FROM stock_movement WHERE notes = $1`, "assignment:"+aKnown); n != 0 {
		t.Errorf("assignment with movements in asset_movement: %d imported movements, want 0 (duplicates)", n)
	}
	type imported struct {
		typ, asset, actor, assignee, reason string
		occurred                            time.Time
	}
	history := func(id string) []imported {
		t.Helper()
		rows, qErr := tx.Query(ctx, `SELECT movement_type, asset_id::text, COALESCE(actor_id::text, ''), COALESCE(assignee_user_id::text, ''),
			COALESCE(reason, ''), occurred_at FROM stock_movement WHERE notes = $1 ORDER BY occurred_at`, "assignment:"+id)
		if qErr != nil {
			t.Fatal(qErr)
		}
		out, qErr := pgx.CollectRows(rows, func(r pgx.CollectableRow) (imported, error) {
			var i imported
			scanErr := r.Scan(&i.typ, &i.asset, &i.actor, &i.assignee, &i.reason, &i.occurred)
			i.occurred = i.occurred.UTC()
			return i, scanErr
		})
		if qErr != nil {
			t.Fatal(qErr)
		}
		return out
	}
	if got, want := history(aNew), []imported{
		{"assignment", asset1, user, user, "", at(40)},
		{"return", asset1, "", "", "good", at(41)},
	}; !slices.Equal(got, want) {
		t.Errorf("assignment without movements: %+v, want %+v", got, want)
	}
	if got, want := history(aCI), []imported{{"assignment", asset2, user, user, "", at(42)}}; !slices.Equal(got, want) {
		t.Errorf("assignment of a CI with an asset: %+v, want %+v", got, want)
	}
	if got := history(aAlone); len(got) != 0 {
		t.Errorf("assignment of a CI without asset: %+v, want no movement", got)
	}
	if n := count(`SELECT count(*) FROM assignment WHERE id IN ($1, $2, $3, $4)`, aKnown, aNew, aCI, aAlone); n != 4 {
		t.Errorf("assignment rows after the up migration: %d, want 4", n)
	}
	if n := count(`SELECT count(*) FROM asset_movement WHERE organization_id = $1`, f.OrgA); n != legacyBefore {
		t.Errorf("asset_movement rows after the up migration: %d, want %d", n, legacyBefore)
	}
	var legacyType string
	if err = tx.QueryRow(ctx, `SELECT movement_type FROM asset_movement WHERE id = $1`, mWarehouse).Scan(&legacyType); err != nil || legacyType != "warehouse_transfer" {
		t.Errorf("asset_movement row after the up migration: %q, %v; want unchanged", legacyType, err)
	}

	// Rows booked after the up migration survive down and up.
	booked, bookedCons := f.ID(), f.ID()
	exec(`INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, movement_type) VALUES ($1, $2, 'asset', $3, 'transfer')`, booked, f.OrgA, asset1)
	exec(`INSERT INTO stock_movement (id, organization_id, item_kind, consumable_id, movement_type, quantity, notes) VALUES ($1, $2, 'consumable', $3, 'receipt', 2, 'PO-2')`,
		bookedCons, f.OrgA, cons)
	exec(down)
	if err = tx.QueryRow(ctx, `SELECT movement_type FROM asset_movement WHERE id = $1`, booked).Scan(&legacyType); err != nil || legacyType != "warehouse_transfer" {
		t.Errorf("asset movement booked after the up migration, after down: %q, %v; want warehouse_transfer", legacyType, err)
	}
	if n := count(`SELECT count(*) FROM asset_movement WHERE notes LIKE 'assignment:%' AND organization_id = $1`, f.OrgA); n != 0 {
		t.Errorf("down wrote %d movements of the assignment history to asset_movement", n)
	}
	for _, c := range []struct{ id, direction, reference string }{{stockIn, "in", "PO-1"}, {stockOut, "out", ""}, {bookedCons, "in", "PO-2"}} {
		var direction, reference string
		if err = tx.QueryRow(ctx, `SELECT direction, COALESCE(reference, '') FROM stock_movement WHERE id = $1`, c.id).Scan(&direction, &reference); err != nil ||
			direction != c.direction || reference != c.reference {
			t.Errorf("former stock movement %s after down: %s %q, %v; want %s %q", c.id, direction, reference, err, c.direction, c.reference)
		}
	}
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000242'`); n != 0 {
		t.Errorf("quarantine entries of 000242 after down: %d", n)
	}
	exec(up)
	for _, c := range []struct{ id, typ string }{{booked, "transfer"}, {bookedCons, "receipt"}, {stockIn, "receipt"}, {mWarehouse, "transfer"}} {
		if got := read(c.id); got.typ != c.typ {
			t.Errorf("movement %s after down and up: %s, want %s", c.id, got.typ, c.typ)
		}
	}
	if got := history(aNew); len(got) != 2 {
		t.Errorf("assignment history after down and up: %+v, want 2 movements", got)
	}
}

// TestDropAssetMovementMigration covers 000246 (AST-07, WP-842) in a
// rolled-back transaction: the down part restores asset_movement (state
// after 000075: append-only, ENABLE + FORCE RLS, truncate guard, RESTRICT
// on the locations) from the ledger with the former type names, without
// the movements of the assignment history and the consumables, and with
// the rows 000242 quarantined; the up part drops table and trigger
// function, and aborts without change while a row of asset_movement is
// missing in stock_movement.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDropAssetMovementMigration(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	ctx := context.Background()
	up, down := readMigration(t, dropMigration)
	asset1 := f.Asset(t, f.OrgA, f.Client1, "mig246-a1")
	room := f.Room(t, f.Client1, "mig246-room")

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), execErr)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return n
	}

	item, cons := f.ID(), f.ID()
	exec(`INSERT INTO quantity_item (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'mig246')`, item, f.OrgA, f.Client1)
	exec(`INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'mig246')`, cons, f.OrgA, f.Client1)
	mTransfer, mRelease, mRepair, mReceipt, mAssigned, mHistory, mConsumable, mQuarantined :=
		f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	insert := `INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, quantity_item_id, consumable_id, movement_type, quantity, to_location_id, notes)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, ''))`
	two := 2.0
	for _, m := range []struct {
		id, kind, asset, item, cons, typ, to, notes string
		quantity                                    *float64
	}{
		{mTransfer, "asset", asset1, "", "", "transfer", room, "", nil},
		{mRelease, "asset", asset1, "", "", "release", "", "", nil},
		{mRepair, "asset", asset1, "", "", "repair", "", "", nil},
		{mReceipt, "quantity_item", "", item, "", "receipt", "", "", &two},
		{mAssigned, "asset", asset1, "", "", "assignment", "", "assigned to someone", nil},
		{mHistory, "asset", asset1, "", "", "assignment", "", "assignment:" + f.ID(), nil},
		{mConsumable, "consumable", "", "", cons, "receipt", "", "", &two},
	} {
		exec(insert, m.id, f.OrgA, m.kind, m.asset, m.item, m.cons, m.typ, m.quantity, m.to, m.notes)
	}
	exec(`INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
		VALUES ($1, 'asset_movement', jsonb_build_object('id', $2::uuid, 'organization_id', $1::uuid, 'item_kind', 'asset',
			'asset_id', $3::uuid, 'movement_type', 'teleport', 'created_at', now()), 'movement_foreign_reference', '000242')`,
		f.OrgA, mQuarantined, asset1)

	// Down.
	exec(down)
	legacy := func(id string) (typ, to string) {
		t.Helper()
		if scanErr := tx.QueryRow(ctx, `SELECT movement_type, COALESCE(to_location_id::text, '') FROM asset_movement WHERE id = $1`, id).
			Scan(&typ, &to); scanErr != nil {
			t.Fatalf("asset_movement row %s: %v", id, scanErr)
		}
		return typ, to
	}
	for _, c := range []struct{ name, id, typ, to string }{
		{"transfer", mTransfer, "warehouse_transfer", room},
		{"release", mRelease, "reservation_release", ""},
		{"repair", mRepair, "repair_transfer", ""},
		{"quantity item", mReceipt, "receipt", ""},
		{"assignment of the handler", mAssigned, "assignment", ""},
		{"quarantined by 000242", mQuarantined, "teleport", ""},
	} {
		if typ, to := legacy(c.id); typ != c.typ || to != c.to {
			t.Errorf("%s after down: %s to %q, want %s to %q", c.name, typ, to, c.typ, c.to)
		}
	}
	for _, id := range []string{mHistory, mConsumable} {
		if n := count(`SELECT count(*) FROM asset_movement WHERE id = $1`, id); n != 0 {
			t.Errorf("movement %s is in asset_movement after down", id)
		}
	}
	if n := count(`SELECT count(*) FROM pg_class WHERE oid = 'asset_movement'::regclass AND relrowsecurity AND relforcerowsecurity`); n != 1 {
		t.Error("asset_movement after down: RLS not enabled and forced")
	}
	if n := count(`SELECT count(*) FROM pg_policy WHERE polrelid = 'asset_movement'::regclass AND polname = 'asset_movement_isolation'`); n != 1 {
		t.Error("asset_movement after down: no policy asset_movement_isolation")
	}
	if n := count(`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'asset_movement'::regclass
		AND tgname IN ('reticora_truncate_guard', 'trg_asset_movement_no_update', 'trg_asset_movement_no_delete')`); n != 3 {
		t.Errorf("asset_movement after down: %d of the 3 guard triggers", n)
	}
	if n := count(`SELECT count(*) FROM pg_constraint WHERE conrelid = 'asset_movement'::regclass AND contype = 'f'
		AND confrelid = 'location'::regclass AND confdeltype = 'r'`); n != 2 {
		t.Errorf("asset_movement after down: %d RESTRICT references on location, want 2", n)
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, updErr := sp.Exec(ctx, `UPDATE asset_movement SET notes = 'rewritten' WHERE id = $1`, mTransfer)
	_ = sp.Rollback(ctx)
	if pgCode(updErr) != "P0001" {
		t.Errorf("UPDATE of asset_movement after down: %v, want the append-only refusal", updErr)
	}

	// Up with a row missing in the ledger aborts and changes nothing.
	unbooked := f.ID()
	exec(`INSERT INTO asset_movement (id, organization_id, asset_id, movement_type) VALUES ($1, $2, $3, 'receipt')`, unbooked, f.OrgA, asset1)
	sp, err = tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, upErr := sp.Exec(ctx, up)
	_ = sp.Rollback(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(upErr, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "asset_movement (000246): 1 rows are missing in stock_movement" {
		t.Errorf("up with a movement missing in stock_movement: %v, want the abort with 1 missing row", upErr)
	}
	if n := count(`SELECT count(*) FROM pg_class WHERE relname = 'asset_movement'`); n != 1 {
		t.Error("asset_movement is gone after the aborted up")
	}

	// Up once every row is in the ledger (or quarantined).
	exec(`ALTER TABLE asset_movement DISABLE TRIGGER trg_asset_movement_no_delete`)
	exec(`DELETE FROM asset_movement WHERE id = $1`, unbooked)
	exec(`ALTER TABLE asset_movement ENABLE TRIGGER trg_asset_movement_no_delete`)
	exec(up)
	if n := count(`SELECT count(*) FROM pg_class WHERE relname = 'asset_movement'`); n != 0 {
		t.Error("asset_movement survives the up migration")
	}
	if n := count(`SELECT count(*) FROM pg_proc WHERE proname = 'reject_asset_movement_mutation'`); n != 0 {
		t.Error("reject_asset_movement_mutation survives the up migration")
	}
	if n := count(`SELECT count(*) FROM stock_movement WHERE id IN ($1, $2, $3)`, mTransfer, mHistory, mConsumable); n != 3 {
		t.Errorf("ledger rows after the up migration: %d of 3", n)
	}
}

// pgCode returns the SQLSTATE of err.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestStockMovementLedgerRules covers the schema of stock_movement (STK-04,
// AST-07, WP-744): append-only, RESTRICT on every referenced object, the
// CHECKs, the derived scope (written values are overwritten, the site
// falls back to the object), the client and site scope of the policy (E-09),
// the scope of a document link to a movement and the LOC-11 answer for a
// location that only a movement keeps.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockMovementLedgerRules(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	ctx := context.Background()
	room1, room2 := f.Room(t, f.Client1, "ledger-s1"), f.Room(t, f.Client1, "ledger-s2")
	site1, site2 := siteOf(t, f, room1), siteOf(t, f, room2)
	// room3 is kept by a movement only (LOC-11).
	room3 := f.Room(t, f.Client1, "ledger-s3")
	asset1 := f.Asset(t, f.OrgA, f.Client1, "ledger-a1")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "ledger-a2")
	located := f.Asset(t, f.OrgA, f.Client1, "ledger-located")
	user := f.AppUser(t, f.OrgA, "ledger-user")
	admin := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), err)
		}
	}
	admin(`UPDATE asset SET location_id = $2 WHERE id = $1`, located, room2)
	item, cons, team := f.ID(), f.ID(), f.ID()
	admin(`INSERT INTO quantity_item (id, organization_id, client_id, name, location_id) VALUES ($1, $2, $3, 'ledger', $4)`, item, f.OrgA, f.Client1, room1)
	admin(`INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'ledger')`, cons, f.OrgA, f.Client1)
	// The stock of the consumable lies in a bin since WP-283.
	consBin := stockBin(t, f, f.Client1, "ledger-bin")
	admin(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 10)`, f.OrgA, cons, consBin)
	admin(`INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'ledger')`, team, f.OrgA)

	t.Run("schema", func(t *testing.T) {
		tx, err := f.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// try runs sql in a savepoint and returns its SQLSTATE ("" on success).
		try := func(sql string, args ...any) string {
			t.Helper()
			sp, spErr := tx.Begin(ctx)
			if spErr != nil {
				t.Fatal(spErr)
			}
			_, execErr := sp.Exec(ctx, sql, args...)
			if execErr != nil {
				_ = sp.Rollback(ctx)
				return pgCode(execErr)
			}
			if spErr = sp.Commit(ctx); spErr != nil {
				t.Fatal(spErr)
			}
			return ""
		}
		insert := `INSERT INTO stock_movement (organization_id, item_kind, asset_id, consumable_id, quantity_item_id, movement_type,
			quantity, from_location_id, to_location_id, assignee_user_id, assignee_team_id, client_id, site_id)
			VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7,
			NULLIF($8, '')::uuid, NULLIF($9, '')::uuid, NULLIF($10, '')::uuid, NULLIF($11, '')::uuid, NULLIF($12, '')::uuid, NULLIF($13, '')::uuid)
			RETURNING id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')`
		one := 1.0
		type mv struct {
			kind, asset, cons, item, typ   string
			quantity                       *float64
			from, to, user, team, cl, site string
		}
		book := func(m mv) (id, client, site string) {
			t.Helper()
			if err = tx.QueryRow(ctx, insert, f.OrgA, m.kind, m.asset, m.cons, m.item, m.typ, m.quantity, m.from, m.to, m.user, m.team, m.cl, m.site).
				Scan(&id, &client, &site); err != nil {
				t.Fatalf("book %+v: %v", m, err)
			}
			return id, client, site
		}

		// Written scope values are overwritten; the site follows to, from and
		// the object.
		for _, c := range []struct {
			name                 string
			m                    mv
			wantClient, wantSite string
		}{
			{"asset to a location", mv{kind: "asset", asset: asset1, typ: "transfer", from: room2, to: room1, cl: f.Client2, site: site2}, f.Client1, site1},
			{"asset from a location", mv{kind: "asset", asset: asset1, typ: "transfer", from: room2}, f.Client1, site2},
			{"asset without location", mv{kind: "asset", asset: located, typ: "assignment", user: user}, f.Client1, site2},
			{"quantity item without location", mv{kind: "quantity_item", item: item, typ: "receipt", quantity: &one, cl: f.Client2}, f.Client1, site1},
			{"consumable", mv{kind: "consumable", cons: cons, typ: "receipt", quantity: &one, site: site1}, f.Client1, ""},
			{"team assignee", mv{kind: "asset", asset: asset2, typ: "assignment", team: team}, f.Client2, ""},
		} {
			if _, client, site := book(c.m); client != c.wantClient || site != c.wantSite {
				t.Errorf("%s: client %q site %q, want %q %q", c.name, client, site, c.wantClient, c.wantSite)
			}
		}
		id, _, _ := book(mv{kind: "asset", asset: asset1, typ: "receipt", to: room1})
		bookedItem, _, _ := book(mv{kind: "quantity_item", item: item, typ: "receipt", quantity: &one})
		book(mv{kind: "asset", asset: asset1, typ: "transfer", from: room3, to: room1})

		// Append-only.
		if code := try(`UPDATE stock_movement SET notes = 'x' WHERE id = $1`, id); code != "P0001" {
			t.Errorf("UPDATE of a movement: %q, want P0001", code)
		}
		if code := try(`DELETE FROM stock_movement WHERE id = $1`, bookedItem); code != "P0001" {
			t.Errorf("DELETE of a movement: %q, want P0001", code)
		}
		// RESTRICT on every object.
		for _, c := range []struct{ name, sql, id string }{
			{"asset", `DELETE FROM asset WHERE id = $1`, asset1},
			{"consumable", `DELETE FROM consumable WHERE id = $1`, cons},
			{"quantity item", `DELETE FROM quantity_item WHERE id = $1`, item},
			{"location", `DELETE FROM location WHERE id = $1`, room3},
		} {
			if code := try(c.sql, c.id); code != "23503" {
				t.Errorf("DELETE of a %s with movements: %q, want 23503", c.name, code)
			}
		}
		// CHECKs.
		for _, c := range []struct {
			name string
			m    mv
		}{
			{"object not matching item_kind", mv{kind: "asset", cons: cons, typ: "receipt"}},
			{"two objects", mv{kind: "asset", asset: asset1, item: item, typ: "receipt"}},
			{"quantity of an asset", mv{kind: "asset", asset: asset1, typ: "receipt", quantity: &one}},
			{"quantity item without quantity", mv{kind: "quantity_item", item: item, typ: "receipt"}},
			{"unknown type", mv{kind: "asset", asset: asset1, typ: "warehouse_transfer"}},
			{"two assignees", mv{kind: "asset", asset: asset1, typ: "assignment", user: user, team: team}},
		} {
			if code := try(insert, f.OrgA, c.m.kind, c.m.asset, c.m.cons, c.m.item, c.m.typ, c.m.quantity, c.m.from, c.m.to, c.m.user, c.m.team, "", ""); code != "23514" {
				t.Errorf("%s: %q, want 23514", c.name, code)
			}
		}

		// A document link to a movement takes the movement's scope.
		doc := f.ID()
		if code := try(`INSERT INTO document (id, organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, $2, 'ledger', 'ledger.pdf', '', $3)`,
			doc, f.OrgA, user); code != "" {
			t.Fatalf("document: %s", code)
		}
		var client, site string
		if err = tx.QueryRow(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'stock_movement', $3)
			RETURNING COALESCE(client_id::text, ''), COALESCE(site_id::text, '')`, f.OrgA, doc, id).Scan(&client, &site); err != nil ||
			client != f.Client1 || site != site1 {
			t.Errorf("document link to a movement: client %q site %q, %v; want %s %s", client, site, err, f.Client1, site1)
		}

		// LOC-11: a location kept only by a movement answers 409 with the
		// kind stock_movement (WP-842), also through the foreign key.
		depErr := locations.CheckDeletable(ctx, tx, room3)
		var de *locations.DependencyError
		if !errors.As(depErr, &de) || !slices.Equal(de.Kinds, []string{locations.DependencyStockMovement}) {
			t.Errorf("dependencies of a location with movements: %v, want %s", depErr, locations.DependencyStockMovement)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, delErr := sp.Exec(ctx, `DELETE FROM location WHERE id = $1`, room3)
		_ = sp.Rollback(ctx)
		rec := httptest.NewRecorder()
		if !locations.WriteDependencyConflict(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/locations/"+room3, nil), locations.DeleteError(delErr)) ||
			rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"dependencies":["stock_movement"]`) {
			t.Errorf("delete of a location kept by a movement: %d %s (%v), want 409 with stock_movement", rec.Code, rec.Body.String(), delErr)
		}
	})

	t.Run("scope", func(t *testing.T) {
		repo := movement.NewPGRepository(f.App)
		consumables := consumable.NewPGRepository(f.App)
		orgCtx := f.OrgCtx(f.OrgA)
		mv := func(assetID, to string) *movement.Movement {
			return &movement.Movement{OrganizationID: f.OrgA, AssetID: assetID, MovementType: "warehouse_transfer", ToLocationID: to}
		}
		own, foreignClient, otherSite := mv(asset1, room1), mv(asset2, ""), mv(located, room2)
		for _, m := range []*movement.Movement{own, foreignClient, otherSite} {
			if err := repo.Record(orgCtx, m); err != nil {
				t.Fatalf("org-wide movement of %s: %v", m.AssetID, err)
			}
		}
		// The answer carries the canonical type and no quantity of an asset.
		if own.MovementType != "transfer" || own.ItemKind != "asset" || own.Quantity != nil {
			t.Errorf("recorded movement: %s %s %v, want an asset transfer without quantity", own.ItemKind, own.MovementType, own.Quantity)
		}
		page := api.PaginationParams{Limit: 200}
		listed := func(ctx context.Context, filter movement.MovementFilter) []string {
			t.Helper()
			list, _, err := repo.ListMovements(ctx, f.OrgA, &filter, page)
			if err != nil {
				t.Fatalf("list movements: %v", err)
			}
			ids := make([]string, 0, len(list))
			for i := range list {
				ids = append(ids, list[i].ID)
			}
			return ids
		}
		if got := listed(orgCtx, movement.MovementFilter{MovementType: "warehouse_transfer"}); !slices.Contains(got, own.ID) {
			t.Errorf("filter by a legacy type name: %v, want the transfer %s", got, own.ID)
		}

		// Client 1 neither sees nor writes movements of a client-2 asset.
		client1 := f.ClientCtx(f.Client1)
		if got := listed(client1, movement.MovementFilter{}); !slices.Contains(got, own.ID) || slices.Contains(got, foreignClient.ID) {
			t.Errorf("client-1 movements %v: want %s and not %s", got, own.ID, foreignClient.ID)
		}
		if err := repo.Record(client1, mv(asset2, "")); !errors.Is(err, movement.ErrAssetNotFound) {
			t.Errorf("client-1 movement of a client-2 asset: %v, want ErrAssetNotFound", err)
		}

		// Site 1 does not see the movement at site 2 and writes no consumable
		// movement (the bin of the consumable lies at another site, so it
		// sees none, WP-283); the org-wide principal does.
		site1Ctx := siteCtx(f, site1)
		if got := listed(site1Ctx, movement.MovementFilter{}); !slices.Contains(got, own.ID) || slices.Contains(got, otherSite.ID) {
			t.Errorf("site-1 movements %v: want %s and not %s", got, own.ID, otherSite.ID)
		}
		out := func(ctx context.Context) error {
			_, err := consumables.AddMovement(ctx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: cons, Direction: "out", Quantity: 1, Reference: "ledger"})
			return err
		}
		if err := out(site1Ctx); err == nil {
			t.Error("site-restricted principal booked a consumable movement without site")
		}
		if err := out(orgCtx); err != nil {
			t.Fatalf("org-wide consumable movement: %v", err)
		}
		list, _, err := consumables.ListMovements(orgCtx, f.OrgA, cons, page)
		if err != nil || len(list) != 1 || list[0].Direction != "out" || list[0].Reference != "ledger" {
			t.Errorf("consumable movements: %+v, %v; want one out with reference ledger", list, err)
		}
		var level float64
		if err = f.Admin.QueryRow(ctx, `SELECT on_hand::float8 FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, cons, consBin).Scan(&level); err != nil || level != 9 {
			t.Errorf("consumable stock after the refused and the booked movement: %v, %v; want 9", level, err)
		}
	})
}
