package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api/speccheck"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// TestG1DefaultSwitchesLateModulesOff covers API-06 and RBA-05 (WP-214,
// E-03): without RETICORA_MODULES every module with a phase tag from P2 or
// [A] is off; its routes are not registered and its workers do not start.
func TestG1DefaultSwitchesLateModulesOff(t *testing.T) {
	g1, err := ParseModules("")
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}
	if keys := g1.EnabledKeys(); len(keys) != 0 {
		t.Fatalf("G1 default enables %v, want none", keys)
	}
	for _, m := range LateModules {
		if !slices.Contains([]string{"P2", "P3", "P4", "A"}, m.Phase) {
			t.Errorf("module %s: phase %q is not a late phase", m.Key, m.Phase)
		}
		if len(m.Prefixes) == 0 || len(m.Nav) == 0 {
			t.Errorf("module %s: needs route prefixes and navigation pages", m.Key)
		}
	}

	mux := testRouterWithModules(t, &audit.Handler{}, g1)
	for _, path := range []string{"/api/v1/tickets", "/api/v1/assets", "/api/v1/workflows", "/api/v1/monitoring/alerts",
		"/api/v1/agents", "/api/v1/iga/connectors", "/scim/v2/Users", "/api/v1/ai/conversations"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the G1 default = %d, want 404", path, rec.Code)
		}
	}
	// The G1 surface stays: CIs and the metrics of MON-02 are served (401
	// without a principal, not 404).
	for _, path := range []string{"/api/v1/cis", "/api/v1/monitoring/metrics"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("GET %s with the G1 default = 404, want it served", path)
		}
	}

	if g1.WorkerAllowed(config.WorkerMonitoring) {
		t.Error("the alert evaluator (MON-04, P3) starts with the G1 default")
	}
	for _, name := range config.WorkerNames {
		if name != config.WorkerMonitoring && !g1.WorkerAllowed(name) {
			t.Errorf("G1 worker %s is switched off by a module", name)
		}
	}
}

// TestModulesSwitchOnPerKey covers the gate switch of each module.
func TestModulesSwitchOnPerKey(t *testing.T) {
	all, err := ParseModules("all")
	if err != nil {
		t.Fatalf("parse all: %v", err)
	}
	for _, m := range LateModules {
		if !all.Enabled(m.Key) {
			t.Errorf("all: module %s is off", m.Key)
		}
	}
	if !all.WorkerAllowed(config.WorkerMonitoring) {
		t.Error("all: the alert evaluator does not start")
	}

	some, err := ParseModules(" ticketing , iga ")
	if err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if got := some.EnabledKeys(); !slices.Equal(got, []string{"iga", "ticketing"}) {
		t.Fatalf("enabled = %v, want [iga ticketing]", got)
	}
	if !some.RouteAllowed("/api/v1/tickets/{id}") || some.RouteAllowed("/api/v1/assets") ||
		!some.RouteAllowed("/scim/v2/Users") || !some.RouteAllowed("/api/v1/cis") {
		t.Error("route gate does not follow the enabled modules")
	}
	// A prefix matches whole segments only.
	if m, ok := ModuleForRoute("/api/v1/aircraft"); ok {
		t.Errorf("/api/v1/aircraft is attributed to module %s", m.Key)
	}

	mux := testRouterWithModules(t, &audit.Handler{}, some)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("GET /api/v1/tickets with ticketing on = 404")
	}

	if _, err := ParseModules("ticketing,tickting"); err == nil {
		t.Error("an unknown module key is accepted")
	}
}

// TestAccessRequestRunsFollowWorkflowsModule covers WP-603 (WFL-04, E-03):
// an IGA access request starts its workflow run only while the workflows
// module is on; with iga alone no workflow action runs.
func TestAccessRequestRunsFollowWorkflowsModule(t *testing.T) {
	runs := workflow.NewExecutor(workflow.NewMemoryRepository(), nil, nil, nil, nil)
	igaOnly, err := ParseModules("iga")
	if err != nil {
		t.Fatal(err)
	}
	both, err := ParseModules("iga,workflows")
	if err != nil {
		t.Fatal(err)
	}
	if accessRequestRuns(igaOnly, runs) != nil {
		t.Error("access requests start workflow runs with the workflows module off")
	}
	if accessRequestRuns(both, runs) == nil || accessRequestRuns(nil, runs) == nil {
		t.Error("access requests start no workflow run with the workflows module on")
	}
}

// TestLateRoutesAreMarkedInSpecification covers the parity rule of WP-214:
// every documented operation of a late module carries x-phase with the
// module's phase, and no other path does.
func TestLateRoutesAreMarkedInSpecification(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	phases, err := speccheck.Phases(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load phases: %v", err)
	}
	for _, op := range documented {
		m, late := ModuleForRoute(op.Path)
		phase, marked := phases[op.Path]
		switch {
		case late && !marked:
			t.Errorf("%s belongs to module %s (%s) but has no x-phase", op, m.Key, m.Phase)
		case late && phase != m.Phase:
			t.Errorf("%s: x-phase %s, module %s has %s", op, phase, m.Key, m.Phase)
		case !late && marked:
			t.Errorf("%s has x-phase %s but belongs to no late module", op, phase)
		}
	}
}

// TestAddonsAreNotImpliedByPlan covers the add-on rule of API-06 and RBA-05:
// IGA and AI are add-ons (WP-075) and no plan includes them.
func TestAddonsAreNotImpliedByPlan(t *testing.T) {
	for _, plan := range []entitlement.Plan{entitlement.PlanEssential, entitlement.PlanStandard,
		entitlement.PlanPro, entitlement.PlanEnterprise} {
		features := entitlement.PlanFeatures(plan)
		for _, addon := range []string{entitlement.FeatureIGA, entitlement.FeatureAI} {
			if slices.Contains(features, addon) {
				t.Errorf("plan %s includes the add-on %s", plan, addon)
			}
		}
	}
	for _, key := range []string{"iga", "ai"} {
		if m, ok := moduleByKey(key); !ok || m.Phase != "A" {
			t.Errorf("module %s is not an add-on module", key)
		}
	}
}

// TestModulesEndpointListsTheReleaseModules covers GET /api/v1/modules, which
// the UI reads to hide the navigation of switched-off modules.
func TestModulesEndpointListsTheReleaseModules(t *testing.T) {
	some, err := ParseModules("documents")
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}
	rec := httptest.NewRecorder()
	modulesHandler{set: some}.list(rec, httptest.NewRequest(http.MethodGet, "/api/v1/modules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data []struct {
			Key     string   `json:"key"`
			Phase   string   `json:"phase"`
			Nav     []string `json:"nav"`
			Enabled bool     `json:"enabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != len(LateModules) {
		t.Fatalf("got %d modules, want %d", len(body.Data), len(LateModules))
	}
	for _, m := range body.Data {
		if m.Enabled != (m.Key == "documents") {
			t.Errorf("module %s enabled = %v", m.Key, m.Enabled)
		}
		if m.Key == "documents" && !slices.Equal(m.Nav, []string{"documents"}) {
			t.Errorf("documents nav = %v", m.Nav)
		}
	}
}
