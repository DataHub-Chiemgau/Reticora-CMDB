package metamodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// importFile is a metamodel export v2 of an organization (WP-722): a
// system type, two own types (a subtype of the other, with lifecycle
// model, relationship type and a set), a set, three definitions (of a
// type, of the set, a global one restricted to the subtype), a template
// with three versions, rules on a type and on the current and an old
// template version, a layout with a role and an own lifecycle model.
func importFile() *citype.MetamodelExportV2 {
	three, one, current := 3, 1, 3
	text := func(s string) citype.LocalizedText { return citype.LocalizedText{"de": s, "en": s} }
	typeBody := func(name, parent string, sets ...string) *citype.ExportTypeBody {
		return &citype.ExportTypeBody{Name: name, DisplayName: text(name), IsActive: true, IsObservable: true,
			ParentTypeKey: parent, AttributeSetKeys: append([]string{}, sets...), AllowedRelationshipTypes: []string{}}
	}
	appX := typeBody("App X", "server", "net")
	appX.LifecycleModelKey, appX.AllowedRelationshipTypes = "lm_app", []string{"depends_on"}
	attr := func(dataType string) *citype.ExportAttributeBody {
		return &citype.ExportAttributeBody{DisplayName: text(dataType), DataType: dataType, UniqueScope: "org", Inheritable: true,
			AppliesTo: citype.AppliesToAll, AppliesToCategories: []string{}, AppliesToTypeKeys: []string{}}
	}
	owner := attr("string")
	owner.AppliesTo, owner.AppliesToTypeKeys = citype.AppliesToTypes, []string{"app_y"}
	version := func(n int, status string) citype.ExportTemplateVersionV2 {
		return citype.ExportTemplateVersionV2{Version: n, Status: status, Items: []citype.ExportTemplateItemV2{
			{AttributeKey: "port", CITypeKey: "app_x", Position: n}}}
	}
	rule := func(attribute string) *citype.ExportRuleBody {
		return &citype.ExportRuleBody{AttributeKey: attribute, Kind: "required", Condition: json.RawMessage("null"),
			AST: json.RawMessage("null"), Message: text("needed"), IsActive: true}
	}
	typeRule, v3Rule, v1Rule := rule("port"), rule("port"), rule("port")
	typeRule.CITypeKey = "app_x"
	v3Rule.TemplateKey, v3Rule.TemplateVersion = "tpl_x", &three
	v1Rule.TemplateKey, v1Rule.TemplateVersion = "tpl_x", &one
	return &citype.MetamodelExportV2{
		FormatVersion: 2, OrganizationID: "org-source", OmittedSections: []string{},
		CITypes: []citype.ExportTypeV2{
			{Key: "app_x", ExportTypeBody: appX},
			{Key: "app_y", ExportTypeBody: typeBody("App Y", "app_x")},
			{Key: "server", IsSystem: true},
		},
		AttributeDefinitions: []citype.ExportAttributeV2{
			{Key: "owner", Scope: "global", ExportAttributeBody: owner},
			{Key: "ip", Scope: "set", AttributeSetKey: "net", ExportAttributeBody: attr("string")},
			{Key: "port", Scope: "type", CITypeKey: "app_x", ExportAttributeBody: attr("integer")},
		},
		AttributeSets: []citype.ExportAttributeSetV2{{Key: "net",
			ExportAttributeSetBody: &citype.ExportAttributeSetBody{DisplayName: text("Net"), IsActive: true}}},
		Templates: []citype.ExportTemplateV2{{Key: "tpl_x", ExportTemplateBody: &citype.ExportTemplateBody{CITypeKey: "app_x",
			DisplayName: text("Tpl"), IsActive: true, CurrentVersion: &current,
			Versions: []citype.ExportTemplateVersionV2{version(1, "retired"), version(2, "retired"), version(3, "published")}}}},
		Rules: []citype.ExportRuleV2{
			{Key: "r_type", ExportRuleBody: typeRule},
			{Key: "r_v1", ExportRuleBody: v1Rule},
			{Key: "r_v3", ExportRuleBody: v3Rule},
		},
		Layouts: []citype.ExportLayoutV2{{Key: "lay_x", CITypeKey: "app_x", RoleName: "Operators", RoleKind: "custom_role",
			Priority: 1, IsActive: true, Layout: citype.LayoutDocument{Sections: []citype.LayoutSection{}}}},
		LifecycleModels: []citype.ExportLifecycleModelV2{{Key: "lm_app",
			ExportLifecycleModelBody: &citype.ExportLifecycleModelBody{Name: "App"}}},
	}
}

