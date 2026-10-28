package citype

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
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

// TestAttributeUniqueFlagConflict covers WP-511 (MET-15, DB-04, F-159)
// against PostgreSQL over HTTP: PATCH /attribute-definitions/{id} with
// unique=true while active CIs share a value is 409 with the reason on the
// field unique; the definition stays non-unique and no index job is queued.
// Without duplicates (a soft-deleted CI does not count) the same PATCH is
// 200 and queues the attribute.index job of the definition, which builds
// idx_attr_<id>_u. The CI type admin renders that reason (CITypeAdminPage).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeUniqueFlagConflict(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ddl := NewDDLJob(f.App, svc)
	repo := NewPGRepository(f.App).WithIndexScheduler(ddl)
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	patch := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/attribute-definitions/"+id, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	queued := func() []jobs.Job {
		t.Helper()
		list, _, err := svc.List(bg, f.OrgA, jobs.Filter{Kind: IndexJobKind}, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	typ := &Type{OrganizationID: f.OrgA, Key: "wp511-server", Name: "wp511-server", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	def, uerr := repo.UpsertField(ctx, f.OrgA, typ.ID, UpsertFieldRequest{Scope: "type", Name: "inventory_no", DataType: "text"})
	if uerr != nil || def.ID == "" {
		t.Fatalf("upsert field: %+v %v", def, uerr)
	}
	t.Cleanup(func() {
		for _, unique := range []bool{false, true} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+IndexName(def.ID, unique))
		}
	})
	var duplicates []string
	for _, name := range []string{"wp511-a", "wp511-b"} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
			VALUES ($1, $2, $3, $4, 'active', '{"inventory_no": "INV-1"}') RETURNING id::text`,
			f.OrgA, f.Client1, typ.ID, name).Scan(&id); err != nil {
			t.Fatalf("seed CI: %v", err)
		}
		duplicates = append(duplicates, id)
	}
	before := len(queued())

	// Duplicates: 409 with the reason on the field unique, nothing changed.
	w := patch(def.ID, `{"unique": true, "searchable": false, "unique_scope": "org"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("PATCH unique over duplicates: %d %s, want 409", w.Code, w.Body.String())
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
		Field  string `json:"field"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusConflict || problem.Field != "unique" || !strings.Contains(problem.Detail, "already share a value") {
		t.Errorf("409 problem = %+v, want the reason on unique", problem)
	}
	stored, gerr := repo.GetAttributeDefinition(ctx, f.OrgA, def.ID)
	if gerr != nil || stored.Unique {
		t.Errorf("definition after the refused PATCH = %+v %v, want not unique", stored, gerr)
	}
	if n := len(queued()); n != before {
		t.Errorf("%d index jobs after the refused PATCH, want %d", n, before)
	}

	// Without duplicates (the soft-deleted CI does not count): 200, the
	// index job of the definition is queued and builds the unique index.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, duplicates[1]); err != nil {
		t.Fatal(err)
	}
	w = patch(def.ID, `{"unique": true, "searchable": false, "unique_scope": "org"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH unique without duplicates: %d %s, want 200", w.Code, w.Body.String())
	}
	var updated AttributeDefinition
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Unique || updated.UniqueScope != "org" {
		t.Errorf("updated = %+v, want unique per org", updated)
	}
	list := queued()
	if len(list) != before+1 || list[0].Status != jobs.StatusQueued || !strings.Contains(string(list[0].Params), def.ID) {
		t.Fatalf("index jobs after the PATCH = %+v, want a queued job of %s", list, def.ID)
	}
	if _, err := ddl.RunDue(bg); err != nil {
		t.Fatalf("run index jobs: %v", err)
	}
	if list = queued(); list[0].Status != jobs.StatusSucceeded {
		t.Fatalf("index job = %s %s, want succeeded", list[0].Status, list[0].Error)
	}
	if got := readIndex(t, f.Admin, IndexName(def.ID, true)); !got.valid || !got.unique {
		t.Errorf("unique index = %+v", got)
	}
}

