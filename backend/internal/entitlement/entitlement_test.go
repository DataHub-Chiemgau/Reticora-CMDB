package entitlement

import (
	"context"
	"testing"
	"time"
)

func testService(t *testing.T, plan Plan) *Service {
	t.Helper()
	return NewService(NewMemoryRepository(), Options{DefaultPlan: plan, Enforce: true})
}

func TestDefaultPlanGrantsOnlyPlanFeatures(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-1", FeatureDiscovery) {
		t.Error("expected discovery to be part of the essential plan")
	}
	if svc.IsEnabled(ctx, "org-1", FeatureTicketing) {
		t.Error("expected ticketing to be excluded from the essential plan")
	}
}

func TestGrantEnablesFeatureForSingleTenant(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureTicketing,
		Plan:           PlanStandard,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	if !svc.IsEnabled(ctx, "org-1", FeatureTicketing) {
		t.Error("expected ticketing to be enabled for org-1")
	}
	if svc.IsEnabled(ctx, "org-2", FeatureTicketing) {
		t.Error("expected ticketing to stay disabled for org-2")
	}
}

func TestDisabledAndExpiredEntitlementsDeny(t *testing.T) {
	svc := testService(t, PlanEnterprise)
	ctx := context.Background()

	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureIGA,
		Plan:           PlanEnterprise,
		Enabled:        false,
	}); err != nil {
		t.Fatal(err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureIGA) {
		t.Error("expected explicitly disabled entitlement to deny access")
	}

	expired := time.Now().UTC().Add(-time.Hour)
	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureIGA,
		Plan:           PlanEnterprise,
		Enabled:        true,
		ExpiresAt:      &expired,
	}); err != nil {
		t.Fatal(err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureIGA) {
		t.Error("expected expired entitlement to deny access")
	}
}

func TestAllowCreateEnforcesLimit(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	limit := int64(2)
	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureCMDB,
		Plan:           PlanEssential,
		Enabled:        true,
		Limit:          &limit,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.AllowCreate(ctx, "org-1", FeatureCMDB, 1); err != nil {
		t.Fatalf("expected creation below the limit to be allowed: %v", err)
	}

	err := svc.AllowCreate(ctx, "org-1", FeatureCMDB, 2)
	if err == nil {
		t.Fatal("expected the limit to be enforced")
	}
	var limitErr *LimitExceededError
	if !asLimitError(err, &limitErr) {
		t.Fatalf("expected LimitExceededError, got %T", err)
	}
	if limitErr.Limit != 2 || limitErr.Current != 2 {
		t.Fatalf("unexpected limit error: %+v", limitErr)
	}
}

func TestEnforcementDisabledAllowsEverything(t *testing.T) {
	svc := NewService(NewMemoryRepository(), Options{DefaultPlan: PlanEssential, Enforce: false})
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-1", FeatureIGA) {
		t.Error("expected all features to be available when enforcement is off")
	}
	if err := svc.AllowCreate(ctx, "org-1", FeatureCMDB, 1_000_000); err != nil {
		t.Errorf("expected limits to be ignored when enforcement is off: %v", err)
	}
}

func TestRequiredFeatureMapping(t *testing.T) {
	cases := map[string]string{
		"/api/v1/tickets":            FeatureTicketing,
		"/api/v1/tickets/abc":        FeatureTicketing,
		"/api/v1/discovery/jobs":     FeatureDiscovery,
		"/api/v1/webhooks":           FeatureWebhooks,
		"/api/v1/monitoring/metrics": FeatureMonitoring,
	}
	for path, want := range cases {
		got, gated := RequiredFeature(path)
		if !gated || got != want {
			t.Errorf("RequiredFeature(%q) = (%q, %v), want (%q, true)", path, got, gated, want)
		}
	}

	for _, path := range []string{"/api/v1/cis", "/api/v1/auth/me", "/api/v1/entitlements", "/healthz"} {
		if _, gated := RequiredFeature(path); gated {
			t.Errorf("expected %q to stay ungated", path)
		}
	}
}

func asLimitError(err error, target **LimitExceededError) bool {
	limitErr, ok := err.(*LimitExceededError)
	if ok {
		*target = limitErr
	}
	return ok
}
