package entitlement

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testService(t *testing.T, plan Plan) *Service {
	t.Helper()
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})
	if _, err := svc.Provision(context.Background(), "org-1", plan); err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestProvisionedPlanGrantsOnlyPlanFeatures covers WP-072 (ENT-05): an
// organization is entitled exactly to its rows; an organization without rows
// has only cmdb_core (no default-plan fallback); provisioning runs once.
func TestProvisionedPlanGrantsOnlyPlanFeatures(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-1", FeatureDiscovery) {
		t.Error("expected discovery to be part of the essential plan")
	}
	if svc.IsEnabled(ctx, "org-1", FeatureTicketing) {
		t.Error("expected ticketing to be excluded from the essential plan")
	}
	if svc.IsEnabled(ctx, "org-2", FeatureDiscovery) || !svc.IsEnabled(ctx, "org-2", FeatureCMDBCore) {
		t.Error("organization without rows: want only cmdb_core")
	}
	if list, _ := svc.List(ctx, "org-2"); len(list) != 1 || list[0].FeatureKey != FeatureCMDBCore || !list[0].Enabled {
		t.Errorf("entitlements of an organization without rows: %+v", list)
	}
	if done, err := svc.Provision(ctx, "org-1", PlanEnterprise); done || err != nil {
		t.Errorf("second provisioning: %v, %v; want no change", done, err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureTicketing) {
		t.Error("second provisioning changed the plan")
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

// TestDisabledAndExpiredEntitlementsDeny covers WP-072 (CH21): a disabled
// row denies; after valid_until only discovery stops, other features stay.
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
		ValidUntil:     &expired,
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.IsEnabled(ctx, "org-1", FeatureIGA) {
		t.Error("expiry stopped a feature other than discovery (CH21)")
	}

	if _, err := svc.Grant(ctx, Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDiscovery, Plan: PlanEnterprise,
		Enabled: true, ValidUntil: &expired}); err != nil {
		t.Fatal(err)
	}
	if _, status, err := svc.Status(ctx, "org-1", FeatureDiscovery); status != StatusExpired || err != nil {
		t.Errorf("expired discovery: %s, %v; want expired", status, err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureDiscovery) {
		t.Error("expired discovery still enabled")
	}
}

func TestAllowCreateEnforcesLimit(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	limit := int64(2)
	if _, err := svc.Grant(ctx, Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureCMDBCore,
		Plan:           PlanEssential,
		Enabled:        true,
		Limits:         map[string]int64{LimitMaxCIs: limit},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.AllowCreate(ctx, "org-1", LimitMaxCIs, 1); err != nil {
		t.Fatalf("expected creation below the limit to be allowed: %v", err)
	}

	err := svc.AllowCreate(ctx, "org-1", LimitMaxCIs, 2)
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
	svc := NewService(NewMemoryRepository(), Options{Enforce: false})
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-1", FeatureIGA) {
		t.Error("expected all features to be available when enforcement is off")
	}
	if err := svc.AllowCreate(ctx, "org-1", LimitMaxCIs, 1_000_000); err != nil {
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
		// WP-072 (ENT-05): ingest alias, exports and topology.
		"/api/v1/ingest/bulk":               FeatureDiscovery,
		"/api/v1/discovery/ingest":          FeatureDiscovery,
		"/api/v1/export/cis":                FeatureExportCSV,
		"/api/v1/export/jobs/j1":            FeatureExportCSV,
		"/api/v1/topology/cis/c1/neighbors": FeatureTopology,
		"/api/v1/cis/c1/dependencies":       FeatureTopology,
		"/api/v1/cis/c1/blast-radius":       FeatureTopology,
	}
	for path, want := range cases {
		got, gated := RequiredFeature(path)
		if !gated || got != want {
			t.Errorf("RequiredFeature(%q) = (%q, %v), want (%q, true)", path, got, gated, want)
		}
	}

	for _, path := range []string{"/api/v1/cis", "/api/v1/cis/c1", "/api/v1/auth/me", "/api/v1/entitlements", "/healthz",
		"/api/v1/collectors/enroll", "/api/v1/users/u1/data-export"} {
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

// TestPhase1FeaturesAndQuotas covers WP-071 (ENT-01, ENT-02): every plan
// includes the eight phase-1 features; stored limits gate the quotas; cmdb_core cannot be disabled or
// limited in time and stays active; sources are restricted to ENT-01.
func TestPhase1FeaturesAndQuotas(t *testing.T) {
	ctx := context.Background()
	want := []string{"cmdb_core", "discovery", "topology", "rack_view", "export_csv", "webhooks", "api_access", "notifications_email"}
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		svc := testService(t, plan)
		for _, f := range want {
			if !svc.IsEnabled(ctx, "org-1", f) {
				t.Errorf("plan %s lacks the phase-1 feature %s", plan, f)
			}
		}
	}
	if len(Quotas) != 4 || Quotas[0] != "max_cis" || Quotas[1] != "max_collectors" || Quotas[2] != "max_users" || Quotas[3] != "max_api_keys" {
		t.Errorf("quotas %v", Quotas)
	}

	// The proposed quotas of ENT-06 (V) have no gate effect (E-34, WP-075):
	// without a stored limit a quota is unlimited.
	svc := testService(t, PlanStandard)
	for _, quota := range Quotas {
		if got, err := svc.Quota(ctx, "org-1", quota); err != nil || got != nil {
			t.Errorf("default quota %s = %v, %v; want unlimited", quota, deref(got), err)
		}
	}
	if err := svc.AllowCreate(ctx, "org-1", LimitMaxCollectors, 1000); err != nil {
		t.Errorf("collector without a stored limit refused: %v", err)
	}
	if _, err := svc.Grant(ctx, Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDiscovery, Plan: PlanStandard, Enabled: true,
		Limits: map[string]int64{LimitMaxCollectors: 8}, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Quota(ctx, "org-1", LimitMaxCollectors); got == nil || *got != 8 {
		t.Errorf("stored quota %v, want 8", deref(got))
	}
	if err := svc.AllowCreate(ctx, "org-1", "max_unknown", 0); err == nil {
		t.Error("unknown quota accepted")
	}

	expired := time.Now().Add(-time.Hour)
	for name, ent := range map[string]Entitlement{
		"disabled core": {OrganizationID: "org-1", FeatureKey: FeatureCMDBCore, Enabled: false},
		"expiring core": {OrganizationID: "org-1", FeatureKey: FeatureCMDBCore, Enabled: true, ValidUntil: &expired},
		"bad source":    {OrganizationID: "org-1", FeatureKey: FeatureWebhooks, Enabled: true, Source: "gift"},
		"negative":      {OrganizationID: "org-1", FeatureKey: FeatureWebhooks, Enabled: true, Limits: map[string]int64{"limit": -1}},
	} {
		var validation *ValidationError
		if _, err := svc.Grant(ctx, ent); !errors.As(err, &validation) {
			t.Errorf("%s: %v, want a validation error", name, err)
		}
	}
	if !svc.IsEnabled(ctx, "org-1", FeatureCMDBCore) {
		t.Error("cmdb_core not active")
	}
}

func deref(v *int64) any {
	if v == nil {
		return "unlimited"
	}
	return *v
}
