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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// readmePath is the operator documentation that lists RETICORA_MODULES.
const readmePath = "../../../docs/README.md"

// modulesWithoutPages are the late modules without a navigation page: the
// GPS history (LOC-09) has no UI of its own (WP-706).
var modulesWithoutPages = []string{"gps", "ipam", "admp", "escalation", "org_lifecycle"}

// TestG2DefaultEnablesP2ModulesOnly covers API-06 and RBA-05 (WP-830,
// E-03, SEQ-01): without RETICORA_MODULES the release default G2 switches
// on exactly the modules with phase P2, the same as the phase value P2;
// the modules of P3, P4 and the add-ons [A] stay off, their routes are not
// registered and their workers do not start. Release G1 (every late module
// off, WP-214) stays as the test profile NoModules.
func TestG2DefaultEnablesP2ModulesOnly(t *testing.T) {
	g2, err := ParseModules("")
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}
	phase, err := ParseModules(PhaseP2)
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}
	var want []string
	for _, m := range LateModules {
		if !slices.Contains([]string{"P2", "P3", "P4", "A"}, m.Phase) {
			t.Errorf("module %s: phase %q is not a late phase", m.Key, m.Phase)
		}
		// Only a module without a UI of its own (gps, WP-706) has no
		// navigation page; its Nav is empty, never nil.
		if len(m.Prefixes) == 0 || m.Nav == nil || (len(m.Nav) == 0 && !slices.Contains(modulesWithoutPages, m.Key)) {
			t.Errorf("module %s: needs route prefixes and navigation pages", m.Key)
		}
		if m.Phase == PhaseP2 {
			want = append(want, m.Key)
		}
	}
	slices.Sort(want)
	if got := g2.EnabledKeys(); !slices.Equal(got, want) || !slices.Equal(got, phase.EnabledKeys()) {
		t.Fatalf("G2 default enables %v, want the P2 modules %v (RETICORA_MODULES=P2: %v)", got, want, phase.EnabledKeys())
	}
	for _, key := range []string{"inventory", "documents", "stocktake", "ticketing", "disposal"} {
		if !slices.Contains(want, key) {
			t.Errorf("module %s is not a P2 module", key)
		}
	}
	for _, key := range []string{"maintenance", "workflows", "compliance", "findings", "monitoring", "ipam", "admp",
		"escalation", "org_lifecycle", "keys", "trainings", "desks", "gps", "agents", "iga", "ai"} {
		if g2.Enabled(key) {
			t.Errorf("module %s (P3, P4 or [A]) is on in the G2 default", key)
		}
	}

	mux := testRouterWithModules(t, &audit.Handler{}, g2)
	// The P2 modules are served (401 or 403 without a principal, not 404),
	// the G1 surface too.
	for _, path := range []string{"/api/v1/assets", "/api/v1/tickets", "/api/v1/documents", "/api/v1/stocktakes",
		"/api/v1/disposal-records", "/api/v1/cis", "/api/v1/monitoring/metrics"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("GET %s with the G2 default = 404, want it served", path)
		}
	}
	for _, path := range []string{"/api/v1/workflows", "/api/v1/maintenance-windows", "/api/v1/monitoring/alerts",
		"/api/v1/agents", "/api/v1/asset-locations", "/api/v1/assets/c0de0000-0000-4000-8000-000000000001/locations",
		"/api/v1/iga/connectors", "/scim/v2/Users", "/api/v1/ai/conversations"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the G2 default = %d, want 404", path, rec.Code)
		}
	}

	if g2.WorkerAllowed(config.WorkerMonitoring) {
		t.Error("the alert evaluator (MON-04, P3) starts with the G2 default")
	}
	if !g2.Enabled(ModuleInventory) {
		t.Error("the reservation sweeper (module inventory) does not start with the G2 default")
	}
	for _, name := range config.WorkerNames {
		p3 := slices.Contains(p3Workers, name)
		if g2.WorkerAllowed(name) == p3 {
			t.Errorf("worker %s: allowed %v in the G2 default, want %v (only the workers of the P3 modules are off)",
				name, g2.WorkerAllowed(name), !p3)
		}
	}

	// The test profile of release G1: every late module off, the P2 paths
	// of the metamodel included (WP-214, E-105).
	g1 := NoModules()
	if keys := g1.EnabledKeys(); len(keys) != 0 {
		t.Fatalf("the G1 profile enables %v, want none", keys)
	}
	g1mux := testRouterWithModules(t, &audit.Handler{}, g1)
	for _, path := range []string{"/api/v1/tickets", "/api/v1/assets", "/api/v1/attribute-sets",
		"/api/v1/templates/c0de0000-0000-4000-8000-000000000001/versions", "/api/v1/rules", "/api/v1/layouts"} {
		rec := httptest.NewRecorder()
		g1mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the G1 profile = %d, want 404", path, rec.Code)
		}
	}
	if g1.WorkerAllowed(config.WorkerMonitoring) || g1.Enabled(ModuleInventory) {
		t.Error("the G1 profile starts the alert evaluator or the reservation sweeper")
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
// module's phase, and no other path does. Each operation of a P2 or P3
// module (WP-863) carries x-feature (path or operation level) equal to the key of the org
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
		case late && (m.Phase == PhaseP2 || m.Phase == PhaseP3) && gated && feature != gate:
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
	// Field permissions (WP-726).
	{path: "/api/v1/field-permissions", module: ModuleMetamodel},
	// New prefixes of existing modules, set with their first route.
	{path: "/api/v1/suppliers", module: ModuleInventory, owner: "WP-736"},
	{path: "/api/v1/contracts", module: ModuleInventory, owner: "WP-269"},
	{path: "/api/v1/labels", module: ModuleInventory, owner: "WP-762"},
	// Business calendars of the SLA clock (WP-768), module ticketing.
	{path: "/api/v1/business-calendars", module: "ticketing"},
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
	if _, err = ParseModules("P4"); err == nil {
		t.Error("the phase value P4 is accepted")
	}
}

