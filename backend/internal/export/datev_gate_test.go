package export

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// The organizations of the gate tests: the plan standard includes
// export_datev and assets_basic, essential only assets_basic; noassets
// holds standard with assets_basic switched off.
const (
	orgStandard  = "org-standard"
	orgEssential = "org-essential"
	orgNoAssets  = "org-noassets"
)

// gateEntitlements is an enforcing entitlement service with one plan per
// organization (ENT-05: plans are isolated per tenant).
func gateEntitlements(t *testing.T) *entitlement.Service {
	t.Helper()
	ctx := context.Background()
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	for org, plan := range map[string]entitlement.Plan{
		orgStandard: entitlement.PlanStandard, orgEssential: entitlement.PlanEssential, orgNoAssets: entitlement.PlanStandard,
	} {
		if _, err := svc.Provision(ctx, org, plan); err != nil {
			t.Fatalf("provision %s: %v", org, err)
		}
	}
	revoke(t, svc, orgNoAssets, entitlement.FeatureAssetsBasic)
	return svc
}

// revoke switches a feature of an organization off.
func revoke(t *testing.T, svc *entitlement.Service, org, feature string) {
	t.Helper()
	if _, err := svc.Grant(context.Background(), &entitlement.Entitlement{OrganizationID: org, FeatureKey: feature,
		Plan: entitlement.PlanStandard, Enabled: false, Source: "manual"}); err != nil {
		t.Fatalf("revoke %s for %s: %v", feature, org, err)
	}
}

// gateRequest is a request of org with a principal holding perms.
func gateRequest(method, target, body, org string, perms ...identity.Permission) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: org, UserID: "user-1"})
	scope := database.OrgWideScope(org, "user-1")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", Permissions: perms})
	return r.WithContext(ctx)
}

// gateMux serves the streaming and the job export behind gate.
func gateMux(t *testing.T, gate *DATEVGate) (chi.Router, *JobWorker, *MemoryJobRepository) {
	t.Helper()
	cis := ci.NewMemoryRepository()
	for _, org := range []string{orgStandard, orgEssential, orgNoAssets} {
		seedCIs(t, cis, org, 2)
	}
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, cis, blobs)
	worker.DATEV = gate
	mux := chi.NewRouter()
	NewHandler(cis).WithDATEVGate(gate).RegisterRoutes(mux)
	NewJobHandler(jobs, worker, blobs).WithDATEVGate(gate).RegisterRoutes(mux)
	return mux, worker, jobs
}

// exportPath is one of the three paths that take the format datev.
type exportPath struct {
	name   string
	method string
	target func(format string) string
	body   func(format string) string
	ok     int
}

var exportPaths = []exportPath{
	{"POST /api/v1/exports", http.MethodPost, func(string) string { return "/api/v1/exports" },
		func(f string) string { return `{"format":"` + f + `"}` }, http.StatusAccepted},
	{"POST /api/v1/export/jobs", http.MethodPost, func(string) string { return "/api/v1/export/jobs" },
		func(f string) string { return `{"format":"` + f + `"}` }, http.StatusAccepted},
	{"GET /api/v1/export/cis", http.MethodGet, func(f string) string { return "/api/v1/export/cis?format=" + f },
		func(string) string { return "" }, http.StatusOK},
}

var fullPerms = []identity.Permission{identity.PermExportRun, identity.PermCIRead, identity.PermAssetRead}

func serve(mux http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func problemOf(t *testing.T, w *httptest.ResponseRecorder) api.ProblemDetail {
	t.Helper()
	var p api.ProblemDetail
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", w.Body.String(), err)
	}
	return p
}

// TestDATEVGateMatrix: on every path DATEV needs the module inventory
// (else 400), export_datev and assets_basic (else 403
// feature-not-entitled) and asset:read (else 403), per organization
// (ENT-02, ENT-05, WP-790).
func TestDATEVGateMatrix(t *testing.T) {
	svc := gateEntitlements(t)
	open, _, _ := gateMux(t, NewDATEVGate(svc, true))
	moduleOff, _, _ := gateMux(t, NewDATEVGate(svc, false))

	cases := []struct {
		name        string
		mux         http.Handler
		org         string
		perms       []identity.Permission
		status      int
		problemType string
		detail      string
	}{
		{"module inventory off", moduleOff, orgStandard, fullPerms, http.StatusBadRequest, "", "inventory"},
		{"without export_datev", open, orgEssential, fullPerms, http.StatusForbidden, api.ProblemFeatureNotEntitled, "export_datev"},
		{"without assets_basic", open, orgNoAssets, fullPerms, http.StatusForbidden, api.ProblemFeatureNotEntitled, "assets_basic"},
		{"without asset:read", open, orgStandard, []identity.Permission{identity.PermExportRun, identity.PermCIRead},
			http.StatusForbidden, "https://reticora.io/problems/403", "asset:read"},
		{"everything granted", open, orgStandard, fullPerms, 0, "", ""},
	}
	for _, p := range exportPaths {
		for _, c := range cases {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				w := serve(c.mux, gateRequest(p.method, p.target(FormatDATEV), p.body(FormatDATEV), c.org, c.perms...))
				want := c.status
				if want == 0 {
					want = p.ok
				}
				if w.Code != want {
					t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
				}
				if c.status == 0 {
					return
				}
				problem := problemOf(t, w)
				if c.problemType != "" && problem.Type != c.problemType {
					t.Errorf("type %q, want %q", problem.Type, c.problemType)
				}
				if !strings.Contains(problem.Detail, c.detail) {
					t.Errorf("detail %q does not name %q", problem.Detail, c.detail)
				}
			})
		}
	}
}

