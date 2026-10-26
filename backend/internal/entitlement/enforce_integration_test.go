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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
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
	mux.Get("/api/v1/racks", ok)
	mux.Get("/api/v1/racks/{id}/layout", ok)
	mux.Get("/api/v1/mounts", ok)
	mux.Patch("/api/v1/rack-mounts/{id}", ok)
	mux.Get("/api/v1/api-keys", ok)
	mux.Post("/api/v1/api-keys", ok)
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
	return callAs(t, h, orgID, method, path, identity.PrincipalTypeUser)
}

// callAs sends the request as a session (PrincipalTypeUser) or as an API
// key (PrincipalTypeAPIKey) of the organization.
func callAs(t *testing.T, h http.Handler, orgID, method, path string, typ identity.PrincipalType) response {
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
		Permissions: []identity.Permission{identity.PermCIRead}, Type: typ})
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
	{http.MethodGet, "/api/v1/racks"},
	{http.MethodGet, "/api/v1/racks/r1/layout"},
	{http.MethodGet, "/api/v1/mounts"},
	{http.MethodPatch, "/api/v1/rack-mounts/m1"},
	{http.MethodGet, "/api/v1/api-keys"},
	{http.MethodPost, "/api/v1/api-keys"},
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
	// WP-471: without the provisioning the new gates would take racks, API
	// keys and e-mail from an organization that held only cmdb_core.
	if got := call(t, routes, f.OrgA, http.MethodGet, "/api/v1/racks"); got.status != http.StatusForbidden {
		t.Fatalf("racks before provisioning: %+v, want 403", got)
	}
	if got := callAs(t, routes, f.OrgA, http.MethodGet, "/api/v1/cis", identity.PrincipalTypeAPIKey); got.status != http.StatusForbidden {
		t.Fatalf("API key before provisioning: %+v, want 403", got)
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
	// WP-471: racks (above), API keys and e-mail stay available.
	if got := callAs(t, routes, f.OrgA, http.MethodGet, "/api/v1/cis", identity.PrincipalTypeAPIKey); got.status != http.StatusNoContent {
		t.Errorf("org A API key request after the start: %+v, want 204", got)
	}
	if mail := newMailNotifier(t, f.OrgA, svc); !mail.deliver(t) {
		t.Errorf("org A e-mail after the start: not sent, want sent (notifications_email provisioned)")
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

// mailProvider records the e-mails it would send.
type mailProvider struct{ sent int }

func (m *mailProvider) Send(_ context.Context, _ *notify.Channel, _ *notify.Message) (*notify.Delivery, error) {
	m.sent++
	return &notify.Delivery{Recipients: 1}, nil
}

// mailNotifier is the notifier of the worker with one central e-mail
// channel and one in-app channel of an organization; the entitlements are
// read from PostgreSQL as reticora_app in the delivery's scope.
type mailNotifier struct {
	n      *notify.Notifier
	store  *notify.MemoryStore
	inbox  *notify.MemoryInbox
	mail   *mailProvider
	orgID  string
	notice int
}

func newMailNotifier(t *testing.T, orgID string, features notify.FeatureChecker) *mailNotifier {
	t.Helper()
	repo := notify.NewMemoryRepository()
	for _, ch := range []*notify.Channel{
		{OrganizationID: orgID, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: orgID, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := repo.Create(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	m := &mailNotifier{store: notify.NewMemoryStore(), inbox: notify.NewMemoryInbox(), mail: &mailProvider{}, orgID: orgID}
	m.n = notify.NewNotifier(repo, m.store, m.store, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: m.mail, notify.ProviderInApp: notify.NewInAppProvider(m.inbox)})).WithEntitlements(features)
	return m
}

// deliver queues one notification for a user with an address, works it off
// and reports whether the e-mail was sent. The in-app delivery is always
// sent; a skipped e-mail carries the reason feature-not-entitled.
func (m *mailNotifier) deliver(t *testing.T) bool {
	t.Helper()
	bg := context.Background()
	m.notice++
	before, inbox := m.mail.sent, len(m.inbox.For(m.orgID, "user-1"))
	if _, err := m.n.Notify(bg, m.orgID, &notify.Request{Category: "export", Subject: "Export fertig",
		DedupKey:   "export:" + time.Now().Format(time.RFC3339Nano),
		Recipients: []notify.Recipient{{UserID: "user-1", Email: "ops@example.org"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.n.ProcessDue(bg); err != nil {
		t.Fatalf("process deliveries: %v", err)
	}
	if got := len(m.inbox.For(m.orgID, "user-1")); got != inbox+1 {
		t.Errorf("in-app delivery %d: %d notices, want %d", m.notice, got, inbox+1)
	}
	sent := m.mail.sent > before
	if !sent {
		rows, _ := m.store.List(bg, m.orgID, 2, 0)
		skipped := false
		for _, r := range rows {
			skipped = skipped || (r.Status == notify.StatusSuppressed && r.LastError == notify.ReasonFeatureNotEntitled)
		}
		if !skipped {
			t.Errorf("e-mail %d neither sent nor skipped as feature-not-entitled: %+v", m.notice, rows)
		}
	}
	return sent
}

// TestEntitlementGatesRackAPIAccessEmail covers WP-471 (ENT-05, ENT-03,
// API-04, F-070) against PostgreSQL (reticora_app) and Redis with two
// service replicas sharing the cache, for rack_view, api_access and
// notifications_email:
//   - provisioned: racks, mounts, API keys, an X-API-Key request and the
//     e-mail work;
//   - a grant disabling the feature on replica A refuses it on replica B at
//     once (403 feature-not-entitled; the e-mail is skipped, in-app stays);
//     a session request without api_access stays 200;
//   - enabled again, everything works;
//   - a change that bypasses the service is seen at the latest when the
//     cache entry expires: the entry lives at most 60 s, and a replica with
//     a short lifetime sees the change after it.
//
// It runs only with TEST_DATABASE_URL and TEST_REDIS_URL.
func TestEntitlementGatesRackAPIAccessEmail(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" || os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL required; skipping entitlement gate test")
	}
	f := scopetest.Seed(t, "64")
	bg := context.Background()
	client, err := redisx.Connect(bg, redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisStore(client.Unwrap())
	key := "entitlements:" + f.OrgA
	_ = store.Delete(bg, key)
	t.Cleanup(func() { _ = store.Delete(bg, key) })

	repo := entitlement.NewPGRepository(f.App)
	replicaA := entitlement.NewService(repo, entitlement.Options{Cache: store, Enforce: true})
	replicaB := entitlement.NewService(repo, entitlement.Options{Cache: store, Enforce: true})
	if n, provErr := replicaA.ProvisionOrganizations(bg, map[string]entitlement.Plan{f.OrgA: entitlement.PlanEssential}); n != 1 || provErr != nil {
		t.Fatalf("provision: %d, %v", n, provErr)
	}
	routesB := enforcedRoutes(replicaB)
	mail := newMailNotifier(t, f.OrgA, replicaB)

	type probe struct {
		method, path string
		typ          identity.PrincipalType
	}
	gated := map[string][]probe{
		entitlement.FeatureRackView: {
			{http.MethodGet, "/api/v1/racks", identity.PrincipalTypeUser},
			{http.MethodGet, "/api/v1/racks/r1/layout", identity.PrincipalTypeUser},
			{http.MethodGet, "/api/v1/mounts", identity.PrincipalTypeUser},
			{http.MethodPatch, "/api/v1/rack-mounts/m1", identity.PrincipalTypeUser},
		},
		entitlement.FeatureAPIAccess: {
			{http.MethodGet, "/api/v1/api-keys", identity.PrincipalTypeUser},
			{http.MethodPost, "/api/v1/api-keys", identity.PrincipalTypeUser},
			{http.MethodGet, "/api/v1/cis", identity.PrincipalTypeAPIKey},
			{http.MethodGet, "/api/v1/exports", identity.PrincipalTypeAPIKey},
		},
	}
	// allowed reports whether the feature's requests (or its e-mail) pass.
	allowed := func(feature string, want bool) {
		t.Helper()
		if feature == entitlement.FeatureNotificationsEmail {
			if got := mail.deliver(t); got != want {
				t.Errorf("e-mail sent %t, want %t", got, want)
			}
			return
		}
		for _, p := range gated[feature] {
			got := callAs(t, routesB, f.OrgA, p.method, p.path, p.typ)
			switch {
			case want && got.status != http.StatusNoContent:
				t.Errorf("%s: %s %s (%s): %+v, want 204", feature, p.method, p.path, p.typ, got)
			case !want && (got.status != http.StatusForbidden || got.problem != api.ProblemFeatureNotEntitled):
				t.Errorf("%s disabled: %s %s (%s): %+v, want 403 feature-not-entitled", feature, p.method, p.path, p.typ, got)
			}
		}
	}
	grant := func(feature string, enabled bool) {
		t.Helper()
		if _, grantErr := replicaA.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: feature,
			Plan: entitlement.PlanEssential, Enabled: enabled, Source: "manual"}); grantErr != nil {
			t.Fatal(grantErr)
		}
	}

	features := []string{entitlement.FeatureRackView, entitlement.FeatureAPIAccess, entitlement.FeatureNotificationsEmail}
	for _, feature := range features {
		allowed(feature, true)
	}
	for _, feature := range features {
		// Replica B has the entitlements cached; the grant on replica A
		// invalidates them at once.
		if _, ok, _ := store.Get(bg, key); !ok {
			t.Fatalf("%s: entitlements of org A not cached in Redis", feature)
		}
		grant(feature, false)
		allowed(feature, false)
		for _, other := range features {
			if other != feature {
				allowed(other, true)
			}
		}
		if feature == entitlement.FeatureAPIAccess {
			if got := call(t, routesB, f.OrgA, http.MethodGet, "/api/v1/cis"); got.status != http.StatusNoContent {
				t.Errorf("session request without api_access: %+v, want 204", got)
			}
		}
		grant(feature, true)
		allowed(feature, true)
	}

	// A change that bypasses the service: the cached state holds until the
	// entry expires, at most 60 s after it was cached (ENT-03).
	if ttl, ttlErr := client.Unwrap().TTL(bg, key).Result(); ttlErr != nil || ttl <= 0 || ttl > entitlement.DefaultCacheTTL {
		t.Errorf("cache lifetime of the entitlements: %s (%v), want at most %s", ttl, ttlErr, entitlement.DefaultCacheTTL)
	}
	_ = store.Delete(bg, key)
	short := entitlement.NewService(repo, entitlement.Options{Cache: store, Enforce: true, CacheTTL: time.Second})
	routesShort := enforcedRoutes(short)
	if got := call(t, routesShort, f.OrgA, http.MethodGet, "/api/v1/racks"); got.status != http.StatusNoContent {
		t.Fatalf("racks before the direct change: %+v", got)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE entitlement SET enabled = false WHERE organization_id = $1
		AND feature_key IN ('rack_view', 'api_access', 'notifications_email')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got := call(t, routesShort, f.OrgA, http.MethodGet, "/api/v1/racks"); got.status != http.StatusNoContent {
		t.Errorf("racks within the cache lifetime: %+v, want the cached state", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := call(t, routesShort, f.OrgA, http.MethodGet, "/api/v1/racks")
		if got.status == http.StatusForbidden {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("racks after the cache lifetime: %+v, want 403", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := callAs(t, routesShort, f.OrgA, http.MethodGet, "/api/v1/cis", identity.PrincipalTypeAPIKey); got.status != http.StatusForbidden {
		t.Errorf("API key after the cache lifetime: %+v, want 403", got)
	}
	if newMailNotifier(t, f.OrgA, short).deliver(t) {
		t.Error("e-mail after the cache lifetime: sent, want skipped")
	}
}
