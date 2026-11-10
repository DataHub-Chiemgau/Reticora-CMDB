package citype_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// formCaller is the caller of a form request.
type formCaller struct {
	user   string
	manage bool     // holds citype:manage
	apiKey bool     // an API key instead of a user session
	client string   // restricted to this client (empty: org-wide)
	groups []string // IdP groups of the session (WP-725)
}

// formCall sends GET path as caller c of organization org.
func (a *setAPI) formCall(org string, c formCaller, path string) *httptest.ResponseRecorder {
	a.t.Helper()
	scope := database.OrgWideScope(org, c.user)
	if c.client != "" {
		scope.Clients = database.ScopeIDs(c.client)
	}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: c.user})
	p := identity.Principal{Subject: c.user, OrganizationID: org, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermCIRead}, Groups: c.groups}
	if c.apiKey {
		p.Type = identity.PrincipalTypeAPIKey
	}
	if c.manage {
		p.Permissions = append(p.Permissions, identity.PermCITypeManage)
	}
	ctx = audit.WithRequestInfo(identity.WithPrincipal(ctx, p), audit.RequestInfo{RequestID: "wp719", Source: audit.SourceUI})
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx)
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// form fetches a resolved form and fails unless the status is 200.
func (a *setAPI) form(org string, c formCaller, path string) citype.ResolvedForm {
	a.t.Helper()
	w := a.formCall(org, c, path)
	if w.Code != http.StatusOK {
		a.t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var out citype.ResolvedForm
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		a.t.Fatalf("GET %s: decode: %v", path, err)
	}
	return out
}

func resolvedField(form *citype.ResolvedForm, name string) (*citype.FormField, string) {
	for i := range form.Sections {
		for j := range form.Sections[i].Fields {
			if form.Sections[i].Fields[j].Name == name {
				return &form.Sections[i].Fields[j], form.Sections[i].Key
			}
		}
	}
	return nil, ""
}

