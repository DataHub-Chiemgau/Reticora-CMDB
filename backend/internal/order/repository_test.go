package order

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestOrderLifecycleAndItems(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	o := &Order{OrganizationID: "org-1", Title: "SFP restock", RequestedBy: "u1"}
	if err := repo.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	if o.OrderNumber == "" || o.Status != StatusDraft {
		t.Fatalf("expected auto order number and draft, got %+v", o)
	}
	if _, err := repo.AddItem(ctx, &Item{OrganizationID: "org-1", OrderID: o.ID, Description: "SFP+", Quantity: 10, UnitPrice: 12.5}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, "org-1", o.ID)
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got.Items))
	}
	sub, err := repo.SetStatus(ctx, "org-1", o.ID, StatusSubmitted, "u1")
	if err != nil || sub.Status != StatusSubmitted {
		t.Fatalf("submit: %+v err=%v", sub, err)
	}
	// Four-eyes rule (STK-05, WP-286): the requester cannot approve.
	if _, err = repo.SetStatus(ctx, "org-1", o.ID, StatusApproved, "u1"); !errors.Is(err, ErrFourEyes) {
		t.Fatalf("approval by the requester: err=%v, want ErrFourEyes", err)
	}
	if cur, _ := repo.GetByID(ctx, "org-1", o.ID); cur.Status != StatusSubmitted || cur.ApprovedBy != "" {
		t.Fatalf("refused approval changed the order: %+v", cur)
	}
	app, err := repo.SetStatus(ctx, "org-1", o.ID, StatusApproved, "u2")
	if err != nil || app.Status != StatusApproved || app.ApprovedBy != "u2" {
		t.Fatalf("approve: %+v err=%v", app, err)
	}
	// Outside draft, the order and its positions are fixed.
	title := "changed"
	if _, err = repo.Update(ctx, "org-1", o.ID, UpdateOrderRequest{Title: &title}); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("update outside draft: err=%v, want ErrNotDraft", err)
	}
	if _, err = repo.AddItem(ctx, &Item{OrganizationID: "org-1", OrderID: o.ID, Description: "late", Quantity: 1}); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("item outside draft: err=%v, want ErrNotDraft", err)
	}
	if err = repo.Delete(ctx, "org-1", o.ID); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("delete outside draft: err=%v, want ErrNotDraft", err)
	}
	// tenant isolation
	if _, err = repo.GetByID(ctx, "org-2", o.ID); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("cross-tenant read: err=%v, want not found", err)
	}
	list, total, _ := repo.List(ctx, "org-1", FilterParams{Status: StatusApproved}, api.PaginationParams{Limit: 10})
	if total != 1 || list[0].ID != o.ID {
		t.Fatalf("expected approved order listed, got %d", total)
	}
}

// TestFourEyesWithoutRequester: an order without requested_by cannot be
// approved, whoever approves it.
func TestFourEyesWithoutRequester(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	o := &Order{OrganizationID: "org-1", Title: "anonymous", Status: StatusSubmitted}
	if err := repo.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetStatus(ctx, "org-1", o.ID, StatusApproved, "u2"); !errors.Is(err, ErrFourEyes) {
		t.Fatalf("approval without requester: err=%v, want ErrFourEyes", err)
	}
	if o2, err := repo.SetStatus(ctx, "org-1", o.ID, StatusRejected, "u2"); err != nil || o2.ApprovedBy != "u2" {
		t.Fatalf("reject: %+v err=%v", o2, err)
	}
}

// TestStateMachine covers the transition table of STK-05 (WP-286): every
// pair of states, with the receipt transitions only for the goods receipt.
func TestStateMachine(t *testing.T) {
	all := []string{StatusDraft, StatusSubmitted, StatusApproved, StatusRejected, StatusOrdered, StatusPartiallyReceived,
		StatusReceived, StatusClosed, StatusCancelled}
	allowed := map[[2]string]bool{
		{StatusDraft, StatusSubmitted}: true, {StatusSubmitted, StatusApproved}: true, {StatusSubmitted, StatusRejected}: true,
		{StatusApproved, StatusOrdered}: true, {StatusReceived, StatusClosed}: true,
		{StatusDraft, StatusCancelled}: true, {StatusSubmitted, StatusCancelled}: true, {StatusApproved, StatusCancelled}: true,
		{StatusOrdered, StatusCancelled}: true, {StatusPartiallyReceived, StatusCancelled}: true,
	}
	receipt := map[[2]string]bool{
		{StatusOrdered, StatusPartiallyReceived}: true, {StatusOrdered, StatusReceived}: true,
		{StatusPartiallyReceived, StatusReceived}: true,
	}
	for _, from := range all {
		for _, to := range all {
			if got := CanTransition(from, to); got != allowed[[2]string{from, to}] {
				t.Errorf("CanTransition(%s, %s) = %v", from, to, got)
			}
			if got := CanReceive(from, to); got != receipt[[2]string{from, to}] {
				t.Errorf("CanReceive(%s, %s) = %v", from, to, got)
			}
		}
	}
	// A refused transition leaves the order unchanged.
	repo := NewMemoryRepository()
	o := &Order{OrganizationID: "org-1", Title: "t", RequestedBy: "u1"}
	if err := repo.Create(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetStatus(context.Background(), "org-1", o.ID, StatusOrdered, "u1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("draft → ordered: err=%v, want ErrInvalidTransition", err)
	}
	if cur, _ := repo.GetByID(context.Background(), "org-1", o.ID); cur.Status != StatusDraft {
		t.Fatalf("refused transition changed the status to %s", cur.Status)
	}
}

// TestItemReferencesConsumableOrAsset: a position with both references is
// a 422 on asset_id.
func TestItemReferencesConsumableOrAsset(t *testing.T) {
	repo := NewMemoryRepository()
	o := &Order{OrganizationID: "org-1", Title: "t", RequestedBy: "u1"}
	if err := repo.Create(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	_, err := repo.AddItem(context.Background(), &Item{OrganizationID: "org-1", OrderID: o.ID, Description: "x", Quantity: 1,
		ConsumableID: "c1", AssetID: "a1"})
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "asset_id" || fe.Status != 422 {
		t.Fatalf("both references: err=%v, want 422 on asset_id", err)
	}
}