// emptyTarget is an organization with the system type server, the
// lifecycle model lm_app (created per REST, WP-723), the role Operators
// and the relationship type depends_on.
func emptyTarget() *ImportTarget {
	doc := &citype.MetamodelExportV2{FormatVersion: 2, CITypes: []citype.ExportTypeV2{{Key: "server", IsSystem: true}}}
	t := newImportTarget(doc)
	t.Types["server"] = TargetRef{ID: "id-type-server", System: true}
	t.Models["lm_app"] = TargetRef{ID: "id-model-lm_app"}
	t.Roles["Operators"] = []string{"id-role-operators"}
	t.RelationshipTypes["depends_on"] = true
	return t
}

// targetOf is the target that holds doc: every part under a synthetic id.
func targetOf(doc *citype.MetamodelExportV2) *ImportTarget {
	t := emptyTarget()
	t.Doc = doc
	for i := range doc.CITypes {
		putRef(t.Types, doc.CITypes[i].Key, TargetRef{ID: "id-type-" + doc.CITypes[i].Key, System: doc.CITypes[i].IsSystem})
	}
	for i := range doc.AttributeSets {
		putRef(t.Sets, doc.AttributeSets[i].Key, TargetRef{ID: "id-set-" + doc.AttributeSets[i].Key, System: doc.AttributeSets[i].IsSystem})
	}
	for i := range doc.AttributeDefinitions {
		d := &doc.AttributeDefinitions[i]
		t.Defs[defKey(d.CITypeKey, d.AttributeSetKey, d.Key)] = TargetRef{ID: "id-def-" + d.Key, System: d.IsSystem}
	}
	for i := range doc.Templates {
		tpl := &doc.Templates[i]
		putRef(t.Templates, tpl.Key, TargetRef{ID: "id-tpl-" + tpl.Key, System: tpl.IsSystem})
		if tpl.ExportTemplateBody != nil {
			for _, v := range tpl.Versions {
				t.Versions[VersionKey{Template: tpl.Key, Version: v.Version}] = "id-version-" + tpl.Key + "-" + string(rune('0'+v.Version))
			}
		}
	}
	for i := range doc.Rules {
		r := &doc.Rules[i]
		if r.ExportRuleBody != nil && r.TemplateKey != "" {
			t.VersionRules[VersionRuleKey{VersionKey: VersionKey{Template: r.TemplateKey, Version: *r.TemplateVersion}, Key: r.Key}] = "id-rule-" + r.Key
			continue
		}
		putRef(t.Rules, r.Key, TargetRef{ID: "id-rule-" + r.Key, System: r.IsSystem})
	}
	for i := range doc.Layouts {
		t.Layouts[doc.Layouts[i].Key] = "id-layout-" + doc.Layouts[i].Key
	}
	return t
}

// steps lists the items as "kind/op".
func steps(items []ImportItem) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].Kind+"/"+items[i].Op)
	}
	return out
}

func action(r *ImportReport, kind, key string) string {
	for _, it := range r.Items {
		if it.Kind == kind && it.Key == key {
			return it.Action
		}
	}
	return ""
}

func hasConflict(r *ImportReport, code, reference, refKey string) bool {
	for _, c := range r.Conflicts {
		if c.Code == code && c.Reference == reference && c.ReferenceKey == refKey {
			return true
		}
	}
	return false
}

func noteCount(r *ImportReport, code string) int {
	n := 0
	for _, note := range r.Notes {
		if note.Code == code {
			n++
		}
	}
	return n
}

