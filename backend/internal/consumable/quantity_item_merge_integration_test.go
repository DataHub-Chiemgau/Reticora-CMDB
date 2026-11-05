package consumable_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-757, STK-03, API-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestQuantityItemMerge|TestInventoryItemsAlias' ./internal/consumable/

const mergeMigration = "../../migrations/000256_quantity_item_merge"

// TestQuantityItemMergeMigration covers the data migration of 000256 (E-40,
// E-26, WP-757) in one transaction that is always rolled back: after the
// down parts of 000269 (WP-758) and 000256, quantity items with stock in a
// bin, in a bin without client restriction, at a room, without location,
// in a bin of another client, with a negative level, with a SKU an
// existing consumable holds and with attributes, and a ledger row of kind
// quantity_item are seeded. The up part takes every item over with its id;
// only the stock in a compatible bin becomes a stock_level row, the rest
// goes to migration_quarantine with its reason (stock and location in the
// row); the colliding SKU becomes NULL; the ledger row is a consumable
// movement and quantity_item_id is gone. Down restores the ledger row, removes the merged consumable and
// stock rows and the quarantine entries; up again gives the same state.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestQuantityItemMergeMigration(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	readPart := func(part string) string {
		t.Helper()
		raw, err := os.ReadFile(mergeMigration + "." + part + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	up, down := readPart("up"), readPart("down")
	// quantity_item is dropped by 000269 (WP-758); its down part rebuilds
	// the table the down part of 000256 works on.
	rawDrop, readErr := os.ReadFile(dropMigration + ".down.sql")
	if readErr != nil {
		t.Fatal(readErr)
	}
	bin1 := binTree(t, f, f.OrgA, f.Client1, "merge-c1").bin
	bin2 := binTree(t, f, f.OrgA, f.Client2, "merge-c2").bin
	room := f.Room(t, f.Client1, "merge-room")
	existing := newConsumable(t, f, f.Client1, "merge-existing")
	if _, err := f.Admin.Exec(bg, `UPDATE consumable SET sku = 'MERGE-SKU' WHERE id = $1`, existing); err != nil {
		t.Fatal(err)
	}

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
	exec(string(rawDrop))
	exec(down)

	type item struct {
		id, client, sku, location, attributes string
		level                                 float64
	}
	qBin, qOpen, qRoom, qNone, qForeign, qNegative, qSku, qAttrs := f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	items := []item{
		{qBin, f.Client1, "MERGE-BIN", bin1, "{}", 5},
		{qOpen, "", "", bin2, "{}", 2},
		{qRoom, f.Client1, "", room, "{}", 3},
		{qNone, f.Client1, "", "", "{}", 4},
		{qForeign, f.Client1, "", bin2, "{}", 1},
		{qNegative, f.Client1, "", bin1, "{}", -2},
		{qSku, f.Client1, "MERGE-SKU", "", "{}", 0},
		{qAttrs, f.Client1, "", "", `{"color": "red"}`, 0},
	}
	for _, it := range items {
		exec(`INSERT INTO quantity_item (id, organization_id, client_id, sku, name, category, unit, stock_level, min_level, location_id, notes, attributes)
			VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, ''), $8, 'cable', 'm', $5, 1.5, NULLIF($6, '')::uuid, 'note', $7::jsonb)`,
			it.id, f.OrgA, it.client, it.sku, it.level, it.location, it.attributes, "merge-"+it.id)
	}
	ledgerRow, assetRow := f.ID(), f.ID()
	assetID := f.Asset(t, f.OrgA, f.Client1, "merge-asset")
	exec(`INSERT INTO stock_movement (id, organization_id, item_kind, quantity_item_id, movement_type, quantity, to_location_id)
		VALUES ($1, $2, 'quantity_item', $3, 'receipt', 2, $4)`, ledgerRow, f.OrgA, qBin, bin1)
	exec(`INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, movement_type) VALUES ($1, $2, 'asset', $3, 'transfer')`,
		assetRow, f.OrgA, assetID)

	// merged checks the state after an up part.
	merged := func(label string) {
		t.Helper()
		// ID-equal articles with the taken-over columns.
		if n := count(`SELECT count(*) FROM quantity_item q JOIN consumable c ON c.id = q.id
			WHERE q.organization_id = $1 AND c.organization_id = q.organization_id AND c.client_id IS NOT DISTINCT FROM q.client_id
			  AND c.name = q.name AND c.category = q.category AND c.unit = q.unit AND c.min_level = q.min_level
			  AND c.notes IS NOT DISTINCT FROM q.notes AND c.created_at = q.created_at AND c.updated_at = q.updated_at
			  AND c.sku IS NOT DISTINCT FROM CASE WHEN q.id = $2 THEN NULL ELSE q.sku END`, f.OrgA, qSku); n != len(items) {
			t.Errorf("%s: %d of %d quantity items taken over with their id and columns", label, n, len(items))
		}
		var sku string
		if err = tx.QueryRow(bg, `SELECT sku FROM consumable WHERE id = $1`, existing).Scan(&sku); err != nil || sku != "MERGE-SKU" {
			t.Errorf("%s: SKU of the existing consumable %q, %v; want it kept", label, sku, err)
		}
		// Stock only in a compatible bin.
		rows, qErr := tx.Query(bg, `SELECT consumable_id::text || '@' || location_id::text || '=' || on_hand::float8::text || '/' || client_id::text
			FROM stock_level WHERE organization_id = $1 AND consumable_id <> $2 ORDER BY 1`, f.OrgA, existing)
		if qErr != nil {
			t.Fatal(qErr)
		}
		stock, qErr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qErr != nil {
			t.Fatal(qErr)
		}
		wantStock := []string{qBin + "@" + bin1 + "=5/" + f.Client1, qOpen + "@" + bin2 + "=2/" + f.Client2}
		slices.Sort(wantStock)
		if !slices.Equal(stock, wantStock) {
			t.Errorf("%s: stock rows %v, want %v", label, stock, wantStock)
		}
		// Quarantine with reason, stock and location.
		rows, qErr = tx.Query(bg, `SELECT row_data ->> 'id', reason, (row_data ->> 'stock_level')::float8, COALESCE(row_data ->> 'location_id', '')
			FROM migration_quarantine WHERE migration = '000256' AND source_table = 'quantity_item' AND organization_id = $1
			ORDER BY 1, 2`, f.OrgA)
		if qErr != nil {
			t.Fatal(qErr)
		}
		type entry struct {
			id, reason string
			level      float64
			location   string
		}
		got, qErr := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
			var e entry
			return e, r.Scan(&e.id, &e.reason, &e.level, &e.location)
		})
		if qErr != nil {
			t.Fatal(qErr)
		}
		want := []entry{
			{qRoom, "stock_without_bin", 3, room},
			{qNone, "stock_without_bin", 4, ""},
			{qForeign, "bin_client_mismatch", 1, bin2},
			{qNegative, "negative_stock", -2, bin1},
			{qSku, "sku_conflict", 0, ""},
			{qAttrs, "attributes_dropped", 0, ""},
		}
		slices.SortFunc(want, func(a, b entry) int { return strings.Compare(a.id+a.reason, b.id+b.reason) })
		if !slices.Equal(got, want) {
			t.Errorf("%s: quarantine %+v, want %+v", label, got, want)
		}
		// The ledger.
		var kind, consumableID string
		if err = tx.QueryRow(bg, `SELECT item_kind, COALESCE(consumable_id::text, '') FROM stock_movement WHERE id = $1`, ledgerRow).
			Scan(&kind, &consumableID); err != nil || kind != "consumable" || consumableID != qBin {
			t.Errorf("%s: ledger row %s %s, %v; want consumable %s", label, kind, consumableID, err, qBin)
		}
		if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'stock_movement' AND column_name = 'quantity_item_id'`); n != 0 {
			t.Errorf("%s: stock_movement.quantity_item_id survives", label)
		}
		if n := count(`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'stock_movement'::regclass AND tgname = 'stock_movement_append_only'
			AND tgenabled = 'O'`); n != 1 {
			t.Errorf("%s: the append-only trigger is not enabled again", label)
		}
		sp, spErr := tx.Begin(bg)
		if spErr != nil {
			t.Fatal(spErr)
		}
		_, insErr := sp.Exec(bg, `INSERT INTO stock_movement (organization_id, item_kind, consumable_id, movement_type, quantity)
			VALUES ($1, 'quantity_item', $2, 'receipt', 1)`, f.OrgA, qBin)
		_ = sp.Rollback(bg)
		if pgCode(insErr) != "23514" {
			t.Errorf("%s: a movement of kind quantity_item: %v, want 23514", label, insErr)
		}
	}

	exec(up)
	merged("up")

	exec(down)
	var kind, itemID string
	if err = tx.QueryRow(bg, `SELECT item_kind, COALESCE(quantity_item_id::text, '') FROM stock_movement WHERE id = $1`, ledgerRow).
		Scan(&kind, &itemID); err != nil || kind != "quantity_item" || itemID != qBin {
		t.Errorf("ledger row after down: %s %s, %v; want quantity_item %s", kind, itemID, err, qBin)
	}
	if n := count(`SELECT count(*) FROM consumable WHERE organization_id = $1`, f.OrgA); n != 1 {
		t.Errorf("consumables after down: %d, want only the existing one", n)
	}
	if n := count(`SELECT count(*) FROM stock_level WHERE organization_id = $1`, f.OrgA); n != 0 {
		t.Errorf("stock rows after down: %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000256'`); n != 0 {
		t.Errorf("quarantine entries of 000256 after down: %d", n)
	}
	if n := count(`SELECT count(*) FROM quantity_item WHERE organization_id = $1`, f.OrgA); n != len(items) {
		t.Errorf("quantity items after down: %d, want %d", n, len(items))
	}

	exec(up)
	merged("up after down")
}

