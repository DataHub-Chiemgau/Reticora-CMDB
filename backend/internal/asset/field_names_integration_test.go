package asset_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// Retest (WP-738, AST-01, E-41, E-120):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetFieldNames' ./internal/asset/

// TestAssetFieldNamesMirror covers the RENAME of 000231 (WP-738): the old
// names asset_tag, purchase_cost and warranty_end mirror inventory_no,
// purchase_price and warranty_until in both directions on INSERT and
// UPDATE; purchase_price holds twelve integer digits; supplier mirrors
// supplier.name of supplier_id, also after a rename; a taken inventory
// number stays 409 (on inventory_no since WP-739, idx_asset_tag_org) and is refused by the
// database under either name.
func TestAssetFieldNamesMirror(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()

	type names struct {
		inventoryNo, assetTag, price, cost, until, end, supplier string
	}
	get := func(id string) names {
		t.Helper()
		var n names
		if err := f.Admin.QueryRow(bg, `SELECT inventory_no, COALESCE(asset_tag, ''), COALESCE(purchase_price::text, ''),
			COALESCE(purchase_cost::text, ''), COALESCE(warranty_until::text, ''), COALESCE(warranty_end::text, ''),
			COALESCE(supplier, '') FROM asset WHERE id = $1`, id).
			Scan(&n.inventoryNo, &n.assetTag, &n.price, &n.cost, &n.until, &n.end, &n.supplier); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// INSERT with the old names (the SQL fixtures) and with the new ones.
	legacy, current := f.ID(), f.ID()
	w.admin(t, `INSERT INTO asset (id, organization_id, asset_tag, name, purchase_cost, warranty_end)
		VALUES ($1, $2, 'FN-OLD', 'old names', 12.5, '2027-01-31')`, legacy, f.OrgA)
	w.admin(t, `INSERT INTO asset (id, organization_id, inventory_no, name, purchase_price, warranty_until)
		VALUES ($1, $2, 'FN-NEW', 'new names', 999999999999.99, '2028-02-29')`, current, f.OrgA)
	if n := get(legacy); n != (names{"FN-OLD", "FN-OLD", "12.50", "12.50", "2027-01-31", "2027-01-31", ""}) {
		t.Errorf("insert with the old names: %+v", n)
	}
	if n := get(current); n != (names{"FN-NEW", "FN-NEW", "999999999999.99", "999999999999.99", "2028-02-29", "2028-02-29", ""}) {
		t.Errorf("insert with the new names (twelve integer digits): %+v", n)
	}

	// UPDATE over either name; the changed side wins.
	w.admin(t, `UPDATE asset SET asset_tag = 'FN-OLD-2', purchase_cost = 1, warranty_end = '2030-01-01' WHERE id = $1`, legacy)
	if n := get(legacy); n != (names{"FN-OLD-2", "FN-OLD-2", "1.00", "1.00", "2030-01-01", "2030-01-01", ""}) {
		t.Errorf("update over the old names: %+v", n)
	}
	w.admin(t, `UPDATE asset SET inventory_no = 'FN-NEW-2', purchase_price = NULL, warranty_until = '2031-05-05' WHERE id = $1`, current)
	if n := get(current); n != (names{"FN-NEW-2", "FN-NEW-2", "", "", "2031-05-05", "2031-05-05", ""}) {
		t.Errorf("update over the new names: %+v", n)
	}
	w.admin(t, `UPDATE asset SET name = 'unrelated' WHERE id = $1`, current)
	if n := get(current); n.inventoryNo != "FN-NEW-2" || n.assetTag != "FN-NEW-2" {
		t.Errorf("update of another column: %+v", n)
	}

	// supplier mirrors supplier.name of supplier_id (E-41).
	var supplierID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO supplier (organization_id, name) VALUES ($1, 'WP738 Lieferant') RETURNING id::text`,
		f.OrgA).Scan(&supplierID); err != nil {
		t.Fatal(err)
	}
	w.admin(t, `UPDATE asset SET supplier_id = $2, supplier = 'Freitext' WHERE id = $1`, legacy, supplierID)
	if n := get(legacy); n.supplier != "WP738 Lieferant" {
		t.Errorf("supplier with supplier_id: %q, want the supplier's name", n.supplier)
	}
	w.admin(t, `UPDATE supplier SET name = 'WP738 Umbenannt' WHERE id = $1`, supplierID)
	if n := get(legacy); n.supplier != "WP738 Umbenannt" {
		t.Errorf("supplier after the rename: %q", n.supplier)
	}
	w.admin(t, `UPDATE asset SET supplier_id = NULL WHERE id = $1`, legacy)
	if n := get(legacy); n.supplier != "" {
		t.Errorf("supplier after clearing supplier_id: %q, want empty", n.supplier)
	}

	// Uniqueness: idx_asset_tag_org moved with inventory_no.
	w.create(t, `{"inventory_no":"FN-DUP","name":"first"}`)
	w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"FN-DUP","name":"second"}`,
		want: http.StatusConflict, wantField: "inventory_no"})
	for _, sql := range []string{
		`INSERT INTO asset (organization_id, inventory_no, name) VALUES ($1, 'FN-DUP', 'x')`,
		`INSERT INTO asset (organization_id, asset_tag, name) VALUES ($1, 'FN-DUP', 'x')`,
	} {
		if _, err := f.Admin.Exec(bg, sql, f.OrgA); !strings.Contains(errString(err), "idx_asset_tag_org") {
			t.Errorf("%s: %v, want idx_asset_tag_org", sql, err)
		}
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO asset (organization_id, name) VALUES ($1, 'without number')`, f.OrgA); err == nil {
		t.Error("an asset without inventory number was accepted")
	}
}

// TestAssetFieldNamesReaders covers the readers of WP-738 outside the
// asset package: a saved view with the old and with the new field names
// finds the same assets; the search finds the inventory number;
// CIAssetFacts carries inventory_no and warranty_until besides asset_tag
// and warranty_end; available_only follows lifecycle_state, and a
// transition to deployed sets status to assigned (E-120 (b)).
func TestAssetFieldNamesReaders(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)

	a := w.create(t, `{"inventory_no":"FN-READ-738","name":"reader asset","warranty_until":"2027-03-31","purchase_price":"250"}`)
	other := w.create(t, `{"inventory_no":"FN-OTHER-738","name":"other asset","warranty_until":"2029-03-31","purchase_price":"10"}`)
	if a.LifecycleState != "ordered" || a.Status != "in_stock" {
		t.Fatalf("created: state %q status %q, want ordered and in_stock", a.LifecycleState, a.Status)
	}

	// Saved views: old and new names, where, sort and columns.
	engine := savedview.NewPGQueryEngine(f.App)
	run := func(spec string) ([]string, []map[string]any) {
		t.Helper()
		var s savedview.FilterSpec
		if err := json.Unmarshal([]byte(spec), &s); err != nil {
			t.Fatal(err)
		}
		rows, _, err := engine.Query(ctx, f.OrgA, &s, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		var ids []string
		var cols []map[string]any
		for i := range rows {
			ids = append(ids, rows[i].ID)
			cols = append(cols, rows[i].Columns)
		}
		return ids, cols
	}
	for _, pair := range [][2]string{
		{`{"field": "asset_tag", "op": "eq", "value": "FN-READ-738"}`, `{"field": "inventory_no", "op": "eq", "value": "FN-READ-738"}`},
		{`{"field": "purchase_cost", "op": "gt", "value": 100}`, `{"field": "purchase_price", "op": "gt", "value": 100}`},
		{`{"field": "warranty_end", "op": "lt", "value": "2028-01-01"}`, `{"field": "warranty_until", "op": "lt", "value": "2028-01-01"}`},
	} {
		oldIDs, _ := run(`{"entity_kind": "asset", "where": ` + pair[0] + `}`)
		newIDs, _ := run(`{"entity_kind": "asset", "where": ` + pair[1] + `}`)
		if !slices.Equal(oldIDs, newIDs) || !slices.Contains(newIDs, a.ID) || slices.Contains(newIDs, other.ID) {
			t.Errorf("%s: %v, %s: %v; want the same assets with %s", pair[0], oldIDs, pair[1], newIDs, a.ID)
		}
	}
	ids, cols := run(`{"entity_kind": "asset", "where": {"field": "asset_tag", "op": "like", "value": "FN-*-738"},
		"sort": [{"field": "warranty_end", "dir": "desc"}], "columns": ["asset_tag", "inventory_no", "purchase_price"]}`)
	if !slices.Equal(ids, []string{other.ID, a.ID}) || cols[1]["asset_tag"] != "FN-READ-738" || cols[1]["inventory_no"] != "FN-READ-738" ||
		cols[1]["purchase_price"] != 250.0 {
		t.Errorf("sort and columns: %v %v", ids, cols)
	}
	if ids, _ = run(`{"entity_kind": "asset", "warranty_within_days": 36500, "where": {"field": "name", "op": "eq", "value": "reader asset"}}`); !slices.Equal(ids, []string{a.ID}) {
		t.Errorf("warranty_within_days: %v", ids)
	}

	// available_only follows lifecycle_state; deployed derives assigned.
	available := func() bool {
		t.Helper()
		got, _ := run(`{"entity_kind": "asset", "available_only": true, "where": {"field": "inventory_no", "op": "eq", "value": "FN-READ-738"}}`)
		return slices.Contains(got, a.ID)
	}
	if !available() {
		t.Error("an ordered asset is not available")
	}
	transition := func(state string) string {
		t.Helper()
		var status string
		err := database.WithTenant(bg, f.App, ptr(database.OrgWideScope(f.OrgA, f.User)), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `UPDATE asset SET lifecycle_state = $2 WHERE id = $1 RETURNING status`, a.ID, state).Scan(&status)
		})
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := transition("deployed"); status != "assigned" || available() {
		t.Errorf("deployed: status %q, available %v; want assigned and not available", status, available())
	}
	for state, want := range map[string]string{"repair": "maintenance", "retired": "retired", "disposed": "disposed", "in_stock": "in_stock"} {
		if status := transition(state); status != want {
			t.Errorf("%s: status %q, want %q", state, status, want)
		}
	}
	transition("in_stock")
	w.admin(t, `UPDATE asset SET status = 'lost' WHERE id = $1`, a.ID)
	if !available() {
		t.Error("available_only reads status instead of lifecycle_state")
	}
	w.admin(t, `UPDATE asset SET lifecycle_state = NULL, status = 'lost' WHERE id = $1`, a.ID)
	if available() {
		t.Error("an asset without state and with status lost is available")
	}

	// Search: the inventory number from inventory_no.
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	res, err := repo.Query(ctx, &search.Query{OrganizationID: f.OrgA, Text: "FN-READ-738", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Data, func(h search.Hit) bool { return h.EntityID == a.ID }) {
		t.Errorf("search for the inventory number: %+v", res.Data)
	}
	var field string
	if err = f.Admin.QueryRow(bg, `SELECT content FROM search_document_field f JOIN search_document d ON d.id = f.document_id
		WHERE d.entity_id = $1 AND f.field = 'core.asset_tag'`, a.ID).Scan(&field); err != nil || field != "FN-READ-738" {
		t.Errorf("field row core.asset_tag: %q (%v)", field, err)
	}

	// CIAssetFacts with the old and the new names.
	ciID := f.CI(t, f.OrgA, "", "wp738-ci")
	w.admin(t, `UPDATE ci SET asset_id = $2 WHERE id = $1`, ciID, a.ID)
	item, err := ci.NewPGRepository(f.App).GetByID(ctx, f.OrgA, ciID)
	if err != nil || item.Asset == nil {
		t.Fatalf("CI with asset facts: %+v %v", item, err)
	}
	raw, _ := json.Marshal(item.Asset)
	var facts map[string]any
	if err = json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["inventory_no"] != "FN-READ-738" || facts["asset_tag"] != "FN-READ-738" ||
		facts["warranty_until"] != "2027-03-31" || facts["warranty_end"] != "2027-03-31" {
		t.Errorf("CIAssetFacts: %s", raw)
	}
}

// TestAssetFieldNamesDataMigration covers the data migration of 000231:
// the saved views and the computed expressions take the new names (where,
// sort and columns; bare and as asset.<field>), values stay, the down
// migration restores the old names. It reruns the migration in a
// transaction that is rolled back.
func TestAssetFieldNamesDataMigration(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile("../../migrations/000231_asset_field_names." + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(step, sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", step, execErr)
		}
	}
	exec("down", read("down"))
	assetView, ciView := f.ID(), f.ID()
	exec("views", `INSERT INTO saved_view (id, organization_id, name, entity_kind, filter_spec) VALUES
		($1, $3, 'wp738 assets', 'asset', '{"entity_kind": "asset", "warranty_within_days": 30,
			"where": {"and": [{"field": "asset_tag", "op": "eq", "value": "asset_tag"}, {"not": {"field": "purchase_cost", "op": "gt", "value": 1}}]},
			"sort": [{"field": "warranty_end", "dir": "asc"}], "columns": ["asset_tag", "name", "attributes.asset_tag"]}'),
		($2, $3, 'wp738 cis', 'ci', '{"where": {"field": "asset.warranty_end", "op": "lt", "value": "2027-01-01"},
			"columns": ["asset.asset_tag", "hostname"]}')`, assetView, ciView, f.OrgA)
	exec("up", read("up"))

	spec := func(id string) string {
		t.Helper()
		var s string
		if scanErr := tx.QueryRow(bg, `SELECT filter_spec::text FROM saved_view WHERE id = $1`, id).Scan(&s); scanErr != nil {
			t.Fatal(scanErr)
		}
		return s
	}
	want := map[string]string{
		assetView: `{"sort": [{"dir": "asc", "field": "warranty_until"}], "where": {"and": [{"op": "eq", "field": "inventory_no", "value": "asset_tag"}, ` +
			`{"not": {"op": "gt", "field": "purchase_price", "value": 1}}]}, "columns": ["inventory_no", "name", "attributes.asset_tag"], ` +
			`"entity_kind": "asset", "warranty_within_days": 30}`,
		ciView: `{"where": {"op": "lt", "field": "asset.warranty_until", "value": "2027-01-01"}, "columns": ["asset.inventory_no", "hostname"]}`,
	}
	for id, s := range want {
		if got := spec(id); !sameJSON(t, got, s) {
			t.Errorf("filter_spec after up:\n got %s\nwant %s", got, s)
		}
	}
	var expr string
	if err = tx.QueryRow(bg, `SELECT computed ->> 'args' FROM attribute_definition WHERE id = 'a7180000-0000-4000-8000-000000000001'`).
		Scan(&expr); err != nil || !strings.Contains(expr, `"asset.warranty_until"`) {
		t.Errorf("computed expression of warranty_days_left: %s (%v)", expr, err)
	}

	exec("down again", read("down"))
	if got := spec(ciView); !sameJSON(t, got, `{"where": {"op": "lt", "field": "asset.warranty_end", "value": "2027-01-01"}, "columns": ["asset.asset_tag", "hostname"]}`) {
		t.Errorf("filter_spec after down: %s", got)
	}
	if err = tx.QueryRow(bg, `SELECT computed ->> 'args' FROM attribute_definition WHERE id = 'a7180000-0000-4000-8000-000000000001'`).
		Scan(&expr); err != nil || !strings.Contains(expr, `"asset.warranty_end"`) {
		t.Errorf("computed expression after down: %s (%v)", expr, err)
	}
}

func ptr[T any](v T) *T { return &v }

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}