// TestImportPlanCreatesEverythingInDependencyOrder covers MET-54 (WP-722):
// into an organization without the parts every part becomes a create item
// in the order of the dependencies (parent before subtype, types and sets
// before assignment and definitions, template version before its rules,
// types before layouts); parts the draft creates are referenced by their
// item; the new template version is published after its rules (they are
// immutable afterwards); system parts and the lifecycle model are only
// resolved; of the
// template with three versions only the current one is taken over and the
// older versions and their rule are history-skipped.
func TestImportPlanCreatesEverythingInDependencyOrder(t *testing.T) {
	plan := planImport(importFile(), emptyTarget())
	if len(plan.report.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", plan.report.Conflicts)
	}
	want := []string{"ci_type/create", "ci_type/create", "attribute_set/create", "ci_type/assign",
		"attribute_definition/create", "attribute_definition/create", "attribute_definition/create",
		"template/create", "template_version/create", "template_version/update",
		"rule/create", "rule/create", "template_version/publish", "layout/create"}
	if got := steps(plan.items); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("items = %v\nwant    %v", got, want)
	}
	items := plan.items
	if items[0].Payload["key"] != "app_x" || items[0].Payload["parent_type_id"] != "id-type-server" ||
		items[0].Payload["lifecycle_model_id"] != "id-model-lm_app" {
		t.Errorf("app_x = %+v", items[0].Payload)
	}
	if items[1].Payload["key"] != "app_y" || items[1].Payload["parent_type_id"] != planRef(0) {
		t.Errorf("app_y must reference the item of its new parent: %+v", items[1].Payload)
	}
	if items[3].Target != planRef(0) {
		t.Errorf("the assignment targets the new type: %+v", items[3])
	}
	sets := items[3].Payload["attribute_sets"].([]any)
	if len(sets) != 1 || sets[0].(map[string]any)["attribute_set_id"] != planRef(2) {
		t.Errorf("assigned sets = %+v", sets)
	}
	byKey := map[string]map[string]any{}
	for _, it := range items[4:7] {
		byKey[it.Payload["key"].(string)] = it.Payload
	}
	if byKey["port"]["ci_type_id"] != planRef(0) || byKey["ip"]["attribute_set_id"] != planRef(2) {
		t.Errorf("definitions must reference their new owners: %+v", byKey)
	}
	if ids := byKey["owner"]["applies_to_type_ids"].([]any); len(ids) != 1 || ids[0] != planRef(1) {
		t.Errorf("applies_to_type_ids = %v", byKey["owner"]["applies_to_type_ids"])
	}
	if items[7].Payload["ci_type_id"] != planRef(0) || items[8].Target != planRef(7) {
		t.Errorf("template and its version: %+v %+v", items[7], items[8])
	}
	for _, it := range []ImportItem{items[9], items[12]} {
		if it.Target != planRef(8) || it.Template != planRef(7) {
			t.Errorf("%s/%s must target the new version of the new template: %+v", it.Kind, it.Op, it)
		}
	}
	tplItems := items[9].Payload["items"].([]any)
	if len(tplItems) != 1 || tplItems[0].(map[string]any)["attribute_definition_id"] == nil {
		t.Errorf("template items = %+v", tplItems)
	}
	if items[11].Payload["template_version_id"] != planRef(8) || items[11].Payload["key"] != "r_v3" {
		t.Errorf("the rule of the current version must reference the new version: %+v", items[11].Payload)
	}
	if items[13].Payload["ci_type_id"] != planRef(0) || items[13].Payload["role_id"] != "id-role-operators" {
		t.Errorf("layout = %+v", items[13].Payload)
	}
	if got := noteCount(&plan.report, NoteHistorySkipped); got != 2 {
		t.Errorf("history-skipped notes = %d, want 2 (versions, rules): %+v", got, plan.report.Notes)
	}
	if action(&plan.report, KindCIType, "server") != ActionResolved || action(&plan.report, KindLifecycleModel, "lm_app") != ActionResolved {
		t.Errorf("system type and lifecycle model must be resolved: %+v", plan.report.Items)
	}
	if plan.report.Unchanged {
		t.Error("a plan with items is not unchanged")
	}
	for _, it := range items {
		if it.Op == OpDelete {
			t.Errorf("the import never deletes: %+v", it)
		}
	}
}

