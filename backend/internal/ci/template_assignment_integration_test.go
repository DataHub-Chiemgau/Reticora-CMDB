package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestTemplateAssignmentOnPostgreSQL covers MET-16, API-06 and AUD-07
// (WP-710) against PostgreSQL:
//   - ABN-01 (c): POST /cis with two templates of the organization is
//     validated against the composed schema: the attribute template 2
//     makes required is 422 without a value, 201 with it, and the CI
//     carries the templates in position order with their published
//     versions; the default of the later template wins (ResolveFields);
//   - GET /cis/{id} and GET /cis/{id}/templates show the assignment;
//   - PUT /cis/{id}/templates with an outdated If-Match is 412; with the
//     current one it replaces the list, raises the version (new ETag) and
//     writes the audit ci.templates_changed, a ci_change row of the field
//     templates in a change set of its own and the event ci.changed with
//     the field templates; the attributes of the CI are validated against
//     the new composition (422);
//   - a draft version, a deactivated template, a template of another type,
//     a template of another organization and a template listed twice are
//     422 on templates[i]; a system template of the type is assignable;
//   - a template a CI uses cannot be deleted (23503, 422);
//   - the type change drops the assignments whose template does not fit
//     the target type and names them in dropped_templates;
//   - client_id and site_id follow the CI, also when it changes client,
//     and a principal of client 2 neither sees nor changes the assignment
//     of a CI of client 1.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTemplateAssignmentOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "71")
	bg := context.Background()
	principal := func(base context.Context, org string) context.Context {
		ctx := tenant.WithTenant(base, tenant.TenantInfo{OrganizationID: org, UserID: f.User})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: org,
			Type: identity.PrincipalTypeUser, Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite}})
	}
	orgA, orgB := principal(f.OrgCtx(f.OrgA), f.OrgA), principal(f.OrgCtx(f.OrgB), f.OrgB)
	client2 := principal(f.ClientCtx(f.Client2), f.OrgA)

	types := citype.NewPGRepository(f.App)
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	svc := ci.NewService(repo).WithFieldResolver(types)
	mux := chi.NewRouter()
	// The field policy source of the server router (WP-265).
	mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	want := func(step string, w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("%s: %d %s, want %d", step, w.Code, w.Body.String(), code)
		}
	}
	violation := func(step string, w *httptest.ResponseRecorder, field string) {
		t.Helper()
		want(step, w, http.StatusUnprocessableEntity)
		var body struct {
			Violations []ci.Violation `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Violations) == 0 || body.Violations[0].Field != field {
			t.Fatalf("%s: %s, want the violation of %s", step, w.Body.String(), field)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	typeID := func(key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	server, application := typeID("server"), typeID("application")

	definition := func(ctx context.Context, org, key string) citype.AttributeDefinition {
		t.Helper()
		d := citype.AttributeDefinition{Key: key, DataType: "text"}
		if err := types.CreateAttributeDefinition(ctx, org, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		return d
	}
	required := true
	// template creates a template of typ with the items of version 1 and
	// publishes it unless draft.
	template := func(ctx context.Context, org, key, typ string, draft bool, items ...citype.TemplateVersionItem) citype.Template {
		t.Helper()
		tpl := citype.Template{Key: key, CITypeID: typ, IsActive: true}
		if err := tpl.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := types.CreateTemplate(ctx, org, &tpl); err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		for i := range items {
			items[i].Position = i
		}
		if _, err := types.SetTemplateVersionItems(ctx, org, tpl.ID, 1, items); err != nil {
			t.Fatalf("template %s items: %v", key, err)
		}
		if !draft {
			if _, err := types.PublishTemplateVersion(ctx, org, tpl.ID, 1); err != nil {
				t.Fatalf("publish %s: %v", key, err)
			}
		}
		return tpl
	}
	owner := definition(orgA, f.OrgA, "w710_owner")
	rack := definition(orgA, f.OrgA, "w710_rack")
	note := definition(orgA, f.OrgA, "w710_note")
	first := template(orgA, f.OrgA, "w710-first", server, false,
		citype.TemplateVersionItem{AttributeDefinitionID: owner.ID, RequiredOverride: &required},
		citype.TemplateVersionItem{AttributeDefinitionID: note.ID, DefaultOverride: json.RawMessage(`"from-first"`)})
	second := template(orgA, f.OrgA, "w710-second", server, false,
		citype.TemplateVersionItem{AttributeDefinitionID: rack.ID, RequiredOverride: &required},
		citype.TemplateVersionItem{AttributeDefinitionID: note.ID, DefaultOverride: json.RawMessage(`"from-second"`)})
	draft := template(orgA, f.OrgA, "w710-draft", server, true)
	inactive := template(orgA, f.OrgA, "w710-inactive", server, false)
	off := false
	if _, err := types.UpdateTemplate(orgA, f.OrgA, inactive.ID, &citype.TemplatePatch{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	otherType := template(orgA, f.OrgA, "w710-application", application, false)
	foreign := template(orgB, f.OrgB, "w710-foreign", server, false)
	var system string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM template WHERE key = 'physical_server' AND organization_id IS NULL`).Scan(&system); err != nil {
		t.Fatal(err)
	}
	refs := func(ids ...string) string {
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{"template_id": id})
		}
		raw, _ := json.Marshal(out)
		return string(raw)
	}
	decode := func(w *httptest.ResponseRecorder) ci.Item {
		t.Helper()
		var item ci.Item
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	checkList := func(step string, got []citype.CITemplate, want ...citype.Template) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: templates %+v, want %d", step, got, len(want))
		}
		for i := range want {
			if got[i].TemplateID != want[i].ID || got[i].Key != want[i].Key || got[i].Version != 1 || got[i].Position != i {
				t.Fatalf("%s: templates %+v, want %s at %d with version 1", step, got, want[i].Key, i)
			}
		}
	}

	// ABN-01 (c): a normal user creates a CI with two templates.
	create := func(attrs string) *httptest.ResponseRecorder {
		return call(orgA, http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+server+`","name":"w710-ci","client_id":"`+f.Client1+
			`","attributes":`+attrs+`,"templates":`+refs(first.ID, second.ID)+`}`)
	}
	violation("create without the attribute template 2 requires", create(`{"w710_owner":"ops"}`), "w710_rack")
	if n := count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND name = 'w710-ci'`, f.OrgA); n != 0 {
		t.Fatalf("a refused creation left %d CIs", n)
	}
	w := create(`{"w710_owner":"ops","w710_rack":"R1"}`)
	want("create with two templates", w, http.StatusCreated)
	device := decode(w)
	checkList("create", device.Templates, first, second)
	defs, err := types.ResolveFields(orgA, f.OrgA, server, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fieldmeta.FieldDefinition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if byName["w710_note"].DefaultValue != "from-second" || !byName["w710_owner"].Required || !byName["w710_rack"].Required {
		t.Fatalf("composed definitions: note %+v, owner %+v, rack %+v", byName["w710_note"], byName["w710_owner"], byName["w710_rack"])
	}
	if n := count(`SELECT count(*) FROM ci_template WHERE ci_id = $1 AND client_id = $2 AND site_id IS NULL`, device.ID, f.Client1); n != 2 {
		t.Fatalf("%d assignment rows with the client of the CI, want 2", n)
	}
	path := "/api/v1/cis/" + device.ID + "/templates"
	w = call(orgA, http.MethodGet, path, "")
	want("GET templates", w, http.StatusOK)
	var list struct {
		Data       []citype.CITemplate `json:"data"`
		NextCursor *string             `json:"next_cursor"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.NextCursor != nil {
		t.Fatalf("GET templates: %s", w.Body.String())
	}
	checkList("GET templates", list.Data, first, second)
	w = call(orgA, http.MethodGet, "/api/v1/cis/"+device.ID, "")
	want("GET /cis/{id}", w, http.StatusOK)
	checkList("GET /cis/{id}", decode(w).Templates, first, second)
	if n := count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.templates_changed'`, device.ID); n != 1 {
		t.Fatalf("%d audit entries ci.templates_changed after the creation, want 1", n)
	}

	// PUT with If-Match: an outdated version is 412.
	etag := w.Header().Get("ETag")
	want("PATCH", call(orgA, http.MethodPatch, "/api/v1/cis/"+device.ID, `{"name":"w710-ci-renamed"}`, "If-Match", etag), http.StatusOK)
	w = call(orgA, http.MethodPut, path, `{"templates":`+refs(second.ID)+`}`, "If-Match", etag)
	want("PUT with an outdated If-Match", w, http.StatusPreconditionFailed)
	w = call(orgA, http.MethodGet, "/api/v1/cis/"+device.ID, "")
	current := w.Header().Get("ETag")
	version := decode(w).Version
	type marks struct{ audits, changes, events, sets int }
	mark := func() marks {
		t.Helper()
		return marks{
			audits: count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.templates_changed'`, device.ID),
			changes: count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'templates' AND change_type = 'update'
				AND change_kind = 'updated'`, device.ID),
			events: count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
				AND data->'changed_fields' = '["templates"]'::jsonb`, device.ID),
			sets: count(`SELECT count(DISTINCT change_set_id) FROM ci_change WHERE ci_id = $1 AND field_name = 'templates'`, device.ID),
		}
	}
	before := mark()
	w = call(orgA, http.MethodPut, path, `{"templates":[{"template_id":"`+second.ID+`","version":1}]}`, "If-Match", current)
	want("PUT", w, http.StatusOK)
	replaced := decode(w)
	checkList("PUT", replaced.Templates, second)
	if replaced.Version != version+1 || w.Header().Get("ETag") != api.ETag(version+1) {
		t.Fatalf("PUT: version %d, ETag %s, want %d", replaced.Version, w.Header().Get("ETag"), version+1)
	}
	if after := mark(); after.audits != before.audits+1 || after.changes != before.changes+1 || after.events != before.events+1 ||
		after.sets != before.sets+1 {
		t.Fatalf("PUT recorded %+v after %+v, want one more audit entry, ci_change row, event and change set", after, before)
	}
	var audited struct {
		Before []citype.CITemplate `json:"before"`
		After  []citype.CITemplate `json:"after"`
	}
	var raw []byte
	if err = f.Admin.QueryRow(bg, `SELECT changes FROM audit_log WHERE resource_id = $1 AND action = 'ci.templates_changed'
		ORDER BY created_at DESC, id DESC LIMIT 1`, device.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &audited); err != nil || len(audited.Before) != 2 || len(audited.After) != 1 ||
		audited.After[0].TemplateID != second.ID {
		t.Fatalf("audit ci.templates_changed: %s", raw)
	}
	// The same list again changes nothing.
	unchanged := mark()
	w = call(orgA, http.MethodPut, path, `{"templates":`+refs(second.ID)+`}`)
	want("PUT of the same list", w, http.StatusOK)
	if decode(w).Version != version+1 || mark() != unchanged {
		t.Fatalf("PUT of the same list: %s, marks %+v, want %+v", w.Body.String(), mark(), unchanged)
	}

	// The attributes are validated against the new composition.
	bare := decode(call(orgA, http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+server+`","name":"w710-bare","client_id":"`+f.Client1+`"}`))
	violation("PUT of a template whose required attribute the CI lacks",
		call(orgA, http.MethodPut, "/api/v1/cis/"+bare.ID+"/templates", `{"templates":`+refs(first.ID)+`}`), "w710_owner")

	// Assignment rules.
	for _, c := range []struct {
		step, body, field string
	}{
		{"a draft version", `{"templates":` + refs(draft.ID) + `}`, "templates[0]"},
		{"a deactivated template", `{"templates":` + refs(inactive.ID) + `}`, "templates[0]"},
		{"a template of another type", `{"templates":` + refs(otherType.ID) + `}`, "templates[0]"},
		{"a template of another organization", `{"templates":` + refs(foreign.ID) + `}`, "templates[0]"},
		{"a template listed twice", `{"templates":` + refs(system, system) + `}`, "templates[1]"},
		{"a version that does not exist", `{"templates":[{"template_id":"` + system + `","version":7}]}`, "templates[0]"},
	} {
		violation(c.step, call(orgA, http.MethodPut, "/api/v1/cis/"+bare.ID+"/templates", c.body), c.field)
	}
	violation("POST /cis with a template of another type", call(orgA, http.MethodPost, "/api/v1/cis",
		`{"ci_type_id":"`+server+`","name":"w710-other","templates":`+refs(otherType.ID)+`}`), "templates[0]")
	w = call(orgA, http.MethodPut, "/api/v1/cis/"+bare.ID+"/templates", `{"templates":`+refs(system)+`}`)
	want("a system template of the type", w, http.StatusOK)
	if got := decode(w).Templates; len(got) != 1 || got[0].Key != "physical_server" || got[0].Version != 1 {
		t.Fatalf("system template: %+v", got)
	}
	want("PATCH with templates", call(orgA, http.MethodPatch, "/api/v1/cis/"+bare.ID, `{"templates":[]}`), http.StatusBadRequest)

	// A template in use cannot be deleted.
	err = types.DeleteTemplate(orgA, f.OrgA, second.ID)
	var pgErr *pgconn.PgError
	if status, _, _, _ := api.ClassifyDBError(err); !errors.As(err, &pgErr) || pgErr.Code != "23503" || status != http.StatusUnprocessableEntity {
		t.Fatalf("delete of an assigned template: %v", err)
	}

	// Client scope: derived, propagated, enforced.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, device.ID, f.Client2); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM ci_template WHERE ci_id = $1 AND client_id = $2`, device.ID, f.Client2); n != 1 {
		t.Fatalf("%d assignment rows followed the CI to client 2, want 1", n)
	}
	if n := count(`SELECT count(*) FROM ci_template WHERE ci_id = $1 AND client_id = $2`, bare.ID, f.Client1); n != 1 {
		t.Fatalf("%d assignment rows of the CI of client 1, want 1", n)
	}
	want("client 2 reads the templates of a CI of client 1", call(client2, http.MethodGet, "/api/v1/cis/"+bare.ID+"/templates", ""),
		http.StatusNotFound)
	want("client 2 replaces the templates of a CI of client 1",
		call(client2, http.MethodPut, "/api/v1/cis/"+bare.ID+"/templates", `{"templates":[]}`), http.StatusNotFound)
	want("client 2 reads the templates of its CI", call(client2, http.MethodGet, path, ""), http.StatusOK)
	if n := count(`SELECT count(*) FROM ci_template WHERE ci_id = $1`, bare.ID); n != 1 {
		t.Fatalf("the refused write of client 2 left %d rows, want 1", n)
	}

	// The type change drops the templates that do not fit the target type.
	w = call(orgA, http.MethodPost, "/api/v1/cis/"+bare.ID+"/type", `{"ci_type_id":"`+application+`"}`)
	want("type change", w, http.StatusOK)
	var changed ci.TypeChangeResult
	if err = json.Unmarshal(w.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.Changed || len(changed.DroppedTemplates) != 1 || changed.DroppedTemplates[0].TemplateID != system ||
		changed.DroppedTemplates[0].Key != "physical_server" || len(changed.CI.Templates) != 0 {
		t.Fatalf("type change: %s", w.Body.String())
	}
	if n := count(`SELECT count(*) FROM ci_template WHERE ci_id = $1`, bare.ID); n != 0 {
		t.Fatalf("%d assignment rows after the type change, want 0", n)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.type_changed'
		AND changes->'dropped_templates'->0->>'key' = 'physical_server'`, bare.ID); n != 1 {
		t.Fatalf("%d audit entries ci.type_changed naming the dropped template, want 1", n)
	}
	// Without a dropped template the field is an empty list.
	w = call(orgA, http.MethodPost, "/api/v1/cis/"+bare.ID+"/type", `{"ci_type_id":"`+server+`"}`)
	want("type change back", w, http.StatusOK)
	var back map[string]json.RawMessage
	if err = json.Unmarshal(w.Body.Bytes(), &back); err != nil || string(back["dropped_templates"]) != "[]" {
		t.Fatalf("type change back: %s", w.Body.String())
	}
}

// TestTemplateAssignmentWithoutDatabase covers WP-710 in memory mode: GET
// and PUT /cis/{id}/templates and a POST /cis with templates answer 503.
func TestTemplateAssignmentWithoutDatabase(t *testing.T) {
	svc := ci.NewService(ci.NewMemoryRepository()).WithFieldResolver(citype.NewMemoryRepository())
	mux := chi.NewRouter()
	ci.NewHandler(svc).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "00000000-0000-0000-0000-0000000000a1"})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/cis/00000000-0000-0000-0000-000000000001/templates", ""},
		{http.MethodPut, "/api/v1/cis/00000000-0000-0000-0000-000000000001/templates", `{"templates":[]}`},
		{http.MethodPost, "/api/v1/cis", `{"ci_type_id":"00000000-0000-0000-0000-000000000002","name":"x",` +
			`"templates":[{"template_id":"00000000-0000-0000-0000-000000000003"}]}`},
	} {
		r := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(c.body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d %s, want 503", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
