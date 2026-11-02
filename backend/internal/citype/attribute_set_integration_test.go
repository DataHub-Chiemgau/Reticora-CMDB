package citype_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// setAPI drives the attribute set API of WP-250 against PostgreSQL as the
// metamodel administrator of an organization.
type setAPI struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	repo *citype.PGRepository
	user string
	n    int
}

func newSetAPI(t *testing.T, f *scopetest.Fixture) *setAPI {
	repo := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	return &setAPI{t: t, f: f, mux: mux, repo: repo, user: f.AppUser(t, f.OrgA, "wp250-admin")}
}

func (a *setAPI) ctx(org string) context.Context {
	a.n++
	scope := database.OrgWideScope(org, a.user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: a.user})
	return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp250-" + strconv.Itoa(a.n),
		CorrelationID: "wp250-sets", Source: audit.SourceUI})
}

func (a *setAPI) call(org, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(a.ctx(org))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// must calls and fails the test unless the status is want; it decodes the
// body into out (may be nil).
func (a *setAPI) must(want int, org, method, path, body string, out any) {
	a.t.Helper()
	w := a.call(org, method, path, body)
	if w.Code != want {
		a.t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			a.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func (a *setAPI) createSet(org, key string) citype.AttributeSet {
	a.t.Helper()
	var s citype.AttributeSet
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-sets",
		`{"key":"`+key+`","display_name":{"de":"Set `+key+`","en":"Set `+key+`"}}`, &s)
	return s
}

func (a *setAPI) createType(org, key string) citype.Type {
	a.t.Helper()
	var typ citype.Type
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/ci-types", `{"key":"`+key+`","name":"`+key+`"}`, &typ)
	return typ
}

func (a *setAPI) setAttribute(org, setID, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.call(org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"attribute_set_id":"`+setID+`",`+strings.TrimPrefix(body, "{"))
}

// systemSet inserts a system set (organization_id NULL) as the migration
// owner would; the cleanup removes it with its attributes and assignments.
func systemSet(t *testing.T, f *scopetest.Fixture, key string) string {
	t.Helper()
	bg := context.Background()
	if _, err := f.Admin.Exec(bg, `DELETE FROM ci_type_attribute_set WHERE attribute_set_id IN
		(SELECT id FROM attribute_set WHERE organization_id IS NULL AND key = $1)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM attribute_set WHERE organization_id IS NULL AND key = $1`, key); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_set (key, display_name, is_system)
		VALUES ($1, '{"de": "System", "en": "System"}', true) RETURNING id::text`, key).Scan(&id); err != nil {
		t.Fatalf("system set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `DELETE FROM ci_type_attribute_set WHERE attribute_set_id = $1`, id); err != nil {
			t.Errorf("cleanup assignments of %s: %v", key, err)
		}
		if _, err := f.Admin.Exec(bg, `DELETE FROM attribute_set WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup system set %s: %v", key, err)
		}
	})
	return id
}

// systemSetAttribute inserts an attribute of a system set as a migration
// would.
func systemSetAttribute(t *testing.T, f *scopetest.Fixture, setID, key string) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO attribute_definition
		(attribute_set_id, scope, key, data_type, required) VALUES ($1, 'set', $2, 'text', true)`, setID, key); err != nil {
		t.Fatalf("system set attribute %s: %v", key, err)
	}
}

type setPage struct {
	Data       []citype.AttributeSet `json:"data"`
	Total      int                   `json:"total"`
	NextCursor string                `json:"next_cursor"`
}

type definitionPage struct {
	Data []citype.AttributeDefinition `json:"data"`
}

func definitionKeys(p definitionPage) []string {
	keys := make([]string, 0, len(p.Data))
	for _, d := range p.Data {
		keys = append(keys, d.Key)
	}
	return keys
}

// TestAttributeSetMaintenanceAgainstPostgres covers API-06 (WP-250): CRUD
// of attribute sets with cursor pagination, the key unique per
// organization (409), read-only system sets (PATCH, DELETE and a new set
// attribute are 403) and set attributes over attribute-definitions with
// the filters attribute_set_id and scope=set.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetMaintenanceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	sys := systemSet(t, f, "wp250-sys-maint")

	var created []citype.AttributeSet
	for i := 0; i < 3; i++ {
		created = append(created, a.createSet(f.OrgA, "wp250-net-"+strconv.Itoa(i)))
	}
	if s := created[0]; s.OrganizationID != f.OrgA || s.IsSystem || !s.IsActive || s.DisplayName["en"] != "Set wp250-net-0" {
		t.Fatalf("created set: %+v", s)
	}
	w := a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-sets", `{"key":"wp250-net-1"}`)
	if p := problemOf(w); w.Code != http.StatusConflict || p.Field != "key" {
		t.Errorf("duplicate key: %d %s", w.Code, w.Body.String())
	}
	// The same key in another organization is free.
	a.createSet(f.OrgB, "wp250-net-1")
	if w = a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-sets", `{"key":" "}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty key: %d %s", w.Code, w.Body.String())
	}

	// Pages of two, newest first, until the cursor runs out: the three
	// sets of the organization and the system set, nothing of org B.
	seen := map[string]bool{}
	path := "/api/v1/attribute-sets?limit=2"
	for pages := 0; path != ""; pages++ {
		if pages > 50 {
			t.Fatal("pagination does not end")
		}
		var page setPage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &page)
		if len(page.Data) > 2 {
			t.Fatalf("page of %d sets, limit 2", len(page.Data))
		}
		for _, s := range page.Data {
			if seen[s.ID] {
				t.Errorf("set %s on two pages", s.Key)
			}
			seen[s.ID] = true
			if s.OrganizationID != f.OrgA && s.OrganizationID != "" {
				t.Errorf("set of another organization listed: %+v", s)
			}
		}
		path = ""
		if page.NextCursor != "" {
			path = "/api/v1/attribute-sets?limit=2&cursor=" + page.NextCursor
		}
	}
	for _, s := range created {
		if !seen[s.ID] {
			t.Errorf("set %s missing from the pages", s.Key)
		}
	}
	if !seen[sys] {
		t.Error("system set missing from the pages")
	}

	var got citype.AttributeSet
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/attribute-sets/"+created[0].ID, "", &got)
	if got.Key != "wp250-net-0" {
		t.Errorf("get: %+v", got)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/attribute-sets/"+created[0].ID,
		`{"display_name":{"de":"Netz","en":"Network"},"description":"VLAN data","is_active":false}`, &got)
	if got.DisplayName["de"] != "Netz" || got.Description != "VLAN data" || got.IsActive || got.Key != "wp250-net-0" {
		t.Errorf("patch: %+v", got)
	}
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/attribute-sets/not-a-uuid", "", nil)

	// System sets are read-only.
	for _, c := range []struct{ method, body string }{
		{http.MethodPatch, `{"is_active":false}`},
		{http.MethodDelete, ""},
	} {
		if w = a.call(f.OrgA, c.method, "/api/v1/attribute-sets/"+sys, c.body); w.Code != http.StatusForbidden {
			t.Errorf("%s system set: %d %s", c.method, w.Code, w.Body.String())
		}
	}
	if w = a.setAttribute(f.OrgA, sys, `{"key":"wp250_sys_vlan","data_type":"text"}`); w.Code != http.StatusForbidden {
		t.Errorf("set attribute in a system set: %d %s", w.Code, w.Body.String())
	}

	// Set attributes over attribute-definitions.
	var vlan citype.AttributeDefinition
	if w = a.setAttribute(f.OrgA, created[1].ID, `{"key":"wp250_vlan","data_type":"integer","order":2}`); w.Code != http.StatusCreated {
		t.Fatalf("set attribute: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &vlan)
	if vlan.Scope != citype.ScopeSet || vlan.AttributeSetID != created[1].ID || vlan.CITypeID != "" || vlan.OrganizationID != f.OrgA {
		t.Errorf("set attribute: %+v", vlan)
	}
	if w = a.setAttribute(f.OrgA, created[1].ID, `{"key":"wp250_zone","data_type":"text","order":1}`); w.Code != http.StatusCreated {
		t.Fatalf("second set attribute: %d %s", w.Code, w.Body.String())
	}
	if w = a.setAttribute(f.OrgA, created[1].ID, `{"key":"wp250_vlan","data_type":"text"}`); w.Code != http.StatusConflict {
		t.Errorf("key twice in a set: %d %s", w.Code, w.Body.String())
	}
	// The same key in another set and as the organization's global
	// attribute.
	if w = a.setAttribute(f.OrgA, created[2].ID, `{"key":"wp250_vlan","data_type":"integer"}`); w.Code != http.StatusCreated {
		t.Errorf("key in a second set: %d %s", w.Code, w.Body.String())
	}
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", `{"key":"wp250_vlan","data_type":"integer"}`, nil)
	unknownSet := f.ID()
	if w = a.setAttribute(f.OrgA, unknownSet, `{"key":"wp250_x","data_type":"text"}`); w.Code != http.StatusUnprocessableEntity ||
		problemOf(w).Field != "attribute_set_id" {
		t.Errorf("unknown set: %d %s", w.Code, w.Body.String())
	}
	typ := a.createType(f.OrgA, "wp250-maint-type")
	if w = a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"key":"wp250_both","data_type":"text","ci_type_id":"`+typ.ID+`","attribute_set_id":"`+created[1].ID+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("type and set: %d %s", w.Code, w.Body.String())
	}
	// attribute_set_id is immutable like key and ci_type_id.
	if w = a.call(f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+vlan.ID,
		`{"attribute_set_id":"`+created[2].ID+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("patch attribute_set_id: %d %s", w.Code, w.Body.String())
	}

	var bySet, byScope definitionPage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/attribute-definitions?attribute_set_id="+created[1].ID, "", &bySet)
	if keys := definitionKeys(bySet); len(keys) != 2 || !strings.Contains(strings.Join(keys, ","), "wp250_zone") {
		t.Errorf("?attribute_set_id: %v", keys)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/attribute-definitions?scope=set&limit=200", "", &byScope)
	if len(byScope.Data) != 3 {
		t.Errorf("?scope=set: %v", definitionKeys(byScope))
	}
	for _, d := range byScope.Data {
		if d.Scope != citype.ScopeSet || d.AttributeSetID == "" {
			t.Errorf("?scope=set lists %+v", d)
		}
	}
	var globals definitionPage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/attribute-definitions?scope=global&limit=200", "", &globals)
	var own []string
	for _, d := range globals.Data {
		if d.OrganizationID == f.OrgA {
			own = append(own, d.Key)
		}
		if d.Scope != "global" || d.AttributeSetID != "" {
			t.Errorf("?scope=global lists %+v", d)
		}
	}
	if len(own) != 1 || own[0] != "wp250_vlan" {
		t.Errorf("?scope=global of the organization: %v", own)
	}
}

// TestAttributeSetAssignmentAgainstPostgres covers API-06 (WP-250): PUT and
// GET of the set list of a type keep the position order and PUT replaces
// the list. An inactive set, a system type and a set of another
// organization are 422; the last creates no row, not even by a direct
// INSERT (trigger, the set is invisible under RLS). A system set can be
// assigned to a type of the organization. DELETE of an assigned set is
// 409; DELETE of an unassigned set deletes its set attributes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetAssignmentAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	sys := systemSet(t, f, "wp250-sys-assign")
	typ := a.createType(f.OrgA, "wp250-assign-type")
	s1, s2, s3 := a.createSet(f.OrgA, "wp250-a1"), a.createSet(f.OrgA, "wp250-a2"), a.createSet(f.OrgA, "wp250-a3")
	foreign := a.createSet(f.OrgB, "wp250-b1")
	path := "/api/v1/ci-types/" + typ.ID + "/attribute-sets"

	var doc citype.TypeAttributeSets
	a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &doc)
	if doc.CITypeID != typ.ID || doc.AttributeSets == nil || len(doc.AttributeSets) != 0 {
		t.Fatalf("empty assignment: %+v", doc)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPut, path, `{"attribute_sets":[
		{"attribute_set_id":"`+s2.ID+`","position":20},{"attribute_set_id":"`+s1.ID+`","position":10},
		{"attribute_set_id":"`+sys+`","position":30}]}`, &doc)
	want := []citype.AttributeSetAssignment{{AttributeSetID: s1.ID, Position: 10}, {AttributeSetID: s2.ID, Position: 20},
		{AttributeSetID: sys, Position: 30}}
	if !reflect.DeepEqual(doc.AttributeSets, want) {
		t.Fatalf("PUT answer: %+v", doc.AttributeSets)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &doc)
	if !reflect.DeepEqual(doc.AttributeSets, want) {
		t.Fatalf("GET after PUT: %+v", doc.AttributeSets)
	}
	// PUT replaces the list as a whole.
	a.must(http.StatusOK, f.OrgA, http.MethodPut, path, `{"ci_type_id":"`+typ.ID+`","attribute_sets":[
		{"attribute_set_id":"`+s3.ID+`","position":0},{"attribute_set_id":"`+s1.ID+`","position":1}]}`, &doc)
	want = []citype.AttributeSetAssignment{{AttributeSetID: s3.ID, Position: 0}, {AttributeSetID: s1.ID, Position: 1}}
	if !reflect.DeepEqual(doc.AttributeSets, want) {
		t.Fatalf("replaced list: %+v", doc.AttributeSets)
	}

	// An inactive set cannot be assigned anew; an assigned one stays.
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/attribute-sets/"+s2.ID, `{"is_active":false}`, nil)
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/attribute-sets/"+s1.ID, `{"is_active":false}`, nil)
	checks := []struct {
		name, body, field string
	}{
		{"inactive set", `{"attribute_sets":[{"attribute_set_id":"` + s2.ID + `","position":0}]}`, "attribute_set_id"},
		{"foreign set", `{"attribute_sets":[{"attribute_set_id":"` + foreign.ID + `","position":0}]}`, "attribute_set_id"},
		{"unknown set", `{"attribute_sets":[{"attribute_set_id":"` + f.ID() + `","position":0}]}`, "attribute_set_id"},
		{"set twice", `{"attribute_sets":[{"attribute_set_id":"` + s3.ID + `","position":0},{"attribute_set_id":"` + s3.ID + `","position":1}]}`, "attribute_set_id"},
		{"position twice", `{"attribute_sets":[{"attribute_set_id":"` + s3.ID + `","position":0},{"attribute_set_id":"` + sys + `","position":0}]}`, "position"},
	}
	for _, c := range checks {
		w := a.call(f.OrgA, http.MethodPut, path, c.body)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != c.field {
			t.Errorf("%s: %d %s, want 422 on %s", c.name, w.Code, w.Body.String(), c.field)
		}
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPut, path, `{"attribute_sets":[
		{"attribute_set_id":"`+s1.ID+`","position":0},{"attribute_set_id":"`+s3.ID+`","position":1}]}`, &doc)
	if len(doc.AttributeSets) != 2 || doc.AttributeSets[0].AttributeSetID != s1.ID {
		t.Errorf("the assigned inactive set does not stay: %+v", doc.AttributeSets)
	}

	// A system type gets no sets of an organization; an unknown type is
	// 422 on ci_type_id as well.
	var systemType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id IS NULL ORDER BY key LIMIT 1`).Scan(&systemType); err != nil {
		t.Fatal(err)
	}
	for _, typeID := range []string{systemType, f.ID()} {
		w := a.call(f.OrgA, http.MethodPut, "/api/v1/ci-types/"+typeID+"/attribute-sets",
			`{"attribute_sets":[{"attribute_set_id":"`+s3.ID+`","position":0}]}`)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != "ci_type_id" {
			t.Errorf("type %s: %d %s", typeID, w.Code, w.Body.String())
		}
	}
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/ci-types/"+systemType+"/attribute-sets", "", &doc)
	if len(doc.AttributeSets) != 0 {
		t.Errorf("system type has sets: %+v", doc)
	}

	// A direct INSERT with a set of another organization: the trigger
	// does not see it under RLS and refuses the row.
	typB := a.createType(f.OrgB, "wp250-assign-type-b")
	err := database.WithRequestTenant(f.OrgCtx(f.OrgB), f.App, f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := tx.Exec(ctx, `INSERT INTO ci_type_attribute_set (ci_type_id, attribute_set_id, position) VALUES ($1, $2, 0)`,
			typB.ID, s3.ID)
		return ierr
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "ci_type_attribute_set_attribute_set_id_fkey" {
		t.Errorf("direct INSERT with a foreign set: %v", err)
	}
	var rows int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_type_attribute_set WHERE attribute_set_id = $1 AND ci_type_id <> $2`,
		s3.ID, typ.ID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows of the foreign set elsewhere: %d %v", rows, err)
	}

	// DELETE of an assigned set is 409, of an unassigned one deletes its
	// set attributes.
	if w := a.call(f.OrgA, http.MethodDelete, "/api/v1/attribute-sets/"+s3.ID, ""); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "urn:reticora:problem:conflict") {
		t.Errorf("delete an assigned set: %d %s", w.Code, w.Body.String())
	}
	for _, key := range []string{"wp250_rack", "wp250_slot"} {
		if w := a.setAttribute(f.OrgA, s2.ID, `{"key":"`+key+`","data_type":"text","searchable":true}`); w.Code != http.StatusCreated {
			t.Fatalf("set attribute %s: %d %s", key, w.Code, w.Body.String())
		}
	}
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/attribute-sets/"+s2.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/attribute-sets/"+s2.ID, "", nil)
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND key IN ('wp250_rack', 'wp250_slot')`,
		f.OrgA).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("set attributes after the delete: %d %v", rows, err)
	}
	// After the type is gone, the set can be deleted.
	if _, err = f.Admin.Exec(bg, `DELETE FROM ci_type WHERE id = $1`, typ.ID); err != nil {
		t.Fatal(err)
	}
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/attribute-sets/"+s3.ID, "", nil)
}

// typeState is what the composition of a type exposes: the materialized
// schema, its version and the resolved definitions.
type typeState struct {
	schema  string
	version int
	fields  []string
}

func stateOf(t *testing.T, a *setAPI, org, typeID string) typeState {
	t.Helper()
	var s typeState
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT attribute_schema::text, schema_version FROM ci_type WHERE id = $1`,
		typeID).Scan(&s.schema, &s.version); err != nil {
		t.Fatal(err)
	}
	defs, err := a.repo.ResolveFields(a.ctx(org), org, typeID, "")
	if err != nil {
		t.Fatalf("resolve fields: %v", err)
	}
	for _, d := range defs {
		s.fields = append(s.fields, d.Name)
	}
	return s
}