// pgCode returns the SQLSTATE of err.
func pgCode(err error) string {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}
	return ""
}

// assetCreator creates the asset of a conversion through the asset
// repository, like the server wiring.
type assetCreator struct{ repo *asset.PGRepository }

func (c assetCreator) Create(ctx context.Context, ref *movement.AssetRef) (string, error) {
	a := &asset.Asset{OrganizationID: ref.OrganizationID, ClientID: ref.ClientID, AssetTag: ref.AssetTag, Name: ref.Name,
		Category: ref.Category, Status: "in_stock", SerialNumber: ref.SerialNumber, LocationID: ref.LocationID, CustomFields: map[string]any{}}
	if err := c.repo.Create(ctx, a); err != nil {
		return "", err
	}
	return a.ID, nil
}

// aliasAPI serves the movement routes with the conversion over PostgreSQL
// for an org-wide principal of organization A.
type aliasAPI struct {
	t      *testing.T
	router chi.Router
}

func newAliasAPI(t *testing.T, f *scopetest.Fixture) *aliasAPI {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			scope := database.OrgWideScope(f.OrgA, f.User)
			ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), &scope),
				tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	movement.NewHandler(movement.NewService(movement.NewPGRepository(f.App))).
		WithAssets(assetCreator{repo: asset.NewPGRepository(f.App)}).RegisterRoutes(r)
	return &aliasAPI{t: t, router: r}
}

