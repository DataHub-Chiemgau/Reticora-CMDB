package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
)

// Versioned workflow definitions (WFL-02, API-06, WP-1086, E-228 option 1):
// a change of trigger, conditions or actions stores an immutable version
// n+1, a run executes the version it started with, PATCH honours If-Match
// and the versions of another organization stay invisible.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed),
// except TestMemoryRepositoryVersions.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestPatchActionsCreatesVersion2|TestWaitingRunKeepsVersion1|TestDefinitionVersionsAreImmutable|TestPatchWorkflowIfMatch412|TestPatchKeepsDeactivatedLegacyCondition422|TestWorkflowVersionsOfOtherOrganization404|TestMemoryRepositoryVersions' ./internal/workflow/

// versionEnv is one organization's view of the workflow API over
// PostgreSQL.
type versionEnv struct {
	f      *scopetest.Fixture
	repo   *workflow.PGRepository
	exec   *workflow.Executor
	router chi.Router
	user   string
}

func newVersionEnv(t *testing.T) *versionEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	repo := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(repo, nil, nil, nil, nil)
	router := chi.NewRouter()
	workflow.NewHandler(repo, exec).RegisterRoutes(router)
	return &versionEnv{f: f, repo: repo, exec: exec, router: router, user: f.AppUser(t, f.OrgA, "wf-versions")}
}

// ctx is the context of a user of orgID (org-wide scope).
func (e *versionEnv) ctx(orgID, user string) context.Context {
	scope := database.OrgWideScope(orgID, user)
	return tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: orgID, UserID: user})
}