// TestImportPlanUnchanged: the file of the target itself has no items and
// is unchanged; no item deletes the parts of the target the file lacks.
func TestImportPlanUnchanged(t *testing.T) {
	file := importFile()
	plan := planImport(file, targetOf(importFile()))
	if len(plan.items) != 0 || len(plan.report.Conflicts) != 0 || !plan.report.Unchanged {
		t.Fatalf("items %v, conflicts %+v, unchanged %v", steps(plan.items), plan.report.Conflicts, plan.report.Unchanged)
	}
	if action(&plan.report, KindTemplateVersion, "tpl_x") != ActionUnchanged || action(&plan.report, KindRule, "r_v3") != ActionUnchanged {
		t.Errorf("report = %+v", plan.report.Items)
	}
	// A file without the subtype, the set and the layout deletes nothing.
	file.CITypes = file.CITypes[:1]
	file.Layouts, file.AttributeSets = nil, nil
	file.CITypes[0].AttributeSetKeys = []string{"net"}
	file.AttributeDefinitions = file.AttributeDefinitions[2:]
	plan = planImport(file, targetOf(importFile()))
	if len(plan.items) != 0 || !plan.report.Unchanged {
		t.Errorf("a smaller file changes nothing: %v %+v", steps(plan.items), plan.report.Conflicts)
	}
}

// TestImportPlanUpdates: changes of every kind become update items on the
// targets; is_active of a type becomes deactivate after every other item,
// a changed set list assign, changed items of the current template version
// a new version (create, update, publish) of the existing template, which
// starts with copies of the rules of its latest version (000207): an equal
// rule stays, a changed one is a note, a new one a create item.
func TestImportPlanUpdates(t *testing.T) {
	file := importFile()
	x := file.CITypes[0].ExportTypeBody
	x.Description, x.IsActive, x.AttributeSetKeys = "changed", false, []string{}
	file.AttributeDefinitions[2].DisplayName = citype.LocalizedText{"de": "Port", "en": "Port"}
	file.AttributeDefinitions[2].Validation = &rules.Rule{}
	file.AttributeSets[0].Description = "changed"
	tpl := file.Templates[0].ExportTemplateBody
	tpl.DisplayName = citype.LocalizedText{"de": "Neu", "en": "New"}
	tpl.Versions[2].Items[0].Position = 7
	file.Rules[0].Message = citype.LocalizedText{"de": "x", "en": "x"}
	extra := *file.Rules[2].ExportRuleBody
	file.Rules = append(file.Rules, citype.ExportRuleV2{Key: "r_v3b", ExportRuleBody: &extra})
	file.Layouts[0].Priority = 9
	plan := planImport(file, targetOf(importFile()))
	if len(plan.report.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", plan.report.Conflicts)
	}
	want := []string{"ci_type/update", "attribute_set/update", "ci_type/assign", "attribute_definition/update",
		"template/update", "template_version/create", "template_version/update",
		"rule/update", "rule/create", "template_version/publish", "layout/update", "ci_type/deactivate"}
	if got := steps(plan.items); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("items = %v\nwant    %v", got, want)
	}
	it := plan.items
	if it[0].Target != "id-type-app_x" || it[0].Payload["description"] != "changed" || len(it[0].Payload) != 1 {
		t.Errorf("type update = %+v", it[0])
	}
	if sets := it[2].Payload["attribute_sets"].([]any); it[2].Target != "id-type-app_x" || len(sets) != 0 {
		t.Errorf("assign = %+v", it[2])
	}
	if it[3].Target != "id-def-port" || it[3].Payload["display_name"] == nil || len(it[3].Payload) != 2 {
		t.Errorf("definition update = %+v", it[3].Payload)
	}
	if it[5].Target != "id-tpl-tpl_x" || it[6].Target != planRef(5) || it[6].Template != "id-tpl-tpl_x" {
		t.Errorf("new version of the existing template: %+v %+v", it[5], it[6])
	}
	if it[8].Payload["template_version_id"] != planRef(5) || it[8].Payload["key"] != "r_v3b" || it[9].Target != planRef(5) {
		t.Errorf("a new rule of the current version goes onto the new version before its publication: %+v %+v", it[8], it[9])
	}
	if action(&plan.report, KindRule, "r_v3") != ActionUnchanged {
		t.Errorf("the copied rule r_v3 = %q", action(&plan.report, KindRule, "r_v3"))
	}
	file.Rules[2].Message = citype.LocalizedText{"de": "anders", "en": "other"}
	if plan = planImport(file, targetOf(importFile())); noteCount(&plan.report, NoteNotImported) != 1 {
		t.Errorf("a changed copied rule must be a note: %+v", plan.report.Notes)
	}
	if it[10].Target != "id-layout-lay_x" || it[10].Payload["priority"] != 9 || it[11].Target != "id-type-app_x" {
		t.Errorf("layout and deactivation: %+v %+v", it[10], it[11])
	}
	if action(&plan.report, KindCIType, "app_y") != ActionUnchanged {
		t.Errorf("app_y = %q", action(&plan.report, KindCIType, "app_y"))
	}
}

