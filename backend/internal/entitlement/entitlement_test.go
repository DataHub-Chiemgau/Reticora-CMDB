package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
// has only cmdb_core (no default-plan fallback); provisioning again adds
// only missing plan features (WP-470) and changes no existing row.
func TestProvisionedPlanGrantsOnlyPlanFeatures(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	if !svc.IsEnabled(ctx, "org-1", FeatureDiscovery) {
		t.Error("expected discovery to be part of the essential plan")
	}
	if svc.IsEnabled(ctx, "org-1", FeatureTicketingStandard) {
		t.Error("expected ticketing_standard to be excluded from the essential plan")
	}
	if svc.IsEnabled(ctx, "org-2", FeatureDiscovery) || !svc.IsEnabled(ctx, "org-2", FeatureCMDBCore) {
		t.Error("organization without rows: want only cmdb_core")
	}
	if list, _ := svc.List(ctx, "org-2"); len(list) != 1 || list[0].FeatureKey != FeatureCMDBCore || !list[0].Enabled {
		t.Errorf("entitlements of an organization without rows: %+v", list)
	}
	if added, err := svc.Provision(ctx, "org-1", PlanEssential); len(added) != 0 || err != nil {
		t.Errorf("second provisioning: %v, %v; want no change", added, err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureTicketingStandard) {
		t.Error("second provisioning changed the plan")
	}
	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureExportCSV, Plan: PlanEssential}); err != nil {
		t.Fatal(err)
	}
	added, err := svc.Provision(ctx, "org-1", PlanStandard)
	if err != nil || !slices.Equal(added, []string{FeatureTicketingStandard, FeatureSignature, FeatureStock,
		FeatureStocktakeStandard, FeatureBarcode, FeatureGPSStandard, FeatureWorkplace, FeatureExportDATEV,
		FeatureMonitoring, FeatureIPAM}) {
		t.Errorf("provisioning a larger plan: %v, %v; want only the missing features", added, err)
	}
	if svc.IsEnabled(ctx, "org-1", FeatureExportCSV) {
		t.Error("provisioning overwrote the disabled export_csv row")
	}
}

