package workflow

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Workflow conditions follow MET-45 (WFL-02, WP-1087, E-229 option 1).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestEvaluationValuesFlattensPaths|TestConditionsAllAnyNot|TestLegacyOperatorsMapping|TestValidateDefinitionPaths' ./internal/workflow/

// runContext is the context of a run started by a lifecycle event of a CI.
func runContext() JSONMap {
	return JSONMap{
		"event": map[string]any{"type": "ci.lifecycle_changed", "entity_id": "ci-1"},
		"data":  map[string]any{"to_state": "retired", "from_state": "active"},
		"ci": map[string]any{
			"status":     "active",
			"tags":       []any{"core", "dc1"},
			"attributes": map[string]any{"rack_units": float64(2), "deep": map[string]any{"a": map[string]any{"b": "c"}}},
		},
		"note": "plain",
	}
}

// TestEvaluationValuesFlattensPaths: the run context becomes the flat map
// with dot paths of rules.EvaluateCondition; lists stay a value, nesting
// ends at depth 4.
func TestEvaluationValuesFlattensPaths(t *testing.T) {
	v := EvaluationValues(runContext())
	for path, want := range map[string]any{
		"ci.attributes.rack_units": float64(2),
		"data.to_state":            "retired",
		"event.type":               "ci.lifecycle_changed",
		"event.entity_id":          "ci-1",
		"ci.status":                "active",
		"note":                     "plain",
		"ci.tags":                  []any{"core", "dc1"},
		"ci.attributes.deep.a":     map[string]any{"b": "c"},
	} {
		if got, ok := v[path]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v (present %v), want %v", path, got, ok, want)
		}
	}
	if _, ok := v["ci.attributes.deep.a.b"]; ok {
		t.Error("ci.attributes.deep.a.b is below depth 4 and must not be flattened")
	}
	if _, ok := v["ci"]; !ok {
		t.Error("an object is a value of its own path as well")
	}
}

