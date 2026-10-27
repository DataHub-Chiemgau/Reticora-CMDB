package order_test

import (
	"context"
	"errors"
	"strings"
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

// TestOrderForeignClientUpdateAndApprove covers the writes on an existing
// order (TEN-06, WP-537): a principal restricted to client 1 can neither
// update a client-2 order nor submit, approve or reject it (not found), and
// the client-2 row stays unchanged; its own order it updates and approves.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrderForeignClientUpdateAndApprove(t *testing.T) {
	f := scopetest.Seed(t, "31")
	bg := context.Background()
	approver := f.AppUser(t, f.OrgA, "approver")
	repo := order.NewPGRepository(f.App)
	own := &order.Order{OrganizationID: f.OrgA, ClientID: f.Client1, Title: "order-c1", Status: "submitted", Supplier: "acme"}
	foreign := &order.Order{OrganizationID: f.OrgA, ClientID: f.Client2, Title: "order-c2", Status: "submitted", Supplier: "acme"}
	for _, o := range []*order.Order{own, foreign} {
		if err := repo.Create(f.OrgCtx(f.OrgA), o); err != nil {
			t.Fatalf("org-wide order %s: %v", o.Title, err)
		}
	}
	type row struct{ client, title, status, supplier, approvedBy string }
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), title, status, COALESCE(supplier, ''), COALESCE(approved_by::text, '') FROM internal_order WHERE id = $1`, id).
			Scan(&r.client, &r.title, &r.status, &r.supplier, &r.approvedBy); err != nil {
			t.Fatalf("stored order %s: %v", id, err)
		}
		return r
	}
	want := row{client: f.Client2, title: "order-c2", status: "submitted", supplier: "acme"}
	title, supplier, client1 := "hijacked", "evil", f.Client1
	req := order.UpdateOrderRequest{Title: &title, Supplier: &supplier}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, req); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("client-1 update of a client-2 order: err=%v, want not found", err)
	}
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, order.UpdateOrderRequest{ClientID: &client1}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("client-1 takeover of a client-2 order: err=%v, want not found", err)
	}
	for _, status := range []string{"submitted", "approved", "rejected"} {
		if _, err := repo.SetStatus(ctx, f.OrgA, foreign.ID, status, approver); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("client-1 transition of a client-2 order to %s: err=%v, want not found", status, err)
		}
	}
	if got := stored(foreign.ID); got != want {
		t.Errorf("client-2 order after foreign writes: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates and approves its own
	// order.
	if o, err := repo.Update(ctx, f.OrgA, own.ID, req); err != nil || o.Title != title {
		t.Fatalf("client-1 update of own order: %+v err=%v", o, err)
	}
	if o, err := repo.SetStatus(ctx, f.OrgA, own.ID, "approved", approver); err != nil || o.Status != "approved" {
		t.Fatalf("client-1 approval of own order: %+v err=%v", o, err)
	}
	if got := stored(own.ID); got != (row{client: f.Client1, title: title, status: "approved", supplier: supplier, approvedBy: approver}) {
		t.Errorf("own order after update and approval: %+v", got)
	}
}
