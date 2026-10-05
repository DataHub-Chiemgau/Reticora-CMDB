package entitlement_test

import (
	"context"
	"encoding/json"
	"errors"
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
	if path == "/api/v1/graphql" {
		body = `{"query":"{ cis { totalCount } }"}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
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
	if got := call(t, routesA, f.OrgB, http.MethodPost, "/api/v1/graphql"); got.status != http.StatusOK {
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

func hasEnabled(list []entitlement.Entitlement, feature string) bool {
	for _, e := range list {
		if e.FeatureKey == feature {
			return e.Enabled
		}
	}
	return false
}
