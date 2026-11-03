package rules

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
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
	Expressions []struct {
		Name     string          `json:"name"`
		Expr     json.RawMessage `json:"expr"`
		Inputs   map[string]any  `json:"inputs"`
		Now      string          `json:"now"`
		Timezone string          `json:"timezone"`
		Result   json.RawMessage `json:"result"`
	} `json:"expressions"`
	InvalidExpressions []struct {
		Name string          `json:"name"`
		Expr json.RawMessage `json:"expr"`
		Path string          `json:"path"`
	} `json:"invalid_expressions"`
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
	if len(c.Cases) == 0 || len(c.InvalidRules) == 0 || len(c.ConditionCases) == 0 || len(c.InvalidConditions) == 0 ||
		len(c.Expressions) == 0 || len(c.InvalidExpressions) == 0 {
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
		// The expressions (WP-258) have operators of their own.
		"expr": ExprOps,
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
	for _, op := range ExprOps {
		if slices.Contains(Ops, op) {
			t.Errorf("expression operator %s is an operator of rules or conditions", op)
		}
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

// TestExpressionConformance runs expressions and invalid_expressions of
// api/rules.conformance.json (WP-258, MET-45): arithmetic, today and
// days_between with a fixed now and time zone (across midnight), strings,
// coalesce, missing inputs and division by 0; invalid expressions are
// refused naming the path. A valid expression survives a JSON round trip.
func TestExpressionConformance(t *testing.T) {
	c := loadConformance(t)
	for _, tc := range c.Expressions {
		t.Run(tc.Name, func(t *testing.T) {
			e, err := ParseExpr(tc.Expr)
			if err != nil {
				t.Fatalf("valid expression rejected: %v", err)
			}
			now, err := time.Parse(time.RFC3339, tc.Now)
			if err != nil {
				t.Fatal(err)
			}
			loc, err := time.LoadLocation(tc.Timezone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(EvalExpr(&e, tc.Inputs, now, loc))
			if err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err = json.Unmarshal(got, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(tc.Result, &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("result = %s, want %s", got, tc.Result)
			}
			again, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var sent, written any
			_ = json.Unmarshal(tc.Expr, &sent)
			_ = json.Unmarshal(again, &written)
			if !reflect.DeepEqual(sent, written) {
				t.Errorf("round trip %s, sent %s", again, tc.Expr)
			}
		})
	}
	for _, tc := range c.InvalidExpressions {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := ParseExpr(tc.Expr)
			if err == nil {
				t.Fatal("invalid expression accepted")
			}
			if !strings.HasPrefix(err.Error(), tc.Path+": ") {
				t.Errorf("error %q does not name the path %s", err, tc.Path)
			}
		})
	}
}

// TestExpressionCoversEveryOperator: expressions have at least one case per
// operator (WP-258).
func TestExpressionCoversEveryOperator(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range loadConformance(t).Expressions {
		e, err := ParseExpr(tc.Expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.Name, err)
		}
		e.walk(func(n *Expr) { seen[n.Op] = true })
	}
	for _, op := range ExprOps {
		if !seen[op] {
			t.Errorf("no expression case uses %s", op)
		}
	}
}

// TestExpressionTypesAndFields: the result type of an expression, its
// references and whether it reads the date; a Go time.Time counts with its
// date in the time zone, a nil location is UTC; a nesting deeper than the
// limit is refused.
func TestExpressionTypesAndFields(t *testing.T) {
	for raw, want := range map[string]string{
		`{"op":"days_between","args":[{"op":"today"},{"op":"field","field":"asset.warranty_end"}]}`: ResultNumber,
		`{"op":"concat","args":[{"op":"literal","value":1}]}`:                                       ResultString,
		`{"op":"today"}`: ResultDate,
		`{"op":"field","field":"asset.purchase_date"}`:                                       ResultDate,
		`{"op":"literal","value":true}`:                                                      ResultBoolean,
		`{"op":"field","field":"owner"}`:                                                     ResultAny,
		`{"op":"coalesce","args":[{"op":"field","field":"a"},{"op":"literal","value":"x"}]}`: ResultString,
	} {
		e, err := ParseExpr([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := e.ResultType(); got != want {
			t.Errorf("%s: type %s, want %s", raw, got, want)
		}
	}
	e, err := ParseExpr([]byte(`{"op":"add","args":[{"op":"field","field":"b"},{"op":"days_between","args":[
		{"op":"today"},{"op":"field","field":"asset.warranty_end"}]},{"op":"field","field":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Fields(); !slices.Equal(got, []string{"asset.warranty_end", "b"}) {
		t.Errorf("fields = %v", got)
	}
	if !e.UsesToday() {
		t.Error("today not found")
	}
	now := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	days := Expr{Op: ExprDaysBetween, Args: []Expr{{Op: ExprToday}, {Op: ExprField, Field: "end"}}}
	end := time.Date(2026, 3, 3, 23, 30, 0, 0, time.UTC)
	if got := EvalExpr(&days, map[string]any{"end": end}, now, berlin); got != float64(2) {
		t.Errorf("days in Berlin = %v, want 2", got)
	}
	if got := EvalExpr(&days, map[string]any{"end": end}, now, nil); got != float64(2) {
		t.Errorf("days in UTC = %v, want 2", got)
	}
	deep := `{"op":"field","field":"a"}`
	for range maxExprDepth + 1 {
		deep = `{"op":"lower","args":[` + deep + `]}`
	}
	if _, err := ParseExpr([]byte(deep)); err == nil {
		t.Error("expression nested too deep accepted")
	}
	var decoded Expr
	if err := json.Unmarshal([]byte(`{"op":"today","x":1}`), &decoded); err == nil {
		t.Error("UnmarshalJSON accepted an unknown property")
	}
	if !e.Equal(&e) || e.Equal(&days) {
		t.Error("Equal")
	}
}
