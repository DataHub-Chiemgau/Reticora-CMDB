package graphqlbff

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// The P3 read views at the CI (API-06, GQL-02, WP-1136) with fake batch
// sources: module gate before right and feature, a partial answer for a
// refused field, one batch read per field for a page of CIs, the
// complexity of the lists and no step payload in WorkflowRun.

// fakeP3 serves one finding, result, score and run per CI and counts its
// batch reads.
type fakeP3 struct {
	mu    sync.Mutex
	calls map[string]int
	ids   map[string][]string
}

func newFakeP3() *fakeP3 { return &fakeP3{calls: map[string]int{}, ids: map[string][]string{}} }

func (f *fakeP3) hit(name string, ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	f.ids[name] = ids
}

func (f *fakeP3) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

var p3Time = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

func (f *fakeP3) FindingsByCIs(_ context.Context, _ string, ids, statuses []string, _ int) ([]security.Finding, error) {
	f.hit("findings", ids)
	out := []security.Finding{}
	for _, id := range ids {
		out = append(out, security.Finding{ID: "f-" + id, CIID: id, Kind: "vulnerability", Severity: "high", Title: "CVE-2026-1",
			Status: statuses[0], Source: security.SourceCVEFeed, CVEIDs: []string{"CVE-2026-1"}, KEV: true, DetectedAt: p3Time, LastSeenAt: p3Time})
	}
	return out, nil
}

func (f *fakeP3) ResultsByCIs(_ context.Context, _ string, ids []string, _ string, _ int) ([]compliance.CIResult, error) {
	f.hit("results", ids)
	out := []compliance.CIResult{}
	for _, id := range ids {
		out = append(out, compliance.CIResult{CIID: id, RuleID: "r-1", RuleName: "ssh", Severity: "high", Status: "fail",
			EvaluatedAt: p3Time, StatusChangedAt: p3Time})
	}
	return out, nil
}

func (f *fakeP3) ScoresByCIs(_ context.Context, _ string, ids []string) (map[string]compliance.ScoreGroup, error) {
	f.hit("scores", ids)
	out := map[string]compliance.ScoreGroup{}
	for _, id := range ids {
		out[id] = compliance.ScoreGroup{Key: id, Passed: 3, Failed: 1, Score: 75}
	}
	return out, nil
}

func (f *fakeP3) RunsByCIs(_ context.Context, _ string, ids []string, _ string, _ int) ([]workflow.CIRun, error) {
	f.hit("runs", ids)
	out := []workflow.CIRun{}
	for _, id := range ids {
		out = append(out, workflow.CIRun{ID: "run-" + id, CIID: id, WorkflowID: "wf-1", WorkflowName: "retire", Version: 2,
			Status: "succeeded", Trigger: "manual", StartedAt: &p3Time, CreatedAt: p3Time})
	}
	return out, nil
}

var p3Perms = []identity.Permission{identity.PermCIRead, identity.PermFindingRead, identity.PermComplianceRead, identity.PermWorkflowRead}

var p3Features = entitled{FeatureCMDBCore: true, entitlement.FeatureComplianceFindings: true, entitlement.FeatureWorkflows: true}

var p3Modules = modules{ModuleFindings: true, ModuleCompliance: true, ModuleWorkflows: true}

type p3Fixture struct {
	*assetFixture
	fake *fakeP3
	cis  []string
	rels relationship.Repository
}

// newP3Fixture builds n CIs of the organization and the handler with the
// fake sources, all modules and features on.
func newP3Fixture(t *testing.T, n int) *p3Fixture {
	t.Helper()
	f := &p3Fixture{assetFixture: &assetFixture{n: &readCounter{reads: map[string]int{}}}, fake: newFakeP3()}
	repo := ci.NewMemoryRepository()
	for i := range n {
		item := &ci.Item{OrganizationID: fixtureOrg, Name: fmt.Sprintf("p3-%03d", i), CITypeID: "server", Status: "active", Attributes: map[string]any{}}
		if err := repo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		f.cis = append(f.cis, item.ID)
	}
	f.rels = relationship.NewMemoryRepository()
	f.h = NewHandler(repo, f.rels).WithEntitlements(p3Features).WithModules(p3Modules).
		WithSources(&Sources{Findings: f.fake, Compliance: f.fake, WorkflowRuns: f.fake})
	return f
}

