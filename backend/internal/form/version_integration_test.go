package form_test

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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
)

// Versioned form definitions (WFL-01, API-06, WP-1100, E-228 option 1): a
// changed schema stores an immutable version, a submission is bound to the
// version it was captured against (API and submit_form), PATCH honours
// If-Match and the versions of a form outside the client scope stay
// invisible.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestSchemaChangeCreatesVersion|TestSubmissionBindsVersion|TestSubmitFormActionBindsCurrentVersion|TestFormVersionImmutable|TestFormIfMatch|TestFormVersionClientScope|TestSubmissionValidatedAgainstBoundVersion|TestFormSiteRestrictedPrincipalWrites|TestMemoryFormVersions' ./internal/form/

type formEnv struct {
	f      *scopetest.Fixture
	repo   *form.PGRepository
	router chi.Router
	user   string
}

func newFormEnv(t *testing.T) *formEnv {
	t.Helper()
	f := scopetest.Seed(t, "3c")
	repo := form.NewPGRepository(f.App)
	router := chi.NewRouter()
	form.NewHandler(repo).RegisterRoutes(router)
	return &formEnv{f: f, repo: repo, router: router, user: f.AppUser(t, f.OrgA, "form-versions")}
}

// orgCtx is an org-wide principal of organization A; clientCtx one bound
// to a client.
func (e *formEnv) orgCtx() context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.user)
	return tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
}

func (e *formEnv) clientCtx(client string) context.Context {
	return tenant.WithTenant(e.f.ClientCtx(client), tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: client, UserID: e.user})
}

