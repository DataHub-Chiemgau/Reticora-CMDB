package ci_test

// The delegating actions of POST /cis/bulk against PostgreSQL (BLK-02,
// E-132; WP-780), wired as the server does (server.CIBulkDelegates):
//
//   - add_relationship / remove_relationship: an edge per CI in both
//     directions with history (relationship_change), an existing edge 409,
//     a target outside the scope 422 on target_ci_id, a projected type 422
//     on params.rel_type without a change, a discovery edge 409, a missing
//     edge 404; ci:write without relationship:write is 403 without a change.
//   - lifecycle_transition: version + 1, entity_change and the event
//     lifecycle.changed; a failed guard 422 lifecycle-guard with fields, a
//     missing edge invalid-transition, a CI without model 422, another
//     from_state 409; without lifecycle:transition 403.
//   - change_template: a migration keeps the override and reports the
//     findings, the same version is ok without a change, replace_template_id
//     takes the same position, a schema violation and a template of another
//     CI type are 422 for the id, an unpublished to_version 422 on
//     params.to_version without a change; 100 ids run as the job ci.bulk.
//   - export: exactly one export job with filters.ids and the scope
//     snapshot, 202 with Location /api/v1/exports/{id} for three ids; the
//     file holds exactly the visible, not deleted CIs; without export:run
//     403, without export_csv 403 feature-not-entitled, datev 422.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run 'TestBulkDelegate' ./internal/ci/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// toggledFeatures is the entitlement port of the test: export_csv on or
// off.
type toggledFeatures struct{ off atomic.Bool }

func (f *toggledFeatures) IsEnabled(context.Context, string, string) bool { return !f.off.Load() }

// delegateEnv is the CI handler with the delegating actions on PostgreSQL.
type delegateEnv struct {
	t        *testing.T
	f        *scopetest.Fixture
	mux      *chi.Mux
	jobSvc   *jobs.Service
	user     string
	typeID   string
	types    *citype.PGRepository
	features *toggledFeatures
	worker   *export.JobWorker
	blobs    blob.Store
	// full is a principal of client 1 with every right of the actions,
	// writer one with ci:write alone.
	full, writer context.Context
}