// TestPhaseValueP3SwitchesOnTheP2AndP3Modules covers release G3 (WP-863,
// E-160, E-170): the phase value P3 switches on every module with phase P2
// or P3 and combines with module keys; the former key alerting stands for
// monitoring; the P3 prefixes of the register map routes to their module.
func TestPhaseValueP3SwitchesOnTheP2AndP3Modules(t *testing.T) {
	g3, err := ParseModules(PhaseP3)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range LateModules {
		if g3.Enabled(m.Key) != (m.Phase == PhaseP2 || m.Phase == PhaseP3) {
			t.Errorf("G3: module %s (%s) enabled = %v", m.Key, m.Phase, g3.Enabled(m.Key))
		}
	}
	combined, err := ParseModules("P3,keys")
	if err != nil {
		t.Fatal(err)
	}
	if !combined.Enabled("keys") || !combined.Enabled(ModuleMonitoring) || combined.Enabled("gps") {
		t.Errorf("P3,keys enables %v", combined.EnabledKeys())
	}
	alias, err := ParseModules("alerting")
	if err != nil {
		t.Fatal(err)
	}
	if got := alias.EnabledKeys(); !slices.Equal(got, []string{ModuleMonitoring}) {
		t.Errorf("RETICORA_MODULES=alerting enables %v, want [monitoring]", got)
	}
	for route, want := range map[string]string{
		"/api/v1/cis/{id}/addresses":              "ipam",
		"/api/v1/cis/{id}/configs":                ModuleMonitoring,
		"/api/v1/cis/{id}/availability":           ModuleMonitoring,
		"/api/v1/cis/{id}/credentials/effective":  ModuleMonitoring,
		"/api/v1/network-interfaces/{id}/admp":    "admp",
		"/api/v1/collector-groups/{id}":           ModuleMonitoring,
		"/api/v1/monitoring/alerts/{id}":          ModuleMonitoring,
		"/api/v1/subnets/{id}/scan":               "ipam",
		"/api/v1/notification-callbacks/{org_id}": "escalation",
		"/api/v1/org/exports":                     "org_lifecycle",
	} {
		if m, ok := ModuleForRoute(route); !ok || m.Key != want {
			t.Errorf("%s belongs to %q, want %s", route, m.Key, want)
		}
	}
	for _, core := range []string{"/api/v1/cis/{id}", "/api/v1/monitoring/metrics", "/api/v1/subnets/{id}", "/api/v1/org"} {
		if m, ok := ModuleForRoute(core); ok {
			t.Errorf("%s belongs to module %s, want core", core, m.Key)
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
// the UI reads to hide the navigation of switched-off modules: with the
// release default G2 (WP-830) exactly the P2 modules are enabled, with a
// list of module keys only the modules named.
func TestModulesEndpointListsTheReleaseModules(t *testing.T) {
	type entry struct {
		Key     string   `json:"key"`
		Phase   string   `json:"phase"`
		Nav     []string `json:"nav"`
		Enabled bool     `json:"enabled"`
	}
	list := func(spec string) []entry {
		t.Helper()
		set, err := ParseModules(spec)
		if err != nil {
			t.Fatalf("parse modules %q: %v", spec, err)
		}
		rec := httptest.NewRecorder()
		modulesHandler{set: set}.list(rec, httptest.NewRequest(http.MethodGet, "/api/v1/modules", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("RETICORA_MODULES=%q: status = %d, want 200", spec, rec.Code)
		}
		var body struct {
			Data []entry `json:"data"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Data) != len(LateModules) {
			t.Fatalf("RETICORA_MODULES=%q: got %d modules, want %d", spec, len(body.Data), len(LateModules))
		}
		return body.Data
	}
	for _, m := range list("") {
		if m.Enabled != (m.Phase == PhaseP2) {
			t.Errorf("G2 default: module %s (%s) enabled = %v, want %v", m.Key, m.Phase, m.Enabled, m.Phase == PhaseP2)
		}
	}
	for _, m := range list("documents") {
		if m.Enabled != (m.Key == "documents") {
			t.Errorf("documents: module %s enabled = %v", m.Key, m.Enabled)
		}
		if m.Key == "documents" && !slices.Equal(m.Nav, []string{"documents"}) {
			t.Errorf("documents nav = %v", m.Nav)
		}
	}
}

// TestBFFFollowsTheModuleGate covers WP-311 (E-03, GQL-04): /bff/graphql
// belongs to no module prefix, so the BFF gates its fields itself. With
// the G1 profile (NoModules) asset answers UNAVAILABLE; the module keys of
// the BFF are keys of LateModules.
func TestBFFFollowsTheModuleGate(t *testing.T) {
	for _, key := range graphqlbff.ModuleKeys {
		if _, ok := moduleByKey(key); !ok {
			t.Errorf("BFF module %q is not in LateModules", key)
		}
	}
	g1 := NoModules()
	query := func(modules *ModuleSet) map[string]any {
		mux := testRouterWithModules(t, &audit.Handler{}, modules)
		req := httptest.NewRequest(http.MethodPost, graphqlbff.Path,
			strings.NewReader(`{"query":"{ asset(id: \"c0de0000-0000-4000-8000-000000000001\") { id } }"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: "org-1",
			Permissions: []identity.Permission{identity.PermAssetRead}, Type: identity.PrincipalTypeUser})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", graphqlbff.Path, rec.Code, rec.Body)
		}
		var body map[string]any
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatalf("decode: %v", decodeErr)
		}
		return body
	}
	code := func(body map[string]any) string {
		errs, _ := body["errors"].([]any)
		if len(errs) == 0 {
			return ""
		}
		ext, _ := errs[0].(map[string]any)["extensions"].(map[string]any)
		c, _ := ext["code"].(string)
		return c
	}
	if got := code(query(g1)); got != "UNAVAILABLE" {
		t.Errorf("asset with the G1 profile: code %q, want UNAVAILABLE", got)
	}
	inventory, err := ParseModules(graphqlbff.ModuleInventory)
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}
	if body := query(inventory); code(body) != "" {
		t.Errorf("asset with inventory on: %v, want null without error", body)
	}
}

// TestEveryP3PrefixIsDocumented covers WP-864 (API-06, E-160): every prefix
// of a P3 module has at least one documented operation, implemented or a
// scaffold with x-status: planned.
func TestEveryP3PrefixIsDocumented(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	for _, m := range LateModules {
		if m.Phase != PhaseP3 {
			continue
		}
		for _, prefix := range m.Prefixes {
			found := false
			for _, op := range documented {
				if mod, ok := ModuleForRoute(op.Path); ok && mod.Key == m.Key && prefixSegments(prefix, strings.Split(op.Path, "/")) >= 0 {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("module %s: prefix %s has no documented operation", m.Key, prefix)
			}
		}
	}
}

// TestP3ModulePrefixesAreGated covers WP-865 (PKG-08, ENT-05, E-151, E-160):
// every prefix and sub-path of a P3 module has a feature key in the org
// gate, except the module org_lifecycle (core, TLC-05, TLC-06) and the
// public provider callback /api/v1/notification-callbacks (WP-1146).
func TestP3ModulePrefixesAreGated(t *testing.T) {
	p3 := 0
	for _, m := range LateModules {
		if m.Phase != PhaseP3 || m.Key == "org_lifecycle" {
			continue
		}
		p3++
		for _, prefix := range m.Prefixes {
			concrete := strings.ReplaceAll(prefix, "{id}", "x1")
			for _, path := range []string{concrete, concrete + "/x2"} {
				for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
					feature, gated := entitlement.RequiredFeature(method, path)
					if prefix == "/api/v1/notification-callbacks" {
						if gated {
							t.Errorf("%s %s: the provider callback has the feature %s", method, path, feature)
						}
						continue
					}
					if !gated || feature == "" {
						t.Errorf("module %s: %s %s has no feature key in the org gate", m.Key, method, path)
					}
				}
			}
		}
	}
	if p3 == 0 {
		t.Fatal("no P3 module in LateModules")
	}
	for path, want := range map[string]string{
		"/api/v1/cis/x1/credentials":           entitlement.FeatureMonitoring,
		"/api/v1/cis/x1/credentials/effective": entitlement.FeatureMonitoring,
		"/api/v1/cis/x1/addresses":             entitlement.FeatureIPAM,
		"/api/v1/cis/x1/configs":               entitlement.FeatureMonitoringNetwork,
		"/api/v1/network-interfaces/x1/admp":   entitlement.FeatureADMP,
	} {
		if got, _ := entitlement.RequiredFeature(http.MethodGet, path); got != want {
			t.Errorf("GET %s: %q, want %q", path, got, want)
		}
	}
}

// p3Workers are the workers of the P3 modules (OPS-02 [P3], WP-869). A work
// package that adds a worker of a P3 module (WP-980 monitoring, WP-1114
// workflow) adds its name here, in LateModules[].Workers, in both
// deployments (TestEveryWorkerSwitchIsDeployable) and in docs/ops/workers.md.
var p3Workers = []string{config.WorkerMonitoring, config.WorkerProbeAssign, config.WorkerProbeIngest, config.WorkerWorkflow,
	config.WorkerAlarmCheck}

// TestP3WorkersBelongToTheirModule covers OPS-02 [P3] (WP-869, E-162):
// every worker of a P3 module is listed in LateModules[].Workers of a P3
// module, is a registered worker switch and does not start in the release
// G2; every worker a P3 module lists is a worker of p3Workers.
func TestP3WorkersBelongToTheirModule(t *testing.T) {
	g2, err := ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, m := range LateModules {
		if m.Phase != PhaseP3 {
			continue
		}
		for _, w := range m.Workers {
			listed[w] = m.Key
			if !slices.Contains(p3Workers, w) {
				t.Errorf("module %s lists the worker %s, which is no P3 worker of p3Workers", m.Key, w)
			}
		}
	}
	for _, w := range p3Workers {
		if _, ok := listed[w]; !ok {
			t.Errorf("P3 worker %s is listed by no P3 module", w)
		}
		if !slices.Contains(config.WorkerNames, w) {
			t.Errorf("P3 worker %s is no registered worker switch", w)
		}
		if g2.WorkerAllowed(w) {
			t.Errorf("P3 worker %s starts in the release G2", w)
		}
	}
	if g3, _ := ParseModules(PhaseP3); !g3.WorkerAllowed(config.WorkerMonitoring) {
		t.Error("the monitoring worker does not start in the release G3")
	}
}
