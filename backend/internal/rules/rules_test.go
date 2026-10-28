package rules

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
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
	if len(c.Cases) == 0 || len(c.InvalidRules) == 0 {
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
}

// TestSchemaMatchesOperators keeps api/rules.schema.json and the Go
// operators in step.
func TestSchemaMatchesOperators(t *testing.T) {
	raw, err := os.ReadFile("../../../api/rules.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Schema string `json:"$schema"`
		Defs   map[string]struct {
			Properties map[string]struct {
				Const string `json:"const"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q", schema.Schema)
	}
	var ops []string
	for name, def := range schema.Defs {
		if op := def.Properties["op"].Const; op != "" {
			if op != name {
				t.Errorf("$defs/%s defines op %q", name, op)
			}
			ops = append(ops, op)
		}
	}
	sort.Strings(ops)
	want := slices.Clone(Ops)
	sort.Strings(want)
	if !slices.Equal(ops, want) {
		t.Errorf("schema operators %v, Go operators %v", ops, want)
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
