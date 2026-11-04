package citype_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// testLifecycleModels and testRoles adapt the lifecycle and user
// repositories like the server does (router.go).
type testLifecycleModels struct{ repo *lifecycle.PGRepository }

func (a testLifecycleModels) ExportLifecycleModels(ctx context.Context, orgID string) ([]citype.ExportLifecycleModelV2, error) {
	defs, _, err := a.repo.List(ctx, orgID, api.PaginationParams{Limit: api.MaxPageLimit})
	if err != nil {
		return nil, err
	}
	out := []citype.ExportLifecycleModelV2{}
	for i := range defs {
		d, getErr := a.repo.GetByID(ctx, orgID, defs[i].ID)
		if getErr != nil {
			return nil, getErr
		}
		body := &citype.ExportLifecycleModelBody{Name: d.Name, AppliesTo: d.AppliesTo, Description: d.Description}
		for _, s := range d.States {
			body.States = append(body.States, citype.ExportLifecycleStateV2{Key: s.Key, Label: s.Label, IsInitial: s.IsInitial,
				IsTerminal: s.IsTerminal, SortOrder: s.SortOrder, RequiredFields: s.RequiredFields})
		}
		for _, tr := range d.Transitions {
			body.Transitions = append(body.Transitions, citype.ExportLifecycleTransitionV2{Key: tr.Key, Label: tr.Label,
				FromStateKey: tr.FromStateKey, ToStateKey: tr.ToStateKey, RequiredFields: tr.RequiredFields, Validation: tr.Validation})
		}
		out = append(out, citype.ExportLifecycleModelV2{ID: d.ID, Key: d.Key, IsSystem: d.IsSystem, ExportLifecycleModelBody: body})
	}
	return out, nil
}

type testRoles struct{ repo *user.PGRepository }

func (a testRoles) ExportRoles(ctx context.Context, orgID string) ([]citype.ExportRole, error) {
	roles, _, err := a.repo.ListRoles(ctx, orgID, api.PaginationParams{Limit: api.MaxPageLimit})
	if err != nil {
		return nil, err
	}
	out := []citype.ExportRole{}
	for _, r := range roles {
		kind := citype.RoleKindCustomRole
		if r.IsBuiltin {
			kind = citype.RoleKindRole
		}
		out = append(out, citype.ExportRole{ID: r.ID, Name: r.Name, Kind: kind})
	}
	return out, nil
}

// uuidPattern finds a UUID anywhere in a document.
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// forbiddenExportFields are surrogates and personal data the export v2
// leaves out everywhere.
var forbiddenExportFields = []string{"id", "organization_id", "ci_type_id", "attribute_set_id", "template_id",
	"template_version_id", "role_id", "parent_type_id", "lifecycle_model_id", "lifecycle_definition_id",
	"cloned_from_id", "attribute_definition_id", "applies_to_type_ids", "published_by", "created_by",
	"created_at", "updated_at", "attribute_schema", "schema_version"}

// findForbidden reports the paths of the forbidden fields in v.
func findForbidden(path string, v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if slices.Contains(forbiddenExportFields, k) {
				*out = append(*out, path+"."+k)
			}
			findForbidden(path+"."+k, child, out)
		}
	case []any:
		for _, child := range x {
			findForbidden(path+"[]", child, out)
		}
	}
}

// exportedAt strips exported_at from a document.
var exportedAt = regexp.MustCompile(`"exported_at":"[^"]*"`)

