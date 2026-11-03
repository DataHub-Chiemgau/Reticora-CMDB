package citype_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// composeEnv is the fixture of the WP-252 tests: the set API (setAPI) with
// the CI routes, a system base and a type chain root → mid → leaf with two
// sets at leaf and a probe type below leaf.
type composeEnv struct {
	*setAPI
	org                        string
	root, mid, leaf, probe     citype.Type
	set1, set2                 citype.AttributeSet
	rootNum, set1Enum, midDesc string
}

// systemDefinition inserts a definition of the system base (organization
// NULL, scope global) as the migration owner would, as
// TestNewTypeWithoutReleaseAgainstPostgres does; the cleanup removes it.
func systemDefinition(t *testing.T, f *scopetest.Fixture, key, dataType, enumValues, validation, def string) {
	t.Helper()
	var enum, deflt any
	if enumValues != "" {
		enum = enumValues
	}
	if def != "" {
		deflt = def
	}
	if validation == "" {
		validation = "{}"
	}
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO attribute_definition
			(organization_id, ci_type_id, scope, key, data_type, enum_values, validation, default_value)
		VALUES (NULL, NULL, 'global', $1, $2, $3::jsonb, $4::jsonb, $5::jsonb) RETURNING id::text`,
		key, dataType, enum, validation, deflt).Scan(&id); err != nil {
		t.Fatalf("system definition %s: %v", key, err)
	}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM attribute_definition WHERE id = $1`, id)
	})
}

func newComposeEnv(t *testing.T) *composeEnv {
	t.Helper()
	f := scopetest.Seed(t, "c9")
	e := &composeEnv{setAPI: newSetAPI(t, f), org: f.OrgA}
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(e.repo)).RegisterRoutes(e.mux)

	// The system base (materialized into every type).
	systemDefinition(t, f, "wp252_enum", "enum", `["a","b","c","d"]`, "", "")
	systemDefinition(t, f, "wp252_num", "decimal", "", `{"max": 100}`, "")
	systemDefinition(t, f, "wp252_ref", "user", "", "", "")
	systemDefinition(t, f, "wp252_multi", "text", "", "", "")
	systemDefinition(t, f, "wp252_pat", "text", "", `{"pattern": "^[a-z]"}`, `"abz"`)

	// The type chain root → mid → leaf (WP-248), inheritable definitions.
	e.root = e.createType(e.org, "wp252-root")
	e.mid = e.childType(e.org, "wp252-mid", e.root.ID)
	e.leaf = e.childType(e.org, "wp252-leaf", e.mid.ID)
	typed := func(typ citype.Type, body string) citype.AttributeDefinition {
		return e.definition(e.org, `{"ci_type_id":"`+typ.ID+`","inheritable":true,`+strings.TrimPrefix(body, "{"))
	}
	typed(e.root, `{"key":"wp252_enum","data_type":"enum","enum_values":["a","b","c"]}`)
	e.rootNum = typed(e.root, `{"key":"wp252_num","data_type":"decimal","required":true,"validation":{"min":1}}`).ID
	typed(e.mid, `{"key":"wp252_len","data_type":"text","validation":{"min_length":2}}`)
	typed(e.mid, `{"key":"wp252_pat","data_type":"text","validation":{"pattern":"z$"}}`)
	e.midDesc = typed(e.mid, `{"key":"wp252_def","data_type":"text","default":"mid"}`).ID
	typed(e.leaf, `{"key":"wp252_num","data_type":"decimal","validation":{"max":50}}`)
	typed(e.leaf, `{"key":"wp252_len","data_type":"text","validation":{"max_length":20}}`)
	typed(e.leaf, `{"key":"wp252_leaf","data_type":"integer"}`)

	// Two sets at leaf in position order (WP-709).
	e.set1, e.set2 = e.createSet(e.org, "wp252-set-1"), e.createSet(e.org, "wp252-set-2")
	e.set1Enum = e.mustSetAttribute(e.org, e.set1.ID,
		`{"key":"wp252_enum","data_type":"enum","enum_values":["b","c","d"],"default":"c","inheritable":true}`)
	e.mustSetAttribute(e.org, e.set2.ID, `{"key":"wp252_enum","data_type":"enum","enum_values":["a","b","c","d"],"default":"b","inheritable":true}`)
	e.mustSetAttribute(e.org, e.set2.ID, `{"key":"wp252_def","data_type":"text","default":"set2","inheritable":true}`)
	e.putSets(e.org, e.leaf.ID, e.set1.ID, e.set2.ID)

	e.probe = e.childType(e.org, "wp252-probe", e.leaf.ID)
	return e
}

