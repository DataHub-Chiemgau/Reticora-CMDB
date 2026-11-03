package citype_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// The template tests live in the external test package because discovery
// imports citype (WP-251).

type templatePage struct {
	Data       []citype.Template `json:"data"`
	Total      int               `json:"total"`
	NextCursor string            `json:"next_cursor"`
}

type templateVersionPage struct {
	Data []citype.TemplateVersion `json:"data"`
}

func (a *setAPI) createTemplate(org, typeID, key string) citype.Template {
	a.t.Helper()
	var tpl citype.Template
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/templates",
		`{"key":"`+key+`","ci_type_id":"`+typeID+`","display_name":{"de":"Vorlage `+key+`","en":"Template `+key+`"}}`, &tpl)
	return tpl
}

func (a *setAPI) createChildType(org, key, parent string) citype.Type {
	a.t.Helper()
	var typ citype.Type
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/ci-types",
		`{"key":"`+key+`","name":"`+key+`","parent_type_id":"`+parent+`"}`, &typ)
	return typ
}

func (a *setAPI) definition(org, body string) citype.AttributeDefinition {
	a.t.Helper()
	var d citype.AttributeDefinition
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions", body, &d)
	return d
}

func (a *setAPI) setTypeSets(org, typeID string, setIDs ...string) {
	a.t.Helper()
	parts := make([]string, 0, len(setIDs))
	for i, id := range setIDs {
		parts = append(parts, `{"attribute_set_id":"`+id+`","position":`+strconv.Itoa(i)+`}`)
	}
	a.must(http.StatusOK, org, http.MethodPut, "/api/v1/ci-types/"+typeID+"/attribute-sets",
		`{"attribute_sets":[`+strings.Join(parts, ",")+`]}`, nil)
}

func versionPath(templateID string, version int) string {
	return "/api/v1/templates/" + templateID + "/versions/" + strconv.Itoa(version)
}