func TestGrantEnablesFeatureForSingleTenant(t *testing.T) {
	svc := testService(t, PlanEssential)
	ctx := context.Background()

	if _, err := svc.Grant(ctx, &Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureTicketingStandard,
		Plan:           PlanStandard,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	if !svc.IsEnabled(ctx, "org-1", FeatureTicketingStandard) {
		t.Error("expected ticketing_standard to be enabled for org-1")
	}
	if svc.IsEnabled(ctx, "org-2", FeatureTicketingStandard) {
		t.Error("expected ticketing_standard to stay disabled for org-2")
	}
}

// TestDisabledAndExpiredEntitlementsDeny covers WP-072 (CH21): a disabled
// row denies; after valid_until only discovery stops, other features stay.
func TestDisabledAndExpiredEntitlementsDeny(t *testing.T) {
	svc := testService(t, PlanEnterprise)
	ctx := context.Background()

	if _, err := svc.Grant(ctx, &Entitlement{
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
	if _, err := svc.Grant(ctx, &Entitlement{
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

	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDiscovery, Plan: PlanEnterprise,
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
	if _, err := svc.Grant(ctx, &Entitlement{
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

// TestLimitExceededDetailFollowsCallerScope covers F-414 (ENT-03): the
// refusal names count and limit only to a principal with org-wide client
// and site scope; a client- or site-scoped principal and a context without
// a user (invitation, first login, enrollment, ingest) get the text without
// numbers. The fields keep both for the callers in code.
func TestLimitExceededDetailFollowsCallerScope(t *testing.T) {
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})
	if _, err := svc.Grant(context.Background(), &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureCMDBCore,
		Plan: PlanEssential, Enabled: true, Limits: map[string]int64{LimitMaxCIs: 7}}); err != nil {
		t.Fatal(err)
	}
	orgWide := database.OrgWideScope("org-1", "user-1")
	clientScoped, siteScoped := orgWide, orgWide
	clientScoped.Clients = database.ScopeIDs("c0dea1a0-0000-4000-8000-0000000000c1")
	siteScoped.Sites = database.ScopeIDs("c0dea1a0-0000-4000-8000-0000000000a1")
	system := database.OrgWideScope("org-1", "")
	const hidden = `entitlement limit for "max_cis" exceeded: the limit of the plan is reached`
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"org-wide principal": {database.ContextWithTenantScope(context.Background(), &orgWide), `entitlement limit for "max_cis" exceeded: 9 of 7 in use`},
		"client-scoped":      {database.ContextWithTenantScope(context.Background(), &clientScoped), hidden},
		"site-scoped":        {database.ContextWithTenantScope(context.Background(), &siteScoped), hidden},
		"system scope":       {database.ContextWithTenantScope(context.Background(), &system), hidden},
		"no scope":           {context.Background(), hidden},
	} {
		err := svc.AllowCreate(tc.ctx, "org-1", LimitMaxCIs, 9)
		var limitErr *LimitExceededError
		if !asLimitError(err, &limitErr) {
			t.Fatalf("%s: %v, want LimitExceededError", name, err)
		}
		if err.Error() != tc.want || limitErr.Limit != 7 || limitErr.Current != 9 {
			t.Errorf("%s: %q (limit %d, current %d), want %q with limit 7 and current 9", name, err.Error(), limitErr.Limit, limitErr.Current, tc.want)
		}
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
		// WP-244 (ENT-06, E-44): the modules of phase 2 use the keys of
		// ENT-06.
		"/api/v1/tickets":                 FeatureTicketingEssential,
		"/api/v1/tickets/abc":             FeatureTicketingEssential,
		"/api/v1/assets":                  FeatureAssetsBasic,
		"/api/v1/assets/a1":               FeatureAssetsBasic,
		"/api/v1/assignments":             FeatureAssetsBasic,
		"/api/v1/disposal-records":        FeatureAssetsBasic,
		"/api/v1/disposal-records/d1":     FeatureAssetsBasic,
		"/api/v1/consumables":             FeatureStock,
		"/api/v1/stocktakes":              FeatureStocktakeStandard,
		"/api/v1/stocktakes/s1/complete":  FeatureStocktakeStandard,
		"/api/v1/orders":                  FeatureInternalOrders,
		"/api/v1/orders/o1":               FeatureInternalOrders,
		"/api/v1/documents":               FeatureDocuments,
		"/api/v1/desks":                   FeatureWorkplace,
		"/api/v1/desk-bookings":           FeatureWorkplace,
		"/api/v1/asset-locations":         FeatureGPSStandard,
		"/api/v1/asset-locations/a1/path": FeatureGPSStandard,
		"/api/v1/discovery/jobs":          FeatureDiscovery,
		"/api/v1/webhooks":                FeatureWebhooks,
		"/api/v1/monitoring/metrics":      FeatureMonitoring,
		// WP-072 (ENT-05): ingest alias, exports and topology.
		"/api/v1/ingest/bulk":               FeatureDiscovery,
		"/api/v1/discovery/ingest":          FeatureDiscovery,
		"/api/v1/export/cis":                FeatureExportCSV,
		"/api/v1/export/jobs/j1":            FeatureExportCSV,
		"/api/v1/topology/cis/c1/neighbors": FeatureTopology,
		"/api/v1/cis/c1/dependencies":       FeatureTopology,
		"/api/v1/cis/c1/blast-radius":       FeatureTopology,
		// WP-168 (API-05): catalog paths are gated like their predecessors.
		"/api/v1/impact/c1":  FeatureTopology,
		"/api/v1/exports/j1": FeatureExportCSV,
		// WP-671 (F-400): the discovery scopes are discovery.
		"/api/v1/discovery-scopes":    FeatureDiscovery,
		"/api/v1/discovery-scopes/s1": FeatureDiscovery,
		// WP-705 (E-44, E-38, E-39): suffix rules go before the prefix of
		// their module, and the prefixes of the phase-2 modules are gated.
		"/api/v1/assets/a1/label.svg":          FeatureBarcode,
		"/api/v1/assets/a1/label":              FeatureBarcode,
		"/api/v1/assets/a1/locations":          FeatureGPSStandard,
		"/api/v1/assets/a1/movements":          FeatureAssetsBasic,
		"/api/v1/tickets/t1/sla":               FeatureTicketingStandard,
		"/api/v1/tickets/t1/comments":          FeatureTicketingEssential,
		"/api/v1/reservations":                 FeatureAssetsBasic,
		"/api/v1/reservations/r1/release":      FeatureAssetsBasic,
		"/api/v1/compositions":                 FeatureAssetsBasic,
		"/api/v1/inventory/items":              FeatureStock,
		"/api/v1/inventory/items/i1/convert":   FeatureStock,
		"/api/v1/inventory/availability":       FeatureStock,
		"/api/v1/slas":                         FeatureTicketingStandard,
		"/api/v1/slas/breaches":                FeatureTicketingStandard,
		"/api/v1/business-calendars":           FeatureTicketingStandard,
		"/api/v1/suppliers":                    FeatureAssetsBasic,
		"/api/v1/contracts/c1":                 FeatureAssetsBasic,
		"/api/v1/labels/batch":                 FeatureBarcode,
		"/api/v1/labels/lookup":                FeatureBarcode,
		"/api/v1/imports":                      FeatureImport,
		"/api/v1/import-profiles/p1":           FeatureImport,
		"/api/v1/stock-movements":              FeatureAssetsBasic,
		"/api/v1/stock-movements/m1/signature": FeatureSignature,
	}
	for path, want := range cases {
		got, gated := RequiredFeature(http.MethodGet, path)
		if !gated || got != want {
			t.Errorf("RequiredFeature(GET, %q) = (%q, %v), want (%q, true)", path, got, gated, want)
		}
	}

	// Method rules: the ledger is read with assets_basic, a booking needs
	// stock; opening a ticket from a review item is ticketing.
	methodCases := []struct{ method, path, want string }{
		{http.MethodGet, "/api/v1/stock-movements", FeatureAssetsBasic},
		{http.MethodPost, "/api/v1/stock-movements", FeatureStock},
		{http.MethodDelete, "/api/v1/stock-movements/m1", FeatureStock},
		{http.MethodPost, "/api/v1/review-items/r1/ticket", FeatureTicketingEssential},
		{http.MethodPost, "/api/v1/discovery/jobs", FeatureDiscovery},
		{http.MethodPut, "/api/v1/tickets/t1/sla", FeatureTicketingStandard},
	}
	for _, c := range methodCases {
		got, gated := RequiredFeature(c.method, c.path)
		if !gated || got != c.want {
			t.Errorf("RequiredFeature(%s, %q) = (%q, %v), want (%q, true)", c.method, c.path, got, gated, c.want)
		}
	}
	// WP-288 (INV-02, E-44): every stocktake path and method is
	// stocktake_standard; the prefix /api/v1/stocktake without a route is
	// no longer gated, the consumables stay stock.
	for _, path := range []string{"/api/v1/stocktakes", "/api/v1/stocktakes/s1", "/api/v1/stocktakes/s1/start",
		"/api/v1/stocktakes/s1/items", "/api/v1/stocktakes/s1/scans", "/api/v1/stocktakes/s1/differences",
		"/api/v1/stocktakes/s1/difference", "/api/v1/stocktakes/s1/complete", "/api/v1/stocktakes/s1/cancel"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			if got, gated := RequiredFeature(method, path); !gated || got != FeatureStocktakeStandard {
				t.Errorf("RequiredFeature(%s, %q) = (%q, %v), want (stocktake_standard, true)", method, path, got, gated)
			}
		}
	}
	for _, path := range []string{"/api/v1/stocktake", "/api/v1/stocktake/scan"} {
		if got, gated := RequiredFeature(http.MethodGet, path); gated {
			t.Errorf("RequiredFeature(GET, %q) = %q, want ungated", path, got)
		}
	}
	if got, _ := RequiredFeature(http.MethodPost, "/api/v1/consumables"); got == FeatureStocktakeStandard {
		t.Error("/api/v1/consumables is stocktake_standard, want stock")
	}

	// A label needs barcode and assets_basic (E-44).
	if got := RequiredFeatures(http.MethodGet, "/api/v1/assets/a1/label.svg"); !slices.Equal(got, []string{FeatureBarcode, FeatureAssetsBasic}) {
		t.Errorf("RequiredFeatures(label.svg) = %v, want [barcode assets_basic]", got)
	}
	// Every rule names at least one known feature key.
	known := FeatureKeys()
	for _, rule := range featureRules {
		if len(rule.features) == 0 {
			t.Errorf("rule %+v has no feature key", rule)
		}
		for _, f := range rule.features {
			if !slices.Contains(known, f) {
				t.Errorf("rule %+v names the unknown feature key %q", rule, f)
			}
		}
	}

	for _, path := range []string{"/api/v1/cis", "/api/v1/cis/c1", "/api/v1/auth/me", "/api/v1/entitlements", "/healthz",
		"/api/v1/collectors/enroll", "/api/v1/users/u1/data-export",
		// WP-671 (F-400): the review queue, its deprecated alias and the
		// location tree are cmdb_core (rack nodes and scope_suggested items
		// check their feature in the handler).
		"/api/v1/review-items", "/api/v1/review-items/r1/resolve", "/api/v1/discovery/review-items",
		"/api/v1/discovery/review-items/r1/resolve", "/api/v1/locations", "/api/v1/locations/tree", "/api/v1/locations/l1"} {
		if _, gated := RequiredFeature(http.MethodGet, path); gated {
			t.Errorf("expected %q to stay ungated", path)
		}
	}
	// The review queue stays core for every method but the ticket action.
	if _, gated := RequiredFeature(http.MethodPost, "/api/v1/review-items/r1/resolve"); gated {
		t.Error("POST /api/v1/review-items/r1/resolve is gated, want core")
	}
}

// TestRequireFeatureAndChecker covers WP-705 (ENT-03, E-44): the route
// middleware for a sub-function refuses a request without the feature
// with 403 feature-not-entitled and passes it once granted; *Service is the
// Checker of the handlers.
func TestRequireFeatureAndChecker(t *testing.T) {
	svc := testService(t, PlanEssential)
	var checker Checker = svc
	ctx := context.Background()
	if checker.IsEnabled(ctx, "org-1", FeatureTicketingStandard) || !checker.IsEnabled(ctx, "org-1", FeatureTicketingEssential) {
		t.Fatal("Checker does not follow the essential plan")
	}

	h := svc.RequireFeature(FeatureTicketingStandard)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func() (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/tickets/t1/sla", nil)))
		var problem api.ProblemDetail
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		return w.Code, problem.Type
	}
	if code, problem := serve(); code != http.StatusForbidden || problem != api.ProblemFeatureNotEntitled {
		t.Errorf("without ticketing_standard: %d %q, want 403 feature-not-entitled", code, problem)
	}
	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureTicketingStandard,
		Plan: PlanStandard, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if code, _ := serve(); code != http.StatusNoContent {
		t.Errorf("with ticketing_standard: %d, want 204", code)
	}
	if !checker.IsEnabled(ctx, "org-1", FeatureTicketingStandard) {
		t.Error("Checker does not see the grant")
	}

	// Without a tenant the middleware refuses the request.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/tickets/t1/sla", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("without tenant: %d, want 401", w.Code)
	}
}

// TestMiddlewareRequiresEveryKeyOfARule covers the multi-key rule of E-44:
// a label needs barcode and assets_basic.
func TestMiddlewareRequiresEveryKeyOfARule(t *testing.T) {
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx := context.Background()
	serve := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/assets/a1/label.svg", nil)))
		return w.Code
	}
	for _, feature := range []string{FeatureBarcode, FeatureAssetsBasic} {
		if code := serve(); code != http.StatusForbidden {
			t.Errorf("label before %s: %d, want 403", feature, code)
		}
		if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: feature, Plan: PlanStandard, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if code := serve(); code != http.StatusNoContent {
		t.Errorf("label with barcode and assets_basic: %d, want 204", code)
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
	if !slices.Equal(Quotas, []string{"max_cis", "max_collectors", "max_users", "max_api_keys", "max_storage_mb",
		"max_monitored_targets", "max_probes_per_minute", "min_probe_interval_s"}) {
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
	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDiscovery, Plan: PlanStandard, Enabled: true,
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
		"negative":      {OrganizationID: "org-1", FeatureKey: FeatureCMDBCore, Enabled: true, Limits: map[string]int64{LimitMaxCIs: -1}},
	} {
		var validation *ValidationError
		if _, err := svc.Grant(ctx, &ent); !errors.As(err, &validation) {
			t.Errorf("%s: %v, want a validation error", name, err)
		}
	}
	if !svc.IsEnabled(ctx, "org-1", FeatureCMDBCore) {
		t.Error("cmdb_core not active")
	}
}

// TestGrantAcceptsOnlyENT02Keys covers WP-564 (ENT-02, F-209) and WP-244
// (ENT-06, E-44): Grant stores only the feature keys of ENT-02, the
// phase-2 keys of ENT-06, the phase-3 keys (WP-862, E-151), the module key
// of phase 4 in use and the add-ons, and only the quotas max_cis, max_collectors, max_users,
// max_api_keys and max_storage_mb; any other key, the former keys
// inventory, stocktake, ticketing, workflow_forms and compliance among
// them, is a ValidationError
// naming the field, and nothing is stored.
func TestGrantAcceptsOnlyENT02Keys(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})

	keys := FeatureKeys()
	for _, f := range slices.Concat(Phase1Features, Addons, []string{"assets_basic", "documents", "import",
		"ticketing_essential", "ticketing_standard", "signature", "stock", "stocktake_standard", "barcode",
		"gps_standard", "workplace", "export_datev", "internal_orders",
		"monitoring", "ipam", "monitoring_network", "credential_vault", "notifications_sms_pager", "workflows",
		"compliance_findings", "monitoring_access", "monitoring_analytics", "compliance_automation",
		"endpoint_agent", "admp"}) {
		if !slices.Contains(keys, f) {
			t.Errorf("FeatureKeys lacks %s", f)
		}
	}
	if len(keys) != 35 {
		t.Errorf("FeatureKeys %v, want the 8 keys of ENT-02, 13 keys of phase 2, 10 keys of phase 3, 1 module key of phase 4 and 3 add-ons", keys)
	}
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		for _, f := range PlanFeatures(plan) {
			if !slices.Contains(keys, f) {
				t.Errorf("plan %s includes %s, which is no known feature key", plan, f)
			}
		}
	}

	for name, tc := range map[string]struct {
		ent   Entitlement
		field string
	}{
		"feature key with a hyphen": {Entitlement{FeatureKey: "rack-view", Enabled: true}, "feature_key"},
		"feature key not created":   {Entitlement{FeatureKey: "ticketing_pro", Enabled: true}, "feature_key"},
		"former key inventory":      {Entitlement{FeatureKey: "inventory", Enabled: true}, "feature_key"},
		"former key stocktake":      {Entitlement{FeatureKey: "stocktake", Enabled: true}, "feature_key"},
		"former key ticketing":      {Entitlement{FeatureKey: "ticketing", Enabled: true}, "feature_key"},
		"former key workflow_forms": {Entitlement{FeatureKey: "workflow_forms", Enabled: true}, "feature_key"},
		"former key compliance":     {Entitlement{FeatureKey: "compliance", Enabled: true}, "feature_key"},
		"feature key in upper case": {Entitlement{FeatureKey: "Discovery", Enabled: true}, "feature_key"},
		"feature key with a space":  {Entitlement{FeatureKey: "discovery ", Enabled: true}, "feature_key"},
		"empty feature key":         {Entitlement{FeatureKey: " ", Enabled: true}, "feature_key"},
		"quota typo":                {Entitlement{FeatureKey: FeatureRackView, Enabled: true, Limits: map[string]int64{"max_cis_typo": 5}}, "limits"},
		"quota in upper case":       {Entitlement{FeatureKey: FeatureCMDBCore, Enabled: true, Limits: map[string]int64{"MAX_CIS": 5}}, "limits"},
		"negative quota":            {Entitlement{FeatureKey: FeatureCMDBCore, Enabled: true, Limits: map[string]int64{LimitMaxCIs: -1}}, "limits"},
		"disabled cmdb_core":        {Entitlement{FeatureKey: FeatureCMDBCore}, "enabled"},
		"bad source":                {Entitlement{FeatureKey: FeatureWebhooks, Enabled: true, Source: "gift"}, "source"},
	} {
		tc.ent.OrganizationID = "org-1"
		var validation *ValidationError
		if _, err := svc.Grant(ctx, &tc.ent); !errors.As(err, &validation) || validation.Field != tc.field {
			t.Errorf("%s: %v, want a validation error of %s", name, err, tc.field)
		}
	}
	if list, err := svc.List(ctx, "org-1"); err != nil || len(list) != 1 || list[0].FeatureKey != FeatureCMDBCore {
		t.Errorf("refused grants stored rows: %+v, %v", list, err)
	}

	for _, f := range keys {
		if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: f, Enabled: true}); err != nil {
			t.Errorf("grant %s: %v", f, err)
		}
	}
	for _, quota := range Quotas {
		if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: QuotaFeature[quota], Enabled: true,
			Limits: map[string]int64{quota: 3}}); err != nil {
			t.Errorf("quota %s: %v", quota, err)
		}
	}
}