// managerCtx is the organization's context of the metamodel administrator
// with the right to change instance attributes (MET-14, WP-256).
func (e *composeEnv) managerCtx() context.Context {
	return identity.WithPrincipal(e.ctx(e.org), identity.Principal{Subject: e.user, OrganizationID: e.org,
		Type: identity.PrincipalTypeUser, Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite,
			identity.PermCIInstanceAttributeManage}})
}

func (e *composeEnv) callAs(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// version is the schema_version of a type.
func (e *composeEnv) version(id string) int {
	e.t.Helper()
	return e.typeOf(e.org, id).SchemaVersion
}

// wantComposed422 checks a refused write: 422 problem+json on field with
// code.
func wantComposed422(t *testing.T, name string, w *httptest.ResponseRecorder, field, code string) {
	t.Helper()
	p := problemOf(w)
	if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Type") != "application/problem+json" ||
		p.Field != field || p.Code != code {
		t.Errorf("%s: %d %s %s, want 422 problem+json on %s with code %s",
			name, w.Code, w.Header().Get("Content-Type"), w.Body.String(), field, code)
	}
}

// TestCompositionParityAgainstPostgres covers MET-16 (WP-252) against
// PostgreSQL: the materialized schema of each type of a chain root → mid →
// leaf with the system base and two sets at leaf (generate_attribute_schema,
// compose_attribute_definition) equals what Compose makes of the same
// definitions (ResolveFields without a CI): the keys and required as sets,
// enum, minimum/maximum, minLength/maxLength, pattern/allOf, default and
// x-data-type per key. A definition that conflicts, written past the
// backend, is refused by the database with the code (DETAIL) that Compose
// reports for the same definitions, and schema_version stays. Changing a
// set attribute, the set list, the parent type or an attribute of the
// parent raises schema_version of each type whose schema changes by
// exactly one per change; a CI written afterwards records the new version.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCompositionParityAgainstPostgres(t *testing.T) {
	e := newComposeEnv(t)
	bg := context.Background()

	for _, typ := range []citype.Type{e.root, e.mid, e.leaf, e.probe} {
		got := e.typeOf(e.org, typ.ID)
		defs, err := e.repo.ResolveFields(e.ctx(e.org), e.org, typ.ID, "")
		if err != nil {
			t.Fatalf("%s: resolve: %v", typ.Key, err)
		}
		compareSchema(t, typ.Key, &got, defs)
	}
	// The composed leaf as both sides see it.
	leaf := e.resolved(e.org, e.leaf.ID, "")
	if v := leaf["wp252_enum"]; !reflect.DeepEqual(v.EnumValues, []string{"b", "c"}) || v.DefaultValue != "b" {
		t.Errorf("leaf wp252_enum = %+v, want [b c] with the default of the later set", v)
	}
	if v := leaf["wp252_num"].Validation; !leaf["wp252_num"].Required || *v.Min != 1 || *v.Max != 50 {
		t.Errorf("leaf wp252_num = %+v %+v", leaf["wp252_num"], v)
	}

	// Conflicts written past the backend: the database refuses them with
	// the code Compose reports.
	probeEffective := e.resolved(e.org, e.probe.ID, "")
	yes := true
	for _, c := range []struct {
		name, column, value string
		def                 fieldmeta.FieldDefinition
	}{
		{"data type", "data_type", "text", fieldmeta.FieldDefinition{Name: "wp252_num", DataType: fieldmeta.TypeText}},
		{"reference target", "data_type", "team", fieldmeta.FieldDefinition{Name: "wp252_ref", DataType: fieldmeta.TypeTeam}},
		{"multi_value", "multi_value", "true", fieldmeta.FieldDefinition{Name: "wp252_multi", DataType: fieldmeta.TypeText, MultiValue: &yes}},
		{"no allowed value left", "enum_values", `["a"]`, fieldmeta.FieldDefinition{Name: "wp252_enum", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a"}}},
		{"empty range", "validation", `{"min": 60}`, fieldmeta.FieldDefinition{Name: "wp252_num", DataType: fieldmeta.TypeDecimal,
			Validation: &fieldmeta.ValidationRules{Min: f64(60)}}},
		{"empty length range", "validation", `{"max_length": 1}`, fieldmeta.FieldDefinition{Name: "wp252_len", DataType: fieldmeta.TypeText,
			Validation: &fieldmeta.ValidationRules{MaxLength: intp(1)}}},
	} {
		_, goErr := citype.Compose([]fieldmeta.FieldDefinition{probeEffective[c.def.Name]}, []fieldmeta.FieldDefinition{c.def})
		var conflict *citype.CompositionError
		if !errors.As(goErr, &conflict) {
			t.Fatalf("%s: Compose = %v, want a conflict", c.name, goErr)
		}
		before := e.version(e.probe.ID)
		dbCode := insertPastBackend(t, e.f, e.org, e.probe.ID, &c.def, c.column, c.value)
		if dbCode != conflict.Code {
			t.Errorf("%s: database code %q, Go code %q", c.name, dbCode, conflict.Code)
		}
		if after := e.version(e.probe.ID); after != before {
			t.Errorf("%s: schema_version %d after the refusal, want %d", c.name, after, before)
		}
	}

	// Regeneration: one step per change for each type whose schema changes.
	versions := func() map[string]int {
		out := map[string]int{}
		for _, typ := range []citype.Type{e.root, e.mid, e.leaf, e.probe} {
			out[typ.Key] = e.version(typ.ID)
		}
		return out
	}
	step := func(name string, change func(), rises ...string) {
		t.Helper()
		before := versions()
		change()
		after := versions()
		for key, v := range after {
			want := before[key]
			if slices.Contains(rises, key) {
				want++
			}
			if v != want {
				t.Errorf("%s: schema_version of %s %d, want %d", name, key, v, want)
			}
		}
		for _, typ := range []citype.Type{e.root, e.mid, e.leaf, e.probe} {
			got := e.typeOf(e.org, typ.ID)
			defs, err := e.repo.ResolveFields(e.ctx(e.org), e.org, typ.ID, "")
			if err != nil {
				t.Fatalf("%s: resolve %s: %v", name, typ.Key, err)
			}
			compareSchema(t, name+": "+typ.Key, &got, defs)
		}
	}
	step("set attribute", func() {
		e.must(http.StatusOK, e.org, http.MethodPatch, "/api/v1/attribute-definitions/"+e.set1Enum, `{"enum_values":["c","d"]}`, nil)
	}, "wp252-leaf", "wp252-probe")
	step("set list", func() { e.putSets(e.org, e.leaf.ID, e.set2.ID) }, "wp252-leaf", "wp252-probe")
	step("attribute of the parent", func() {
		e.must(http.StatusOK, e.org, http.MethodPatch, "/api/v1/attribute-definitions/"+e.midDesc, `{"default":"mid-2"}`, nil)
	}, "wp252-mid")
	step("parent type", func() {
		e.must(http.StatusOK, e.org, http.MethodPatch, "/api/v1/ci-types/"+e.probe.ID, `{"parent_type_id":"`+e.mid.ID+`"}`, nil)
	}, "wp252-probe")

	// A CI written afterwards records the version of its type.
	ciID := e.f.CI(t, e.org, e.f.Client1, "wp252-parity-ci")
	if _, err := e.f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciID, e.probe.ID); err != nil {
		t.Fatal(err)
	}
	e.must(http.StatusOK, e.org, http.MethodPatch, "/api/v1/attribute-definitions/"+e.rootNum, `{"description":"wp252 v2"}`, nil)
	want := e.version(e.probe.ID)
	w := e.callAs(e.managerCtx(), http.MethodPatch, "/api/v1/cis/"+ciID, `{"attributes":{"wp252_num":5}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("CI write: %d %s", w.Code, w.Body.String())
	}
	var stored int
	if err := e.f.Admin.QueryRow(bg, `SELECT schema_version FROM ci WHERE id = $1`, ciID).Scan(&stored); err != nil || stored != want {
		t.Errorf("CI schema_version %d (%v), want %d", stored, err, want)
	}
}

// insertPastBackend writes a definition of def.Name with column = value at
// typeID straight into attribute_definition as the migration owner, past
// the backend's check, and returns the code the database reports
// (DETAIL of attribute_definition_composition). The write is rolled back.
func insertPastBackend(t *testing.T, f *scopetest.Fixture, org, typeID string, def *fieldmeta.FieldDefinition, column, value string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cols := map[string]string{"data_type": "$4", "multi_value": "false", "enum_values": "NULL", "validation": "'{}'::jsonb"}
	switch column {
	case "multi_value":
		cols[column] = value
	case "enum_values", "validation":
		cols[column] = "'" + value + "'::jsonb"
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO attribute_definition
			(organization_id, ci_type_id, scope, key, data_type, multi_value, enum_values, validation)
		VALUES ($1, $2, 'type', $3, %s, %s, %s, %s)`, cols["data_type"], cols["multi_value"], cols["enum_values"], cols["validation"]),
		org, typeID, def.Name, def.DataType)
	if err == nil {
		err = tx.Commit(ctx)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "attribute_definition_composition" {
		t.Errorf("%s past the backend: %v, want a refusal of attribute_definition_composition", def.Name, err)
		return ""
	}
	return pgErr.Detail
}

// compareSchema compares the materialized schema of typ with the
// definitions Compose made of the same layers: the keys and required as
// sets (generate_attribute_schema sorts by key, Compose keeps the first
// appearance), per key of this test enum, minimum/maximum,
// minLength/maxLength, pattern/allOf, default and x-data-type.
func compareSchema(t *testing.T, name string, typ *citype.Type, defs []fieldmeta.FieldDefinition) {
	t.Helper()
	props := properties(typ)
	var schemaKeys, goKeys, goRequired []string
	for k := range props {
		schemaKeys = append(schemaKeys, k)
	}
	for i := range defs {
		goKeys = append(goKeys, defs[i].Name)
		if defs[i].Required {
			goRequired = append(goRequired, defs[i].Name)
		}
	}
	required := append([]string(nil), typ.RequiredFields...)
	for _, s := range [][]string{schemaKeys, goKeys, goRequired, required} {
		slices.Sort(s)
	}
	if !slices.Equal(schemaKeys, goKeys) {
		t.Errorf("%s: schema keys %v, Compose keys %v", name, schemaKeys, goKeys)
	}
	if !slices.Equal(required, goRequired) {
		t.Errorf("%s: required %v, Compose %v", name, required, goRequired)
	}
	for i := range defs {
		d := &defs[i]
		if !strings.HasPrefix(d.Name, "wp252_") {
			continue
		}
		p, _ := props[d.Name].(map[string]any)
		if problem := propertyDiff(p, d); problem != "" {
			t.Errorf("%s: %s: %s (schema %v, Compose %+v %+v)", name, d.Name, problem, p, *d, d.Validation)
		}
	}
}

// propertyDiff names the first difference between a property of the
// materialized schema and a composed definition ("" none).
func propertyDiff(p map[string]any, d *fieldmeta.FieldDefinition) string {
	if p == nil {
		return "missing in the schema"
	}
	if p["x-data-type"] != d.DataType {
		return fmt.Sprintf("x-data-type %v, want %s", p["x-data-type"], d.DataType)
	}
	v := d.Validation
	if v == nil {
		v = &fieldmeta.ValidationRules{}
	}
	var enum []any
	switch {
	case len(d.EnumValues) > 0:
		enum = fieldmeta.AnyValues(d.EnumValues)
	case len(v.Options) > 0:
		enum = v.Options
	}
	got, _ := p["enum"].([]any)
	if len(got) > 0 || len(enum) > 0 {
		if !reflect.DeepEqual(got, enum) {
			return fmt.Sprintf("enum %v, want %v", got, enum)
		}
	}
	number := func(key string, want *float64) string {
		n, ok := p[key].(float64)
		if ok != (want != nil) || ok && n != *want {
			return fmt.Sprintf("%s %v, want %v", key, p[key], want)
		}
		return ""
	}
	length := func(key string, want *int) string {
		var w *float64
		if want != nil {
			f := float64(*want)
			w = &f
		}
		return number(key, w)
	}
	for _, diff := range []string{number("minimum", v.Min), number("maximum", v.Max),
		length("minLength", v.MinLength), length("maxLength", v.MaxLength)} {
		if diff != "" {
			return diff
		}
	}
	if v.Pattern != "" && p["pattern"] != v.Pattern {
		return fmt.Sprintf("pattern %v, want %s", p["pattern"], v.Pattern)
	}
	var allOf []string
	if list, ok := p["allOf"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				allOf = append(allOf, fmt.Sprint(m["pattern"]))
			}
		}
	}
	if !slices.Equal(allOf, v.Patterns) {
		return fmt.Sprintf("allOf %v, want %v", allOf, v.Patterns)
	}
	def, has := p["default"]
	switch s, isString := def.(string); {
	case !has && d.DefaultValue == "":
	case isString && s == d.DefaultValue:
	case has && !isString:
		raw, _ := json.Marshal(def)
		if string(raw) != d.DefaultValue {
			return fmt.Sprintf("default %s, want %s", raw, d.DefaultValue)
		}
	default:
		return fmt.Sprintf("default %v, want %q", def, d.DefaultValue)
	}
	return ""
}

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