func newDelegateEnv(t *testing.T) *delegateEnv {
	t.Helper()
	f := scopetest.Seed(t, "d2")
	bg := context.Background()
	e := &delegateEnv{t: t, f: f, user: f.AppUser(t, f.OrgA, "bulk-delegate"), features: &toggledFeatures{}}
	scope := database.OrgWideScope(f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(f.Client1)
	principal := func(perms ...identity.Permission) context.Context {
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user, ClientID: f.Client1})
		ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp780", CorrelationID: "wp780", Source: audit.SourceUI})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: e.user, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: append([]identity.Permission{identity.PermJobRead, identity.PermCIRead,
				identity.PermCIWrite}, perms...)})
	}
	e.full = principal(identity.PermRelationshipWrite, identity.PermLifecycleTransition, identity.PermExportRun)
	e.writer = principal()

	e.types = citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp780-dev", Name: "wp780-dev", IsObservable: true}
	if err := e.types.Create(e.full, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	e.typeID = typ.ID

	e.jobSvc = jobs.NewService(jobs.NewPGRepository(f.App))
	e.jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	pg := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	repo := ci.NewIndexingRepository(pg, nopIndexer{})

	rels := relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	relHandler := relationship.NewHandler(rels, relationshiptype.NewPGRepository(f.App))
	lcRepo := lifecycle.NewPGRepository(f.App)
	lcService := lifecycle.NewService(lcRepo, lifecycle.NewPGStateStore(f.App), lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	resolver := lifecycle.NewResolver(f.App)
	exportJobs := export.NewPGJobRepository(f.App)
	e.blobs = blob.NewFileStore(t.TempDir())
	e.worker = export.NewJobWorker(exportJobs, repo, e.blobs)
	delegates := server.CIBulkDelegates(relHandler, rels, lcService, resolver, export.NewJobHandler(exportJobs, e.worker, e.blobs), nil)
	delegates.Entitlements = e.features

	svc := ci.NewService(repo).WithFieldResolver(e.types).WithBulkJobs(e.jobSvc).WithCreatorPolicy(phase1CreatorPolicy).
		WithBulkDelegates(delegates)
	e.mux = chi.NewRouter()
	e.mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(e.mux)
	jobs.NewAPIHandler(e.jobSvc).WithKind(ci.BulkJobKind, identity.PermCIWrite, nil).RegisterRoutes(e.mux)
	lifecycle.NewHandler(lcRepo, lcService, resolver).RegisterRoutes(e.mux)
	return e
}

func (e *delegateEnv) do(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	case nil:
	default:
		raw, _ = json.Marshal(b)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// bulk posts a bulk of the full principal below 100 ids and returns the
// report.
func (e *delegateEnv) bulk(action string, ids []string, params map[string]any) ci.BulkResult {
	e.t.Helper()
	w := e.do(e.full, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": action, "ids": ids, "params": params})
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		e.t.Fatalf("bulk %s: %d %s, want 200 with the report", action, w.Code, w.Body.String())
	}
	return res
}

// refused posts a bulk as ctx and checks the status and problem of the
// whole request.
func (e *delegateEnv) refused(ctx context.Context, body map[string]any, code int, field, problemType string) {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", body)
	var p struct {
		Type   string `json:"type"`
		Field  string `json:"field"`
		Action string `json:"action"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != code || (field != "" && p.Field != field) || (problemType != "" && p.Type != problemType) {
		e.t.Fatalf("bulk %v: %d %s, want %d on %q of type %q", body["action"], w.Code, w.Body.String(), code, field, problemType)
	}
}

// reportOf returns the report of id.
func reportOf(t *testing.T, res *ci.BulkResult, id string) ci.BulkItemResult {
	t.Helper()
	for _, it := range res.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no report of %s in %+v", id, res.Items)
	return ci.BulkItemResult{}
}

// wantItem checks the outcome of id: code, problem type (empty: any) and
// the fields (nil: any).
func wantItem(t *testing.T, res *ci.BulkResult, id string, code int, typ string, fields ...string) ci.BulkItemResult {
	t.Helper()
	it := reportOf(t, res, id)
	if it.Code != code || (typ != "" && it.Type != typ) || (fields != nil && !slices.Equal(it.Fields, fields)) {
		t.Fatalf("report of %s: %+v, want %d %s %v", id, it, code, typ, fields)
	}
	return it
}

func (e *delegateEnv) count(query string, args ...any) int {
	e.t.Helper()
	return countRows(e.t, e.f, query, args...)
}

// ci inserts a CI of the test type in client 1 and returns its id.
func (e *delegateEnv) ci(name string) string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.typeID, name).Scan(&id); err != nil {
		e.t.Fatalf("insert ci %s: %v", name, err)
	}
	return id
}

func (e *delegateEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// TestBulkDelegateRelationships covers add_relationship and
// remove_relationship.
func TestBulkDelegateRelationships(t *testing.T) {
	e := newDelegateEnv(t)
	r1, r2, target := e.ci("wp780-r1"), e.ci("wp780-r2"), e.ci("wp780-target")
	foreign := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp780-foreign")
	edges := func(source, target string) int {
		return e.count(`SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'depends_on'`,
			source, target)
	}
	total := func() int {
		return e.count(`SELECT count(*) FROM ci_relationship WHERE organization_id = $1`, e.f.OrgA)
	}

	// Without relationship:write the whole request is 403, before a
	// change; a projected type is 422 on params.rel_type.
	body := map[string]any{"action": ci.BulkAddRelationship, "ids": []string{r1, r2},
		"params": map[string]any{"rel_type": "depends_on", "target_ci_id": target}}
	e.refused(e.writer, body, http.StatusForbidden, "", api.ProblemTypeForbidden)
	e.refused(e.full, map[string]any{"action": ci.BulkAddRelationship, "ids": []string{r1},
		"params": map[string]any{"rel_type": "contains", "target_ci_id": target}}, http.StatusUnprocessableEntity, "params.rel_type", "")
	if n := total(); n != 0 {
		t.Fatalf("%d edges after the refused requests, want 0", n)
	}

	// An edge per CI, outgoing and incoming; the manual edge is verified
	// and has its history.
	res := e.bulk(ci.BulkAddRelationship, []string{r1, r2}, map[string]any{"rel_type": "depends_on", "target_ci_id": target})
	if res.Succeeded != 2 || edges(r1, target) != 1 || edges(r2, target) != 1 {
		t.Fatalf("outgoing: %+v, edges %d %d", res, edges(r1, target), edges(r2, target))
	}
	res = e.bulk(ci.BulkAddRelationship, []string{r1}, map[string]any{"rel_type": "depends_on", "target_ci_id": target, "direction": "incoming"})
	if res.Succeeded != 1 || edges(target, r1) != 1 {
		t.Fatalf("incoming: %+v, edges %d", res, edges(target, r1))
	}
	if n := e.count(`SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND source = 'manual'
		AND verification_state = 'verified'`, e.f.OrgA); n != 3 {
		t.Errorf("%d verified manual edges, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM relationship_change WHERE organization_id = $1`, e.f.OrgA); n != 3 {
		t.Errorf("%d relationship_change rows, want 3", n)
	}

	// The same edge again is 409, a target outside the scope 422 on
	// target_ci_id for every id.
	res = e.bulk(ci.BulkAddRelationship, []string{r1}, map[string]any{"rel_type": "depends_on", "target_ci_id": target})
	wantItem(t, &res, r1, http.StatusConflict, "")
	res = e.bulk(ci.BulkAddRelationship, []string{r1, r2}, map[string]any{"rel_type": "depends_on", "target_ci_id": foreign})
	wantItem(t, &res, r1, http.StatusUnprocessableEntity, "", "target_ci_id")
	wantItem(t, &res, r2, http.StatusUnprocessableEntity, "", "target_ci_id")
	// A CI outside the scope is 404.
	res = e.bulk(ci.BulkAddRelationship, []string{foreign}, map[string]any{"rel_type": "depends_on", "target_ci_id": target})
	wantItem(t, &res, foreign, http.StatusNotFound, "")
	if n := total(); n != 3 {
		t.Fatalf("%d edges, want 3", n)
	}

	// remove_relationship: the edge goes, a missing one is 404, a discovery
	// edge 409 and stays.
	e.exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		VALUES ($1, $2, $3, 'depends_on', 'discovery')`, e.f.OrgA, target, r2)
	res = e.bulk(ci.BulkRemoveRelationship, []string{r1, r2}, map[string]any{"rel_type": "depends_on", "target_ci_id": target})
	if res.Succeeded != 2 || edges(r1, target) != 0 || edges(r2, target) != 0 {
		t.Fatalf("remove: %+v", res)
	}
	res = e.bulk(ci.BulkRemoveRelationship, []string{r1}, map[string]any{"rel_type": "depends_on", "target_ci_id": target})
	wantItem(t, &res, r1, http.StatusNotFound, "")
	res = e.bulk(ci.BulkRemoveRelationship, []string{r1, r2}, map[string]any{"rel_type": "depends_on", "target_ci_id": target,
		"direction": "incoming"})
	wantItem(t, &res, r1, http.StatusOK, "")
	wantItem(t, &res, r2, http.StatusConflict, "")
	if edges(target, r2) != 1 || edges(target, r1) != 0 {
		t.Errorf("after the incoming removal: discovery edge %d (want 1), manual %d (want 0)", edges(target, r2), edges(target, r1))
	}
}

// TestBulkDelegateLifecycle covers lifecycle_transition.
func TestBulkDelegateLifecycle(t *testing.T) {
	e := newDelegateEnv(t)
	w := e.do(e.full, http.MethodPost, "/api/v1/lifecycle-models", `{"key":"wp780_service","name":"Service","applies_to":"ci","states":[
		{"key":"draft","label":"Draft","is_initial":true,"sort_order":10},
		{"key":"production","label":"Production","sort_order":20,"required_fields":["owner"]},
		{"key":"retired","label":"Retired","is_terminal":true,"sort_order":30}],
		"transitions":[{"from_state_key":"draft","to_state_key":"production","key":"go_live",
			"guard":{"fields":{"version":{"op":"all","rules":[{"op":"required"},{"op":"pattern","pattern":"^v[0-9]+$"}]}}}},
		{"from_state_key":"","to_state_key":"retired","key":"retire"}]}`)
	var model lifecycle.Definition
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &model) != nil {
		t.Fatalf("create model: %d %s", w.Code, w.Body.String())
	}
	var serviceType string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
		VALUES ($1, 'wp780-service', 'WP-780 service', $2) RETURNING id::text`, e.f.OrgA, model.ID).Scan(&serviceType); err != nil {
		t.Fatal(err)
	}
	l1, l2, l3, plain := e.ci("wp780-l1"), e.ci("wp780-l2"), e.ci("wp780-l3"), e.ci("wp780-plain")
	e.exec(`UPDATE ci SET ci_type_id = $1 WHERE id = ANY($2::uuid[])`, serviceType, []string{l1, l2, l3})
	state := func(id string) (string, int) {
		t.Helper()
		var s string
		var v int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(lifecycle_state, ''), version FROM ci WHERE id = $1`, id).
			Scan(&s, &v); err != nil {
			t.Fatal(err)
		}
		return s, v
	}
	trailOf := func(id string) (changes, events int) {
		return e.count(`SELECT count(*) FROM entity_change WHERE entity_id = $1::uuid AND change_type = 'lifecycle_transition'`, id),
			e.count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'lifecycle.changed'`, id)
	}

	// Without lifecycle:transition 403 before any change.
	e.refused(e.writer, map[string]any{"action": ci.BulkLifecycleTransition, "ids": []string{l1},
		"params": map[string]any{"to_state": "draft"}}, http.StatusForbidden, "", api.ProblemTypeForbidden)
	if s, _ := state(l1); s != "" {
		t.Fatalf("state %q after a refused request", s)
	}

	// Into the initial state: version + 1, history and event per CI; a CI
	// without model is 422 on to_state.
	_, before := state(l1)
	res := e.bulk(ci.BulkLifecycleTransition, []string{l1, l2, plain}, map[string]any{"to_state": "draft", "reason": "wp780"})
	wantItem(t, &res, l1, http.StatusOK, "")
	wantItem(t, &res, l2, http.StatusOK, "")
	wantItem(t, &res, plain, http.StatusUnprocessableEntity, "", "to_state")
	if s, v := state(l1); s != "draft" || v != before+1 {
		t.Errorf("l1: state %q version %d, want draft %d", s, v, before+1)
	}
	if changes, outbox := trailOf(l1); changes != 1 || outbox != 1 {
		t.Errorf("l1: %d entity_change, %d lifecycle.changed, want 1 and 1", changes, outbox)
	}

	// A failed guard is 422 lifecycle-guard with its fields, a missing edge
	// invalid-transition, another from_state 409.
	res = e.bulk(ci.BulkLifecycleTransition, []string{l1, l3}, map[string]any{"to_state": "production", "context": map[string]any{"version": "x"}})
	guard := wantItem(t, &res, l1, http.StatusUnprocessableEntity, lifecycle.ProblemLifecycleGuard)
	if !slices.Contains(guard.Fields, "version") || !slices.Contains(guard.Fields, "owner") {
		t.Errorf("guard fields %v, want version and owner", guard.Fields)
	}
	wantItem(t, &res, l3, http.StatusUnprocessableEntity, lifecycle.ProblemInvalidTransition, "to_state")
	res = e.bulk(ci.BulkLifecycleTransition, []string{l2}, map[string]any{"to_state": "retired", "from_state": "production"})
	wantItem(t, &res, l2, http.StatusConflict, lifecycle.ProblemConflict, "from_state")
	if s, _ := state(l1); s != "draft" {
		t.Errorf("l1 after the refused guard: %q", s)
	}
	res = e.bulk(ci.BulkLifecycleTransition, []string{l1}, map[string]any{"to_state": "production",
		"context": map[string]any{"version": "v2", "owner": "ops"}})
	if res.Succeeded != 1 {
		t.Fatalf("go live: %+v", res)
	}
	if s, _ := state(l1); s != "production" {
		t.Errorf("l1: %q, want production", s)
	}
}