// TestConditionsAllAnyNot: the list is combined with all, nodes use all,
// any and not of MET-45, an empty list holds.
func TestConditionsAllAnyNot(t *testing.T) {
	ctx := runContext()
	cases := []struct {
		name       string
		conditions []JSONMap
		want       bool
	}{
		{"empty list", nil, true},
		{"eq", []JSONMap{{"op": "eq", "field": "data.to_state", "value": "retired"}}, true},
		{"list is all", []JSONMap{{"op": "eq", "field": "data.to_state", "value": "retired"}, {"op": "eq", "field": "ci.status", "value": "retired"}}, false},
		{"any", []JSONMap{{"op": "any", "rules": []any{
			map[string]any{"op": "eq", "field": "ci.status", "value": "retired"},
			map[string]any{"op": "gte", "field": "ci.attributes.rack_units", "value": 2},
		}}}, true},
		{"not", []JSONMap{{"op": "not", "rules": []any{map[string]any{"op": "in", "field": "event.type", "value": []any{"ci.created", "ci.lifecycle_changed"}}}}}, false},
		{"all nested", []JSONMap{{"op": "all", "rules": []any{
			map[string]any{"op": "contains", "field": "ci.tags", "value": "dc1"},
			map[string]any{"op": "empty", "field": "data.missing"},
			map[string]any{"op": "not_empty", "field": "event.entity_id"},
		}}}, true},
	}
	for _, c := range cases {
		got, err := conditionsHold(c.conditions, ctx)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if _, err := conditionsHold([]JSONMap{{"op": "matches", "field": "note", "value": ".*"}}, ctx); !errors.Is(err, errConditionInvalid) {
		t.Errorf("stored legacy matches: %v, want errConditionInvalid", err)
	}
}

// TestLegacyOperatorsMapping: the rule of the data migration maps exists
// to not_empty and takes eq, ne, contains and not_empty 1:1; matches and
// unknown operators are refused (the definition is deactivated).
func TestLegacyOperatorsMapping(t *testing.T) {
	cases := []struct {
		in   JSONMap
		want JSONMap
	}{
		{JSONMap{"field": "ci.status", "op": "exists"}, JSONMap{"op": "not_empty", "field": "ci.status"}},
		{JSONMap{"field": "ci.status", "op": "not_empty", "value": "x"}, JSONMap{"op": "not_empty", "field": "ci.status"}},
		{JSONMap{"field": "ci.status", "op": "eq", "value": "active"}, JSONMap{"op": "eq", "field": "ci.status", "value": "active"}},
		{JSONMap{"field": "ci.status", "value": "active"}, JSONMap{"op": "eq", "field": "ci.status", "value": "active"}},
		{JSONMap{"field": "ci.status", "op": "ne", "value": "retired"}, JSONMap{"op": "ne", "field": "ci.status", "value": "retired"}},
		{JSONMap{"field": "name", "op": "contains", "value": "sw"}, JSONMap{"op": "contains", "field": "name", "value": "sw"}},
		{JSONMap{"field": "name", "op": "matches", "value": "^sw"}, nil},
		{JSONMap{"field": "name", "op": "regex", "value": "^sw"}, nil},
		{JSONMap{"op": "eq", "value": "x"}, nil},
		// eq, ne and contains without a scalar value have no MET-45 form.
		{JSONMap{"field": "ci.status", "op": "eq"}, nil},
		{JSONMap{"field": "ci.status", "op": "ne", "value": nil}, nil},
		{JSONMap{"field": "ci.status", "op": "contains", "value": []any{"a"}}, nil},
		// A node already in MET-45 form stays (second run of 000330).
		{JSONMap{"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "a", "value": 1}}}, JSONMap{"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "a", "value": 1}}}},
		{JSONMap{"op": "lt", "field": "ci.attributes.rack_units", "value": 4}, JSONMap{"op": "lt", "field": "ci.attributes.rack_units", "value": 4}},
		{JSONMap{"op": "in", "field": "ci.status", "value": []any{"active"}}, JSONMap{"op": "in", "field": "ci.status", "value": []any{"active"}}},
		{JSONMap{"op": "empty", "field": "ci.owner"}, JSONMap{"op": "empty", "field": "ci.owner"}},
		{JSONMap{"op": "gt", "field": "n", "value": "x"}, nil},
	}
	for _, c := range cases {
		got, ok := MigrateLegacyCondition(c.in)
		if (c.want == nil) == ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("MigrateLegacyCondition(%v) = %v %v, want %v", c.in, got, ok, c.want)
		}
		if ok {
			if _, err := parseCondition(map[string]any(got)); err != nil {
				t.Errorf("migrated %v is no MET-45 condition: %v", got, err)
			}
		}
	}
	// The G1 operator matches is no MET-45 condition (pattern is a value rule).
	if _, err := parseCondition(map[string]any{"op": "matches", "field": "name", "value": "^sw"}); err == nil {
		t.Error("matches must be refused by rules.ParseCondition")
	}
	if _, err := parseCondition(map[string]any{"op": "exists", "field": "name"}); err == nil {
		t.Error("exists must be refused by rules.ParseCondition")
	}
}

// TestValidateDefinitionPaths: a refused node is a 422 on conditions[i]
// or trigger.filter with the path of the parser message.
func TestValidateDefinitionPaths(t *testing.T) {
	cases := []struct {
		trigger    JSONMap
		conditions []JSONMap
		field      string
	}{
		{JSONMap{"type": "manual"}, []JSONMap{{"op": "eq", "field": "a", "value": 1}, {"op": "bogus", "field": "a"}}, "conditions[1].op"},
		{JSONMap{"type": "manual"}, []JSONMap{{"op": "all", "rules": []any{map[string]any{"op": "eq", "value": 1}}}}, "conditions[0].rules[0].field"},
		{JSONMap{"type": "event", "event": "ci.created", "filter": map[string]any{"op": rules.OpPattern, "pattern": "x"}}, nil, "trigger.filter.op"},
		{JSONMap{"type": "event", "event": "ci.created", "filter": "nope"}, nil, "trigger.filter"},
	}
	for _, c := range cases {
		err := ValidateDefinition(c.trigger, c.conditions)
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != c.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("ValidateDefinition(%v, %v) = %v, want 422 on %s", c.trigger, c.conditions, err, c.field)
		}
	}
	if err := ValidateDefinition(JSONMap{"type": "event", "event": "ci.created", "filter": map[string]any{"op": "eq", "field": "data.kind", "value": "server"}},
		[]JSONMap{{"op": "not_empty", "field": "ci.status"}}); err != nil {
		t.Errorf("valid definition refused: %v", err)
	}
}
