package citype_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// globalAPI drives the CI type, attribute definition and CI API of WP-255
// (MET-13) against PostgreSQL, with the index job of DB-04.
type globalAPI struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	repo *citype.PGRepository
	ddl  *citype.DDLJob
}

func newGlobalAPI(t *testing.T, f *scopetest.Fixture) *globalAPI {
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	repo := citype.NewPGRepository(f.App).WithIndexScheduler(ddl)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(repo)).RegisterRoutes(mux)
	return &globalAPI{t: t, f: f, mux: mux, repo: repo, ddl: ddl}
}

func (a *globalAPI) ctx(org string) context.Context {
	return tenant.WithTenant(a.f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: a.f.User})
}

// call answers the status and the decoded body.
func (a *globalAPI) call(org, method, path, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(a.ctx(org))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// mustCreate posts body to path in organization A and returns the new ID.
func (a *globalAPI) mustCreate(path, body string) string {
	a.t.Helper()
	code, out := a.call(a.f.OrgA, http.MethodPost, path, body)
	id, _ := out["id"].(string)
	if code != http.StatusCreated || id == "" {
		a.t.Fatalf("POST %s %s: %d %v", path, body, code, out)
	}
	return id
}

// createType creates a type of organization A with category and parent.
func (a *globalAPI) createType(key, category, parent string) string {
	a.t.Helper()
	body := `{"key":"` + key + `","name":"` + key + `","category":"` + category + `"`
	if parent != "" {
		body += `,"parent_type_id":"` + parent + `"`
	}
	return a.mustCreate("/api/v1/ci-types", body+"}")
}

// runJobs runs the queued index jobs.
func (a *globalAPI) runJobs() {
	a.t.Helper()
	if _, err := a.ddl.RunDue(context.Background()); err != nil {
		a.t.Fatalf("run index jobs: %v", err)
	}
}

// indexDef is the definition of an index, "" when it does not exist.
func (a *globalAPI) indexDef(name string) string {
	a.t.Helper()
	var def string
	err := a.f.Admin.QueryRow(context.Background(),
		`SELECT pg_get_indexdef(c.oid) FROM pg_class c WHERE c.relname = $1 AND c.relkind = 'i'`, name).Scan(&def)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		a.t.Fatal(err)
	}
	return def
}