// TestTemplateMaintenanceAgainstPostgres covers MET-16, API-06, MET-53 and
// AUD-07 (WP-251): CRUD of templates with the filter ci_type_id and
// cursor pagination, key and ci_type_id immutable (422), versions with
// items of the type chain and global definitions (a definition of another
// type, of another organization or of a set is 422, an override of
// unique/searchable is 422), direct publishing (current_version,
// published_by), a published version is immutable (409), a new version is
// a copy of the current one. System templates are read-only (PATCH,
// DELETE, PUT and POST of versions, publish: 403) but can be cloned into
// an organization template with cloned_from_id. Organization B sees the
// system templates but none of organization A. The changes are audited as
// template.* and template_version.* with before and after.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTemplateMaintenanceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()

	parent := a.createType(f.OrgA, "wp251-parent")
	typ := a.createChildType(f.OrgA, "wp251-child", parent.ID)
	other := a.createType(f.OrgA, "wp251-other")
	foreignType := a.createType(f.OrgB, "wp251-foreign")
	parentAttr := a.definition(f.OrgA, `{"ci_type_id":"`+parent.ID+`","key":"wp251_rack","data_type":"text","inheritable":true}`)
	speed := a.definition(f.OrgA, `{"ci_type_id":"`+typ.ID+`","key":"wp251_speed","data_type":"integer"}`)
	otherAttr := a.definition(f.OrgA, `{"ci_type_id":"`+other.ID+`","key":"wp251_other","data_type":"text"}`)
	global := a.definition(f.OrgA, `{"key":"wp251_owner","data_type":"text"}`)
	foreignAttr := a.definition(f.OrgB, `{"ci_type_id":"`+foreignType.ID+`","key":"wp251_foreign","data_type":"text"}`)
	set := a.createSet(f.OrgA, "wp251-set")
	var setAttr citype.AttributeDefinition
	w := a.setAttribute(f.OrgA, set.ID, `{"key":"wp251_vlan","data_type":"integer"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("set attribute: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &setAttr)

	// CRUD and list.
	var created []citype.Template
	for i := 0; i < 3; i++ {
		created = append(created, a.createTemplate(f.OrgA, typ.ID, "wp251-tpl-"+strconv.Itoa(i)))
	}
	a.createTemplate(f.OrgA, other.ID, "wp251-tpl-other")
	foreignTpl := a.createTemplate(f.OrgB, foreignType.ID, "wp251-tpl-b")
	tpl := created[0]
	if tpl.OrganizationID != f.OrgA || tpl.IsSystem || !tpl.IsActive || tpl.CurrentVersion != nil || tpl.CITypeID != typ.ID ||
		tpl.DisplayName["en"] != "Template wp251-tpl-0" || tpl.ClonedFromID != "" {
		t.Fatalf("created template: %+v", tpl)
	}
	w = a.call(f.OrgA, http.MethodPost, "/api/v1/templates", `{"key":"wp251-tpl-1","ci_type_id":"`+typ.ID+`"}`)
	if p := problemOf(w); w.Code != http.StatusConflict || p.Field != "key" {
		t.Errorf("duplicate key: %d %s", w.Code, w.Body.String())
	}
	for _, typeID := range []string{f.ID(), foreignType.ID, "not-a-uuid"} {
		w = a.call(f.OrgA, http.MethodPost, "/api/v1/templates", `{"key":"wp251-x","ci_type_id":"`+typeID+`"}`)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != "ci_type_id" {
			t.Errorf("template on type %s: %d %s", typeID, w.Code, w.Body.String())
		}
	}

	seen := map[string]bool{}
	path := "/api/v1/templates?limit=2&ci_type_id=" + typ.ID
	for pages := 0; path != ""; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		var page templatePage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &page)
		if len(page.Data) > 2 || page.Total != 3 {
			t.Fatalf("page of %d templates (total %d), limit 2", len(page.Data), page.Total)
		}
		for _, x := range page.Data {
			if seen[x.ID] || x.CITypeID != typ.ID {
				t.Errorf("template %s twice or of another type", x.Key)
			}
			seen[x.ID] = true
		}
		path = ""
		if page.NextCursor != "" {
			path = "/api/v1/templates?limit=2&ci_type_id=" + typ.ID + "&cursor=" + page.NextCursor
		}
	}
	if len(seen) != 3 {
		t.Errorf("filtered pages list %d templates, want 3", len(seen))
	}
	systemKeys := func(org string) (own []string, system []string) {
		var page templatePage
		a.must(http.StatusOK, org, http.MethodGet, "/api/v1/templates?limit=200", "", &page)
		for _, x := range page.Data {
			switch {
			case x.IsSystem && x.OrganizationID == "":
				system = append(system, x.Key)
			case x.OrganizationID == org:
				own = append(own, x.Key)
			default:
				t.Errorf("organization %s sees template %s of %s", org, x.Key, x.OrganizationID)
			}
		}
		slices.Sort(own)
		return own, system
	}
	ownA, systemA := systemKeys(f.OrgA)
	ownB, systemB := systemKeys(f.OrgB)
	if len(ownA) != 4 || len(systemA) != 9 || len(systemB) != 9 || !slices.Equal(ownB, []string{"wp251-tpl-b"}) {
		t.Errorf("visible templates: A %v + %d system, B %v + %d system", ownA, len(systemA), ownB, len(systemB))
	}
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, "/api/v1/templates/"+tpl.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/templates/"+foreignTpl.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodPatch, "/api/v1/templates/"+tpl.ID, `{"description":"x"}`, nil)

	var got citype.Template
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/templates/"+tpl.ID,
		`{"display_name":{"de":"Netz","en":"Network"},"description":"switch ports","is_active":false}`, &got)
	if got.DisplayName["de"] != "Netz" || got.Description != "switch ports" || got.IsActive || got.Key != tpl.Key {
		t.Errorf("patch: %+v", got)
	}
	for _, body := range []string{`{"key":"wp251-renamed"}`, `{"ci_type_id":"` + other.ID + `"}`} {
		if w = a.call(f.OrgA, http.MethodPatch, "/api/v1/templates/"+tpl.ID, body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("patch %s: %d %s", body, w.Code, w.Body.String())
		}
	}

	// Version 1 is an empty draft; its items reference the type chain and
	// global definitions.
	var v citype.TemplateVersion
	a.must(http.StatusOK, f.OrgA, http.MethodGet, versionPath(tpl.ID, 1), "", &v)
	if v.Status != citype.TemplateVersionDraft || v.Version != 1 || len(v.Items) != 0 || v.TemplateID != tpl.ID {
		t.Fatalf("version 1: %+v", v)
	}
	items := `{"items":[` +
		`{"attribute_definition_id":"` + speed.ID + `","position":1,"required_override":true,"default_override":5,"validation_override":{"min":1,"max":100}},` +
		`{"attribute_definition_id":"` + parentAttr.ID + `","position":0},` +
		`{"attribute_definition_id":"` + global.ID + `","position":2,"default_override":"ops"}]}`
	a.must(http.StatusOK, f.OrgA, http.MethodPut, versionPath(tpl.ID, 1), items, &v)
	keys := make([]string, 0, len(v.Items))
	for _, it := range v.Items {
		keys = append(keys, it.Key)
	}
	if !slices.Equal(keys, []string{"wp251_rack", "wp251_speed", "wp251_owner"}) || v.Status != citype.TemplateVersionDraft {
		t.Fatalf("items: %v %+v", keys, v)
	}
	if it := v.Items[1]; it.RequiredOverride == nil || !*it.RequiredOverride || string(it.DefaultOverride) != "5" || it.ValidationOverride == nil {
		t.Errorf("overrides: %+v", it)
	}

	for _, c := range []struct{ name, body, field string }{
		{"definition of another type", `{"items":[{"attribute_definition_id":"` + otherAttr.ID + `","position":0}]}`, "items[0].attribute_definition_id"},
		{"definition of another organization", `{"items":[{"attribute_definition_id":"` + foreignAttr.ID + `","position":0}]}`, "items[0].attribute_definition_id"},
		{"set attribute", `{"items":[{"attribute_definition_id":"` + setAttr.ID + `","position":0}]}`, "items[0].attribute_definition_id"},
		{"unknown definition", `{"items":[{"attribute_definition_id":"` + f.ID() + `","position":0}]}`, "items[0].attribute_definition_id"},
		{"override of unique", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"unique":true}]}`, "items[0].unique"},
		{"override of searchable", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"searchable":true}]}`, "items[0].searchable"},
		{"override of data_type", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"data_type":"text"}]}`, "items[0].data_type"},
		{"invalid default", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"default_override":"fast"}]}`, "items[0].default_override"},
		{"default outside the narrowed range", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"validation_override":{"max":10},"default_override":50}]}`, "items[0].default_override"},
		{"empty range", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0,"validation_override":{"min":10,"max":1}}]}`, "items[0].validation_override"},
		{"position twice", `{"items":[{"attribute_definition_id":"` + speed.ID + `","position":0},{"attribute_definition_id":"` + global.ID + `","position":0}]}`, "items[1].position"},
	} {
		w = a.call(f.OrgA, http.MethodPut, versionPath(tpl.ID, 1), c.body)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != c.field {
			t.Errorf("%s: %d %s, want 422 on %s", c.name, w.Code, w.Body.String(), c.field)
		}
	}
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, versionPath(tpl.ID, 7), "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/templates/"+tpl.ID+"/versions/one", "", nil)

	// Publishing sets current_version; the published version is immutable.
	var published citype.TemplateVersion
	a.must(http.StatusOK, f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", "", &published)
	if published.Status != citype.TemplateVersionPublished || published.PublishedAt == nil || published.PublishedBy != a.user ||
		len(published.Items) != 3 {
		t.Fatalf("published: %+v", published)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/templates/"+tpl.ID, "", &got)
	if got.CurrentVersion == nil || *got.CurrentVersion != 1 {
		t.Errorf("current_version after publish: %+v", got)
	}
	a.must(http.StatusConflict, f.OrgA, http.MethodPut, versionPath(tpl.ID, 1), `{"items":[]}`, nil)
	a.must(http.StatusConflict, f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", "", nil)

	// A new version is a draft copy of the current one.
	var v2 citype.TemplateVersion
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/templates/"+tpl.ID+"/versions", "", &v2)
	if v2.Version != 2 || v2.Status != citype.TemplateVersionDraft || len(v2.Items) != 3 || v2.Items[1].Key != "wp251_speed" ||
		string(v2.Items[1].DefaultOverride) != "5" {
		t.Fatalf("version 2: %+v", v2)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPut, versionPath(tpl.ID, 2),
		`{"items":[{"attribute_definition_id":"`+global.ID+`","position":0}]}`, &v2)
	var versions templateVersionPage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/templates/"+tpl.ID+"/versions?limit=10", "", &versions)
	if len(versions.Data) != 2 || versions.Data[0].Version != 2 || versions.Data[1].Version != 1 || len(versions.Data[0].Items) != 1 ||
		len(versions.Data[1].Items) != 3 {
		t.Errorf("versions: %+v", versions.Data)
	}
	// Version 1 stays as published.
	var v1 citype.TemplateVersion
	a.must(http.StatusOK, f.OrgA, http.MethodGet, versionPath(tpl.ID, 1), "", &v1)
	if v1.Status != citype.TemplateVersionPublished || len(v1.Items) != 3 {
		t.Errorf("version 1 after version 2: %+v", v1)
	}

	// System templates are read-only but can be cloned.
	var sysPage templatePage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/templates?limit=200&ci_type_id="+systemTypeID(t, f, "server"), "", &sysPage)
	var sys citype.Template
	for _, x := range sysPage.Data {
		if x.Key == "physical_server" {
			sys = x
		}
	}
	if sys.ID == "" || !sys.IsSystem || sys.CurrentVersion == nil || *sys.CurrentVersion != 1 {
		t.Fatalf("system template physical_server: %+v", sysPage.Data)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/templates/" + sys.ID, `{"is_active":false}`},
		{http.MethodDelete, "/api/v1/templates/" + sys.ID, ""},
		{http.MethodPut, versionPath(sys.ID, 1), `{"items":[]}`},
		{http.MethodPost, "/api/v1/templates/" + sys.ID + "/versions", ""},
		{http.MethodPost, versionPath(sys.ID, 1) + "/publish", ""},
	} {
		if w = a.call(f.OrgA, c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s on a system template: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	var clone citype.Template
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/templates/"+sys.ID+"/clone", `{"key":"wp251-server"}`, &clone)
	if clone.OrganizationID != f.OrgA || clone.IsSystem || clone.ClonedFromID != sys.ID || clone.CITypeID != sys.CITypeID ||
		clone.CurrentVersion != nil || clone.DisplayName["en"] != sys.DisplayName["en"] {
		t.Errorf("clone of the system template: %+v", clone)
	}
	var cloneV1 citype.TemplateVersion
	a.must(http.StatusOK, f.OrgA, http.MethodGet, versionPath(clone.ID, 1), "", &cloneV1)
	if cloneV1.Status != citype.TemplateVersionDraft || len(cloneV1.Items) != 0 || cloneV1.PublishedAt != nil {
		t.Errorf("clone version 1: %+v", cloneV1)
	}
	a.must(http.StatusConflict, f.OrgA, http.MethodPost, "/api/v1/templates/"+sys.ID+"/clone", `{"key":"wp251-server"}`, nil)
	// A clone takes the items of the current (published) version.
	var ownClone citype.Template
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/templates/"+tpl.ID+"/clone",
		`{"key":"wp251-copy","display_name":{"de":"Kopie","en":"Copy"}}`, &ownClone)
	var ownV1 citype.TemplateVersion
	a.must(http.StatusOK, f.OrgA, http.MethodGet, versionPath(ownClone.ID, 1), "", &ownV1)
	if ownClone.ClonedFromID != tpl.ID || ownClone.DisplayName["en"] != "Copy" || len(ownV1.Items) != 3 ||
		ownV1.Status != citype.TemplateVersionDraft {
		t.Errorf("clone of an own template: %+v %+v", ownClone, ownV1)
	}

	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/templates/"+created[2].ID, "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/templates/"+created[2].ID, "", nil)

	// Audit (AUD-07): template.* and template_version.* with before/after.
	rows, err := f.Admin.Query(bg, `SELECT action, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''), changes
		FROM audit_log WHERE organization_id = $1 AND resource_type IN ('template', 'template_version') ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type entry struct {
		action, resource, actor string
		changes                 map[string]any
	}
	byAction := map[string][]entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.action, &e.resource, &e.actor, &e.changes); err != nil {
			t.Fatal(err)
		}
		if e.actor != a.user {
			t.Errorf("%s: actor %s", e.action, e.actor)
		}
		byAction[e.action] = append(byAction[e.action], e)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]struct {
		n             int
		before, after bool
	}{
		"template.created":           {4, false, true},
		"template.updated":           {1, true, true},
		"template.cloned":            {2, false, true},
		"template.deleted":           {1, true, false},
		"template_version.created":   {7, false, true},
		"template_version.updated":   {2, true, true},
		"template_version.published": {1, true, true},
	} {
		got := byAction[action]
		if len(got) != want.n {
			t.Errorf("%s: %d entries, want %d", action, len(got), want.n)
			continue
		}
		for _, e := range got {
			if (e.changes["before"] != nil) != want.before || (e.changes["after"] != nil) != want.after {
				t.Errorf("%s: changes %v", action, e.changes)
			}
		}
	}
	if pub := byAction["template_version.published"]; len(pub) == 1 {
		before, _ := pub[0].changes["before"].(map[string]any)
		after, _ := pub[0].changes["after"].(map[string]any)
		if before["status"] != citype.TemplateVersionDraft || after["status"] != citype.TemplateVersionPublished {
			t.Errorf("template_version.published: %v", pub[0].changes)
		}
	}
	if result, verr := audit.Verify(bg, f.App, f.OrgA); verr != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, verr)
	}
}