// TestResolvedFormAgainstPostgres covers MET-50, MET-52, GQL-03 and API-06
// (WP-719): GET /cis/{id}/form for a user with two roles (smallest
// priority), a user with a custom role, a user without a role (the layout
// without a role) and an API key; the layout of the parent type
// (inheritance); a CI with two templates (the highest position); an
// organization global required attribute in _required; an instance
// attribute in _instance; an invisible CI of another client is 404. GET
// /ci-types/{id}/form?template_id= carries the required_override and the
// catalog rules of the current template version and evaluates against
// the defaults. A preview needs citype:manage (403, otherwise 200) and
// writes no audit_log or ci_change row; an own role_id needs no further
// right, another role does (403). Unknown or foreign template_id or
// role_id is 404, a template outside the type chain 422, template_id on a
// CI without preview 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestResolvedFormAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	org := f.OrgA

	parent := a.createType(org, "wp719-parent")
	leaf := a.createChildType(org, "wp719-leaf", parent.ID)
	sibling := a.createChildType(org, "wp719-sibling", parent.ID)
	other := a.createType(org, "wp719-other")
	a.definition(org, `{"ci_type_id":"`+parent.ID+`","key":"wp719_rack","data_type":"text","inheritable":true,"order":1}`)
	a.definition(org, `{"ci_type_id":"`+leaf.ID+`","key":"wp719_mode","data_type":"enum","enum_values":["on","off"],"default":"on","order":2}`)
	a.definition(org, `{"ci_type_id":"`+leaf.ID+`","key":"wp719_serial","data_type":"text","order":3}`)
	note := a.definition(org, `{"ci_type_id":"`+leaf.ID+`","key":"wp719_note","data_type":"text","order":4}`)
	a.definition(org, `{"ci_type_id":"`+other.ID+`","key":"wp719_misc","data_type":"text"}`)
	a.definition(org, `{"key":"wp719_owner","data_type":"text","required":true,"applies_to":"types","applies_to_type_ids":["`+parent.ID+`"]}`)
	a.createRule(org, `{"key":"wp719-show-serial","attribute_key":"wp719_serial","kind":"visible","ci_type_id":"`+leaf.ID+`",
		"condition":{"op":"eq","field":"wp719_mode","value":"on"}}`)

	// Templates of the leaf type, each with a published version 1; the
	// rule and the required_override sit on version 1 of tpl1.
	publish := func(key, typeID, items string) citype.Template {
		tpl := a.createTemplate(org, typeID, key)
		a.must(http.StatusOK, org, http.MethodPut, versionPath(tpl.ID, 1), `{"items":[`+items+`]}`, nil)
		return tpl
	}
	tpl1 := publish("wp719-tpl1", leaf.ID, `{"attribute_definition_id":"`+note.ID+`","position":0,"required_override":true}`)
	v1 := a.templateVersion(org, tpl1.ID, 1)
	a.createRule(org, `{"key":"wp719-tpl-readonly","attribute_key":"wp719_note","kind":"readonly","template_version_id":"`+v1.ID+`"}`)
	a.must(http.StatusOK, org, http.MethodPost, versionPath(tpl1.ID, 1)+"/publish", "", nil)
	tpl2 := publish("wp719-tpl2", leaf.ID, "")
	a.must(http.StatusOK, org, http.MethodPost, versionPath(tpl2.ID, 1)+"/publish", "", nil)
	tplOther := publish("wp719-tpl-other", other.ID, "")
	a.must(http.StatusOK, org, http.MethodPost, versionPath(tplOther.ID, 1)+"/publish", "", nil)
	foreignTypeB := a.createType(f.OrgB, "wp719-foreign")
	tplForeign := a.createTemplate(f.OrgB, foreignTypeB.ID, "wp719-foreign-tpl")

	// Roles and users: two roles, a custom role, an unassigned role, a
	// role of organization B.
	roleA, roleB, roleX, roleForeign := f.ID(), f.ID(), f.ID(), f.ID()
	for _, r := range []struct{ id, org, name string }{
		{roleA, org, "wp719-a"}, {roleB, org, "wp719-b"}, {roleX, org, "wp719-x"}, {roleForeign, f.OrgB, "wp719-foreign"},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, '["ci:read"]')`,
			r.id, r.org, r.name); err != nil {
			t.Fatal(err)
		}
	}
	var customC string
	if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
		VALUES ($1, 'wp719-custom', '["ci:read"]') RETURNING id::text`, org).Scan(&customC); err != nil {
		t.Fatal(err)
	}
	userTwo, userCustom, userNone := f.AppUser(t, org, "wp719-two"), f.AppUser(t, org, "wp719-custom"), f.AppUser(t, org, "wp719-none")
	for _, role := range []string{roleA, roleB} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
			VALUES ($1, $2, $3, $4)`, org, userTwo, role, f.Client1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ($1, $2)`, userCustom, customC); err != nil {
		t.Fatal(err)
	}

	// The builtin role engineer, held only through an IdP group (WP-725).
	var engineer string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND is_builtin AND scope = 'org' AND name = 'engineer'`,
		org).Scan(&engineer); err != nil {
		t.Fatal(err)
	}

	// Layouts of the leaf type per role, of the parent type, of both
	// templates.
	onLeaf := `"ci_type_id":"` + leaf.ID + `"`
	for _, l := range []struct{ key, target, role, priority string }{
		{"wp719-l-none", onLeaf, "", "0"}, {"wp719-l-a", onLeaf, roleA, "5"}, {"wp719-l-b", onLeaf, roleB, "1"},
		{"wp719-l-c", onLeaf, customC, "0"}, {"wp719-l-x", onLeaf, roleX, "0"}, {"wp725-l-eng", onLeaf, engineer, "0"},
		{"wp719-p", `"ci_type_id":"` + parent.ID + `"`, "", "0"},
		{"wp719-t1", `"template_id":"` + tpl1.ID + `"`, "", "0"}, {"wp719-t2", `"template_id":"` + tpl2.ID + `"`, "", "0"},
	} {
		body := `{"key":"` + l.key + `",` + l.target + `,"priority":` + l.priority
		if l.role != "" {
			body += `,"role_id":"` + l.role + `"`
		}
		a.createLayout(org, body+`,"layout":{"sections":[`+section("main", `{"name":"wp719_rack","width":6}`)+`]}}`)
	}
	layoutID := map[string]string{}
	var all layoutPage
	a.must(http.StatusOK, org, http.MethodGet, "/api/v1/layouts?limit=200", "", &all)
	for _, l := range all.Data {
		layoutID[l.ID] = l.Key
	}

	// CIs: one of the leaf type with an instance attribute, one with two
	// templates, one of the sibling type, one of another client.
	newCI := func(typeID, client, name, attrs string) string {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
			VALUES ($1, $2, $3, $4, 'active', $5) RETURNING id::text`, org, client, typeID, name, attrs).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ciLeaf := newCI(leaf.ID, f.Client1, "wp719-leaf-ci", `{"wp719_mode":"off"}`)
	a.must(http.StatusOK, org, http.MethodPut, "/api/v1/cis/"+ciLeaf+"/field-definitions", `{"name":"wp719_local","data_type":"text"}`, nil)
	ciTemplates := newCI(leaf.ID, f.Client1, "wp719-tpl-ci", `{}`)
	for pos, tpl := range []string{tpl1.ID, tpl2.ID} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_template (organization_id, client_id, ci_id, template_id, version, position)
			VALUES ($1, $2, $3, $4, 1, $5)`, org, f.Client1, ciTemplates, tpl, pos); err != nil {
			t.Fatal(err)
		}
	}
	ciSibling := newCI(sibling.ID, f.Client1, "wp719-sibling-ci", `{}`)
	ciHidden := newCI(leaf.ID, f.Client2, "wp719-hidden-ci", `{}`)

	two, custom, none := formCaller{user: userTwo}, formCaller{user: userCustom}, formCaller{user: userNone}
	manager := formCaller{user: userNone, manage: true}
	key := formCaller{user: userTwo, apiKey: true}
	ciForm := func(id string) string { return "/api/v1/cis/" + id + "/form" }

	// Layout choice per caller.
	for _, c := range []struct {
		name   string
		caller formCaller
		path   string
		want   string
	}{
		{"two roles: the smaller priority", two, ciForm(ciLeaf), "wp719-l-b"},
		{"custom role", custom, ciForm(ciLeaf), "wp719-l-c"},
		{"no role", none, ciForm(ciLeaf), "wp719-l-none"},
		{"engineer through an IdP group", formCaller{user: userNone, groups: []string{"/engineer"}}, ciForm(ciLeaf), "wp725-l-eng"},
		{"unmapped IdP group", formCaller{user: userNone, groups: []string{"wp725-unknown"}}, ciForm(ciLeaf), "wp719-l-none"},
		{"api key", key, ciForm(ciLeaf), "wp719-l-none"},
		{"own role_id", two, ciForm(ciLeaf) + "?role_id=" + roleA, "wp719-l-a"},
		{"parent type", none, ciForm(ciSibling), "wp719-p"},
		{"two templates: the highest position", none, ciForm(ciTemplates), "wp719-t2"},
		{"another role with citype:manage", manager, ciForm(ciLeaf) + "?role_id=" + roleX, "wp719-l-x"},
		{"preview of a role and a template", manager, ciForm(ciLeaf) + "?preview=true&role_id=" + roleA + "&template_id=" + tpl1.ID, "wp719-t1"},
	} {
		form := a.form(org, c.caller, c.path)
		if got := layoutID[form.LayoutID]; got != c.want || form.Source != citype.FormSourceLayout {
			t.Errorf("%s: layout %q (%s), want %q", c.name, got, form.Source, c.want)
		}
	}

	// Fields: the org global required attribute in _required, the instance
	// attribute in _instance, the visible rule against the stored values.
	form := a.form(org, two, ciForm(ciLeaf))
	if form.RoleID != roleB || form.CITypeID != leaf.ID || form.Sections[0].Key != "main" {
		t.Errorf("form: %+v", form)
	}
	if f, section := resolvedField(&form, "wp719_owner"); f == nil || section != citype.FormSectionRequired || !f.State.Required ||
		!f.Definition.Required {
		t.Errorf("org global required attribute: %+v in %q", f, section)
	}
	if f, section := resolvedField(&form, "wp719_local"); f == nil || section != citype.FormSectionInstance {
		t.Errorf("instance attribute: %+v in %q", f, section)
	}
	if f, _ := resolvedField(&form, "wp719_rack"); f == nil || f.Width != 6 {
		t.Errorf("layout field: %+v", f)
	}
	standard := a.form(org, none, "/api/v1/ci-types/"+other.ID+"/form")
	if standard.Source != citype.FormSourceDefault || standard.LayoutID != "" {
		t.Errorf("type without layouts: %+v", standard)
	}
	if f, _ := resolvedField(&standard, "wp719_misc"); f == nil || f.Width != 12 {
		t.Errorf("standard layout: %+v", standard.Sections)
	}

	// The visible rule: hidden against the stored value off of the CI,
	// visible against the default on of the type form.
	// Without the layouts of no role the standard layout shows every field.
	for _, k := range []string{"wp719-l-none", "wp719-p"} {
		a.must(http.StatusNoContent, org, http.MethodDelete, "/api/v1/layouts/"+keyOf(layoutID, k), "", nil)
	}
	byValues := a.form(org, none, ciForm(ciLeaf))
	if f, _ := resolvedField(&byValues, "wp719_serial"); f == nil || f.State.Visible {
		t.Errorf("serial against mode off: %+v", f)
	}
	byDefaults := a.form(org, none, "/api/v1/ci-types/"+leaf.ID+"/form")
	if f, _ := resolvedField(&byDefaults, "wp719_serial"); f == nil || !f.State.Visible {
		t.Errorf("serial against the default on: %+v", f)
	}

	// The type form with a template: required_override and the rules of the
	// current version.
	withTpl := a.form(org, none, "/api/v1/ci-types/"+leaf.ID+"/form?template_id="+tpl1.ID)
	if layoutID[withTpl.LayoutID] != "wp719-t1" || withTpl.TemplateID != tpl1.ID {
		t.Errorf("template layout: %+v", withTpl)
	}
	if f, _ := resolvedField(&withTpl, "wp719_note"); f == nil || !f.Definition.Required || !f.State.Required || !f.State.ReadOnly {
		t.Errorf("note with the template: %+v", f)
	}
	if !slices.ContainsFunc(withTpl.Rules, func(r citype.FormRule) bool { return r.Key == "wp719-tpl-readonly" }) ||
		!slices.ContainsFunc(withTpl.Rules, func(r citype.FormRule) bool { return r.Key == "wp719-show-serial" && r.Condition != nil }) {
		t.Errorf("rules with the template: %+v", withTpl.Rules)
	}
	if f, _ := resolvedField(&byDefaults, "wp719_note"); f != nil && (f.Definition.Required || f.State.ReadOnly) {
		t.Errorf("note without the template: %+v", f)
	}

	// A preview writes nothing.
	count := func() (n int) {
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM audit_log WHERE organization_id = $1)
			+ (SELECT count(*) FROM ci_change WHERE organization_id = $1)`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	a.form(org, manager, ciForm(ciLeaf)+"?preview=true&role_id="+roleX)
	a.form(org, manager, "/api/v1/ci-types/"+leaf.ID+"/form?preview=true&template_id="+tpl2.ID)
	if after := count(); after != before {
		t.Errorf("preview wrote %d rows", after-before)
	}

	// Refusals.
	for _, c := range []struct {
		name   string
		caller formCaller
		path   string
		status int
	}{
		{"preview without citype:manage", two, ciForm(ciLeaf) + "?preview=true&role_id=" + roleA, http.StatusForbidden},
		{"another role without citype:manage", two, ciForm(ciLeaf) + "?role_id=" + roleX, http.StatusForbidden},
		{"invisible ci", formCaller{user: userNone, client: f.Client1}, ciForm(ciHidden), http.StatusNotFound},
		{"visible in the client scope", formCaller{user: userNone, client: f.Client1}, ciForm(ciLeaf), http.StatusOK},
		{"unknown ci", none, ciForm(f.ID()), http.StatusNotFound},
		{"unknown type", none, "/api/v1/ci-types/" + f.ID() + "/form", http.StatusNotFound},
		{"type of organization B", none, "/api/v1/ci-types/" + foreignTypeB.ID + "/form", http.StatusNotFound},
		{"unknown template", manager, ciForm(ciLeaf) + "?preview=true&template_id=" + f.ID(), http.StatusNotFound},
		{"template of organization B", none, "/api/v1/ci-types/" + leaf.ID + "/form?template_id=" + tplForeign.ID, http.StatusNotFound},
		{"unknown role", manager, ciForm(ciLeaf) + "?preview=true&role_id=" + f.ID(), http.StatusNotFound},
		{"role of organization B", manager, ciForm(ciLeaf) + "?preview=true&role_id=" + roleForeign, http.StatusNotFound},
		{"template outside the type chain", manager, ciForm(ciLeaf) + "?preview=true&template_id=" + tplOther.ID, http.StatusUnprocessableEntity},
		{"template outside the chain of the type", none, "/api/v1/ci-types/" + leaf.ID + "/form?template_id=" + tplOther.ID,
			http.StatusUnprocessableEntity},
		{"template on a ci without preview", manager, ciForm(ciLeaf) + "?template_id=" + tpl1.ID, http.StatusBadRequest},
		{"preview not a boolean", manager, ciForm(ciLeaf) + "?preview=maybe", http.StatusBadRequest},
	} {
		w := a.formCall(org, c.caller, c.path)
		if w.Code != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.status)
		}
		if c.status == http.StatusUnprocessableEntity || c.status == http.StatusBadRequest {
			if p := problemOf(w); c.name != "preview not a boolean" && p.Field != "template_id" {
				t.Errorf("%s: field %q", c.name, p.Field)
			}
		}
	}
}

