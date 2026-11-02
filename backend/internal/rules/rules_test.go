package rules

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

type conformance struct {
	Cases []struct {
		Name       string          `json:"name"`
		Rule       json.RawMessage `json:"rule"`
		Value      json.RawMessage `json:"value"`
		Absent     bool            `json:"absent"`
		Violations []string        `json:"violations"`
	} `json:"cases"`
	InvalidRules []struct {
		Name string          `json:"name"`
		Rule json.RawMessage `json:"rule"`
	} `json:"invalid_rules"`
	ConditionCases []struct {
		Name      string          `json:"name"`
		Condition json.RawMessage `json:"condition"`
		Values    map[string]any  `json:"values"`
		Result    bool            `json:"result"`
	} `json:"condition_cases"`
	InvalidConditions []struct {
		Name      string          `json:"name"`
		Condition json.RawMessage `json:"condition"`
		Path      string          `json:"path"`
	} `json:"invalid_conditions"`
}

func loadConformance(t *testing.T) conformance {
	t.Helper()
	raw, err := os.ReadFile("../../../api/rules.conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var c conformance
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) == 0 || len(c.InvalidRules) == 0 || len(c.ConditionCases) == 0 || len(c.InvalidConditions) == 0 {
		t.Fatal("conformance file has no cases")
	}
	return c
}

// TestConformance runs the shared cases of api/rules.conformance.json
// (MET-45); frontend/src/rules/rules.test.ts runs the same file.
func TestConformance(t *testing.T) {
	c := loadConformance(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			rule, err := Parse(tc.Rule)
			if err != nil {
				t.Fatalf("valid rule rejected: %v", err)
			}
			var value any
			if len(tc.Value) > 0 {
				if err := json.Unmarshal(tc.Value, &value); err != nil {
					t.Fatal(err)
				}
			}
			got := []string{}
			for _, v := range Evaluate(&rule, value, !tc.Absent) {
				got = append(got, v.Code)
			}
			want := tc.Violations
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("violations = %v, want %v", got, want)
			}
		})
	}
	for _, tc := range c.InvalidRules {
		t.Run(tc.Name, func(t *testing.T) {
			if _, err := Parse(tc.Rule); err == nil {
				t.Error("invalid rule accepted")
			}
		})
	}
	for _, tc := range c.ConditionCases {
		t.Run(tc.Name, func(t *testing.T) {
			cond, err := ParseCondition(tc.Condition)
			if err != nil {
				t.Fatalf("valid condition rejected: %v", err)
			}
			if got := EvaluateCondition(&cond, tc.Values); got != tc.Result {
				t.Errorf("result = %v, want %v", got, tc.Result)
			}
			// A condition is no value rule (WP-715).
			if _, err := Parse(tc.Condition); err == nil && cond.Op != OpAll {
				t.Error("condition accepted as value rule")
			}
		})
	}
	for _, tc := range c.InvalidConditions {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := ParseCondition(tc.Condition)
			if err == nil {
				t.Fatal("invalid condition accepted")
			}
			if !strings.HasPrefix(err.Error(), tc.Path+": ") {
				t.Errorf("error %q does not name the path %s", err, tc.Path)
			}
		})
	}
}

// TestConditionCoversEveryOperator: condition_cases have at least one case
// per comparison and logic operator (WP-715).
func TestConditionCoversEveryOperator(t *testing.T) {
	seen := map[string]bool{}
	var walk func(r *Rule)
	walk = func(r *Rule) {
		seen[r.Op] = true
		for i := range r.Rules {
			walk(&r.Rules[i])
		}
	}
	for _, tc := range loadConformance(t).ConditionCases {
		cond, err := ParseCondition(tc.Condition)
		if err != nil {
			t.Fatalf("%s: %v", tc.Name, err)
		}
		walk(&cond)
	}
	for _, op := range slices.Concat(LogicOps, CompareOps) {
		if !seen[op] {
			t.Errorf("no condition case uses %s", op)
		}
	}
}

