package movement_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
)

// TestMovementRepositoryClientScope runs the movement ledger and the
// articles of the alias inventory/items with the principal's tenant scope
// (TEN-06, WP-018): a principal restricted to client 1 neither sees nor
// records movements of client-2 assets and sees no client-2 article and no
// client-2 stock. The articles live in consumable and stock_level since
// WP-757 (E-40).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMovementRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2f-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2f-foreign")

	repo := movement.NewPGRepository(f.App)
	mv := func(assetID string) *movement.Movement {
		return &movement.Movement{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: assetID, MovementType: "receipt"}
	}
	orgCtx := f.OrgCtx(f.OrgA)
	for _, id := range []string{own, foreign} {
		if err := repo.Record(orgCtx, mv(id)); err != nil {
			t.Fatalf("org-wide movement of %s: %v", id, err)
		}
	}
	bins := map[string]string{f.Client1: stockBin(t, f, f.Client1, "scope-bin-c1"), f.Client2: stockBin(t, f, f.Client2, "scope-bin-c2")}
	items := map[string]*movement.QuantityItem{}
	for client, name := range map[string]string{f.Client1: "cable-c1", f.Client2: "cable-c2"} {
		it := &movement.QuantityItem{OrganizationID: f.OrgA, ClientID: client, Name: name, Category: "cable", Unit: "pcs",
			StockLevel: 5, LocationID: bins[client]}
		if err := repo.CreateItem(orgCtx, it); err != nil {
			t.Fatalf("org-wide article %s: %v", name, err)
		}
		items[client] = it
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	list, total, err := repo.ListMovements(ctx, f.OrgA, &movement.MovementFilter{AssetID: own}, page)
	if err != nil || total != 1 || len(list) != 1 || list[0].AssetID != own {
		t.Fatalf("client-1 movements of the own asset: total=%d %+v err=%v, want the one", total, list, err)
	}
	if list, _, err = repo.ListMovements(ctx, f.OrgA, &movement.MovementFilter{AssetID: foreign}, page); err != nil || len(list) != 0 {
		t.Fatalf("client-1 movements of the client-2 asset: %+v err=%v, want none", list, err)
	}
	if err = repo.Record(ctx, mv(foreign)); err == nil {
		t.Fatal("client-1 principal recorded a movement of a client-2 asset")
	}
	got, _, err := repo.ListItems(ctx, f.OrgA, page)
	if err != nil || len(got) != 1 || got[0].Name != "cable-c1" || got[0].StockLevel != 5 || got[0].LocationID != bins[f.Client1] {
		t.Fatalf("client-1 articles: %+v err=%v, want only cable-c1 with 5 in its bin", got, err)
	}
	if _, err = repo.GetItem(ctx, f.OrgA, items[f.Client2].ID); !errors.Is(err, movement.ErrItemNotFound) {
		t.Errorf("client-1 read of the client-2 article: %v, want ErrItemNotFound", err)
	}
	if _, _, err = consumable.NewPGRepository(f.App).ListStock(ctx, f.OrgA, items[f.Client2].ID, page); !errors.Is(err, consumable.ErrNotFound) {
		t.Errorf("client-1 stock of the client-2 article: %v, want consumable.ErrNotFound", err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM stock_movement WHERE asset_id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 movements: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.ListMovements(context.Background(), f.OrgA, &movement.MovementFilter{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestQuantityItemForeignClientUpdate covers the writes on an existing
// article through the alias (TEN-06, WP-537, WP-757): a principal
// restricted to client 1 can neither update a client-2 article nor move
// stock of it by quantity_item_id (ErrItemNotFound, 404); the consumable
// row keeps its fields, its stock_level row its on_hand, and no movement
// arises. Its own article it updates and books.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestQuantityItemForeignClientUpdate(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	bg := context.Background()
	repo := movement.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownBin, foreignBin := stockBin(t, f, f.Client1, "upd-bin-c1"), stockBin(t, f, f.Client2, "upd-bin-c2")
	own := &movement.QuantityItem{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "cable-c1", Category: "cable", Unit: "pcs",
		StockLevel: 10, LocationID: ownBin, Notes: "original"}
	foreign := &movement.QuantityItem{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "cable-c2", Category: "cable", Unit: "pcs",
		StockLevel: 10, LocationID: foreignBin, Notes: "original"}
	for _, it := range []*movement.QuantityItem{own, foreign} {
		if err := repo.CreateItem(orgCtx, it); err != nil {
			t.Fatalf("org-wide article %s: %v", it.Name, err)
		}
	}
	type row struct {
		client, name, notes string
		onHand              float64
		movements           int
	}
	stored := func(id, bin string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(c.client_id::text, ''), c.name, COALESCE(c.notes, ''),
			(SELECT on_hand::float8 FROM stock_level WHERE consumable_id = c.id AND location_id = $2),
			(SELECT count(*) FROM stock_movement WHERE consumable_id = c.id) FROM consumable c WHERE c.id = $1`, id, bin).
			Scan(&r.client, &r.name, &r.notes, &r.onHand, &r.movements); err != nil {
			t.Fatalf("stored article %s: %v", id, err)
		}
		return r
	}
	// The receipt of CreateItem is the one movement of each article.
	want := row{client: f.Client2, name: "cable-c2", notes: "original", onHand: 10, movements: 1}
	name, notes := "hijacked", "hijacked"
	req := movement.UpdateItemRequest{Name: &name, Notes: &notes}
	qty := 4.0
	mv := func(itemID, kind, from, to string) *movement.Movement {
		return &movement.Movement{OrganizationID: f.OrgA, ItemKind: "quantity_item", QuantityItemID: itemID, MovementType: kind,
			FromLocationID: from, ToLocationID: to, Quantity: &qty}
	}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.UpdateItem(ctx, f.OrgA, foreign.ID, req); !errors.Is(err, movement.ErrItemNotFound) {
		t.Errorf("client-1 update of a client-2 article: err=%v, want ErrItemNotFound", err)
	}
	for _, m := range []*movement.Movement{mv(foreign.ID, "retrieval", foreignBin, ""), mv(foreign.ID, "receipt", "", foreignBin),
		mv(foreign.ID, "correction", "", foreignBin)} {
		if err := repo.Record(ctx, m); !errors.Is(err, movement.ErrItemNotFound) {
			t.Errorf("client-1 %s of a client-2 article: err=%v, want ErrItemNotFound", m.MovementType, err)
		}
	}
	if got := stored(foreign.ID, foreignBin); got != want {
		t.Errorf("client-2 article after foreign writes: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates its own article and
	// retrieves stock from it through the alias.
	if it, err := repo.UpdateItem(ctx, f.OrgA, own.ID, req); err != nil || it.Name != name || it.StockLevel != 10 || it.LocationID != ownBin {
		t.Fatalf("client-1 update of own article: %+v err=%v", it, err)
	}
	m := mv(own.ID, "retrieval", ownBin, "")
	if err := repo.Record(ctx, m); err != nil {
		t.Fatalf("client-1 retrieval from own article: %v", err)
	}
	if m.ItemKind != "consumable" || m.ConsumableID != own.ID || m.QuantityItemID != "" {
		t.Errorf("booked alias movement: %s %s %q, want consumable %s", m.ItemKind, m.ConsumableID, m.QuantityItemID, own.ID)
	}
	if got := stored(own.ID, ownBin); got != (row{client: f.Client1, name: name, notes: notes, onHand: 6, movements: 2}) {
		t.Errorf("own article after update and retrieval: %+v", got)
	}
}
