package locations_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLocationBarcodeRegistry covers WP-289 (INV-03): PATCH
// /api/v1/locations/{id} assigns and removes location.barcode with the audit
// location.barcode_changed; a code is unique per organization over assets
// (barcode and inventory number), consumables and locations through
// barcode_registry, also against an asset of a client the caller cannot
// see; PATCH of an asset or a consumable with the code of a location is 409,
// not 404; of two parallel PATCH with one code exactly one wins; a code
// change and a delete clean the register up; the 409 does not name the
// object holding the code; a client-bound principal neither changes nor
// deletes a register row without client.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationBarcodeRegistry(t *testing.T) {
	f := scopetest.Seed(t, "46")
	mux := chi.NewRouter()
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(mux)
	asset.NewHandler(asset.NewPGRepository(f.App)).RegisterRoutes(mux)
	consumable.NewHandler(consumable.NewPGRepository(f.App)).RegisterRoutes(mux)
	principal := func(ctx context.Context) context.Context {
		return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	}
	orgCtx, client1Ctx := principal(f.OrgCtx(f.OrgA)), principal(f.ClientCtx(f.Client1))
	// send is safe for goroutines: it reports nothing through t.
	send := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	do := func(ctx context.Context, method, path, body string) (int, map[string]any) {
		t.Helper()
		w := send(ctx, method, path, body)
		out := map[string]any{}
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s %s: %d %q: %v", method, path, w.Code, w.Body.String(), err)
			}
		}
		return w.Code, out
	}
	create := func(body string) string {
		t.Helper()
		code, out := do(orgCtx, http.MethodPost, "/api/v1/locations", body)
		if code != http.StatusCreated {
			t.Fatalf("POST %s = %d %v, want 201", body, code, out)
		}
		return out["id"].(string)
	}
	setBarcode := func(ctx context.Context, path, value string) (int, map[string]any) {
		t.Helper()
		return do(ctx, http.MethodPatch, path, `{"barcode":"`+value+`"}`)
	}
	// register returns the register rows of a code as "source object
	// client site" (empty for NULL).
	register := func(code string) []string {
		t.Helper()
		rows, err := f.Admin.Query(context.Background(), `
			SELECT source || ' ' || COALESCE(asset_id, consumable_id, location_id)::text || ' '
			       || COALESCE(client_id::text, '') || ' ' || COALESCE(site_id::text, '')
			  FROM barcode_registry WHERE organization_id = $1 AND code = $2`, f.OrgA, code)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// wantConflict checks a 409 problem that names no holder of the code.
	wantConflict := func(t *testing.T, what string, code int, out map[string]any, field string, holder ...string) {
		t.Helper()
		if code != http.StatusConflict {
			t.Errorf("%s = %d %v, want 409", what, code, out)
			return
		}
		if field != "" && out["field"] != field {
			t.Errorf("%s: field %v, want %s", what, out["field"], field)
		}
		for key, value := range out {
			if s, ok := value.(string); ok && slices.Contains(holder, s) {
				t.Errorf("%s: %q names the holder of the code: %v", what, key, out)
			}
		}
	}

	site := create(`{"kind":"site","client_id":"` + f.Client1 + `","name":"bc-site"}`)
	wh1 := create(`{"kind":"warehouse","parent_id":"` + site + `","name":"bc-wh-1"}`)
	wh2 := create(`{"kind":"warehouse","parent_id":"` + site + `","name":"bc-wh-2"}`)
	foreignAsset := f.Asset(t, f.OrgA, f.Client2, "BC-INV-2")
	consumableID := f.ID()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE asset SET barcode = 'BC-ASSET-2' WHERE id = $1`, []any{foreignAsset}},
		{`INSERT INTO consumable (id, organization_id, name, barcode) VALUES ($1, $2, 'bc-toner', 'BC-CONS')`, []any{consumableID, f.OrgA}},
	} {
		if _, err := f.Admin.Exec(context.Background(), s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	if got := register("BC-INV-2"); len(got) != 1 || got[0] != "inventory_no "+foreignAsset+" "+f.Client2+" " {
		t.Errorf("register of the inventory number: %v", got)
	}
	if got := register("BC-CONS"); len(got) != 1 || got[0] != "barcode "+consumableID+"  " {
		t.Errorf("register of the consumable: %v", got)
	}

	// Assign, also on a site (specialist table): the register row carries
	// client and site of the location, the audit old and new value.
	code, out := setBarcode(orgCtx, "/api/v1/locations/"+site, "BC-SITE")
	if code != http.StatusOK || out["barcode"] != "BC-SITE" {
		t.Fatalf("assign to the site = %d %v, want 200 with the barcode", code, out)
	}
	if got := register("BC-SITE"); len(got) != 1 || got[0] != "barcode "+site+" "+f.Client1+" "+site {
		t.Errorf("register after assign: %v", got)
	}
	var changes map[string]map[string]string
	if err := f.Admin.QueryRow(context.Background(), `SELECT changes FROM audit_log
		WHERE organization_id = $1 AND action = 'location.barcode_changed' AND resource_id = $2`, f.OrgA, site).Scan(&changes); err != nil {
		t.Fatalf("audit location.barcode_changed: %v", err)
	}
	if b := changes["barcode"]; b["old"] != "" || b["new"] != "BC-SITE" {
		t.Errorf("audit changes %v, want barcode old empty and new BC-SITE", changes)
	}
	if code, out = do(orgCtx, http.MethodGet, "/api/v1/locations/"+site, ""); code != http.StatusOK || out["barcode"] != "BC-SITE" {
		t.Errorf("GET after assign = %d %v", code, out)
	}

	// Duplicates: a location of client 1 against the barcode and the
	// inventory number of an asset of client 2 the caller cannot see, a
	// consumable and another location.
	for _, c := range []struct {
		value  string
		holder []string
	}{
		{"BC-ASSET-2", []string{foreignAsset, "BC-INV-2"}},
		{"BC-INV-2", []string{foreignAsset, "BC-ASSET-2"}},
		{"BC-CONS", []string{consumableID, "bc-toner"}},
		{"BC-SITE", []string{site, "bc-site"}},
	} {
		code, out = setBarcode(client1Ctx, "/api/v1/locations/"+wh1, c.value)
		wantConflict(t, "PATCH location with "+c.value, code, out, "barcode", c.holder...)
	}
	if got := register("BC-ASSET-2"); len(got) != 1 || got[0] != "barcode "+foreignAsset+" "+f.Client2+" " {
		t.Errorf("register of the asset barcode after the refused PATCH: %v", got)
	}
	var stored *string
	if err := f.Admin.QueryRow(context.Background(), `SELECT barcode FROM location WHERE id = $1`, wh1).Scan(&stored); err != nil || stored != nil {
		t.Errorf("barcode of the refused location: %v, %v; want NULL", stored, err)
	}

	// Asset and consumable with the code of a location: 409, not 404.
	code, out = setBarcode(orgCtx, "/api/v1/assets/"+foreignAsset, "BC-SITE")
	wantConflict(t, "PATCH asset with the code of a location", code, out, "", site, "bc-site")
	code, out = setBarcode(orgCtx, "/api/v1/consumables/"+consumableID, "BC-SITE")
	wantConflict(t, "PATCH consumable with the code of a location", code, out, "", site, "bc-site")

	// Two parallel PATCH with one code: exactly one wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, id := range []string{wh1, wh2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = send(orgCtx, http.MethodPatch, "/api/v1/locations/"+id, `{"barcode":"BC-RACE"}`).Code
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Errorf("parallel PATCH = %v, want one 200 and one 409", codes)
	}
	if got := register("BC-RACE"); len(got) != 1 {
		t.Errorf("register after the race: %v, want one row", got)
	}

	// A code change frees the old code; removing it frees the new one.
	if code, out = setBarcode(orgCtx, "/api/v1/locations/"+site, "BC-SITE-2"); code != http.StatusOK || out["barcode"] != "BC-SITE-2" {
		t.Fatalf("change the code = %d %v", code, out)
	}
	if got := register("BC-SITE"); len(got) != 0 {
		t.Errorf("register of the old code: %v, want none", got)
	}
	if code, out = setBarcode(orgCtx, "/api/v1/consumables/"+consumableID, "BC-SITE"); code != http.StatusOK {
		t.Errorf("PATCH consumable with the freed code = %d %v, want 200", code, out)
	}
	if code, out = setBarcode(orgCtx, "/api/v1/locations/"+site, ""); code != http.StatusOK || out["barcode"] != nil {
		t.Errorf("remove the code = %d %v, want 200 without barcode", code, out)
	}
	if got := register("BC-SITE-2"); len(got) != 0 {
		t.Errorf("register after removal: %v, want none", got)
	}

	// A client-bound principal neither changes nor deletes the register
	// row of the consumable without client.
	err := database.WithRequestTenant(f.ClientCtx(f.Client1), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		for _, sql := range []string{
			`UPDATE barcode_registry SET code = code || '-x' WHERE consumable_id = $1`,
			`DELETE FROM barcode_registry WHERE consumable_id = $1`,
		} {
			tag, execErr := tx.Exec(ctx, sql, consumableID)
			if execErr != nil {
				return execErr
			}
			if tag.RowsAffected() != 0 {
				t.Errorf("%s as client 1: %d rows, want none", sql, tag.RowsAffected())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Deleting the objects removes their register rows.
	for _, id := range []string{wh1, wh2} {
		if code, out = do(orgCtx, http.MethodDelete, "/api/v1/locations/"+id, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE location = %d %v", code, out)
		}
	}
	if got := register("BC-RACE"); len(got) != 0 {
		t.Errorf("register after deleting the locations: %v, want none", got)
	}
	if _, err = f.Admin.Exec(context.Background(), `DELETE FROM asset WHERE id = $1`, foreignAsset); err != nil {
		t.Fatal(err)
	}
	if got := append(register("BC-ASSET-2"), register("BC-INV-2")...); len(got) != 0 {
		t.Errorf("register after deleting the asset: %v, want none", got)
	}
}

// TestMigration255BackfillsBarcodeRegistry runs the real SQL of migration
// 000255 (WP-289, E-26) inside one transaction that is always rolled back:
// it reverts 000255, seeds assets, consumables and retired location nodes
// with codes, migrates up and down again. The codes of a directly mapped bin
// and room node are taken over, that of a floor node mapped onto its site is
// not; a location with two mapped nodes with a code gets none; codes taken
// already go to migration_quarantine, and the down part writes the barcodes
// of assets and consumables back.
func TestMigration255BackfillsBarcodeRegistry(t *testing.T) {
	f := scopetest.Seed(t, "d1")
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/000255_barcode_registry." + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	up, down := read("up"), read("down")
	ctx := context.Background()
	tree := locations.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	node := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(orgCtx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	room := f.Room(t, f.Client1, "mig255")
	var site string
	if err := f.Admin.QueryRow(ctx, `SELECT site_id::text FROM location WHERE id = $1`, room).Scan(&site); err != nil {
		t.Fatal(err)
	}
	wh := node(locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: site, Name: "mig255-wh"})
	zone := node(locations.CreateRequest{Kind: locations.KindZone, ParentID: wh, Name: "mig255-zone"})
	shelf := node(locations.CreateRequest{Kind: locations.KindShelf, ParentID: zone, Name: "mig255-shelf"})
	bin := node(locations.CreateRequest{Kind: locations.KindBin, ParentID: shelf, Name: "mig255-bin"})

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
	exec(down)

	// Retired nodes: the floor node maps onto the site, the shelf code is
	// the inventory number of an asset, the warehouse has two coded nodes.
	for _, n := range []struct{ kind, location, code string }{
		{"bin", bin, "MIG-BIN"},
		{"room", room, "MIG-ROOM"},
		{"floor", site, "MIG-FLOOR"},
		{"shelf", shelf, "MIG-INV-A"},
		{"warehouse", wh, "MIG-WH-1"},
		{"warehouse", wh, "MIG-WH-2"},
	} {
		exec(`INSERT INTO location_node_retired (id, organization_id, client_id, site_id, location_id, node)
			SELECT $1::uuid, $2, l.client_id, l.site_id, l.id,
			       jsonb_build_object('id', $1::text, 'node_type', $4::text, 'name', $4::text, 'barcode', $5::text)
			  FROM location l WHERE l.id = $3`, f.ID(), f.OrgA, n.location, n.kind, n.code)
	}
	assetA, assetB, assetC := f.ID(), f.ID(), f.ID()
	for _, a := range []struct{ id, client, inventory, barcode string }{
		{assetA, f.Client1, "MIG-INV-A", "MIG-BC-A"},
		{assetB, "", "MIG-INV-B", "MIG-INV-A"}, // the inventory number of A
		{assetC, f.Client2, "MIG-INV-C", "MIG-INV-C"},
	} {
		exec(`INSERT INTO asset (id, organization_id, client_id, asset_tag, name, barcode)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $4, $5)`, a.id, f.OrgA, a.client, a.inventory, a.barcode)
	}
	consA, consB := f.ID(), f.ID()
	exec(`INSERT INTO consumable (id, organization_id, client_id, name, barcode)
		VALUES ($1, $2, $3, 'mig255-a', 'MIG-BC-A'), ($4, $2, NULL, 'mig255-b', 'MIG-CONS')`, consA, f.OrgA, f.Client1, consB)

	exec(up)

	rows, err := tx.Query(ctx, `
		SELECT code || ' ' || source || ' ' || COALESCE(asset_id, consumable_id, location_id)::text
		  FROM barcode_registry WHERE organization_id = $1 ORDER BY code`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"MIG-BC-A barcode " + assetA,
		"MIG-BIN barcode " + bin,
		"MIG-CONS barcode " + consB,
		"MIG-INV-A inventory_no " + assetA,
		"MIG-INV-B inventory_no " + assetB,
		"MIG-INV-C inventory_no " + assetC,
		"MIG-ROOM barcode " + room,
	}
	if !slices.Equal(got, want) {
		t.Errorf("register after up:\n  %v\nwant\n  %v", got, want)
	}
	readBarcode := func(table, id string) string {
		t.Helper()
		var v *string
		if scanErr := tx.QueryRow(ctx, `SELECT barcode FROM `+table+` WHERE id = $1`, id).Scan(&v); scanErr != nil {
			t.Fatal(scanErr)
		}
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	for _, c := range []struct{ table, id, want string }{
		{"location", bin, "MIG-BIN"},
		{"location", room, "MIG-ROOM"},
		{"location", site, "<nil>"},
		{"location", shelf, "<nil>"},
		{"location", wh, "<nil>"},
		{"asset", assetB, "<nil>"},
		{"consumable", consA, "<nil>"},
		{"asset", assetC, "MIG-INV-C"},
	} {
		if v := readBarcode(c.table, c.id); v != c.want {
			t.Errorf("%s %s barcode after up: %s, want %s", c.table, c.id, v, c.want)
		}
	}
	rows, err = tx.Query(ctx, `
		SELECT source_table || ' ' || reason || ' ' || COALESCE(row_data ->> 'location_id', row_data ->> 'id')
		  FROM migration_quarantine WHERE migration = '000255' AND organization_id = $1 ORDER BY 1`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"asset barcode_conflict " + assetB,
		"consumable barcode_conflict " + consA,
		"location_node_retired barcode_ambiguous " + wh,
		"location_node_retired barcode_ambiguous " + wh,
		"location_node_retired barcode_conflict " + shelf,
	}
	if !slices.Equal(got, want) {
		t.Errorf("quarantine after up:\n  %v\nwant\n  %v", got, want)
	}
	// The triggers keep the register after the backfill.
	exec(`UPDATE location SET barcode = 'MIG-SHELF' WHERE id = $1`, shelf)
	var registered int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM barcode_registry WHERE location_id = $1 AND code = 'MIG-SHELF'`, shelf).Scan(&registered); err != nil || registered != 1 {
		t.Errorf("register row of the shelf after the backfill: %d, %v; want 1", registered, err)
	}

	exec(down)

	for _, c := range []struct{ table, id, want string }{
		{"asset", assetB, "MIG-INV-A"},
		{"consumable", consA, "MIG-BC-A"},
		{"asset", assetA, "MIG-BC-A"},
	} {
		if v := readBarcode(c.table, c.id); v != c.want {
			t.Errorf("%s %s barcode after down: %s, want %s", c.table, c.id, v, c.want)
		}
	}
	var left int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine WHERE migration = '000255'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("quarantine rows of 000255 after down: %d, %v; want none", left, err)
	}
	var registry *string
	if err = tx.QueryRow(ctx, `SELECT to_regclass('barcode_registry')::text`).Scan(&registry); err != nil || registry != nil {
		t.Errorf("barcode_registry after down: %v, %v; want dropped", registry, err)
	}
}
