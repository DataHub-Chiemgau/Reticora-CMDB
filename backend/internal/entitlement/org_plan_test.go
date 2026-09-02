package entitlement

import (
	"context"
	"testing"
)

// TestOrganizationPlanResolution verifies the base plan comes from the
// organization record, falling back to DefaultPlan when unset, and that one
// tenant's plan never affects another (audit finding H5).
func TestOrganizationPlanResolution(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SetOrganizationPlan("org-pro", PlanPro)
	repo.SetOrganizationPlan("org-essential", PlanEssential)

	svc := NewService(repo, Options{DefaultPlan: PlanEssential, Enforce: true})
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-pro", FeatureWebhooks) {
		t.Fatal("pro org should have webhooks")
	}
	if svc.IsEnabled(ctx, "org-essential", FeatureWebhooks) {
		t.Fatal("essential org must not have webhooks")
	}
	// Org without a plan column value falls back to the server default.
	if svc.IsEnabled(ctx, "org-unset", FeatureWebhooks) {
		t.Fatal("default-plan org must not have webhooks")
	}
	if !svc.IsEnabled(ctx, "org-unset", FeatureCMDB) {
		t.Fatal("default-plan org should have cmdb")
	}
}

func TestExplicitEntitlementOverridesBasePlan(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SetOrganizationPlan("org", PlanEssential)
	if _, err := repo.Upsert(context.Background(), Entitlement{
		OrganizationID: "org", FeatureKey: FeatureWebhooks, Plan: PlanEssential, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(repo, Options{DefaultPlan: PlanEssential, Enforce: true})
	if !svc.IsEnabled(context.Background(), "org", FeatureWebhooks) {
		t.Fatal("explicit entitlement row should override the base plan")
	}
}

func TestPlanChangeInvalidatesAfterUpsert(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, Options{DefaultPlan: PlanEssential, Enforce: true})
	ctx := context.Background()

	if svc.IsEnabled(ctx, "org", FeatureWebhooks) {
		t.Fatal("webhooks must start disabled")
	}
	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org", FeatureKey: FeatureWebhooks, Plan: PlanPro, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.IsEnabled(ctx, "org", FeatureWebhooks) {
		t.Fatal("Grant must invalidate the cache immediately")
	}
}
