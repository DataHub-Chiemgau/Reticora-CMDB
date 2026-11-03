package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api/speccheck"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// readmePath is the operator documentation that lists RETICORA_MODULES.
const readmePath = "../../../docs/README.md"

// modulesWithoutPages are the late modules without a navigation page: the
// GPS history (LOC-09) has no UI of its own (WP-706).
var modulesWithoutPages = []string{"gps"}

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
		// Only a module without a UI of its own (gps, WP-706) has no
		// navigation page; its Nav is empty, never nil.
		if len(m.Prefixes) == 0 || m.Nav == nil || (len(m.Nav) == 0 && !slices.Contains(modulesWithoutPages, m.Key)) {
			t.Errorf("module %s: needs route prefixes and navigation pages", m.Key)
		}
	}

	mux := testRouterWithModules(t, &audit.Handler{}, g1)
	for _, path := range []string{"/api/v1/tickets", "/api/v1/assets", "/api/v1/workflows", "/api/v1/monitoring/alerts",
		"/api/v1/agents", "/api/v1/iga/connectors", "/scim/v2/Users", "/api/v1/ai/conversations",
		"/api/v1/attribute-sets", "/api/v1/ci-types/c0de0000-0000-4000-8000-000000000001/attribute-sets",
		"/api/v1/templates", "/api/v1/templates/c0de0000-0000-4000-8000-000000000001/versions",
		"/api/v1/rules", "/api/v1/rules/c0de0000-0000-4000-8000-000000000001",
		"/api/v1/layouts", "/api/v1/layouts/c0de0000-0000-4000-8000-000000000001"} {
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
// module's phase, and no other path does. Each operation of a P2 module
// carries x-feature (path or operation level) equal to the key of the org
// gate, entitlement.RequiredFeature; an ungated operation carries none, and
// any x-feature elsewhere names the key of the org gate as well (WP-706,
// E-38, E-44).
func TestLateRoutesAreMarkedInSpecification(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	phases, err := speccheck.Phases(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load phases: %v", err)
	}
	features, err := speccheck.Features(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	p2Operations := 0
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

		gate, gated := entitlement.RequiredFeature(op.Method, op.Path)
		feature, hasFeature := features[op]
		switch {
		case late && m.Phase == PhaseP2 && gated && feature != gate:
			t.Errorf("%s (module %s): x-feature %q, the org gate requires %q", op, m.Key, feature, gate)
		case hasFeature && !gated:
			t.Errorf("%s: x-feature %q on an operation the org gate does not gate", op, feature)
		case hasFeature && feature != gate:
			t.Errorf("%s: x-feature %q, the org gate requires %q", op, feature, gate)
		}
		if late && m.Phase == PhaseP2 {
			p2Operations++
		}
	}
	if p2Operations == 0 {
		t.Fatal("no documented operation of a P2 module")
	}
}

// TestModulePhasesFollowE44 covers RBA-05 and API-06 (WP-706, E-38, E-44):
// disposal is P2 (MGT-02), the GPS history is the P4 module gps (LOC-09)
// with the longest prefix winning over inventory, import is a P2 module of
// its own (E-105), and GET /api/v1/modules lists gps with nav [], not null.
func TestModulePhasesFollowE44(t *testing.T) {
	for key, phase := range map[string]string{"disposal": "P2", "gps": "P4", "import": "P2", ModuleInventory: "P2",
		"documents": "P2", "stocktake": "P2", "ticketing": "P2", ModuleMetamodel: "P2"} {
		if m, ok := moduleByKey(key); !ok || m.Phase != phase {
			t.Errorf("module %s: %+v, want phase %s", key, m, phase)
		}
	}
	for route, want := range map[string]string{
		"/api/v1/assets/{id}/locations":                     "gps",
		"/api/v1/assets/{assetID}/locations":                "gps",
		"/api/v1/asset-locations":                           "gps",
		"/api/v1/assets/{id}":                               ModuleInventory,
		"/api/v1/assets":                                    ModuleInventory,
		"/api/v1/assets/{id}/label.svg":                     ModuleInventory,
		"/api/v1/assets/locations":                          ModuleInventory,
		"/api/v1/assets/{id}/locationsx":                    ModuleInventory,
		"/api/v1/disposal-records/{id}":                     "disposal",
		"/api/v1/imports/{id}/report":                       "import",
		"/api/v1/import-profiles":                           "import",
		"/api/v1/attribute-sets/{id}":                       ModuleMetamodel,
		"/api/v1/ci-types/{id}/attribute-sets":              ModuleMetamodel,
		"/api/v1/templates/{id}/versions/{version}/publish": ModuleMetamodel,
		"/api/v1/rules/{id}":                                ModuleMetamodel,
		"/api/v1/layouts/{id}":                              ModuleMetamodel,
	} {
		if m, ok := ModuleForRoute(route); !ok || m.Key != want {
			t.Errorf("ModuleForRoute(%s) = %s (%v), want %s", route, m.Key, ok, want)
		}
	}
	if m, ok := moduleByKey("import"); !ok || !slices.Equal(m.Nav, []string{"import"}) {
		t.Errorf("import nav = %v, want [import]", m.Nav)
	}
	for _, m := range LateModules {
		if slices.Contains(m.Nav, "ci-types") {
			t.Errorf("module %s names the G1 page ci-types", m.Key)
		}
	}

	// Release G2: inventory on, gps off; the GPS routes stay unregistered.
	g2, err := ParseModules(PhaseP2)
	if err != nil {
		t.Fatal(err)
	}
	if !g2.RouteAllowed("/api/v1/assets/{id}") || g2.RouteAllowed("/api/v1/assets/{id}/locations") ||
		g2.RouteAllowed("/api/v1/asset-locations") || !g2.RouteAllowed("/api/v1/disposal-records") {
		t.Error("the G2 route gate does not follow the module phases")
	}

	rec := httptest.NewRecorder()
	modulesHandler{set: g2}.list(rec, httptest.NewRequest(http.MethodGet, "/api/v1/modules", nil))
	var body struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range body.Data {
		var key, phase string
		_ = json.Unmarshal(m["key"], &key)
		_ = json.Unmarshal(m["phase"], &phase)
		seen[key] = true
		switch key {
		case "gps":
			if string(m["nav"]) != "[]" || phase != "P4" || string(m["enabled"]) != "false" {
				t.Errorf("gps = %s %s nav %s enabled %s, want P4, nav [] and off", key, phase, m["nav"], m["enabled"])
			}
		case "disposal":
			if phase != "P2" || string(m["enabled"]) != "true" {
				t.Errorf("disposal = phase %s enabled %s, want P2 and on in G2", phase, m["enabled"])
			}
		}
	}
	if !seen["gps"] || !seen["disposal"] || !seen["import"] {
		t.Errorf("GET /api/v1/modules lists %v, want gps, disposal and import", seen)
	}
}

// e38Resource is one P2 resource of the path inventory of E-38 in the
// register of E-105: module is the late module serving it, empty for a core
// path without release gate (entitlement only). owner is the work package
// that adds the prefix to module together with the first route (E-105);
// until then no operation below the path may be documented.
type e38Resource struct {
	path   string
	module string
	owner  string
}

// e38Register lists the P2 resources of E-38 (sections (a) to (c)) that
// have no prefix of their own in the G1 release, after E-105.
var e38Register = []e38Resource{
	// Metamodel (MET-*): the module metamodel, created by WP-250 with the
	// attribute sets, templates since WP-251, rules since WP-716, layouts
	// since WP-259; the other resources add their prefixes with their first
	// route.
	{path: "/api/v1/attribute-sets", module: ModuleMetamodel},
	{path: "/api/v1/ci-types/{id}/attribute-sets", module: ModuleMetamodel},
	{path: "/api/v1/templates", module: ModuleMetamodel},
	{path: "/api/v1/rules", module: ModuleMetamodel},
	{path: "/api/v1/layouts", module: ModuleMetamodel},
	// The drafts (WP-260) and their test run (WP-721) under the prefix
	// /api/v1/metamodel.
	{path: "/api/v1/metamodel/drafts", module: ModuleMetamodel},
	{path: "/api/v1/field-permissions", module: ModuleMetamodel, owner: "WP-726"},
	// New prefixes of existing modules, set with their first route.
	{path: "/api/v1/suppliers", module: ModuleInventory, owner: "WP-736"},
	{path: "/api/v1/contracts", module: ModuleInventory, owner: "WP-269"},
	{path: "/api/v1/labels", module: ModuleInventory, owner: "WP-762"},
	{path: "/api/v1/business-calendars", module: "ticketing", owner: "WP-768"},
	{path: "/api/v1/review-items/{id}/ticket", module: "ticketing", owner: "WP-291"},
	// Import (IMP-IO-01): the module import of this work package.
	{path: "/api/v1/import-profiles", module: "import"},
	{path: "/api/v1/imports", module: "import"},
	// Sub-paths of inventory resources stay with inventory.
	{path: "/api/v1/assets/{id}/assign", module: ModuleInventory},
	{path: "/api/v1/assets/{id}/reserve", module: ModuleInventory},
	{path: "/api/v1/assets/{id}/label", module: ModuleInventory},
	{path: "/api/v1/assets/{id}/as-of", module: ModuleInventory},
	{path: "/api/v1/assets/{id}/tags", module: ModuleInventory},
	{path: "/api/v1/assets/{id}/lifecycle-transitions", module: ModuleInventory},
	{path: "/api/v1/stock-movements/{id}/signature", module: ModuleInventory},
	{path: "/api/v1/disposal-records", module: "disposal"},
	// Core without release gate: tags (the G1 write path), the successors
	// of G1 paths, which ship without x-phase as long as their G1
	// counterpart does, and the sub-paths of core resources.
	{path: "/api/v1/tags", module: ""},
	{path: "/api/v1/cis/{id}/tags", module: ""},
	{path: "/api/v1/lifecycle-models", module: ""},
	{path: "/api/v1/cis/{id}/instance-attributes", module: ""},
	{path: "/api/v1/cis/{id}/as-of", module: ""},
	{path: "/api/v1/cis/{id}/form", module: ""},
	{path: "/api/v1/ci-types/{id}/form", module: ""},
	{path: "/api/v1/cis/{id}/templates", module: ""},
	{path: "/api/v1/cis/{id}/lifecycle-transitions", module: ""},
	{path: "/api/v1/subnets/{id}/reservations", module: ""},
	{path: "/api/v1/relationships/{id}/changes", module: ""},
	{path: "/api/v1/attribute-definitions/{id}/reindex", module: ""},
	{path: "/api/v1/jobs/{id}/revert", module: ""},
	{path: "/api/v1/ci-types/templates", module: ""},
}

// TestE38ResourcesHaveAReleaseGate covers API-06 and RBA-05 (WP-706, E-38,
// E-105): every P2 resource of E-38 belongs to its module, then its
// documented operations carry x-phase P2, or it is core and they carry
// none. A resource whose module prefix a later work package sets has no
// documented operation until then, so no P2 route ships ungated.
func TestE38ResourcesHaveAReleaseGate(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	phases, err := speccheck.Phases(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load phases: %v", err)
	}
	below := func(path string) []speccheck.Operation {
		var ops []speccheck.Operation
		for _, op := range documented {
			if op.Path == path || strings.HasPrefix(op.Path, path+"/") {
				ops = append(ops, op)
			}
		}
		return ops
	}
	for _, r := range e38Register {
		m, inModule := ModuleForRoute(r.path)
		switch {
		case r.module == "":
			if inModule {
				t.Errorf("core path %s belongs to module %s", r.path, m.Key)
			}
			for _, op := range below(r.path) {
				if phase, marked := phases[op.Path]; marked {
					t.Errorf("core operation %s carries x-phase %s", op, phase)
				}
			}
		case inModule && m.Key == r.module:
			for _, op := range below(r.path) {
				if phases[op.Path] != m.Phase {
					t.Errorf("%s (module %s): x-phase %q, want %s", op, m.Key, phases[op.Path], m.Phase)
				}
			}
		case inModule:
			t.Errorf("%s belongs to module %s, want %s (E-105)", r.path, m.Key, r.module)
		case r.owner == "":
			t.Errorf("%s belongs to no module, want %s", r.path, r.module)
		default:
			if ops := below(r.path); len(ops) > 0 {
				t.Errorf("%s is documented (%v) before %s adds its prefix to module %s", r.path, ops, r.owner, r.module)
			}
		}
	}
}

// TestReadmeListsTheModuleKeys covers the documentation of RETICORA_MODULES
// (WP-706): every key docs/README.md lists is accepted by ParseModules, the
// phase value P2 among them, and every late module is listed.
func TestReadmeListsTheModuleKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(readmePath))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	var entry string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "- `RETICORA_MODULES`") {
			entry = line
			break
		}
	}
	_, list, ok := strings.Cut(entry, "comma list of")
	if !ok {
		t.Fatal("docs/README.md: no RETICORA_MODULES entry with a comma list of module keys")
	}
	list, _, _ = strings.Cut(list, "An unknown key")
	var keys []string
	for i, part := range strings.Split(list, "`") {
		if i%2 == 1 {
			keys = append(keys, part)
		}
	}
	for _, key := range keys {
		if _, perr := ParseModules(key); perr != nil {
			t.Errorf("docs/README.md lists %q: %v", key, perr)
		}
	}
	if !slices.Contains(keys, PhaseP2) {
		t.Errorf("docs/README.md does not list the phase value %s", PhaseP2)
	}
	for _, m := range LateModules {
		if !slices.Contains(keys, m.Key) {
			t.Errorf("docs/README.md does not list module %s", m.Key)
		}
	}
}