// keyOf returns the layout id of key.
func keyOf(ids map[string]string, key string) string {
	for id, k := range ids {
		if k == key {
			return id
		}
	}
	return ""
}

// TestResolvedFormWithoutDatabase covers the 503 of the form routes
// without a database (WP-719, like the layouts).
func TestResolvedFormWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewMemoryRepository()).RegisterRoutes(mux)
	const id = "c0de0000-0000-4000-8000-000000000002"
	for _, path := range []string{"/api/v1/cis/" + id + "/form", "/api/v1/ci-types/" + id + "/form"} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(
			tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "c0de0000-0000-4000-8000-000000000001"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s: %d %s, want 503", path, w.Code, w.Body.String())
		}
	}
}

// TestResolvedFormFieldRightsAgainstPostgres covers RBA-07 in the resolved
// form (WP-727): with the factory of the server (server.FormPolicyFactory)
// and the policy source of the router, a field the caller may not read is
// left out of the layout (also an instance attribute), a field it may only
// read is read only, both on GET /cis/{id}/form and GET
// /ci-types/{id}/form; a preview as role X follows the policy of X; a
// caller whose roles restrict no field gets every field editable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestResolvedFormFieldRightsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "a8")
	a := newSetAPI(t, f)
	bg := context.Background()
	org := f.OrgA
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	leaf := a.createType(org, "wp727-leaf")
	for i, key := range []string{"wp727_open", "wp727_hidden", "wp727_ro"} {
		a.definition(org, `{"ci_type_id":"`+leaf.ID+`","key":"`+key+`","data_type":"text","order":`+strconv.Itoa(i+1)+`}`)
	}
	var ciID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
		VALUES ($1, $2, $3, 'wp727-form-ci', 'active', '{}') RETURNING id::text`, org, f.Client1, leaf.ID).Scan(&ciID); err != nil {
		t.Fatal(err)
	}
	a.must(http.StatusOK, org, http.MethodPut, "/api/v1/cis/"+ciID+"/field-definitions", `{"name":"wp727_inst","data_type":"text"}`, nil)

	// Role R of the user: wp727_hidden and the instance attribute none,
	// wp727_ro read. Role X (preview only): wp727_open none.
	roleR, roleX := f.ID(), f.ID()
	for _, r := range []struct{ id, name string }{{roleR, "wp727-r"}, {roleX, "wp727-x"}} {
		exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, '["ci:read", "ci:write"]')`, r.id, org, r.name)
	}
	userR, manager := f.AppUser(t, org, "wp727-form-r"), f.AppUser(t, org, "wp727-form-manager")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, org, userR, roleR)
	for _, g := range []struct{ role, key, level string }{
		{roleR, "attributes.wp727_hidden", "none"}, {roleR, "_instance.wp727_inst", "none"}, {roleR, "attributes.wp727_ro", "read"},
		{roleX, "attributes.wp727_open", "none"},
	} {
		exec(`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', $3, $4)`,
			org, g.role, g.key, g.level)
	}

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	resolver := permission.NewFieldResolver(repos.Permission, nil, repos.FieldGrants, nil)
	mux := chi.NewRouter()
	mux.Use(permission.FieldPolicyMiddleware(resolver))
	citype.NewHandler(a.repo).WithFormPolicy(server.FormPolicyFactory(&repos, resolver)).RegisterRoutes(mux)
	get := func(user string, perms []identity.Permission, path string) citype.ResolvedForm {
		t.Helper()
		scope := database.OrgWideScope(org, user)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: org, UserID: user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser,
			Permissions: perms})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var out citype.ResolvedForm
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// state checks the fields of a form: absent ones and read-only ones.
	state := func(what string, form *citype.ResolvedForm, absent []string, readOnly map[string]bool) {
		t.Helper()
		for _, name := range absent {
			if field, _ := resolvedField(form, name); field != nil {
				t.Errorf("%s: unreadable field %s in the layout", what, name)
			}
		}
		for name, ro := range readOnly {
			field, _ := resolvedField(form, name)
			if field == nil {
				t.Errorf("%s: readable field %s missing", what, name)
				continue
			}
			if field.State.ReadOnly != ro {
				t.Errorf("%s: field %s read_only %v, want %v", what, name, field.State.ReadOnly, ro)
			}
		}
	}
	write := []identity.Permission{identity.PermCIRead, identity.PermCIWrite}
	ciForm := get(userR, write, "/api/v1/cis/"+ciID+"/form")
	state("CI form of role R", &ciForm, []string{"wp727_hidden", "wp727_inst"}, map[string]bool{"wp727_open": false, "wp727_ro": true})
	typeForm := get(userR, write, "/api/v1/ci-types/"+leaf.ID+"/form")
	state("type form of role R", &typeForm, []string{"wp727_hidden"}, map[string]bool{"wp727_open": false, "wp727_ro": true})

	managePerms := []identity.Permission{identity.PermCIRead, identity.PermCIWrite, identity.PermCITypeManage}
	preview := get(manager, managePerms, "/api/v1/cis/"+ciID+"/form?preview=true&role_id="+roleX)
	state("preview as role X", &preview, []string{"wp727_open"},
		map[string]bool{"wp727_hidden": false, "wp727_ro": false, "wp727_inst": false})
	other := get(manager, managePerms, "/api/v1/cis/"+ciID+"/form?role_id="+roleR)
	state("another role R as a preview", &other, []string{"wp727_hidden", "wp727_inst"}, map[string]bool{"wp727_ro": true})
	own := get(manager, managePerms, "/api/v1/cis/"+ciID+"/form")
	state("caller without restricting roles", &own, nil,
		map[string]bool{"wp727_open": false, "wp727_hidden": false, "wp727_ro": false, "wp727_inst": false})

	// SR-59 (WP-872): a catalog rule, an attribute condition and the
	// visible_when of a section over wp727_hidden, which role R cannot
	// read. Two CIs that differ only in wp727_hidden give R the same form,
	// with the conservative states; the caller without restricting roles
	// sees the conditions act.
	a.createRule(org, `{"key":"wp872-req","attribute_key":"wp727_open","kind":"required","ci_type_id":"`+leaf.ID+`",
		"condition":{"op":"eq","field":"wp727_hidden","value":"on"}}`)
	a.createRule(org, `{"key":"wp872-sec","attribute_key":"wp727_ro","kind":"readonly","ci_type_id":"`+leaf.ID+`",
		"condition":{"op":"eq","field":"wp727_hidden","value":"on"}}`)
	a.definition(org, `{"ci_type_id":"`+leaf.ID+`","key":"wp872_cond","data_type":"text","order":9,
		"conditional":{"visible_when":[{"field":"wp727_hidden","op":"eq","value":"off"}]}}`)
	a.createLayout(org, `{"key":"wp872-layout","ci_type_id":"`+leaf.ID+`","layout":{"sections":[
		{"key":"main","title":{"de":"Haupt","en":"Main"},"fields":[{"name":"wp727_open"},{"name":"wp727_ro"}]},
		{"key":"cond","title":{"de":"Bedingt","en":"Conditional"},"visible_when":["wp872-sec"],"fields":[{"name":"wp872_cond"}]}]}}`)
	ciWith := func(name, hidden string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
			VALUES ($1, $2, $3, $4, 'active', jsonb_build_object('wp727_hidden', $5::text)) RETURNING id::text`,
			org, f.Client1, leaf.ID, name, hidden).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	onCI, offCI := ciWith("wp872-on", "on"), ciWith("wp872-off", "off")
	formOn := get(userR, write, "/api/v1/cis/"+onCI+"/form")
	formOff := get(userR, write, "/api/v1/cis/"+offCI+"/form")
	if !reflect.DeepEqual(formOn, formOff) {
		t.Errorf("SR-59: the value of the unreadable wp727_hidden changes the form of role R:\n%+v\n%+v", formOn, formOff)
	}
	for _, form := range []*citype.ResolvedForm{&formOn, &formOff} {
		if open, _ := resolvedField(form, "wp727_open"); open == nil || open.State.Required {
			t.Errorf("SR-59: wp727_open must not be required by an unknown rule: %+v", open)
		}
		if cond, _ := resolvedField(form, "wp872_cond"); cond == nil || !cond.State.Visible {
			t.Errorf("SR-59: wp872_cond must stay visible under an unknown condition: %+v", cond)
		}
		for _, sec := range form.Sections {
			if sec.Key == "cond" && !sec.Visible {
				t.Errorf("SR-59: section cond hidden by an unknown visible_when")
			}
		}
	}
	// The caller without restricting roles: the conditions act.
	fullOn := get(manager, managePerms, "/api/v1/cis/"+onCI+"/form")
	fullOff := get(manager, managePerms, "/api/v1/cis/"+offCI+"/form")
	if open, _ := resolvedField(&fullOn, "wp727_open"); open == nil || !open.State.Required {
		t.Errorf("readable wp727_hidden=on: wp727_open must be required: %+v", open)
	}
	if cond, _ := resolvedField(&fullOn, "wp872_cond"); cond != nil && cond.State.Visible {
		t.Errorf("readable wp727_hidden=on: wp872_cond must be hidden: %+v", cond)
	}
	if reflect.DeepEqual(fullOn, fullOff) {
		t.Errorf("readable wp727_hidden: both values give the same form")
	}
}
