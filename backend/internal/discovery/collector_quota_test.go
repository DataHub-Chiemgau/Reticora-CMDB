package discovery_test

// WP-472 (ENT-03, ENT-08), finding F-072: max_collectors is enforced on
// both ways a collector comes into being, POST /api/v1/collectors and the
// enrollment POST /api/v1/collectors/enroll. Over the limit the answer is
// 403 entitlement-limit and nothing is registered; a refused enrollment
// leaves the code valid. Without a limit the number of collectors is not
// capped.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestCollectorQuota' ./internal/discovery/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const quotaOrg = "4720c0de-0000-4000-8000-000000000001"

// quotaEnv is the discovery handler with max_collectors from the
// entitlement service (enforcement on, as in production).
type quotaEnv struct {
	t    *testing.T
	svc  *entitlement.Service
	repo discovery.Repository
	mux  *chi.Mux
	org  string
	// ctx is the context of an authenticated request of org, admin the
	// org-wide context of the grants.
	ctx, admin context.Context
}

func newQuotaEnv(t *testing.T, svc *entitlement.Service, repo discovery.Repository, org string, ctx, admin context.Context) *quotaEnv {
	t.Helper()
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).WithLimits(svc).RegisterRoutes(mux)
	return &quotaEnv{t: t, svc: svc, repo: repo, mux: mux, org: org, ctx: ctx, admin: admin}
}

// grant stores the discovery feature with max_collectors (no quota when
// limit is negative).
func (e *quotaEnv) grant(limit int64) {
	e.t.Helper()
	limits := map[string]int64{}
	if limit >= 0 {
		limits[entitlement.LimitMaxCollectors] = limit
	}
	if _, err := e.svc.Grant(e.admin, entitlement.Entitlement{OrganizationID: e.org, FeatureKey: entitlement.FeatureDiscovery,
		Plan: entitlement.PlanEssential, Enabled: true, Source: "manual", Limits: limits}); err != nil {
		e.t.Fatal(err)
	}
}

// call sends a request, authenticated as the organization or, for the
// enrollment, without principal.
func (e *quotaEnv) call(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := context.Background()
	if authenticated {
		ctx = tenant.WithTenant(e.ctx, tenant.TenantInfo{OrganizationID: e.org, UserID: "4720c0de-0000-4000-8000-0000000000f1"})
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func (e *quotaEnv) register(name, clientID string) *httptest.ResponseRecorder {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "client_id": clientID})
	return e.call(http.MethodPost, "/api/v1/collectors", string(body), true)
}