// TestPhaseValueSwitchesOnTheP2Modules covers release G2 (WP-706): the
// phase value P2 switches on every module with phase P2 and combines with
// module keys.
func TestPhaseValueSwitchesOnTheP2Modules(t *testing.T) {
	g2, err := ParseModules(PhaseP2)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range LateModules {
		if g2.Enabled(m.Key) != (m.Phase == PhaseP2) {
			t.Errorf("G2: module %s (%s) enabled = %v", m.Key, m.Phase, g2.Enabled(m.Key))
		}
	}
	combined, err := ParseModules("P2, gps")
	if err != nil {
		t.Fatal(err)
	}
	if !combined.Enabled("gps") || !combined.Enabled("disposal") || combined.Enabled("keys") {
		t.Errorf("P2,gps enables %v", combined.EnabledKeys())
	}
	if _, err = ParseModules("P3"); err == nil {
		t.Error("the phase value P3 is accepted")
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

// TestP2ModulePrefixesAreGated covers RBA-05 and API-06 (WP-705, E-44,
// E-105): every route prefix of a P2 module has a feature key in the org
// gate of package entitlement, for reading and writing, so a switched-on
// module is not usable without its license. The core module metamodel has
// no feature key (E-105).
func TestP2ModulePrefixesAreGated(t *testing.T) {
	p2 := 0
	for _, m := range LateModules {
		if m.Phase != "P2" || m.Key == ModuleMetamodel {
			continue
		}
		p2++
		for _, prefix := range m.Prefixes {
			for _, path := range []string{prefix, prefix + "/x1"} {
				for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
					if feature, gated := entitlement.RequiredFeature(method, path); !gated || feature == "" {
						t.Errorf("module %s: %s %s has no feature key in the org gate", m.Key, method, path)
					}
				}
			}
		}
	}
	if p2 == 0 {
		t.Fatal("no P2 module in LateModules")
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