// TestMetamodelExportV2AgainstPostgres covers MET-54 phase 2 (WP-262): the
// export v2 holds every part of the metamodel by keys (types with parent,
// lifecycle model and sets, definitions with applies_to and computed, sets,
// templates with versions and items, rules on a template version, layouts
// with role and visible_when, lifecycle models), no ids, surrogates or
// version counters, system parts only as references and nothing of org B,
// sorted as specified and byte-identical on a second export except for
// exported_at. v1 stays the default, format_version=3 is 400, the memory
// handler answers 503. RunInSnapshot reads one snapshot (a change
// committed meanwhile stays invisible) and refuses writes (READ ONLY).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetamodelExportV2AgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	mux := chi.NewRouter()
	citype.NewHandler(a.repo).WithLifecycleModels(testLifecycleModels{repo: lifecycle.NewPGRepository(f.App)}).
		WithRoles(testRoles{repo: user.NewPGRepository(f.App)}).RegisterRoutes(mux)
	get := func(org, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(a.ctx(org))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// The metamodel of org A.
	model := &lifecycle.Definition{OrganizationID: f.OrgA, Key: "wp262-model", Name: "WP-262 Model", AppliesTo: "ci",
		States: []lifecycle.State{{Key: "planned", Label: "Planned", IsInitial: true, SortOrder: 0},
			{Key: "active", Label: "Active", SortOrder: 1, RequiredFields: []string{"wp262_host"}}},
		Transitions: []lifecycle.Transition{{Key: "go-live", Label: "Go live", FromStateKey: "planned", ToStateKey: "active"}}}
	if err := lifecycle.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), model); err != nil {
		t.Fatalf("create lifecycle model: %v", err)
	}
	var parent citype.Type
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/ci-types",
		`{"key":"wp262-parent","name":"wp262-parent","lifecycle_model_id":"`+model.ID+`"}`, &parent)
	child := a.childType(f.OrgA, "wp262-child", parent.ID)
	host := a.definition(f.OrgA, `{"ci_type_id":"`+parent.ID+`","key":"wp262_host","data_type":"text"}`)
	a.definition(f.OrgA, `{"ci_type_id":"`+parent.ID+`","key":"wp262_label","data_type":"text",
		"computed":{"op":"upper","args":[{"op":"field","field":"wp262_host"}]}}`)
	global := a.definition(f.OrgA, `{"key":"wp262_owner","data_type":"text","applies_to":"types","applies_to_type_ids":["`+parent.ID+`"]}`)
	set := a.createSet(f.OrgA, "wp262-set")
	a.mustSetAttribute(f.OrgA, set.ID, `{"key":"wp262_rack","data_type":"text"}`)
	a.putSets(f.OrgA, child.ID, set.ID)
	tpl := a.createTemplate(f.OrgA, parent.ID, "wp262-tpl")
	a.must(http.StatusOK, f.OrgA, http.MethodPut, versionPath(tpl.ID, 1), `{"items":[`+
		`{"attribute_definition_id":"`+global.ID+`","position":1,"default_override":"ops"},`+
		`{"attribute_definition_id":"`+host.ID+`","position":0,"required_override":true}]}`, nil)
	v1 := a.templateVersion(f.OrgA, tpl.ID, 1)
	a.createRule(f.OrgA, `{"key":"wp262-tpl-rule","attribute_key":"wp262_host","kind":"readonly","template_version_id":"`+v1.ID+`"}`)
	a.must(http.StatusOK, f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", "", nil)
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/templates/"+tpl.ID+"/versions", "", nil)
	a.createRule(f.OrgA, `{"key":"wp262-show","attribute_key":"wp262_host","kind":"readonly","ci_type_id":"`+parent.ID+`"}`)
	var customRole string
	if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
		VALUES ($1, 'wp262-reader', '["ci:read"]') RETURNING id::text`, f.OrgA).Scan(&customRole); err != nil {
		t.Fatal(err)
	}
	a.createLayout(f.OrgA, layoutBody("wp262-layout", `"ci_type_id":"`+parent.ID+`"`, customRole,
		`{"key":"main","title":{"de":"Haupt","en":"Main"},"visible_when":["wp262-show"],"fields":[{"name":"wp262_host","width":6}]}`))
	foreign := a.createType(f.OrgB, "wp262-foreign")
	a.definition(f.OrgB, `{"ci_type_id":"`+foreign.ID+`","key":"wp262_foreign","data_type":"text"}`)

	// The export v2.
	w := get(f.OrgA, "/api/v1/ci-types/export?format_version=2")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export v2: %d %s %s", w.Code, w.Header(), w.Body.String())
	}
	body := w.Body.Bytes()
	var doc citype.MetamodelExportV2
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.FormatVersion != 2 || doc.OrganizationID != f.OrgA || doc.OmittedSections == nil || len(doc.OmittedSections) != 0 {
		t.Fatalf("export v2 header: %d %s %v", doc.FormatVersion, doc.OrganizationID, doc.OmittedSections)
	}

	types := map[string]citype.ExportTypeV2{}
	for _, typ := range doc.CITypes {
		if !typ.IsSystem {
			types[typ.Key] = typ
		}
	}
	p, c := types["wp262-parent"], types["wp262-child"]
	if p.ExportTypeBody == nil || c.ExportTypeBody == nil || p.LifecycleModelKey != "wp262-model" || p.Name != "wp262-parent" ||
		c.ParentTypeKey != "wp262-parent" || !slices.Equal(c.AttributeSetKeys, []string{"wp262-set"}) ||
		p.AttributeSetKeys == nil || len(p.AttributeSetKeys) != 0 {
		t.Errorf("types: parent %+v, child %+v", p.ExportTypeBody, c.ExportTypeBody)
	}
	if _, ok := types["wp262-foreign"]; ok {
		t.Error("the type of org B is exported")
	}

	defs := map[string]citype.ExportAttributeV2{}
	for _, d := range doc.AttributeDefinitions {
		if !d.IsSystem {
			defs[d.Scope+"/"+d.CITypeKey+d.AttributeSetKey+"/"+d.Key] = d
		}
	}
	if d := defs["global//wp262_owner"]; d.ExportAttributeBody == nil || d.AppliesTo != "types" ||
		!slices.Equal(d.AppliesToTypeKeys, []string{"wp262-parent"}) {
		t.Errorf("global definition: %+v", d.ExportAttributeBody)
	}
	if d := defs["type/wp262-parent/wp262_label"]; d.ExportAttributeBody == nil || d.Computed == nil || d.DataType != "text" {
		t.Errorf("computed definition: %+v", d.ExportAttributeBody)
	}
	if d := defs["set/wp262-set/wp262_rack"]; d.ExportAttributeBody == nil || d.CITypeKey != "" {
		t.Errorf("set attribute: %+v", d)
	}
	if _, ok := defs["type/wp262-foreign/wp262_foreign"]; ok {
		t.Error("the definition of org B is exported")
	}

	sets := map[string]citype.ExportAttributeSetV2{}
	for _, s := range doc.AttributeSets {
		sets[s.Key] = s
	}
	if s := sets["wp262-set"]; s.ExportAttributeSetBody == nil || !s.IsActive || s.DisplayName["en"] != "Set wp262-set" {
		t.Errorf("set: %+v", s)
	}

	var tplOut *citype.ExportTemplateV2
	for i := range doc.Templates {
		if doc.Templates[i].Key == "wp262-tpl" {
			tplOut = &doc.Templates[i]
		}
	}
	if tplOut == nil || tplOut.ExportTemplateBody == nil || tplOut.CITypeKey != "wp262-parent" || tplOut.CurrentVersion == nil ||
		*tplOut.CurrentVersion != 1 || len(tplOut.Versions) != 2 {
		t.Fatalf("template: %+v", tplOut)
	}
	first := tplOut.Versions[0]
	if first.Version != 1 || first.Status != citype.TemplateVersionPublished || first.PublishedAt == nil || len(first.Items) != 2 ||
		first.Items[0].AttributeKey != "wp262_host" || first.Items[0].CITypeKey != "wp262-parent" ||
		first.Items[0].RequiredOverride == nil || first.Items[1].AttributeKey != "wp262_owner" || first.Items[1].CITypeKey != "" ||
		string(first.Items[1].DefaultOverride) != `"ops"` || tplOut.Versions[1].Version != 2 ||
		tplOut.Versions[1].Status != citype.TemplateVersionDraft {
		t.Errorf("template versions: %+v", tplOut.Versions)
	}

	var bindings []string
	for _, r := range doc.Rules {
		if r.IsSystem || r.ExportRuleBody == nil || !strings.HasPrefix(r.Key, "wp262") {
			continue
		}
		version := 0
		if r.TemplateVersion != nil {
			version = *r.TemplateVersion
		}
		bindings = append(bindings, r.Key+"@"+r.CITypeKey+r.AttributeSetKey+r.TemplateKey+":"+string(rune('0'+version)))
	}
	// The rule of version 1 is copied into version 2 (WP-716).
	if want := []string{"wp262-show@wp262-parent:0", "wp262-tpl-rule@wp262-tpl:1", "wp262-tpl-rule@wp262-tpl:2"}; !slices.Equal(bindings, want) {
		t.Errorf("rules: %v, want %v", bindings, want)
	}

	if len(doc.Layouts) != 1 {
		t.Fatalf("layouts: %+v", doc.Layouts)
	}
	l := doc.Layouts[0]
	if l.Key != "wp262-layout" || l.CITypeKey != "wp262-parent" || l.TemplateKey != "" || l.RoleName != "wp262-reader" ||
		l.RoleKind != citype.RoleKindCustomRole || len(l.Layout.Sections) != 1 ||
		!slices.Equal(l.Layout.Sections[0].VisibleWhen, []string{"wp262-show"}) || l.Layout.Sections[0].Fields[0].Name != "wp262_host" {
		t.Errorf("layout: %+v", l)
	}

	var modelOut *citype.ExportLifecycleModelV2
	systemModels := 0
	for i := range doc.LifecycleModels {
		switch m := &doc.LifecycleModels[i]; {
		case m.Key == "wp262-model":
			modelOut = m
		case m.IsSystem:
			systemModels++
		}
	}
	if modelOut == nil || modelOut.ExportLifecycleModelBody == nil || modelOut.AppliesTo != "ci" || len(modelOut.States) != 2 ||
		modelOut.States[0].Key != "planned" || len(modelOut.Transitions) != 1 || modelOut.Transitions[0].FromStateKey != "planned" ||
		modelOut.Transitions[0].ToStateKey != "active" || systemModels == 0 {
		t.Fatalf("lifecycle models: %+v (%d system)", modelOut, systemModels)
	}

	// No ids, surrogates, version counters or UUIDs; system parts only as
	// references.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "organization_id")
	var forbidden []string
	findForbidden("", raw, &forbidden)
	for _, item := range raw["ci_types"].([]any) {
		for _, k := range []string{"fields", "required_fields", "version"} {
			if _, ok := item.(map[string]any)[k]; ok {
				forbidden = append(forbidden, "ci_types[]."+k)
			}
		}
	}
	for _, section := range []string{"rules", "layouts", "lifecycle_models"} {
		for _, item := range raw[section].([]any) {
			if _, ok := item.(map[string]any)["version"]; ok {
				forbidden = append(forbidden, section+"[].version")
			}
		}
	}
	if len(forbidden) > 0 {
		t.Errorf("forbidden fields in the export: %v", forbidden)
	}
	if rest, _ := json.Marshal(raw); uuidPattern.Match(rest) {
		t.Errorf("a UUID in the export: %s", uuidPattern.Find(rest))
	}
	systemFields := map[string][]string{
		"ci_types": {"key", "is_system"}, "attribute_sets": {"key", "is_system"}, "templates": {"key", "is_system"},
		"rules": {"key", "is_system"}, "lifecycle_models": {"key", "is_system"},
		"attribute_definitions": {"key", "is_system", "scope", "ci_type_key", "attribute_set_key"},
	}
	systemTypes := 0
	for section, allowed := range systemFields {
		for _, item := range raw[section].([]any) {
			m := item.(map[string]any)
			if m["is_system"] != true {
				continue
			}
			if section == "ci_types" {
				systemTypes++
			}
			for k := range m {
				if !slices.Contains(allowed, k) {
					t.Errorf("%s: system part %v carries %s", section, m["key"], k)
				}
			}
		}
	}
	if systemTypes == 0 {
		t.Error("no system type as a reference")
	}

	// Sorting.
	keysOf := func(n int, key func(i int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = key(i)
		}
		return out
	}
	for name, keys := range map[string][]string{
		"ci_types":         keysOf(len(doc.CITypes), func(i int) string { return doc.CITypes[i].Key }),
		"attribute_sets":   keysOf(len(doc.AttributeSets), func(i int) string { return doc.AttributeSets[i].Key }),
		"templates":        keysOf(len(doc.Templates), func(i int) string { return doc.Templates[i].Key }),
		"layouts":          keysOf(len(doc.Layouts), func(i int) string { return doc.Layouts[i].Key }),
		"lifecycle_models": keysOf(len(doc.LifecycleModels), func(i int) string { return doc.LifecycleModels[i].Key }),
	} {
		if !slices.IsSorted(keys) {
			t.Errorf("%s not sorted by key: %v", name, keys)
		}
	}
	if !slices.IsSortedFunc(doc.AttributeDefinitions, func(x, y citype.ExportAttributeV2) int {
		return cmp.Or(cmp.Compare(x.Scope, y.Scope), cmp.Compare(x.CITypeKey, y.CITypeKey),
			cmp.Compare(x.AttributeSetKey, y.AttributeSetKey), cmp.Compare(x.Key, y.Key))
	}) {
		t.Error("attribute definitions not sorted by scope, owner and key")
	}
	for _, s := range modelOut.States {
		if s.RequiredFields == nil {
			t.Errorf("state %s without required_fields", s.Key)
		}
	}

	// A second export of the same state is byte-identical except for
	// exported_at.
	again := get(f.OrgA, "/api/v1/ci-types/export?format_version=2")
	if !bytes.Equal(exportedAt.ReplaceAll(body, nil), exportedAt.ReplaceAll(again.Body.Bytes(), nil)) {
		t.Error("a second export v2 differs")
	}

	// v1 stays the default; another version is 400; memory is 503.
	v1Default, v1Explicit := get(f.OrgA, "/api/v1/ci-types/export"), get(f.OrgA, "/api/v1/ci-types/export?format_version=1")
	if v1Default.Code != http.StatusOK || v1Explicit.Code != http.StatusOK ||
		!bytes.Equal(exportedAt.ReplaceAll(v1Default.Body.Bytes(), nil), exportedAt.ReplaceAll(v1Explicit.Body.Bytes(), nil)) {
		t.Errorf("v1: %d, %d", v1Default.Code, v1Explicit.Code)
	}
	var v1Doc citype.MetamodelExport
	if err := json.Unmarshal(v1Default.Body.Bytes(), &v1Doc); err != nil || v1Doc.FormatVersion != 1 || len(v1Doc.CITypes) == 0 ||
		v1Doc.CITypes[0].ID == "" {
		t.Errorf("v1 default: %v %d", err, v1Doc.FormatVersion)
	}
	for _, q := range []string{"3", "0", "two"} {
		if w = get(f.OrgA, "/api/v1/ci-types/export?format_version="+q); w.Code != http.StatusBadRequest {
			t.Errorf("format_version=%s: %d", q, w.Code)
		}
	}
	memMux := chi.NewRouter()
	citype.NewHandler(citype.NewMemoryRepository()).RegisterRoutes(memMux)
	memReq := httptest.NewRequest(http.MethodGet, "/api/v1/ci-types/export?format_version=2", nil).
		WithContext(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: a.user}))
	memW := httptest.NewRecorder()
	memMux.ServeHTTP(memW, memReq)
	if memW.Code != http.StatusServiceUnavailable {
		t.Errorf("memory handler v2: %d %s", memW.Code, memW.Body.String())
	}
	// Without the readers of lifecycle models and roles v2 is 503 as well.
	if w = a.call(f.OrgA, http.MethodGet, "/api/v1/ci-types/export?format_version=2", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("v2 without readers: %d", w.Code)
	}

	// One snapshot: a type change committed in a second connection after
	// the snapshot began stays invisible in it.
	err := a.repo.RunInSnapshot(a.ctx(f.OrgA), f.OrgA, func(ctx context.Context) error {
		if !database.InTx(ctx) {
			t.Error("RunInSnapshot without a context transaction")
		}
		before, _, err := a.repo.List(ctx, f.OrgA, citype.FilterParams{Search: "wp262-child"}, api.PaginationParams{Limit: 10})
		if err != nil {
			return err
		}
		if _, err = f.Admin.Exec(bg, `UPDATE ci_type SET name = 'wp262-renamed' WHERE id = $1`, child.ID); err != nil {
			return err
		}
		after, _, err := a.repo.List(ctx, f.OrgA, citype.FilterParams{Search: "wp262"}, api.PaginationParams{Limit: 10})
		if err != nil {
			return err
		}
		for _, typ := range after {
			if typ.ID == child.ID && typ.Name != "wp262-child" {
				t.Errorf("the snapshot sees the later change: %s", typ.Name)
			}
		}
		if len(before) != 1 {
			t.Errorf("snapshot read: %d types", len(before))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if typ := a.typeOf(f.OrgA, child.ID); typ.Name != "wp262-renamed" {
		t.Errorf("after the snapshot: %s", typ.Name)
	}

	// The snapshot is READ ONLY.
	err = a.repo.RunInSnapshot(a.ctx(f.OrgA), f.OrgA, func(ctx context.Context) error {
		_, writeErr := a.repo.Update(ctx, f.OrgA, child.ID, &citype.UpdateTypeRequest{Name: ptr("wp262-written")})
		return writeErr
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Errorf("write in the snapshot: %v, want read_only_sql_transaction", err)
	}
	if typ := a.typeOf(f.OrgA, child.ID); typ.Name != "wp262-renamed" {
		t.Errorf("the write in the snapshot was stored: %s", typ.Name)
	}
}

func ptr[T any](v T) *T { return &v }
