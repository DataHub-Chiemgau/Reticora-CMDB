package citype

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAttributeDefinitionsAgainstPostgres covers WP-116 (DB-03, MET-10,
// API-05) against PostgreSQL: attribute_definition is the single source of
// type and global attributes (the legacy field endpoints write it too, no
// carrier types remain); /attribute-definitions creates, changes and
// deletes definitions with every MET-10 field; ci_type.attribute_schema is
// generated (JSON Schema 2020-12) with a rising schema_version and cannot
// be written through the API or the database; system definitions are
// read-only; other organizations see nothing.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeDefinitionsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	repo := NewPGRepository(f.App)
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	ctxFor := func(orgID string) context.Context {
		return tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})
	}
	orgA, orgB := ctxFor(f.OrgA), ctxFor(f.OrgB)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	getType := func(id string) *Type {
		t.Helper()
		typ, err := repo.GetByID(orgA, f.OrgA, id)
		if err != nil {
			t.Fatalf("get type: %v", err)
		}
		return typ
	}

	typ := &Type{OrganizationID: f.OrgA, Key: "wp116-router", Name: "wp116-router", IsObservable: true}
	if err := repo.Create(orgA, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	if got := getType(typ.ID); got.SchemaVersion != 1 || got.AttributeSchema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("new type schema v%d %v", got.SchemaVersion, got.AttributeSchema)
	}

	// Create with every MET-10 field.
	w := call(orgA, http.MethodPost, "/api/v1/attribute-definitions", `{
		"ci_type_id": "`+typ.ID+`", "key": "port_count", "display_name": {"de": "Portanzahl", "en": "Port count"},
		"description": "Physical ports", "data_type": "integer", "required": true, "default": 24,
		"multi_value": false, "searchable": true, "unique": false, "unique_scope": "client",
		"inheritable": false, "validation": {"min": 1, "max": 512}, "group": "hardware", "order": 3, "unit": "ports"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var def AttributeDefinition
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Scope != "type" || def.OrganizationID != f.OrgA || def.DisplayName["de"] != "Portanzahl" || string(def.Default) != "24" ||
		!def.Searchable || def.UniqueScope != "client" || def.Inheritable || def.Group != "hardware" || def.Order != 3 ||
		def.Unit != "ports" || def.IsSystem {
		t.Errorf("created = %+v", def)
	}

	generated := getType(typ.ID)
	props, _ := generated.AttributeSchema["properties"].(map[string]any)
	port, _ := props["port_count"].(map[string]any)
	if generated.SchemaVersion != 2 || port["type"] != "integer" || port["minimum"] != float64(1) || port["x-unit"] != "ports" ||
		port["default"] != float64(24) || port["title"] != "Port count" {
		t.Errorf("generated schema v%d port_count %v", generated.SchemaVersion, port)
	}
	if len(generated.RequiredFields) != 1 || generated.RequiredFields[0] != "port_count" {
		t.Errorf("required_fields = %v", generated.RequiredFields)
	}

	// Change and delete regenerate with a new version.
	if rec := call(orgA, http.MethodPatch, "/api/v1/attribute-definitions/"+def.ID, `{"required": false, "multi_value": true}`); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	changed := getType(typ.ID)
	port = changed.AttributeSchema["properties"].(map[string]any)["port_count"].(map[string]any)
	if changed.SchemaVersion != 3 || port["type"] != "array" || len(changed.RequiredFields) != 0 {
		t.Errorf("after patch v%d %v required %v", changed.SchemaVersion, port, changed.RequiredFields)
	}

	// The legacy field endpoint writes the same source.
	if _, err := repo.UpsertField(orgA, f.OrgA, typ.ID, UpsertFieldRequest{Scope: "type", Name: "mgmt_ip", Label: "Management IP", DataType: "ip"}); err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}
	if _, err := repo.UpsertField(orgA, f.OrgA, "", UpsertFieldRequest{Scope: "global", Name: "cost_center", DataType: "text"}); err != nil {
		t.Fatalf("legacy global upsert: %v", err)
	}
	var carriers, globals int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_type WHERE key = '__global_attributes__'`).Scan(&carriers); err != nil || carriers != 0 {
		t.Errorf("carrier types = %d (%v)", carriers, err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND ci_type_id IS NULL
		AND scope = 'global' AND key = 'cost_center'`, f.OrgA).Scan(&globals); err != nil || globals != 1 {
		t.Errorf("global definitions = %d (%v)", globals, err)
	}
	if getType(typ.ID).SchemaVersion != 4 {
		t.Errorf("legacy field did not regenerate the schema")
	}
	resolved, err := ResolveFields(orgA, repo, f.OrgA, typ.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range resolved {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "cost_center,mgmt_ip,port_count" {
		t.Errorf("resolved fields = %v", names)
	}

	if rec := call(orgA, http.MethodDelete, "/api/v1/attribute-definitions/"+def.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if after := getType(typ.ID); after.SchemaVersion != 5 || after.AttributeSchema["properties"].(map[string]any)["port_count"] != nil {
		t.Errorf("after delete v%d", after.SchemaVersion)
	}

	// The generated schema is read-only: not part of the type API, and the
	// database rejects direct writes.
	if rec := call(orgA, http.MethodPatch, "/api/v1/ci-types/"+typ.ID, `{"attribute_schema": {"type": "object"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH attribute_schema: %d, want 400", rec.Code)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci_type SET attribute_schema = '{}' WHERE id = $1`, typ.ID); err == nil {
		t.Error("direct write of attribute_schema accepted")
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci_type SET schema_version = 99 WHERE id = $1`, typ.ID); err == nil {
		t.Error("direct write of schema_version accepted")
	}

	// Validation and conflicts.
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"invalid default":  {`{"ci_type_id":"` + typ.ID + `","key":"x1","data_type":"integer","default":"many"}`, http.StatusBadRequest},
		"reserved key":     {`{"ci_type_id":"` + typ.ID + `","key":"_instance","data_type":"text"}`, http.StatusBadRequest},
		"unknown language": {`{"ci_type_id":"` + typ.ID + `","key":"x2","data_type":"text","display_name":{"fr":"X"}}`, http.StatusBadRequest},
		"unique scope":     {`{"ci_type_id":"` + typ.ID + `","key":"x3","data_type":"text","unique_scope":"site"}`, http.StatusBadRequest},
		"duplicate key":    {`{"ci_type_id":"` + typ.ID + `","key":"mgmt_ip","data_type":"text"}`, http.StatusConflict},
		"unknown type":     {`{"ci_type_id":"` + f.ID() + `","key":"x4","data_type":"text"}`, http.StatusUnprocessableEntity},
	} {
		if rec := call(orgA, http.MethodPost, "/api/v1/attribute-definitions", tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), tc.status)
		}
	}
	var systemType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id IS NULL AND key = 'switch'`).Scan(&systemType); err != nil {
		t.Fatal(err)
	}
	if rec := call(orgA, http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+systemType+`","key":"x5","data_type":"text"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("attribute on a system type: %d, want 422", rec.Code)
	}

	// System definitions: visible to everyone, read-only.
	var systemDef string
	if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type)
		VALUES (NULL, NULL, 'global', 'wp116_asset_tag', 'text') RETURNING id::text`).Scan(&systemDef); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM attribute_definition WHERE id = $1`, systemDef)
	})
	w = call(orgA, http.MethodGet, "/api/v1/attribute-definitions?scope=global", "")
	if !strings.Contains(w.Body.String(), `"key":"wp116_asset_tag"`) || !strings.Contains(w.Body.String(), `"is_system":true`) {
		t.Errorf("system definition not listed: %s", w.Body.String())
	}
	if rec := call(orgA, http.MethodPatch, "/api/v1/attribute-definitions/"+systemDef, `{"required": true}`); rec.Code != http.StatusForbidden {
		t.Errorf("patch system definition: %d, want 403", rec.Code)
	}
	if rec := call(orgA, http.MethodDelete, "/api/v1/attribute-definitions/"+systemDef, ""); rec.Code != http.StatusForbidden {
		t.Errorf("delete system definition: %d, want 403", rec.Code)
	}

	// Organization B sees the system definition but none of A's.
	w = call(orgB, http.MethodGet, "/api/v1/attribute-definitions", "")
	if strings.Contains(w.Body.String(), "cost_center") || strings.Contains(w.Body.String(), "mgmt_ip") {
		t.Errorf("organization B sees definitions of A: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "wp116_asset_tag") {
		t.Error("organization B does not see the system definition")
	}
}
