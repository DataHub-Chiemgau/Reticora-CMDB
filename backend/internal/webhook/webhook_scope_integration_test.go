package webhook_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestWebhookRepositoryScope runs the subscription repository with the
// principal's tenant scope (TEN-06, WP-016). A subscription carries the
// scope snapshot of its creator (E-98, WP-601): a client-1 principal of
// organization A stores subscriptions of client 1 only and lists, reads and
// deletes those, not the subscriptions of client 2, the org-wide one or the
// one without snapshot; an org-wide principal manages all of them,
// subscriptions of organization B stay invisible, and a lookup without
// scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestWebhookRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "29")
	repo := webhook.NewPGRepository(f.App)
	snapshotOf := func(ctx context.Context) *webhook.Scope {
		scope, _ := database.TenantScopeFromContext(ctx)
		return webhook.ScopeOf(&scope)
	}
	create := func(ctx context.Context, org, name string, snapshot *webhook.Scope) *webhook.Subscription {
		t.Helper()
		sub := &webhook.Subscription{OrganizationID: org, Name: name, URL: "https://" + name + ".example.invalid/hook", Secret: "s",
			Events: []string{"ci.created"}, IsActive: true, Scope: snapshot}
		if err := repo.Create(ctx, sub); err != nil {
			t.Fatalf("create subscription %s: %v", name, err)
		}
		return sub
	}
	orgA, orgB := f.OrgCtx(f.OrgA), f.OrgCtx(f.OrgB)
	client1, client2 := f.ClientCtx(f.Client1), f.ClientCtx(f.Client2)
	subB := create(orgB, f.OrgB, "b", snapshotOf(orgB))
	sub1 := create(client1, f.OrgA, "a-client1", snapshotOf(client1))
	sub2 := create(client2, f.OrgA, "a-client2", snapshotOf(client2))
	subOrg := create(orgA, f.OrgA, "a-orgwide", snapshotOf(orgA))
	legacy := create(orgA, f.OrgA, "a-legacy", nil)

	ids := func(subs []webhook.Subscription) []string {
		var out []string
		for _, s := range subs {
			out = append(out, s.ID)
		}
		slices.Sort(out)
		return out
	}
	sorted := func(v ...string) []string { slices.Sort(v); return v }
	for _, c := range []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"client 1", client1, []string{sub1.ID}},
		{"client 2", client2, []string{sub2.ID}},
		{"org-wide", orgA, sorted(sub1.ID, sub2.ID, subOrg.ID, legacy.ID)},
	} {
		subs, total, err := repo.List(c.ctx, f.OrgA, api.PaginationParams{Limit: 100})
		if err != nil || !slices.Equal(ids(subs), c.want) || total != len(c.want) {
			t.Errorf("subscriptions of organization A as %s: %v (total %d) err=%v, want %v", c.name, ids(subs), total, err, c.want)
		}
		matching, err := repo.ListByEvent(c.ctx, f.OrgA, "ci.created")
		if err != nil || !slices.Equal(ids(matching), c.want) {
			t.Errorf("subscriptions for ci.created as %s: %v err=%v, want %v", c.name, ids(matching), err, c.want)
		}
	}
	got, err := repo.GetByID(client1, f.OrgA, sub1.ID)
	if err != nil || got.Scope == nil || !slices.Equal(got.Scope.Clients, []string{f.Client1}) || got.Scope.Sites != nil || got.Scope.Teams != nil {
		t.Fatalf("own subscription as client 1: %+v err=%v, want the client-1 snapshot", got, err)
	}
	if got, err = repo.GetByID(orgA, f.OrgA, legacy.ID); err != nil || got.Scope != nil {
		t.Errorf("subscription without snapshot as org-wide principal: %+v err=%v", got, err)
	}
	for _, id := range []string{sub2.ID, subOrg.ID, legacy.ID} {
		if _, err = repo.GetByID(client1, f.OrgA, id); err == nil {
			t.Errorf("client 1 reads subscription %s outside its scope", id)
		}
		if err = repo.Delete(client1, f.OrgA, id); err == nil {
			t.Errorf("client 1 deletes subscription %s outside its scope", id)
		}
		if _, err = repo.GetByID(orgA, f.OrgA, id); err != nil {
			t.Errorf("subscription %s after the refused delete: %v", id, err)
		}
	}
	// A client-1 principal cannot store a subscription outside its scope.
	for name, snapshot := range map[string]*webhook.Scope{
		"without snapshot": nil, "of client 2": snapshotOf(client2), "org-wide": snapshotOf(orgA),
	} {
		sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "refused", URL: "https://refused.example.invalid/hook", Secret: "s",
			Events: []string{"ci.created"}, IsActive: true, Scope: snapshot}
		if err = repo.Create(client1, sub); err == nil {
			t.Errorf("client 1 stores a subscription %s", name)
		}
	}
	if err = repo.Delete(client1, f.OrgA, sub1.ID); err != nil {
		t.Errorf("client 1 deletes its own subscription: %v", err)
	}
	if err = repo.Delete(client1, f.OrgB, subB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B subscription: got %v, want ErrTenantMismatch", err)
	}
	if _, err = repo.ListByEvent(context.Background(), f.OrgA, "ci.created"); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("lookup without scope: got %v, want ErrNoTenantScope", err)
	}
}
