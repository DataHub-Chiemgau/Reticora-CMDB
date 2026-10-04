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