// TestImportPlanConflicts: each conflict of MET-54 refuses the import:
// another data type, a missing reference of every kind (also a role name
// two roles carry) and an own part whose key is a system part.
func TestImportPlanConflicts(t *testing.T) {
	cases := []struct {
		name            string
		edit            func(f *citype.MetamodelExportV2, tg *ImportTarget)
		code, ref, rkey string
	}{
		{"data type", func(f *citype.MetamodelExportV2, tg *ImportTarget) {
			*tg = *targetOf(importFile())
			f.AttributeDefinitions[2].DataType = "string"
		}, ConflictDatatype, "", ""},
		{"parent type", func(f *citype.MetamodelExportV2, _ *ImportTarget) { f.CITypes[1].ParentTypeKey = "nope" },
			ConflictMissingReference, "parent_type", "nope"},
		{"lifecycle model", func(_ *citype.MetamodelExportV2, tg *ImportTarget) { delete(tg.Models, "lm_app") },
			ConflictMissingReference, KindLifecycleModel, "lm_app"},
		{"relationship type", func(_ *citype.MetamodelExportV2, tg *ImportTarget) { delete(tg.RelationshipTypes, "depends_on") },
			ConflictMissingReference, "relationship_type", "depends_on"},
		{"ci type of a definition", func(f *citype.MetamodelExportV2, _ *ImportTarget) {
			f.AttributeDefinitions[0].AppliesToTypeKeys = []string{"nope"}
		}, ConflictMissingReference, KindCIType, "nope"},
		{"attribute set", func(f *citype.MetamodelExportV2, _ *ImportTarget) { f.CITypes[0].AttributeSetKeys = []string{"nope"} },
			ConflictMissingReference, KindAttributeSet, "nope"},
		{"attribute definition", func(f *citype.MetamodelExportV2, _ *ImportTarget) {
			f.Templates[0].Versions[2].Items[0].AttributeKey = "nope"
		}, ConflictMissingReference, KindAttributeDefinition, "ci_type:app_x/nope"},
		{"template", func(f *citype.MetamodelExportV2, _ *ImportTarget) {
			f.Layouts[0].CITypeKey, f.Layouts[0].TemplateKey = "", "nope"
		}, ConflictMissingReference, KindTemplate, "nope"},
		{"rule", func(f *citype.MetamodelExportV2, _ *ImportTarget) {
			f.Rules = append(f.Rules, citype.ExportRuleV2{Key: "sys_rule", IsSystem: true})
		}, ConflictMissingReference, KindRule, "sys_rule"},
		{"role", func(_ *citype.MetamodelExportV2, tg *ImportTarget) { delete(tg.Roles, "Operators") },
			ConflictMissingReference, kindRole, "Operators"},
		{"ambiguous role", func(_ *citype.MetamodelExportV2, tg *ImportTarget) {
			tg.Roles["Operators"] = []string{"id-role-a", "id-custom-role-b"}
		}, ConflictMissingReference, kindRole, "Operators"},
		{"system key", func(_ *citype.MetamodelExportV2, tg *ImportTarget) {
			tg.Types["app_x"] = TargetRef{ID: "id-system-app_x", System: true}
		}, ConflictSystemKeyCollision, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, tg := importFile(), emptyTarget()
			tc.edit(f, tg)
			plan := planImport(f, tg)
			if !hasConflict(&plan.report, tc.code, tc.ref, tc.rkey) {
				t.Fatalf("want %s %s %s, got %+v", tc.code, tc.ref, tc.rkey, plan.report.Conflicts)
			}
			if plan.report.Unchanged {
				t.Error("a refused import is not unchanged")
			}
		})
	}
}

