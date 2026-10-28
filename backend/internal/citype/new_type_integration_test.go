package citype_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestNewTypeWithoutReleaseAgainstPostgres covers WP-119 (MET-04, MET-16)
// against PostgreSQL: a type created through the API is usable at once —
// attributes, materialized schema and CIs — without a migration or a
// release. The materialized schema composes the system base with the
// type's attributes by the rules of MET-16 and is versioned; conflicting
// definitions are rejected when they are published; every CI records the
// schema version it was written against.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNewTypeWithoutReleaseAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "8a")
	bg := context.Background()
	repo := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	typeOf := func(id string) *citype.Type {
		t.Helper()
		typ, err := repo.GetByID(ctx, f.OrgA, id)
		if err != nil {
			t.Fatalf("get type: %v", err)
		}
		return typ
	}
	property := func(typ *citype.Type, key string) map[string]any {
		props, _ := typ.AttributeSchema["properties"].(map[string]any)
		p, _ := props[key].(map[string]any)
		return p
	}

	// The system base: definitions of the system for every type.
	system := func(key, dataType, extra string) {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_definition
				(organization_id, ci_type_id, scope, key, data_type, enum_values, validation)
			VALUES (NULL, NULL, 'global', $1, $2, $3::jsonb, $4::jsonb) RETURNING id::text`,
			key, dataType, nullJSON(extra, "enum_values"), validationJSON(extra)).Scan(&id); err != nil {
			t.Fatalf("system definition %s: %v", key, err)
		}
		t.Cleanup(func() {
			_, _ = f.Admin.Exec(context.Background(), `DELETE FROM attribute_definition WHERE id = $1`, id)
		})
	}
	system("wp119_class", "enum", `{"enum_values": ["a", "b", "c"], "pattern": "^[a-z]$"}`)
	system("wp119_units", "text", "")

	// MET-04: a new type through the API, at once with the system base.
	w := call(http.MethodPost, "/api/v1/ci-types", `{"key": "wp119-sensor", "name": "WP-119 Sensor"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create type: %d %s", w.Code, w.Body.String())
	}
	var created citype.Type
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	typ := typeOf(created.ID)
	if typ.SchemaVersion != 1 || property(typ, "wp119_class")["type"] != "string" {
		t.Fatalf("new type v%d schema %v", typ.SchemaVersion, typ.AttributeSchema)
	}

	// The type's attribute composes with the base: allowed values
	// intersected, required OR-ed, both patterns apply.
	w = call(http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id": "`+typ.ID+`", "key": "wp119_class",
		"data_type": "enum", "enum_values": ["b", "c", "d"], "required": true, "validation": {"pattern": "^[ab]$"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create attribute: %d %s", w.Code, w.Body.String())
	}
	var classDef citype.AttributeDefinition
	if err := json.Unmarshal(w.Body.Bytes(), &classDef); err != nil {
		t.Fatal(err)
	}
	typ = typeOf(typ.ID)
	class := property(typ, "wp119_class")
	allOf, _ := class["allOf"].([]any)
	if typ.SchemaVersion != 2 || !reflect.DeepEqual(class["enum"], []any{"b", "c"}) || class["pattern"] != "^[a-z]$" ||
		len(allOf) != 1 || !reflect.DeepEqual(typ.RequiredFields, []string{"wp119_class"}) {
		t.Errorf("composed schema v%d %v required %v", typ.SchemaVersion, class, typ.RequiredFields)
	}

	// Conflicts are publish errors and leave the schema unchanged.
	for name, tc := range map[string]struct{ method, path, body string }{
		"data type against the base": {http.MethodPost, "/api/v1/attribute-definitions",
			`{"ci_type_id": "` + typ.ID + `", "key": "wp119_units", "data_type": "integer"}`},
		"no allowed value left": {http.MethodPatch, "/api/v1/attribute-definitions/" + classDef.ID, `{"enum_values": ["d"]}`},
	} {
		if rec := call(tc.method, tc.path, tc.body); rec.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(rec.Body.String(), "MET-16") {
			t.Errorf("%s: %d %s, want 422", name, rec.Code, rec.Body.String())
		}
	}
	// The organization's global attributes compose as well (checked when
	// published).
	if rec := call(http.MethodPost, "/api/v1/attribute-definitions", `{"key": "wp119_slot", "data_type": "integer"}`); rec.Code != http.StatusCreated {
		t.Fatalf("org global: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id": "`+typ.ID+`", "key": "wp119_slot", "data_type": "text"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("type attribute against the org global: %d %s, want 422", rec.Code, rec.Body.String())
	}
	if after := typeOf(typ.ID); after.SchemaVersion != 2 {
		t.Errorf("rejected definitions changed the schema: v%d", after.SchemaVersion)
	}

	// A CI of the new type validates against the composed schema and
	// records its version.
	cis := ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(repo)
	item := &ci.Item{OrganizationID: f.OrgA, CITypeID: typ.ID, Name: "wp119-ci", Status: "active",
		Attributes: map[string]any{"wp119_class": "c"}}
	var invalid *ci.ValidationError
	if err := cis.Create(ctx, item); !errors.As(err, &invalid) {
		t.Fatalf("value outside the type's pattern: %v, want a validation error", err)
	}
	item.Attributes["wp119_class"] = "b"
	if err := cis.Create(ctx, item); err != nil {
		t.Fatalf("create CI: %v", err)
	}
	if item.SchemaVersion != 2 {
		t.Errorf("created CI schema_version = %d, want 2", item.SchemaVersion)
	}

	// A new schema version applies to the CI with its next write.
	if rec := call(http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id": "`+typ.ID+`", "key": "wp119_note", "data_type": "multiline"}`); rec.Code != http.StatusCreated {
		t.Fatalf("second attribute: %d %s", rec.Code, rec.Body.String())
	}
	if v := typeOf(typ.ID).SchemaVersion; v != 3 {
		t.Fatalf("schema version after the second attribute = %d, want 3", v)
	}
	got, err := cis.GetByID(ctx, f.OrgA, item.ID)
	if err != nil || got.SchemaVersion != 2 {
		t.Fatalf("CI before its next write: v%d %v", got.SchemaVersion, err)
	}
	updated, err := cis.Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{Attributes: map[string]any{"wp119_note": "line 1\nline 2"}})
	if err != nil {
		t.Fatalf("update CI: %v", err)
	}
	if updated.SchemaVersion != 3 {
		t.Errorf("updated CI schema_version = %d, want 3", updated.SchemaVersion)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET schema_version = 99 WHERE id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := f.Admin.QueryRow(bg, `SELECT schema_version FROM ci WHERE id = $1`, item.ID).Scan(&stored); err != nil || stored != 3 {
		t.Errorf("schema_version written directly: %d (%v), want it kept at 3", stored, err)
	}
}

// TestOrgGlobalDefinitionsAreRuntimeLayer covers MET-16 (WP-590, F-308)
// by decision E-93 (transitional rule, option 2) against PostgreSQL: an
// organization-global attribute definition (organization_id set, no CI
// type) is a validated runtime layer. The CI service enforces it on every
// write of the organization — a required one answers 422 on the attribute
// without a value and 201 with one, on an own and on a system type — but
// it is not materialized into a type's schema and does not enter
// schema_version: neither the organization's type nor the system type,
// materialized for every organization, gets the property or a new version,
// and the CI references the version of base and type. Another
// organization's CIs of the same system type are not bound by it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrgGlobalDefinitionsAreRuntimeLayer(t *testing.T) {
	f := scopetest.Seed(t, "8c")
	bg := context.Background()
	repo := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(repo)).RegisterRoutes(mux)
	ctxA := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ctxB := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	schemaOf := func(id string) (version int, props map[string]any, required []string) {
		t.Helper()
		typ, err := repo.GetByID(ctxA, f.OrgA, id)
		if err != nil {
			t.Fatalf("get type: %v", err)
		}
		props, _ = typ.AttributeSchema["properties"].(map[string]any)
		return typ.SchemaVersion, props, typ.RequiredFields
	}
	violationFields := func(out map[string]any) []string {
		var fields []string
		violations, _ := out["violations"].([]any)
		for _, v := range violations {
			if m, ok := v.(map[string]any); ok {
				field, _ := m["field"].(string)
				fields = append(fields, field)
			}
		}
		return fields
	}

	code, out := call(ctxA, http.MethodPost, "/api/v1/ci-types", `{"key": "wp590-probe", "name": "WP-590 Probe"}`)
	own, _ := out["id"].(string)
	if code != http.StatusCreated || own == "" {
		t.Fatalf("create type: %d %v", code, out)
	}
	var system string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&system); err != nil {
		t.Fatalf("system type: %v", err)
	}
	ownBefore, _, _ := schemaOf(own)
	systemBefore, _, _ := schemaOf(system)

	// A required organization-global definition of organization A.
	code, out = call(ctxA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"key": "wp590_cost_center", "data_type": "text", "required": true}`)
	defID, _ := out["id"].(string)
	if code != http.StatusCreated || defID == "" || out["ci_type_id"] != nil {
		t.Fatalf("org-global definition: %d %v", code, out)
	}

	// Not materialized, not versioned.
	types := []struct {
		name, id string
		before   int
	}{{"own-type", own, ownBefore}, {"system-type", system, systemBefore}}
	for _, tc := range types {
		version, props, required := schemaOf(tc.id)
		if _, ok := props["wp590_cost_center"]; ok || version != tc.before || slices.Contains(required, "wp590_cost_center") {
			t.Errorf("%s: v%d (was v%d), property present %v, required %v; want the runtime layer outside the schema",
				tc.name, version, tc.before, ok, required)
		}
	}

	// Enforced on every CI write of organization A: 422 without a value,
	// 201 with one; the CI references the version of base and type.
	var ciID string
	for _, tc := range types {
		code, out = call(ctxA, http.MethodPost, "/api/v1/cis", `{"name": "wp590-`+tc.name+`", "ci_type_id": "`+tc.id+`"}`)
		if code != http.StatusUnprocessableEntity || !slices.Contains(violationFields(out), "wp590_cost_center") {
			t.Errorf("%s without the value: %d %v, want 422 on wp590_cost_center", tc.name, code, out)
		}
		code, out = call(ctxA, http.MethodPost, "/api/v1/cis", `{"name": "wp590-`+tc.name+`", "ci_type_id": "`+tc.id+`",
			"attributes": {"wp590_cost_center": "CC-590"}}`)
		if code != http.StatusCreated || out["schema_version"] != float64(tc.before) {
			t.Errorf("%s with the value: %d %v, want 201 with schema_version %d", tc.name, code, out, tc.before)
		}
		ciID, _ = out["id"].(string)
	}

	// A write that drops the value is refused as well.
	code, out = call(ctxA, http.MethodPatch, "/api/v1/cis/"+ciID, `{"attributes": {"wp590_cost_center": null}}`)
	if code != http.StatusUnprocessableEntity || !slices.Contains(violationFields(out), "wp590_cost_center") {
		t.Errorf("PATCH removing the value: %d %v, want 422 on wp590_cost_center", code, out)
	}

	// A change of the organization-global definition leaves the versions
	// alone; the type's own definitions version the type.
	if code, out = call(ctxA, http.MethodPatch, "/api/v1/attribute-definitions/"+defID, `{"description": "WP-590"}`); code != http.StatusOK {
		t.Fatalf("patch org-global definition: %d %v", code, out)
	}
	if version, _, _ := schemaOf(own); version != ownBefore {
		t.Errorf("own type after the org-global change: v%d, want v%d", version, ownBefore)
	}
	if code, out = call(ctxA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id": "`+own+`", "key": "wp590_rack", "data_type": "text"}`); code != http.StatusCreated {
		t.Fatalf("type definition: %d %v", code, out)
	}
	if version, _, _ := schemaOf(own); version != ownBefore+1 {
		t.Errorf("own type after its own definition: v%d, want v%d", version, ownBefore+1)
	}

	// Organization B's CIs of the same system type are not bound by it.
	if code, out = call(ctxB, http.MethodPost, "/api/v1/cis", `{"name": "wp590-other-org", "ci_type_id": "`+system+`"}`); code != http.StatusCreated {
		t.Errorf("organization B without the value: %d %v, want 201", code, out)
	}
}