// TestTemplatePublishCompositionAgainstPostgres covers MET-16 and MET-53
// (WP-251): publishing composes the version after the system base, the
// organization's global attributes, the type chain and the sets of the
// type. An override whose allowed values leave nothing against the type
// chain or a set of the type is accepted in the draft (it narrows its own
// definition) but refused at publishing with 422, the attribute key as
// field and the code enum-empty; nothing is published. A compatible
// override publishes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTemplatePublishCompositionAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)

	parent := a.createType(f.OrgA, "wp251-cmp-parent")
	typ := a.createChildType(f.OrgA, "wp251-cmp-child", parent.ID)
	mode := a.definition(f.OrgA, `{"key":"wp251_mode","data_type":"enum","enum_values":["a","b","c"]}`)
	a.definition(f.OrgA, `{"ci_type_id":"`+parent.ID+`","key":"wp251_mode","data_type":"enum","enum_values":["a","b"],"inheritable":true}`)
	color := a.definition(f.OrgA, `{"key":"wp251_color","data_type":"enum","enum_values":["red","green","blue"]}`)
	set := a.createSet(f.OrgA, "wp251-cmp-set")
	if w := a.setAttribute(f.OrgA, set.ID, `{"key":"wp251_color","data_type":"enum","enum_values":["red","green"]}`); w.Code != http.StatusCreated {
		t.Fatalf("set attribute: %d %s", w.Code, w.Body.String())
	}
	a.setTypeSets(f.OrgA, typ.ID, set.ID)

	for _, c := range []struct{ name, defID, key, options string }{
		{"type chain", mode.ID, "wp251_mode", `["c"]`},
		{"set of the type", color.ID, "wp251_color", `["blue"]`},
	} {
		tpl := a.createTemplate(f.OrgA, typ.ID, "wp251-cmp-"+c.key)
		a.must(http.StatusOK, f.OrgA, http.MethodPut, versionPath(tpl.ID, 1),
			`{"items":[{"attribute_definition_id":"`+c.defID+`","position":0,"validation_override":{"options":`+c.options+`}}]}`, nil)
		w := a.call(f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", "")
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != c.key || p.Code != citype.CodeEnumEmpty {
			t.Errorf("%s: publish %d %s, want 422 on %s with %s", c.name, w.Code, w.Body.String(), c.key, citype.CodeEnumEmpty)
		}
		var v citype.TemplateVersion
		a.must(http.StatusOK, f.OrgA, http.MethodGet, versionPath(tpl.ID, 1), "", &v)
		var got citype.Template
		a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/templates/"+tpl.ID, "", &got)
		if v.Status != citype.TemplateVersionDraft || got.CurrentVersion != nil {
			t.Errorf("%s: refused publish changed the version: %+v %+v", c.name, v, got)
		}
		// A compatible override publishes.
		a.must(http.StatusOK, f.OrgA, http.MethodPut, versionPath(tpl.ID, 1),
			`{"items":[{"attribute_definition_id":"`+c.defID+`","position":0,"validation_override":{"options":["a","red"]},"required_override":true}]}`, nil)
		w = a.call(f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", "")
		if w.Code != http.StatusOK {
			t.Errorf("%s: compatible override: %d %s", c.name, w.Code, w.Body.String())
		}
	}
}

// TestSystemTemplatesMatchCollectorProfiles covers MET-16 (WP-251, E-37,
// WP-581): the migration seeds exactly nine read-only system templates,
// each at the system type of its profiles with version 1 published and
// without items, and discovery.SystemTemplateKey maps every systemTemplate
// value of the shipped collector profiles onto the key of one of them;
// every system template is the target of a profile.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSystemTemplatesMatchCollectorProfiles(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	bg := context.Background()
	wantTypes := map[string]string{
		"physical_server": "server", "switch": "switch", "router": "router", "firewall": "firewall",
		"access_point": "access_point", "hypervisor": "hypervisor", "storage": "storage_array", "ups": "ups", "pdu": "pdu",
	}
	rows, err := f.Admin.Query(bg, `SELECT t.key, ct.key, t.is_system, COALESCE(t.current_version, 0),
			(SELECT count(*) FROM template_version v WHERE v.template_id = t.id AND v.version = 1 AND v.status = 'published'),
			(SELECT count(*) FROM template_version_item i JOIN template_version v ON v.id = i.template_version_id WHERE v.template_id = t.id)
		FROM template t JOIN ci_type ct ON ct.id = t.ci_type_id WHERE t.organization_id IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	system := map[string]bool{}
	for rows.Next() {
		var key, typeKey string
		var isSystem bool
		var current, published, items int
		if err = rows.Scan(&key, &typeKey, &isSystem, &current, &published, &items); err != nil {
			t.Fatal(err)
		}
		system[key] = true
		if wantTypes[key] != typeKey || !isSystem || current != 1 || published != 1 || items != 0 {
			t.Errorf("system template %s: type %s, is_system %v, current %d, published v1 %d, items %d",
				key, typeKey, isSystem, current, published, items)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(system) != len(wantTypes) {
		t.Errorf("system templates %v, want the %d keys of %v", system, len(wantTypes), wantTypes)
	}

	files, err := filepath.Glob("../../../collector/profiles/data/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("collector profiles: %v (%d files)", err, len(files))
	}
	targets := map[string]bool{}
	for _, file := range files {
		raw, rerr := os.ReadFile(filepath.Clean(file))
		if rerr != nil {
			t.Fatal(rerr)
		}
		var profiles []struct {
			Name           string `yaml:"name"`
			SystemTemplate string `yaml:"systemTemplate"`
		}
		if uerr := yaml.Unmarshal(raw, &profiles); uerr != nil {
			t.Fatalf("%s: %v", file, uerr)
		}
		for _, p := range profiles {
			if p.SystemTemplate == "" {
				continue
			}
			key, ok := discovery.SystemTemplateKey(p.SystemTemplate)
			if !ok || !system[key] {
				t.Errorf("%s profile %s: systemTemplate %s maps to %q (%v), no system template", filepath.Base(file), p.Name,
					p.SystemTemplate, key, ok)
			}
			targets[key] = true
		}
	}
	for key := range system {
		if !targets[key] {
			t.Errorf("system template %s is the target of no collector profile", key)
		}
	}
}
