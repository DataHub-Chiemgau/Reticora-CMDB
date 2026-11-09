package savedview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestCompileSQLEmptySpec(t *testing.T) {
	where, args, err := (&FilterSpec{}).CompileSQL("ci", []any{"org"}, 2)
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

// Tags of CIs and assets are assignments of the tag catalog (ci_tag,
// WP-281; asset_tag_link, WP-755). Every tag is required.
func TestCompileSQLTags(t *testing.T) {
	spec := FilterSpec{EntityKind: "ci", Tags: []string{"prod", "dmz"}}
	where, args, err := spec.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id AND t.key = $2) AND " +
		"EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id AND t.key = $3)"
	if where != want {
		t.Fatalf("ci tags: got %s", where)
	}
	if len(args) != 3 || args[1] != "prod" || args[2] != "dmz" {
		t.Fatalf("ci tag args: %v", args)
	}
	if strings.Contains(where, "attributes") {
		t.Fatalf("ci tags must not read attributes: %s", where)
	}

	spec = FilterSpec{EntityKind: "asset", Tags: []string{"prod", "dmz"}}
	where, args, err = spec.CompileSQL("asset", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	want = "EXISTS (SELECT 1 FROM asset_tag_link atl JOIN tag t ON t.id = atl.tag_id WHERE atl.asset_id = asset.id AND t.key = $2) AND " +
		"EXISTS (SELECT 1 FROM asset_tag_link atl JOIN tag t ON t.id = atl.tag_id WHERE atl.asset_id = asset.id AND t.key = $3)"
	if where != want || len(args) != 3 || args[1] != "prod" || args[2] != "dmz" {
		t.Fatalf("asset tags: %s %v", where, args)
	}
	if strings.Contains(where, "custom_fields") {
		t.Fatalf("asset tags must not read custom_fields: %s", where)
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
	// The step probes the edges per row of the work table: a LATERAL
	// subquery the planner cannot flatten (OFFSET 0; load run 42619469662,
	// TestRelationshipWalkStarBudget).
	if !strings.Contains(where, "CROSS JOIN LATERAL (SELECT r.source_ci_id AS far FROM ci_relationship r WHERE r.target_ci_id = up.id OFFSET 0)") {
		t.Fatalf("expected the step of the walk as a LATERAL subquery with OFFSET 0, got %s", where)
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
	where, args, err := (&FilterSpec{EntityKind: "ci", HasRelationship: "depends_on"}).CompileSQL("ci", []any{"org"}, 2)
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
	if !strings.Contains(where, "asset.assigned_user_id IS NULL AND asset.assigned_team_id IS NULL") || strings.Contains(where, "FROM assignment") {
		t.Fatalf("expected has_owner=false over the holder cache, got %s", where)
	}
	if !strings.Contains(where, "asset.warranty_until <= CURRENT_DATE") {
		t.Fatalf("expected warranty predicate, got %s", where)
	}
	// has_owner=true: a user or a team holds the asset (holder cache,
	// WP-747).
	trueVal := true
	held := FilterSpec{EntityKind: "asset", HasOwner: &trueVal}
	if where, _, err = held.CompileSQL("asset", []any{"org"}, 2); err != nil {
		t.Fatal(err)
	}
	if where != "(asset.assigned_user_id IS NOT NULL OR asset.assigned_team_id IS NOT NULL)" {
		t.Fatalf("has_owner=true = %s, want the holder cache", where)
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

// TestPresetsCompile runs over every system view (VIE-02, WP-307): the
// filter decodes with DisallowUnknownFields as the query handler decodes
// it (the shipped location_search once was an unknown field), compiles for
// its entity kind, sort and columns pass validatePresentation, the keys are
// unique and every preset has both display names and system true.
func TestPresetsCompile(t *testing.T) {
	keys := map[string]bool{}
	names := map[string]bool{}
	for _, p := range Presets() {
		if p.Key == "" || keys[p.Key] || names[p.Name] {
			t.Errorf("preset %q: key %q missing or not unique, or name not unique", p.Name, p.Key)
		}
		keys[p.Key], names[p.Name] = true, true
		if p.DisplayName.DE == "" || p.DisplayName.EN == "" || !p.System || p.Name == "" {
			t.Errorf("preset %s: display_name %+v, system %v, name %q", p.Key, p.DisplayName, p.System, p.Name)
		}
		raw, err := json.Marshal(p.FilterSpec)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var spec FilterSpec
		if err := dec.Decode(&spec); err != nil {
			t.Fatalf("preset %s has a filter_spec the DSL cannot decode: %v", p.Key, err)
		}
		if p.EntityKind != "ci" && p.EntityKind != "asset" {
			t.Fatalf("preset %s: entity_kind %q", p.Key, p.EntityKind)
		}
		if _, _, err := spec.CompileSQL(p.EntityKind, []any{"org"}, 2); err != nil {
			t.Fatalf("preset %s does not compile: %v", p.Key, err)
		}
		if err := validatePresentation(p.EntityKind, p.Sort, p.Columns); err != nil {
			t.Errorf("preset %s: sort or columns refused: %v", p.Key, err)
		}
		if err := ValidateView(p.EntityKind, &UpsertRequest{FilterSpec: p.FilterSpec, Sort: p.Sort, Columns: p.Columns}); err != nil {
			t.Errorf("preset %s is no valid view: %v", p.Key, err)
		}
	}
	for _, key := range []string{"my_devices_asset", "my_devices_ci", "warranty_expiring", "servers_without_monitoring"} {
		if _, ok := PresetByKey(key); !ok {
			t.Errorf("system view %s missing", key)
		}
	}
	if _, ok := PresetByKey("nope"); ok {
		t.Error("unknown preset key found")
	}
}

// TestCompileSQLSystemViewPredicates covers the predicates of VIE-02
// (WP-307): values are parameters, never part of the SQL text; owned_by_me
// reads the calling user from app.user_id; the directed relationship
// filters read exactly one end; no_metric_within_hours reads
// metric_sample_v; ci_type_subtree walks ci_type_subtree.
func TestCompileSQLSystemViewPredicates(t *testing.T) {
	const marker = "wp307'; DROP TABLE ci; --"
	spec := FilterSpec{
		CITypeSubtree: marker, LacksOutgoingRelationship: marker + "o", LacksIncomingRelationship: marker + "i",
		NoMetricWithinHours: 24, OwnedByMe: true,
	}
	where, args, err := spec.CompileSQL("ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(where, "wp307") || strings.Contains(where, "24") {
		t.Errorf("a value in the SQL text: %s", where)
	}
	if want := []any{"org", marker, marker + "o", marker + "i", 24}; len(args) != len(want) {
		t.Fatalf("args %v, want %v", args, want)
	} else {
		for i := range want {
			if args[i] != want[i] {
				t.Errorf("args[%d] = %v, want %v", i, args[i], want[i])
			}
		}
	}
	sql := squash(where)
	for _, part := range []string{
		"ci.ci_type_id = ANY (ARRAY(SELECT s.id FROM ci_type t CROSS JOIN LATERAL ci_type_subtree(t.id) s WHERE t.key = $2 OR t.name = $2))",
		"ci.owner_user_id = NULLIF(current_setting('app.user_id', true), '')::uuid",
		"ci.owner_team_id IN (SELECT tm.team_id FROM team_member tm WHERE tm.user_id = NULLIF(current_setting('app.user_id', true), '')::uuid)",
		"NOT EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = $3 AND r.source_ci_id = ci.id)",
		"NOT EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = $4 AND r.target_ci_id = ci.id)",
		"(SELECT 1 FROM metric_sample_v ms WHERE ms.ci_id = ci.id AND ms.time >= now() - make_interval(hours => $5::int) LIMIT 1) IS NULL",
	} {
		if !strings.Contains(sql, part) {
			t.Errorf("missing %s in %s", part, sql)
		}
	}
	// Each directed filter reads exactly one end.
	for _, c := range []struct {
		spec       FilterSpec
		end, other string
	}{
		{FilterSpec{LacksOutgoingRelationship: "monitored_by"}, "r.source_ci_id = ci.id", "r.target_ci_id"},
		{FilterSpec{LacksIncomingRelationship: "monitors"}, "r.target_ci_id = ci.id", "r.source_ci_id"},
	} {
		w, _, cerr := c.spec.CompileSQL("ci", nil, 1)
		if cerr != nil || !strings.Contains(w, c.end) || strings.Contains(w, c.other) {
			t.Errorf("%+v: %s %v, want only %s", c.spec, w, cerr, c.end)
		}
	}
	// The flat relationship filter stays undirected.
	if w, _, _ := (&FilterSpec{LacksRelationship: "backed_up_by"}).CompileSQL("ci", nil, 1); !strings.Contains(w, "r.source_ci_id = ci.id OR r.target_ci_id = ci.id") {
		t.Errorf("lacks_relationship: %s", w)
	}

	// Assets: the holder counts, the CI-only predicates do not apply.
	where, args, err = spec.CompileSQL("asset", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	sql = squash(where)
	for _, part := range []string{
		"asset.asset_type_id = ANY (ARRAY(SELECT s.id FROM ci_type t CROSS JOIN LATERAL ci_type_subtree(t.id) s WHERE t.key = $1 OR t.name = $1))",
		"asset.ci_id IN (SELECT id FROM ci WHERE ci_type_id = ANY (ARRAY(SELECT s.id FROM ci_type t CROSS JOIN LATERAL ci_type_subtree(t.id) s",
		"asset.owner_user_id = NULLIF(current_setting('app.user_id', true), '')::uuid",
		"asset.assigned_user_id = NULLIF(current_setting('app.user_id', true), '')::uuid",
		"asset.assigned_team_id IN (SELECT tm.team_id FROM team_member tm",
	} {
		if !strings.Contains(sql, part) {
			t.Errorf("asset: missing %s in %s", part, sql)
		}
	}
	if strings.Contains(sql, "ci_relationship") || strings.Contains(sql, "metric_sample_v") || len(args) != 1 {
		t.Errorf("asset: CI-only predicates or args %v in %s", args, sql)
	}

	for _, bad := range []FilterSpec{{NoMetricWithinHours: -1}, {NoMetricWithinHours: MaxMetricWindowHours + 1}} {
		var fe *api.FieldError
		if _, _, err := bad.CompileSQL("ci", nil, 1); !errors.As(err, &fe) || fe.Field != "no_metric_within_hours" {
			t.Errorf("%+v: %v, want 422 on no_metric_within_hours", bad, err)
		}
	}
	refs := (&FilterSpec{OwnedByMe: true, CITypeSubtree: "server"}).ReferencedFields("ci")
	var paths []string
	for _, r := range refs {
		paths = append(paths, r.Path+"="+r.Name)
	}
	if got := strings.Join(paths, ","); got != "ci_type_id=ci_type_subtree,owner_user_id=owned_by_me,owner_team_id=owned_by_me" {
		t.Errorf("referenced fields: %s", got)
	}
	if !usesRelationships(&FilterSpec{LacksOutgoingRelationship: "x"}) || !usesRelationships(&FilterSpec{LacksIncomingRelationship: "x"}) {
		t.Error("the directed relationship filters need relationship:read")
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
		"EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND (sa.warranty_until)::timestamptz < $7::timestamptz)",
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
	where, _, err := (&FilterSpec{Status: "active", Where: mustAST(t, `{"field": "hostname", "op": "exists"}`)}).CompileSQL("ci", []any{"org"}, 2)
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
		"unknown operator":      {"ci", `{"field": "name", "op": "regex", "value": "x"}`},
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

// TestFilterASTLegacyAssetNames covers AST-01 (WP-738): the deprecated
// names asset_tag, purchase_cost and warranty_end are aliases of
// inventory_no, purchase_price and warranty_until, on assets and as
// asset.<field> of a CI, in where, sort and columns; the field permission
// check maps both names onto the same field key.
func TestFilterASTLegacyAssetNames(t *testing.T) {
	for old, name := range map[string]string{"asset_tag": "inventory_no", "purchase_cost": "purchase_price", "warranty_end": "warranty_until"} {
		value := `"x"`
		switch name {
		case "purchase_price":
			value = `100`
		case "warranty_until":
			value = `"2027-01-01"`
		}
		compile := func(kind, field string) string {
			t.Helper()
			ast := mustAST(t, fmt.Sprintf(`{"field": %q, "op": "eq", "value": %s}`, field, value))
			if err := ast.Validate(kind); err != nil {
				t.Fatalf("%s %s: %v", kind, field, err)
			}
			sql, _, _, err := ast.CompileSQL(kind, kind, []any{"org"}, 2)
			if err != nil {
				t.Fatalf("%s %s: %v", kind, field, err)
			}
			return sql
		}
		if a, b := compile("asset", old), compile("asset", name); a != b || !strings.Contains(a, "asset."+name) || strings.Contains(a, old) {
			t.Errorf("asset %s: %q, %s: %q; want the same SQL on asset.%s", old, a, name, b, name)
		}
		if a, b := compile("ci", "asset."+old), compile("ci", "asset."+name); a != b || !strings.Contains(a, "sa."+name) {
			t.Errorf("ci asset.%s: %q, asset.%s: %q; want the same SQL on sa.%s", old, a, name, b, name)
		}
		columns, orderBy, _ := presentationSQL("asset", []SortKey{{Field: old}}, []string{old}, nil)
		if !strings.Contains(columns, "asset."+name) || !strings.HasPrefix(orderBy, "asset."+name+" ") {
			t.Errorf("sort and column %s: %q, %q; want asset.%s", old, columns, orderBy, name)
		}
		for _, field := range []string{old, name} {
			if key, checked, known := fieldKey("asset", field); key != "core."+old || !checked || !known {
				t.Errorf("fieldKey(asset, %s) = %q, %v, %v; want core.%s", field, key, checked, known, old)
			}
		}
	}
}

// TestFilterASTOwnerFields covers CI-07 (WP-752): owner_user_id and
// owner_team_id of a CI compile with eq, ne and in as uuid parameters, are
// valid sort keys and columns of a view, map onto their field keys and
// take only UUIDs and no ordering; unknown fields the AST still refuses
// (the owner of an asset is a field of the asset since WP-309).
func TestFilterASTOwnerFields(t *testing.T) {
	const user, team, other = "0b1e5c1a-0000-4000-8000-00000000a001", "0b1e5c1a-0000-4000-8000-00000000b002", "0b1e5c1a-0000-4000-8000-00000000c003"
	ast := mustAST(t, `{"or": [{"field": "owner_user_id", "op": "eq", "value": "`+user+`"},
		{"field": "owner_team_id", "op": "in", "value": ["`+team+`", "`+other+`"]},
		{"field": "owner_user_id", "op": "ne", "value": "`+other+`"}]}`)
	if err := ast.Validate("ci"); err != nil {
		t.Fatal(err)
	}
	sql, args, next, err := ast.CompileSQL("ci", "ci", []any{"org"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := "((ci.owner_user_id)::uuid = $2::uuid OR (ci.owner_team_id)::uuid = ANY($3::uuid[]) OR (ci.owner_user_id)::uuid IS DISTINCT FROM $4::uuid)"; sql != want {
		t.Errorf("SQL %q, want %q", sql, want)
	}
	if strings.Contains(sql, user) || strings.Contains(sql, team) || next != 5 || len(args) != 4 || args[1] != user {
		t.Errorf("not parameterized: %q, args %v, next %d", sql, args, next)
	}
	// Sort and columns of a saved view (viewField, WP-555).
	req := &UpsertRequest{Name: "my devices", EntityKind: "ci",
		FilterSpec: map[string]any{"where": map[string]any{"field": "owner_user_id", "op": "eq", "value": user}},
		Sort:       []SortKey{{Field: "owner_team_id", Dir: "desc"}, {Field: "owner_user_id"}},
		Columns:    []string{"owner_user_id", "owner_team_id"}}
	if err := ValidateView("ci", req); err != nil {
		t.Fatalf("view with the owner: %v", err)
	}
	columns, orderBy, _ := presentationSQL("ci", req.Sort, req.Columns, nil)
	if !strings.Contains(columns, "ci.owner_user_id") || !strings.Contains(columns, "ci.owner_team_id") ||
		orderBy != "ci.owner_team_id DESC NULLS LAST, ci.owner_user_id ASC NULLS LAST, ci.id ASC" {
		t.Errorf("presentation: %q, %q", columns, orderBy)
	}
	for _, field := range []string{"owner_user_id", "owner_team_id"} {
		if key, checked, known := fieldKey("ci", field); key != "core."+field || !checked || !known {
			t.Errorf("fieldKey(ci, %s) = %q, %v, %v; want core.%s", field, key, checked, known, field)
		}
	}
	for name, c := range map[string]struct{ kind, raw string }{
		"no uuid":         {"ci", `{"field": "owner_user_id", "op": "eq", "value": "alice"}`},
		"order on a uuid": {"ci", `{"field": "owner_team_id", "op": "gt", "value": "` + team + `"}`},
		"like on a uuid":  {"ci", `{"field": "owner_user_id", "op": "like", "value": "0b1e*"}`},
		"unknown field":   {"ci", `{"field": "owner_name", "op": "eq", "value": "x"}`},
	} {
		var astErr *ASTError
		if err := mustAST(t, c.raw).Validate(c.kind); !errors.As(err, &astErr) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := ValidateView("ci", &UpsertRequest{Name: "x", Sort: []SortKey{{Field: "owner_name"}}}); err == nil {
		t.Error("sort on an unknown field accepted")
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

// TestFilterASTBetween covers SRC-03 (WP-309): between compares numbers and
// times inclusively with exactly two parameters cast to the field kind;
// one value, three values, text and uuid fields are refused.
func TestFilterASTBetween(t *testing.T) {
	for _, c := range []struct{ kind, raw, want string }{
		{"asset", `{"field": "purchase_price", "op": "between", "value": [100, 500.5]}`,
			`(asset.purchase_price)::numeric BETWEEN $2::numeric AND $3::numeric`},
		{"ci", `{"field": "created_at", "op": "between", "value": ["2026-01-01", "2026-12-31T23:59:59Z"]}`,
			`(ci.created_at)::timestamptz BETWEEN $2::timestamptz AND $3::timestamptz`},
		{"ci", `{"field": "asset.warranty_until", "op": "between", "value": ["2026-01-01", "2027-01-01"]}`,
			`EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND (sa.warranty_until)::timestamptz BETWEEN $2::timestamptz AND $3::timestamptz)`},
		{"ci", `{"field": "attributes.ram_gb", "op": "between", "value": [8, 64]}`,
			`CASE WHEN jsonb_typeof(ci.attributes->$2) = 'number' THEN (ci.attributes->>$2)::numeric END BETWEEN $3::numeric AND $4::numeric`},
	} {
		ast := mustAST(t, c.raw)
		if err := ast.Validate(c.kind); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		sql, args, _, err := ast.CompileSQL(c.kind, c.kind, []any{"org"}, 2)
		if err != nil || sql != c.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", c.raw, sql, err, c.want)
		}
		if strings.Contains(sql, "2026") || len(args) < 3 {
			t.Errorf("%s: values not parameterized: %s %v", c.raw, sql, args)
		}
	}
	for _, c := range []struct{ kind, raw, path string }{
		{"asset", `{"field": "purchase_price", "op": "between", "value": [100]}`, ".value"},
		{"asset", `{"field": "purchase_price", "op": "between", "value": 100}`, ".value"},
		{"asset", `{"field": "purchase_price", "op": "between", "value": [1, 2, 3]}`, ".value"},
		{"asset", `{"field": "purchase_price", "op": "between", "value": [1, "x"]}`, ".value[1]"},
		{"ci", `{"field": "created_at", "op": "between", "value": ["2026-01-01", "garbage"]}`, ".value[1]"},
		{"ci", `{"field": "name", "op": "between", "value": ["a", "b"]}`, ""},
		{"ci", `{"field": "client_id", "op": "between", "value": ["` + uuidA + `", "` + uuidA + `"]}`, ""},
		{"ci", `{"field": "is_manual", "op": "between", "value": [false, true]}`, ""},
	} {
		var astErr *ASTError
		if err := mustAST(t, c.raw).Validate(c.kind); !errors.As(err, &astErr) || astErr.Path != c.path {
			t.Errorf("%s: %v, want an ASTError at where%s", c.raw, err, c.path)
		}
	}
}

const uuidA = "00000000-0000-4000-8000-0000000000aa"

// TestFilterASTTags covers SRC-03 (WP-309): has_any is one EXISTS over the
// tag keys, has_all one EXISTS per key, over ci_tag for CIs and
// asset_tag_link for assets with tag.key, the building block of the flat
// tags; has_any and has_all apply to tags only, tags take nothing else.
func TestFilterASTTags(t *testing.T) {
	for _, c := range []struct {
		kind, raw, want string
		args            []any
	}{
		{"ci", `{"field": "tags", "op": "has_any", "value": ["prod", "dmz"]}`,
			`EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id AND t.key = ANY($2::text[]))`,
			[]any{"org", []string{"prod", "dmz"}}},
		{"ci", `{"field": "tags", "op": "has_all", "value": ["prod", "dmz"]}`,
			`(EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id AND t.key = $2) AND ` +
				`EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id AND t.key = $3))`,
			[]any{"org", "prod", "dmz"}},
		{"asset", `{"field": "tags", "op": "has_any", "value": ["prod"]}`,
			`EXISTS (SELECT 1 FROM asset_tag_link atl JOIN tag t ON t.id = atl.tag_id WHERE atl.asset_id = asset.id AND t.key = ANY($2::text[]))`,
			[]any{"org", []string{"prod"}}},
		{"asset", `{"field": "tags", "op": "has_all", "value": ["prod"]}`,
			`(EXISTS (SELECT 1 FROM asset_tag_link atl JOIN tag t ON t.id = atl.tag_id WHERE atl.asset_id = asset.id AND t.key = $2))`,
			[]any{"org", "prod"}},
	} {
		ast := mustAST(t, c.raw)
		if err := ast.Validate(c.kind); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		sql, args, _, err := ast.CompileSQL(c.kind, c.kind, []any{"org"}, 2)
		if err != nil || sql != c.want || fmt.Sprint(args) != fmt.Sprint(c.args) {
			t.Errorf("%s (%s):\n got %s %v (%v)\nwant %s %v", c.raw, c.kind, sql, args, err, c.want, c.args)
		}
	}
	// The flat tags are has_all with the same building block.
	flat, _, err := (&FilterSpec{Tags: []string{"prod", "dmz"}}).CompileSQL("ci", []any{"org"}, 2)
	all, _, _, aerr := mustAST(t, `{"field": "tags", "op": "has_all", "value": ["prod", "dmz"]}`).CompileSQL("ci", "ci", []any{"org"}, 2)
	if err != nil || aerr != nil || "("+flat+")" != all {
		t.Errorf("flat tags %q and has_all %q differ (%v, %v)", flat, all, err, aerr)
	}
	for _, c := range []struct{ raw, path string }{
		{`{"field": "tags", "op": "eq", "value": "prod"}`, ""},
		{`{"field": "tags", "op": "in", "value": ["prod"]}`, ""},
		{`{"field": "tags", "op": "exists"}`, ""},
		{`{"field": "name", "op": "has_any", "value": ["prod"]}`, ""},
		{`{"field": "attributes.tags", "op": "has_all", "value": ["prod"]}`, ""},
		{`{"field": "tags", "op": "has_any", "value": []}`, ".value"},
		{`{"field": "tags", "op": "has_all", "value": "prod"}`, ".value"},
		{`{"field": "tags", "op": "has_any", "value": ["prod", 5]}`, ".value[1]"},
		{`{"field": "tags", "op": "has_any", "value": [""]}`, ".value[0]"},
	} {
		var astErr *ASTError
		if err := mustAST(t, c.raw).Validate("ci"); !errors.As(err, &astErr) || astErr.Path != c.path {
			t.Errorf("%s: %v, want an ASTError at where%s", c.raw, err, c.path)
		}
	}
}

// TestFilterASTAssetFieldsAST01 covers WP-309: the AST-01 columns of WP-271
// and currency are asset fields, on assets and as asset.<field> of a CI in
// the EXISTS over ci.asset_id; asset.attributes.<key> reads the custom
// fields of the linked asset; it is no field of an asset query.
func TestFilterASTAssetFieldsAST01(t *testing.T) {
	for field, kind := range map[string]string{"manufacturer": kindText, "model": kindText, "cost_center": kindText, "currency": kindText,
		"supplier_id": kindUUID, "contract_id": kindUUID, "owner_user_id": kindUUID, "owner_team_id": kindUUID} {
		value := `"x"`
		if kind == kindUUID {
			value = `"` + uuidA + `"`
		}
		ast := mustAST(t, fmt.Sprintf(`{"field": "asset.%s", "op": "eq", "value": %s}`, field, value))
		if err := ast.Validate("ci"); err != nil {
			t.Fatalf("asset.%s: %v", field, err)
		}
		sql, _, _, err := ast.CompileSQL("ci", "ci", []any{"org"}, 2)
		col := "sa." + field
		if kind == kindUUID {
			col = "(sa." + field + ")::uuid"
		}
		if want := "EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND " + col + " = $2"; err != nil || !strings.HasPrefix(sql, want) {
			t.Errorf("asset.%s: %s (%v), want prefix %s", field, sql, err, want)
		}
		if err = mustAST(t, fmt.Sprintf(`{"field": %q, "op": "eq", "value": %s}`, field, value)).Validate("asset"); err != nil {
			t.Errorf("%s on assets: %v", field, err)
		}
	}
	ast := mustAST(t, `{"field": "asset.attributes.room", "op": "eq", "value": "R1"}`)
	if err := ast.Validate("ci"); err != nil {
		t.Fatal(err)
	}
	sql, args, _, err := ast.CompileSQL("ci", "ci", []any{"org"}, 2)
	if want := "EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND sa.custom_fields->>$2 = $3)"; err != nil || sql != want || args[1] != "room" {
		t.Errorf("asset.attributes.room: %s %v (%v), want %s", sql, args, err, want)
	}
	for _, c := range []struct{ kind, raw string }{
		{"asset", `{"field": "asset.attributes.room", "op": "eq", "value": "R1"}`},
		{"ci", `{"field": "asset.attributes.Bad-Key", "op": "eq", "value": "R1"}`},
	} {
		var astErr *ASTError
		if err := mustAST(t, c.raw).Validate(c.kind); !errors.As(err, &astErr) {
			t.Errorf("%s on %s: %v, want an ASTError", c.raw, c.kind, err)
		}
	}
}

// TestFilterASTTypedAttributes covers API-02 (WP-309): with the catalog the
// kind of an attribute comes from its definition, not from the JSON value.
// A CI attribute is read with citype.KeyExpression (the expression of the
// attribute indexes, E-73), restricted to the types of its definitions;
// a number given as a string compares numerically; a value that does not
// fit the definition is refused; the flat attributes compare the same way.
func TestFilterASTTypedAttributes(t *testing.T) {
	cat := &catalog{attrs: map[attrRef]resolvedAttribute{
		{"ci", "ram_gb"}:      {kind: kindNumber},
		{"ci", "env"}:         {kind: kindText, types: []string{uuidA}},
		{"asset", "room"}:     {kind: kindText},
		{"asset", "acquired"}: {kind: kindTime, types: []string{uuidA}},
	}}
	for _, c := range []struct{ kind, raw, want string }{
		// The string "16" is a number by the definition.
		{"ci", `{"field": "attributes.ram_gb", "op": "gte", "value": "16"}`,
			`CASE WHEN jsonb_typeof(ci.attributes->'ram_gb') = 'number' THEN (reticora_attr_text(ci.attributes, 'ram_gb'))::numeric END >= $2::numeric`},
		{"ci", `{"field": "attributes.ram_gb", "op": "between", "value": ["8", 64]}`,
			`CASE WHEN jsonb_typeof(ci.attributes->'ram_gb') = 'number' THEN (reticora_attr_text(ci.attributes, 'ram_gb'))::numeric END BETWEEN $2::numeric AND $3::numeric`},
		// Equality on text is the index expression with the key as a literal.
		{"ci", `{"field": "attributes.env", "op": "eq", "value": "prod"}`,
			`(ci.ci_type_id = ANY($2::uuid[]) AND reticora_attr_text(ci.attributes, 'env') = $3)`},
		{"ci", `{"field": "asset.attributes.room", "op": "eq", "value": "R1"}`,
			`EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND sa.custom_fields->>$2 = $3)`},
		{"ci", `{"field": "asset.attributes.acquired", "op": "between", "value": ["2024-01-01", "2024-12-31"]}`,
			`EXISTS (SELECT 1 FROM asset sa WHERE sa.id = ci.asset_id AND (sa.asset_type_id = ANY($2::uuid[]) AND CASE WHEN ` +
				`jsonb_typeof(sa.custom_fields->$3) = 'string' AND sa.custom_fields->>$3 ~ '^\d{4}-\d{2}-\d{2}' AND ` +
				`pg_input_is_valid(sa.custom_fields->>$3, 'timestamptz') THEN (sa.custom_fields->>$3)::timestamptz END ` +
				`BETWEEN $4::timestamptz AND $5::timestamptz))`},
		{"asset", `{"field": "attributes.room", "op": "in", "value": ["R1", "R2"]}`, `asset.custom_fields->>$2 = ANY($3::text[])`},
	} {
		sql, _, _, err := mustAST(t, c.raw).compile(c.kind, c.kind, cat, []any{"org"}, 2)
		if err != nil || sql != c.want {
			t.Errorf("%s (%s):\n got %s (%v)\nwant %s", c.raw, c.kind, sql, err, c.want)
		}
	}
	for _, c := range []struct{ kind, raw, path string }{
		{"ci", `{"field": "attributes.ram_gb", "op": "eq", "value": "16GB"}`, ".value"},
		{"ci", `{"field": "attributes.ram_gb", "op": "like", "value": "16"}`, ""},
		{"ci", `{"field": "attributes.env", "op": "eq", "value": 5}`, ".value"},
		{"ci", `{"field": "attributes.env", "op": "between", "value": ["a", "b"]}`, ""},
		{"ci", `{"field": "asset.attributes.acquired", "op": "lt", "value": "yesterday"}`, ".value"},
	} {
		nodes := 0
		var astErr *ASTError
		if err := mustAST(t, c.raw).validate(c.kind, "", 1, &nodes, cat); !errors.As(err, &astErr) || astErr.Path != c.path {
			t.Errorf("%s: %v, want an ASTError at where%s", c.raw, err, c.path)
		}
	}
	// The flat attributes are eq comparisons of the AST under the catalog.
	spec := FilterSpec{Attributes: map[string]any{"ram_gb": "16"}}
	where, args, err := spec.compile("ci", cat, []any{"org"}, 2)
	if want := `CASE WHEN jsonb_typeof(ci.attributes->'ram_gb') = 'number' THEN (reticora_attr_text(ci.attributes, 'ram_gb'))::numeric END = $2::numeric`; err != nil ||
		where != want || len(args) != 2 {
		t.Errorf("flat attribute: %s %v (%v), want %s", where, args, err, want)
	}
	var fe *api.FieldError
	if _, _, err = (&FilterSpec{Attributes: map[string]any{"ram_gb": "16GB"}}).compile("ci", cat, []any{"org"}, 2); !errors.As(err, &fe) ||
		fe.Field != "attributes.ram_gb" || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("flat attribute 16GB: %v, want a 422 on attributes.ram_gb", err)
	}
}

// TestReferencedFieldsWP309 covers the field permission check of WP-309:
// asset.attributes.<key> is a custom field of the asset policy, the tag
// comparisons read attributes.tags like the flat tags, between and a
// computed attribute are their field.
func TestReferencedFieldsWP309(t *testing.T) {
	spec := FilterSpec{Where: mustAST(t, `{"and": [
		{"field": "asset.attributes.room", "op": "eq", "value": "R1"},
		{"field": "tags", "op": "has_any", "value": ["prod"]},
		{"field": "asset.cost_center", "op": "eq", "value": "CC"},
		{"field": "created_at", "op": "between", "value": ["2024-01-01", "2025-01-01"]},
		{"field": "attributes.warranty_days_left", "op": "lt", "value": 30}]}`)}
	got := fmt.Sprint(spec.ReferencedFields("ci"))
	want := fmt.Sprint([]FieldRef{
		{"asset", "attributes.room", "asset.attributes.room", false},
		{"ci", "attributes.tags", "tags", false},
		{"asset", "cost_center", "asset.cost_center", false},
		{"ci", "created_at", "created_at", false},
		{"ci", "attributes.warranty_days_left", "attributes.warranty_days_left", false},
	})
	if got != want {
		t.Errorf("ReferencedFields:\n got %s\nwant %s", got, want)
	}
	if key, checked, known := fieldKey("asset", "attributes.room"); key != "attributes.room" || !checked || !known {
		t.Errorf("fieldKey(asset, attributes.room) = %q, %v, %v", key, checked, known)
	}
}

// squash collapses the white space of SQL.
func squash(sql string) string { return strings.Join(strings.Fields(sql), " ") }

// TestFilterASTRelated covers the related node (SRC-04, WP-789): a
// recursive CTE seeded with the CIs that are not deleted and match the
// filter (on rn1, values as parameters), stepping in a LATERAL union over
// ci_relationship of the types (a parameter) from the target to the source
// (out), from the source to the target (in) or both ways, through visible
// CIs that are not
// deleted and at most max_depth steps (default 1). The structural type
// contains is read from ci_structure_edge; without rel_types every stored
// and structural type counts. A related node nests with its own alias.
func TestFilterASTRelated(t *testing.T) {
	sql, args, next, err := mustAST(t, `{"related": {"direction": "out", "rel_types": ["runs_on", "hosted_on"],
		"where": {"and": [{"field": "location_id", "op": "eq", "value": "00000000-0000-0000-0000-0000000000aa"},
			{"field": "tags", "op": "has_any", "value": ["prod"]}]}}}`).CompileSQL("ci", "ci", []any{"org"}, 2)
	want := `ci.id IN (WITH RECURSIVE rel1 (id, depth) AS ( ` +
		`SELECT rn1.id, 0 FROM ci rn1 WHERE rn1.deleted_at IS NULL AND ((rn1.location_id)::uuid = $2::uuid AND ` +
		`EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = rn1.id AND t.key = ANY($3::text[]))) ` +
		`UNION SELECT rc1.id, rel1.depth + 1 FROM rel1 ` +
		`CROSS JOIN LATERAL (SELECT r.source_ci_id AS far FROM ci_relationship r WHERE r.target_ci_id = rel1.id AND r.rel_type = ANY($4::text[])) rs1 ` +
		`JOIN ci rc1 ON rc1.id = rs1.far AND rc1.deleted_at IS NULL WHERE rel1.depth < 1 ) SELECT id FROM rel1 WHERE depth > 0)`
	if err != nil || squash(sql) != want || next != 5 {
		t.Errorf("out:\n got %s (%v, next %d)\nwant %s", squash(sql), err, next, want)
	}
	if len(args) != 4 || fmt.Sprint(args[3]) != "[runs_on hosted_on]" {
		t.Errorf("out args %v", args)
	}

	for _, c := range []struct {
		name, raw string
		want      []string
		not       []string
	}{
		{"in at depth 3", `{"related": {"direction": "in", "max_depth": 3, "rel_types": ["depends_on"], "where": {"field": "name", "op": "eq", "value": "db"}}}`,
			[]string{`(SELECT r.target_ci_id AS far FROM ci_relationship r WHERE r.source_ci_id = rel1.id AND r.rel_type = ANY($3::text[])) rs1`, `rel1.depth < 3`},
			[]string{"r.source_ci_id AS far", "ci_structure_edge"}},
		{"both", `{"related": {"direction": "both", "rel_types": ["depends_on"], "where": {"field": "name", "op": "eq", "value": "db"}}}`,
			[]string{`(SELECT r.source_ci_id AS far FROM ci_relationship r WHERE r.target_ci_id = rel1.id AND r.rel_type = ANY($3::text[]) UNION ALL ` +
				`SELECT r.target_ci_id AS far FROM ci_relationship r WHERE r.source_ci_id = rel1.id AND r.rel_type = ANY($3::text[])) rs1`},
			[]string{"ci_structure_edge"}},
		{"structural type", `{"related": {"direction": "out", "rel_types": ["contains"], "where": {"field": "name", "op": "eq", "value": "rack"}}}`,
			[]string{`(SELECT e.source_ci_id AS far FROM ci_structure_edge e WHERE e.target_ci_id = rel1.id AND e.rel_type = ANY($3::text[]) AND e.target_ci_id IS NOT NULL) rs1`},
			[]string{"ci_relationship"}},
		{"all types", `{"related": {"direction": "out", "where": {"field": "name", "op": "eq", "value": "rack"}}}`,
			[]string{`(SELECT r.source_ci_id AS far FROM ci_relationship r WHERE r.target_ci_id = rel1.id UNION ALL ` +
				`SELECT e.source_ci_id AS far FROM ci_structure_edge e WHERE e.target_ci_id = rel1.id AND e.rel_type = ANY($3::text[]) AND e.target_ci_id IS NOT NULL) rs1`},
			nil},
		{"nested", `{"related": {"direction": "out", "where": {"related": {"direction": "in", "rel_types": ["contains"], "where": {"field": "name", "op": "eq", "value": "R12"}}}}}`,
			[]string{`SELECT rn1.id, 0 FROM ci rn1 WHERE rn1.deleted_at IS NULL AND rn1.id IN (WITH RECURSIVE rel2 (id, depth) AS ( SELECT rn2.id, 0 FROM ci rn2 WHERE rn2.deleted_at IS NULL AND rn2.name = $2`},
			nil},
		{"under not", `{"not": {"related": {"direction": "out", "rel_types": ["backed_up_by"], "where": {"field": "name", "op": "like", "value": "bk*"}}}}`,
			[]string{`(NOT COALESCE(ci.id IN (WITH RECURSIVE rel1`}, nil},
	} {
		caseSQL, caseArgs, _, caseErr := mustAST(t, c.raw).CompileSQL("ci", "ci", []any{"org"}, 2)
		got := squash(caseSQL)
		if caseErr != nil {
			t.Errorf("%s: %v", c.name, caseErr)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing in %s (%v)", c.name, w, got, caseArgs)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q in %s", c.name, w, got)
			}
		}
	}
	// Without rel_types the structural types are the projected ones in order.
	_, args, _, err = mustAST(t, `{"related": {"direction": "out", "where": {"field": "name", "op": "eq", "value": "x"}}}`).CompileSQL("ci", "ci", []any{"org"}, 2)
	if err != nil || fmt.Sprint(args) != "[org x [contains located_in mounted_in]]" {
		t.Errorf("all types: args %v (%v)", args, err)
	}

	// Refusals with the path of the node (422 on where.<path>).
	for _, c := range []struct{ name, kind, raw, path string }{
		{"asset", "asset", `{"related": {"direction": "out", "where": {"field": "name", "op": "eq", "value": "x"}}}`, "where.related"},
		{"max_depth 0", "ci", `{"related": {"direction": "out", "max_depth": 0, "where": {"field": "name", "op": "eq", "value": "x"}}}`, "where.related.max_depth"},
		{"max_depth 6", "ci", `{"related": {"direction": "out", "max_depth": 6, "where": {"field": "name", "op": "eq", "value": "x"}}}`, "where.related.max_depth"},
		{"mixed node", "ci", `{"field": "name", "op": "eq", "value": "x", "related": {"direction": "out", "where": {"field": "name", "op": "eq", "value": "x"}}}`, "where"},
		{"unknown field in the filter", "ci", `{"related": {"direction": "out", "where": {"field": "secret", "op": "eq", "value": "x"}}}`, "where.related.where"},
		{"value per field kind in the filter", "ci", `{"related": {"direction": "in", "where": {"field": "location_id", "op": "eq", "value": "R12"}}}`, "where.related.where.value"},
		{"between on text in the filter", "ci", `{"related": {"direction": "in", "where": {"field": "name", "op": "between", "value": ["a", "b"]}}}`, "where.related.where"},
		{"unknown asset field in the filter", "ci", `{"related": {"direction": "in", "where": {"field": "asset.nope", "op": "eq", "value": "x"}}}`, "where.related.where"},
	} {
		spec := FilterSpec{EntityKind: c.kind, Where: mustAST(t, c.raw)}
		var fe *api.FieldError
		if err := spec.Validate(c.kind); !errors.As(err, &fe) || fe.Field != c.path || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want a 422 on %s", c.name, err, c.path)
		}
		if _, _, err := spec.CompileSQL(c.kind, nil, 1); err == nil {
			t.Errorf("%s compiles", c.name)
		}
	}
}

// TestFilterASTRelatedCatalog: the attributes of the filter of a related
// node are resolved apart from those of the queried CIs (relAttrs) and
// compared by their definition on the alias of the related CIs.
func TestFilterASTRelatedCatalog(t *testing.T) {
	cat := &catalog{
		attrs:    map[attrRef]resolvedAttribute{{"ci", "days"}: {kind: kindText}},
		relAttrs: map[attrRef]resolvedAttribute{{"ci", "days"}: {kind: kindNumber, types: []string{"t1"}}},
	}
	sql, args, _, err := mustAST(t, `{"and": [{"field": "attributes.days", "op": "eq", "value": "x"},
		{"related": {"direction": "out", "where": {"field": "attributes.days", "op": "lt", "value": "30"}}}]}`).compile("ci", "ci", cat, []any{"org"}, 2)
	got := squash(sql)
	for _, want := range []string{
		`(reticora_attr_text(ci.attributes, 'days') = $2 AND ci.id IN`,
		`WHERE rn1.deleted_at IS NULL AND (rn1.ci_type_id = ANY($3::uuid[]) AND CASE WHEN jsonb_typeof(rn1.attributes->'days') = 'number' ` +
			`THEN (reticora_attr_text(rn1.attributes, 'days'))::numeric END < $4::numeric)`,
	} {
		if err != nil || !strings.Contains(got, want) {
			t.Errorf("%q missing in %s (%v)", want, got, err)
		}
	}
	if len(args) != 5 {
		t.Errorf("args %v", args)
	}
	// A text value for the number attribute of a related CI is refused.
	spec := FilterSpec{Where: mustAST(t, `{"related": {"direction": "out", "where": {"field": "attributes.days", "op": "lt", "value": "soon"}}}`)}
	var fe *api.FieldError
	if err := spec.validate("ci", cat); !errors.As(err, &fe) || fe.Field != "where.related.where.value" {
		t.Errorf("text for a number attribute of a related CI: %v", err)
	}
}

// TestReferencedFieldsRelated: the fields of the filter of a related node
// are fields of the related CIs (Related) for the field permission check
// (WP-789); asset fields there stay asset fields.
func TestReferencedFieldsRelated(t *testing.T) {
	spec := FilterSpec{Where: mustAST(t, `{"and": [{"field": "hostname", "op": "eq", "value": "vm"},
		{"related": {"direction": "out", "where": {"and": [{"field": "hostname", "op": "eq", "value": "h"},
			{"field": "attributes.warranty_days_left", "op": "lt", "value": 30},
			{"field": "asset.cost_center", "op": "eq", "value": "CC"}]}}}]}`)}
	got := fmt.Sprint(spec.ReferencedFields("ci"))
	want := fmt.Sprint([]FieldRef{
		{"ci", "hostname", "hostname", false},
		{"ci", "hostname", "hostname", true},
		{"ci", "attributes.warranty_days_left", "attributes.warranty_days_left", true},
		{"asset", "cost_center", "asset.cost_center", false},
	})
	if got != want {
		t.Errorf("ReferencedFields:\n got %s\nwant %s", got, want)
	}
	attrs, _, relTypes := specUses(&FilterSpec{Where: mustAST(t, `{"related": {"direction": "out", "rel_types": ["runs_on"],
		"where": {"field": "attributes.days", "op": "lt", "value": 3}}}`)})
	if fmt.Sprint(attrs) != "[{{ci days} where.related.where true}]" || fmt.Sprint(relTypes) != "[{runs_on where.related.rel_types[0]}]" {
		t.Errorf("specUses: %v %v", attrs, relTypes)
	}
}

// TestUsesRelationships: related and the flat relationship fields need
// relationship:read (WP-789).
func TestUsesRelationships(t *testing.T) {
	for _, c := range []struct {
		spec FilterSpec
		want bool
	}{
		{FilterSpec{}, false},
		{FilterSpec{Where: mustAST(t, `{"field": "name", "op": "eq", "value": "x"}`)}, false},
		{FilterSpec{HasRelationship: "runs_on"}, true},
		{FilterSpec{LacksRelationship: "backed_up_by"}, true},
		{FilterSpec{UpstreamOf: "00000000-0000-0000-0000-0000000000aa"}, true},
		{FilterSpec{DownstreamOf: "00000000-0000-0000-0000-0000000000aa"}, true},
		{FilterSpec{Where: mustAST(t, `{"not": {"related": {"direction": "in", "where": {"field": "name", "op": "eq", "value": "x"}}}}`)}, true},
	} {
		if got := usesRelationships(&c.spec); got != c.want {
			t.Errorf("%+v: %v, want %v", c.spec, got, c.want)
		}
	}
}

// filterConformance are the filter cases of api/rules.conformance.json
// (MET-45, WP-789).
type filterConformance struct {
	FilterCases []struct {
		Name       string          `json:"name"`
		EntityKind string          `json:"entity_kind"`
		Filter     json.RawMessage `json:"filter"`
	} `json:"filter_cases"`
	InvalidFilters []struct {
		Name       string          `json:"name"`
		EntityKind string          `json:"entity_kind"`
		Filter     json.RawMessage `json:"filter"`
		Path       string          `json:"path"`
	} `json:"invalid_filters"`
}

// decodeFilter decodes a filter as the API does (unknown members refused).
func decodeFilter(raw json.RawMessage) (*Expr, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Expr
	err := dec.Decode(&e)
	return &e, err
}

// TestFilterConformance runs the filter cases of api/rules.conformance.json
// (MET-45, WP-789) through Validate: every filter of filter_cases is valid
// for its entity kind, compiles and survives a JSON round trip, every
// filter of invalid_filters is refused at its path (where.<path>);
// frontend/src/rules/filter.test.ts runs the same cases through parseFilter.
func TestFilterConformance(t *testing.T) {
	raw, err := os.ReadFile("../../../api/rules.conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var c filterConformance
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.FilterCases) == 0 || len(c.InvalidFilters) == 0 {
		t.Fatal("no filter cases")
	}
	for _, tc := range c.FilterCases {
		t.Run(tc.Name, func(t *testing.T) {
			e, derr := decodeFilter(tc.Filter)
			if derr != nil {
				t.Fatal(derr)
			}
			if verr := e.Validate(tc.EntityKind); verr != nil {
				t.Fatalf("valid filter refused: %v", verr)
			}
			again, merr := json.Marshal(e)
			if merr != nil {
				t.Fatal(merr)
			}
			var want, got any
			_ = json.Unmarshal(tc.Filter, &want)
			_ = json.Unmarshal(again, &got)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("round trip %s, want %s", again, tc.Filter)
			}
			if _, _, _, cerr := e.CompileSQL(tc.EntityKind, tc.EntityKind, nil, 1); cerr != nil {
				t.Errorf("compile: %v", cerr)
			}
		})
	}
	for _, tc := range c.InvalidFilters {
		t.Run(tc.Name, func(t *testing.T) {
			e, derr := decodeFilter(tc.Filter)
			if derr != nil {
				t.Fatal(derr)
			}
			var astErr *ASTError
			if verr := e.Validate(tc.EntityKind); !errors.As(verr, &astErr) || "where"+astErr.Path != tc.Path {
				t.Errorf("%v, want an error at %s", verr, tc.Path)
			}
		})
	}
}

// TestFilterSchemaMatchesOperators: $defs/filter of api/rules.schema.json
// has the nodes of Expr and the operators FilterOps (WP-789).
func TestFilterSchemaMatchesOperators(t *testing.T) {
	raw, err := os.ReadFile("../../../api/rules.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			OneOf []struct {
				Ref string `json:"$ref"`
			} `json:"oneOf"`
			Required   []string `json:"required"`
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var nodes []string
	for _, alt := range schema.Defs["filter"].OneOf {
		nodes = append(nodes, strings.Join(schema.Defs[strings.TrimPrefix(alt.Ref, "#/$defs/")].Required, "+"))
	}
	if got := strings.Join(nodes, " "); got != "and or not related field+op" {
		t.Errorf("$defs/filter nodes %s", got)
	}
	if got := schema.Defs["filter_compare"].Properties["op"].Enum; !slices.Equal(got, FilterOps) {
		t.Errorf("$defs/filter_compare operators %v, Go %v", got, FilterOps)
	}
}