// TestAttributeSetScopeAgainstPostgres covers API-06 (WP-250, until
// WP-709): a set attribute of an organization or of a system set, even of
// a set assigned to the type, leaves ResolveFields, ci_type.attribute_schema
// and schema_version of every type unchanged; GET /ci-fields/global does
// not list it, and two system set attributes of one key do not collide in
// attribute_definition_system_global_key.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	typ := a.createType(f.OrgA, "wp250-scope-type")
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typ.ID+`","key":"wp250_own","data_type":"text"}`, nil)
	var systemType string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE organization_id IS NULL ORDER BY key LIMIT 1`).Scan(&systemType); err != nil {
		t.Fatal(err)
	}
	orgBefore, sysBefore := stateOf(t, a, f.OrgA, typ.ID), stateOf(t, a, f.OrgA, systemType)

	set := a.createSet(f.OrgA, "wp250-scope")
	sysA, sysB := systemSet(t, f, "wp250-sys-scope-a"), systemSet(t, f, "wp250-sys-scope-b")
	a.must(http.StatusOK, f.OrgA, http.MethodPut, "/api/v1/ci-types/"+typ.ID+"/attribute-sets", `{"attribute_sets":[
		{"attribute_set_id":"`+set.ID+`","position":0},{"attribute_set_id":"`+sysA+`","position":1}]}`, nil)
	if w := a.setAttribute(f.OrgA, set.ID, `{"key":"wp250_scoped","data_type":"text","required":true}`); w.Code != http.StatusCreated {
		t.Fatalf("org set attribute: %d %s", w.Code, w.Body.String())
	}
	// Two system set attributes of one key: no collision in the unique key
	// of the system base.
	systemSetAttribute(t, f, sysA, "wp250_sys_scoped")
	systemSetAttribute(t, f, sysB, "wp250_sys_scoped")

	for name, c := range map[string]struct {
		typeID string
		before typeState
	}{"organization type": {typ.ID, orgBefore}, "system type": {systemType, sysBefore}} {
		if after := stateOf(t, a, f.OrgA, c.typeID); !reflect.DeepEqual(after, c.before) {
			t.Errorf("%s changed by set attributes:\nbefore %+v\nafter  %+v", name, c.before, after)
		}
	}
	var globals struct {
		Data []citype.Field `json:"data"`
	}
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/ci-fields/global", "", &globals)
	for _, fld := range globals.Data {
		if strings.HasPrefix(fld.Name, "wp250_") {
			t.Errorf("GET /ci-fields/global lists the set attribute %s", fld.Name)
		}
	}
	// The legacy global path neither finds nor deletes a set attribute.
	a.must(http.StatusNotFound, f.OrgA, http.MethodDelete, "/api/v1/ci-fields/global/wp250_scoped", "", nil)
}

// TestAttributeSetRLSAndAuditAgainstPostgres covers AUD-07 and API-06
// (WP-250): org B sees neither the sets nor the set attributes nor the
// assignments of org A; attribute_set.created|updated|deleted and
// ci_type.attribute_sets_changed carry actor, source, request and
// correlation id with before/after in the transaction of the change, and
// the hash chain of the organization stays intact.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetRLSAndAuditAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	typ := a.createType(f.OrgA, "wp250-audit-type")
	set := a.createSet(f.OrgA, "wp250-audit")
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/attribute-sets/"+set.ID, `{"description":"audited"}`, nil)
	var def citype.AttributeDefinition
	if w := a.setAttribute(f.OrgA, set.ID, `{"key":"wp250_audited","data_type":"text"}`); w.Code != http.StatusCreated {
		t.Fatalf("set attribute: %d %s", w.Code, w.Body.String())
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &def)
	}
	path := "/api/v1/ci-types/" + typ.ID + "/attribute-sets"
	a.must(http.StatusOK, f.OrgA, http.MethodPut, path, `{"attribute_sets":[{"attribute_set_id":"`+set.ID+`","position":3}]}`, nil)

	// Org B sees nothing of org A.
	var page setPage
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/attribute-sets?limit=200", "", &page)
	for _, s := range page.Data {
		if s.OrganizationID == f.OrgA {
			t.Errorf("org B lists set %s of org A", s.Key)
		}
	}
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, "/api/v1/attribute-sets/"+set.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodPatch, "/api/v1/attribute-sets/"+set.ID, `{"is_active":false}`, nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodDelete, "/api/v1/attribute-sets/"+set.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, path, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, "/api/v1/attribute-definitions/"+def.ID, "", nil)
	var defs definitionPage
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/attribute-definitions?attribute_set_id="+set.ID, "", &defs)
	if len(defs.Data) != 0 {
		t.Errorf("org B lists set attributes of org A: %v", definitionKeys(defs))
	}
	var visible int
	if err := database.WithRequestTenant(f.OrgCtx(f.OrgB), f.App, f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM ci_type_attribute_set WHERE ci_type_id = $1)
			+ (SELECT count(*) FROM attribute_set WHERE id = $2)
			+ (SELECT count(*) FROM attribute_definition WHERE attribute_set_id = $2)`, typ.ID, set.ID).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Errorf("org B sees %d rows of org A: %v", visible, err)
	}

	// Remove the assignment and the set.
	a.must(http.StatusOK, f.OrgA, http.MethodPut, path, `{"attribute_sets":[]}`, nil)
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/attribute-sets/"+set.ID, "", nil)

	rows, err := f.Admin.Query(bg, `SELECT action, resource_type, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''),
		source, COALESCE(request_id, ''), COALESCE(correlation_id, ''), changes
		FROM audit_log WHERE organization_id = $1 AND resource_type IN ('attribute_set', 'attribute_definition')
		   OR (organization_id = $1 AND action = 'ci_type.attribute_sets_changed') ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type entry struct {
		action, resourceType, resource, actor, source, request, correlation string
		changes                                                             map[string]any
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.action, &e.resourceType, &e.resource, &e.actor, &e.source, &e.request, &e.correlation, &e.changes); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	wantActions := []string{"attribute_set.created", "attribute_set.updated", "attribute_definition.created",
		"ci_type.attribute_sets_changed", "ci_type.attribute_sets_changed", "attribute_definition.deleted", "attribute_set.deleted"}
	actions := make([]string, len(got))
	for i, e := range got {
		actions[i] = e.action
		if e.actor != a.user || e.source != audit.SourceUI || e.correlation != "wp250-sets" || !strings.HasPrefix(e.request, "wp250-") {
			t.Errorf("%s: actor %s, source %s, request %s, correlation %s", e.action, e.actor, e.source, e.request, e.correlation)
		}
	}
	if strings.Join(actions, " ") != strings.Join(wantActions, " ") {
		t.Fatalf("audit actions:\n got %v\nwant %v", actions, wantActions)
	}
	if e := got[0]; e.resourceType != "attribute_set" || e.resource != set.ID || e.changes["after"] == nil || e.changes["before"] != nil {
		t.Errorf("set created: %+v", e)
	}
	if b, _ := got[1].changes["before"].(map[string]any); b["description"] != nil {
		t.Errorf("set updated, before: %+v", got[1].changes)
	}
	if after, _ := got[1].changes["after"].(map[string]any); after["description"] != "audited" {
		t.Errorf("set updated, after: %+v", got[1].changes)
	}
	changed := got[3]
	before, _ := changed.changes["before"].(map[string]any)
	after, _ := changed.changes["after"].(map[string]any)
	list, _ := after["attribute_sets"].([]any)
	if changed.resourceType != "ci_type" || changed.resource != typ.ID || len(list) != 1 || before == nil {
		t.Errorf("ci_type.attribute_sets_changed: %+v", changed)
	}
	if prev, _ := before["attribute_sets"].([]any); len(prev) != 0 {
		t.Errorf("assignment before: %+v", before)
	}
	if e := got[5]; e.resource != def.ID || e.changes["before"] == nil || e.changes["after"] != nil {
		t.Errorf("set attribute deleted with its set: %+v", e)
	}
	if e := got[6]; e.resource != set.ID || e.changes["before"] == nil || e.changes["after"] != nil {
		t.Errorf("set deleted: %+v", e)
	}
	if result, verr := audit.Verify(bg, f.App, f.OrgA); verr != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, verr)
	}
}