// TestCheckCompositionAgainstPostgres covers MET-16 (WP-252) against
// PostgreSQL: CheckComposition composes in the open transaction of the
// caller — a type with two conflicting organization globals written in it
// reports both with key and code, a draft template version with a conflict
// reports its code, and after the rollback nothing is changed. Every layer
// is checked when it is written, with 422 problem+json, field and code:
// an instance definition with another data type (PUT
// /cis/{id}/field-definitions, field name, datatype-conflict: no row in
// ci_instance_field_definition, no audit ci_instance_field.*), the legacy
// paths PUT /ci-types/{id}/fields and PUT /ci-fields/global, a set
// assignment (field attribute_set_id) and the direct publishing of a
// template version; schema_version stays. An instance definition that
// narrows max is stored (200), and a CI PATCH above it is 422 with the
// attribute in violations[].field.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCheckCompositionAgainstPostgres(t *testing.T) {
	e := newComposeEnv(t)
	bg := context.Background()
	errRollback := errors.New("rollback")
	inTx := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := database.WithRequestTenant(e.ctx(e.org), e.f.App, e.org, fn); !errors.Is(err, errRollback) {
			t.Fatalf("transaction: %v", err)
		}
	}
	codes := func(conflicts []*citype.CompositionError) map[string]string {
		out := map[string]string{}
		for _, c := range conflicts {
			out[c.Key] = c.Code
		}
		return out
	}

	// A type with two conflicts in a draft: both are reported.
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		for _, body := range []string{`'wp252_num', 'text', '{}'`, `'wp252_len', 'text', '{"max_length": 1}'`} {
			if _, err := tx.Exec(ctx, `INSERT INTO attribute_definition (organization_id, scope, key, data_type, validation)
				VALUES ($1, 'global', `+body+`::jsonb)`, e.org); err != nil {
				return err
			}
		}
		conflicts, err := citype.CheckComposition(ctx, tx, citype.CompositionScope{TypeID: e.leaf.ID})
		if err != nil {
			return err
		}
		want := map[string]string{"wp252_num": citype.CodeDatatypeConflict, "wp252_len": citype.CodeLengthRangeEmpty}
		if got := codes(conflicts); !reflect.DeepEqual(got, want) || len(conflicts) != 2 {
			t.Errorf("type with two conflicts: %v, want %v", got, want)
		}
		return errRollback
	})
	var leftover int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition
		WHERE organization_id = $1 AND scope = 'global' AND key LIKE 'wp252_%'`, e.org).Scan(&leftover); err != nil || leftover != 0 {
		t.Errorf("after the rollback %d organization globals (%v), want none", leftover, err)
	}
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		conflicts, err := citype.CheckComposition(ctx, tx, citype.CompositionScope{TypeID: e.probe.ID})
		if err != nil || len(conflicts) != 0 {
			t.Errorf("committed probe type: %v %v, want no conflict", conflicts, err)
		}
		return errRollback
	})

	// A template version with a conflict: its code, before and at the
	// direct publishing.
	enumDef := e.typeAttribute(e.root.ID, "wp252_enum")
	tpl := e.createTemplate(e.org, e.leaf.ID, "wp252-tpl")
	var draft citype.TemplateVersion
	e.must(http.StatusOK, e.org, http.MethodPut, versionPath(tpl.ID, 1),
		`{"items":[{"attribute_definition_id":"`+enumDef+`","position":0,"validation_override":{"options":["a"]}}]}`, &draft)
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		conflicts, err := citype.CheckComposition(ctx, tx, citype.CompositionScope{TypeID: e.leaf.ID, TemplateVersionIDs: []string{draft.ID}})
		if err != nil {
			return err
		}
		if got := codes(conflicts); !reflect.DeepEqual(got, map[string]string{"wp252_enum": citype.CodeEnumEmpty}) {
			t.Errorf("template version: %v, want enum-empty on wp252_enum", got)
		}
		return errRollback
	})
	wantComposed422(t, "publish", e.call(e.org, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", ""), "wp252_enum", citype.CodeEnumEmpty)

	// The layers checked when written; schema_version stays.
	probeVersion, leafVersion := e.version(e.probe.ID), e.version(e.leaf.ID)
	ciID := e.f.CI(t, e.org, e.f.Client1, "wp252-instance-ci")
	if _, err := e.f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciID, e.probe.ID); err != nil {
		t.Fatal(err)
	}
	fields := "/api/v1/cis/" + ciID + "/field-definitions"
	wantComposed422(t, "instance definition", e.call(e.org, http.MethodPut, fields, `{"name":"wp252_num","data_type":"text"}`),
		"name", citype.CodeDatatypeConflict)
	var rows, audits int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_instance_field_definition WHERE ci_id = $1`, ciID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("refused instance definition stored %d rows (%v)", rows, err)
	}
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action LIKE 'ci_instance_field.%'`,
		ciID).Scan(&audits); err != nil || audits != 0 {
		t.Errorf("refused instance definition audited %d times (%v)", audits, err)
	}
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		conflicts, err := citype.CheckComposition(ctx, tx, citype.CompositionScope{CIID: ciID})
		if err != nil || len(conflicts) != 0 {
			t.Errorf("CI scope: %v %v, want no conflict", conflicts, err)
		}
		return errRollback
	})
	wantComposed422(t, "PUT /ci-types/{id}/fields", e.call(e.org, http.MethodPut, "/api/v1/ci-types/"+e.probe.ID+"/fields",
		`{"name":"wp252_enum","data_type":"text"}`), "key", citype.CodeDatatypeConflict)
	wantComposed422(t, "PUT /ci-fields/global", e.call(e.org, http.MethodPut, "/api/v1/ci-fields/global",
		`{"name":"wp252_num","data_type":"text"}`), "key", citype.CodeDatatypeConflict)
	set3 := e.createSet(e.org, "wp252-set-3")
	e.mustSetAttribute(e.org, set3.ID, `{"key":"wp252_enum","data_type":"enum","enum_values":["z"]}`)
	wantComposed422(t, "set assignment", e.call(e.org, http.MethodPut, "/api/v1/ci-types/"+e.leaf.ID+"/attribute-sets",
		assignment(e.set1.ID, e.set2.ID, set3.ID)), "attribute_set_id", citype.CodeEnumEmpty)
	if e.version(e.probe.ID) != probeVersion || e.version(e.leaf.ID) != leafVersion {
		t.Errorf("refused writes changed schema_version: probe %d leaf %d, want %d %d",
			e.version(e.probe.ID), e.version(e.leaf.ID), probeVersion, leafVersion)
	}

	// A narrowing instance definition refines the attribute as the last
	// layer: stored, and the CI is validated against it.
	e.must(http.StatusOK, e.org, http.MethodPut, fields, `{"name":"wp252_num","data_type":"decimal","validation":{"max":20}}`, nil)
	w := e.callAs(e.managerCtx(), http.MethodPatch, "/api/v1/cis/"+ciID, `{"attributes":{"wp252_num":30}}`)
	var problem struct {
		Violations []struct {
			Field string `json:"field"`
		} `json:"violations"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	if w.Code != http.StatusUnprocessableEntity || len(problem.Violations) == 0 || problem.Violations[0].Field != "wp252_num" {
		t.Errorf("CI PATCH above the instance max: %d %s, want 422 on wp252_num", w.Code, w.Body.String())
	}
	if w = e.callAs(e.managerCtx(), http.MethodPatch, "/api/v1/cis/"+ciID, `{"attributes":{"wp252_num":10}}`); w.Code != http.StatusOK {
		t.Errorf("CI PATCH within the instance max: %d %s", w.Code, w.Body.String())
	}
}

// typeAttribute is the id of the definition key of typeID.
func (e *composeEnv) typeAttribute(typeID, key string) string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(),
		`SELECT id::text FROM attribute_definition WHERE ci_type_id = $1 AND key = $2`, typeID, key).Scan(&id); err != nil {
		e.t.Fatalf("definition %s: %v", key, err)
	}
	return id
}
