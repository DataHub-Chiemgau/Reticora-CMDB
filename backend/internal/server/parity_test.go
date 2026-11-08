package server

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api/speccheck"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// specPath points at the single source of truth for the public API contract.
const specPath = "../../../api/openapi.yaml"

func testRouter(t *testing.T) *chi.Mux {
	t.Helper()
	return testRouterWith(t, &audit.Handler{})
}

// testRouterWith builds the router; a nil audit handler is the --no-db
// profile, which has no PostgreSQL audit trail.
func testRouterWith(t *testing.T, auditHandler *audit.Handler) *chi.Mux {
	t.Helper()
	return testRouterWithModules(t, auditHandler, nil)
}

// testRouterWithModules builds the router with a set of late modules; nil
// enables them all (WP-214).
func testRouterWithModules(t *testing.T, auditHandler *audit.Handler, modules *ModuleSet) *chi.Mux {
	t.Helper()

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate master key: %v", err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	repos := MemoryRepositories()
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{
		Enforce: false,
	})

	mux, _, err := NewRouter(repos, Options{
		Version:      "test",
		Entitlements: entitlements,
		Dispatcher:   webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         identity.NewOIDCProvider(identity.OIDCConfig{}),
		Audit:        auditHandler,
		Modules:      modules,
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return mux
}

// routerOperations walks the chi route tree and normalises the routes into the
// notation used by the OpenAPI specification.
func routerOperations(t *testing.T, mux *chi.Mux) []speccheck.Operation {
	t.Helper()

	var operations []speccheck.Operation
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/*")
		if route == "" {
			route = "/"
		}
		if route != "/" {
			route = strings.TrimSuffix(route, "/")
		}
		operations = append(operations, speccheck.Operation{Method: strings.ToUpper(method), Path: route})
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return operations
}

// parityReport is the comparison of the registered routes with the
// documented operations of one profile and release.
type parityReport struct {
	// servedPlanned are operations marked x-status: planned that a route
	// serves: the implementing work package removes the mark (E-38).
	servedPlanned []speccheck.Operation
	// undocumented are routes without a documented operation.
	undocumented []speccheck.Operation
	// unimplemented are documented operations without planned and without
	// a route.
	unimplemented []speccheck.Operation
}

// compareWithSpecification compares the registered operations with the
// operations the release documents (expected). An operation marked
// x-status: planned is no part of the expectation (E-38, WP-247); a route
// for it is reported in servedPlanned, not as undocumented.
func compareWithSpecification(registered, expected []speccheck.Operation, planned map[speccheck.Operation]bool) parityReport {
	var report parityReport
	served := make([]speccheck.Operation, 0, len(registered))
	for _, op := range registered {
		if planned[op] {
			report.servedPlanned = append(report.servedPlanned, op)
			continue
		}
		served = append(served, op)
	}
	documented := make([]speccheck.Operation, 0, len(expected))
	for _, op := range expected {
		if !planned[op] {
			documented = append(documented, op)
		}
	}
	report.undocumented, report.unimplemented = speccheck.Diff(served, documented)
	return report
}

// errors renders the findings of the report, one message per kind.
func (r parityReport) errors(context string) []string {
	var out []string
	if len(r.servedPlanned) > 0 {
		out = append(out, context+": routes registered for operations marked x-status: planned; "+
			"x-status: planned entfernen (remove the mark together with the scaffold, E-38):\n"+format(r.servedPlanned))
	}
	if len(r.undocumented) > 0 {
		out = append(out, context+": routes registered but missing from api/openapi.yaml (or marked x-phase):\n"+format(r.undocumented))
	}
	if len(r.unimplemented) > 0 {
		out = append(out, context+": operations documented in api/openapi.yaml but not registered:\n"+format(r.unimplemented))
	}
	return out
}

// TestParityComparisonHandlesPlannedOperations covers the comparison of
// WP-247 with synthetic operations: a planned operation without route
// passes, a planned operation with a route fails with "x-status: planned
// entfernen", and a documented operation without planned and without route
// fails, as does a route without documentation.
func TestParityComparisonHandlesPlannedOperations(t *testing.T) {
	list := speccheck.Operation{Method: http.MethodGet, Path: "/api/v1/tags"}
	create := speccheck.Operation{Method: http.MethodPost, Path: "/api/v1/tags"}
	served := speccheck.Operation{Method: http.MethodGet, Path: "/api/v1/cis"}
	extra := speccheck.Operation{Method: http.MethodDelete, Path: "/api/v1/cis"}
	documented := []speccheck.Operation{list, create, served}

	ok := compareWithSpecification([]speccheck.Operation{served}, documented, map[speccheck.Operation]bool{list: true, create: true})
	if msgs := ok.errors("ok"); len(msgs) != 0 {
		t.Errorf("planned operations without route: %v, want no finding", msgs)
	}

	bad := compareWithSpecification([]speccheck.Operation{served, create}, documented, map[speccheck.Operation]bool{list: true, create: true})
	if len(bad.servedPlanned) != 1 || bad.servedPlanned[0] != create || len(bad.undocumented) != 0 {
		t.Errorf("served planned operation: %+v, want servedPlanned [%s]", bad, create)
	}
	if msgs := bad.errors("served"); len(msgs) != 1 || !strings.Contains(msgs[0], "x-status: planned entfernen") {
		t.Errorf("served planned operation: messages %v, want \"x-status: planned entfernen\"", msgs)
	}

	missing := compareWithSpecification([]speccheck.Operation{extra}, documented, map[speccheck.Operation]bool{list: true})
	if len(missing.unimplemented) != 2 || len(missing.undocumented) != 1 || missing.undocumented[0] != extra {
		t.Errorf("unplanned operations without route: %+v, want POST /api/v1/tags and GET /api/v1/cis unimplemented, DELETE /api/v1/cis undocumented", missing)
	}
	if msgs := missing.errors("missing"); len(msgs) != 2 {
		t.Errorf("unplanned operations without route: messages %v, want two", msgs)
	}
}

// TestRoutesAndSpecificationAreInParity covers API-01 for both profiles:
// the PostgreSQL profile and --no-db serve exactly the documented
// operations. With every late module on, that is the whole specification;
// with release G1 (no late module, WP-214, E-03; the test profile
// NoModules since WP-830) it is the specification without the paths marked
// x-phase; with release G2, the default without RETICORA_MODULES
// (ParseModules(""), WP-830; the same as the phase value P2 of WP-706), the
// paths without x-phase or with x-phase P2. Operations marked x-status:
// planned (E-38, WP-247) are left out in every release and must not be
// served.
func TestRoutesAndSpecificationAreInParity(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	phases, err := speccheck.Phases(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load phases: %v", err)
	}
	planned, err := speccheck.Planned(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load planned operations: %v", err)
	}
	g1 := NoModules()
	g2, err := ParseModules("")
	if err != nil {
		t.Fatalf("parse modules: %v", err)
	}

	for profile, auditHandler := range map[string]*audit.Handler{"postgres": {}, "no-db": nil} {
		for release, modules := range map[string]*ModuleSet{"all modules": AllModules(), "G1": g1, "G2": g2} {
			registered := routerOperations(t, testRouterWithModules(t, auditHandler, modules))

			// /metrics is the Prometheus scrape endpoint, not part of the tenant
			// API contract, and is therefore excluded from the specification.
			filtered := registered[:0]
			for _, op := range registered {
				if op.Path == "/metrics" {
					continue
				}
				// The GraphQL BFF is not publicly documented (GQL-01).
				if strings.HasPrefix(op.Path, "/bff/") {
					continue
				}
				filtered = append(filtered, op)
			}

			// G1 serves the operations without x-phase, G2 those without
			// x-phase or with x-phase P2 (WP-706).
			expected := documented
			if release != "all modules" {
				expected = nil
				for _, op := range documented {
					phase, late := phases[op.Path]
					if !late || (release == "G2" && phase == PhaseP2) {
						expected = append(expected, op)
					}
				}
			}

			for _, msg := range compareWithSpecification(filtered, expected, planned).errors(profile + ", " + release) {
				t.Error(msg)
			}
		}
	}
}

// specDocument is the part of api/openapi.yaml the contract tests of
// WP-247 read: the operations with their responses and the shared
// responses.
type specDocument struct {
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Responses map[string]contractResponse `yaml:"responses"`
		Schemas   map[string]struct {
			Required []string `yaml:"required"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

// TestPlannedOperationsUseProblemResponses covers the scaffold of E-38
// (WP-247, API-03): every 4xx and 5xx response of an operation marked
// x-status: planned is a reference to #/components/responses/*, whose body
// is application/problem+json with the schema ProblemDetail, trace_id
// required (WP-163). TestEveryErrorIsAProblemWithTraceID covers the
// runtime.
func TestPlannedOperationsUseProblemResponses(t *testing.T) {
	planned, err := speccheck.Planned(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load planned operations: %v", err)
	}
	if len(planned) == 0 {
		t.Fatal("no operation is marked x-status: planned")
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc specDocument
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	if !slices.Contains(doc.Components.Schemas["ProblemDetail"].Required, "trace_id") {
		t.Fatal("ProblemDetail does not require trace_id")
	}
	const prefix = "#/components/responses/"
	for op := range planned {
		node, ok := doc.Paths[op.Path][strings.ToLower(op.Method)]
		if !ok {
			t.Errorf("%s: planned but not documented", op)
			continue
		}
		var operation struct {
			Responses map[string]contractResponse `yaml:"responses"`
		}
		if derr := node.Decode(&operation); derr != nil {
			t.Fatalf("decode %s: %v", op, derr)
		}
		success := false
		for code, response := range operation.Responses {
			if strings.HasPrefix(code, "2") {
				success = true
				continue
			}
			if !strings.HasPrefix(code, "4") && !strings.HasPrefix(code, "5") {
				continue
			}
			if !strings.HasPrefix(response.Ref, prefix) {
				t.Errorf("%s %s: no reference to %s*", op, code, prefix)
				continue
			}
			component, known := doc.Components.Responses[strings.TrimPrefix(response.Ref, prefix)]
			if !known {
				t.Errorf("%s %s: %s is no shared response", op, code, response.Ref)
				continue
			}
			media, problem := component.Content["application/problem+json"]
			var body struct {
				Schema struct {
					Ref string `yaml:"$ref"`
				} `yaml:"schema"`
			}
			if problem {
				problem = media.Decode(&body) == nil && body.Schema.Ref == "#/components/schemas/ProblemDetail"
			}
			if !problem || len(component.Content) != 1 {
				t.Errorf("%s %s: %s is no application/problem+json with ProblemDetail", op, code, response.Ref)
			}
		}
		if !success {
			t.Errorf("%s: no 2xx response", op)
		}
	}
}

// TestDeprecationSuccessorsAreServed covers the successor rule of E-38
// (WP-247, API-01): the x-successor of every deprecated operation
// (middleware.ParseDeprecations) is a documented path with at least one
// operation not marked x-status: planned, and its x-phase is empty or that
// of the deprecated path, so every release that serves the alias serves
// the successor too.
func TestDeprecationSuccessorsAreServed(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	deprecations, err := middleware.ParseDeprecations(raw)
	if err != nil {
		t.Fatalf("parse deprecations: %v", err)
	}
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	phases, err := speccheck.Phases(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load phases: %v", err)
	}
	planned, err := speccheck.Planned(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load planned operations: %v", err)
	}
	// x-successor may name the path parameters differently from the
	// documented path ({id} for {ci_id}); compare with anonymous ones.
	anonymous := func(path string) string {
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
				parts[i] = "{}"
			}
		}
		return strings.Join(parts, "/")
	}
	served := map[string]string{}
	for _, op := range documented {
		if !planned[op] {
			served[anonymous(op.Path)] = op.Path
		}
	}

	successors := 0
	for key, d := range deprecations {
		if d.Successor == "" {
			continue
		}
		successors++
		_, alias, _ := strings.Cut(key, " ")
		target, _, _ := strings.Cut(d.Successor, "?")
		successor, ok := served[anonymous(target)]
		if !ok {
			t.Errorf("%s: x-successor %s is no documented path with an operation that is not planned", key, d.Successor)
			continue
		}
		if phase := phases[successor]; phase != "" && phase != phases[alias] {
			t.Errorf("%s: x-successor %s has x-phase %s, the alias %q", key, successor, phase, phases[alias])
		}
	}
	// 22 deprecations of the G1 contract and assets/{id}/movements (WP-247).
	if successors < 23 {
		t.Errorf("%d deprecations with x-successor, want at least 23", successors)
	}
	if d := deprecations["GET /api/v1/assets/{id}/movements"]; d.Successor != "/api/v1/stock-movements" {
		t.Errorf("GET /api/v1/assets/{id}/movements: x-successor %q, want /api/v1/stock-movements", d.Successor)
	}
}

func format(ops []speccheck.Operation) string {
	lines := make([]string, 0, len(ops))
	for _, op := range ops {
		lines = append(lines, "  "+op.String())
	}
	return strings.Join(lines, "\n")
}

func TestNewRouterRequiresCoreDependencies(t *testing.T) {
	if _, _, err := NewRouter(Repositories{}, Options{}); err == nil {
		t.Fatal("expected an error when required dependencies are missing")
	}
}