func (e *versionEnv) do(t *testing.T, ctx context.Context, method, path string, body any, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *versionEnv) create(t *testing.T, ctx context.Context, name string, actions []workflow.JSONMap) workflow.Definition {
	t.Helper()
	rec := e.do(t, ctx, http.MethodPost, "/api/v1/workflows", map[string]any{"name": name, "trigger": map[string]any{"type": "manual"}, "actions": actions}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workflow %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var d workflow.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func decodeDef(t *testing.T, rec *httptest.ResponseRecorder) workflow.Definition {
	t.Helper()
	var d workflow.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return d
}

// TestPatchActionsCreatesVersion2: POST stores version 1, a PATCH of the
// actions version 2 with the creating user, a PATCH of the name no version.
func TestPatchActionsCreatesVersion2(t *testing.T) {
	e := newVersionEnv(t)
	ctx := e.ctx(e.f.OrgA, e.user)
	d := e.create(t, ctx, "wf-v2", []workflow.JSONMap{{"type": "noop"}})
	if d.Version != 1 || d.CurrentVersionID == "" {
		t.Fatalf("created definition version %d id %q, want 1 and a version id", d.Version, d.CurrentVersionID)
	}
	rec := e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"actions": []any{map[string]any{"type": "require_approval"}}}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != api.ETag(2) {
		t.Fatalf("patch actions: %d ETag %q %s, want 200 with ETag \"2\"", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	v2 := decodeDef(t, rec)
	if v2.Version != 2 || v2.CurrentVersionID == d.CurrentVersionID || len(v2.Actions) != 1 || v2.Actions[0]["type"] != "require_approval" {
		t.Fatalf("after the actions PATCH: %+v, want version 2 with the new actions", v2)
	}
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"name": "wf-v2-renamed", "active": false}, nil)
	renamed := decodeDef(t, rec)
	if rec.Code != http.StatusOK || renamed.Version != 2 || renamed.CurrentVersionID != v2.CurrentVersionID || renamed.Name != "wf-v2-renamed" {
		t.Fatalf("name PATCH: %d %+v, want version 2 unchanged", rec.Code, renamed)
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/workflows/"+d.ID, nil, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != api.ETag(2) {
		t.Fatalf("GET: %d ETag %q, want ETag \"2\"", rec.Code, rec.Header().Get("ETag"))
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/workflows/"+d.ID+"/versions", nil, nil)
	var list struct {
		Data  []workflow.DefinitionVersion `json:"data"`
		Total int                          `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("versions: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	if list.Total != 2 || len(list.Data) != 2 || list.Data[0].Version != 2 || list.Data[1].Version != 1 {
		t.Fatalf("versions %+v, want 2 then 1", list.Data)
	}
	if list.Data[0].CreatedBy != e.user || list.Data[1].Actions[0]["type"] != "noop" {
		t.Fatalf("versions %+v, want version 2 by %s and version 1 with the noop action", list.Data, e.user)
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/workflows/"+d.ID+"/versions/1", nil, nil)
	var one workflow.DefinitionVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != http.StatusOK || one.Version != 1 || one.ID != d.CurrentVersionID {
		t.Fatalf("version 1: %d %+v %v", rec.Code, one, err)
	}
	for _, number := range []string{"3", "0", "2147483648", "99999999999", "x"} {
		if rec = e.do(t, ctx, http.MethodGet, "/api/v1/workflows/"+d.ID+"/versions/"+number, nil, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("version %s: %d %s, want 404", number, rec.Code, rec.Body.String())
		}
	}
	// The same actions again store no new version: a version records a
	// change of content, not every PATCH that carries the field.
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"actions": []any{map[string]any{"type": "require_approval"}}}, nil)
	if same := decodeDef(t, rec); rec.Code != http.StatusOK || same.Version != 2 || same.CurrentVersionID != v2.CurrentVersionID {
		t.Fatalf("PATCH with unchanged actions: %d %+v, want version 2 unchanged", rec.Code, same)
	}
}

// TestWaitingRunKeepsVersion1: a run waits for its approval, a PATCH
// replaces the actions, and Approve executes the actions of version 1.
func TestWaitingRunKeepsVersion1(t *testing.T) {
	e := newVersionEnv(t)
	ctx := e.ctx(e.f.OrgA, e.user)
	d := e.create(t, ctx, "wf-waiting", []workflow.JSONMap{{"type": "require_approval", "exclude_initiator": false}, {"type": "noop"}})
	rec := e.do(t, ctx, http.MethodPost, "/api/v1/workflows/"+d.ID+"/runs", map[string]any{"trigger": "manual", "context": map[string]any{}}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("trigger: %d %s", rec.Code, rec.Body.String())
	}
	var run workflow.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.StatusWaitingApproval || run.DefinitionVersionID != d.CurrentVersionID {
		t.Fatalf("run %+v, want waiting on version %s", run, d.CurrentVersionID)
	}
	// Version 2 would fail the run: its second action is unknown.
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"actions": []any{
		map[string]any{"type": "require_approval"}, map[string]any{"type": "no_such_action"}}}, nil)
	if rec.Code != http.StatusOK || decodeDef(t, rec).Version != 2 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	done, err := e.exec.Approve(ctx, e.f.OrgA, run.ID, "approved", "")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != workflow.StatusSucceeded || done.DefinitionVersionID != d.CurrentVersionID {
		t.Fatalf("approved run %+v, want succeeded on version 1", done)
	}
	steps, err := e.repo.ListSteps(ctx, e.f.OrgA, run.ID)
	if err != nil || len(steps) != 2 || steps[1].ActionType != "noop" || steps[1].Status != workflow.StatusSucceeded {
		t.Fatalf("steps %+v %v, want the noop of version 1 executed", steps, err)
	}
	// A new run binds version 2.
	rec = e.do(t, ctx, http.MethodPost, "/api/v1/workflows/"+d.ID+"/runs", map[string]any{"trigger": "manual", "context": map[string]any{}}, nil)
	var second workflow.Run
	_ = json.Unmarshal(rec.Body.Bytes(), &second)
	if rec.Code != http.StatusCreated || second.DefinitionVersionID == d.CurrentVersionID || second.DefinitionVersionID == "" {
		t.Fatalf("second run %d %+v, want version 2", rec.Code, second)
	}
}

// TestDefinitionVersionsAreImmutable: UPDATE and DELETE of a version fail,
// deleting the definition deletes its versions.
func TestDefinitionVersionsAreImmutable(t *testing.T) {
	e := newVersionEnv(t)
	ctx := e.ctx(e.f.OrgA, e.user)
	d := e.create(t, ctx, "wf-immutable", []workflow.JSONMap{{"type": "noop"}})
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `UPDATE workflow_def_version SET actions = '[]' WHERE id = $1`, d.CurrentVersionID); err == nil {
		t.Fatal("UPDATE of a definition version succeeded")
	}
	if _, err := e.f.Admin.Exec(bg, `DELETE FROM workflow_def_version WHERE id = $1`, d.CurrentVersionID); err == nil {
		t.Fatal("DELETE of a definition version succeeded")
	}
	if rec := e.do(t, ctx, http.MethodDelete, "/api/v1/workflows/"+d.ID, nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete definition: %d %s", rec.Code, rec.Body.String())
	}
	var left int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM workflow_def_version WHERE workflow_id = $1`, d.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("versions after the delete: %d %v, want 0", left, err)
	}
}

// TestPatchWorkflowIfMatch412: an outdated If-Match is 412 and changes
// nothing; the current one and "*" pass.
func TestPatchWorkflowIfMatch412(t *testing.T) {
	e := newVersionEnv(t)
	ctx := e.ctx(e.f.OrgA, e.user)
	d := e.create(t, ctx, "wf-ifmatch", []workflow.JSONMap{{"type": "noop"}})
	patch := map[string]any{"actions": []any{map[string]any{"type": "noop", "n": 2}}}
	if rec := e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, patch, map[string]string{"If-Match": api.ETag(1)}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH with the current If-Match: %d %s", rec.Code, rec.Body.String())
	}
	stale := map[string]any{"actions": []any{map[string]any{"type": "noop", "n": 3}}}
	rec := e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, stale, map[string]string{"If-Match": api.ETag(1)})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("PATCH with a stale If-Match: %d %s, want 412", rec.Code, rec.Body.String())
	}
	if rec = e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, stale, map[string]string{"If-Match": "not-a-version"}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("PATCH with a malformed If-Match: %d, want 412", rec.Code)
	}
	got, err := e.repo.GetDefinition(ctx, e.f.OrgA, d.ID)
	if err != nil || got.Version != 2 {
		t.Fatalf("definition after the refused PATCH: %+v %v, want version 2", got, err)
	}
	if rec = e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, stale, map[string]string{"If-Match": "*"}); rec.Code != http.StatusOK || rec.Header().Get("ETag") != api.ETag(3) {
		t.Fatalf("PATCH with If-Match *: %d ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// TestPatchKeepsDeactivatedLegacyCondition422: a definition migration
// 000330 deactivated keeps its G1 condition; PATCH {"active": true} checks
// the conditions in effect and answers 422 on conditions[0].op, the
// definition stays inactive at version 1 (WP-1087 AK1/AK6).
func TestPatchKeepsDeactivatedLegacyCondition422(t *testing.T) {
	e := newVersionEnv(t)
	ctx := e.ctx(e.f.OrgA, e.user)
	d := &workflow.Definition{OrganizationID: e.f.OrgA, Name: "wf-legacy", Trigger: workflow.JSONMap{"type": "manual"},
		Conditions: []workflow.JSONMap{{"field": "name", "op": "matches", "value": "^sw"}}, Actions: []workflow.JSONMap{{"type": "noop"}}}
	if err := e.repo.CreateDefinition(ctx, d); err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, ctx, http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"active": true}, nil)
	var p api.FieldProblem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusUnprocessableEntity || p.Field != "conditions[0].op" {
		t.Fatalf("PATCH active=true: %d %s, want 422 on conditions[0].op", rec.Code, rec.Body.String())
	}
	got, err := e.repo.GetDefinition(ctx, e.f.OrgA, d.ID)
	if err != nil || got.Active || got.Version != 1 {
		t.Fatalf("definition after the refused PATCH: %+v %v, want inactive version 1", got, err)
	}
}