const p3Detail = `{ ci(id: "%s") { name findings { id severity status cveIds kev } complianceResults { ruleId ruleName status }
	complianceScore { score passed failed notApplicable } workflowRuns { id workflowName version status } } }`

// TestP3FieldsModuleOff: with the module findings off findings is [] with
// UNAVAILABLE at ci.findings; name and complianceScore are set.
func TestP3FieldsModuleOff(t *testing.T) {
	f := newP3Fixture(t, 1)
	f.h.WithModules(modules{ModuleCompliance: true, ModuleWorkflows: true})
	resp := f.run(t, f.h, p3Perms, nil, fmt.Sprintf(p3Detail, f.cis[0]))
	if resp.codeAt("ci.findings") != "UNAVAILABLE" || len(resp.Errors) != 1 {
		t.Fatalf("errors %+v, want UNAVAILABLE at ci.findings only", resp.Errors)
	}
	c, _ := resp.Data["ci"].(map[string]any)
	if c == nil || c["name"] != "p3-000" {
		t.Fatalf("ci %v, want the CI with its name", resp.Data)
	}
	if list, ok := c["findings"].([]any); !ok || len(list) != 0 {
		t.Errorf("findings %v, want []", c["findings"])
	}
	if score, _ := c["complianceScore"].(map[string]any); score == nil || score["score"] != 75.0 {
		t.Errorf("complianceScore %v, want set", c["complianceScore"])
	}
	if f.fake.count("findings") != 0 {
		t.Error("findings read with the module off")
	}
}

// TestP3FieldsPermission: without finding:read findings is FORBIDDEN,
// without the feature compliance_findings the compliance fields are.
func TestP3FieldsPermission(t *testing.T) {
	f := newP3Fixture(t, 1)
	perms := slices.DeleteFunc(slices.Clone(p3Perms), func(p identity.Permission) bool { return p == identity.PermFindingRead })
	resp := f.run(t, f.h, perms, nil, fmt.Sprintf(p3Detail, f.cis[0]))
	if resp.codeAt("ci.findings") != codeForbidden || resp.codeAt("ci.complianceResults") != "" {
		t.Errorf("without finding:read: %+v", resp.Errors)
	}
	f.h.WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureWorkflows: true})
	resp = f.run(t, f.h, p3Perms, nil, fmt.Sprintf(p3Detail, f.cis[0]))
	if resp.codeAt("ci.complianceResults") != codeForbidden || resp.codeAt("ci.complianceScore") != codeForbidden ||
		resp.codeAt("ci.findings") != codeForbidden || resp.codeAt("ci.workflowRuns") != "" {
		t.Errorf("without compliance_findings: %+v", resp.Errors)
	}
	if c, _ := resp.Data["ci"].(map[string]any); c == nil || c["name"] != "p3-000" {
		t.Errorf("the CI is lost with a refused field: %v", resp.Data)
	}
	// Without a module checker every P3 field is refused (fail-closed).
	f.h.WithEntitlements(p3Features).WithModules(nil)
	resp = f.run(t, f.h, p3Perms, nil, fmt.Sprintf(p3Detail, f.cis[0]))
	if resp.codeAt("ci.workflowRuns") != "UNAVAILABLE" {
		t.Errorf("without a module checker: %+v", resp.Errors)
	}
}

