package consumable_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestConsumableRepositoryClientScope runs the consumable repository with the
// principal's tenant scope (TEN-06, WP-018): a principal restricted to client
// 1 neither sees a client-2 consumable nor its stock movements and cannot
// book stock on it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "30")
	repo := consumable.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "toner-c1", Category: "toner", Unit: "pcs"}
	foreign := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "toner-c2", Category: "toner", Unit: "pcs"}
	for _, c := range []*consumable.Consumable{own, foreign} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide consumable %s: %v", c.Name, err)
		}
		if _, err := repo.AddMovement(orgCtx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: c.ID, Direction: "in", Quantity: 5}); err != nil {
			t.Fatalf("org-wide stock movement on %s: %v", c.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	list, _, err := repo.List(ctx, f.OrgA, consumable.FilterParams{}, page)
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 consumables: %+v err=%v, want only the own one", list, err)
	}
	moves, total, err := repo.ListMovements(ctx, f.OrgA, foreign.ID, page)
	if err != nil || total != 0 || len(moves) != 0 {
		t.Fatalf("client-1 movements of client-2 consumable: total=%d err=%v, want none", total, err)
	}
	if _, err = repo.AddMovement(ctx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: foreign.ID, Direction: "out", Quantity: 1}); err == nil {
		t.Fatal("client-1 principal booked stock on a client-2 consumable")
	}
	var level float64
	if err = f.Admin.QueryRow(context.Background(), `SELECT stock_level FROM consumable WHERE id = $1`, foreign.ID).Scan(&level); err != nil || level != 5 {
		t.Fatalf("client-2 stock level: %v err=%v, want 5", level, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, consumable.FilterParams{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