// TestAttributeDefinitionListPages covers WP-565 (API-02, F-215): GET
// /attribute-definitions pages newest first on (created_at, id) with its
// filters. ?limit=1 answers one entry and a next_cursor; following the
// cursors returns every definition of the type once. Without filter the
// pages of 200 hold the organization's and the system's definitions, none
// of another organization, each once. A forged cursor is 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeDefinitionListPages(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	repo := NewPGRepository(f.App)
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	ctxFor := func(orgID string) context.Context {
		return tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})
	}
	orgA := ctxFor(f.OrgA)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	for _, org := range []string{f.OrgA, f.OrgB} {
		typ := &Type{OrganizationID: org, Key: "wp565-switch", Name: "wp565-switch"}
		if err := repo.Create(ctxFor(org), typ); err != nil {
			t.Fatalf("create type: %v", err)
		}
		for _, key := range []string{"ports", "uplinks", "firmware"} {
			if w := call(ctxFor(org), http.MethodPost, "/api/v1/attribute-definitions",
				`{"ci_type_id":"`+typ.ID+`","key":"`+key+`","display_name":{"en":"`+key+`"},"data_type":"string"}`); w.Code != http.StatusCreated {
				t.Fatalf("create %s: %d %s", key, w.Code, w.Body.String())
			}
		}
	}
	var typeA string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id = $1 AND key = 'wp565-switch'`, f.OrgA).Scan(&typeA); err != nil {
		t.Fatal(err)
	}
	ids := func(query string, args ...any) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	// pages reads the list along next_cursor with the given limit.
	pages := func(path string, limit int) (got []string, total int) {
		t.Helper()
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		base := path + sep + "limit=" + strconv.Itoa(limit)
		for next := base; next != ""; {
			w := call(orgA, http.MethodGet, next, "")
			var page struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Total      int    `json:"total"`
				Limit      int    `json:"limit"`
				NextCursor string `json:"next_cursor"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &page); w.Code != http.StatusOK || err != nil || len(page.Data) == 0 ||
				len(page.Data) > limit || page.Limit != limit {
				t.Fatalf("GET %s: %d %.300s; want 1 to %d entries", next, w.Code, w.Body.String(), limit)
			}
			for _, d := range page.Data {
				got = append(got, d.ID)
			}
			total = page.Total
			if len(got) < total && page.NextCursor == "" {
				t.Fatalf("GET %s: %d of %d entries without next_cursor", next, len(got), total)
			}
			if len(got) > total {
				t.Fatalf("GET %s: more entries than the total %d", next, total)
			}
			next = ""
			if page.NextCursor != "" {
				next = base + "&cursor=" + url.QueryEscape(page.NextCursor)
			}
		}
		return got, total
	}

	want := ids(`SELECT id::text FROM attribute_definition WHERE ci_type_id = $1 ORDER BY created_at DESC, id DESC`, typeA)
	if got, total := pages("/api/v1/attribute-definitions?ci_type_id="+typeA, 1); len(want) != 3 || !slices.Equal(got, want) || total != 3 {
		t.Errorf("definitions of the type with limit=1: %v (total %d), want %v newest first", got, total, want)
	}
	want = ids(`SELECT id::text FROM attribute_definition WHERE organization_id = $1 OR organization_id IS NULL
		ORDER BY created_at DESC, id DESC`, f.OrgA)
	if got, total := pages("/api/v1/attribute-definitions", 200); !slices.Equal(got, want) || total != len(want) {
		t.Errorf("every definition in pages of 200: %d rows (total %d), want the %d of the organization and the system newest first",
			len(got), total, len(want))
	}
	if w := call(orgA, http.MethodGet, "/api/v1/attribute-definitions?cursor=eyJpIjoiY2ktOSJ9.AAAAAAAAAAAAAAAAAAAAAA", ""); w.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d %.200s, want 400", w.Code, w.Body.String())
	}
}