// TestPlanMatrixFollowsE44 covers WP-244 (ENT-06 in the version of E-44,
// E-34, E-39, CH14): every plan includes assets_basic, documents, import
// and ticketing_essential; standard adds ticketing_standard, signature,
// stock, stocktake_standard, barcode, gps_standard, workplace and
// export_datev (essential has no stock) and from WP-862 (E-151) monitoring
// and ipam; pro adds internal_orders, monitoring_network, credential_vault,
// notifications_sms_pager, workflows and compliance_findings; enterprise
// monitoring_access, monitoring_analytics, compliance_automation and
// endpoint_agent. No plan includes an add-on (admp among them), a former key
// or a key not created.
func TestPlanMatrixFollowsE44(t *testing.T) {
	essential := slices.Concat(Phase1Features, []string{"assets_basic", "documents", "import", "ticketing_essential"})
	standard := slices.Concat(essential, []string{"ticketing_standard", "signature", "stock", "stocktake_standard",
		"barcode", "gps_standard", "workplace", "export_datev", "monitoring", "ipam"})
	pro := slices.Concat(standard, []string{"internal_orders", "monitoring_network", "credential_vault",
		"notifications_sms_pager", "workflows", "compliance_findings"})
	enterprise := slices.Concat(pro, []string{"monitoring_access", "monitoring_analytics", "compliance_automation",
		"endpoint_agent"})
	for plan, want := range map[Plan][]string{PlanEssential: essential, PlanStandard: standard, PlanPro: pro, PlanEnterprise: enterprise} {
		got := PlanFeatures(plan)
		if !slices.Equal(sorted(got), sorted(want)) {
			t.Errorf("plan %s: %v, want %v", plan, got, want)
		}
		for _, f := range slices.Concat(Addons, []string{"inventory", "stocktake", "ticketing", "workflow_forms", "compliance", "ticketing_pro",
			"stocktake_pro", "disposition", "audit_extended", "usage_tracking"}) {
			if slices.Contains(got, f) {
				t.Errorf("plan %s includes %s", plan, f)
			}
		}
	}
	if slices.Contains(PlanFeatures(PlanEssential), FeatureStock) {
		t.Error("essential includes stock")
	}
	for _, f := range []string{FeatureTicketingStandard, FeatureStocktakeStandard} {
		if !slices.Contains(PlanFeatures(PlanStandard), f) {
			t.Errorf("standard lacks %s", f)
		}
	}
	if slices.Contains(PlanFeatures(PlanStandard), FeatureInternalOrders) || !slices.Contains(PlanFeatures(PlanPro), FeatureInternalOrders) {
		t.Error("internal_orders: want it from pro on")
	}
	// The phase-2 keys in the order of the plans.
	if !slices.Equal(Phase2Features, []string{"assets_basic", "documents", "import", "ticketing_essential",
		"ticketing_standard", "signature", "stock", "stocktake_standard", "barcode", "gps_standard", "workplace",
		"export_datev", "internal_orders"}) {
		t.Errorf("Phase2Features %v", Phase2Features)
	}
	// Standard holds the monitoring core only; the extended functions start
	// with pro (decision of the client of 2026-11-09, E-151).
	if !slices.Contains(PlanFeatures(PlanStandard), FeatureMonitoring) || slices.Contains(PlanFeatures(PlanStandard), FeatureMonitoringNetwork) ||
		slices.Contains(PlanFeatures(PlanEssential), FeatureMonitoring) {
		t.Error("monitoring: want it from standard on, monitoring_network from pro on")
	}
	// An unknown plan provisions the essential plan.
	if !slices.Equal(PlanFeatures("trial"), PlanFeatures(PlanEssential)) {
		t.Errorf("unknown plan: %v", PlanFeatures("trial"))
	}
}

