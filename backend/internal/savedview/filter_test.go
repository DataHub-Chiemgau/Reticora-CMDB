package savedview

import (
	"strings"
	"testing"
)

func TestCompileSQLEmptySpec(t *testing.T) {
	where, args, err := FilterSpec{}.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if where != "true" {
		t.Fatalf("expected neutral where, got %q", where)
	}
	if len(args) != 1 {
		t.Fatalf("expected args unchanged, got %v", args)
	}
}

func TestCompileSQLParameterizesValues(t *testing.T) {
	spec := FilterSpec{
		EntityKind: "ci",
		Search:     "srv'; DROP TABLE ci; --",
		CIType:     "server",
		Lifecycle:  "deployed",
		Attributes: map[string]any{"environment": "production"},
	}
	where, args, err := spec.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// No user value may appear verbatim in the SQL text — values are
	// parameterized ($N) and the parameter counter advances per value.
	if strings.Contains(where, "srv'") || strings.Contains(where, "production") || strings.Contains(where, "DROP") {
		t.Fatalf("user input leaked into SQL text: %s", where)
	}
	if !strings.Contains(where, "ci.name ILIKE $2") {
		t.Fatalf("expected search predicate, got %s", where)
	}
	if !strings.Contains(where, "ci_type_id IN") {
		t.Fatalf("expected ci_type subquery, got %s", where)
	}
	if !strings.Contains(where, "lifecycle_state =") {
		t.Fatalf("expected lifecycle predicate, got %s", where)
	}
	if !strings.Contains(where, "attributes->>") {
		t.Fatalf("expected JSONB attribute predicate, got %s", where)
	}
	// org + search + ci_type(1) + lifecycle + attr key/value = 6 args.
	if len(args) != 1+1+1+1+2 {
		t.Fatalf("expected 6 args, got %d (%v)", len(args), args)
	}
}

func TestCompileSQLAssetKindUsesCustomFields(t *testing.T) {
	spec := FilterSpec{EntityKind: "asset", Attributes: map[string]any{"cost_center": "CC-1"}}
	where, _, err := spec.CompileSQL("asset", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "custom_fields->>") {
		t.Fatalf("assets must query custom_fields, got %s", where)
	}
	if strings.Contains(where, "attributes->>") {
		t.Fatalf("asset kind must not reference ci.attributes, got %s", where)
	}
}

func TestCompileSQLRelationshipPredicates(t *testing.T) {
	spec := FilterSpec{EntityKind: "ci", LacksRelationship: "backed_up_by", UpstreamOf: "00000000-0000-0000-0000-0000000000aa"}
	where, _, err := spec.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "NOT EXISTS (SELECT 1 FROM ci_relationship") {
		t.Fatalf("expected lacks-relationship predicate, got %s", where)
	}
	if !strings.Contains(where, "WITH RECURSIVE up AS") {
		t.Fatalf("expected upstream recursive predicate, got %s", where)
	}
}

func TestCompileSQLAssetLifecycleAndOwner(t *testing.T) {
	falseVal := false
	spec := FilterSpec{EntityKind: "asset", Lifecycle: "deployed", HasOwner: &falseVal, WarrantyWithinDays: 90}
	where, _, err := spec.CompileSQL("asset", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "asset.lifecycle_state =") {
		t.Fatalf("expected asset lifecycle predicate, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS (SELECT 1 FROM assignment") {
		t.Fatalf("expected has_owner=false predicate, got %s", where)
	}
	if !strings.Contains(where, "warranty_end <= CURRENT_DATE") {
		t.Fatalf("expected warranty predicate, got %s", where)
	}
}

func TestCompileSQLReconciliationConflict(t *testing.T) {
	spec := FilterSpec{EntityKind: "ci", ReconciliationConflict: true}
	where, _, err := spec.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "ci_field_value") || !strings.Contains(where, "IS DISTINCT FROM") {
		t.Fatalf("expected reconciliation conflict predicate, got %s", where)
	}
}