func (e *formEnv) do(t *testing.T, ctx context.Context, method, path string, body any, header map[string]string) *httptest.ResponseRecorder {
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

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

var (
	schemaV1 = map[string]any{"type": "object", "required": []any{"host"}, "properties": map[string]any{"host": map[string]any{"type": "string"}}}
	schemaV2 = map[string]any{"type": "object", "required": []any{"host", "rack"},
		"properties": map[string]any{"host": map[string]any{"type": "string"}, "rack": map[string]any{"type": "string"}}}
)

func (e *formEnv) create(t *testing.T, ctx context.Context, name, client string) form.Definition {
	t.Helper()
	body := map[string]any{"name": name, "schema": schemaV1}
	if client != "" {
		body["client_id"] = client
	}
	rec := e.do(t, ctx, http.MethodPost, "/api/v1/forms", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create form %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return decode[form.Definition](t, rec)
}

// TestSchemaChangeCreatesVersion: a PATCH with a new schema stores version
// 2, a PATCH of the name or with the same schema stores none.
func TestSchemaChangeCreatesVersion(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-schema", "")
	if d.SchemaVersion != 1 || d.Version != 1 || d.CurrentVersionID == "" {
		t.Fatalf("created form %+v, want schema version 1", d)
	}
	rec := e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil)
	v2 := decode[form.Definition](t, rec)
	if rec.Code != http.StatusOK || v2.SchemaVersion != 2 || v2.CurrentVersionID == d.CurrentVersionID || rec.Header().Get("ETag") != api.ETag(2) {
		t.Fatalf("schema PATCH: %d %+v ETag %q, want schema version 2 and ETag \"2\"", rec.Code, v2, rec.Header().Get("ETag"))
	}
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"name": "fv-schema-renamed"}, nil)
	renamed := decode[form.Definition](t, rec)
	if rec.Code != http.StatusOK || renamed.SchemaVersion != 2 || renamed.CurrentVersionID != v2.CurrentVersionID || renamed.Version != 3 {
		t.Fatalf("name PATCH: %d %+v, want schema version 2 and version 3", rec.Code, renamed)
	}
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil)
	if same := decode[form.Definition](t, rec); rec.Code != http.StatusOK || same.SchemaVersion != 2 || same.Version != 3 {
		t.Fatalf("same schema PATCH: %d %+v, want no new version", rec.Code, same)
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/forms/"+d.ID+"/versions", nil, nil)
	list := decode[struct {
		Data  []form.DefinitionVersion `json:"data"`
		Total int                      `json:"total"`
	}](t, rec)
	if rec.Code != http.StatusOK || list.Total != 2 || list.Data[0].Version != 2 || list.Data[1].Version != 1 || list.Data[0].CreatedBy != e.user {
		t.Fatalf("versions %d %+v, want 2 (by %s) then 1", rec.Code, list, e.user)
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/forms/"+d.ID+"/versions/1", nil, nil)
	if one := decode[form.DefinitionVersion](t, rec); rec.Code != http.StatusOK || one.ID != d.CurrentVersionID || one.Schema["required"] == nil {
		t.Fatalf("version 1: %d %+v", rec.Code, one)
	}
	if rec = e.do(t, ctx, http.MethodGet, "/api/v1/forms/"+d.ID+"/versions/9", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("version 9: %d, want 404", rec.Code)
	}
}

// TestSubmissionBindsVersion: a submission before and one after the schema
// change carry version 1 and 2; the old one keeps version 1, and the
// handler validates against the schema of the current version.
func TestSubmissionBindsVersion(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-bind", "")
	rec := e.do(t, ctx, http.MethodPost, "/api/v1/forms/"+d.ID+"/submissions", map[string]any{"values": map[string]any{"host": "a"}}, nil)
	first := decode[form.Submission](t, rec)
	if rec.Code != http.StatusCreated || first.FormVersionID != d.CurrentVersionID || first.FormVersion != 1 {
		t.Fatalf("first submission: %d %+v, want version 1", rec.Code, first)
	}
	v2 := decode[form.Definition](t, e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil))
	// Values valid under version 1 only are refused by version 2.
	if rec = e.do(t, ctx, http.MethodPost, "/api/v1/forms/"+d.ID+"/submissions", map[string]any{"values": map[string]any{"host": "b"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("submission against version 2 without rack: %d, want 400", rec.Code)
	}
	rec = e.do(t, ctx, http.MethodPost, "/api/v1/forms/"+d.ID+"/submissions", map[string]any{"values": map[string]any{"host": "b", "rack": "r1"}}, nil)
	second := decode[form.Submission](t, rec)
	if rec.Code != http.StatusCreated || second.FormVersionID != v2.CurrentVersionID || second.FormVersion != 2 {
		t.Fatalf("second submission: %d %+v, want version 2", rec.Code, second)
	}
	rec = e.do(t, ctx, http.MethodGet, "/api/v1/form-submissions/"+first.ID, nil, nil)
	if again := decode[form.Submission](t, rec); rec.Code != http.StatusOK || again.FormVersionID != d.CurrentVersionID || again.FormVersion != 1 {
		t.Fatalf("first submission read again: %d %+v, want version 1", rec.Code, again)
	}
	// A submission validated against an outdated version is refused.
	stale := &form.Submission{OrganizationID: e.f.OrgA, FormID: d.ID, Values: form.JSONMap{"host": "c"}, ExpectedVersionID: d.CurrentVersionID}
	if err := e.repo.CreateSubmission(ctx, stale); !errors.Is(err, form.ErrVersionChanged) {
		t.Fatalf("submission against an outdated version: %v, want ErrVersionChanged", err)
	}
}

// TestSubmitFormActionBindsCurrentVersion: the workflow action submit_form
// binds the current version as well.
func TestSubmitFormActionBindsCurrentVersion(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-action", "")
	v2 := decode[form.Definition](t, e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil))
	runs := workflow.NewPGRepository(e.f.App)
	exec := workflow.NewExecutor(runs, nil, nil, e.repo, nil)
	def := &workflow.Definition{OrganizationID: e.f.OrgA, Name: "fv-submit", Trigger: workflow.JSONMap{"type": "manual"}, Active: true,
		Actions: []workflow.JSONMap{{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"host": "h", "rack": "r"}}}}
	if err := runs.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(ctx, e.f.OrgA, def, "manual", workflow.JSONMap{})
	if err != nil || run.Status != workflow.StatusSucceeded {
		t.Fatalf("run %+v %v, want succeeded", run, err)
	}
	steps, err := runs.ListSteps(ctx, e.f.OrgA, run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps %+v %v", steps, err)
	}
	id, _ := steps[0].Output["submission_id"].(string)
	sub, err := e.repo.GetSubmission(ctx, e.f.OrgA, id)
	if err != nil || sub.FormVersionID != v2.CurrentVersionID || sub.FormVersion != 2 {
		t.Fatalf("submission of submit_form: %+v %v, want version 2", sub, err)
	}
}