// fakeTarget and fakeStore stand in for the snapshot and the draft
// repository.
type fakeTarget struct{ target *ImportTarget }

func (f fakeTarget) ReadImportTarget(context.Context, string) (*ImportTarget, error) {
	return f.target, nil
}

type fakeStore struct {
	calls int
	items []ImportItem
}

func (s *fakeStore) CreateImportDraft(_ context.Context, orgID, _ string, items []ImportItem) (*Draft, error) {
	s.calls++
	s.items = items
	return &Draft{ID: "11111111-1111-4111-8111-111111111111", OrganizationID: orgID, Version: int64(1 + len(items))}, nil
}

// postImport posts body with the permissions perms (nil: all).
func postImport(h *ImportHandler, body string, perms []identity.Permission) *httptest.ResponseRecorder {
	if perms == nil {
		perms = identity.AllPermissions()
	}
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	ctx := identity.WithPermissions(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-b"}), perms)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metamodel/import", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func fileBody(t *testing.T, f *citype.MetamodelExportV2) string {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestImportHandler covers the answers of POST /api/v1/metamodel/import:
// 201 with draft_id, Location and report; 200 unchanged without a draft;
// 422 for conflicts and for format_version 1 without a draft; 400 for an
// unknown field; 413 above 10 MiB; 403 without the permission of an item;
// 503 without the database.
func TestImportHandler(t *testing.T) {
	store := &fakeStore{}
	h := NewImportHandler(fakeTarget{target: emptyTarget()}, store)
	body := fileBody(t, importFile())

	w := postImport(h, body, nil)
	if w.Code != http.StatusCreated || w.Header().Get("Location") != "/api/v1/metamodel/drafts/11111111-1111-4111-8111-111111111111" {
		t.Fatalf("import = %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	var res ImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.DraftID == nil || res.Report.Unchanged || len(res.Report.Items) == 0 {
		t.Fatalf("result = %+v (%v)", res, err)
	}
	if store.calls != 1 || len(store.items) != 14 {
		t.Fatalf("store got %d calls with %d items", store.calls, len(store.items))
	}

	same := NewImportHandler(fakeTarget{target: targetOf(importFile())}, store)
	if w = postImport(same, body, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unchanged":true`) ||
		!strings.Contains(w.Body.String(), `"draft_id":null`) || store.calls != 1 {
		t.Errorf("unchanged = %d %s (%d drafts)", w.Code, w.Body.String(), store.calls)
	}

	conflicting := importFile()
	conflicting.CITypes[1].ParentTypeKey = "nope"
	w = postImport(h, fileBody(t, conflicting), nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), ProblemImportConflict) ||
		!strings.Contains(w.Body.String(), `"missing-reference"`) || !strings.Contains(w.Body.String(), `"trace_id"`) || store.calls != 1 {
		t.Errorf("conflict = %d %s", w.Code, w.Body.String())
	}

	v1 := `{"format_version":1,"exported_at":"2026-01-01T00:00:00Z","types":[],"global_fields":[]}`
	if w = postImport(h, v1, nil); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), ProblemUnsupportedFormatVersion) {
		t.Errorf("format_version 1 = %d %s", w.Code, w.Body.String())
	}
	unknown := strings.Replace(body, `"format_version":2`, `"format_version":2,"unknown_field":true`, 1)
	if w = postImport(h, unknown, nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown_field") {
		t.Errorf("unknown field = %d %s", w.Code, w.Body.String())
	}
	large := `{"format_version":2,"organization_id":"` + strings.Repeat("x", MaxImportBody) + `"}`
	if w = postImport(h, large, nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("large body = %d", w.Code)
	}

	// Without metamodel:publish the template version cannot be published.
	var perms []identity.Permission
	for _, p := range identity.AllPermissions() {
		if p != identity.PermMetamodelPublish {
			perms = append(perms, p)
		}
	}
	if w = postImport(h, body, perms); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "metamodel:publish") || store.calls != 1 {
		t.Errorf("without metamodel:publish = %d %s", w.Code, w.Body.String())
	}

	if w = postImport(NewImportHandler(nil, nil), body, nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without the database = %d", w.Code)
	}
}

// TestImportRoutes covers the item references of the routes (E-112): an
// item whose target another item creates gets placeholders that the
// publication replaces by the created id and version number.
func TestImportRoutes(t *testing.T) {
	ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	c := &capture{Kind: KindTemplateVersion, Op: OpPublish, Spec: ItemKinds[KindTemplateVersion][OpPublish], TargetID: ids[1]}
	got := linkedRoute(c, planRef(0), ids)
	if want := "POST /api/v1/templates/{$item:" + ids[0] + "}/versions/{$version:" + ids[1] + "}/publish"; got != want {
		t.Fatalf("route = %s, want %s", got, want)
	}
	_, path, _ := strings.Cut(got, " ")
	resolved, err := resolveRoute(path, map[string]string{ids[0]: "tpl-1", ids[1]: "ver-1"}, map[string]string{ids[1]: "2"})
	if err != nil || resolved != "/api/v1/templates/tpl-1/versions/2/publish" {
		t.Errorf("resolved = %s (%v)", resolved, err)
	}
	if _, err = resolveRoute(path, map[string]string{}, map[string]string{}); err == nil {
		t.Error("an unresolved placeholder must fail")
	}
	c = &capture{Kind: KindCIType, Op: OpDeactivate, Spec: ItemKinds[KindCIType][OpDeactivate], TargetID: ids[0]}
	if got = linkedRoute(c, nil, ids); got != "POST /api/v1/ci-types/{$item:"+ids[0]+"}/deactivate" {
		t.Errorf("type route = %s", got)
	}
	m := materialize(map[string]any{"a": planRef(1), "l": []any{planRef(0), "x"}}, ids).(map[string]any)
	if m["a"].(map[string]any)["$item"] != ids[1] || m["l"].([]any)[0].(map[string]any)["$item"] != ids[0] || m["l"].([]any)[1] != "x" {
		t.Errorf("materialize = %+v", m)
	}
}

// TestImportPlanNewRuleNeedsNewVersion: the rules of a published template
// version are immutable, so a new rule of the current version (same items)
// becomes a new version with the rule, published after it.
func TestImportPlanNewRuleNeedsNewVersion(t *testing.T) {
	file := importFile()
	extra := *file.Rules[2].ExportRuleBody
	file.Rules = append(file.Rules, citype.ExportRuleV2{Key: "r_v3_new", ExportRuleBody: &extra})
	plan := planImport(file, targetOf(importFile()))
	want := []string{"template_version/create", "template_version/update", "rule/create", "template_version/publish"}
	if got := steps(plan.items); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if plan.items[2].Payload["key"] != "r_v3_new" || plan.items[2].Payload["template_version_id"] != planRef(0) {
		t.Errorf("rule = %+v", plan.items[2].Payload)
	}
	if action(&plan.report, KindRule, "r_v3") != ActionUnchanged {
		t.Errorf("the copied rule r_v3 = %q", action(&plan.report, KindRule, "r_v3"))
	}
}
