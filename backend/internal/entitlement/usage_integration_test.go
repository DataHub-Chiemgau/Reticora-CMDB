package entitlement_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// usageEnv is organization A on standard with the counters of the router.
type usageEnv struct {
	f   *scopetest.Fixture
	svc *entitlement.Service
}

func newUsageEnv(t *testing.T) *usageEnv {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "67")
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true}).
		WithUsageCounters(server.UsageCounters(&repos))
	bg := context.Background()
	if _, err := f.Admin.Exec(bg, `DELETE FROM entitlement WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET plan = 'standard' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	ctx := f.OrgCtx(f.OrgA)
	defaults := entitlement.PlanQuotaDefaults(entitlement.PlanStandard)
	for _, feature := range entitlement.PlanFeatures(entitlement.PlanStandard) {
		limits := map[string]int64{}
		for quota, value := range defaults {
			if owner, _ := entitlement.QuotaFeatureOf(quota); owner == feature {
				limits[quota] = value
			}
		}
		if feature == entitlement.FeatureCMDBCore {
			limits[entitlement.LimitMaxCIs] = 3000 // override
		}
		if _, err := svc.Grant(ctx, &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: feature,
			Plan: entitlement.PlanStandard, Enabled: true, Limits: limits, Origin: entitlement.OriginPlan}); err != nil {
			t.Fatal(err)
		}
	}
	return &usageEnv{f: f, svc: svc}
}

func byQuota(items []entitlement.QuotaUsage) map[string]entitlement.QuotaUsage {
	out := map[string]entitlement.QuotaUsage{}
	for _, u := range items {
		out[u.Quota] = u
	}
	return out
}

// TestUsageMatchesCounts covers PKG-05 (WP-885): 3 CIs, 1 collector, 2
// users, 1 API key and 5 MiB raise used by exactly these counts (the
// counters of the AllowCreate callers); limit and origin follow the stored
// value (max_cis 3,000 override, max_users 25 plan); every quota of Quotas
// is reported, the monitoring counts are 0.
func TestUsageMatchesCounts(t *testing.T) {
	e := newUsageEnv(t)
	f := e.f
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	before, err := e.svc.Usage(ctx, f.OrgA, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f.CI(t, f.OrgA, "", "usage-ci-"+string(rune('a'+i)))
	}
	owner := f.AppUser(t, f.OrgA, "usage-1")
	f.AppUser(t, f.OrgA, "usage-2")
	if _, err = f.Admin.Exec(bg, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, created_by)
		VALUES ($1, 'usage-key', 'usage-hash-67', 'usg67', $2)`, f.OrgA, owner); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'usage-collector', 'active')`,
		`INSERT INTO organization_usage (organization_id, storage_bytes) VALUES ($1, 5 * 1048576)
			ON CONFLICT (organization_id) DO UPDATE SET storage_bytes = organization_usage.storage_bytes + 5 * 1048576`,
	} {
		if _, err = f.Admin.Exec(bg, sql, f.OrgA); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	after, err := e.svc.Usage(ctx, f.OrgA, true)
	if err != nil {
		t.Fatal(err)
	}
	b, a := byQuota(before), byQuota(after)
	for _, quota := range entitlement.Quotas {
		if _, ok := a[quota]; !ok {
			t.Errorf("quota %s missing from the report", quota)
		}
	}
	for quota, delta := range map[string]int64{entitlement.LimitMaxCIs: 3, entitlement.LimitMaxUsers: 2,
		entitlement.LimitMaxCollectors: 1, entitlement.LimitMaxAPIKeys: 1, entitlement.LimitMaxStorageMB: 5} {
		if a[quota].Used == nil || b[quota].Used == nil || *a[quota].Used-*b[quota].Used != delta {
			t.Errorf("%s used before %v after %v, want +%d", quota, b[quota].Used, a[quota].Used, delta)
		}
	}
	if u := a[entitlement.LimitMaxCIs]; u.Limit == nil || *u.Limit != 3000 || u.Origin != "override" {
		t.Errorf("max_cis %+v, want limit 3000 override", u)
	}
	if u := a[entitlement.LimitMaxUsers]; u.Limit == nil || *u.Limit != 25 || u.Origin != entitlement.OriginPlan {
		t.Errorf("max_users %+v, want limit 25 plan", u)
	}
	if u := a[entitlement.LimitMaxAPIKeys]; u.Limit != nil || u.Origin != entitlement.OriginPlan {
		t.Errorf("max_api_keys %+v, want unlimited plan", u)
	}
	if u := a[entitlement.LimitMaxMonitoredTargets]; u.Used == nil || *u.Used != 0 || u.Limit == nil || *u.Limit != 500 {
		t.Errorf("max_monitored_targets %+v, want used 0 limit 500", u)
	}
	if u := a[entitlement.LimitMinProbeIntervalS]; u.Used != nil {
		t.Errorf("min_probe_interval_s used %v, want null", *u.Used)
	}
}

// usageRequest calls GET /entitlements/usage with the scope.
func (e *usageEnv) usageRequest(t *testing.T, scope *database.TenantScope) entitlement.QuotaUsageResponse {
	t.Helper()
	mux := chi.NewRouter()
	entitlement.NewHandler(e.svc).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/entitlements/usage", nil)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), scope), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: scope.UserID})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", w.Code, w.Body.String())
	}
	var resp entitlement.QuotaUsageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestUsageHidesCountsForScopedPrincipal covers F-414: a principal limited
// to a client sees the limits but no counts; an org-wide one sees both.
func TestUsageHidesCountsForScopedPrincipal(t *testing.T) {
	e := newUsageEnv(t)
	orgWide := database.OrgWideScope(e.f.OrgA, e.f.User)
	resp := e.usageRequest(t, &orgWide)
	if u := byQuota(resp.Data)[entitlement.LimitMaxCIs]; u.Used == nil || resp.Plan != entitlement.PlanStandard {
		t.Errorf("org-wide principal: %+v plan %s", u, resp.Plan)
	}
	scoped := database.OrgWideScope(e.f.OrgA, e.f.User)
	scoped.Clients = database.ScopeIDs(e.f.Client1)
	for _, u := range e.usageRequest(t, &scoped).Data {
		if u.Used != nil {
			t.Errorf("scoped principal sees %s used %d", u.Quota, *u.Used)
		}
		if u.Quota == entitlement.LimitMaxCIs && (u.Limit == nil || *u.Limit != 3000) {
			t.Errorf("scoped principal: max_cis limit %v", u.Limit)
		}
	}
}

// TestPlanMatrixEqualsPlanFeatures covers AK3 for the tenant path: the
// matrix is PlanFeatures and PlanQuotaDefaults of the four plans with the
// add-ons.
func TestPlanMatrixEqualsPlanFeatures(t *testing.T) {
	mux := chi.NewRouter()
	entitlement.NewHandler(entitlement.NewService(nil, entitlement.Options{})).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/entitlements/plans", nil))
	var resp entitlement.PlanMatrixResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil || len(resp.Data) != 4 {
		t.Fatalf("plans: %d %s", w.Code, w.Body.String())
	}
	for _, p := range resp.Data {
		if !slices.Equal(p.Features, entitlement.PlanFeatures(p.Plan)) {
			t.Errorf("%s features %v", p.Plan, p.Features)
		}
		for quota, value := range entitlement.PlanQuotaDefaults(p.Plan) {
			if p.QuotaDefaults[quota] != value {
				t.Errorf("%s %s = %d, want %d", p.Plan, quota, p.QuotaDefaults[quota], value)
			}
		}
	}
	if len(resp.Addons) != len(entitlement.Addons) {
		t.Errorf("addons %v", resp.Addons)
	}
}

// TestUsageRequiresEntitlementRead covers AK2: both tenant paths require
// entitlement:read (route table of the authorizing router); a
// client_technician, who lacks it, gets 403.
func TestUsageRequiresEntitlementRead(t *testing.T) {
	table, err := os.ReadFile("../server/testdata/route_permissions.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"GET /api/v1/entitlements/usage entitlement:read", "GET /api/v1/entitlements/plans entitlement:read"} {
		if !slices.Contains(strings.Split(string(table), "\n"), line) {
			t.Errorf("route table lacks %q", line)
		}
	}
	keys, ok := permission.StandardRolePermissions("client_technician")
	if !ok || slices.Contains(keys, string(identity.PermEntitlementRead)) {
		t.Fatalf("client_technician permissions %v", keys)
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	h := identity.RequirePermission(identity.PermEntitlementRead)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/entitlements/usage", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(identity.WithPermissions(req.Context(), perms)))
	if w.Code != http.StatusForbidden {
		t.Errorf("client_technician: %d, want 403", w.Code)
	}
}

// TestOperatorUsageOfOrganization covers AK2 and AK3 for the operator
// paths: the usage of an organization with counts (200), an unknown
// organization 404, a request without operator credentials 401, and the
// plan matrix.
func TestOperatorUsageOfOrganization(t *testing.T) {
	e := newUsageEnv(t)
	const token = "usage-break-glass-token-with-32-characters"
	mux := chi.NewRouter()
	operator.NewHandler(operator.Config{BreakGlassToken: token}, nil, nil, operator.NewAuditor(e.f.App), e.f.App).
		WithEntitlements(e.svc).RegisterRoutes(mux)
	get := func(path string, operatorToken bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if operatorToken {
			req.Header.Set(operator.BreakGlassHeader, token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := get("/api/v1/admin/orgs/"+e.f.OrgA+"/usage", true)
	var resp entitlement.QuotaUsageResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("operator usage: %d %s", w.Code, w.Body.String())
	}
	if u := byQuota(resp.Data)[entitlement.LimitMaxCIs]; u.Used == nil || u.Limit == nil || *u.Limit != 3000 || resp.Plan != entitlement.PlanStandard {
		t.Errorf("operator usage max_cis %+v plan %s", u, resp.Plan)
	}
	if w = get("/api/v1/admin/orgs/00000000-0000-4000-8000-0000000000fe/usage", true); w.Code != http.StatusNotFound {
		t.Errorf("unknown organization: %d", w.Code)
	}
	if w = get("/api/v1/admin/orgs/"+e.f.OrgA+"/usage", false); w.Code != http.StatusUnauthorized {
		t.Errorf("without operator credentials: %d, want 401", w.Code)
	}
	var matrix entitlement.PlanMatrixResponse
	if w = get("/api/v1/admin/plans", true); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &matrix) != nil || len(matrix.Data) != 4 {
		t.Errorf("operator plan matrix: %d %s", w.Code, w.Body.String())
	}
}
