package metamodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// draftAPI serves the citype resource routes (fixtures) and the draft
// routes over PostgreSQL.
type draftAPI struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	user string
	mu   sync.Mutex
	n    int
}

func newDraftAPI(t *testing.T, f *scopetest.Fixture) *draftAPI {
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewPGRepository(f.App)).RegisterRoutes(mux)
	metamodel.NewHandler(metamodel.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &draftAPI{t: t, f: f, mux: mux, user: f.AppUser(t, f.OrgA, "wp260-admin")}
}

// response is a recorded response with the request id it ran under.
type response struct {
	*httptest.ResponseRecorder
	requestID string
}

// do runs a request of org with the permissions perms (nil: all) and an
// optional If-Match.
func (a *draftAPI) do(org string, perms []identity.Permission, method, path, body, ifMatch string) response {
	a.t.Helper()
	a.mu.Lock()
	a.n++
	requestID := "wp260-" + strconv.Itoa(a.n)
	a.mu.Unlock()
	if perms == nil {
		perms = identity.AllPermissions()
	}
	scope := database.OrgWideScope(org, a.user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: a.user})
	ctx = audit.WithRequestInfo(identity.WithPermissions(ctx, perms),
		audit.RequestInfo{RequestID: requestID, CorrelationID: "wp260-drafts", Source: audit.SourceUI})
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return response{ResponseRecorder: w, requestID: requestID}
}

func (a *draftAPI) call(org, method, path, body string) response {
	a.t.Helper()
	return a.do(org, nil, method, path, body, "")
}

// must calls and fails unless the status is want; it decodes into out.
func (a *draftAPI) must(want int, org, method, path, body string, out any) response {
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
	return w
}

func (a *draftAPI) id(org, method, path, body string) string {
	a.t.Helper()
	var obj struct {
		ID string `json:"id"`
	}
	want := http.StatusCreated
	if method == http.MethodPut {
		want = http.StatusOK
	}
	a.must(want, org, method, path, body, &obj)
	return obj.ID
}

func (a *draftAPI) draft(org, id string) metamodel.Draft {
	a.t.Helper()
	var d metamodel.Draft
	a.must(http.StatusOK, org, http.MethodGet, "/api/v1/metamodel/drafts/"+id, "", &d)
	return d
}

func (a *draftAPI) newDraft(org, name string) metamodel.Draft {
	a.t.Helper()
	var d metamodel.Draft
	w := a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/metamodel/drafts", `{"name":"`+name+`"}`, &d)
	if got := w.Header().Get("ETag"); got != `"1"` {
		a.t.Fatalf("ETag of a new draft = %s, want \"1\"", got)
	}
	return d
}

// addItem posts an item and returns the response.
func (a *draftAPI) addItem(org, draftID, body string) response {
	a.t.Helper()
	return a.call(org, http.MethodPost, "/api/v1/metamodel/drafts/"+draftID+"/items", body)
}

// auditOf checks that the request wrote exactly one metamodel_draft entry
// with the action, after and (unless created) before.
func (a *draftAPI) auditOf(w response, action string) {
	a.t.Helper()
	var n, drafts int
	var hasBefore, hasAfter bool
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT count(*),
			count(*) FILTER (WHERE action = $2 AND resource_type = 'metamodel_draft'),
			COALESCE(bool_and(changes ? 'before'), false), COALESCE(bool_and(changes ? 'after'), false)
		FROM audit_log WHERE request_id = $1`, w.requestID, action).Scan(&n, &drafts, &hasBefore, &hasAfter); err != nil {
		a.t.Fatal(err)
	}
	wantBefore := action != "metamodel_draft.created"
	if n != 1 || drafts != 1 || !hasAfter || hasBefore != wantBefore {
		a.t.Errorf("%s (%s): %d entries, %d of the action, before %v after %v; want exactly one with before %v and after",
			action, w.requestID, n, drafts, hasBefore, hasAfter, wantBefore)
	}
}

func (a *draftAPI) noAudit(w response) {
	a.t.Helper()
	var n int
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE request_id = $1`,
		w.requestID).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	if n != 0 {
		a.t.Errorf("refused request %s wrote %d audit entries", w.requestID, n)
	}
}

