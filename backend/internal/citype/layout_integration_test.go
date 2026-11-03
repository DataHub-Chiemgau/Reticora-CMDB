package citype_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type layoutPage struct {
	Data       []citype.FormLayout `json:"data"`
	Total      int                 `json:"total"`
	NextCursor string              `json:"next_cursor"`
}

func (a *setAPI) createLayout(org, body string) citype.FormLayout {
	a.t.Helper()
	var l citype.FormLayout
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/layouts", body, &l)
	return l
}

// callIfMatch is call with an If-Match header.
func (a *setAPI) callIfMatch(org, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(a.ctx(org))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", ifMatch)
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// section is a layout section with the title Haupt/Main.
func section(key, fields string) string {
	return `{"key":"` + key + `","title":{"de":"Haupt","en":"Main"},"fields":[` + fields + `]}`
}

// layoutBody is a create request for a target ("ci_type_id":"…") and an
// optional role with the given sections.
func layoutBody(key, target, role string, sections ...string) string {
	body := `{"key":"` + key + `",` + target
	if role != "" {
		body += `,"role_id":"` + role + `"`
	}
	return body + `,"layout":{"sections":[` + strings.Join(sections, ",") + `]}}`
}

func layoutKeys(p layoutPage) []string {
	keys := make([]string, 0, len(p.Data))
	for _, l := range p.Data {
		keys = append(keys, l.Key)
	}
	slices.Sort(keys)
	return keys
}

// inOrg runs stmt as the application role with app.org_id set to org (the
// role triggers then work under row level security).
func inOrg(t *testing.T, f *scopetest.Fixture, org, stmt string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.App.Begin(f.OrgCtx(org))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true)`, org); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestFormLayoutsAgainstPostgres covers MET-50 and API-06 (WP-259): CRUD of
// form layouts per CI type, template and role with the filters ci_type_id,
// template_id and role_id and cursor pagination. The layout is checked
// against the effective fields of the target (a template layout against
// the fields of the template's type); unknown fields, a field in two
// sections, duplicate or reserved section keys, a title without de or en,
// a width outside 1 to 12, an unknown rule key in visible_when, an
// invisible type, an unknown or foreign role (from role and custom_role)
// and a change of ci_type_id are 422. A second layout per target and role
// and a taken key are 409. If-Match with a stale version is 412, without
// If-Match the change is written. Changes are audited as form_layout.*
// with before and after. Deleting a role or a custom role removes its
// layouts; a clone takes over the type layouts with a new key; a layout
// of a system type is a row of the organization that org B does not see.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFormLayoutsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()

	typ := a.createType(f.OrgA, "wp259-type")
	foreignType := a.createType(f.OrgB, "wp259-foreign")
	a.definition(f.OrgA, `{"ci_type_id":"`+typ.ID+`","key":"wp259_serial","data_type":"text"}`)
	a.definition(f.OrgA, `{"ci_type_id":"`+typ.ID+`","key":"wp259_note","data_type":"text"}`)
	tplType := a.createType(f.OrgA, "wp259-tpl-type")
	a.definition(f.OrgA, `{"ci_type_id":"`+tplType.ID+`","key":"wp259_rack","data_type":"text"}`)
	tpl := a.createTemplate(f.OrgA, tplType.ID, "wp259-tpl")
	a.createRule(f.OrgA, `{"key":"wp259-show","attribute_key":"wp259_note","kind":"readonly","ci_type_id":"`+typ.ID+`"}`)

	roleA, roleB, customA, roleGone, customGone, roleSpare := f.ID(), f.ID(), "", f.ID(), "", f.ID()
	for _, r := range []struct{ id, org, name string }{
		{roleA, f.OrgA, "wp259-reader"}, {roleB, f.OrgB, "wp259-reader"}, {roleGone, f.OrgA, "wp259-gone"},
		{roleSpare, f.OrgA, "wp259-spare"},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, '["ci:read"]')`,
			r.id, r.org, r.name); err != nil {
			t.Fatal(err)
		}
	}
	for name, dst := range map[string]*string{"wp259-custom": &customA, "wp259-custom-gone": &customGone} {
		if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
			VALUES ($1, $2, '["ci:read"]') RETURNING id::text`, f.OrgA, name).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	var customB string
	if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
		VALUES ($1, 'wp259-custom-b', '["ci:read"]') RETURNING id::text`, f.OrgB).Scan(&customB); err != nil {
		t.Fatal(err)
	}

	onType := `"ci_type_id":"` + typ.ID + `"`
	main := `{"key":"main","title":{"de":"Haupt","en":"Main"},"visible_when":["wp259-show"],
		"fields":[{"name":"wp259_serial","width":6},{"name":"wp259_note"}]}`

	// Create for the type (without role, with a role, with a custom role)
	// and for the template.
	base := a.createLayout(f.OrgA, layoutBody("wp259-base", onType, "", main))
	if base.OrganizationID != f.OrgA || base.Version != 1 || !base.IsActive || base.CITypeID != typ.ID || base.RoleID != "" ||
		len(base.Layout.Sections) != 1 || base.Layout.Sections[0].Fields[1].Width != 12 ||
		base.Layout.Sections[0].VisibleWhen[0] != "wp259-show" || base.Layout.Sections[0].Title["en"] != "Main" {
		t.Fatalf("created layout: %+v", base)
	}
	forRole := a.createLayout(f.OrgA, layoutBody("wp259-role", onType, roleA, section("s", `{"name":"wp259_note","width":4}`)))
	forCustom := a.createLayout(f.OrgA, layoutBody("wp259-custom", onType, customA, section("s", "")))
	a.createLayout(f.OrgA, layoutBody("wp259-gone", onType, roleGone, section("s", "")))
	a.createLayout(f.OrgA, layoutBody("wp259-custom-gone", onType, customGone, section("s", "")))
	onTpl := `"template_id":"` + tpl.ID + `"`
	tplLayout := a.createLayout(f.OrgA, layoutBody("wp259-tpl", onTpl, "", section("rack", `{"name":"wp259_rack","width":3}`)))
	if tplLayout.TemplateID != tpl.ID || tplLayout.CITypeID != "" {
		t.Fatalf("template layout: %+v", tplLayout)
	}

	// Refusals of the request.
	two := func(x, y string) string { return x + "," + y }
	for _, c := range []struct {
		name, body string
		status     int
		field      string
	}{
		{"taken key", layoutBody("wp259-base", `"ci_type_id":"`+tplType.ID+`"`, "", section("s", "")), http.StatusConflict, "key"},
		{"second layout per type and role", layoutBody("wp259-x", onType, roleA, section("s", "")), http.StatusConflict, "role_id"},
		{"second layout per type without role", layoutBody("wp259-x", onType, "", section("s", "")), http.StatusConflict, "role_id"},
		{"second layout per template", layoutBody("wp259-x", onTpl, "", section("s", "")), http.StatusConflict, "role_id"},
		{"no target", layoutBody("wp259-x", `"priority":1`, "", section("s", "")), http.StatusUnprocessableEntity, "ci_type_id"},
		{"two targets", layoutBody("wp259-x", onType+","+onTpl, "", section("s", "")), http.StatusUnprocessableEntity, "ci_type_id"},
		{"unknown field", layoutBody("wp259-x", onType, roleGone, section("s", `{"name":"wp259_rack"}`)),
			http.StatusUnprocessableEntity, "layout.sections[0].fields[0].name"},
		{"foreign role", layoutBody("wp259-x", onType, roleB, section("s", "")), http.StatusUnprocessableEntity, "role_id"},
		{"foreign custom role", layoutBody("wp259-x", onType, customB, section("s", "")), http.StatusUnprocessableEntity, "role_id"},
		{"field unknown to the type", layoutBody("wp259-x", `"ci_type_id":"`+tplType.ID+`"`, "",
			section("a", ""), section("b", `{"name":"wp259_note"}`)), http.StatusUnprocessableEntity, "layout.sections[1].fields[0].name"},
		{"template layout with a field of another type", layoutBody("wp259-x", `"template_id":"`+tpl.ID+`"`, roleA,
			section("s", `{"name":"wp259_serial"}`)), http.StatusUnprocessableEntity, "layout.sections[0].fields[0].name"},
		{"field in two sections", layoutBody("wp259-x", onType, customGone, two(section("a", `{"name":"wp259_note"}`),
			section("b", `{"name":"wp259_note"}`))), http.StatusUnprocessableEntity, "layout.sections[1].fields[0].name"},
		{"duplicate section key", layoutBody("wp259-x", onType, roleGone, section("a", ""), section("a", "")),
			http.StatusUnprocessableEntity, "layout.sections[1].key"},
		{"reserved section key _instance", layoutBody("wp259-x", onType, roleGone, section("_instance", "")),
			http.StatusUnprocessableEntity, "layout.sections[0].key"},
		{"reserved section key _required", layoutBody("wp259-x", onType, roleGone, section("_required", "")),
			http.StatusUnprocessableEntity, "layout.sections[0].key"},
		{"title without en", layoutBody("wp259-x", onType, roleGone, `{"key":"s","title":{"de":"Haupt"},"fields":[]}`),
			http.StatusUnprocessableEntity, "layout.sections[0].title"},
		{"title without de", layoutBody("wp259-x", onType, roleGone, `{"key":"s","title":{"en":"Main"},"fields":[]}`),
			http.StatusUnprocessableEntity, "layout.sections[0].title"},
		{"width 0", layoutBody("wp259-x", onType, roleGone, section("s", `{"name":"wp259_note","width":0}`)),
			http.StatusUnprocessableEntity, "layout.sections[0].fields[0].width"},
		{"width 13", layoutBody("wp259-x", onType, roleGone, section("s", `{"name":"wp259_note","width":13}`)),
			http.StatusUnprocessableEntity, "layout.sections[0].fields[0].width"},
		{"unknown rule key", layoutBody("wp259-x", onType, roleGone,
			`{"key":"s","title":{"de":"A","en":"A"},"visible_when":["wp259-show","wp259-nope"],"fields":[]}`),
			http.StatusUnprocessableEntity, "layout.sections[0].visible_when[1]"},
		{"invisible type", layoutBody("wp259-x", `"ci_type_id":"`+foreignType.ID+`"`, "", section("s", "")),
			http.StatusUnprocessableEntity, "ci_type_id"},
		{"unknown type", layoutBody("wp259-x", `"ci_type_id":"`+f.ID()+`"`, "", section("s", "")),
			http.StatusUnprocessableEntity, "ci_type_id"},
		{"unknown template", layoutBody("wp259-x", `"template_id":"`+f.ID()+`"`, "", section("s", "")),
			http.StatusUnprocessableEntity, "template_id"},
		{"unknown role", layoutBody("wp259-x", onType, f.ID(), section("s", "")), http.StatusUnprocessableEntity, "role_id"},
		{"no layout", `{"key":"wp259-x",` + onType + `}`, http.StatusUnprocessableEntity, "layout"},
	} {
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/layouts", c.body)
		if p := problemOf(w); w.Code != c.status || p.Field != c.field {
			t.Errorf("%s: %d %s, want %d on %s", c.name, w.Code, w.Body.String(), c.status, c.field)
		}
	}

	// List with filters and cursor.
	seen := map[string]bool{}
	path := "/api/v1/layouts?limit=2&ci_type_id=" + typ.ID
	for pages := 0; path != ""; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		var page layoutPage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &page)
		if len(page.Data) > 2 || page.Total != 5 {
			t.Fatalf("page of %d layouts (total %d), limit 2", len(page.Data), page.Total)
		}
		for _, l := range page.Data {
			if seen[l.ID] || l.CITypeID != typ.ID {
				t.Errorf("listed layout %+v (seen %v)", l, seen[l.ID])
			}
			seen[l.ID] = true
		}
		path = ""
		if page.NextCursor != "" {
			path = "/api/v1/layouts?limit=2&ci_type_id=" + typ.ID + "&cursor=" + page.NextCursor
		}
	}
	if len(seen) != 5 {
		t.Errorf("listed %d type layouts, want 5", len(seen))
	}
	for query, want := range map[string]string{
		"template_id=" + tpl.ID: "wp259-tpl",
		"role_id=" + roleA:      "wp259-role",
		"role_id=" + customA:    "wp259-custom",
	} {
		var page layoutPage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/layouts?"+query, "", &page)
		if keys := layoutKeys(page); strings.Join(keys, " ") != want {
			t.Errorf("layouts with %s: %v, want %s", query, keys, want)
		}
	}
	a.must(http.StatusBadRequest, f.OrgA, http.MethodGet, "/api/v1/layouts?cursor=forged", "", nil)

	// Get with ETag, patch with and without If-Match, immutable fields.
	w := a.call(f.OrgA, http.MethodGet, "/api/v1/layouts/"+base.ID, "")
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s, ETag %q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/layouts/"+f.ID(), "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/layouts/not-a-uuid", "", nil)
	var updated citype.FormLayout
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, `{"priority":5,"is_active":false}`, &updated)
	if updated.Version != 2 || updated.Priority != 5 || updated.IsActive || len(updated.Layout.Sections) != 1 {
		t.Errorf("patched without If-Match: %+v", updated)
	}
	if w = a.callIfMatch(f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, `{"priority":6}`, `"1"`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d %s", w.Code, w.Body.String())
	}
	if w = a.callIfMatch(f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, `{"priority":6}`, `x`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("invalid If-Match: %d %s", w.Code, w.Body.String())
	}
	w = a.callIfMatch(f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID,
		`{"role_id":"`+roleSpare+`","layout":{"sections":[`+section("other", `{"name":"wp259_serial","width":12}`)+`]}}`, `"2"`)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("patch with If-Match: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.RoleID != roleSpare || updated.Layout.Sections[0].Key != "other" {
		t.Errorf("patched with If-Match: %+v", updated)
	}
	updated = citype.FormLayout{}
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, `{"role_id":null}`, &updated)
	if updated.RoleID != "" || updated.Version != 4 {
		t.Errorf("role unbound: %+v", updated)
	}
	for field, body := range map[string]string{
		"key":                                 `{"key":"other"}`,
		"ci_type_id":                          `{"ci_type_id":"` + tplType.ID + `"}`,
		"template_id":                         `{"template_id":"` + tpl.ID + `"}`,
		"role_id":                             `{"role_id":"` + roleB + `"}`,
		"layout.sections[0].fields[0].name":   `{"layout":{"sections":[` + section("s", `{"name":"wp259_rack"}`) + `]}}`,
		"layout.sections[0].visible_when[0]":  `{"layout":{"sections":[{"key":"s","title":{"de":"A","en":"A"},"visible_when":["x"],"fields":[]}]}}`,
		"layout.sections[0].fields[0].width":  `{"layout":{"sections":[` + section("s", `{"name":"wp259_note","width":-1}`) + `]}}`,
		"layout.sections[0].key":              `{"layout":{"sections":[` + section("_instance", "") + `]}}`,
		"layout.sections[1].fields[0].name":   `{"layout":{"sections":[` + section("a", `{"name":"wp259_note"}`) + `,` + section("b", `{"name":"wp259_note"}`) + `]}}`,
		"layout.sections[0].title":            `{"layout":{"sections":[{"key":"s","title":{"en":"A"},"fields":[]}]}}`,
		"layout.sections[0].fields[0].width ": `{"layout":{"sections":[` + section("s", `{"name":"wp259_note","width":20}`) + `]}}`,
	} {
		w = a.call(f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, body)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != strings.TrimSpace(field) {
			t.Errorf("patch %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// A role already bound on the same type is a conflict.
	a.must(http.StatusConflict, f.OrgA, http.MethodPatch, "/api/v1/layouts/"+base.ID, `{"role_id":"`+roleA+`"}`, nil)

	// Delete with a stale If-Match is 412, then without.
	if w = a.callIfMatch(f.OrgA, http.MethodDelete, "/api/v1/layouts/"+forCustom.ID, "", `"7"`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("delete with stale If-Match: %d %s", w.Code, w.Body.String())
	}
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/layouts/"+forCustom.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/layouts/"+forCustom.ID, "", nil)

	// Deleting a role or a custom role removes its layouts.
	inOrg(t, f, f.OrgA, `DELETE FROM role WHERE id = $1`, roleGone)
	inOrg(t, f, f.OrgA, `DELETE FROM custom_role WHERE id = $1`, customGone)
	var page layoutPage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/layouts?limit=200&ci_type_id="+typ.ID, "", &page)
	if keys := layoutKeys(page); strings.Join(keys, " ") != "wp259-base wp259-role" {
		t.Errorf("layouts after deleting the roles: %v", keys)
	}

	// A clone takes over the type layouts with a new key.
	var clone citype.Type
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/ci-types/"+typ.ID+"/clone", `{"key":"wp259-clone","name":"wp259 clone"}`, &clone)
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/layouts?ci_type_id="+clone.ID, "", &page)
	if keys := layoutKeys(page); strings.Join(keys, " ") != "wp259-clone.wp259-base wp259-clone.wp259-role" {
		t.Errorf("layouts of the clone: %v", keys)
	}
	for _, l := range page.Data {
		if l.Version != 1 || l.OrganizationID != f.OrgA {
			t.Errorf("cloned layout: %+v", l)
		}
		if l.Key == "wp259-clone.wp259-role" && (l.RoleID != roleA || l.Layout.Sections[0].Fields[0].Width != 4) {
			t.Errorf("cloned role layout: %+v (source %+v)", l, forRole)
		}
		if l.Key == "wp259-clone.wp259-base" && (l.Priority != 5 || l.IsActive) {
			t.Errorf("cloned base layout: %+v", l)
		}
	}

	// A layout of a system type is a row of the organization; org B does
	// not see it.
	sysType := systemTypeID(t, f, "server")
	onSystem := a.createLayout(f.OrgA, layoutBody("wp259-system", `"ci_type_id":"`+sysType+`"`, "", section("s", "")))
	if onSystem.OrganizationID != f.OrgA {
		t.Errorf("layout of a system type: %+v", onSystem)
	}
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, "/api/v1/layouts/"+onSystem.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodPatch, "/api/v1/layouts/"+onSystem.ID, `{"priority":1}`, nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodDelete, "/api/v1/layouts/"+onSystem.ID, "", nil)
	// Org B may have its own layout for the same system type and key.
	a.createLayout(f.OrgB, layoutBody("wp259-system", `"ci_type_id":"`+sysType+`"`, roleB, section("s", "")))
	var pageB layoutPage
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/layouts?limit=200", "", &pageB)
	if len(pageB.Data) != 1 || pageB.Data[0].OrganizationID != f.OrgB {
		t.Errorf("org B lists %+v", pageB.Data)
	}

	// Audit: form_layout.* with before and after, actor and request id;
	// the cloned layouts and the layouts removed with a role have no entry
	// of their own.
	rows, err := f.Admin.Query(bg, `SELECT action, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''),
		COALESCE(request_id, ''), changes FROM audit_log
		WHERE organization_id = $1 AND resource_type = 'form_layout' ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	byResource := map[string][]map[string]any{}
	for rows.Next() {
		var action, resource, actor, request string
		var changes map[string]any
		if err = rows.Scan(&action, &resource, &actor, &request, &changes); err != nil {
			t.Fatal(err)
		}
		counts[action]++
		if actor != a.user || !strings.HasPrefix(request, "wp250-") {
			t.Errorf("%s: actor %s, request %s", action, actor, request)
		}
		byResource[resource] = append(byResource[resource], changes)
	}
	// base, role, custom, gone, custom-gone, template, system; base 3x.
	if counts["form_layout.created"] != 7 || counts["form_layout.updated"] != 3 || counts["form_layout.deleted"] != 1 {
		t.Errorf("audit counts: %v", counts)
	}
	if entries := byResource[base.ID]; len(entries) != 4 || entries[0]["before"] != nil || entries[0]["after"] == nil {
		t.Errorf("audit of %s: %+v", base.ID, entries)
	} else {
		before, _ := entries[1]["before"].(map[string]any)
		after, _ := entries[1]["after"].(map[string]any)
		if before["priority"] != float64(0) || after["priority"] != float64(5) || after["version"] != float64(2) {
			t.Errorf("layout updated: before %+v after %+v", before, after)
		}
	}
	if entries := byResource[forCustom.ID]; len(entries) != 2 || entries[1]["after"] != nil || entries[1]["before"] == nil {
		t.Errorf("audit of the deleted layout: %+v", entries)
	}
	if result, verr := audit.Verify(bg, f.App, f.OrgA); verr != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, verr)
	}
}

// TestFormLayoutsWithoutDatabase covers WP-259: in memory mode the layout
// routes answer 503 like the attribute definitions.
func TestFormLayoutsWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "c0de0000-0000-4000-8000-000000000001"})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/layouts", ""},
		{http.MethodPost, "/api/v1/layouts", `{"key":"x"}`},
		{http.MethodGet, "/api/v1/layouts/c0de0000-0000-4000-8000-000000000002", ""},
		{http.MethodPatch, "/api/v1/layouts/c0de0000-0000-4000-8000-000000000002", `{}`},
		{http.MethodDelete, "/api/v1/layouts/c0de0000-0000-4000-8000-000000000002", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s in memory mode: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
