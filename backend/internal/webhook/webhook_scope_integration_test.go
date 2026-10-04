package webhook_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestWebhookRepositoryScope runs the subscription repository with the
// principal's tenant scope (TEN-06, WP-016). Subscriptions belong to the
// organization: a client-scoped principal of organization A manages A's
// subscriptions only, subscriptions of organization B stay invisible, and a
// lookup without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestWebhookRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "29")
	repo := webhook.NewPGRepository(f.App)
	subB := &webhook.Subscription{OrganizationID: f.OrgB, Name: "b", URL: "https://b.example.invalid/hook", Secret: "s", Events: []string{"ci.created"}, IsActive: true}
	if err := repo.Create(f.OrgCtx(f.OrgB), subB); err != nil {
		t.Fatalf("create organization B subscription: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	subA := &webhook.Subscription{OrganizationID: f.OrgA, Name: "a", URL: "https://a.example.invalid/hook", Secret: "s", Events: []string{"ci.created"}, IsActive: true}
	if err := repo.Create(ctx, subA); err != nil {
		t.Fatalf("create organization A subscription: %v", err)
	}
	subs, _, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 100})
	if err != nil || len(subs) != 1 || subs[0].ID != subA.ID {
		t.Fatalf("organization A subscriptions: %+v err=%v, want only the A subscription", subs, err)
	}
	matching, err := repo.ListByEvent(ctx, f.OrgA, "ci.created")
	if err != nil || len(matching) != 1 || matching[0].ID != subA.ID {
		t.Fatalf("organization A subscriptions for ci.created: %+v err=%v", matching, err)
	}
	if err = repo.Delete(ctx, f.OrgB, subB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B subscription: got %v, want ErrTenantMismatch", err)
	}
	if _, err = repo.ListByEvent(context.Background(), f.OrgA, "ci.created"); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("lookup without scope: got %v, want ErrNoTenantScope", err)
	}
}
