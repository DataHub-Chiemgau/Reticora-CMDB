package metamodel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// importModels and importRoles adapt the lifecycle and user repositories
// like the server does (router.go, lifecycleModelExport and roleExport).
type importModels struct{ repo *lifecycle.PGRepository }

func (a importModels) ExportLifecycleModels(ctx context.Context, orgID string) ([]citype.ExportLifecycleModelV2, error) {
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

type importRoles struct{ repo *user.PGRepository }

func (a importRoles) ExportRoles(ctx context.Context, orgID string) ([]citype.ExportRole, error) {
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

// importAPI serves the resource routes, the lifecycle models, the drafts
// with test run and publication, and the import behind the idempotency
// middleware, over PostgreSQL, for both organizations.
type importAPI struct {
	t     *testing.T
	f     *scopetest.Fixture
	mux   *chi.Mux
	jobs  *jobs.Service
	users map[string]string
	n     int
}

func newImportAPI(t *testing.T, f *scopetest.Fixture) *importAPI {
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	svc.Register(jobs.Kind{Name: metamodel.TestJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	catalog := citype.NewPGRepository(f.App)
	catalog.WithIndexScheduler(citype.NewDDLJob(f.App, svc))
	models, roles := importModels{repo: lifecycle.NewPGRepository(f.App)}, importRoles{repo: user.NewPGRepository(f.App)}
	drafts := metamodel.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(catalog).WithLifecycleModels(models).WithRoles(roles).RegisterRoutes(mux)
	metamodel.NewHandler(drafts).WithJobs(svc).WithPublish(catalog, nil).RegisterRoutes(mux)
	lifecycle.NewHandler(lifecycle.NewPGRepository(f.App), nil, nil).RegisterRoutes(mux)
	importer := metamodel.NewImportHandler(metamodel.NewImportTarget(catalog, models, roles,
		relationshiptype.NewPGRepository(f.App)), drafts)
	mux.With(middleware.IdempotencyWithStore(cache.NewMemoryStore())).Post("/api/v1/metamodel/import", importer.Import)
	return &importAPI{t: t, f: f, mux: mux, jobs: svc, users: map[string]string{
		f.OrgA: f.AppUser(t, f.OrgA, "wp722-admin-a"), f.OrgB: f.AppUser(t, f.OrgB, "wp722-admin-b")}}
}

// do runs a request of org with perms (nil: all) and extra headers.
func (a *importAPI) do(org string, perms []identity.Permission, method, path string, body []byte, header map[string]string) *httptest.ResponseRecorder {
	a.t.Helper()
	a.n++
	if perms == nil {
		perms = identity.AllPermissions()
	}
	uid := a.users[org]
	scope := database.OrgWideScope(org, uid)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: uid})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: uid, OrganizationID: org, Type: identity.PrincipalTypeUser})
	ctx = audit.WithRequestInfo(identity.WithPermissions(ctx, perms),
		audit.RequestInfo{RequestID: "wp722-" + strconv.Itoa(a.n), CorrelationID: "wp722-import", Source: audit.SourceUI})
	req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// must calls and fails unless the status is want; it decodes into out.
func (a *importAPI) must(want int, org, method, path, body string, out any) *httptest.ResponseRecorder {
	a.t.Helper()
	w := a.do(org, nil, method, path, []byte(body), nil)
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

func (a *importAPI) id(org, method, path, body string) string {
	a.t.Helper()
	var obj struct {
		ID string `json:"id"`
	}
	a.must(http.StatusCreated, org, method, path, body, &obj)
	return obj.ID
}

// export reads the export v2 of org.
func (a *importAPI) export(org string) []byte {
	a.t.Helper()
	return a.must(http.StatusOK, org, http.MethodGet, "/api/v1/ci-types/export?format_version=2", "", nil).Body.Bytes()
}

func (a *importAPI) importFile(org string, file []byte, header map[string]string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(org, nil, http.MethodPost, "/api/v1/metamodel/import", file, header)
}

func (a *importAPI) drafts(org string) int {
	a.t.Helper()
	var n int
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM metamodel_draft WHERE organization_id = $1`, org).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

// lifecycleModel creates the lifecycle model wp722-model per REST.
func (a *importAPI) lifecycleModel(org string) {
	a.t.Helper()
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/lifecycle-models", `{"key":"wp722-model","name":"WP-722 Model",
		"applies_to":"ci","states":[{"key":"planned","label":"Planned","is_initial":true},{"key":"active","label":"Active","sort_order":1}],
		"transitions":[{"key":"go-live","label":"Go live","from_state_key":"planned","to_state_key":"active"}]}`, nil)
}

// testAndWait runs the test of the draft (WP-721) and waits for its job.
func (a *importAPI) testAndWait(org, draftID string) {
	a.t.Helper()
	var started struct {
		JobID string `json:"job_id"`
	}
	a.must(http.StatusAccepted, org, http.MethodPost, "/api/v1/metamodel/drafts/"+draftID+"/test", "", &started)
	deadline := time.Now().Add(60 * time.Second)
	for {
		job, err := a.jobs.Get(a.f.OrgCtx(org), org, started.JobID)
		if err != nil {
			a.t.Fatal(err)
		}
		if job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning {
			if job.Status != jobs.StatusSucceeded {
				a.t.Fatalf("test job: %s %s", job.Status, job.Error)
			}
			return
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("test job %s still %s", started.JobID, job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// comparableExport strips what the roundtrip may change: exported_at,
// organization_id, the versions of the templates except the current one
// (without its number) with current_version, and the rules of the older
// template versions (the rules of the current version without the number).
func comparableExport(t *testing.T, raw []byte) *citype.MetamodelExportV2 {
	t.Helper()
	var doc citype.MetamodelExportV2
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.ExportedAt, doc.OrganizationID = time.Time{}, ""
	current := map[string]int{}
	for i := range doc.Templates {
		b := doc.Templates[i].ExportTemplateBody
		if b == nil {
			continue
		}
		var kept []citype.ExportTemplateVersionV2
		for _, v := range b.Versions {
			if b.CurrentVersion != nil && v.Version == *b.CurrentVersion {
				current[doc.Templates[i].Key] = v.Version
				v.Version, v.PublishedAt = 0, nil
				kept = append(kept, v)
			}
		}
		b.Versions, b.CurrentVersion = kept, nil
	}
	rules := doc.Rules[:0]
	for _, r := range doc.Rules {
		if r.ExportRuleBody != nil && r.TemplateVersion != nil {
			if v, ok := current[r.TemplateKey]; !ok || v != *r.TemplateVersion {
				continue
			}
			body := *r.ExportRuleBody
			body.TemplateVersion = nil
			r.ExportRuleBody = &body
		}
		rules = append(rules, r)
	}
	doc.Rules = rules
	return &doc
}

// sameSections compares two comparable exports section by section.
func sameSections(t *testing.T, a, b *citype.MetamodelExportV2) {
	t.Helper()
	for _, s := range []struct {
		name string
		x, y any
	}{
		{"ci_types", a.CITypes, b.CITypes}, {"attribute_definitions", a.AttributeDefinitions, b.AttributeDefinitions},
		{"attribute_sets", a.AttributeSets, b.AttributeSets}, {"templates", a.Templates, b.Templates},
		{"rules", a.Rules, b.Rules}, {"layouts", a.Layouts, b.Layouts}, {"lifecycle_models", a.LifecycleModels, b.LifecycleModels},
	} {
		rx, _ := json.Marshal(s.x)
		ry, _ := json.Marshal(s.y)
		if !bytes.Equal(rx, ry) {
			t.Errorf("%s differ after the roundtrip:\nA %s\nB %s", s.name, rx, ry)
		}
	}
}

// TestMetamodelImportRoundtripAgainstPostgres covers MET-54 (WP-722): the
// export v2 of org A (types with parent, lifecycle model, relationship type,
// set and a deactivated one, definitions of a type, a set and a global one
// with applies_to, a template with two versions and rules on both, a type
// rule, a layout with a role both organizations name alike) is imported
// into org B, whose lifecycle model was created per REST: 201 with a draft
// that the test run (WP-721) and the publication (WP-261) make productive;
// the export of org B then equals the one of org A except for exported_at,
// organization_id, the version numbers and the older template versions with
// their rules. Further: a second import into org A is 200 unchanged without
// draft; another data type and a missing lifecycle key are 422 without
// draft; a body above 10 MiB is 413, also with Idempotency-Key; a file
// above 1 MiB with Idempotency-Key is 201 and its repetition the same
// answer; a caller without citype:manage is 403; a change of a target type
// between import and publication is 409.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetamodelImportRoundtripAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e2")
	a := newImportAPI(t, f)
	bg := context.Background()
	orgA, orgB := f.OrgA, f.OrgB

	// 1. The metamodel of org A.
	a.lifecycleModel(orgA)
	var role string
	for _, org := range []string{orgA, orgB} {
		if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
			VALUES ($1, 'wp722-operators', '["ci:read"]') RETURNING id::text`, org).Scan(&role); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM custom_role WHERE organization_id = $1 AND name = 'wp722-operators'`, orgA).Scan(&role); err != nil {
		t.Fatal(err)
	}
	var modelID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM lifecycle_definition WHERE organization_id = $1 AND key = 'wp722-model'`, orgA).Scan(&modelID); err != nil {
		t.Fatal(err)
	}
	parent := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp722-parent","name":"wp722-parent","category":"software",
		"lifecycle_model_id":"`+modelID+`","allowed_relationship_types":["depends_on"]}`)
	child := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp722-child","name":"wp722-child","parent_type_id":"`+parent+`"}`)
	old := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp722-old","name":"wp722-old"}`)
	a.must(http.StatusOK, orgA, http.MethodPost, "/api/v1/ci-types/"+old+"/deactivate", "", nil)
	host := a.id(orgA, http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+parent+`","key":"wp722_host","data_type":"text"}`)
	owner := a.id(orgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"key":"wp722_owner","data_type":"text","applies_to":"types","applies_to_type_ids":["`+parent+`"]}`)
	set := a.id(orgA, http.MethodPost, "/api/v1/attribute-sets", `{"key":"wp722-set"}`)
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/attribute-definitions", `{"attribute_set_id":"`+set+`","key":"wp722_rack","data_type":"text"}`, nil)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-types/"+child+"/attribute-sets",
		`{"attribute_sets":[{"attribute_set_id":"`+set+`","position":0}]}`, nil)
	tpl := a.id(orgA, http.MethodPost, "/api/v1/templates", `{"key":"wp722-tpl","ci_type_id":"`+parent+`"}`)
	versions := "/api/v1/templates/" + tpl + "/versions/"
	a.must(http.StatusOK, orgA, http.MethodPut, versions+"1", `{"items":[{"attribute_definition_id":"`+host+`","position":0,"required_override":true}]}`, nil)
	var v1 struct {
		ID string `json:"id"`
	}
	a.must(http.StatusOK, orgA, http.MethodGet, versions+"1", "", &v1)
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/rules",
		`{"key":"wp722-old-rule","attribute_key":"wp722_host","kind":"readonly","template_version_id":"`+v1.ID+`"}`, nil)
	a.must(http.StatusOK, orgA, http.MethodPost, versions+"1/publish", "", nil)
	v2 := a.id(orgA, http.MethodPost, "/api/v1/templates/"+tpl+"/versions", "")
	a.must(http.StatusOK, orgA, http.MethodPut, versions+"2", `{"items":[{"attribute_definition_id":"`+host+`","position":0,"required_override":true},
		{"attribute_definition_id":"`+owner+`","position":1,"default_override":"ops"}]}`, nil)
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/rules",
		`{"key":"wp722-v2-rule","attribute_key":"wp722_owner","kind":"required","template_version_id":"`+v2+`"}`, nil)
	a.must(http.StatusOK, orgA, http.MethodPost, versions+"2/publish", "", nil)
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/rules",
		`{"key":"wp722-show","attribute_key":"wp722_host","kind":"readonly","ci_type_id":"`+parent+`"}`, nil)
	a.must(http.StatusCreated, orgA, http.MethodPost, "/api/v1/layouts", `{"key":"wp722-layout","ci_type_id":"`+parent+`","role_id":"`+role+`",
		"layout":{"sections":[{"key":"main","title":{"de":"Haupt","en":"Main"},"visible_when":["wp722-show"],"fields":[{"name":"wp722_host","width":6}]}]}}`, nil)
	fileA := a.export(orgA)

	// The second import into org A changes nothing.
	if w := a.importFile(orgA, fileA, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unchanged":true`) || a.drafts(orgA) != 0 {
		t.Fatalf("import into org A: %d %s (%d drafts)", w.Code, w.Body.String(), a.drafts(orgA))
	}

	// A lifecycle key org B does not know is 422 missing-reference.
	if w := a.importFile(orgB, fileA, nil); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"reference":"lifecycle_model","reference_key":"wp722-model"`) || a.drafts(orgB) != 0 {
		t.Fatalf("missing lifecycle model: %d %s", w.Code, w.Body.String())
	}

	// 2. The lifecycle model of org B per REST.
	a.lifecycleModel(orgB)

	// Without citype:manage the import is 403.
	var noManage []identity.Permission
	for _, p := range identity.AllPermissions() {
		if p != identity.PermCITypeManage {
			noManage = append(noManage, p)
		}
	}
	if w := a.do(orgB, noManage, http.MethodPost, "/api/v1/metamodel/import", fileA, nil); w.Code != http.StatusForbidden || a.drafts(orgB) != 0 {
		t.Fatalf("without citype:manage: %d %s", w.Code, w.Body.String())
	}

	// 3. The import into org B.
	w := a.importFile(orgB, fileA, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("import into org B: %d %s", w.Code, w.Body.String())
	}
	var res metamodel.ImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.DraftID == nil ||
		w.Header().Get("Location") != "/api/v1/metamodel/drafts/"+*res.DraftID {
		t.Fatalf("import result: %s (%v)", w.Body.String(), err)
	}
	var skipped []string
	for _, n := range res.Report.Notes {
		if n.Code == metamodel.NoteHistorySkipped {
			skipped = append(skipped, n.Kind+":"+n.Key)
		}
	}
	if !slices.Equal(skipped, []string{"template:wp722-tpl", "rule:wp722-tpl"}) {
		t.Errorf("history-skipped notes: %v", res.Report.Notes)
	}
	var audits int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		AND action IN ('metamodel_draft.created', 'metamodel_draft.item_added')`, orgB, *res.DraftID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	var draft metamodel.Draft
	a.must(http.StatusOK, orgB, http.MethodGet, "/api/v1/metamodel/drafts/"+*res.DraftID, "", &draft)
	if audits != 1+draft.ItemCount || draft.ItemCount == 0 {
		t.Errorf("audit entries of the draft: %d for %d items", audits, draft.ItemCount)
	}

	// 4. Test run and publication.
	a.testAndWait(orgB, *res.DraftID)
	a.must(http.StatusOK, orgB, http.MethodGet, "/api/v1/metamodel/drafts/"+*res.DraftID, "", &draft)
	if draft.Status != metamodel.StatusTested {
		t.Fatalf("draft after the test: %s %s", draft.Status, draft.TestReport)
	}
	a.must(http.StatusOK, orgB, http.MethodPost, "/api/v1/metamodel/drafts/"+*res.DraftID+"/publish", "", nil)

	// 5. The export of org B equals the one of org A.
	fileB := a.export(orgB)
	sameSections(t, comparableExport(t, fileA), comparableExport(t, fileB))

	// The import of the file into org B now changes nothing.
	if w = a.importFile(orgB, fileA, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unchanged":true`) {
		t.Errorf("second import into org B: %d %s", w.Code, w.Body.String())
	}

	// Another data type of the same attribute key is 422 without draft.
	drafts := a.drafts(orgB)
	changed := bytes.Replace(fileA, []byte(`"key":"wp722_host","scope":"type","ci_type_key":"wp722-parent","is_system":false,`+
		`"display_name":{"de":"wp722_host","en":"wp722_host"},"data_type":"text"`), []byte(`"key":"wp722_host","scope":"type",`+
		`"ci_type_key":"wp722-parent","is_system":false,"display_name":{"de":"wp722_host","en":"wp722_host"},"data_type":"integer"`), 1)
	if bytes.Equal(changed, fileA) {
		t.Fatalf("the definition wp722_host was not found in the export: %s", fileA)
	}
	if w = a.importFile(orgB, changed, nil); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), metamodel.ConflictDatatype) ||
		a.drafts(orgB) != drafts {
		t.Errorf("datatype conflict: %d %s", w.Code, w.Body.String())
	}

	// A body above 10 MiB is 413, with and without Idempotency-Key.
	huge := append([]byte(`{"format_version":2,"organization_id":"`), bytes.Repeat([]byte("x"), metamodel.MaxImportBody)...)
	huge = append(huge, `"}`...)
	for _, header := range []map[string]string{nil, {"Idempotency-Key": "wp722-huge"}} {
		if w = a.importFile(orgB, huge, header); w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("huge body (%v): %d", header, w.Code)
		}
	}

	// A file above 1 MiB with Idempotency-Key: 201, the repetition the same
	// answer without a second draft.
	var doc map[string]any
	if err := json.Unmarshal(fileA, &doc); err != nil {
		t.Fatal(err)
	}
	doc["ci_types"] = append(doc["ci_types"].([]any), map[string]any{"key": "wp722-big", "is_system": false, "name": "wp722-big",
		"display_name": map[string]any{"de": "big", "en": "big"}, "is_builtin": false, "is_active": true, "is_logical": false,
		"is_observable": true, "attribute_set_keys": []any{}, "capabilities": map[string]any{}, "allowed_relationship_types": []any{},
		"ui_schema": map[string]any{"pad": strings.Repeat("p", 1<<20+1024)}, "compliance_rules": []any{}, "discovery_mappings": []any{}})
	big, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	drafts = a.drafts(orgA)
	key := map[string]string{"Idempotency-Key": "wp722-big"}
	first := a.importFile(orgA, big, key)
	if first.Code != http.StatusCreated || len(big) <= 1<<20 {
		t.Fatalf("big file (%d bytes): %d %s", len(big), first.Code, first.Body.String())
	}
	second := a.importFile(orgA, big, key)
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() || a.drafts(orgA) != drafts+1 {
		t.Errorf("repetition: %d %s (%d drafts, want %d)", second.Code, second.Body.String(), a.drafts(orgA), drafts+1)
	}

	// A change of a target type between import and publication is 409.
	var changedDoc citype.MetamodelExportV2
	if err = json.Unmarshal(fileA, &changedDoc); err != nil {
		t.Fatal(err)
	}
	for i := range changedDoc.CITypes {
		if changedDoc.CITypes[i].Key == "wp722-parent" {
			changedDoc.CITypes[i].Description = "wp722 changed"
		}
	}
	changedFile, err := json.Marshal(&changedDoc)
	if err != nil {
		t.Fatal(err)
	}
	if w = a.importFile(orgB, changedFile, nil); w.Code != http.StatusCreated {
		t.Fatalf("import of the change: %d %s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.DraftID == nil {
		t.Fatal(w.Body.String())
	}
	var parentB string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id = $1 AND key = 'wp722-parent'`, orgB).Scan(&parentB); err != nil {
		t.Fatal(err)
	}
	a.must(http.StatusOK, orgB, http.MethodPatch, "/api/v1/ci-types/"+parentB, `{"icon":"wp722"}`, nil)
	if _, err = f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'tested' WHERE id = $1`, *res.DraftID); err != nil {
		t.Fatal(err)
	}
	if w = a.do(orgB, nil, http.MethodPost, "/api/v1/metamodel/drafts/"+*res.DraftID+"/publish", nil, nil); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "stale_items") {
		t.Errorf("publication after a change of the target: %d %s", w.Code, w.Body.String())
	}
}
