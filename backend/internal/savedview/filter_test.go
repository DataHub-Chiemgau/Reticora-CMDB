package savedview

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// TestCompileSQLRelationshipPredicatesReadTheProjection pins the SQL of
// has_relationship and lacks_relationship for the structural types (F-244,
// WP-573): contains, mounted_in and located_in are never stored in
// ci_relationship but projected by ci_structure_edge, so the predicate is
// the union of both, the projection read once per end of the edge with
// the other CI end checked for visibility; the type is one parameter.
// Other types read ci_relationship only.
func TestCompileSQLRelationshipPredicatesReadTheProjection(t *testing.T) {
	for _, relType := range []string{"contains", "mounted_in", "located_in"} {
		for _, lacks := range []bool{false, true} {
			spec := FilterSpec{EntityKind: "ci", HasRelationship: relType}
			if lacks {
				spec = FilterSpec{EntityKind: "ci", LacksRelationship: relType}
			}
			where, args, err := spec.CompileSQL("ci", []any{"org"}, 2)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "("
			if lacks {
				prefix = "NOT ("
			}
			for _, want := range []string{
				`EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = $2 AND (r.source_ci_id = ci.id OR r.target_ci_id = ci.id))`,
				`OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = $2 AND e.source_ci_id = ci.id`,
				`AND (e.target_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci t WHERE t.id = e.target_ci_id))`,
				`OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = $2 AND e.target_ci_id = ci.id`,
				`AND EXISTS (SELECT 1 FROM ci s WHERE s.id = e.source_ci_id)`,
			} {
				if !strings.Contains(where, want) {
					t.Errorf("%s (lacks %v): %q missing in %s", relType, lacks, want, where)
				}
			}
			if !strings.HasPrefix(where, prefix) || strings.Count(where, "ci_structure_edge") != 2 {
				t.Errorf("%s (lacks %v): want %q and two projection arms, got %s", relType, lacks, prefix, where)
			}
			if len(args) != 2 || args[1] != relType {
				t.Errorf("%s (lacks %v): args %v, want [org %s]", relType, lacks, args, relType)
			}
		}
	}
	where, args, err := FilterSpec{EntityKind: "ci", HasRelationship: "depends_on"}.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(where, "ci_structure_edge") || len(args) != 2 ||
		where != `EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = $2 AND (r.source_ci_id = ci.id OR r.target_ci_id = ci.id))` {
		t.Errorf("depends_on reads the stored relationships only: %s %v", where, args)
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
		{"field": "created_at", "op": "gt", "value": "2026-01-01T00:00:00Z"},
		{"field": "hostname", "op": "eq", "value": "x'); DROP TABLE ci; --"}
	]}`)
	if err := ast.Validate("ci"); err != nil {
		t.Fatal(err)
	}
	sql, args, next, err := ast.CompileSQL("ci", "ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"((ci.status = $2 OR ci.status = $3) AND (NOT COALESCE(ci.name ILIKE $4, false)) AND " +
			"CASE WHEN jsonb_typeof(ci.attributes->$5) = 'number' THEN (ci.attributes->>$5)::numeric END >= $6::numeric",
		"EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND (sa.warranty_end)::timestamptz < $7::timestamptz)",
		"(SELECT COALESCE(t.key, t.name) FROM ci_type t WHERE t.id = ci.ci_type_id) = ANY($8::text[])",
		"(ci.created_at)::timestamptz > $9::timestamptz",
		"ci.hostname = $10)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "DROP") || next != 11 || len(args) != 10 || args[3] != "test%" || args[4] != "ram_gb" {
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

// TestFilterASTValuesPerFieldKind covers F-179 (WP-405): Validate checks
// every value against the kind of its field (uuid, RFC 3339 time or date,
// number, bool, text), each element of in included, before any SQL runs,
// and names the refused value in the path; the valid values of every kind
// pass.
func TestFilterASTValuesPerFieldKind(t *testing.T) {
	const id = "00000000-0000-4000-8000-0000000000aa"
	for _, c := range []struct{ kind, raw, path string }{
		// uuid
		{"ci", `{"field": "client_id", "op": "eq", "value": "not-a-uuid"}`, ".value"},
		{"ci", `{"field": "site_id", "op": "ne", "value": 5}`, ".value"},
		{"ci", `{"field": "client_id", "op": "in", "value": ["` + id + `", "x"]}`, ".value[1]"},
		{"ci", `{"field": "asset.location_id", "op": "eq", "value": "{` + id + `}"}`, ".value"},
		// time
		{"ci", `{"field": "created_at", "op": "gt", "value": "garbage"}`, ".value"},
		{"ci", `{"field": "created_at", "op": "gt", "value": 2020}`, ".value"},
		{"ci", `{"field": "updated_at", "op": "lt", "value": "2024-02-30"}`, ".value"},
		{"ci", `{"field": "last_seen_at", "op": "gte", "value": "0000-01-01"}`, ".value"},
		{"ci", `{"field": "created_at", "op": "lte", "value": "2024-01-01T00:00:00+16:00"}`, ".value"},
		{"ci", `{"field": "created_at", "op": "lte", "value": "2024-01-01T00:00:00+00:60"}`, ".value"},
		{"ci", `{"field": "created_at", "op": "lte", "value": "2024-01-01T00:00:00,5Z"}`, ".value"},
		{"ci", `{"field": "created_at", "op": "eq", "value": "today"}`, ".value"},
		{"asset", `{"field": "purchase_date", "op": "in", "value": ["2024-01-01", "2024-13-01"]}`, ".value[1]"},
		// number
		{"asset", `{"field": "purchase_cost", "op": "gt", "value": "100"}`, ".value"},
		{"asset", `{"field": "purchase_cost", "op": "in", "value": [1, true]}`, ".value[1]"},
		{"ci", `{"field": "asset.purchase_cost", "op": "lte", "value": "1e3"}`, ".value"},
		// bool
		{"ci", `{"field": "is_manual", "op": "eq", "value": "yes"}`, ".value"},
		{"ci", `{"field": "is_manual", "op": "in", "value": [true, 1]}`, ".value[1]"},
		// text
		{"ci", `{"field": "name", "op": "eq", "value": 5}`, ".value"},
		{"ci", `{"field": "hostname", "op": "like", "value": "a\u0000b"}`, ".value"},
		{"ci", `{"field": "status", "op": "in", "value": ["active", {"a": 1}]}`, ".value[1]"},
		{"ci", `{"field": "ci_type", "op": "in", "value": [["server"]]}`, ".value[0]"},
		{"ci", `{"field": "name", "op": "eq", "value": null}`, ".value"},
		{"ci", `{"field": "name", "op": "in", "value": "x"}`, ".value"},
		// attributes take one scalar type
		{"ci", `{"field": "attributes.ram", "op": "in", "value": [{"a": 1}]}`, ".value[0]"},
		{"ci", `{"field": "attributes.ram", "op": "in", "value": [8, "16"]}`, ".value[1]"},
		{"ci", `{"field": "attributes.ram", "op": "eq", "value": [8]}`, ".value"},
		{"asset", `{"field": "attributes.cost_center", "op": "eq", "value": {"a": 1}}`, ".value"},
		{"ci", `{"field": "attributes.flag", "op": "lt", "value": true}`, ""},
		// exists takes a boolean or nothing
		{"ci", `{"field": "hostname", "op": "exists", "value": "no"}`, ".value"},
		// the path leads to the value in the tree
		{"ci", `{"and": [{"field": "name", "op": "eq", "value": "x"}, {"or": [{"field": "client_id", "op": "eq", "value": "x"}]}]}`, ".and[1].or[0].value"},
	} {
		err := mustAST(t, c.raw).Validate(c.kind)
		var astErr *ASTError
		if !errors.As(err, &astErr) || astErr.Path != c.path {
			t.Errorf("%s (%s): %v, want an ASTError at where%s", c.raw, c.kind, err, c.path)
		}
	}
	for _, c := range []struct{ kind, raw string }{
		{"ci", `{"field": "client_id", "op": "eq", "value": "` + id + `"}`},
		{"ci", `{"field": "site_id", "op": "in", "value": ["` + id + `", "` + strings.ToUpper(id) + `"]}`},
		{"ci", `{"field": "created_at", "op": "gt", "value": "2024-01-01T00:00:00Z"}`},
		{"ci", `{"field": "created_at", "op": "lt", "value": "2024-01-01T10:00:00.123456789+15:59"}`},
		{"ci", `{"field": "updated_at", "op": "gte", "value": "0001-01-01"}`},
		{"ci", `{"field": "last_seen_at", "op": "lte", "value": "9999-12-31T23:59:59-15:59"}`},
		{"asset", `{"field": "warranty_end", "op": "in", "value": ["2024-01-01", "2024-02-29"]}`},
		{"asset", `{"field": "purchase_cost", "op": "gt", "value": -1.5}`},
		{"asset", `{"field": "purchase_cost", "op": "in", "value": [1, 1e300]}`},
		{"ci", `{"field": "is_manual", "op": "eq", "value": false}`},
		{"ci", `{"field": "is_manual", "op": "ne", "value": true}`},
		{"ci", `{"field": "name", "op": "like", "value": "web*"}`},
		{"ci", `{"field": "status", "op": "in", "value": ["active", "maintenance"]}`},
		{"ci", `{"field": "attributes.ram", "op": "gte", "value": 16}`},
		{"ci", `{"field": "attributes.ram", "op": "in", "value": [8, 64]}`},
		{"ci", `{"field": "attributes.flag", "op": "eq", "value": true}`},
		{"ci", `{"field": "attributes.installed", "op": "gte", "value": "2024-01-01"}`},
		{"ci", `{"field": "attributes.firmware", "op": "gt", "value": "1.2"}`},
		{"asset", `{"field": "attributes.cost_center", "op": "in", "value": ["CC-1", "CC-2"]}`},
		{"ci", `{"field": "attributes.ram", "op": "exists", "value": false}`},
		{"ci", `{"field": "hostname", "op": "exists"}`},
	} {
		if err := mustAST(t, c.raw).Validate(c.kind); err != nil {
			t.Errorf("%s (%s): %v", c.raw, c.kind, err)
		}
	}
}

// TestFilterASTAttributeCastsAreGuarded covers F-165 (WP-405): a dynamic
// attribute is cast only under the check of its JSON type (number,
// boolean, a string holding a time), so a value of another type compares
// as NULL instead of failing the query; text and the typed columns need
// no check.
func TestFilterASTAttributeCastsAreGuarded(t *testing.T) {
	for _, c := range []struct{ kind, raw, want string }{
		{"ci", `{"field": "attributes.ram_gb", "op": "gte", "value": 16}`,
			`CASE WHEN jsonb_typeof(ci.attributes->$2) = 'number' THEN (ci.attributes->>$2)::numeric END >= $3::numeric`},
		{"ci", `{"field": "attributes.ram_gb", "op": "ne", "value": 8}`,
			`CASE WHEN jsonb_typeof(ci.attributes->$2) = 'number' THEN (ci.attributes->>$2)::numeric END IS DISTINCT FROM $3::numeric`},
		{"ci", `{"field": "attributes.ram_gb", "op": "in", "value": [8, 64]}`,
			`CASE WHEN jsonb_typeof(ci.attributes->$2) = 'number' THEN (ci.attributes->>$2)::numeric END = ANY($3::numeric[])`},
		{"ci", `{"field": "attributes.monitored", "op": "eq", "value": true}`,
			`CASE WHEN jsonb_typeof(ci.attributes->$2) = 'boolean' THEN (ci.attributes->>$2)::boolean END = $3::boolean`},
		{"asset", `{"field": "attributes.installed", "op": "lt", "value": "2024-01-01T00:00:00Z"}`,
			`CASE WHEN jsonb_typeof(asset.custom_fields->$2) = 'string' AND asset.custom_fields->>$2 ~ '^\d{4}-\d{2}-\d{2}' ` +
				`AND pg_input_is_valid(asset.custom_fields->>$2, 'timestamptz') THEN (asset.custom_fields->>$2)::timestamptz END < $3::timestamptz`},
		{"ci", `{"field": "attributes.installed", "op": "eq", "value": "2024-01-01"}`, `ci.attributes->>$2 = $3`},
		{"ci", `{"field": "attributes.ram_gb", "op": "exists", "value": false}`, `ci.attributes->>$2 IS NULL`},
		{"ci", `{"field": "created_at", "op": "gt", "value": "2024-01-01"}`, `(ci.created_at)::timestamptz > $2::timestamptz`},
		{"ci", `{"field": "is_manual", "op": "eq", "value": true}`, `(ci.is_manual)::boolean = $2::boolean`},
	} {
		ast := mustAST(t, c.raw)
		if err := ast.Validate(c.kind); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		sql, _, _, err := ast.CompileSQL(c.kind, c.kind, []any{"org"}, 2)
		if err != nil || sql != c.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", c.raw, sql, err, c.want)
		}
	}
}

// TestFilterASTSpecValidationNamesTheField covers F-179 and the saved-view
// part of F-047 (WP-405): the ids of the flat filter fields are UUIDs
// (upstream_of, downstream_of, client_id, location_id) and an invalid AST
// value is a 422 field error naming where.<path>, both for the query
// (FilterSpec.Validate, also inside CompileSQL before any SQL is built)
// and for a saved view (filter_spec.<field>).
func TestFilterASTSpecValidationNamesTheField(t *testing.T) {
	for _, c := range []struct {
		spec  FilterSpec
		field string
	}{
		{FilterSpec{UpstreamOf: "not-a-uuid"}, "upstream_of"},
		{FilterSpec{DownstreamOf: "x"}, "downstream_of"},
		{FilterSpec{ClientID: "client-1"}, "client_id"},
		{FilterSpec{LocationID: "berlin"}, "location_id"},
		{FilterSpec{Where: mustAST(t, `{"field": "client_id", "op": "eq", "value": "not-a-uuid"}`)}, "where.value"},
		{FilterSpec{Where: mustAST(t, `{"or": [{"field": "name", "op": "eq", "value": "x"}, {"field": "created_at", "op": "in", "value": ["x"]}]}`)},
			"where.or[1].value[0]"},
		{FilterSpec{Where: mustAST(t, `{"field": "password", "op": "eq", "value": "x"}`)}, "where"},
	} {
		var fe *api.FieldError
		if err := c.spec.Validate("ci"); !errors.As(err, &fe) || fe.Field != c.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%+v: %v, want a 422 field error on %s", c.spec, err, c.field)
		}
		if where, _, err := c.spec.CompileSQL("ci", []any{"org"}, 2); !errors.As(err, &fe) || where != "" {
			t.Errorf("CompileSQL %+v: %q %v, want the field error and no SQL", c.spec, where, err)
		}
		raw, err := json.Marshal(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		var filter map[string]any
		if err = json.Unmarshal(raw, &filter); err != nil {
			t.Fatal(err)
		}
		err = ValidateView("ci", &UpsertRequest{Name: "v", FilterSpec: filter})
		if !errors.As(err, &fe) || fe.Field != "filter_spec."+c.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("saved view %+v: %v, want a 422 field error on filter_spec.%s", c.spec, err, c.field)
		}
	}
	const id = "00000000-0000-4000-8000-0000000000aa"
	valid := FilterSpec{UpstreamOf: id, DownstreamOf: id, ClientID: id, LocationID: id,
		Where: mustAST(t, `{"field": "attributes.ram_gb", "op": "gte", "value": 16}`)}
	if err := valid.Validate("ci"); err != nil {
		t.Errorf("valid spec: %v", err)
	}
}