// TestAttributeDefinitionASTValidationCIWrites covers WP-589 (MET-10,
// MET-45, F-307) against PostgreSQL over HTTP: a definition whose
// validation is the rule AST range 1–5 (POST /attribute-definitions)
// applies to CI writes, evaluated bindingly in Go: POST /cis with the
// value 7 is 422 naming the attribute, with 3 it is 201; PATCH to 7 is
// 422 and leaves the value. TestAttributeDefinitionASTValidation
// (attrdef_integration_test.go) covers the definition API.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeDefinitionASTValidationCIWrites(t *testing.T) {
	f := scopetest.Seed(t, "8a")
	repo := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(repo)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp589-sensor", Name: "wp589-sensor", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	if w := call(http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typ.ID+`","key":"wp589_level",
		"data_type":"integer","validation":{"op":"range","min":1,"max":5}}`); w.Code != http.StatusCreated {
		t.Fatalf("definition with a rule AST: %d %s, want 201", w.Code, w.Body.String())
	}

	w := call(http.MethodPost, "/api/v1/cis", `{"name":"wp589-out","ci_type_id":"`+typ.ID+`","client_id":"`+f.Client1+`",
		"attributes":{"wp589_level":7}}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "wp589_level") {
		t.Errorf("CI with 7 outside the range 1..5: %d %s, want 422 naming wp589_level", w.Code, w.Body.String())
	}
	w = call(http.MethodPost, "/api/v1/cis", `{"name":"wp589-in","ci_type_id":"`+typ.ID+`","client_id":"`+f.Client1+`",
		"attributes":{"wp589_level":3}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("CI with 3 inside the range 1..5: %d %s, want 201", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPatch, "/api/v1/cis/"+created.ID, `{"attributes":{"wp589_level":7}}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH to 7: %d %s, want 422", w.Code, w.Body.String())
	}
	var level float64
	if err := f.Admin.QueryRow(context.Background(), `SELECT (attributes ->> 'wp589_level')::numeric FROM ci WHERE id = $1`,
		created.ID).Scan(&level); err != nil || level != 3 {
		t.Errorf("stored level = %v (%v), want 3", level, err)
	}
}

// nullJSON returns the value of key in the JSON object extra, or nil.
func nullJSON(extra, key string) any {
	if extra == "" {
		return nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(extra), &m)
	if raw, ok := m[key]; ok {
		return string(raw)
	}
	return nil
}

// validationJSON is extra without enum_values: the validation object.
func validationJSON(extra string) string {
	if extra == "" {
		return "{}"
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(extra), &m)
	delete(m, "enum_values")
	raw, _ := json.Marshal(m)
	return string(raw)
}