// TestValueRulesRejectConditionNodes: comparisons, any and not are no
// value rules; the value operators are no conditions (WP-715).
func TestValueRulesRejectConditionNodes(t *testing.T) {
	for _, raw := range []string{
		`{"op":"eq","field":"env","value":"prod"}`,
		`{"op":"empty","field":"env"}`,
		`{"op":"any","rules":[]}`,
		`{"op":"not","rules":[{"op":"required"}]}`,
		`{"op":"all","rules":[{"op":"required","field":"env"}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s accepted as value rule", raw)
		}
	}
	for _, op := range ValueOps {
		if _, err := ParseCondition([]byte(`{"op":"` + op + `"}`)); err == nil {
			t.Errorf("%s accepted as condition", op)
		}
	}
}

// TestConditionGoValues: EvaluateCondition also takes Go values that are
// not decoded JSON (ints, []string), as stored attributes may hold.
func TestConditionGoValues(t *testing.T) {
	cond := Rule{Op: OpAll, Rules: []Rule{
		{Op: OpGte, Field: "cpu_count", Value: 4},
		{Op: OpContains, Field: "disks", Value: "ssd"},
		{Op: OpIn, Field: "env", Value: []string{"prod", "test"}},
		{Op: OpEq, Field: "big", Value: 1e21},
	}}
	values := map[string]any{"cpu_count": int64(4), "disks": []string{"ssd"}, "env": "test", "big": "1e21"}
	if !EvaluateCondition(&cond, values) {
		t.Error("condition does not hold for Go values")
	}
	for v, want := range map[float64]string{0: "0", 1: "1", -2.5: "-2.5", 1e21: "1e+21", 1.5e-7: "1.5e-7", 123456789: "123456789"} {
		if got, _ := text(v); got != want {
			t.Errorf("text(%v) = %q, want %q", v, got, want)
		}
	}
}

// TestSchemaMatchesOperators keeps api/rules.schema.json and the Go
// operators in step: the value rules ($defs/rule) use all and ValueOps,
// the conditions ($defs/condition) all, any, not and CompareOps.
func TestSchemaMatchesOperators(t *testing.T) {
	raw, err := os.ReadFile("../../../api/rules.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	type def struct {
		OneOf []struct {
			Ref string `json:"$ref"`
		} `json:"oneOf"`
		Properties map[string]struct {
			Const string   `json:"const"`
			Enum  []string `json:"enum"`
		} `json:"properties"`
	}
	var schema struct {
		Schema string         `json:"$schema"`
		Ref    string         `json:"$ref"`
		Defs   map[string]def `json:"$defs"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q", schema.Schema)
	}
	if schema.Ref != "#/$defs/rule" {
		t.Errorf("schema root = %q, want the value rule", schema.Ref)
	}
	// operators of the alternatives of a oneOf definition.
	operators := func(union string) []string {
		var ops []string
		for _, alt := range schema.Defs[union].OneOf {
			name := strings.TrimPrefix(alt.Ref, "#/$defs/")
			op := schema.Defs[name].Properties["op"]
			if op.Const != "" {
				if op.Const != name && union+"_"+op.Const != name {
					t.Errorf("$defs/%s defines op %q", name, op.Const)
				}
				ops = append(ops, op.Const)
			}
			ops = append(ops, op.Enum...)
		}
		sort.Strings(ops)
		return ops
	}
	for union, want := range map[string][]string{
		"rule":      slices.Concat([]string{OpAll}, ValueOps),
		"condition": slices.Concat(LogicOps, CompareOps),
	} {
		want = slices.Clone(want)
		sort.Strings(want)
		if got := operators(union); !slices.Equal(got, want) {
			t.Errorf("$defs/%s operators %v, Go operators %v", union, got, want)
		}
	}
	all := slices.Clone(Ops)
	sort.Strings(all)
	if !slices.Equal(all, slices.Compact(slices.Sorted(slices.Values(slices.Concat(operators("rule"), operators("condition")))))) {
		t.Errorf("Ops %v are not the operators of the schema", Ops)
	}
}

// TestMarshalAll covers WP-589: a stored rule keeps the form of
// api/rules.schema.json, all always with its rules.
func TestMarshalAll(t *testing.T) {
	for _, tc := range []struct {
		rule Rule
		want string
	}{
		{Rule{Op: OpAll}, `{"op":"all","rules":[]}`},
		{Rule{Op: OpAll, Rules: []Rule{{Op: OpRequired}}}, `{"op":"all","rules":[{"op":"required"}]}`},
		{Rule{Op: OpRange, Of: OfLength, Min: new(float64)}, `{"op":"range","of":"length","min":0}`},
	} {
		got, err := json.Marshal(tc.rule)
		if err != nil || string(got) != tc.want {
			t.Errorf("%+v = %s (%v), want %s", tc.rule, got, err, tc.want)
		}
		parsed, err := Parse(got)
		if err != nil {
			t.Errorf("%s: %v", got, err)
		}
		if again, _ := json.Marshal(parsed); string(again) != tc.want {
			t.Errorf("round trip %s, want %s", again, tc.want)
		}
	}
}

// TestMarshalCondition: a condition keeps the form of $defs/condition,
// logic nodes always with their rules, a comparison with value 0 or false.
func TestMarshalCondition(t *testing.T) {
	for _, tc := range []struct {
		cond Rule
		want string
	}{
		{Rule{Op: OpAny}, `{"op":"any","rules":[]}`},
		{Rule{Op: OpNot, Rules: []Rule{{Op: OpEmpty, Field: "owner"}}}, `{"op":"not","rules":[{"op":"empty","field":"owner"}]}`},
		{Rule{Op: OpEq, Field: "managed", Value: false}, `{"op":"eq","field":"managed","value":false}`},
		{Rule{Op: OpLt, Field: "ram_gb", Value: 0.0}, `{"op":"lt","field":"ram_gb","value":0}`},
	} {
		got, err := json.Marshal(tc.cond)
		if err != nil || string(got) != tc.want {
			t.Errorf("%+v = %s (%v), want %s", tc.cond, got, err, tc.want)
		}
		parsed, err := ParseCondition(got)
		if err != nil {
			t.Errorf("%s: %v", got, err)
		}
		if again, _ := json.Marshal(parsed); string(again) != tc.want {
			t.Errorf("round trip %s, want %s", again, tc.want)
		}
	}
}