func violationsOf(out map[string]any) []string {
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

// TestGlobalAttributeApplicability covers WP-255 (MET-13) against
// PostgreSQL: existing definitions apply to all types after the migration;
// a required organization global with applies_to categories enforces its
// value on the CIs of a type of the category (422) and not on a type of
// another category (201); with applies_to types it applies to the listed
// type and its subtype, not to a sibling; GET
// /attribute-definitions?applicable_to= lists the type's definitions and
// exactly the globals that apply to it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestGlobalAttributeApplicability(t *testing.T) {
	f := scopetest.Seed(t, "8c")
	a := newGlobalAPI(t, f)
	bg := context.Background()

	var restricted int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition
		WHERE (applies_to <> 'all' OR cardinality(applies_to_categories) > 0 OR cardinality(applies_to_type_ids) > 0)
		  AND (organization_id IS NULL OR organization_id NOT IN ($1, $2))`, f.OrgA, f.OrgB).Scan(&restricted); err != nil || restricted != 0 {
		t.Errorf("restricted definitions besides the fixture = %d (%v), want every existing row on all", restricted, err)
	}

	infra := a.createType("wp255-switch", "infrastructure", "")
	service := a.createType("wp255-portal", "service", "")
	x := a.createType("wp255-x", "network", "")
	xSub := a.createType("wp255-x-sub", "network", x)
	sibling := a.createType("wp255-sibling", "network", "")
	ownDef := a.mustCreate("/api/v1/attribute-definitions", `{"ci_type_id":"`+x+`","key":"wp255_x_own","data_type":"text"}`)

	code, out := a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", `{"key":"wp255_cc","data_type":"text",
		"required":true,"applies_to":"categories","applies_to_categories":[" infrastructure ","infrastructure"]}`)
	byCategory, _ := out["id"].(string)
	if code != http.StatusCreated || out["applies_to"] != "categories" ||
		fmt.Sprint(out["applies_to_categories"]) != "[infrastructure]" || fmt.Sprint(out["applies_to_type_ids"]) != "[]" {
		t.Fatalf("global by category: %d %v", code, out)
	}
	byType := a.mustCreate("/api/v1/attribute-definitions", `{"key":"wp255_rack","data_type":"text","required":true,
		"applies_to":"types","applies_to_type_ids":["`+strings.ToUpper(x)+`"]}`)

	for _, tc := range []struct {
		name, typeID string
		missing      []string
	}{
		{"infrastructure", infra, []string{"wp255_cc"}},
		{"service", service, nil},
		{"listed type", x, []string{"wp255_rack"}},
		{"subtype", xSub, []string{"wp255_rack"}},
		{"sibling", sibling, nil},
	} {
		code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"wp255-`+tc.name+`","ci_type_id":"`+tc.typeID+`"}`)
		switch {
		case len(tc.missing) == 0 && code != http.StatusCreated:
			t.Errorf("%s without values: %d %v, want 201", tc.name, code, out)
		case len(tc.missing) > 0 && (code != http.StatusUnprocessableEntity || !slices.Equal(violationsOf(out), tc.missing)):
			t.Errorf("%s without values: %d %v, want 422 on %v", tc.name, code, out, tc.missing)
		}
	}
	code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"wp255-x-ok","ci_type_id":"`+xSub+`",
		"attributes":{"wp255_rack":"R1"}}`)
	if code != http.StatusCreated {
		t.Errorf("subtype with the value: %d %v", code, out)
	}

	listed := func(typeID string) []string {
		t.Helper()
		status, page := a.call(f.OrgA, http.MethodGet, "/api/v1/attribute-definitions?limit=200&applicable_to="+typeID, "")
		if status != http.StatusOK {
			t.Fatalf("applicable_to=%s: %d %v", typeID, status, page)
		}
		var ids []string
		data, _ := page["data"].([]any)
		for _, d := range data {
			m, _ := d.(map[string]any)
			if m["organization_id"] == f.OrgA {
				ids = append(ids, m["id"].(string))
			}
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string {
		slices.Sort(ids)
		return ids
	}
	for name, tc := range map[string]struct {
		typeID string
		want   []string
	}{
		"infrastructure": {infra, sorted(byCategory)},
		"service":        {service, nil},
		"listed type":    {x, sorted(byType, ownDef)},
		"subtype":        {xSub, sorted(byType)},
		"sibling":        {sibling, nil},
		"no uuid":        {"wp255", nil},
	} {
		if got := listed(tc.typeID); !slices.Equal(got, tc.want) {
			t.Errorf("applicable_to %s = %v, want %v", name, got, tc.want)
		}
	}
	// The other organization does not see the types of A: nothing applies.
	if code, out = a.call(f.OrgB, http.MethodGet, "/api/v1/attribute-definitions?applicable_to="+x, ""); code != http.StatusOK ||
		len(out["data"].([]any)) != 0 {
		t.Errorf("applicable_to of another organization's type: %d %v", code, out)
	}
}

// TestGlobalAttributeCompositionAndInput covers the composition check and
// the input rules of WP-255: extending applies_to to a type with a
// conflicting definition is 422 on key; a category change of a type into
// a conflict is 422 and leaves the type unchanged, without a conflict it
// is 200 and ResolveFields takes the global; applies_to on a type
// definition and an empty list are 400, a type ID of another organization
// is 422, a system type is accepted; PUT /ci-fields/global keeps the
// applicability of an existing definition and GET shows it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestGlobalAttributeCompositionAndInput(t *testing.T) {
	f := scopetest.Seed(t, "8c")
	a := newGlobalAPI(t, f)
	bg := context.Background()

	storage := a.createType("wp255-array", "storage", "")
	a.mustCreate("/api/v1/attribute-definitions", `{"ci_type_id":"`+storage+`","key":"wp255_size","data_type":"integer"}`)
	empty := a.createType("wp255-shelf", "storage", "")
	global := a.mustCreate("/api/v1/attribute-definitions", `{"key":"wp255_size","data_type":"text",
		"applies_to":"categories","applies_to_categories":["network"]}`)

	// Extending the scope to the conflicting type: 422 on key.
	code, out := a.call(f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+global,
		`{"applies_to_categories":["network","storage"]}`)
	if code != http.StatusUnprocessableEntity || out["field"] != "key" {
		t.Errorf("extend applies_to into a conflict: %d %v, want 422 on key", code, out)
	}
	code, out = a.call(f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+global,
		`{"applies_to":"types","applies_to_type_ids":["`+storage+`"]}`)
	if code != http.StatusUnprocessableEntity || out["field"] != "key" {
		t.Errorf("switch applies_to to the conflicting type: %d %v, want 422 on key", code, out)
	}
	if _, out = a.call(f.OrgA, http.MethodGet, "/api/v1/attribute-definitions/"+global, ""); out["applies_to"] != "categories" ||
		fmt.Sprint(out["applies_to_categories"]) != "[network]" {
		t.Errorf("definition after the refused changes: %v", out)
	}

	// A category change of a type into the conflict: 422, nothing stored.
	code, out = a.call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+storage, `{"category":"network","name":"wp255-renamed"}`)
	if code != http.StatusUnprocessableEntity || out["field"] != "category" || !strings.Contains(fmt.Sprint(out["detail"]), "wp255_size") {
		t.Errorf("category change into a conflict: %d %v, want 422 on category naming the key", code, out)
	}
	if typ, err := a.repo.GetByID(a.ctx(f.OrgA), f.OrgA, storage); err != nil || typ.Category != "storage" || typ.Name != "wp255-array" {
		t.Errorf("type after the refused change: %+v %v", typ, err)
	}
	// Without a conflict: 200, the global applies.
	if code, out = a.call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+empty, `{"category":"network"}`); code != http.StatusOK {
		t.Fatalf("category change: %d %v", code, out)
	}
	defs, err := a.repo.ResolveFields(a.ctx(f.OrgA), f.OrgA, empty, "")
	if err != nil {
		t.Fatalf("resolve after the change: %v", err)
	}
	if i := slices.IndexFunc(defs, func(d fieldmeta.FieldDefinition) bool { return d.Name == "wp255_size" }); i < 0 || defs[i].DataType != "text" {
		t.Errorf("resolved fields after the category change = %+v, want the global wp255_size", defs)
	}
	// An unknown type stays 404.
	if code, _ = a.call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/00000000-0000-0000-0000-000000000255", `{"category":"x"}`); code != http.StatusNotFound {
		t.Errorf("unknown type: %d, want 404", code)
	}

	// Input rules.
	var system string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&system); err != nil {
		t.Fatal(err)
	}
	foreign := ""
	code, out = a.call(f.OrgB, http.MethodPost, "/api/v1/ci-types", `{"key":"wp255-foreign","name":"wp255-foreign"}`)
	if foreign, _ = out["id"].(string); code != http.StatusCreated || foreign == "" {
		t.Fatalf("type of organization B: %d %v", code, out)
	}
	for name, tc := range map[string]struct {
		body  string
		code  int
		field string
	}{
		"type definition": {`{"ci_type_id":"` + storage + `","key":"wp255_t","data_type":"text",
			"applies_to":"categories","applies_to_categories":["network"]}`, http.StatusBadRequest, "applies_to"},
		"empty list":       {`{"key":"wp255_e","data_type":"text","applies_to":"types","applies_to_type_ids":[]}`, http.StatusBadRequest, "applies_to_type_ids"},
		"no list":          {`{"key":"wp255_e","data_type":"text","applies_to":"categories"}`, http.StatusBadRequest, "applies_to_categories"},
		"list of the mode": {`{"key":"wp255_e","data_type":"text","applies_to_categories":["network"]}`, http.StatusBadRequest, "applies_to_categories"},
		"unknown mode":     {`{"key":"wp255_e","data_type":"text","applies_to":"some"}`, http.StatusBadRequest, "applies_to"},
		"foreign type": {`{"key":"wp255_e","data_type":"text","applies_to":"types","applies_to_type_ids":["` + foreign + `"]}`,
			http.StatusUnprocessableEntity, "applies_to_type_ids"},
		"system type": {`{"key":"wp255_server","data_type":"text","applies_to":"types","applies_to_type_ids":["` + system + `"]}`,
			http.StatusCreated, ""},
	} {
		code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", tc.body)
		if code != tc.code || (tc.field != "" && out["field"] != tc.field) {
			t.Errorf("%s: %d %v, want %d on %q", name, code, out, tc.code, tc.field)
		}
	}
	typeDef := a.mustCreate("/api/v1/attribute-definitions", `{"ci_type_id":"`+storage+`","key":"wp255_t2","data_type":"text"}`)
	if code, out = a.call(f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+typeDef,
		`{"applies_to":"types","applies_to_type_ids":["`+storage+`"]}`); code != http.StatusBadRequest || out["field"] != "applies_to" {
		t.Errorf("PATCH applies_to of a type definition: %d %v, want 400", code, out)
	}

	// The legacy upsert keeps the applicability; GET shows it.
	if code, out = a.call(f.OrgA, http.MethodPut, "/api/v1/ci-fields/global",
		`{"name":"wp255_size","data_type":"text","label":"Size"}`); code != http.StatusOK || out["applies_to"] != "categories" {
		t.Errorf("legacy upsert: %d %v", code, out)
	}
	if _, out = a.call(f.OrgA, http.MethodGet, "/api/v1/attribute-definitions/"+global, ""); out["applies_to"] != "categories" ||
		fmt.Sprint(out["applies_to_categories"]) != "[network]" {
		t.Errorf("definition after the legacy upsert: %v", out)
	}
	if code, out = a.call(f.OrgA, http.MethodPut, "/api/v1/ci-fields/global", `{"name":"wp255_new","data_type":"text"}`); code != http.StatusOK ||
		out["applies_to"] != "all" {
		t.Errorf("legacy upsert of a new global: %d %v, want applies_to all", code, out)
	}
	_, out = a.call(f.OrgA, http.MethodGet, "/api/v1/ci-fields/global", "")
	found := false
	for _, d := range out["data"].([]any) {
		if m, _ := d.(map[string]any); m["name"] == "wp255_size" {
			found = m["applies_to"] == "categories" && fmt.Sprint(m["applies_to_categories"]) == "[network]"
		}
	}
	if !found {
		t.Errorf("GET /ci-fields/global lacks the applicability of wp255_size: %v", out)
	}
}

// TestGlobalAttributeDeleteAndIndex covers the delete paths, the indexes
// and the uniqueness of global attributes (WP-255, DB-04, MET-15): a
// searchable global uses the key index of WP-607, a unique one gets
// idx_attr_<id>_u without a type predicate; uniqueness holds
// organization-wide, also against a type the global does not apply to
// (409); DELETE /ci-fields/global/{name} and /attribute-definitions/{id}
// start the index job, which drops the unique index and the key index once
// no definition uses the key; two CIs may then share the value, and a CI
// PATCH without the key keeps it. A system global is 404 on the legacy
// path and 403 on /attribute-definitions/{id}; organization B deletes
// nothing of A (404); DELETE /ci-types/{id}/fields/{name} does not reach a
// global of the same name (404, the global stays).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestGlobalAttributeDeleteAndIndex(t *testing.T) {
	f := scopetest.Seed(t, "8c")
	a := newGlobalAPI(t, f)
	bg := context.Background()
	run := time.Now().UnixNano()
	tagKey, serialKey := fmt.Sprintf("wp255_tag_%d", run), fmt.Sprintf("wp255_serial_%d", run)
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.KeyIndexName(tagKey)) })

	typ := a.createType("wp255-host", "infrastructure", "")
	other := a.createType("wp255-app", "service", "")

	tagA := a.mustCreate("/api/v1/attribute-definitions", `{"key":"`+tagKey+`","data_type":"text","searchable":true}`)
	code, out := a.call(f.OrgB, http.MethodPost, "/api/v1/attribute-definitions", `{"key":"`+tagKey+`","data_type":"text","searchable":true}`)
	if code != http.StatusCreated {
		t.Fatalf("searchable global of B: %d %v", code, out)
	}
	serial := a.mustCreate("/api/v1/attribute-definitions", `{"key":"`+serialKey+`","data_type":"text","unique":true,
		"applies_to":"types","applies_to_type_ids":["`+typ+`"]}`)
	uniqueIndex := citype.IndexName(serial, true)
	t.Cleanup(func() {
		for _, id := range []string{tagA, serial} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(id, false)+`, `+citype.IndexName(id, true))
		}
	})
	a.runJobs()
	if def := a.indexDef(citype.KeyIndexName(tagKey)); def == "" || strings.Contains(def, "ci_type_id") {
		t.Errorf("key index of the searchable global = %q", def)
	}
	if def := a.indexDef(citype.IndexName(tagA, false)); def != "" {
		t.Errorf("searchable global has an own index: %q", def)
	}
	if def := a.indexDef(uniqueIndex); def == "" || !strings.Contains(def, "UNIQUE") || strings.Contains(def, "ci_type_id") {
		t.Errorf("unique index of the global = %q, want one without a type predicate", def)
	}

	// Uniqueness is organization-wide, also for a type it does not apply to.
	code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"wp255-h1","ci_type_id":"`+typ+`",
		"attributes":{"`+serialKey+`":"S1"}}`)
	ciID, _ := out["id"].(string)
	if code != http.StatusCreated {
		t.Fatalf("CI with the serial: %d %v", code, out)
	}
	if code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"wp255-a1","ci_type_id":"`+other+`",
		"attributes":{"`+serialKey+`":"S1"}}`); code != http.StatusConflict {
		t.Errorf("same value on a type the global does not apply to: %d %v, want 409", code, out)
	}

	// Organization B deletes nothing of A.
	if code, _ = a.call(f.OrgB, http.MethodDelete, "/api/v1/ci-fields/global/"+serialKey, ""); code != http.StatusNotFound {
		t.Errorf("B on the legacy path: %d, want 404", code)
	}
	if code, _ = a.call(f.OrgB, http.MethodDelete, "/api/v1/attribute-definitions/"+serial, ""); code != http.StatusNotFound {
		t.Errorf("B on /attribute-definitions: %d, want 404", code)
	}

	// A type path does not reach a global of the same name.
	if code, _ = a.call(f.OrgA, http.MethodDelete, "/api/v1/ci-types/"+typ+"/fields/"+serialKey, ""); code != http.StatusNotFound {
		t.Errorf("type path on a global field: %d, want 404", code)
	}
	if code, _ = a.call(f.OrgA, http.MethodGet, "/api/v1/attribute-definitions/"+serial, ""); code != http.StatusOK {
		t.Errorf("global after the type path: %d, want it kept", code)
	}

	// The legacy path deletes the global and drops its unique index.
	if code, out = a.call(f.OrgA, http.MethodDelete, "/api/v1/ci-fields/global/"+serialKey, ""); code != http.StatusNoContent {
		t.Fatalf("legacy delete: %d %v", code, out)
	}
	a.runJobs()
	if def := a.indexDef(uniqueIndex); def != "" {
		t.Errorf("unique index after the legacy delete: %q", def)
	}
	for _, name := range []string{"wp255-h2", "wp255-h3"} {
		if code, out = a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"`+name+`","ci_type_id":"`+typ+`",
			"attributes":{"`+serialKey+`":"S1"}}`); code != http.StatusCreated {
			t.Errorf("CI %s with a shared value after the delete: %d %v", name, code, out)
		}
	}
	// The values stay; a PATCH without the key keeps it.
	if code, out = a.call(f.OrgA, http.MethodPatch, "/api/v1/cis/"+ciID, `{"name":"wp255-h1-renamed"}`); code != http.StatusOK {
		t.Errorf("CI PATCH after the delete: %d %v", code, out)
	} else if attrs, _ := out["attributes"].(map[string]any); attrs[serialKey] != "S1" {
		t.Errorf("value after the delete and PATCH = %v, want S1", attrs)
	}

	// /attribute-definitions/{id}: the key index stays while B uses the key,
	// the legacy path of B then drops it.
	if code, out = a.call(f.OrgA, http.MethodDelete, "/api/v1/attribute-definitions/"+tagA, ""); code != http.StatusNoContent {
		t.Fatalf("delete by id: %d %v", code, out)
	}
	a.runJobs()
	if def := a.indexDef(citype.KeyIndexName(tagKey)); def == "" {
		t.Error("key index dropped while organization B still uses the key")
	}
	if code, out = a.call(f.OrgB, http.MethodDelete, "/api/v1/ci-fields/global/"+tagKey, ""); code != http.StatusNoContent {
		t.Fatalf("legacy delete of B: %d %v", code, out)
	}
	a.runJobs()
	if def := a.indexDef(citype.KeyIndexName(tagKey)); def != "" {
		t.Errorf("key index after the last definition of the key: %q", def)
	}

	// A system global: 404 on the legacy path, 403 by ID.
	sysKey := fmt.Sprintf("wp255_sys_%d", run)
	var sysID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type)
		VALUES (NULL, NULL, 'global', $1, 'text') RETURNING id::text`, sysKey).Scan(&sysID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = $1`, sysID) })
	if code, _ = a.call(f.OrgA, http.MethodDelete, "/api/v1/ci-fields/global/"+sysKey, ""); code != http.StatusNotFound {
		t.Errorf("system global on the legacy path: %d, want 404", code)
	}
	if code, _ = a.call(f.OrgA, http.MethodDelete, "/api/v1/attribute-definitions/"+sysID, ""); code != http.StatusForbidden {
		t.Errorf("system global by ID: %d, want 403", code)
	}
}