// TestWorkflowVersionsOfOtherOrganization404: the versions of a definition
// of organization B answer 404 to organization A (TEN-04).
func TestWorkflowVersionsOfOtherOrganization404(t *testing.T) {
	e := newVersionEnv(t)
	userB := e.f.AppUser(t, e.f.OrgB, "wf-versions-b")
	defB := e.create(t, e.ctx(e.f.OrgB, userB), "wf-org-b", []workflow.JSONMap{{"type": "noop"}})
	ctxA := e.ctx(e.f.OrgA, e.user)
	for _, path := range []string{"/api/v1/workflows/" + defB.ID + "/versions", "/api/v1/workflows/" + defB.ID + "/versions/1", "/api/v1/workflows/" + defB.ID} {
		if rec := e.do(t, ctxA, http.MethodGet, path, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s from organization A: %d %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	if _, err := e.repo.GetVersionByID(ctxA, e.f.OrgA, defB.CurrentVersionID); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("version of organization B by id: %v, want ErrVersionNotFound", err)
	}
}

// TestMemoryRepositoryVersions: the memory repository (--no-db) keeps the
// versions with the semantics of PostgreSQL.
func TestMemoryRepositoryVersions(t *testing.T) {
	ctx := context.Background()
	repo := workflow.NewMemoryRepository()
	exec := workflow.NewExecutor(repo, nil, nil, nil, nil)
	d := &workflow.Definition{OrganizationID: "org-1", Name: "mem", Trigger: workflow.JSONMap{"type": "manual"}, Active: true,
		Actions: []workflow.JSONMap{{"type": "require_approval"}, {"type": "noop"}}}
	if err := repo.CreateDefinition(ctx, d); err != nil || d.Version != 1 || d.CurrentVersionID == "" {
		t.Fatalf("create: %+v %v", d, err)
	}
	run, err := exec.Trigger(ctx, "org-1", d, "manual", workflow.JSONMap{})
	if err != nil || run.DefinitionVersionID != d.CurrentVersionID {
		t.Fatalf("run %+v %v, want version 1", run, err)
	}
	name := "mem-renamed"
	same, err := repo.UpdateDefinition(ctx, "org-1", d.ID, &workflow.UpdateDefinitionRequest{Name: &name})
	if err != nil || same.Version != 1 {
		t.Fatalf("rename: %+v %v, want version 1", same, err)
	}
	stale := int64(2)
	if _, err = repo.UpdateDefinition(ctx, "org-1", d.ID, &workflow.UpdateDefinitionRequest{Actions: []workflow.JSONMap{{"type": "x"}}, IfMatch: &stale}); !errors.Is(err, workflow.ErrPreconditionFailed) {
		t.Fatalf("stale If-Match: %v, want ErrPreconditionFailed", err)
	}
	v2, err := repo.UpdateDefinition(ctx, "org-1", d.ID, &workflow.UpdateDefinitionRequest{Actions: []workflow.JSONMap{{"type": "require_approval"}, {"type": "no_such_action"}}})
	if err != nil || v2.Version != 2 || v2.CurrentVersionID == d.CurrentVersionID {
		t.Fatalf("actions PATCH: %+v %v, want version 2", v2, err)
	}
	versions, total, err := repo.ListVersions(ctx, "org-1", d.ID, api.PaginationParams{Limit: 10})
	if err != nil || total != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions %+v (%d) %v, want 2 then 1", versions, total, err)
	}
	if _, err = repo.GetVersion(ctx, "org-2", d.ID, 1); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Fatalf("version of another organization: %v, want ErrVersionNotFound", err)
	}
	if _, _, err = repo.ListVersions(ctx, "org-2", d.ID, api.PaginationParams{Limit: 10}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("versions of another organization: %v, want ErrNotFound", err)
	}
	done, err := exec.Approve(ctx, "org-1", run.ID, "approved", "")
	if err != nil || done.Status != workflow.StatusSucceeded {
		t.Fatalf("approve: %+v %v, want the run of version 1 to succeed", done, err)
	}
	if err = repo.DeleteDefinition(ctx, "org-1", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetVersionByID(ctx, "org-1", d.CurrentVersionID); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Fatalf("version after the delete: %v, want ErrVersionNotFound", err)
	}
}