// TestP3FieldsUseTheLoader: cis with 50 CIs calls each batch source once
// with all 50 CIs (no N+1, AK6), and every CI gets its rows.
func TestP3FieldsUseTheLoader(t *testing.T) {
	f := newP3Fixture(t, 50)
	// Small pages per CI keep the page of 50 CIs within the budget.
	resp := f.run(t, f.h, p3Perms, nil, `{ cis(first: 50) { nodes { id findings(first: 3) { id } complianceResults(first: 3) { ruleId }
		complianceScore { score } workflowRuns(first: 3) { id version } } } }`)
	if len(resp.Errors) != 0 {
		t.Fatalf("errors %+v", resp.Errors)
	}
	for _, name := range []string{"findings", "results", "scores", "runs"} {
		if n := f.fake.count(name); n != 1 || len(f.fake.ids[name]) != 50 {
			t.Errorf("%s: %d reads over %d CIs, want one over 50", name, n, len(f.fake.ids[name]))
		}
	}
	conn, _ := resp.Data["cis"].(map[string]any)
	nodes, _ := conn["nodes"].([]any)
	if len(nodes) != 50 {
		t.Fatalf("nodes %d, want 50", len(nodes))
	}
	for _, raw := range nodes {
		n, _ := raw.(map[string]any)
		id, _ := n["id"].(string)
		findings, _ := n["findings"].([]any)
		runs, _ := n["workflowRuns"].([]any)
		if len(findings) != 1 || len(runs) != 1 {
			t.Fatalf("CI %s: findings %v runs %v, want one each", id, findings, runs)
		}
		if fid, _ := findings[0].(map[string]any)["id"].(string); fid != "f-"+id {
			t.Errorf("CI %s got finding %s", id, fid)
		}
	}
	// Default status filter: open and acknowledged.
	f.fake.calls = map[string]int{}
	resp = f.run(t, f.h, p3Perms, nil, fmt.Sprintf(`{ ci(id: "%s") { findings { status } } }`, f.cis[0]))
	c, _ := resp.Data["ci"].(map[string]any)
	if list, _ := c["findings"].([]any); len(list) != 1 || list[0].(map[string]any)["status"] != security.StatusOpen {
		t.Errorf("default statuses: %v", c)
	}
}

// TestP3FieldsBatchOverRelationships: the CIs at the ends of a
// relationship list are one level too: findings of every target CI are
// read once over all of them (AK4, no N+1 outside cis).
func TestP3FieldsBatchOverRelationships(t *testing.T) {
	f := newP3Fixture(t, 21)
	for _, target := range f.cis[1:] {
		if err := f.rels.Create(context.Background(), &relationship.Relationship{OrganizationID: fixtureOrg, SourceCIID: f.cis[0],
			TargetCIID: target, RelType: "depends_on"}); err != nil {
			t.Fatal(err)
		}
	}
	perms := append(slices.Clone(p3Perms), identity.PermRelationshipRead)
	resp := f.run(t, f.h, perms, nil, fmt.Sprintf(`{ relationships(ciId: "%s", first: 50) { targetCi { id findings(first: 1) { id } } } }`, f.cis[0]))
	if len(resp.Errors) != 0 {
		t.Fatalf("errors %+v", resp.Errors)
	}
	if n := f.fake.count("findings"); n != 1 {
		t.Errorf("findings read %d times over the targets of 20 relationships, want once", n)
	}
	rels, _ := resp.Data["relationships"].([]any)
	if len(rels) != 20 {
		t.Fatalf("relationships %d, want 20", len(rels))
	}
	for _, raw := range rels {
		target, _ := raw.(map[string]any)["targetCi"].(map[string]any)
		id, _ := target["id"].(string)
		findings, _ := target["findings"].([]any)
		if len(findings) != 1 || findings[0].(map[string]any)["id"] != "f-"+id {
			t.Errorf("target %s findings %v, want its own", id, findings)
		}
	}
}

// TestP3Complexity: cis(first: 100) with findings(first: 100) exceeds the
// budget and is refused before it runs; the detail query of one CI with
// all four fields is admitted.
func TestP3Complexity(t *testing.T) {
	f := newP3Fixture(t, 3)
	resp := f.run(t, f.h, p3Perms, nil, `{ cis(first: 100) { nodes { findings(first: 100) { id } } } }`)
	if len(resp.Errors) == 0 || !strings.Contains(strings.ToLower(resp.Errors[0].Message), "complexity") {
		t.Fatalf("errors %+v, want the complexity refusal", resp.Errors)
	}
	if f.fake.count("findings") != 0 {
		t.Error("the refused query read findings")
	}
	resp = f.run(t, f.h, p3Perms, nil, fmt.Sprintf(p3Detail, f.cis[0]))
	if len(resp.Errors) != 0 {
		t.Errorf("detail query refused: %+v", resp.Errors)
	}
}

// TestP3NoStepPayload: WorkflowRun names no input, output, context or
// steps.
func TestP3NoStepPayload(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
	def := schema.Types["WorkflowRun"]
	if def == nil {
		t.Fatal("WorkflowRun is not in the schema")
	}
	for _, field := range def.Fields {
		name := strings.ToLower(field.Name)
		for _, banned := range []string{"input", "output", "context", "step", "payload"} {
			if strings.Contains(name, banned) {
				t.Errorf("WorkflowRun.%s exposes a step payload", field.Name)
			}
		}
	}
}
