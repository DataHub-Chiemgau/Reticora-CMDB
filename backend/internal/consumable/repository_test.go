package consumable

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestStockMovementAdjustsLevel(t *testing.T) {
	repo := NewMemoryRepository()
	c := &Consumable{OrganizationID: "org-1", Name: "Cat6 cable", StockLevel: 10, MinLevel: 5}
	if err := repo.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// in +3
	updated, err := repo.AddMovement(context.Background(), &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "in", Quantity: 3})
	if err != nil || updated.StockLevel != 13 {
		t.Fatalf("expected 13 after in, got %+v err=%v", updated, err)
	}
	// out 5
	updated, err = repo.AddMovement(context.Background(), &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "out", Quantity: 5})
	if err != nil || updated.StockLevel != 8 {
		t.Fatalf("expected 8 after out, got %+v err=%v", updated, err)
	}
	// out beyond stock must fail
	if _, err = repo.AddMovement(context.Background(), &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "out", Quantity: 100}); err == nil {
		t.Fatal("expected insufficient stock error")
	}
	// low stock filter
	items, _, _ := repo.List(context.Background(), "org-1", FilterParams{LowStock: true}, api.PaginationParams{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("8 > 5 min, expected no low stock, got %d", len(items))
	}
	repo.AddMovement(context.Background(), &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "out", Quantity: 5})
	items, _, _ = repo.List(context.Background(), "org-1", FilterParams{LowStock: true}, api.PaginationParams{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("3 <= 5 min, expected low stock item, got %d", len(items))
	}
}
