package fieldmeta

import "testing"

func TestValidateDefinition(t *testing.T) {
	valid := FieldDefinition{Name: "rack", DataType: TypeText}
	if err := ValidateDefinition(valid); err != nil {
		t.Fatalf("expected valid definition, got %v", err)
	}

	if err := ValidateDefinition(FieldDefinition{Name: "", DataType: TypeText}); err == nil {
		t.Fatal("expected error for empty name")
	}
	if err := ValidateDefinition(FieldDefinition{Name: "x", DataType: "bogus"}); err == nil {
		t.Fatal("expected error for unknown type")
	}
	if err := ValidateDefinition(FieldDefinition{Name: "x", DataType: TypeAssetRef, ReferenceTarget: "bogus"}); err == nil {
		t.Fatal("expected error for unknown reference target")
	}
	if err := ValidateDefinition(FieldDefinition{
		Name: "x", DataType: TypeText,
		Conditional: &ConditionalRules{VisibleWhen: []Predicate{{Field: "kind", Op: "bogus"}}},
	}); err == nil {
		t.Fatal("expected error for unknown predicate operator")
	}
	// Legacy aliases from the migration 000002 CHECK constraint keep working.
	for _, legacy := range []string{"string", "number", "boolean", "date", "enum"} {
		if err := ValidateDefinition(FieldDefinition{Name: "x", DataType: legacy}); err != nil {
			t.Fatalf("expected legacy type %q to remain valid, got %v", legacy, err)
		}
	}
}

func TestEvaluateVisibility(t *testing.T) {
	def := FieldDefinition{
		Name: "rack", DataType: TypeText,
		Conditional: &ConditionalRules{
			VisibleWhen: []Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
		},
	}
	state := Evaluate(def, map[string]any{"server_kind": "physical"})
	if !state.Visible {
		t.Fatal("expected field visible for physical server")
	}
	state = Evaluate(def, map[string]any{"server_kind": "virtual"})
	if state.Visible {
		t.Fatal("expected field hidden for virtual server")
	}
	// Invisible fields are never required.
	hidden := Evaluate(FieldDefinition{
		Name: "rack", DataType: TypeText, Required: true,
		Conditional: &ConditionalRules{
			VisibleWhen: []Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
		},
	}, map[string]any{"server_kind": "virtual"})
	if hidden.Required {
		t.Fatal("expected hidden field to not be required")
	}
}

func TestEvaluateRequiredAndReadOnly(t *testing.T) {
	def := FieldDefinition{
		Name: "serial", DataType: TypeText,
		Conditional: &ConditionalRules{
			RequiredWhen: []Predicate{{Field: "kind", Op: "eq", Value: "physical"}},
			ReadOnlyWhen: []Predicate{{Field: "locked", Op: "eq", Value: true}},
		},
	}
	state := Evaluate(def, map[string]any{"kind": "physical"})
	if !state.Required {
		t.Fatal("expected conditional required to apply")
	}
	state = Evaluate(def, map[string]any{"kind": "virtual"})
	if state.Required {
		t.Fatal("expected conditional required to not apply")
	}
	state = Evaluate(def, map[string]any{"locked": true})
	if !state.ReadOnly {
		t.Fatal("expected conditional read-only to apply")
	}
}

func TestEvaluateAllowedValues(t *testing.T) {
	def := FieldDefinition{
		Name: "size", DataType: TypeEnum, EnumValues: []string{"s", "m", "l"},
		Conditional: &ConditionalRules{
			AllowedValuesWhen: &ConditionalValues{
				When:   []Predicate{{Field: "kind", Op: "eq", Value: "virtual"}},
				Values: []any{"s", "m"},
			},
		},
	}
	state := Evaluate(def, map[string]any{"kind": "virtual"})
	if len(state.AllowedValues) != 2 {
		t.Fatalf("expected 2 allowed values, got %v", state.AllowedValues)
	}
	state = Evaluate(def, map[string]any{"kind": "physical"})
	if len(state.AllowedValues) != 3 {
		t.Fatalf("expected all enum values, got %v", state.AllowedValues)
	}
}

