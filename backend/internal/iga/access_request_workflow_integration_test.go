package iga_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
)

// TestAccessRequestStartsWorkflowRun covers WP-603 (WFL-04, IGA-01): an
// access request with an active workflow definition for the event
// iga.access_request starts a run of it with the request's data and names
// the run in workflow_run_id; before, the handler stored the definition's id
// there, which the foreign key to workflow_run refused (422). Without a
// definition workflow_run_id stays NULL. The run and the request are stored
// in one transaction: a refused request leaves no run. The actions run after
// the commit: a failing one marks the run failed and keeps the request.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//	  go test -count=1 -run '^TestAccessRequestStartsWorkflowRun$' ./internal/iga/
func TestAccessRequestStartsWorkflowRun(t *testing.T) {
	f := scopetest.Seed(t, "40")
	bg := context.Background()
	requester := f.AppUser(t, f.OrgA, "ar-requester")
	subject := f.AppUser(t, f.OrgA, "ar-subject")
	userB := f.AppUser(t, f.OrgB, "ar-user-b")
	workflows := workflow.NewPGRepository(f.App)
	runs := workflow.NewExecutor(workflows, nil, nil, form.NewPGRepository(f.App), nil)
	router := chi.NewRouter()
	iga.NewHandler(iga.NewPGRepository(f.App), nil, nil, nil, workflows).WithWorkflowRuns(runs).RegisterRoutes(router)
	scope := database.OrgWideScope(f.OrgA, requester)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: requester})

	post := func(body map[string]any) (int, iga.AccessRequest, string) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/iga/access-requests", bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var ar iga.AccessRequest
		if rec.Code == http.StatusCreated {
			if err := json.Unmarshal(rec.Body.Bytes(), &ar); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
		}
		for _, m := range []string{"SQLSTATE", "violates", `table "`, "iga_access_request", "_fkey"} {
			if strings.Contains(rec.Body.String(), m) {
				t.Errorf("POST access request answered %d and discloses database text %q: %s", rec.Code, m, rec.Body.String())
			}
		}
		return rec.Code, ar, rec.Body.String()
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	runCount := func() int {
		return count(`SELECT count(*) FROM workflow_run WHERE organization_id = $1`, f.OrgA)
	}
	storedRun := func(requestID string) *string {
		t.Helper()
		var id *string
		if err := f.Admin.QueryRow(bg, `SELECT workflow_run_id::text FROM iga_access_request WHERE id = $1`, requestID).Scan(&id); err != nil {
			t.Fatalf("access request %s: %v", requestID, err)
		}
		return id
	}
	define := func(name string, trigger workflow.JSONMap, active bool, actions ...workflow.JSONMap) *workflow.Definition {
		t.Helper()
		def := &workflow.Definition{OrganizationID: f.OrgA, Name: name, Trigger: trigger, Actions: actions, Active: active}
		if err := workflows.CreateDefinition(ctx, def); err != nil {
			t.Fatalf("workflow %s: %v", name, err)
		}
		return def
	}
	event := workflow.JSONMap{"type": "event", "event": iga.AccessRequestEvent}

	// Without a definition: no run, workflow_run_id NULL.
	code, ar, body := post(map[string]any{"subject_user_id": subject, "entitlement": "grp-none"})
	if code != http.StatusCreated || ar.WorkflowRunID != "" {
		t.Fatalf("access request without workflow: %d %s, want 201 without workflow_run_id", code, body)
	}
	if id := storedRun(ar.ID); id != nil {
		t.Errorf("workflow_run_id without workflow: %q, want NULL", *id)
	}
	if n := runCount(); n != 0 {
		t.Errorf("workflow runs without workflow: %d, want 0", n)
	}

	// Only the active definition for the event starts a run; one of another
	// event and an inactive one are passed over.
	define("a-other-event", workflow.JSONMap{"type": "event", "event": "ticket.created"}, true, workflow.JSONMap{"type": "noop"})
	define("a-inactive", event, false, workflow.JSONMap{"type": "noop"})
	approval := define("b-access-approval", event, true, workflow.JSONMap{"type": "noop"})
	code, ar, body = post(map[string]any{"subject_user_id": subject, "entitlement": "grp-admins", "reason": "on call"})
	if code != http.StatusCreated || ar.WorkflowRunID == "" {
		t.Fatalf("access request with workflow: %d %s, want 201 with workflow_run_id", code, body)
	}
	if id := storedRun(ar.ID); id == nil || *id != ar.WorkflowRunID {
		t.Errorf("stored workflow_run_id %v, want the run %s of the response", id, ar.WorkflowRunID)
	}
	var workflowID, trigger, status, requestID, subjectID, entitlement string
	if err := f.Admin.QueryRow(bg, `SELECT workflow_id::text, trigger, status, context->>'access_request_id', context->>'subject_user_id', context->>'entitlement'
		FROM workflow_run WHERE id = $1`, ar.WorkflowRunID).Scan(&workflowID, &trigger, &status, &requestID, &subjectID, &entitlement); err != nil {
		t.Fatalf("workflow run %s of the access request: %v", ar.WorkflowRunID, err)
	}
	if workflowID != approval.ID || trigger != iga.AccessRequestEvent || status != workflow.StatusSucceeded {
		t.Errorf("run: workflow %s trigger %q status %q, want %s %q %q", workflowID, trigger, status, approval.ID, iga.AccessRequestEvent, workflow.StatusSucceeded)
	}
	if requestID != ar.ID || subjectID != subject || entitlement != "grp-admins" {
		t.Errorf("run context: access_request_id %q subject_user_id %q entitlement %q, want %s %s grp-admins", requestID, subjectID, entitlement, ar.ID, subject)
	}
	if n := runCount(); n != 1 {
		t.Errorf("workflow runs after one request: %d, want 1", n)
	}

	// A refused request (subject of organization B) starts no run and
	// stores nothing.
	code, _, body = post(map[string]any{"subject_user_id": userB, "entitlement": "grp-admins"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"subject_user_id"`) {
		t.Errorf("access request for a user of organization B: %d %s, want 422 on subject_user_id", code, body)
	}
	if n := runCount(); n != 1 {
		t.Errorf("workflow runs after a refused request: %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM iga_access_request WHERE subject_user_id = $1`, userB); n != 0 {
		t.Errorf("access requests for the organization-B user: %d, want 0", n)
	}

	// A failing action marks the run failed; the request is created and
	// names the run.
	inactive := false
	if _, err := workflows.UpdateDefinition(ctx, f.OrgA, approval.ID, workflow.UpdateDefinitionRequest{Active: &inactive}); err != nil {
		t.Fatalf("deactivate %s: %v", approval.Name, err)
	}
	define("c-access-form", event, true, workflow.JSONMap{"type": "submit_form", "form_id": "00000000-0000-4000-8000-0000000000fe"})
	code, ar, body = post(map[string]any{"subject_user_id": subject, "entitlement": "grp-form"})
	if code != http.StatusCreated || ar.WorkflowRunID == "" {
		t.Fatalf("access request with a failing workflow: %d %s, want 201 with workflow_run_id", code, body)
	}
	if id := storedRun(ar.ID); id == nil || *id != ar.WorkflowRunID {
		t.Errorf("stored workflow_run_id %v, want the run %s of the response", id, ar.WorkflowRunID)
	}
	var stepError string
	if err := f.Admin.QueryRow(bg, `SELECT r.status, s.error FROM workflow_run r JOIN workflow_step s ON s.run_id = r.id WHERE r.id = $1`,
		ar.WorkflowRunID).Scan(&status, &stepError); err != nil {
		t.Fatalf("failed run %s: %v", ar.WorkflowRunID, err)
	}
	if status != workflow.StatusFailed || stepError != form.ErrNotFound.Error() {
		t.Errorf("run with an unknown form: status %q step error %q, want %q %q", status, stepError, workflow.StatusFailed, form.ErrNotFound.Error())
	}
}