func (a *draftAPI) updatedAt(table, id string) time.Time {
	a.t.Helper()
	var at time.Time
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT updated_at FROM `+table+` WHERE id = $1`, id).Scan(&at); err != nil {
		a.t.Fatalf("updated_at of %s %s: %v", table, id, err)
	}
	return at
}

func (a *draftAPI) fieldUpdatedAt(typeID, key string) time.Time {
	a.t.Helper()
	var at time.Time
	q := `SELECT updated_at FROM attribute_definition WHERE ci_type_id = $1 AND key = $2`
	args := []any{typeID, key}
	if typeID == "" {
		q = `SELECT updated_at FROM attribute_definition WHERE scope = 'global' AND organization_id = $1 AND key = $2`
		args = []any{a.f.OrgA, key}
	}
	if err := a.f.Admin.QueryRow(context.Background(), q, args...).Scan(&at); err != nil {
		a.t.Fatalf("updated_at of field %s: %v", key, err)
	}
	return at
}

// sameBase compares the base of an item with want (a time, a number, a
// string, a list of ids or nil).
func sameBase(base json.RawMessage, want any) bool {
	if at, ok := want.(time.Time); ok {
		var got time.Time
		return json.Unmarshal(base, &got) == nil && got.Equal(at)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return false
	}
	var g, w any
	return json.Unmarshal(base, &g) == nil && json.Unmarshal(raw, &w) == nil && jsonEqual(g, w)
}

func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

// noReport reports whether a test report is absent (null).
func noReport(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

type itemPage struct {
	Data       []metamodel.Item `json:"data"`
	Total      int              `json:"total"`
	NextCursor string           `json:"next_cursor"`
}

type draftPage struct {
	Data       []metamodel.Draft `json:"data"`
	Total      int               `json:"total"`
	NextCursor string            `json:"next_cursor"`
}

// TestMetamodelDraftsAgainstPostgres covers MET-53 and API-06 (WP-260): the
// draft lifecycle over /api/v1/metamodel/drafts (create, read, list with
// status filter and cursor, rename, discard), items for every pair of
// metamodel.ItemKinds with the base of their target and the schema
// versions of the affected types, item references of E-112, strict
// payloads (400), system targets (403, 404), the reset of a tested draft,
// immutable published and discarded drafts (409), If-Match (412), parallel
// item captures under the row lock, the item permission (403), one audit
// entry metamodel_draft.* per change and the isolation of organizations.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetamodelDraftsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e2")
	a := newDraftAPI(t, f)
	bg := context.Background()
	orgA := f.OrgA

	// Fixtures over the resource routes (direct writes, MET-04).
	typ := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp260_type","name":"wp260 type"}`)
	def := a.id(orgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typ+`","key":"wp260_attr","data_type":"string"}`)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-types/"+typ+"/fields", `{"name":"wp260_field","data_type":"string"}`, nil)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-fields/global", `{"name":"wp260_global","data_type":"string"}`, nil)
	set := a.id(orgA, http.MethodPost, "/api/v1/attribute-sets", `{"key":"wp260_set"}`)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-types/"+typ+"/attribute-sets",
		`{"attribute_sets":[{"attribute_set_id":"`+set+`","position":0}]}`, nil)
	tpl := a.id(orgA, http.MethodPost, "/api/v1/templates", `{"key":"wp260_tpl","ci_type_id":"`+typ+`"}`)
	var v1 citype.TemplateVersion
	a.must(http.StatusOK, orgA, http.MethodPost, "/api/v1/templates/"+tpl+"/versions/1/publish", `{}`, &v1)
	rule := a.id(orgA, http.MethodPost, "/api/v1/rules",
		`{"key":"wp260_rule","ci_type_id":"`+typ+`","attribute_key":"wp260_attr","kind":"required"}`)
	layout := a.id(orgA, http.MethodPost, "/api/v1/layouts", `{"key":"wp260_layout","ci_type_id":"`+typ+
		`","layout":{"sections":[{"key":"main","title":{"de":"Haupt","en":"Main"},"fields":[{"name":"wp260_attr","width":6}]}]}}`)
	var schemaVersion int64
	if err := f.Admin.QueryRow(bg, `SELECT schema_version FROM ci_type WHERE id = $1`, typ).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}

	// ── Lifecycle ────────────────────────────────────────────────────────
	created := a.call(orgA, http.MethodPost, "/api/v1/metamodel/drafts", `{"name":"wp260 all pairs"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create draft: %d %s", created.Code, created.Body.String())
	}
	a.auditOf(created, "metamodel_draft.created")
	var d metamodel.Draft
	if err := json.Unmarshal(created.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Status != metamodel.StatusDraft || d.Version != 1 || d.ItemCount != 0 || d.CreatedBy == nil || *d.CreatedBy != a.user ||
		d.PublishedAt != nil {
		t.Fatalf("new draft = %+v", d)
	}
	if w := a.call(orgA, http.MethodPost, "/api/v1/metamodel/drafts", `{"name":"x","bogus":1}`); w.Code != http.StatusBadRequest {
		t.Errorf("draft with an unknown field = %d, want 400", w.Code)
	}
	if w := a.call(orgA, http.MethodPost, "/api/v1/metamodel/drafts", `{"name":" "}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("draft without name = %d, want 422", w.Code)
	}
	renamed := a.do(orgA, nil, http.MethodPatch, "/api/v1/metamodel/drafts/"+d.ID, `{"name":"wp260 renamed"}`, `"1"`)
	if renamed.Code != http.StatusOK || renamed.Header().Get("ETag") != `"2"` {
		t.Fatalf("rename: %d %s ETag %s", renamed.Code, renamed.Body.String(), renamed.Header().Get("ETag"))
	}
	a.auditOf(renamed, "metamodel_draft.updated")
	if got := a.draft(orgA, d.ID); got.Name != "wp260 renamed" || got.Version != 2 {
		t.Errorf("renamed draft = %q version %d", got.Name, got.Version)
	}
	stale := a.do(orgA, nil, http.MethodPatch, "/api/v1/metamodel/drafts/"+d.ID, `{"name":"stale"}`, `"1"`)
	if stale.Code != http.StatusPreconditionFailed {
		t.Errorf("rename with a stale If-Match = %d, want 412", stale.Code)
	}
	a.noAudit(stale)

	// ── Items for every pair of ItemKinds ────────────────────────────────
	cases := []struct {
		kind, op, target, key, payload string
		base                           any
		typed                          bool
	}{
		{"ci_type", "create", "", "", `{"key":"wp260_new","name":"wp260 new"}`, nil, false},
		{"ci_type", "update", typ, "", `{"description":"changed"}`, a.updatedAt("ci_type", typ), true},
		{"ci_type", "clone", typ, "", `{"key":"wp260_clone","name":"wp260 clone"}`, a.updatedAt("ci_type", typ), true},
		{"ci_type", "activate", typ, "", ``, a.updatedAt("ci_type", typ), true},
		{"ci_type", "deactivate", typ, "", `{}`, a.updatedAt("ci_type", typ), true},
		{"ci_type", "assign", typ, "", `{"attribute_sets":[{"attribute_set_id":"` + set + `","position":1}]}`, []string{set}, true},
		{"ci_type_field", "upsert", typ, "", `{"name":"wp260_field","data_type":"string","label":"Field"}`,
			a.fieldUpdatedAt(typ, "wp260_field"), true},
		{"ci_type_field", "delete", typ, "wp260_field", ``, a.fieldUpdatedAt(typ, "wp260_field"), true},
		{"global_field", "upsert", "", "", `{"name":"wp260_global","data_type":"string"}`, a.fieldUpdatedAt("", "wp260_global"), false},
		{"global_field", "delete", "", "wp260_global", ``, a.fieldUpdatedAt("", "wp260_global"), false},
		{"attribute_definition", "create", "", "", `{"ci_type_id":"` + typ + `","key":"wp260_new_attr","data_type":"string"}`, nil, true},
		{"attribute_definition", "update", def, "", `{"description":"changed"}`, a.updatedAt("attribute_definition", def), true},
		{"attribute_definition", "delete", def, "", ``, a.updatedAt("attribute_definition", def), true},
		{"attribute_set", "create", "", "", `{"key":"wp260_set2"}`, nil, false},
		{"attribute_set", "update", set, "", `{"description":"changed"}`, a.updatedAt("attribute_set", set), true},
		{"attribute_set", "delete", set, "", ``, a.updatedAt("attribute_set", set), true},
		{"template", "create", "", "", `{"key":"wp260_tpl2","ci_type_id":"` + typ + `"}`, nil, true},
		{"template", "update", tpl, "", `{"description":"changed"}`, a.updatedAt("template", tpl), true},
		{"template", "delete", tpl, "", ``, a.updatedAt("template", tpl), true},
		{"template", "clone", tpl, "", `{"key":"wp260_tpl_clone"}`, a.updatedAt("template", tpl), true},
		{"template_version", "create", tpl, "", ``, 1, true},
		{"template_version", "update", v1.ID, "", `{"items":[]}`, "published", true},
		{"template_version", "publish", v1.ID, "", ``, "published", true},
		{"rule", "create", "", "", `{"key":"wp260_rule2","ci_type_id":"` + typ + `","attribute_key":"wp260_attr","kind":"required"}`, nil, true},
		{"rule", "update", rule, "", `{"kind":"readonly"}`, 1, true},
		{"rule", "delete", rule, "", ``, 1, true},
		{"layout", "create", "", "", `{"key":"wp260_layout2","ci_type_id":"` + typ +
			`","layout":{"sections":[{"key":"main","title":{"de":"Haupt","en":"Main"},"fields":[{"name":"wp260_attr"}]}]}}`, nil, true},
		{"layout", "update", layout, "", `{"priority":1}`, 1, true},
		{"layout", "delete", layout, "", ``, 1, true},
	}
	covered := map[string]bool{}
	version := int64(2)
	for _, c := range cases {
		covered[c.kind+"/"+c.op] = true
		body := map[string]any{"kind": c.kind, "op": c.op}
		if c.target != "" {
			body["target_id"] = c.target
		}
		if c.key != "" {
			body["target_key"] = c.key
		}
		if c.payload != "" {
			body["payload"] = json.RawMessage(c.payload)
		}
		raw, _ := json.Marshal(body)
		w := a.addItem(orgA, d.ID, string(raw))
		if w.Code != http.StatusCreated {
			t.Errorf("%s/%s: %d %s, want 201", c.kind, c.op, w.Code, w.Body.String())
			continue
		}
		version++
		if got := w.Header().Get("ETag"); got != `"`+strconv.FormatInt(version, 10)+`"` {
			t.Errorf("%s/%s: ETag %s, want version %d", c.kind, c.op, got, version)
		}
		a.auditOf(w, "metamodel_draft.item_added")
		var it metamodel.Item
		if err := json.Unmarshal(w.Body.Bytes(), &it); err != nil {
			t.Fatal(err)
		}
		if it.Kind != c.kind || it.Op != c.op || it.ID == "" || it.Route == "" || strings.Contains(it.Route, "{") ||
			it.CreatedBy == nil || *it.CreatedBy != a.user {
			t.Errorf("%s/%s: item %+v", c.kind, c.op, it)
		}
		if (c.target == "") != (it.TargetID == nil) {
			t.Errorf("%s/%s: target_id %v, want %q", c.kind, c.op, it.TargetID, c.target)
		}
		if !sameBase(it.Base, c.base) {
			t.Errorf("%s/%s: base %s, want %v", c.kind, c.op, it.Base, c.base)
		}
		if c.typed && it.SchemaVersions[typ] != schemaVersion {
			t.Errorf("%s/%s: schema_versions %v, want %s: %d", c.kind, c.op, it.SchemaVersions, typ, schemaVersion)
		}
	}
	for kind, ops := range metamodel.ItemKinds {
		for op := range ops {
			if !covered[kind+"/"+op] {
				t.Errorf("no case for the pair %s/%s", kind, op)
			}
		}
	}
	all := a.draft(orgA, d.ID)
	if all.ItemCount != len(cases) || all.Version != version {
		t.Errorf("draft after the items: %d items version %d, want %d and %d", all.ItemCount, all.Version, len(cases), version)
	}
	// The items are captured, not applied (MET-53).
	var description *string
	if err := f.Admin.QueryRow(bg, `SELECT description FROM ci_type WHERE id = $1`, typ).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description != nil {
		t.Errorf("a draft item changed the type: description %q", *description)
	}

	// The item list pages newest first with limit and cursor.
	var p1, p2 itemPage
	a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts/"+d.ID+"/items?limit=20", "", &p1)
	if len(p1.Data) != 20 || p1.NextCursor == "" || p1.Total != len(cases) {
		t.Fatalf("first item page: %d items, total %d, cursor %q", len(p1.Data), p1.Total, p1.NextCursor)
	}
	a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts/"+d.ID+"/items?limit=20&cursor="+
		url.QueryEscape(p1.NextCursor), "", &p2)
	if len(p2.Data) != len(cases)-20 || p2.NextCursor != "" {
		t.Errorf("second item page: %d items, cursor %q", len(p2.Data), p2.NextCursor)
	}

	// ── Refusals of items ────────────────────────────────────────────────
	refusals := []struct {
		name  string
		perms []identity.Permission
		body  string
		want  int
	}{
		{"unknown payload field", nil, `{"kind":"ci_type","op":"update","target_id":"` + typ + `","payload":{"bogus":1}}`, http.StatusBadRequest},
		{"payload of the wrong type", nil, `{"kind":"rule","op":"update","target_id":"` + rule + `","payload":{"kind":1}}`, http.StatusBadRequest},
		{"unknown field without payload", nil, `{"kind":"ci_type","op":"activate","target_id":"` + typ + `","payload":{"x":1}}`, http.StatusBadRequest},
		{"unknown kind", nil, `{"kind":"lifecycle_model","op":"create","payload":{}}`, http.StatusUnprocessableEntity},
		{"pair not in ItemKinds", nil, `{"kind":"ci_type","op":"publish","target_id":"` + typ + `"}`, http.StatusUnprocessableEntity},
		{"create with target", nil, `{"kind":"rule","op":"create","target_id":"` + rule + `","payload":{}}`, http.StatusUnprocessableEntity},
		{"update without target", nil, `{"kind":"rule","op":"update","payload":{}}`, http.StatusUnprocessableEntity},
		{"field delete without key", nil, `{"kind":"ci_type_field","op":"delete","target_id":"` + typ + `"}`, http.StatusUnprocessableEntity},
		{"missing target", nil, `{"kind":"rule","op":"delete","target_id":"` + f.ID() + `"}`, http.StatusNotFound},
		{"missing field", nil, `{"kind":"ci_type_field","op":"delete","target_id":"` + typ + `","target_key":"wp260_none"}`, http.StatusNotFound},
		{"attribute item without ci_attribute:manage", []identity.Permission{identity.PermCITypeManage},
			`{"kind":"attribute_definition","op":"update","target_id":"` + def + `","payload":{}}`, http.StatusForbidden},
		{"publish without metamodel:publish", []identity.Permission{identity.PermCITypeManage},
			`{"kind":"template_version","op":"publish","target_id":"` + v1.ID + `"}`, http.StatusForbidden},
	}
	var sysType, sysDef string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id IS NULL ORDER BY key LIMIT 1`).Scan(&sysType); err != nil {
		t.Fatalf("system type: %v", err)
	}
	// A system attribute definition as a migration would insert it.
	if _, err := f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE organization_id IS NULL AND key = 'wp260_sys'`); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO attribute_definition (ci_type_id, scope, key, data_type)
		VALUES ($1, 'type', 'wp260_sys', 'text') RETURNING id::text`, sysType).Scan(&sysDef); err != nil {
		t.Fatalf("system attribute definition: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = $1`, sysDef); err != nil {
			t.Errorf("cleanup system attribute definition: %v", err)
		}
	})
	refusals = append(refusals,
		struct {
			name  string
			perms []identity.Permission
			body  string
			want  int
		}{"system attribute definition", nil, `{"kind":"attribute_definition","op":"update","target_id":"` + sysDef + `","payload":{}}`, http.StatusForbidden},
		struct {
			name  string
			perms []identity.Permission
			body  string
			want  int
		}{"system type as update target", nil, `{"kind":"ci_type","op":"update","target_id":"` + sysType + `","payload":{}}`, http.StatusNotFound},
	)
	for _, r := range refusals {
		w := a.do(orgA, r.perms, http.MethodPost, "/api/v1/metamodel/drafts/"+d.ID+"/items", r.body, "")
		if w.Code != r.want {
			t.Errorf("%s: %d %s, want %d", r.name, w.Code, w.Body.String(), r.want)
		}
		a.noAudit(w)
	}
	if got := a.draft(orgA, d.ID); got.Version != version || got.ItemCount != len(cases) {
		t.Errorf("refused items changed the draft: version %d, %d items", got.Version, got.ItemCount)
	}

	// ── Item references (E-112) ──────────────────────────────────────────
	refDraft := a.newDraft(orgA, "wp260 references")
	var parent, child metamodel.Item
	if err := json.Unmarshal(a.addItem(orgA, refDraft.ID, `{"kind":"ci_type","op":"create","payload":{"key":"wp260_ref","name":"wp260 ref"}}`).
		Body.Bytes(), &parent); err != nil || parent.ID == "" {
		t.Fatalf("parent item: %v", err)
	}
	w := a.addItem(orgA, refDraft.ID, `{"kind":"attribute_definition","op":"create","payload":{"ci_type_id":{"$item":"`+parent.ID+
		`"},"key":"wp260_ref_attr","data_type":"string"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("item with a reference: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(child.Payload), `{"$item":"`+parent.ID+`"}`) {
		t.Errorf("the payload lost its reference: %s", child.Payload)
	}
	var update metamodel.Item
	_ = json.Unmarshal(a.addItem(orgA, refDraft.ID, `{"kind":"ci_type","op":"update","target_id":"`+typ+`","payload":{}}`).Body.Bytes(), &update)
	for name, ref := range map[string]string{"unknown item": f.ID(), "item of another draft": all.Items[0].ID, "update item": update.ID} {
		w := a.addItem(orgA, refDraft.ID, `{"kind":"rule","op":"create","payload":{"key":"wp260_x","ci_type_id":{"$item":"`+ref+
			`"},"attribute_key":"wp260_attr","kind":"required"}}`)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("reference to the %s: %d %s, want 422", name, w.Code, w.Body.String())
		}
	}
	if w := a.addItem(orgA, refDraft.ID, `{"kind":"rule","op":"create","payload":{"key":"wp260_x","ci_type_id":{"$item":"`+parent.ID+
		`","x":1},"attribute_key":"wp260_attr","kind":"required"}}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("reference with a second key: %d, want 422", w.Code)
	}
	referenced := a.call(orgA, http.MethodDelete, "/api/v1/metamodel/drafts/"+refDraft.ID+"/items/"+parent.ID, "")
	if referenced.Code != http.StatusConflict {
		t.Errorf("remove a referenced item: %d %s, want 409", referenced.Code, referenced.Body.String())
	}
	removed := a.call(orgA, http.MethodDelete, "/api/v1/metamodel/drafts/"+refDraft.ID+"/items/"+child.ID, "")
	if removed.Code != http.StatusOK {
		t.Fatalf("remove an item: %d %s", removed.Code, removed.Body.String())
	}
	a.auditOf(removed, "metamodel_draft.item_removed")
	if got := a.draft(orgA, refDraft.ID); got.ItemCount != 2 {
		t.Errorf("after the removal %d items, want 2", got.ItemCount)
	}
	if w := a.call(orgA, http.MethodDelete, "/api/v1/metamodel/drafts/"+refDraft.ID+"/items/"+child.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("remove a removed item: %d, want 404", w.Code)
	}

	// ── A tested draft returns to draft with an item change ─────────────
	if _, err := f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'tested', test_report = '{"ok": true}' WHERE id = $1`,
		refDraft.ID); err != nil {
		t.Fatal(err)
	}
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/metamodel/drafts/"+refDraft.ID+"/items",
		`{"kind":"attribute_set","op":"create","payload":{"key":"wp260_set3"}}`, nil)
	if got := a.draft(orgA, refDraft.ID); got.Status != metamodel.StatusDraft || !noReport(got.TestReport) {
		t.Errorf("after an item change: status %s, test_report %s", got.Status, got.TestReport)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'tested', test_report = '{"ok": true}' WHERE id = $1`,
		refDraft.ID); err != nil {
		t.Fatal(err)
	}
	a.must(http.StatusOK, orgA, http.MethodDelete, "/api/v1/metamodel/drafts/"+refDraft.ID+"/items/"+update.ID, "", nil)
	if got := a.draft(orgA, refDraft.ID); got.Status != metamodel.StatusDraft || !noReport(got.TestReport) {
		t.Errorf("after an item removal: status %s, test_report %s", got.Status, got.TestReport)
	}

	// ── Parallel item captures under the row lock ────────────────────────
	par := a.newDraft(orgA, "wp260 parallel")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = a.addItem(orgA, par.ID, `{"kind":"attribute_set","op":"create","payload":{"key":"wp260_par`+strconv.Itoa(i)+`"}}`).Code
		}(i)
	}
	wg.Wait()
	if got := a.draft(orgA, par.ID); codes[0] != http.StatusCreated || codes[1] != http.StatusCreated ||
		got.ItemCount != 2 || got.Version != par.Version+2 {
		t.Errorf("parallel items: codes %v, %d items, version %d, want 2 items and version %d", codes, got.ItemCount, got.Version,
			par.Version+2)
	}

	// ── Published and discarded drafts are immutable ─────────────────────
	if _, err := f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'published', published_at = now() WHERE id = $1`, par.ID); err != nil {
		t.Fatal(err)
	}
	discarded := a.call(orgA, http.MethodDelete, "/api/v1/metamodel/drafts/"+refDraft.ID, "")
	if discarded.Code != http.StatusOK {
		t.Fatalf("discard: %d %s", discarded.Code, discarded.Body.String())
	}
	a.auditOf(discarded, "metamodel_draft.discarded")
	if got := a.draft(orgA, refDraft.ID); got.Status != metamodel.StatusDiscarded {
		t.Errorf("discarded draft has status %s", got.Status)
	}
	for _, id := range []string{par.ID, refDraft.ID} {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodPatch, "/api/v1/metamodel/drafts/" + id, `{"name":"late"}`},
			{http.MethodDelete, "/api/v1/metamodel/drafts/" + id, ""},
			{http.MethodPost, "/api/v1/metamodel/drafts/" + id + "/items", `{"kind":"attribute_set","op":"create","payload":{"key":"wp260_late"}}`},
			{http.MethodDelete, "/api/v1/metamodel/drafts/" + id + "/items/" + parent.ID, ""},
		} {
			w := a.call(orgA, req.method, req.path, req.body)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "urn:reticora:problem:conflict") {
				t.Errorf("%s %s on a final draft: %d %s, want 409 conflict", req.method, req.path, w.Code, w.Body.String())
			}
			a.noAudit(w)
		}
	}

	// ── Status filter and cursor of the draft list ───────────────────────
	var discardedOnly draftPage
	a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts?status=discarded", "", &discardedOnly)
	if len(discardedOnly.Data) != 1 || discardedOnly.Data[0].ID != refDraft.ID || discardedOnly.Data[0].Items != nil {
		t.Errorf("status=discarded lists %+v", discardedOnly.Data)
	}
	var l1, l2 draftPage
	a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts?limit=2", "", &l1)
	if len(l1.Data) != 2 || l1.Total != 3 || l1.NextCursor == "" || l1.Data[0].ID != par.ID {
		t.Fatalf("first draft page: %+v", l1)
	}
	a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts?limit=2&cursor="+url.QueryEscape(l1.NextCursor), "", &l2)
	if len(l2.Data) != 1 || l2.Data[0].ID != d.ID || l2.NextCursor != "" {
		t.Errorf("second draft page: %+v", l2)
	}
	if w := a.call(orgA, http.MethodGet, "/api/v1/metamodel/drafts?status=open", ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown status filter: %d, want 400", w.Code)
	}

	// ── Organizations are isolated (RLS) ─────────────────────────────────
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/metamodel/drafts/" + d.ID, ""},
		{http.MethodGet, "/api/v1/metamodel/drafts/" + d.ID + "/items", ""},
		{http.MethodPatch, "/api/v1/metamodel/drafts/" + d.ID, `{"name":"foreign"}`},
		{http.MethodDelete, "/api/v1/metamodel/drafts/" + d.ID, ""},
		{http.MethodPost, "/api/v1/metamodel/drafts/" + d.ID + "/items", `{"kind":"attribute_set","op":"create","payload":{"key":"wp260_b"}}`},
	} {
		if w := a.call(f.OrgB, req.method, req.path, req.body); w.Code != http.StatusNotFound {
			t.Errorf("org B %s %s: %d, want 404", req.method, req.path, w.Code)
		}
	}
	var listB draftPage
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/metamodel/drafts", "", &listB)
	if len(listB.Data) != 0 {
		t.Errorf("org B lists %d drafts of org A", len(listB.Data))
	}
	// The resource routes stay productive without a draft and write no
	// draft row (MET-04).
	a.must(http.StatusOK, orgA, http.MethodPatch, "/api/v1/ci-types/"+typ, `{"description":"direct"}`, nil)
	var drafts int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM metamodel_draft WHERE organization_id = $1`, orgA).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if drafts != 3 {
		t.Errorf("%d drafts after a direct write, want 3", drafts)
	}
}

// TestMetamodelDraftRoutesWithoutDatabase: without a repository every
// draft route answers 503 (in-memory profile).
func TestMetamodelDraftRoutesWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	metamodel.NewHandler(nil).RegisterRoutes(mux)
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/metamodel/drafts"},
		{http.MethodPost, "/api/v1/metamodel/drafts"},
		{http.MethodGet, "/api/v1/metamodel/drafts/x"},
		{http.MethodPatch, "/api/v1/metamodel/drafts/x"},
		{http.MethodDelete, "/api/v1/metamodel/drafts/x"},
		{http.MethodGet, "/api/v1/metamodel/drafts/x/items"},
		{http.MethodPost, "/api/v1/metamodel/drafts/x/items"},
		{http.MethodDelete, "/api/v1/metamodel/drafts/x/items/y"},
	} {
		ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(req.method, req.path, strings.NewReader("{}")).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s without database: %d, want 503", req.method, req.path, w.Code)
		}
	}
}