func TestPredicateOperators(t *testing.T) {
	values := map[string]any{
		"n": 5.0, "s": "hello world", "list": []any{"a", "b"},
		"empty": "", "missing": nil,
	}
	cases := []struct {
		p    Predicate
		want bool
	}{
		{Predicate{Field: "n", Op: "gt", Value: 4}, true},
		{Predicate{Field: "n", Op: "gte", Value: 5}, true},
		{Predicate{Field: "n", Op: "lt", Value: 6}, true},
		{Predicate{Field: "n", Op: "lte", Value: 5}, true},
		{Predicate{Field: "n", Op: "eq", Value: 5}, true},
		{Predicate{Field: "n", Op: "ne", Value: 5}, false},
		{Predicate{Field: "n", Op: "in", Value: []any{4, 5}}, true},
		{Predicate{Field: "n", Op: "not_in", Value: []any{4, 5}}, false},
		{Predicate{Field: "s", Op: "contains", Value: "world"}, true},
		{Predicate{Field: "list", Op: "contains", Value: "a"}, true},
		{Predicate{Field: "empty", Op: "empty"}, true},
		{Predicate{Field: "missing", Op: "empty"}, true},
		{Predicate{Field: "s", Op: "empty"}, false},
		{Predicate{Field: "s", Op: "not_empty"}, true},
	}
	for i, tc := range cases {
		if got := match(tc.p, values[tc.p.Field]); got != tc.want {
			t.Errorf("case %d (%+v): got %v want %v", i, tc.p, got, tc.want)
		}
	}
}

func TestValidateValueTypes(t *testing.T) {
	if err := ValidateValue(FieldDefinition{Name: "ip", DataType: TypeIP}, "10.0.0.1"); err != nil {
		t.Fatalf("valid IP rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "ip", DataType: TypeIP}, "not-an-ip"); err == nil {
		t.Fatal("invalid IP accepted")
	}
	if err := ValidateValue(FieldDefinition{Name: "m", DataType: TypeMAC}, "aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("valid MAC rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "m", DataType: TypeMAC}, "xyz"); err == nil {
		t.Fatal("invalid MAC accepted")
	}
	if err := ValidateValue(FieldDefinition{Name: "e", DataType: TypeEmail}, "a@b.io"); err != nil {
		t.Fatalf("valid email rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "u", DataType: TypeURL}, "https://example.com/x"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "u", DataType: TypeURL}, "notaurl"); err == nil {
		t.Fatal("invalid URL accepted")
	}
	if err := ValidateValue(FieldDefinition{Name: "d", DataType: TypeDate}, "2026-09-02"); err != nil {
		t.Fatalf("valid date rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "d", DataType: TypeDate}, "02.09.2026"); err == nil {
		t.Fatal("invalid date accepted")
	}
	if err := ValidateValue(FieldDefinition{Name: "i", DataType: TypeInteger}, 42.0); err != nil {
		t.Fatalf("valid integer rejected: %v", err)
	}
	if err := ValidateValue(FieldDefinition{Name: "i", DataType: TypeInteger}, 42.5); err == nil {
		t.Fatal("fractional value accepted as integer")
	}
	// Empty values are valid; required enforcement happens at state level.
	if err := ValidateValue(FieldDefinition{Name: "x", DataType: TypeText, Required: true}, ""); err != nil {
		t.Fatalf("empty value should pass value validation: %v", err)
	}
}

func TestValidateValueRules(t *testing.T) {
	min, max := 1.0, 10.0
	def := FieldDefinition{
		Name: "cores", DataType: TypeInteger,
		Validation: &ValidationRules{Min: &min, Max: &max},
	}
	if err := ValidateValue(def, 5.0); err != nil {
		t.Fatalf("in-range value rejected: %v", err)
	}
	if err := ValidateValue(def, 20.0); err == nil {
		t.Fatal("out-of-range value accepted")
	}

	enumDef := FieldDefinition{Name: "os", DataType: TypeEnum, EnumValues: []string{"linux", "windows"}}
	if err := ValidateValue(enumDef, "linux"); err != nil {
		t.Fatalf("allowed enum rejected: %v", err)
	}
	if err := ValidateValue(enumDef, "plan9"); err == nil {
		t.Fatal("disallowed enum accepted")
	}

	multi := FieldDefinition{Name: "tags", DataType: TypeMultiEnum, EnumValues: []string{"a", "b"}}
	if err := ValidateValue(multi, []any{"a", "b"}); err != nil {
		t.Fatalf("allowed multi-enum rejected: %v", err)
	}
	if err := ValidateValue(multi, []any{"a", "c"}); err == nil {
		t.Fatal("disallowed multi-enum value accepted")
	}

	pat := FieldDefinition{Name: "code", DataType: TypeText, Validation: &ValidationRules{Pattern: "^[A-Z]{3}$"}}
	if err := ValidateValue(pat, "ABC"); err != nil {
		t.Fatalf("pattern-matching value rejected: %v", err)
	}
	if err := ValidateValue(pat, "abc"); err == nil {
		t.Fatal("pattern-violating value accepted")
	}
}