// TestDATEVGateMissingRefuses: without a gate DATEV is refused (fail
// closed); CSV and JSON are not touched.
func TestDATEVGateMissingRefuses(t *testing.T) {
	mux, _, _ := gateMux(t, nil)
	for _, p := range exportPaths {
		t.Run(p.name, func(t *testing.T) {
			w := serve(mux, gateRequest(p.method, p.target(FormatDATEV), p.body(FormatDATEV), orgStandard, fullPerms...))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
			}
			if got := problemOf(t, w).Type; got != api.ProblemFeatureNotEntitled {
				t.Errorf("type %q, want feature-not-entitled", got)
			}
			for _, format := range []string{"csv", "json"} {
				if w := serve(mux, gateRequest(p.method, p.target(format), p.body(format), orgStandard)); w.Code != p.ok {
					t.Errorf("%s without gate: %d, want %d", format, w.Code, p.ok)
				}
			}
		})
	}
}

// TestDATEVGateLeavesCSVAndJSON: CSV and JSON need neither export_datev
// nor assets_basic nor asset:read; the path gate export_csv stays the only
// gate.
func TestDATEVGateLeavesCSVAndJSON(t *testing.T) {
	svc := gateEntitlements(t)
	mux, _, _ := gateMux(t, NewDATEVGate(svc, false))
	for _, p := range exportPaths {
		for _, format := range []string{"csv", "json"} {
			w := serve(mux, gateRequest(p.method, p.target(format), p.body(format), orgEssential, identity.PermExportRun, identity.PermCIRead))
			if w.Code != p.ok {
				t.Errorf("%s %s: %d, want %d: %s", p.name, format, w.Code, p.ok, w.Body.String())
			}
		}
	}
}

// TestDATEVLegacyPathKeepsCILayout: GET /api/v1/export/cis?format=datev
// keeps the CI layout (UTF-8 with BOM) until its sunset, behind the gate
// (API-01, E-141).
func TestDATEVLegacyPathKeepsCILayout(t *testing.T) {
	mux, _, _ := gateMux(t, NewDATEVGate(gateEntitlements(t), true))
	w := serve(mux, gateRequest(http.MethodGet, "/api/v1/export/cis?format=datev", "", orgStandard, fullPerms...))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, utf8BOM+"Inventarnummer;Bezeichnung;Hersteller;Modell;Seriennummer;Anschaffungsdatum;Standort;Kostenstelle;Status") {
		t.Fatalf("not the CI layout with BOM: %q", body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("content type %q", ct)
	}
}

// TestDATEVJobFailsAfterRevoke: an entitlement revoked after the job was
// queued fails the job with the feature key in its message.
func TestDATEVJobFailsAfterRevoke(t *testing.T) {
	svc := gateEntitlements(t)
	mux, worker, jobs := gateMux(t, NewDATEVGate(svc, true))
	w := serve(mux, gateRequest(http.MethodPost, "/api/v1/exports", `{"format":"datev"}`, orgStandard, fullPerms...))
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	revoke(t, svc, orgStandard, entitlement.FeatureExportDATEV)
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}
	job, err := jobs.GetJob(context.Background(), orgStandard, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusFailed || !strings.Contains(job.ErrorMessage, entitlement.FeatureExportDATEV) {
		t.Fatalf("job %s %q, want failed naming export_datev", job.Status, job.ErrorMessage)
	}
}

// TestDATEVJobFailsWithModuleOff: a worker whose module inventory is off
// fails a DATEV job and names the module.
func TestDATEVJobFailsWithModuleOff(t *testing.T) {
	svc := gateEntitlements(t)
	mux, _, jobs := gateMux(t, NewDATEVGate(svc, true))
	w := serve(mux, gateRequest(http.MethodPost, "/api/v1/exports", `{"format":"datev"}`, orgStandard, fullPerms...))
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	worker := NewJobWorker(jobs, ci.NewMemoryRepository(), blob.NewFileStore(t.TempDir()))
	worker.DATEV = NewDATEVGate(svc, false)
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}
	job, err := jobs.GetJob(context.Background(), orgStandard, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusFailed || !strings.Contains(job.ErrorMessage, "inventory") {
		t.Fatalf("job %s %q, want failed naming the module inventory", job.Status, job.ErrorMessage)
	}
}

// scopedChecker grants every feature, but only in a context that carries a
// tenant scope, as entitlement.PGRepository.List reads through
// database.WithRequestTenant.
type scopedChecker struct{ calls, scoped int }

func (c *scopedChecker) IsEnabled(ctx context.Context, orgID, _ string) bool {
	c.calls++
	scope, ok := database.TenantScopeFromContext(ctx)
	if ok && scope.OrgID == orgID {
		c.scoped++
		return true
	}
	return false
}

// TestDATEVWorkerChecksInJobScope: the worker checks the entitlements in
// the scope of the job.
func TestDATEVWorkerChecksInJobScope(t *testing.T) {
	checker := &scopedChecker{}
	mux, worker, jobs := gateMux(t, NewDATEVGate(checker, true))
	w := serve(mux, gateRequest(http.MethodPost, "/api/v1/export/jobs", `{"format":"datev"}`, orgStandard, fullPerms...))
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	before := checker.calls
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}
	if checker.calls == before {
		t.Fatal("the worker did not check the entitlements")
	}
	job, err := jobs.GetJob(context.Background(), orgStandard, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusCompleted {
		t.Fatalf("job %s %q, want completed with the scope of the job", job.Status, job.ErrorMessage)
	}
}
