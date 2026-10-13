package savedview

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if !strings.Contains(where, "WITH RECURSIVE up (id, depth) AS") || !strings.Contains(where, "up.depth < 5") {
		t.Fatalf("expected upstream recursive predicate bounded to depth 5, got %s", where)
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

// The shipped "Available laptops in Berlin" preset filters by location name,
// but FilterSpec only understood location IDs, so the preset was rejected with
// "unknown field location_search" every time a user ran it.
func TestPresetsCompile(t *testing.T) {
	for _, p := range Presets() {
		raw, err := json.Marshal(p.FilterSpec)
		if err != nil {
			t.Fatal(err)
		}
		// Decode exactly as the Query handler does: unknown fields are
		// rejected, which is how the broken preset produced a 400.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var spec FilterSpec
		if err := dec.Decode(&spec); err != nil {
			t.Fatalf("preset %q has a filter_spec the DSL cannot decode: %v", p.Name, err)
		}
		table := "ci"
		if spec.EntityKind == "asset" || p.EntityKind == "asset" {
			table = "asset"
		}
		if _, _, err := spec.CompileSQL(table, []any{"org"}, 2); err != nil {
			t.Fatalf("preset %q does not compile: %v", p.Name, err)
		}
	}
}

func TestCompileSQLLocationSearchMatchesSubtree(t *testing.T) {
	spec := FilterSpec{LocationSearch: "Berlin'; DROP TABLE asset; --"}
	where, args, err := spec.CompileSQL("asset", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// The value must be parameterized, never inlined.
	if strings.Contains(where, "Berlin") || strings.Contains(where, "DROP") {
		t.Fatalf("location value leaked into SQL text: %s", where)
	}
	if !strings.Contains(where, "name ILIKE $2") {
		t.Fatalf("expected a name match predicate, got %s", where)
	}
	// Descendants must be included, so "Berlin" also finds racks inside it:
	// the subtree of the canonical location tree by ltree path.
	if !strings.Contains(where, "FROM location l") || !strings.Contains(where, "l.path <@ ANY") {
		t.Fatalf("expected a location subtree by path, got %s", where)
	}
	if len(args) != 2 {
		t.Fatalf("expected org + location args, got %v", args)
	}
	if got, ok := args[1].(string); !ok || !strings.HasPrefix(got, "%") {
		t.Fatalf("expected a wrapped ILIKE pattern, got %v", args[1])
	}
}

func mustAST(t *testing.T, raw string) *Expr {
	t.Helper()
	var e Expr
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	return &e
}

// TestFilterASTCompilesAndOrNot covers SRC-03 (WP-187): and, or and not
// nest into SQL, values are parameters, fields come from the whitelist with
// the casts of their kind.
func TestFilterASTCompilesAndOrNot(t *testing.T) {
	ast := mustAST(t, `{"and": [
		{"or": [{"field": "status", "op": "eq", "value": "active"}, {"field": "status", "op": "eq", "value": "maintenance"}]},
		{"not": {"field": "name", "op": "like", "value": "test*"}},
		{"field": "attributes.ram_gb", "op": "gte", "value": 16},
		{"field": "asset.warranty_end", "op": "lt", "value": "2027-01-01"},
		{"field": "ci_type", "op": "in", "value": ["server", "switch"]},
		{"field": "created_at", "op": "gt", "value": "x'); DROP TABLE ci; --"}
	]}`)
	if err := ast.Validate("ci"); err != nil {
		t.Fatal(err)
	}
	sql, args, next, err := ast.CompileSQL("ci", "ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"((ci.status = $2 OR ci.status = $3) AND (NOT COALESCE(ci.name ILIKE $4, false)) AND (ci.attributes->>$5)::numeric >= $6::numeric",
		"EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND (sa.warranty_end)::timestamptz < $7::timestamptz)",
		"(SELECT COALESCE(t.key, t.name) FROM ci_type t WHERE t.id = ci.ci_type_id) = ANY($8::text[])",
		"(ci.created_at)::timestamptz > $9::timestamptz)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "DROP") || next != 10 || len(args) != 9 || args[3] != "test%" || args[4] != "ram_gb" {
		t.Errorf("args %v, next %d", args, next)
	}
	// The AST joins the flat fields of the spec.
	where, _, err := FilterSpec{Status: "active", Where: mustAST(t, `{"field": "hostname", "op": "exists"}`)}.CompileSQL("ci", []any{"org"}, 2)
	if err != nil || where != "ci.status = $2 AND ci.hostname IS NOT NULL" {
		t.Errorf("spec with where: %q %v", where, err)
	}
}

// TestFilterASTValidation: unknown fields and operators, wrong operators
// for a kind, mixed nodes and oversized trees are refused.
func TestFilterASTValidation(t *testing.T) {
	deep := `{"field": "name", "op": "eq", "value": "x"}`
	for i := 0; i < MaxExprDepth; i++ {
		deep = `{"not": ` + deep + `}`
	}
	for name, c := range map[string]struct{ kind, raw string }{
		"unknown field":         {"ci", `{"field": "password", "op": "eq", "value": "x"}`},
		"asset field on assets": {"asset", `{"field": "asset.status", "op": "eq", "value": "x"}`},
		"bad attribute key":     {"ci", `{"field": "attributes.a'b", "op": "eq", "value": "x"}`},
		"unknown operator":      {"ci", `{"field": "name", "op": "between", "value": "x"}`},
		"like on a time":        {"ci", `{"field": "created_at", "op": "like", "value": "2026"}`},
		"order on a uuid":       {"ci", `{"field": "client_id", "op": "lt", "value": "x"}`},
		"empty in":              {"ci", `{"field": "status", "op": "in", "value": []}`},
		"object value":          {"ci", `{"field": "status", "op": "eq", "value": {"a": 1}}`},
		"mixed node":            {"ci", `{"field": "name", "op": "eq", "value": "x", "not": {"field": "name", "op": "eq", "value": "y"}}`},
		"empty node":            {"ci", `{}`},
		"too deep":              {"ci", deep},
	} {
		err := mustAST(t, c.raw).Validate(c.kind)
		var astErr *ASTError
		if !errors.As(err, &astErr) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := mustAST(t, `{"field": "warranty_end", "op": "lte", "value": "2027-01-01"}`).Validate("asset"); err != nil {
		t.Errorf("asset field: %v", err)
	}
}