// TestStocktakeStandardIsTheOnlyStocktakeKey covers WP-288 (INV-02,
// ENT-06): phase 2 creates no stocktake_pro; stocktake_standard is the
// only key with the prefix stocktake in any plan, pro and enterprise
// include it, essential includes none.
func TestStocktakeStandardIsTheOnlyStocktakeKey(t *testing.T) {
	if slices.Contains(FeatureKeys(), "stocktake_pro") {
		t.Error("stocktake_pro is a feature key in phase 2")
	}
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		var keys []string
		for _, f := range PlanFeatures(plan) {
			if strings.HasPrefix(f, "stocktake") {
				keys = append(keys, f)
			}
		}
		want := []string{FeatureStocktakeStandard}
		if plan == PlanEssential {
			want = nil
		}
		if !slices.Equal(keys, want) {
			t.Errorf("plan %s: stocktake keys %v, want %v", plan, keys, want)
		}
	}
}

// TestStorageQuotaBelongsToDocuments covers WP-244 (MGT-01, ENT-06, E-34,
// E-44): max_storage_mb is a quota of documents (QuotaFeature); it has no
// plan default and gates only when it is stored in the limits of the
// documents row.
func TestStorageQuotaBelongsToDocuments(t *testing.T) {
	ctx := context.Background()
	if LimitMaxStorageMB != "max_storage_mb" || QuotaFeature[LimitMaxStorageMB] != FeatureDocuments {
		t.Fatalf("max_storage_mb belongs to %q, want documents", QuotaFeature[LimitMaxStorageMB])
	}
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		svc := testService(t, plan)
		if got, err := svc.Quota(ctx, "org-1", LimitMaxStorageMB); err != nil || got != nil {
			t.Errorf("plan %s: max_storage_mb %v, %v; want unlimited without a stored limit", plan, deref(got), err)
		}
	}
	svc := testService(t, PlanEssential)
	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDocuments, Plan: PlanEssential,
		Enabled: true, Limits: map[string]int64{LimitMaxStorageMB: 1000}}); err != nil {
		t.Fatalf("max_storage_mb on documents: %v", err)
	}
	if got, err := svc.Quota(ctx, "org-1", LimitMaxStorageMB); err != nil || got == nil || *got != 1000 {
		t.Errorf("stored max_storage_mb %v, %v; want 1000", deref(got), err)
	}
	// Without documents the quota allows nothing.
	if _, err := svc.Grant(ctx, &Entitlement{OrganizationID: "org-1", FeatureKey: FeatureDocuments, Plan: PlanEssential,
		Limits: map[string]int64{LimitMaxStorageMB: 1000}}); err != nil {
		t.Fatal(err)
	}
	var notEntitled *FeatureNotEntitledError
	if err := svc.AllowCreate(ctx, "org-1", LimitMaxStorageMB, 0); !errors.As(err, &notEntitled) || notEntitled.FeatureKey != FeatureDocuments {
		t.Errorf("max_storage_mb without documents: %v, want feature-not-entitled documents", err)
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func deref(v *int64) any {
	if v == nil {
		return "unlimited"
	}
	return *v
}

// TestPhase3RouteGates covers WP-865 (PKG-08, ENT-06, E-151, E-160): every
// prefix and sub-path rule of the P3 modules maps to its feature key; the
// recommendation of an incident is monitoring_network (decision of the
// client of 2026-11-09); the provider callback and the organization export
// and deletion are core.
func TestPhase3RouteGates(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         []string
	}{
		{http.MethodGet, "/api/v1/monitoring/probes", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/monitoring/alarms", []string{FeatureMonitoring}},
		{http.MethodPost, "/api/v1/monitoring/alarms/x1/ack", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/monitoring/alarm-settings", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/monitoring/incidents/x1", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/monitoring/incidents/x1/recommendation", []string{FeatureMonitoringNetwork}},
		{http.MethodGet, "/api/v1/monitoring/sla", []string{FeatureMonitoringAnalytics}},
		{http.MethodPost, "/api/v1/monitoring/sla/targets", []string{FeatureMonitoringAnalytics}},
		{http.MethodGet, "/api/v1/monitoring/forecasts/x1", []string{FeatureMonitoringAnalytics}},
		{http.MethodGet, "/api/v1/monitoring/metrics", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/collector-groups/x1", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/cis/x1/availability", []string{FeatureMonitoring}},
		{http.MethodPut, "/api/v1/cis/x1/credentials", []string{FeatureMonitoring}},
		{http.MethodGet, "/api/v1/cis/x1/credentials/effective", []string{FeatureMonitoring}},
		{http.MethodPost, "/api/v1/credential-bindings", []string{FeatureMonitoring}},
		{http.MethodPost, "/api/v1/vault-connections", []string{FeatureCredentialVault}},
		{http.MethodGet, "/api/v1/cis/x1/configs", []string{FeatureMonitoringNetwork}},
		{http.MethodGet, "/api/v1/ipam-sections", []string{FeatureIPAM}},
		{http.MethodPost, "/api/v1/ipam/imports", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/asns/x1", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/asn-ranges", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/bgp-peerings", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/nat-translations", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/nameserver-sets", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/vlan-domains", []string{FeatureIPAM}},
		{http.MethodGet, "/api/v1/cis/x1/addresses", []string{FeatureIPAM}},
		{http.MethodPost, "/api/v1/subnets/x1/scan", []string{FeatureIPAM, FeatureMonitoring}},
		{http.MethodGet, "/api/v1/subnets/x1/rpki", []string{FeatureIPAM, FeatureMonitoringNetwork}},
		{http.MethodGet, "/api/v1/admp/connections/x1", []string{FeatureADMP}},
		{http.MethodGet, "/api/v1/cis/x1/admp-connections", []string{FeatureADMP}},
		{http.MethodGet, "/api/v1/network-interfaces/x1/admp", []string{FeatureADMP}},
		{http.MethodGet, "/api/v1/escalation-policies", []string{FeatureNotificationsSMSPager}},
		{http.MethodPost, "/api/v1/notification-escalations/x1/acknowledge", []string{FeatureNotificationsSMSPager}},
		{http.MethodPost, "/api/v1/notification-callbacks/o1/c1", nil},
		{http.MethodPost, "/api/v1/org/exports", nil},
		{http.MethodGet, "/api/v1/org/exports/x1/download", nil},
		{http.MethodPost, "/api/v1/org/deletion", nil},
		// The core resources of NET-05 stay ungated.
		{http.MethodGet, "/api/v1/subnets/x1", nil},
		{http.MethodGet, "/api/v1/cis/x1", nil},
	} {
		if got := RequiredFeatures(c.method, c.path); !slices.Equal(got, c.want) {
			t.Errorf("RequiredFeatures(%s, %q) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestProbeTypeFeature covers WP-865 (E-151): every key of the probe type
// registry maps to its feature key; long forms and unknown types are errors.
func TestProbeTypeFeature(t *testing.T) {
	want := map[string]string{}
	for _, k := range []string{"icmp", "tcp", "http", "dns"} {
		want[k] = FeatureMonitoring
	}
	for _, k := range []string{"snmp_status", "snmp_if", "snmp_custom", "redfish", "bgp", "ospf", "isis", "bfd",
		"optical", "dhcp", "stamp", "twamp_control", "gnmi", "rpki_rtr", "rpki_roa"} {
		want[k] = FeatureMonitoringNetwork
	}
	for _, k := range []string{"bng", "pon", "tr069", "usp"} {
		want[k] = FeatureMonitoringAccess
	}
	if len(ProbeTypes()) != 23 || len(want) != 23 {
		t.Fatalf("probe types %v, want the 23 keys of the registry", ProbeTypes())
	}
	for _, k := range ProbeTypes() {
		got, err := ProbeTypeFeature(k)
		if err != nil || got != want[k] {
			t.Errorf("ProbeTypeFeature(%q) = %q, %v; want %q", k, got, err, want[k])
		}
		if !slices.Contains(FeatureKeys(), got) {
			t.Errorf("probe type %s maps to the unknown feature key %s", k, got)
		}
	}
	for _, k := range []string{"icmp_ping", "snmp_interfaces", "tcp_connect", "ping", ""} {
		if f, err := ProbeTypeFeature(k); err == nil {
			t.Errorf("ProbeTypeFeature(%q) = %q, want an error", k, f)
		}
	}
	// Standard holds only the reachability probes (decision of 2026-11-09).
	for _, k := range ProbeTypes() {
		f, _ := ProbeTypeFeature(k)
		if inStandard := slices.Contains(PlanFeatures(PlanStandard), f); inStandard != slices.Contains([]string{"icmp", "tcp", "http", "dns"}, k) {
			t.Errorf("probe type %s in standard = %v", k, inStandard)
		}
	}
}

// TestSubFunctionFeature covers WP-865 (E-151, decision of the client of
// 2026-11-09): correlation is part of every monitoring plan; recommendation,
// threshold rules and configuration change detection start with pro; SLA
// reports and capacity forecasts are analytics.
func TestSubFunctionFeature(t *testing.T) {
	for sub, want := range map[string]string{
		SubFunctionCorrelation:      FeatureMonitoring,
		SubFunctionRecommendation:   FeatureMonitoringNetwork,
		SubFunctionThresholds:       FeatureMonitoringNetwork,
		SubFunctionConfigChange:     FeatureMonitoringNetwork,
		SubFunctionSLAReport:        FeatureMonitoringAnalytics,
		SubFunctionCapacityForecast: FeatureMonitoringAnalytics,
	} {
		if got, err := SubFunctionFeature(sub); err != nil || got != want {
			t.Errorf("SubFunctionFeature(%q) = %q, %v; want %q", sub, got, err, want)
		}
	}
	for _, sub := range []string{"monitoring_correlation", "monitoring_sla", "unknown"} {
		if _, err := SubFunctionFeature(sub); err == nil {
			t.Errorf("SubFunctionFeature(%q): want an error", sub)
		}
	}
}

// TestEntitlementOriginValidate covers WP-880 (PKG-01): an empty origin is
// derived (add-on → addon, plan feature → plan, else manual); an add-on with
// another origin and a non-add-on with origin addon are refused on origin.
func TestEntitlementOriginValidate(t *testing.T) {
	for _, c := range []struct {
		feature string
		plan    Plan
		want    string
	}{
		{FeatureIGA, PlanEnterprise, OriginAddon},
		{FeatureMonitoring, PlanPro, OriginPlan},
		{FeatureEndpointAgent, PlanPro, OriginManual},
		{FeatureCMDBCore, PlanEssential, OriginPlan},
	} {
		e := Entitlement{FeatureKey: c.feature, Plan: c.plan, Enabled: true}
		if err := e.Validate(); err != nil || e.Origin != c.want {
			t.Errorf("%s in %s: origin %q, %v; want %q", c.feature, c.plan, e.Origin, err, c.want)
		}
	}
	for name, e := range map[string]Entitlement{
		"add-on with origin plan":        {FeatureKey: FeatureIGA, Plan: PlanPro, Enabled: true, Origin: OriginPlan},
		"plan feature with origin addon": {FeatureKey: FeatureMonitoring, Plan: PlanPro, Enabled: true, Origin: OriginAddon},
		"unknown origin":                 {FeatureKey: FeatureMonitoring, Plan: PlanPro, Enabled: true, Origin: "gift"},
	} {
		var verr *ValidationError
		if err := e.Validate(); !errors.As(err, &verr) || verr.Field != "origin" {
			t.Errorf("%s: %v, want a ValidationError on origin", name, err)
		}
	}
}

// TestProvisionReportsPlanMismatch covers AK5 of WP-880: the startup check
// reports an active row of origin plan outside the plan of the organization
// (warning and gauge) and changes nothing.
func TestProvisionReportsPlanMismatch(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, Options{Enforce: true})
	ctx := context.Background()
	const org = "org-mismatch"
	for _, f := range PlanFeatures(PlanStandard) {
		if _, err := repo.Upsert(ctx, &Entitlement{OrganizationID: org, FeatureKey: f, Plan: PlanStandard, Enabled: true, Origin: OriginPlan}); err != nil {
			t.Fatal(err)
		}
	}
	// A pro feature of origin plan in a standard organization.
	if _, err := repo.Upsert(ctx, &Entitlement{OrganizationID: org, FeatureKey: FeatureWorkflows, Plan: PlanStandard, Enabled: true, Origin: OriginPlan}); err != nil {
		t.Fatal(err)
	}
	if got := PlanMismatch(PlanStandard, mustList(t, repo, org)); !slices.Equal(got, []string{FeatureWorkflows}) {
		t.Errorf("PlanMismatch = %v, want [workflows]", got)
	}
	if _, err := svc.ProvisionOrganizations(ctx, map[string]Plan{org: PlanStandard}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(planMismatchOrgs); got != 1 {
		t.Errorf("gauge reticora_entitlement_plan_mismatch_organizations = %v, want 1", got)
	}
	var kept bool
	for _, e := range mustList(t, repo, org) {
		kept = kept || (e.FeatureKey == FeatureWorkflows && e.Enabled && e.Origin == OriginPlan)
	}
	if !kept {
		t.Errorf("the startup check changed the deviating row")
	}
	// A manual grant outside the plan is no deviation.
	other := NewMemoryRepository()
	if _, err := other.Upsert(ctx, &Entitlement{OrganizationID: org, FeatureKey: FeatureWorkflows, Plan: PlanStandard, Enabled: true, Origin: OriginManual}); err != nil {
		t.Fatal(err)
	}
	if got := PlanMismatch(PlanStandard, mustList(t, other, org)); len(got) != 0 {
		t.Errorf("a manual grant counts as deviation: %v", got)
	}
}

func mustList(t *testing.T, repo *MemoryRepository, org string) []Entitlement {
	t.Helper()
	items, err := repo.List(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// TestPlanQuotaDefaultsFollowE151 covers AK2 of WP-881 (E-153): the default
// limits of the four plans; the monitoring quotas only where the plan
// includes monitoring.
func TestPlanQuotaDefaultsFollowE151(t *testing.T) {
	want := map[Plan]map[string]int64{
		PlanEssential: {LimitMaxCIs: 500, LimitMaxCollectors: 2, LimitMaxUsers: 5, LimitMaxStorageMB: 1000},
		PlanStandard: {LimitMaxCIs: 2500, LimitMaxCollectors: 5, LimitMaxUsers: 25, LimitMaxStorageMB: 10000,
			LimitMaxMonitoredTargets: 500, LimitMaxProbesPerMinute: 1000, LimitMinProbeIntervalS: 30},
		PlanPro: {LimitMaxCIs: 10000, LimitMaxCollectors: 20, LimitMaxUsers: 100, LimitMaxStorageMB: 50000,
			LimitMaxMonitoredTargets: 5000, LimitMaxProbesPerMinute: 10000, LimitMinProbeIntervalS: 30},
		PlanEnterprise: {LimitMaxCIs: 50000, LimitMaxMonitoredTargets: 25000, LimitMaxProbesPerMinute: 30000, LimitMinProbeIntervalS: 10},
	}
	for plan, w := range want {
		got := PlanQuotaDefaults(plan)
		if !maps.Equal(got, w) {
			t.Errorf("PlanQuotaDefaults(%s) = %v, want %v", plan, got, w)
		}
		_, hasMonitoring := got[LimitMaxMonitoredTargets]
		if hasMonitoring != slices.Contains(PlanFeatures(plan), FeatureMonitoring) {
			t.Errorf("%s: monitoring quotas %v, monitoring in plan %v", plan, hasMonitoring, !hasMonitoring)
		}
	}
}

// TestTargetTypeQuotaKeys covers AK1 of WP-881 (E-154): max_targets_<type>
// is a monitoring quota; a malformed key is refused on limits.
func TestTargetTypeQuotaKeys(t *testing.T) {
	ok := Entitlement{FeatureKey: FeatureMonitoring, Plan: PlanStandard, Enabled: true, Limits: map[string]int64{"max_targets_icmp": 100}}
	if err := ok.Validate(); err != nil {
		t.Errorf("max_targets_icmp: %v", err)
	}
	if f, known := QuotaFeatureOf("max_targets_icmp"); !known || f != FeatureMonitoring {
		t.Errorf("QuotaFeatureOf(max_targets_icmp) = %q, %v", f, known)
	}
	for _, key := range []string{"max_targets_", "max_targetsx", "max_targets_ICMP", "max_whatever"} {
		e := Entitlement{FeatureKey: FeatureMonitoring, Plan: PlanStandard, Enabled: true, Limits: map[string]int64{key: 1}}
		var verr *ValidationError
		if err := e.Validate(); !errors.As(err, &verr) || verr.Field != "limits" {
			t.Errorf("%s: %v, want a ValidationError on limits", key, err)
		}
	}
}

// TestCheckInterval covers AK6 of WP-881: an interval below
// min_probe_interval_s is a 422 on interval_s; equal and above pass; without
// a stored value every interval passes.
func TestCheckInterval(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, Options{Enforce: true})
	ctx := context.Background()
	if _, err := repo.Upsert(ctx, &Entitlement{OrganizationID: "o", FeatureKey: FeatureMonitoring, Plan: PlanStandard, Enabled: true,
		Limits: map[string]int64{LimitMinProbeIntervalS: 30}}); err != nil {
		t.Fatal(err)
	}
	var fe *api.FieldError
	if err := svc.CheckInterval(ctx, "o", 29); !errors.As(err, &fe) || fe.Field != "interval_s" || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("29 s: %v, want 422 on interval_s", err)
	}
	for _, sec := range []int{30, 60} {
		if err := svc.CheckInterval(ctx, "o", sec); err != nil {
			t.Errorf("%d s: %v", sec, err)
		}
	}
	if _, err := repo.Upsert(ctx, &Entitlement{OrganizationID: "p", FeatureKey: FeatureMonitoring, Plan: PlanStandard, Enabled: true,
		Limits: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckInterval(ctx, "p", 1); err != nil {
		t.Errorf("without min_probe_interval_s: %v", err)
	}
}

// TestLimitOrigin covers AK4 of WP-881.
func TestLimitOrigin(t *testing.T) {
	v := func(n int64) *int64 { return &n }
	for _, c := range []struct {
		plan  Plan
		quota string
		value *int64
		want  string
	}{
		{PlanStandard, LimitMaxCIs, v(2500), "plan"},
		{PlanStandard, LimitMaxCIs, v(3000), "override"},
		{PlanStandard, LimitMaxCIs, nil, "override"},
		{PlanEnterprise, LimitMaxUsers, nil, "plan"},
		{PlanEnterprise, LimitMaxUsers, v(10), "override"},
	} {
		if got := LimitOrigin(c.plan, c.quota, c.value); got != c.want {
			t.Errorf("LimitOrigin(%s, %s, %v) = %s, want %s", c.plan, c.quota, c.value, got, c.want)
		}
	}
}
