package entitlement_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// enforcedRoutes are the gated paths of WP-072 with stub handlers behind the
// entitlement middleware, plus the GraphQL endpoint and an ungated core route.
func enforcedRoutes(svc *entitlement.Service) http.Handler {
	mux := chi.NewRouter()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	mux.Post("/api/v1/ingest/bulk", ok)
	mux.Post("/api/v1/discovery/ingest", ok)
	mux.Post("/api/v1/discovery/jobs", ok)
	mux.Get("/api/v1/discovery/jobs", ok)
	mux.Post("/api/v1/collectors/{id}/heartbeat", ok)
	mux.Get("/api/v1/export/cis", ok)
	mux.Post("/api/v1/export/jobs", ok)
	mux.Get("/api/v1/exports", ok)
	mux.Get("/api/v1/topology", ok)
	mux.Get("/api/v1/cis/{id}/blast-radius", ok)
	mux.Get("/api/v1/cis", ok)
	graphqlbff.NewHandler(ci.NewMemoryRepository(), relationship.NewMemoryRepository()).
		WithEntitlements(svc).RegisterRoutes(mux)
	return svc.Middleware(mux)
}

type response struct {
	status  int
	problem string
	license string
}

func call(t *testing.T, h http.Handler, orgID, method, path string) response {
	t.Helper()
	body := ""
	if path == "/bff/graphql" {
		body = `{"query":"{ cis { totalCount } }"}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	scope := database.OrgWideScope(orgID, "")
	ctx := database.ContextWithTenantScope(req.Context(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: orgID,
		Permissions: []identity.Permission{identity.PermCIRead}, Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	var problem api.ProblemDetail
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	return response{status: w.Code, problem: problem.Type, license: w.Header().Get(entitlement.LicenseStatusHeader)}
}

var gatedCalls = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/ingest/bulk"},
	{http.MethodPost, "/api/v1/discovery/ingest"},
	{http.MethodPost, "/api/v1/discovery/jobs"},
	{http.MethodGet, "/api/v1/discovery/jobs"},
	{http.MethodPost, "/api/v1/collectors/c1/heartbeat"},
	{http.MethodGet, "/api/v1/export/cis"},
	{http.MethodPost, "/api/v1/export/jobs"},
	{http.MethodGet, "/api/v1/exports"},
	{http.MethodGet, "/api/v1/topology"},
	{http.MethodGet, "/api/v1/cis/c1/blast-radius"},
}

// TestEntitlementEnforcement covers WP-072 (ENT-03, ENT-05, ENT-07, API-04,
// CH21) against PostgreSQL and Redis with two service replicas sharing the
// cache:
//   - export, ingest (alias included), topology and GraphQL paths check
//     their feature; an organization without rows has only cmdb_core (no
//     default-plan fallback);
//   - after valid_until only ingest and new scans stop, with the
//     license-expired problem type and the refusal counter; reading,
//     export and the heartbeat (license status header) stay available;
//   - entitlements are cached in Redis and a change on one replica
//     invalidates the other at once;
//   - an exhausted quota has the entitlement-limit problem type.
//
// It runs only with TEST_DATABASE_URL and TEST_REDIS_URL.
func TestEntitlementEnforcement(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" || os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL required; skipping entitlement enforcement test")
	}
	f := scopetest.Seed(t, "64")
	bg := context.Background()
	client, err := redisx.Connect(bg, redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisStore(client.Unwrap())
	for _, org := range []string{f.OrgA, f.OrgB} {
		_ = store.Delete(bg, "entitlements:"+org)
		t.Cleanup(func() { _ = store.Delete(bg, "entitlements:"+org) })
	}

	repo := entitlement.NewPGRepository(f.App)
	replicaA := entitlement.NewService(repo, entitlement.Options{Cache: store, Enforce: true})
	replicaB := entitlement.NewService(repo, entitlement.Options{Cache: store, Enforce: true})
	if n, provErr := replicaA.ProvisionOrganizations(bg, map[string]entitlement.Plan{f.OrgA: entitlement.PlanEssential}); n != 1 || provErr != nil {
		t.Fatalf("provision: %d, %v", n, provErr)
	}
	routesA, routesB := enforcedRoutes(replicaA), enforcedRoutes(replicaB)

	// Provisioned organization: every path passes; the heartbeat reports an
	// active license.
	for _, c := range gatedCalls {
		if got := call(t, routesB, f.OrgA, c.method, c.path); got.status != http.StatusNoContent {
			t.Errorf("org A %s %s: %+v, want 204", c.method, c.path, got)
		}
	}
	if got := call(t, routesB, f.OrgA, http.MethodPost, "/api/v1/collectors/c1/heartbeat"); got.license != "active" {
		t.Errorf("heartbeat license status %q, want active", got.license)
	}

	// No rows: only the core (no default-plan fallback), GraphQL included.
	for _, c := range gatedCalls {
		if got := call(t, routesA, f.OrgB, c.method, c.path); got.status != http.StatusForbidden || got.problem != api.ProblemFeatureNotEntitled {
			t.Errorf("org B %s %s: %+v, want 403 feature-not-entitled", c.method, c.path, got)
		}
	}
	if got := call(t, routesA, f.OrgB, http.MethodGet, "/api/v1/cis"); got.status != http.StatusNoContent {
		t.Errorf("org B core route: %+v", got)
	}
	if got := call(t, routesA, f.OrgB, http.MethodPost, "/bff/graphql"); got.status != http.StatusOK {
		t.Errorf("org B GraphQL core field: %+v", got)
	}

	// The cache lives in Redis: a change that bypasses the service is not
	// seen until the entry expires or is invalidated.
	if _, ok, _ := store.Get(bg, "entitlements:"+f.OrgA); !ok {
		t.Fatal("entitlements of org A not cached in Redis")
	}
	if _, err = f.Admin.Exec(bg, `UPDATE entitlement SET enabled = false WHERE organization_id = $1 AND feature_key = 'topology'`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got := call(t, routesB, f.OrgA, http.MethodGet, "/api/v1/topology"); got.status != http.StatusNoContent {
		t.Errorf("cached topology entitlement: %+v, want the cached state", got)
	}

	// CH21: a grant on replica A invalidates replica B at once; after
	// valid_until only ingest and new scans stop.
	before := entitlement.RefusedIngest()
	expired := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	for _, feature := range []string{entitlement.FeatureDiscovery, entitlement.FeatureExportCSV} {
		if _, err = replicaA.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: feature,
			Plan: entitlement.PlanEssential, Enabled: true, ValidUntil: &expired, Source: "billing"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := call(t, routesB, f.OrgA, http.MethodGet, "/api/v1/topology"); got.status != http.StatusForbidden {
		t.Errorf("topology after invalidation: %+v, want the disabled row", got)
	}
	for _, c := range gatedCalls {
		got := call(t, routesB, f.OrgA, c.method, c.path)
		ingest := entitlement.IsIngest(c.method, c.path)
		switch {
		case c.path == "/api/v1/topology" || c.path == "/api/v1/cis/c1/blast-radius":
		case ingest && (got.status != http.StatusForbidden || got.problem != api.ProblemLicenseExpired):
			t.Errorf("expired %s %s: %+v, want 403 license-expired", c.method, c.path, got)
		case !ingest && got.status != http.StatusNoContent:
			t.Errorf("expired license stopped %s %s: %+v (CH21)", c.method, c.path, got)
		}
	}
	if got := entitlement.RefusedIngest() - before; got != 3 {
		t.Errorf("refused ingest counter grew by %d, want 3", got)
	}
	if got := call(t, routesB, f.OrgA, http.MethodPost, "/api/v1/collectors/c1/heartbeat"); got.status != http.StatusNoContent || got.license != "expired" {
		t.Errorf("heartbeat after expiry: %+v, want 204 with license expired", got)
	}
	if list, _ := replicaB.List(f.OrgCtx(f.OrgA), f.OrgA); !hasEnabled(list, entitlement.FeatureExportCSV) || hasEnabled(list, entitlement.FeatureDiscovery) {
		t.Errorf("listed entitlements after expiry: %+v", list)
	}

	// Renewal resumes ingest.
	if _, err = replicaB.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureDiscovery,
		Plan: entitlement.PlanEssential, Enabled: true, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	if got := call(t, routesA, f.OrgA, http.MethodPost, "/api/v1/discovery/ingest"); got.status != http.StatusNoContent || got.license != "active" {
		t.Errorf("ingest after renewal: %+v", got)
	}

	// Quotas: an exhausted limit has its own problem type.
	if _, err = replicaA.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureCMDBCore,
		Plan: entitlement.PlanEssential, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCIs: 3}}); err != nil {
		t.Fatal(err)
	}
	limitErr := replicaB.AllowCreate(f.OrgCtx(f.OrgA), f.OrgA, entitlement.LimitMaxCIs, 3)
	var exceeded *entitlement.LimitExceededError
	if !errors.As(limitErr, &exceeded) {
		t.Fatalf("quota: %v, want LimitExceededError", limitErr)
	}
	w := httptest.NewRecorder()
	if !api.WriteTypedProblem(w, http.StatusForbidden, limitErr) || !strings.Contains(w.Body.String(), api.ProblemEntitlementLimit) {
		t.Errorf("limit problem: %s", w.Body.String())
	}
}

// TestProvisionAddsMissingPlanFeatures covers WP-470 (ENT-05, F-210)
// against PostgreSQL as reticora_app: an organization that holds only
// cmdb_core from before the plan fallback was removed gets every missing
// feature of its plan at startup; a deactivated export_csv stays
// deactivated and no existing row changes; a second start changes nothing;
// organization B, which is not provisioned, stays untouched.
//
// It runs only with TEST_DATABASE_URL.
func TestProvisionAddsMissingPlanFeatures(t *testing.T) {
	f := scopetest.Seed(t, "64")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Both organizations hold only cmdb_core (with a quota) as before
	// WP-072; organization A has the pro plan.
	exec(`UPDATE organization SET plan = 'pro' WHERE id = $1`, f.OrgA)
	for _, org := range []string{f.OrgA, f.OrgB} {
		exec(`INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, source)
			VALUES ($1, 'cmdb_core', 'essential', true, '{"max_cis": 500}', 'billing')`, org)
	}
	// rows returns the stored rows of the organization, one line each.
	rows := func(org string) map[string]string {
		t.Helper()
		out := map[string]string{}
		r, err := f.Admin.Query(bg, `SELECT feature_key, plan || '|' || enabled || '|' || limits::text || '|' ||
			coalesce(valid_until::text, '-') || '|' || source || '|' || updated_at::text
			FROM entitlement WHERE organization_id = $1`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var key, line string
			if err = r.Scan(&key, &line); err != nil {
				t.Fatal(err)
			}
			out[key] = line
		}
		if err = r.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// start runs the startup provisioning for organization A with the plan
	// stored for it.
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Cache: cache.NewMemoryStore(), Enforce: true})
	start := func() int {
		t.Helper()
		var plan string
		if err := f.Admin.QueryRow(bg, `SELECT plan FROM organization WHERE id = $1`, f.OrgA).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		n, err := svc.ProvisionOrganizations(bg, map[string]entitlement.Plan{f.OrgA: entitlement.Plan(plan)})
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		return n
	}
	routes := enforcedRoutes(svc)
	if got := call(t, routes, f.OrgA, http.MethodGet, "/api/v1/topology"); got.status != http.StatusForbidden {
		t.Fatalf("topology before provisioning: %+v, want 403 (F-210)", got)
	}
	coreA, rowsB := rows(f.OrgA)[entitlement.FeatureCMDBCore], rows(f.OrgB)

	// Start: every feature of the pro plan is active, the core row is kept.
	if n := start(); n != 1 {
		t.Fatalf("first start provisioned %d organizations, want 1", n)
	}
	for _, feature := range entitlement.PlanFeatures(entitlement.PlanPro) {
		if !svc.IsEnabled(f.OrgCtx(f.OrgA), f.OrgA, feature) {
			t.Errorf("feature %s of the pro plan not active after the start", feature)
		}
	}
	stored := rows(f.OrgA)
	if len(stored) != len(entitlement.PlanFeatures(entitlement.PlanPro)) {
		t.Errorf("rows after the start: %v, want exactly the pro plan", stored)
	}
	if stored[entitlement.FeatureCMDBCore] != coreA {
		t.Errorf("cmdb_core row changed: %q, was %q", stored[entitlement.FeatureCMDBCore], coreA)
	}
	for _, addon := range entitlement.Addons {
		if _, ok := stored[addon]; ok {
			t.Errorf("add-on %s provisioned", addon)
		}
	}
	for _, c := range gatedCalls {
		if got := call(t, routes, f.OrgA, c.method, c.path); got.status != http.StatusNoContent {
			t.Errorf("org A %s %s after the start: %+v, want 204", c.method, c.path, got)
		}
	}

	// A deactivated export_csv and an expired discovery stay as they are;
	// only the missing topology row is added again.
	exec(`UPDATE entitlement SET enabled = false WHERE organization_id = $1 AND feature_key = 'export_csv'`, f.OrgA)
	exec(`UPDATE entitlement SET valid_until = now() - interval '1 day' WHERE organization_id = $1 AND feature_key = 'discovery'`, f.OrgA)
	exec(`DELETE FROM entitlement WHERE organization_id = $1 AND feature_key = 'topology'`, f.OrgA)
	before := rows(f.OrgA)
	if n := start(); n != 1 {
		t.Fatalf("start after the deactivation provisioned %d organizations, want 1", n)
	}
	after := rows(f.OrgA)
	if _, ok := after[entitlement.FeatureTopology]; !ok || len(after) != len(before)+1 {
		t.Errorf("rows after the second start: %v, want the missing topology row added", after)
	}
	for key, line := range before {
		if after[key] != line {
			t.Errorf("row %s changed: %q, was %q", key, after[key], line)
		}
	}
	if svc.IsEnabled(f.OrgCtx(f.OrgA), f.OrgA, entitlement.FeatureExportCSV) {
		t.Error("deactivated export_csv active after the start")
	}
	if svc.IsEnabled(f.OrgCtx(f.OrgA), f.OrgA, entitlement.FeatureDiscovery) {
		t.Error("expired discovery active after the start")
	}

	// A further start changes nothing.
	settled := rows(f.OrgA)
	if n := start(); n != 0 {
		t.Errorf("repeated start provisioned %d organizations, want 0", n)
	}
	if again := rows(f.OrgA); !maps.Equal(again, settled) {
		t.Errorf("repeated start changed rows: %v, was %v", again, settled)
	}

	// Organization B is untouched.
	if got := rows(f.OrgB); !maps.Equal(got, rowsB) {
		t.Errorf("rows of organization B: %v, was %v", got, rowsB)
	}
	if got := call(t, routes, f.OrgB, http.MethodGet, "/api/v1/topology"); got.status != http.StatusForbidden {
		t.Errorf("org B topology: %+v, want 403", got)
	}
}

func hasEnabled(list []entitlement.Entitlement, feature string) bool {
	for _, e := range list {
		if e.FeatureKey == feature {
			return e.Enabled
		}
	}
	return false
}