// TestSubmissionValidatedAgainstBoundVersion: values checked against
// version 1 by a caller that read the form before a schema change (as the
// submit_form action does) are refused when the repository binds version 2.
func TestSubmissionValidatedAgainstBoundVersion(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-bound", "")
	if form.Validate(d.Schema, map[string]any{"host": "h"}) != nil {
		t.Fatal("the values must satisfy version 1")
	}
	if rec := e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	sub := &form.Submission{OrganizationID: e.f.OrgA, FormID: d.ID, Values: form.JSONMap{"host": "h"}, Status: "submitted"}
	var ve form.ValidationError
	if err := e.repo.CreateSubmission(ctx, sub); !errors.As(err, &ve) {
		t.Fatalf("submission valid only for version 1: %v, want a validation error against version 2", err)
	}
	var stored int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM form_submission WHERE form_id = $1`, d.ID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored submissions: %d %v, want 0", stored, err)
	}
}

// TestFormSiteRestrictedPrincipalWrites: a principal restricted to a site
// (all clients) creates a form and changes its schema; a version carries no
// site, so the site scope does not refuse it (as for form_def itself).
func TestFormSiteRestrictedPrincipalWrites(t *testing.T) {
	e := newFormEnv(t)
	scope := database.TenantScope{OrgID: e.f.OrgA, UserID: e.user, Clients: database.AllScopes(),
		Sites: database.ScopeIDs("5a5a5a5a-0000-4000-8000-000000000001"), Teams: database.AllScopes()}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
	d := e.create(t, ctx, "fv-site", "")
	rec := e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil)
	if v2 := decode[form.Definition](t, rec); rec.Code != http.StatusOK || v2.SchemaVersion != 2 {
		t.Fatalf("schema PATCH as site-restricted principal: %d %s, want 200 with version 2", rec.Code, rec.Body.String())
	}
}

// TestFormVersionImmutable: UPDATE and DELETE of a version fail, deleting
// the form deletes its versions.
func TestFormVersionImmutable(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-immutable", "")
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `UPDATE form_def_version SET schema = '{}' WHERE id = $1`, d.CurrentVersionID); err == nil {
		t.Fatal("UPDATE of a form version succeeded")
	}
	if _, err := e.f.Admin.Exec(bg, `DELETE FROM form_def_version WHERE id = $1`, d.CurrentVersionID); err == nil {
		t.Fatal("DELETE of a form version succeeded")
	}
	if rec := e.do(t, ctx, http.MethodDelete, "/api/v1/forms/"+d.ID, nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete form: %d %s", rec.Code, rec.Body.String())
	}
	var left int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM form_def_version WHERE form_id = $1`, d.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("versions after the delete: %d %v, want 0", left, err)
	}
}

// TestFormIfMatch: an outdated If-Match is 412 and changes nothing.
func TestFormIfMatch(t *testing.T) {
	e := newFormEnv(t)
	ctx := e.orgCtx()
	d := e.create(t, ctx, "fv-ifmatch", "")
	rec := e.do(t, ctx, http.MethodGet, "/api/v1/forms/"+d.ID, nil, nil)
	etag := rec.Header().Get("ETag")
	if etag != api.ETag(1) {
		t.Fatalf("GET ETag %q, want \"1\"", etag)
	}
	if rec = e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"description": "first"}, map[string]string{"If-Match": etag}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH with the current If-Match: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, map[string]string{"If-Match": etag})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("PATCH with a stale If-Match: %d %s, want 412", rec.Code, rec.Body.String())
	}
	got, err := e.repo.GetDefinition(ctx, e.f.OrgA, d.ID)
	if err != nil || got.SchemaVersion != 1 || got.Version != 2 || got.Description != "first" {
		t.Fatalf("form after the refused PATCH: %+v %v", got, err)
	}
	if rec = e.do(t, ctx, http.MethodPatch, "/api/v1/forms/"+d.ID, map[string]any{"schema": schemaV2}, nil); rec.Code != http.StatusOK {
		t.Fatalf("PATCH without If-Match: %d", rec.Code)
	}
}

