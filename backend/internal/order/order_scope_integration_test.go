package order_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
)

// TestOrderRepositoryClientScope runs the internal order repository with the
// principal's tenant scope (TEN-06, WP-018): a principal restricted to client
// 1 neither sees a client-2 order nor its items, and cannot put a client-2
// asset on its own order.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrderRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "31")
	foreignAsset := f.Asset(t, f.OrgA, f.Client2, "scopetest-31-foreign")
	repo := order.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &order.Order{OrganizationID: f.OrgA, ClientID: f.Client1, Title: "order-c1", Status: "draft"}
	foreign := &order.Order{OrganizationID: f.OrgA, ClientID: f.Client2, Title: "order-c2", Status: "draft"}
	for _, o := range []*order.Order{own, foreign} {
		if err := repo.Create(orgCtx, o); err != nil {
			t.Fatalf("org-wide order %s: %v", o.Title, err)
		}
	}
	if _, err := repo.AddItem(orgCtx, &order.Item{OrganizationID: f.OrgA, OrderID: foreign.ID, Description: "switch", Quantity: 1}); err != nil {
		t.Fatalf("org-wide order item: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, order.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 orders: %+v err=%v, want only the own one", list, err)
	}
	if items, listErr := repo.ListItems(ctx, f.OrgA, foreign.ID); listErr != nil || len(items) != 0 {
		t.Fatalf("client-1 items of client-2 order: %+v err=%v, want none", items, listErr)
	}
	if _, err = repo.AddItem(ctx, &order.Item{OrganizationID: f.OrgA, OrderID: foreign.ID, Description: "intruder", Quantity: 1}); err == nil {
		t.Fatal("client-1 principal added an item to a client-2 order")
	}
	if _, err = repo.AddItem(ctx, &order.Item{OrganizationID: f.OrgA, OrderID: own.ID, Description: "foreign asset", Quantity: 1, AssetID: foreignAsset}); err == nil {
		t.Fatal("client-1 principal ordered a client-2 asset")
	}
	if _, err = repo.AddItem(ctx, &order.Item{OrganizationID: f.OrgA, OrderID: own.ID, Description: "patch cable", Quantity: 2}); err != nil {
		t.Fatalf("client-1 item on own order: %v", err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM internal_order_item WHERE order_id = $1`, foreign.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 order items: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, order.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
