package movement_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
)

// TestMovementRepositoryClientScope runs the movement ledger and quantity
// items with the principal's tenant scope (TEN-06, WP-018): a principal
// restricted to client 1 neither sees nor records movements of client-2
// assets and sees no client-2 quantity item.
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
	for _, it := range []*movement.QuantityItem{
		{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "cable-c1", Category: "cable", Unit: "pcs", Attributes: map[string]any{}},
		{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "cable-c2", Category: "cable", Unit: "pcs", Attributes: map[string]any{}},
	} {
		if err := repo.CreateItem(orgCtx, it); err != nil {
			t.Fatalf("org-wide quantity item %s: %v", it.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	list, total, err := repo.ListMovements(ctx, f.OrgA, movement.MovementFilter{}, page)
	if err != nil || total != 1 || len(list) != 1 || list[0].AssetID != own {
		t.Fatalf("client-1 movements: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if err = repo.Record(ctx, mv(foreign)); err == nil {
		t.Fatal("client-1 principal recorded a movement of a client-2 asset")
	}
	items, _, err := repo.ListItems(ctx, f.OrgA, page)
	if err != nil || len(items) != 1 || items[0].Name != "cable-c1" {
		t.Fatalf("client-1 quantity items: %+v err=%v, want only cable-c1", items, err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM asset_movement WHERE asset_id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 movements: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.ListMovements(context.Background(), f.OrgA, movement.MovementFilter{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestQuantityItemForeignClientUpdate covers the writes on an existing
// quantity item (TEN-06, WP-537): a principal restricted to client 1 can
// neither update a client-2 quantity item nor move stock of it
// (ErrItemNotFound, 404); the client-2 row keeps its fields and stock level
// and gets no movement. Its own item it updates and moves.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestQuantityItemForeignClientUpdate(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	bg := context.Background()
	repo := movement.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &movement.QuantityItem{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "cable-c1", Category: "cable", Unit: "pcs", StockLevel: 10, Notes: "original", Attributes: map[string]any{}}
	foreign := &movement.QuantityItem{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "cable-c2", Category: "cable", Unit: "pcs", StockLevel: 10, Notes: "original", Attributes: map[string]any{}}
	for _, it := range []*movement.QuantityItem{own, foreign} {
		if err := repo.CreateItem(orgCtx, it); err != nil {
			t.Fatalf("org-wide quantity item %s: %v", it.Name, err)
		}
	}
	type row struct {
		client, name, notes string
		level               float64
		movements           int
	}
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), name, COALESCE(notes, ''), stock_level,
			(SELECT count(*) FROM asset_movement WHERE quantity_item_id = quantity_item.id) FROM quantity_item WHERE id = $1`, id).
			Scan(&r.client, &r.name, &r.notes, &r.level, &r.movements); err != nil {
			t.Fatalf("stored quantity item %s: %v", id, err)
		}
		return r
	}
	want := row{client: f.Client2, name: "cable-c2", notes: "original", level: 10}
	name, notes := "hijacked", "hijacked"
	req := movement.UpdateItemRequest{Name: &name, Notes: &notes, Attributes: map[string]any{"owner": "client-1"}}
	qty := 4.0
	mv := func(itemID, kind string) *movement.Movement {
		return &movement.Movement{OrganizationID: f.OrgA, ItemKind: "quantity_item", QuantityItemID: itemID, MovementType: kind, Quantity: &qty}
	}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.UpdateItem(ctx, f.OrgA, foreign.ID, req); !errors.Is(err, movement.ErrItemNotFound) {
		t.Errorf("client-1 update of a client-2 quantity item: err=%v, want ErrItemNotFound", err)
	}
	for _, kind := range []string{"deployment", "receipt", "correction"} {
		if err := repo.Record(ctx, mv(foreign.ID, kind)); !errors.Is(err, movement.ErrItemNotFound) {
			t.Errorf("client-1 %s movement of a client-2 quantity item: err=%v, want ErrItemNotFound", kind, err)
		}
	}
	if got := stored(foreign.ID); got != want {
		t.Errorf("client-2 quantity item after foreign writes: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates its own item and
	// deploys stock from it.
	if it, err := repo.UpdateItem(ctx, f.OrgA, own.ID, req); err != nil || it.Name != name {
		t.Fatalf("client-1 update of own quantity item: %+v err=%v", it, err)
	}
	if err := repo.Record(ctx, mv(own.ID, "deployment")); err != nil {
		t.Fatalf("client-1 deployment from own quantity item: %v", err)
	}
	if got := stored(own.ID); got != (row{client: f.Client1, name: name, notes: notes, level: 6, movements: 1}) {
		t.Errorf("own quantity item after update and deployment: %+v", got)
	}
}