// TestFormVersionClientScope: the versions of a form of another client
// answer 404 to a principal bound to client 1, the versions of its own
// client and the moved versions follow the client of their form.
func TestFormVersionClientScope(t *testing.T) {
	e := newFormEnv(t)
	org := e.orgCtx()
	foreign := e.create(t, org, "fv-client2", e.f.Client2)
	own := e.create(t, org, "fv-client1", e.f.Client1)
	c1 := e.clientCtx(e.f.Client1)
	for _, path := range []string{"/api/v1/forms/" + foreign.ID + "/versions", "/api/v1/forms/" + foreign.ID + "/versions/1"} {
		if rec := e.do(t, c1, http.MethodGet, path, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as client 1: %d %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(t, c1, http.MethodGet, "/api/v1/forms/"+own.ID+"/versions/1", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("own version as client 1: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT client_id::text FROM form_def_version WHERE id = $1`, foreign.CurrentVersionID).Scan(&client); err != nil || client != e.f.Client2 {
		t.Fatalf("client of the version: %q %v, want client 2", client, err)
	}
	// The form moves to client 1: its versions follow.
	if rec := e.do(t, org, http.MethodPatch, "/api/v1/forms/"+foreign.ID, map[string]any{"client_id": e.f.Client1}, nil); rec.Code != http.StatusOK {
		t.Fatalf("move form: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, c1, http.MethodGet, "/api/v1/forms/"+foreign.ID+"/versions/1", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("moved version as client 1: %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// TestMemoryFormVersions: the memory repository (--no-db) keeps the
// versions with the semantics of PostgreSQL.
func TestMemoryFormVersions(t *testing.T) {
	ctx := context.Background()
	repo := form.NewMemoryRepository()
	d := &form.Definition{OrganizationID: "org-1", Name: "mem", Schema: form.JSONMap{"type": "object"}, Active: true}
	if err := repo.CreateDefinition(ctx, d); err != nil || d.SchemaVersion != 1 || d.Version != 1 {
		t.Fatalf("create %+v %v", d, err)
	}
	first := &form.Submission{OrganizationID: "org-1", FormID: d.ID}
	if err := repo.CreateSubmission(ctx, first); err != nil || first.FormVersion != 1 {
		t.Fatalf("first submission %+v %v", first, err)
	}
	name := "mem2"
	same, err := repo.UpdateDefinition(ctx, "org-1", d.ID, form.UpdateDefinitionRequest{Name: &name, Schema: form.JSONMap{"type": "object"}})
	if err != nil || same.SchemaVersion != 1 || same.Version != 2 {
		t.Fatalf("rename with the same schema: %+v %v", same, err)
	}
	stale := int64(1)
	if _, err = repo.UpdateDefinition(ctx, "org-1", d.ID, form.UpdateDefinitionRequest{Schema: form.JSONMap{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}}, IfMatch: &stale}); err == nil {
		t.Fatal("stale If-Match accepted")
	}
	v2, err := repo.UpdateDefinition(ctx, "org-1", d.ID, form.UpdateDefinitionRequest{Schema: form.JSONMap{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}}})
	if err != nil || v2.SchemaVersion != 2 || v2.Version != 3 {
		t.Fatalf("schema change: %+v %v", v2, err)
	}
	second := &form.Submission{OrganizationID: "org-1", FormID: d.ID}
	if err = repo.CreateSubmission(ctx, second); err != nil || second.FormVersion != 2 || second.FormVersionID != v2.CurrentVersionID {
		t.Fatalf("second submission %+v %v", second, err)
	}
	if err = repo.CreateSubmission(ctx, &form.Submission{OrganizationID: "org-1", FormID: d.ID, ExpectedVersionID: d.CurrentVersionID}); err == nil {
		t.Fatal("submission against an outdated version accepted")
	}
	versions, total, err := repo.ListVersions(ctx, "org-1", d.ID, api.PaginationParams{Limit: 10})
	if err != nil || total != 2 || versions[0].Version != 2 {
		t.Fatalf("versions %+v %d %v", versions, total, err)
	}
	if _, err = repo.GetVersion(ctx, "org-2", d.ID, 1); err == nil {
		t.Fatal("version of another organization found")
	}
}
