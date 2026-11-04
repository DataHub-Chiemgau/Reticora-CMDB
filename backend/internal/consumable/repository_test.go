package consumable

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
)

// TestMemoryStockPerBin covers the stock per bin of the in-memory store
// (No-DB profile, WP-283): bookings per type through Book, the shortfall of
// a bin (ErrInsufficientStock, matching movement.ErrInsufficientStock,
// nothing changed), the rules per type, the sum as stock_level, low_stock
// over it, the transitional movements with and without bin and the delete
// kept by stock.
func TestMemoryStockPerBin(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	c := &Consumable{OrganizationID: "org-1", Name: "Cat6 cable", MinLevel: 5}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	book := func(kind, from, to string, qty float64) error {
		return repo.Book(ctx, &Booking{OrganizationID: "org-1", ConsumableID: c.ID, MovementType: kind,
			FromLocationID: from, ToLocationID: to, Quantity: qty})
	}
	stock := func() map[string]float64 {
		t.Helper()
		rows, _, err := repo.ListStock(ctx, "org-1", c.ID, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, s := range rows {
			out[s.LocationID] = s.OnHand
		}
		return out
	}
	if err := book("receipt", "", "bin-a", 10); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if err := book("transfer", "bin-a", "bin-b", 4); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if err := book("correction", "", "bin-b", 1); err != nil {
		t.Fatalf("correction up: %v", err)
	}
	if err := book("correction", "bin-a", "", 2); err != nil {
		t.Fatalf("correction down: %v", err)
	}
	if err := book("retrieval", "bin-b", "", 6); !errors.Is(err, ErrInsufficientStock) || !errors.Is(err, movement.ErrInsufficientStock) {
		t.Fatalf("retrieval beyond the bin: %v, want ErrInsufficientStock", err)
	}
	if err := book("transfer", "bin-a", "bin-c", 5); !IsInsufficientStock(err) {
		t.Fatalf("transfer beyond the bin: %v, want ErrInsufficientStock", err)
	}
	if got := stock(); len(got) != 2 || got["bin-a"] != 4 || got["bin-b"] != 5 {
		t.Fatalf("stock per bin %v, want bin-a 4 and bin-b 5", got)
	}
	for name, b := range map[string]struct {
		kind, from, to string
		qty            float64
		field          string
	}{
		"receipt without bin":      {"receipt", "", "", 1, "to_location_id"},
		"receipt from a bin":       {"receipt", "bin-a", "bin-b", 1, "to_location_id"},
		"retrieval into a bin":     {"retrieval", "", "bin-a", 1, "from_location_id"},
		"correction with two bins": {"correction", "bin-a", "bin-b", 1, "to_location_id"},
		"transfer within a bin":    {"transfer", "bin-a", "bin-a", 1, "to_location_id"},
		"zero quantity":            {"receipt", "", "bin-a", 0, "quantity"},
		"reserved type":            {"disposal", "bin-a", "", 1, "movement_type"},
	} {
		var fe *api.FieldError
		if err := book(b.kind, b.from, b.to, b.qty); !errors.As(err, &fe) || fe.Field != b.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 on %s", name, err, b.field)
		}
	}

	got, err := repo.GetByID(ctx, "org-1", c.ID)
	if err != nil || got.StockLevel != 9 {
		t.Fatalf("stock_level %+v, %v; want the sum 9", got, err)
	}
	low := func() int {
		items, _, listErr := repo.List(ctx, "org-1", FilterParams{LowStock: true}, api.PaginationParams{Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		return len(items)
	}
	if n := low(); n != 0 {
		t.Fatalf("9 > 5: %d low-stock items, want none", n)
	}

	// The transitional path: several bins need location_id.
	var fe *api.FieldError
	if _, err = repo.AddMovement(ctx, &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "out", Quantity: 1}); !errors.As(err, &fe) || fe.Field != "location_id" {
		t.Fatalf("out without bin on two bins: %v, want 422 on location_id", err)
	}
	updated, err := repo.AddMovement(ctx, &Movement{OrganizationID: "org-1", ConsumableID: c.ID, Direction: "out", LocationID: "bin-b", Quantity: 5})
	if err != nil || updated.StockLevel != 4 {
		t.Fatalf("out of bin-b: %+v, %v; want stock_level 4", updated, err)
	}
	if n := low(); n != 1 {
		t.Fatalf("4 <= 5: %d low-stock items, want one", n)
	}

	// Delete: stock keeps the consumable, an empty bin does not.
	if err = repo.Delete(ctx, "org-1", c.ID); !errors.Is(err, ErrHasStock) {
		t.Fatalf("delete with stock: %v, want ErrHasStock", err)
	}
	if err = book("retrieval", "bin-a", "", 4); err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, "org-1", c.ID); err != nil {
		t.Fatalf("delete with empty bins: %v", err)
	}

	// A consumable with exactly one bin books without location_id.
	one := &Consumable{OrganizationID: "org-1", Name: "Toner"}
	if err = repo.Create(ctx, one); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.AddMovement(ctx, &Movement{OrganizationID: "org-1", ConsumableID: one.ID, Direction: "in", Quantity: 1}); !errors.As(err, &fe) {
		t.Fatalf("in without any bin: %v, want 422", err)
	}
	if _, err = repo.AddMovement(ctx, &Movement{OrganizationID: "org-1", ConsumableID: one.ID, Direction: "in", LocationID: "bin-x", Quantity: 3}); err != nil {
		t.Fatal(err)
	}
	if updated, err = repo.AddMovement(ctx, &Movement{OrganizationID: "org-1", ConsumableID: one.ID, Direction: "out", Quantity: 2}); err != nil || updated.StockLevel != 1 {
		t.Fatalf("out of the only bin: %+v, %v; want 1", updated, err)
	}
}

// TestStockPageCursor covers the keyset page of the stock list: the page
// ends at the limit and the cursor continues after its last bin.
func TestStockPageCursor(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	c := &Consumable{OrganizationID: "org-1", Name: "SFP"}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"bin-c", "bin-a", "bin-b"} {
		if err := repo.Book(ctx, &Booking{OrganizationID: "org-1", ConsumableID: c.ID, MovementType: "receipt", ToLocationID: bin, Quantity: 1}); err != nil {
			t.Fatal(err)
		}
	}
	page := api.PaginationParams{Limit: 2}
	rows, total, err := repo.ListStock(ctx, "org-1", c.ID, page)
	if err != nil {
		t.Fatal(err)
	}
	first := StockPage(rows, total, page)
	if total != 3 || len(first.Data) != 2 || !first.HasMore || first.NextCursor == "" || first.Data[0].LocationID != "bin-a" {
		t.Fatalf("first page %+v", first)
	}
	cursor, err := api.DecodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	page.Cursor = &cursor
	rows, total, err = repo.ListStock(ctx, "org-1", c.ID, page)
	if err != nil {
		t.Fatal(err)
	}
	second := StockPage(rows, total, page)
	if len(second.Data) != 1 || second.HasMore || second.Data[0].LocationID != "bin-c" {
		t.Fatalf("second page %+v", second)
	}
	if _, _, err = repo.ListStock(ctx, "org-1", "unknown", page); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stock of an unknown consumable: %v, want ErrNotFound", err)
	}
}
