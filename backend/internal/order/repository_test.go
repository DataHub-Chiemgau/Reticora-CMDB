package order

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestOrderLifecycleAndItems(t *testing.T) {
	repo := NewMemoryRepository()
	o := &Order{OrganizationID: "org-1", Title: "SFP restock"}
	if err := repo.Create(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if o.OrderNumber == "" || o.Status != "draft" {
		t.Fatalf("expected auto order number and draft, got %+v", o)
	}
	if _, err := repo.AddItem(context.Background(), &Item{OrganizationID: "org-1", OrderID: o.ID, Description: "SFP+", Quantity: 10, UnitPrice: 12.5}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(context.Background(), "org-1", o.ID)
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got.Items))
	}
	sub, err := repo.SetStatus(context.Background(), "org-1", o.ID, "submitted", "u1")
	if err != nil || sub.Status != "submitted" {
		t.Fatalf("submit: %+v err=%v", sub, err)
	}
	app, err := repo.SetStatus(context.Background(), "org-1", o.ID, "approved", "u2")
	if err != nil || app.Status != "approved" || app.ApprovedBy != "u2" {
		t.Fatalf("approve: %+v err=%v", app, err)
	}
	// tenant isolation
	if _, err := repo.GetByID(context.Background(), "org-2", o.ID); err == nil {
		t.Fatal("cross-tenant read must fail")
	}
	list, total, _ := repo.List(context.Background(), "org-1", FilterParams{Status: "approved"}, api.PaginationParams{Limit: 10})
	if total != 1 || list[0].ID != o.ID {
		t.Fatalf("expected approved order listed, got %d", total)
	}
}