// TestBulkDelegateChangeTemplate covers change_template.
func TestBulkDelegateChangeTemplate(t *testing.T) {
	e := newDelegateEnv(t)
	ctx := e.full
	define := func(typeID, key string) citype.AttributeDefinition {
		t.Helper()
		d := citype.AttributeDefinition{CITypeID: typeID, Key: key, DataType: "text"}
		if err := e.types.CreateAttributeDefinition(ctx, e.f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		return d
	}
	note, size, needed := define(e.typeID, "w780_note"), define(e.typeID, "w780_size"), define(e.typeID, "w780_needed")
	item := func(def citype.AttributeDefinition, defaultValue string, required bool) citype.TemplateVersionItem {
		it := citype.TemplateVersionItem{AttributeDefinitionID: def.ID}
		if defaultValue != "" {
			it.DefaultOverride = json.RawMessage(defaultValue)
		}
		if required {
			it.RequiredOverride = &required
		}
		return it
	}
	setItems := func(tpl *citype.Template, version int, items []citype.TemplateVersionItem) {
		t.Helper()
		for i := range items {
			items[i].Position = i
		}
		if _, err := e.types.SetTemplateVersionItems(ctx, e.f.OrgA, tpl.ID, version, items); err != nil {
			t.Fatalf("template %s v%d items: %v", tpl.Key, version, err)
		}
	}
	publish := func(tpl *citype.Template, version int, items []citype.TemplateVersionItem) {
		t.Helper()
		setItems(tpl, version, items)
		if _, err := e.types.PublishTemplateVersion(ctx, e.f.OrgA, tpl.ID, version); err != nil {
			t.Fatalf("publish %s v%d: %v", tpl.Key, version, err)
		}
	}
	// template creates a template of typeID with one published version per
	// item list.
	template := func(typeID, key string, versions ...[]citype.TemplateVersionItem) citype.Template {
		t.Helper()
		tpl := citype.Template{Key: key, CITypeID: typeID, IsActive: true}
		if err := e.types.CreateTemplate(ctx, e.f.OrgA, &tpl); err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		for i, items := range versions {
			if i > 0 {
				if _, err := e.types.CreateTemplateVersion(ctx, e.f.OrgA, tpl.ID); err != nil {
					t.Fatalf("template %s v%d: %v", key, i+1, err)
				}
			}
			publish(&tpl, i+1, items)
		}
		return tpl
	}
	assign := func(ciID string, version int, tpls ...citype.Template) {
		t.Helper()
		for i := range tpls {
			e.exec(`INSERT INTO ci_template (organization_id, ci_id, template_id, version, position) VALUES ($1, $2, $3, $4, $5)`,
				e.f.OrgA, ciID, tpls[i].ID, version, i)
		}
	}
	templateAt := func(ciID string, position int) (string, int) {
		t.Helper()
		var id string
		var v int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT template_id::text, version FROM ci_template WHERE ci_id = $1 AND position = $2`,
			ciID, position).Scan(&id, &v); err != nil {
			t.Fatalf("template of %s at %d: %v", ciID, position, err)
		}
		return id, v
	}
	attribute := func(ciID, key string) string {
		t.Helper()
		var s string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(attributes->>$2, '') FROM ci WHERE id = $1`, ciID, key).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	primary := template(e.typeID, "w780-primary", []citype.TemplateVersionItem{item(note, "", false)},
		[]citype.TemplateVersionItem{item(note, `"from-v2"`, false), item(size, `"5"`, false), item(needed, "", true)})
	other := template(e.typeID, "w780-other", []citype.TemplateVersionItem{item(note, "", false)})
	replacement := template(e.typeID, "w780-replacement", []citype.TemplateVersionItem{item(size, "", false)})
	strict := template(e.typeID, "w780-strict", []citype.TemplateVersionItem{item(needed, "", true)})
	var otherType string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp780-other', 'WP-780 other')
		RETURNING id::text`, e.f.OrgA).Scan(&otherType); err != nil {
		t.Fatal(err)
	}
	foreign := template(otherType, "w780-foreign", []citype.TemplateVersionItem{})

	migrated, current, none := e.ci("w780-migrated"), e.ci("w780-current"), e.ci("w780-none")
	assign(migrated, 1, primary)
	assign(current, 2, primary)
	// The override of w780_size keeps its value through the migration.
	w := e.do(ctx, http.MethodPatch, "/api/v1/cis/"+migrated, map[string]any{"attributes": map[string]any{"w780_size": "manual"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	versionOf := func(id string) int { return e.count(`SELECT version FROM ci WHERE id = $1`, id) }
	currentVersion := versionOf(current)

	// A to_version that is not published is 422 for the request, before a
	// change.
	if _, err := e.types.CreateTemplateVersion(ctx, e.f.OrgA, primary.ID); err != nil {
		t.Fatal(err)
	}
	e.refused(ctx, map[string]any{"action": ci.BulkChangeTemplate, "ids": []string{migrated},
		"params": map[string]any{"template_id": primary.ID, "to_version": 3}}, http.StatusUnprocessableEntity, "params.to_version", "")
	if _, v := templateAt(migrated, 0); v != 1 {
		t.Fatalf("version %d after the refused request, want 1", v)
	}

	// (a) Migration to version 2 (the current version): the default lands
	// in the empty field, the override stays, the required field is a
	// finding; the CI on version 2 is ok without a change; a CI without the
	// template is 422 on template_id.
	res := e.bulk(ci.BulkChangeTemplate, []string{migrated, current, none}, map[string]any{"template_id": primary.ID})
	got := wantItem(t, &res, migrated, http.StatusOK, "")
	if len(got.Findings) == 0 || got.Findings[0].Field != "w780_needed" {
		t.Errorf("findings %+v, want w780_needed", got.Findings)
	}
	if _, v := templateAt(migrated, 0); v != 2 {
		t.Errorf("migrated on version %d, want 2", v)
	}
	if note, size := attribute(migrated, "w780_note"), attribute(migrated, "w780_size"); note != "from-v2" || size != "manual" {
		t.Errorf("attributes note %q size %q, want from-v2 and manual (override)", note, size)
	}
	if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'template_migration'`, migrated); n != 1 {
		t.Errorf("%d template_migration changes, want 1", n)
	}
	wantItem(t, &res, current, http.StatusOK, "")
	if versionOf(current) != currentVersion {
		t.Errorf("the CI on the target version changed")
	}
	wantItem(t, &res, none, http.StatusUnprocessableEntity, "", "template_id")

	// (b) replace_template_id at the same position; a violation of the new
	// schema and a template of another CI type are 422 for the id.
	swapped, violated, typed := e.ci("w780-swapped"), e.ci("w780-violated"), e.ci("w780-typed")
	assign(swapped, 1, primary, other)
	assign(violated, 1, other)
	assign(typed, 1, other)
	res = e.bulk(ci.BulkChangeTemplate, []string{swapped}, map[string]any{"template_id": replacement.ID, "replace_template_id": other.ID})
	wantItem(t, &res, swapped, http.StatusOK, "")
	if id, v := templateAt(swapped, 1); id != replacement.ID || v != 1 {
		t.Errorf("position 1: %s v%d, want the replacement v1", id, v)
	}
	if id, _ := templateAt(swapped, 0); id != primary.ID {
		t.Errorf("position 0 changed")
	}
	res = e.bulk(ci.BulkChangeTemplate, []string{violated}, map[string]any{"template_id": strict.ID, "replace_template_id": other.ID})
	wantItem(t, &res, violated, http.StatusUnprocessableEntity, "", "w780_needed")
	res = e.bulk(ci.BulkChangeTemplate, []string{typed}, map[string]any{"template_id": foreign.ID, "replace_template_id": other.ID})
	wantItem(t, &res, typed, http.StatusUnprocessableEntity, "", "template_id")
	for _, id := range []string{violated, typed} {
		if tpl, _ := templateAt(id, 0); tpl != other.ID {
			t.Errorf("%s: template %s after the refusal, want the old one", id, tpl)
		}
	}

	// 100 ids run as the job ci.bulk.
	many := make([]string, 0, ci.BulkJobFromIDs)
	for i := range ci.BulkJobFromIDs {
		id := e.ci(fmt.Sprintf("w780-many-%03d", i))
		assign(id, 1, other)
		many = append(many, id)
	}
	v2Items := []citype.TemplateVersionItem{item(note, `"v2"`, false)}
	if _, err := e.types.CreateTemplateVersion(ctx, e.f.OrgA, other.ID); err != nil {
		t.Fatal(err)
	}
	publish(&other, 2, v2Items)
	w = e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": ci.BulkChangeTemplate, "ids": many,
		"params": map[string]any{"template_id": other.ID}})
	var acc struct {
		JobID string `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &acc) != nil || acc.Kind != ci.BulkJobKind ||
		w.Header().Get("Location") != "/api/v1/jobs/"+acc.JobID {
		t.Fatalf("100 ids: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		job, err := e.jobSvc.Get(ctx, e.f.OrgA, acc.JobID)
		if err == nil && job.Status.Terminal() {
			if job.Status != jobs.StatusSucceeded {
				t.Fatalf("job ended %s: %s", job.Status, job.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s not done", acc.JobID)
		}
	}
	if n := e.count(`SELECT count(*) FROM ci_template WHERE ci_id = ANY($1::uuid[]) AND template_id = $2 AND version = 2`, many, other.ID); n != len(many) {
		t.Errorf("%d of %d CIs migrated by the job", n, len(many))
	}
}

// TestBulkDelegateExport covers export.
func TestBulkDelegateExport(t *testing.T) {
	e := newDelegateEnv(t)
	e1, e2, gone := e.ci("w780-e1"), e.ci("w780-e2"), e.ci("w780-gone")
	invisible := e.f.CI(t, e.f.OrgA, e.f.Client2, "w780-invisible")
	e.exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, gone)
	exportJobs := func() int { return e.count(`SELECT count(*) FROM export_job WHERE organization_id = $1`, e.f.OrgA) }
	body := func(format string, ids ...string) map[string]any {
		return map[string]any{"action": ci.BulkExport, "ids": ids, "params": map[string]any{"format": format}}
	}

	// Without export:run, without export_csv and with datev the request is
	// refused before a job.
	e.refused(e.writer, body("json", e1), http.StatusForbidden, "", api.ProblemTypeForbidden)
	e.features.off.Store(true)
	e.refused(e.full, body("json", e1), http.StatusForbidden, "", api.ProblemFeatureNotEntitled)
	e.features.off.Store(false)
	e.refused(e.full, body("datev", e1), http.StatusUnprocessableEntity, "params.format", "")
	if n := exportJobs(); n != 0 {
		t.Fatalf("%d export jobs after the refusals, want 0", n)
	}

	// Four ids, one deleted and one outside the scope: one job, 202 with
	// its Location.
	w := e.do(e.full, http.MethodPost, "/api/v1/cis/bulk", body("json", e2, gone, e1, invisible))
	var acc struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Kind   string `json:"kind"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &acc) != nil || acc.Kind != export.ExportJobKind ||
		w.Header().Get("Location") != "/api/v1/exports/"+acc.JobID {
		t.Fatalf("export: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if n := exportJobs(); n != 1 {
		t.Fatalf("%d export jobs, want 1", n)
	}
	var ids []string
	var scoped bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(filters->'ids')), scope_recorded AND scope_clients = ARRAY[$2]::uuid[]
		FROM export_job WHERE id = $1`, acc.JobID, e.f.Client1).Scan(&ids, &scoped); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{e2, gone, e1, invisible}) || !scoped {
		t.Fatalf("filters.ids %v scope %v", ids, scoped)
	}

	// The file holds exactly the visible, not deleted CIs.
	for deadline := time.Now().Add(30 * time.Second); ; {
		status := ""
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM export_job WHERE id = $1`, acc.JobID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == export.JobStatusCompleted {
			break
		}
		if status == export.JobStatusFailed || time.Now().After(deadline) {
			t.Fatalf("export job %s", status)
		}
		e.worker.ProcessOne(context.Background())
	}
	var key string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT object_key FROM export_job WHERE id = $1`, acc.JobID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	rc, err := e.blobs.Get(context.Background(), export.ExportBucket, key)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	defer rc.Close()
	var file struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(rc).Decode(&file); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	var got []string
	for _, row := range file.Data {
		got = append(got, row.ID)
	}
	slices.Sort(got)
	want := []string{e1, e2}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("exported %v, want %v", got, want)
	}
}