func (a *aliasAPI) do(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		raw = string(b)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			a.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestInventoryItemsAlias covers the alias inventory/items on consumable
// (API-06, STK-03, E-38, E-40, WP-757) over the handlers against
// PostgreSQL: POST creates a consumable and books stock_level as a receipt
// into the bin location_id (without bin, at a room or with attributes 422,
// nothing created); GET answers QuantityItem with the sum over the bins and
// the only bin; PATCH changes the consumable, location_id and attributes
// are 422; quantity_item_id books and filters as the alias of
// consumable_id; convert retrieves one unit from the bin and creates the
// asset there in one transaction (without location_id and several bins
// 422), a shortfall is 409 insufficient-stock and leaves no asset; DELETE
// of an article with stock is 409. The route snapshot of the alias reads
// the consumable row, and the audit scope of the type inventory follows
// the client of the consumable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInventoryItemsAlias(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	a := newAliasAPI(t, f)
	bin := binTree(t, f, f.OrgA, f.Client1, "alias-1").bin
	bin2 := binTree(t, f, f.OrgA, f.Client1, "alias-2").bin
	room := f.Room(t, f.Client1, "alias-room")
	const items = "/api/v1/inventory/items"
	countNamed := func(name string) int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM consumable WHERE organization_id = $1 AND name = $2`, f.OrgA, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Create.
	for name, tc := range map[string]struct {
		body  map[string]any
		field string
	}{
		"stock without bin": {map[string]any{"name": "alias-refused", "stock_level": 2}, "location_id"},
		"stock at a room":   {map[string]any{"name": "alias-refused", "stock_level": 2, "location_id": room}, "location_id"},
		"bin without stock": {map[string]any{"name": "alias-refused", "location_id": bin}, "location_id"},
		"negative stock":    {map[string]any{"name": "alias-refused", "stock_level": -1, "location_id": bin}, "stock_level"},
		"attributes":        {map[string]any{"name": "alias-refused", "attributes": map[string]any{"color": "red"}}, "attributes"},
	} {
		if code, body := a.do(http.MethodPost, items, tc.body); code != http.StatusUnprocessableEntity || body["field"] != tc.field {
			t.Errorf("POST %s: %d %v, want 422 on %s", name, code, body, tc.field)
		}
	}
	if n := countNamed("alias-refused"); n != 0 {
		t.Errorf("consumables after the refused creates: %d, want 0", n)
	}
	code, body := a.do(http.MethodPost, items, map[string]any{"name": "alias-cable", "client_id": f.Client1, "sku": "ALIAS-1",
		"stock_level": 4, "location_id": bin, "min_level": 1})
	id, _ := body["id"].(string)
	if code != http.StatusCreated || id == "" || body["stock_level"] != 4.0 || body["location_id"] != bin || body["category"] != "general" {
		t.Fatalf("POST: %d %v", code, body)
	}
	if got := onHand(t, f, id, bin); got != 4 {
		t.Errorf("stock of the created article: %v, want 4", got)
	}
	var kind, actor string
	if err := f.Admin.QueryRow(bg, `SELECT item_kind || '/' || movement_type, COALESCE(actor_id::text, '') FROM stock_movement WHERE consumable_id = $1`, id).
		Scan(&kind, &actor); err != nil || kind != "consumable/receipt" || actor != f.User {
		t.Errorf("receipt of the created article: %s by %s, %v; want consumable/receipt by %s", kind, actor, err, f.User)
	}
	if code, body = a.do(http.MethodPost, items, map[string]any{"name": "alias-dup", "sku": "ALIAS-1"}); code != http.StatusConflict {
		t.Errorf("POST with a taken SKU: %d %v, want 409", code, body)
	}

	// Read.
	if code, body = a.do(http.MethodGet, items+"/"+id, nil); code != http.StatusOK || body["stock_level"] != 4.0 || body["location_id"] != bin ||
		body["sku"] != "ALIAS-1" || body["min_level"] != 1.0 {
		t.Errorf("GET: %d %v", code, body)
	}
	if code, body = a.do(http.MethodGet, items+"?limit=50", nil); code != http.StatusOK || body["total"] != 1.0 {
		t.Errorf("GET list: %d %v, want the one article", code, body)
	}

	// Update.
	if code, body = a.do(http.MethodPatch, items+"/"+id, map[string]any{"name": "alias-cable-2", "notes": "patched"}); code != http.StatusOK ||
		body["name"] != "alias-cable-2" || body["stock_level"] != 4.0 {
		t.Errorf("PATCH: %d %v", code, body)
	}
	if n := countNamed("alias-cable-2"); n != 1 {
		t.Errorf("consumable after PATCH: %d rows with the new name, want 1", n)
	}
	for field, value := range map[string]any{"location_id": bin2, "attributes": map[string]any{}} {
		if code, body = a.do(http.MethodPatch, items+"/"+id, map[string]any{field: value}); code != http.StatusUnprocessableEntity || body["field"] != field {
			t.Errorf("PATCH %s: %d %v, want 422 on it", field, code, body)
		}
	}

	// quantity_item_id is the alias of consumable_id.
	if code, body = a.do(http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "transfer", "quantity_item_id": id,
		"from_location_id": bin, "to_location_id": bin2, "quantity": 1}); code != http.StatusCreated || body["consumable_id"] != id {
		t.Fatalf("transfer through quantity_item_id: %d %v", code, body)
	}
	if code, body = a.do(http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "receipt", "quantity_item_id": f.ID(),
		"to_location_id": bin, "quantity": 1}); code != http.StatusNotFound || body["detail"] != movement.ErrItemNotFound.Error() {
		t.Errorf("receipt of an unknown article: %d %v, want 404 %q", code, body, movement.ErrItemNotFound.Error())
	}
	_, byAlias := a.do(http.MethodGet, "/api/v1/stock-movements?quantity_item_id="+id, nil)
	_, byID := a.do(http.MethodGet, "/api/v1/stock-movements?consumable_id="+id, nil)
	if byAlias["total"] != 2.0 || byID["total"] != 2.0 {
		t.Errorf("movements by quantity_item_id %v and consumable_id %v, want the same two", byAlias["total"], byID["total"])
	}
	if _, body = a.do(http.MethodGet, items+"/"+id, nil); body["stock_level"] != 4.0 || body["location_id"] != nil {
		t.Errorf("GET with two bins: %v, want stock_level 4 and no location_id", body)
	}

	// Convert.
	convert := items + "/" + id + "/convert"
	if code, body = a.do(http.MethodPost, convert, map[string]any{"asset_tag": "ALIAS-CONV-0"}); code != http.StatusUnprocessableEntity ||
		body["field"] != "location_id" {
		t.Errorf("convert without location_id with two bins: %d %v, want 422 on location_id", code, body)
	}
	code, body = a.do(http.MethodPost, convert, map[string]any{"asset_tag": "ALIAS-CONV-1", "location_id": bin2})
	assetID, _ := body["asset_id"].(string)
	if code != http.StatusCreated || assetID == "" || body["quantity_item_id"] != id {
		t.Fatalf("convert: %d %v", code, body)
	}
	var location string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, assetID).Scan(&location); err != nil || location != bin2 {
		t.Errorf("converted asset at %q, %v; want the bin %s", location, err, bin2)
	}
	var reason string
	if err := f.Admin.QueryRow(bg, `SELECT reason FROM stock_movement WHERE consumable_id = $1 AND movement_type = 'retrieval' AND from_location_id = $2`,
		id, bin2).Scan(&reason); err != nil || reason != "converted to serialized asset "+assetID {
		t.Errorf("retrieval of the conversion: %q, %v", reason, err)
	}
	if got := onHand(t, f, id, bin2); got != 0 {
		t.Errorf("stock of the bin after the conversion: %v, want 0", got)
	}
	ledger := ledgerCount(t, f, id)
	if code, body = a.do(http.MethodPost, convert, map[string]any{"asset_tag": "ALIAS-CONV-2", "location_id": bin2}); code != http.StatusConflict ||
		body["type"] != movement.ProblemInsufficientStock {
		t.Errorf("convert from an empty bin: %d %v, want 409 %s", code, body, movement.ProblemInsufficientStock)
	}
	var assets int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM asset WHERE organization_id = $1 AND asset_tag = 'ALIAS-CONV-2'`, f.OrgA).Scan(&assets); err != nil || assets != 0 {
		t.Errorf("assets after the refused conversion: %d, %v; want none (rolled back)", assets, err)
	}
	if got := ledgerCount(t, f, id); got != ledger {
		t.Errorf("movements after the refused conversion: %d, want %d", got, ledger)
	}

	// Delete: stock keeps the article.
	if code, body = a.do(http.MethodDelete, items+"/"+id, nil); code != http.StatusConflict {
		t.Errorf("DELETE of an article with stock: %d %v, want 409", code, body)
	}

	// Route snapshot and audit scope of the article created through the
	// alias: the consumable row, visible to client 1 only.
	spec, ok := audit.RouteSnapshotFor("PATCH /api/v1/inventory/items/{id}")
	if !ok || spec.Table != "consumable" {
		t.Fatalf("snapshot of PATCH inventory/items: %+v, want table consumable", spec)
	}
	auditor := audit.NewPGRouteAuditor(f.App)
	row, table, err := auditor.RouteSnapshot(f.ClientCtx(f.Client1), f.OrgA, &spec, []any{id})
	if err != nil || table != "consumable" || row["name"] != "alias-cable-2" {
		t.Errorf("snapshot for client 1: %v %s, %v; want the consumable row", row, table, err)
	}
	if row, _, err = auditor.RouteSnapshot(f.ClientCtx(f.Client2), f.OrgA, &spec, []any{id}); err != nil || row != nil {
		t.Errorf("snapshot for client 2: %v, %v; want none", row, err)
	}
	if !audit.HasScopeRule("inventory") {
		t.Fatal("no audit scope rule for inventory")
	}
	visible := func(ctx context.Context) bool {
		t.Helper()
		var v bool
		if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT `+audit.VisibleEntries(ctx)+` FROM (SELECT 'inventory'::text AS resource_type, $1::uuid AS resource_id) audit_log`,
				id).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !visible(f.ClientCtx(f.Client1)) || visible(f.ClientCtx(f.Client2)) {
		t.Errorf("audit entry of type inventory: visible to client 1 %v, to client 2 %v; want true, false",
			visible(f.ClientCtx(f.Client1)), visible(f.ClientCtx(f.Client2)))
	}
}