func (e *quotaEnv) enrollmentCode() string {
	e.t.Helper()
	w := e.call(http.MethodPost, "/api/v1/collectors/enrollment-codes", `{"label":"quota"}`, true)
	var created struct {
		Code string `json:"code"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Code == "" {
		e.t.Fatalf("enrollment code: %d %s", w.Code, w.Body.String())
	}
	return created.Code
}

func (e *quotaEnv) enroll(code, name string) *httptest.ResponseRecorder {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"code": code, "name": name})
	return e.call(http.MethodPost, "/api/v1/collectors/enroll", string(body), false)
}

func (e *quotaEnv) count() int64 {
	e.t.Helper()
	n, err := e.repo.CountCollectors(e.ctx, e.org)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func isLimit(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), api.ProblemEntitlementLimit)
}

// runCollectorQuota is the scenario of both repositories: org holds base
// collectors already.
func runCollectorQuota(t *testing.T, e *quotaEnv, clientID string) {
	base := e.count()

	// max_collectors = stock + 1: the first new collector is created, the
	// second is refused.
	e.grant(base + 1)
	if w := e.register("quota-first", clientID); w.Code != http.StatusCreated {
		t.Fatalf("first collector within max_collectors: %d %s, want 201", w.Code, w.Body.String())
	}
	if w := e.register("quota-second", clientID); !isLimit(w) {
		t.Errorf("second collector over max_collectors: %d %s, want 403 entitlement-limit", w.Code, w.Body.String())
	}
	if got := e.count(); got != base+1 {
		t.Errorf("collectors after the refused registration: %d, want %d", got, base+1)
	}

	// Enrollment at the limit: 403, nothing registered, the code stays
	// valid and enrolls once the limit is raised.
	code := e.enrollmentCode()
	if w := e.enroll(code, "quota-enrolled"); !isLimit(w) {
		t.Errorf("enrollment over max_collectors: %d %s, want 403 entitlement-limit", w.Code, w.Body.String())
	}
	if got := e.count(); got != base+1 {
		t.Errorf("collectors after the refused enrollment: %d, want %d", got, base+1)
	}
	e.grant(base + 2)
	if w := e.enroll(code, "quota-enrolled"); w.Code != http.StatusCreated {
		t.Fatalf("enrollment with the kept code after raising the limit: %d %s, want 201", w.Code, w.Body.String())
	}
	if w := e.enroll(code, "quota-enrolled-again"); w.Code != http.StatusUnauthorized {
		t.Errorf("second use of the consumed code: %d, want 401", w.Code)
	}

	// Without a limit the number is not capped.
	e.grant(-1)
	for _, name := range []string{"quota-free-1", "quota-free-2", "quota-free-3"} {
		if w := e.register(name, clientID); w.Code != http.StatusCreated {
			t.Errorf("collector %s without max_collectors: %d %s, want 201", name, w.Code, w.Body.String())
		}
	}
	if w := e.enroll(e.enrollmentCode(), "quota-free-enrolled"); w.Code != http.StatusCreated {
		t.Errorf("enrollment without max_collectors: %d %s, want 201", w.Code, w.Body.String())
	}
	if got := e.count(); got != base+6 {
		t.Errorf("collectors without max_collectors: %d, want %d", got, base+6)
	}
}

// TestCollectorQuota covers the handler with the in-memory repository.
func TestCollectorQuota(t *testing.T) {
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	runCollectorQuota(t, newQuotaEnv(t, svc, discovery.NewMemoryRepository(), quotaOrg, context.Background(), context.Background()), "")
}

// TestCollectorQuotaPostgres covers the PostgreSQL repository with a
// client-scoped caller: max_collectors counts the organization's
// collectors, including those of another client the caller does not see,
// and the enrollment checks it in its transaction before the code is
// consumed.
func TestCollectorQuotaPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e1")
	bg := context.Background()
	// A collector of client 2, invisible to the client-1 caller, and a
	// disabled one, which does not count.
	for _, row := range []struct{ client, status string }{{f.Client2, "active"}, {f.Client1, "disabled"}} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO collector (organization_id, client_id, name, status, config) VALUES ($1, $2, $3, $4, '{}')`,
			f.OrgA, row.client, "quota-seed-"+row.status, row.status); err != nil {
			t.Fatalf("seed collector: %v", err)
		}
	}
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	repo := discovery.NewPGRepository(f.App)
	// The requests run as a client-1 caller, the grants org-wide.
	e := newQuotaEnv(t, svc, repo, f.OrgA, f.ClientCtx(f.Client1), f.OrgCtx(f.OrgA))
	if got := e.count(); got != 1 {
		t.Fatalf("org-wide count for a client-1 caller: %d, want 1 (client 2's collector, not the disabled one)", got)
	}
	grant := e.grant
	grant(1)
	if w := e.register("quota-client1", f.Client1); !isLimit(w) {
		t.Errorf("client-1 collector at the org-wide limit: %d %s, want 403 entitlement-limit", w.Code, w.Body.String())
	}
	var codeUsed bool
	code := e.enrollmentCode()
	if w := e.enroll(code, "quota-enrolled"); !isLimit(w) {
		t.Errorf("enrollment at the org-wide limit: %d %s, want 403 entitlement-limit", w.Code, w.Body.String())
	}
	if err := f.Admin.QueryRow(bg, `SELECT used_at IS NOT NULL FROM collector_enrollment_code WHERE organization_id = $1 AND label = 'quota'`,
		f.OrgA).Scan(&codeUsed); err != nil || codeUsed {
		t.Errorf("enrollment code after the refusal: used %v, %v; want unused", codeUsed, err)
	}
	grant(2)
	if w := e.enroll(code, "quota-enrolled"); w.Code != http.StatusCreated {
		t.Errorf("enrollment with the kept code after raising the limit: %d %s, want 201", w.Code, w.Body.String())
	}
	if w := e.register("quota-client1", f.Client1); !isLimit(w) {
		t.Errorf("client-1 collector at the raised limit: %d %s, want 403 entitlement-limit", w.Code, w.Body.String())
	}
	grant(-1)
	if w := e.register("quota-client1", f.Client1); w.Code != http.StatusCreated {
		t.Errorf("client-1 collector without max_collectors: %d %s, want 201", w.Code, w.Body.String())
	}
}