// TestAttributeDefinitionASTValidation covers WP-589 (MET-10, MET-45,
// API-05, F-307) against PostgreSQL over HTTP: POST and PATCH
// /attribute-definitions accept validation as a rule AST of
// api/rules.schema.json, check it with internal/rules (an invalid rule is
// 400 on the field validation, nothing stored) and store the AST; the
// legacy structure stays valid input and is stored as its AST, and a
// definition stored in the legacy structure before is answered as its AST.
// The materialized schema reads the AST (migration 000184, the same
// translation as fieldmeta.ValidationFromRule), and the resolved
// definition the CI service validates against applies the rule.
// TestAttributeDefinitionASTValidationCIWrites (new_type_integration_test.go)
// proves the CI writes over HTTP (7 → 422, 3 → 201).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeDefinitionASTValidation(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	repo := NewPGRepository(f.App)
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	normalized := func(s string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return v
	}
	// stored is the validation column of a definition.
	stored := func(id string) any {
		t.Helper()
		var raw []byte
		if err := f.Admin.QueryRow(bg, `SELECT validation FROM attribute_definition WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return normalized(string(raw))
	}
	answered := func(w *httptest.ResponseRecorder) (id string, validation any) {
		t.Helper()
		var d struct {
			ID         string `json:"id"`
			Validation any    `json:"validation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d.ID, d.Validation
	}

	typ := &Type{OrganizationID: f.OrgA, Key: "wp589-switch", Name: "wp589-switch", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}

	// A rule AST: 201, answered and stored as sent; the materialized
	// schema carries the range.
	const levelAST = `{"op":"range","min":1,"max":5}`
	w := call(http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typ.ID+`","key":"wp589_level",
		"data_type":"integer","validation":`+levelAST+`}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST with a rule AST: %d %s, want 201", w.Code, w.Body.String())
	}
	levelID, got := answered(w)
	if !reflect.DeepEqual(got, normalized(levelAST)) || !reflect.DeepEqual(stored(levelID), normalized(levelAST)) {
		t.Errorf("rule AST answered %v, stored %v, want %s", got, stored(levelID), levelAST)
	}
	typeNow, err := repo.GetByID(ctx, f.OrgA, typ.ID)
	if err != nil {
		t.Fatal(err)
	}
	props, _ := typeNow.AttributeSchema["properties"].(map[string]any)
	level, _ := props["wp589_level"].(map[string]any)
	if level["minimum"] != float64(1) || level["maximum"] != float64(5) {
		t.Errorf("materialized schema of the AST = %v, want minimum 1 and maximum 5", level)
	}

	// The rule applies to the resolved definition the CI service checks
	// values against (Go evaluation, internal/rules).
	resolved, err := ResolveFields(ctx, repo, f.OrgA, typ.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, d := range resolved {
		if d.Name != "wp589_level" {
			continue
		}
		checked = true
		if verr := fieldmeta.ValidateValue(d, float64(7)); verr == nil {
			t.Error("value 7 accepted by the resolved range 1..5")
		}
		if verr := fieldmeta.ValidateValue(d, float64(3)); verr != nil {
			t.Errorf("value 3 refused by the resolved range 1..5: %v", verr)
		}
	}
	if !checked {
		t.Error("wp589_level is not resolved")
	}

	// Invalid rules: 400 on the field validation, nothing stored.
	for name, validation := range map[string]string{
		"unknown operator":       `{"op":"between","min":1}`,
		"range without bounds":   `{"op":"range"}`,
		"range min above max":    `{"op":"range","min":5,"max":1}`,
		"unknown property":       `{"op":"range","min":1,"minimum":1}`,
		"property of another op": `{"op":"pattern","pattern":"^a","min":1}`,
		"lookaround":             `{"op":"pattern","pattern":"(?=a)"}`,
		"empty enum":             `{"op":"enum","values":[]}`,
		"object in enum":         `{"op":"enum","values":[{"a":1}]}`,
		"nested invalid":         `{"op":"all","rules":[{"op":"required"},{"op":"range","of":"size","max":3}]}`,
		"no value left":          `{"op":"all","rules":[{"op":"enum","values":["a"]},{"op":"enum","values":["b"]}]}`,
		"empty range":            `{"op":"all","rules":[{"op":"range","max":2},{"op":"range","min":3}]}`,
		"not an object":          `"min 1"`,
		"unknown legacy field":   `{"minimum":1}`,
	} {
		rec := call(http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typ.ID+`","key":"wp589_invalid",
			"data_type":"integer","validation":`+validation+`}`)
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		if rec.Code != http.StatusBadRequest || problem.Field != "validation" {
			t.Errorf("%s: %d %s, want 400 on the field validation", name, rec.Code, rec.Body.String())
		}
	}
	var invalid int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition WHERE key = 'wp589_invalid'`).Scan(&invalid); err != nil || invalid != 0 {
		t.Errorf("%d definitions stored from invalid rules (%v)", invalid, err)
	}

	// The legacy structure: accepted, answered and stored as its AST.
	w = call(http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typ.ID+`","key":"wp589_name",
		"data_type":"text","validation":{"min_length":2,"max_length":8,"pattern":"^[a-z]","patterns":["[0-9]$"],"options":["ab1","cd2"]}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST with the legacy structure: %d %s, want 201", w.Code, w.Body.String())
	}
	const nameAST = `{"op":"all","rules":[{"op":"enum","values":["ab1","cd2"]},{"op":"pattern","pattern":"^[a-z]"},
		{"op":"pattern","pattern":"[0-9]$"},{"op":"range","of":"length","min":2,"max":8}]}`
	nameID, got := answered(w)
	if !reflect.DeepEqual(got, normalized(nameAST)) || !reflect.DeepEqual(stored(nameID), normalized(nameAST)) {
		t.Errorf("legacy structure answered %v, stored %v, want the AST %s", got, stored(nameID), nameAST)
	}
	// The materialized schema reads the stored AST (all of enum, patterns
	// and a length range).
	if typeNow, err = repo.GetByID(ctx, f.OrgA, typ.ID); err != nil {
		t.Fatal(err)
	}
	props, _ = typeNow.AttributeSchema["properties"].(map[string]any)
	name, _ := props["wp589_name"].(map[string]any)
	if name["minLength"] != float64(2) || name["maxLength"] != float64(8) || name["pattern"] != "^[a-z]" ||
		!reflect.DeepEqual(name["enum"], []any{"ab1", "cd2"}) || !reflect.DeepEqual(name["allOf"], []any{map[string]any{"pattern": "[0-9]$"}}) {
		t.Errorf("materialized schema of the stored AST = %v", name)
	}

	// PATCH with a rule AST; a required rule makes the definition required.
	const patchAST = `{"op":"all","rules":[{"op":"required"},{"op":"pattern","pattern":"^[a-z]+$"},
		{"op":"all","rules":[{"op":"range","of":"length","max":3},{"op":"enum","values":["ab","abc","abcd"]}]}]}`
	w = call(http.MethodPatch, "/api/v1/attribute-definitions/"+nameID, `{"validation":`+patchAST+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH with a rule AST: %d %s, want 200", w.Code, w.Body.String())
	}
	if _, got = answered(w); !reflect.DeepEqual(got, normalized(patchAST)) || !reflect.DeepEqual(stored(nameID), normalized(patchAST)) {
		t.Errorf("patched AST answered %v, stored %v, want %s", got, stored(nameID), patchAST)
	}
	patched, err := repo.GetAttributeDefinition(ctx, f.OrgA, nameID)
	if err != nil || !patched.Required {
		t.Errorf("definition with a required rule = %+v %v, want required", patched, err)
	}
	if w = call(http.MethodPatch, "/api/v1/attribute-definitions/"+nameID, `{"validation":{"op":"range","min":3,"max":1}}`); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with an invalid rule: %d %s, want 400", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(stored(nameID), normalized(patchAST)) {
		t.Errorf("refused PATCH changed the rule: %v", stored(nameID))
	}
	if w = call(http.MethodPatch, "/api/v1/attribute-definitions/"+nameID, `{"validation":null}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH validation null: %d %s", w.Code, w.Body.String())
	}
	if _, got = answered(w); got != nil || !reflect.DeepEqual(stored(nameID), map[string]any{}) {
		t.Errorf("after PATCH null: answered %v, stored %v, want no rule", got, stored(nameID))
	}

	// A definition stored in the legacy structure before is answered as
	// its AST; the legacy field view keeps the structure.
	var legacyID string
	if err = f.Admin.QueryRow(bg, `INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, validation)
		VALUES ($1, $2, 'type', 'wp589_legacy', 'decimal', '{"min": 0.5, "max": 9}') RETURNING id::text`,
		f.OrgA, typ.ID).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, "/api/v1/attribute-definitions/"+legacyID, "")
	if _, got = answered(w); w.Code != http.StatusOK || !reflect.DeepEqual(got, normalized(`{"op":"range","min":0.5,"max":9}`)) {
		t.Errorf("stored legacy structure answered %d %v, want its AST", w.Code, got)
	}
	fields, err := repo.ListFields(ctx, f.OrgA, typ.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if field.Name == "wp589_legacy" && (field.Validation == nil || field.Validation.Min == nil || *field.Validation.Min != 0.5 ||
			field.Validation.Max == nil || *field.Validation.Max != 9) {
			t.Errorf("legacy field view = %+v", field.Validation)
		}
	}

	// The database translates an AST like the Go evaluation.
	for _, ast := range []string{levelAST, nameAST, patchAST,
		`{"op":"all","rules":[{"op":"range","min":0},{"op":"range","min":2,"max":8},{"op":"range","of":"length","min":1.5,"max":9.7},
			{"op":"enum","values":[1,"a",true,3]},{"op":"enum","values":[3.0,1,"x"]}]}`,
		`{"op":"all","rules":[]}`, `{"op":"required"}`} {
		rule, err := fieldmeta.ParseValidation([]byte(ast))
		if err != nil {
			t.Fatalf("%s: %v", ast, err)
		}
		v, _, err := fieldmeta.ValidationFromRule(rule)
		if err != nil {
			t.Fatalf("%s: %v", ast, err)
		}
		goJSON := []byte(`{}`)
		if v != nil {
			goJSON, _ = json.Marshal(v)
		}
		var sqlJSON []byte
		if err := f.Admin.QueryRow(bg, `SELECT reticora_validation_legacy($1::jsonb)`, ast).Scan(&sqlJSON); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized(string(goJSON)), normalized(string(sqlJSON))) {
			t.Errorf("%s: Go %s, database %s", ast, goJSON, sqlJSON)
		}
	}
}
