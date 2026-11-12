package compliance

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
)

func TestEvaluatorScoresFailures(t *testing.T) {
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	_ = cis.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "srv", CITypeID: "type-server", Status: "active", Attributes: map[string]any{"encrypted": false}})
	rule := &Rule{OrganizationID: "org-1", CITypeID: "type-server", Name: "encrypted", Severity: "high", Category: "ISO27001", Expression: JSONMap{"field": "attributes.encrypted", "op": "eq", "value": true}, RemediationHint: "Enable encryption", Active: true}
	_ = repo.CreateRule(context.Background(), rule)
	resp, err := NewEvaluator(repo, cis).Evaluate(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Overall.Failed != 1 || resp.Overall.Score != 0 {
		t.Fatalf("unexpected score %#v", resp.Overall)
	}
}

// Compliance rules are MET-45 conditions over the inputs of a CI (SEC-03,
// CMP-02, WP-1125, E-229 option 1).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestEvaluateMET45Condition|TestAppliesWhen|TestFindingInputs|TestLifecycleStateInput|TestValidateConditionInputs' ./internal/compliance/

// evaluate runs the rules over the CIs of org-1 and returns the status per
// rule name and CI name.
func evaluate(t *testing.T, repo *MemoryRepository, items []*ci.Item, rules ...*Rule) map[string]string {
	t.Helper()
	ctx := context.Background()
	cis := ci.NewMemoryRepository()
	names := map[string]string{}
	for _, item := range items {
		item.OrganizationID = "org-1"
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		names[item.ID] = item.Name
	}
	ruleNames := map[string]string{}
	for _, r := range rules {
		r.OrganizationID, r.Severity, r.Category, r.Active = "org-1", "high", "ISO27001", true
		if err := repo.CreateRule(ctx, r); err != nil {
			t.Fatal(err)
		}
		ruleNames[r.ID] = r.Name
	}
	resp, err := NewEvaluator(repo, cis).Evaluate(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, res := range resp.Results {
		out[ruleNames[res.RuleID]+"/"+names[res.CIID]] = res.Status
	}
	return out
}

// TestEvaluateMET45Condition: all, any and not with eq, in, gte, contains
// and empty on columns and attributes.
func TestEvaluateMET45Condition(t *testing.T) {
	items := []*ci.Item{
		{Name: "sw-1", CITypeID: "t-switch", Status: "active", Manufacturer: "Cisco", OSVersion: "17.9", Attributes: map[string]any{"ports": 48, "roles": []any{"core"}}},
		{Name: "sw-2", CITypeID: "t-switch", Status: "maintenance", Manufacturer: "HPE", Attributes: map[string]any{"ports": "24", "owner": "net"}},
	}
	got := evaluate(t, NewMemoryRepository(), items,
		&Rule{Name: "all", Expression: JSONMap{"op": "all", "rules": []any{
			map[string]any{"op": "eq", "field": "status", "value": "active"},
			map[string]any{"op": "gte", "field": "attributes.ports", "value": 48},
		}}},
		&Rule{Name: "any", Expression: JSONMap{"op": "any", "rules": []any{
			map[string]any{"op": "in", "field": "manufacturer", "value": []any{"HPE", "Aruba"}},
			map[string]any{"op": "contains", "field": "attributes.roles", "value": "core"},
		}}},
		&Rule{Name: "not", Expression: JSONMap{"op": "not", "rules": []any{map[string]any{"op": "empty", "field": "attributes.owner"}}}},
		&Rule{Name: "os", Expression: JSONMap{"op": "contains", "field": "os_version", "value": "17."}},
	)
	for key, want := range map[string]string{
		"all/sw-1": "pass", "all/sw-2": "fail",
		"any/sw-1": "pass", "any/sw-2": "pass",
		"not/sw-1": "fail", "not/sw-2": "pass",
		"os/sw-1": "pass", "os/sw-2": "fail",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

// TestAppliesWhen: a false applies_when and a CI type that does not match
// are not_applicable.
func TestAppliesWhen(t *testing.T) {
	items := []*ci.Item{
		{Name: "prod", CITypeID: "t-server", Status: "active", Attributes: map[string]any{"env": "prod", "backup": true}},
		{Name: "lab", CITypeID: "t-server", Status: "active", Attributes: map[string]any{"env": "lab"}},
		{Name: "sw", CITypeID: "t-switch", Status: "active", Attributes: map[string]any{"env": "prod"}},
	}
	got := evaluate(t, NewMemoryRepository(), items, &Rule{Name: "backup", CITypeID: "t-server",
		AppliesWhen: JSONMap{"op": "eq", "field": "attributes.env", "value": "prod"},
		Expression:  JSONMap{"op": "eq", "field": "attributes.backup", "value": true}})
	for key, want := range map[string]string{"backup/prod": "pass", "backup/lab": "not_applicable", "backup/sw": "not_applicable"} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

// TestFindingInputs: two open vulnerabilities and a resolved one count 2;
// the highest severity comes from the open findings; compliance_violation
// is no input.
func TestFindingInputs(t *testing.T) {
	ctx := context.Background()
	findings := security.NewMemoryRepository()
	repo := NewMemoryRepository().WithFindings(findings)
	cis := ci.NewMemoryRepository()
	item := &ci.Item{OrganizationID: "org-1", Name: "srv", CITypeID: "t-server", Status: "active"}
	if err := cis.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*security.Finding{
		{Kind: "vulnerability", Severity: "medium", Status: "open", Reference: "CVE-1"},
		{Kind: "vulnerability", Severity: "high", Status: "acknowledged", Reference: "CVE-2"},
		{Kind: "vulnerability", Severity: "critical", Status: "resolved", Reference: "CVE-3"},
		{Kind: "compliance_violation", Severity: "critical", Status: "open", Reference: "rule-1"},
	} {
		f.OrganizationID, f.CIID, f.Title, f.Source = "org-1", item.ID, f.Reference, "manual"
		if err := findings.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := repo.FindingFacts(ctx, "org-1", []string{item.ID})
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs(item, facts[item.ID])
	if in["finding.open_count.vulnerability"] != 2 || in["finding.max_open_severity"] != "high" || in["finding.max_open_severity_rank"] != 3 {
		t.Errorf("finding inputs: count %v max %v rank %v, want 2 high 3", in["finding.open_count.vulnerability"],
			in["finding.max_open_severity"], in["finding.max_open_severity_rank"])
	}
	if in["finding.open_count.missing_patch"] != 0 {
		t.Errorf("open_count of a kind without findings: %v, want 0", in["finding.open_count.missing_patch"])
	}
	if _, ok := in["finding.open_count.compliance_violation"]; ok {
		t.Error("compliance_violation must be no input")
	}
	if verr := ValidateCondition("expression", JSONMap{"op": "eq", "field": "finding.open_count.compliance_violation", "value": 0}); verr == nil {
		t.Error("a rule on compliance_violation must be refused")
	}
	// The evaluator reads the same facts.
	rule := &Rule{OrganizationID: "org-1", Name: "no-high", Severity: "high", Category: "ISO27001", Active: true,
		Expression: JSONMap{"op": "lt", "field": "finding.max_open_severity_rank", "value": 3}}
	if err = repo.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	resp, err := NewEvaluator(repo, cis).Evaluate(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Status != "fail" {
		t.Errorf("rule on the open severity: %+v, want fail", resp.Results)
	}
	none := Inputs(&ci.Item{}, FindingFacts{})
	if none["finding.max_open_severity"] != "none" || none["finding.max_open_severity_rank"] != 0 {
		t.Errorf("CI without findings: %v %v, want none 0", none["finding.max_open_severity"], none["finding.max_open_severity_rank"])
	}
}

// TestLifecycleStateInput: lifecycle_state is an input.
func TestLifecycleStateInput(t *testing.T) {
	items := []*ci.Item{
		{Name: "old", CITypeID: "t-app", Status: "active", LifecycleState: "end_of_life"},
		{Name: "new", CITypeID: "t-app", Status: "active", LifecycleState: "production"},
	}
	got := evaluate(t, NewMemoryRepository(), items, &Rule{Name: "supported",
		Expression: JSONMap{"op": "not_in", "field": "lifecycle_state", "value": []any{"end_of_life", "retired"}}})
	if got["supported/old"] != "fail" || got["supported/new"] != "pass" {
		t.Errorf("lifecycle rule: %v", got)
	}
}

// TestValidateConditionInputs: the path of a refused expression names the
// failing node.
func TestValidateConditionInputs(t *testing.T) {
	for _, c := range []struct {
		node  JSONMap
		field string
	}{
		{JSONMap{"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "name", "value": "a"}, map[string]any{"op": "eq", "value": "b"}}}, "expression.rules[1].field"},
		{JSONMap{"op": "matches", "field": "name", "value": "^a"}, "expression.op"},
		{JSONMap{"op": "any", "rules": []any{map[string]any{"op": "eq", "field": "hostname", "value": "a"}}}, "expression.rules[0].field"},
		{JSONMap{"op": "eq", "field": "finding.open_count.unknown", "value": 1}, "expression.field"},
	} {
		err := ValidateCondition("expression", c.node)
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Status != http.StatusUnprocessableEntity || fe.Field != c.field {
			t.Errorf("%v: %v, want 422 on %s", c.node, err, c.field)
		}
	}
	for _, ok := range []JSONMap{
		{"op": "eq", "field": "attributes.x", "value": 1},
		{"op": "gte", "field": "finding.max_open_severity_rank", "value": 2},
		{"op": "eq", "field": "finding.open_count.vulnerability", "value": 0},
		{"op": "in", "field": "lifecycle_state", "value": []any{"a"}},
	} {
		if err := ValidateCondition("expression", ok); err != nil {
			t.Errorf("%v refused: %v", ok, err)
		}
	}
}
