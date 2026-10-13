package citype_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
