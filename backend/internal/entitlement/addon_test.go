package entitlement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestAddonsNeedTheirOwnEntitlement covers WP-075 (CH14, ENT-06, API-06,
// IGA-01..04, AI-02): no plan includes IGA or AI; an enterprise organization
// without the add-on gets 403 on the IGA, SCIM and AI paths; its own
// entitlement row unlocks exactly that add-on.
func TestAddonsNeedTheirOwnEntitlement(t *testing.T) {
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		for _, addon := range Addons {
			if slices.Contains(PlanFeatures(plan), addon) {
				t.Errorf("plan %s includes the add-on %s", plan, addon)
			}
		}
	}
	if FeatureAI != "ai" || FeatureIGA != "iga" {
		t.Errorf("add-on keys %q, %q; want ai and iga (ENT-06)", FeatureAI, FeatureIGA)
	}

	svc := testService(t, PlanEnterprise)
	handler := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	status := func(path string) int {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, tenantCtx(httptest.NewRequest(http.MethodGet, path, nil)))
		return w.Code
	}
	paths := map[string]string{"/api/v1/iga/connectors": FeatureIGA, "/scim/v2/Users": FeatureIGA, "/api/v1/ai/chat": FeatureAI}
	for path := range paths {
		if got := status(path); got != http.StatusForbidden {
			t.Errorf("enterprise without add-on: %s = %d, want 403", path, got)
		}
	}
	if got := status("/api/v1/compliance/reports"); got != http.StatusNoContent {
		t.Errorf("enterprise module: %d, want 204", got)
	}

	if _, err := svc.Grant(context.Background(), Entitlement{OrganizationID: "org-1", FeatureKey: FeatureAI,
		Plan: PlanEnterprise, Enabled: true, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	for path, feature := range paths {
		want := http.StatusForbidden
		if feature == FeatureAI {
			want = http.StatusNoContent
		}
		if got := status(path); got != want {
			t.Errorf("with the AI add-on: %s = %d, want %d", path, got, want)
		}
	}
}
