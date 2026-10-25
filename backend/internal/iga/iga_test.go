package iga

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
)

func TestParseUserNameFilter(t *testing.T) {
	got, ok := ParseUserNameFilter(`userName eq "alice@example.test"`)
	if !ok || got != "alice@example.test" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := ParseUserNameFilter(`displayName co "Alice"`); ok {
		t.Fatal("unsupported filter accepted")
	}
}
func TestSCIMConnectorUsesPagingAndPatch(t *testing.T) {
	var sawAuth bool
	var patchPath string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Users":
			if r.URL.Query().Get("startIndex") != "2" || r.URL.Query().Get("count") != "1" {
				t.Fatalf("bad paging: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"totalResults": 1, "Resources": []map[string]any{{"id": "u1", "userName": "a", "active": true}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/Users/u1":
			patchPath = r.URL.Path
			w.WriteHeader(204)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := NewSCIMConnector(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, total, err := c.ReadAccounts(context.Background(), 2, 1)
	if err != nil || total != 1 || len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("bad read %#v %d %v", got, total, err)
	}
	if !sawAuth {
		t.Fatal("bearer auth missing")
	}
	if err := c.DisableAccount(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if patchPath != "/Users/u1" {
		t.Fatalf("patch not called")
	}
	_, err = NewSCIMConnector("http://example.test", "", &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}})
	if err == nil {
		t.Fatal("http base URL accepted")
	}
}
func TestLifecycleResolutionCreatesTasks(t *testing.T) {
	repo := NewMemoryRepository()
	active := true
	p := &LifecyclePolicy{OrganizationID: "org", Name: "join", Event: "joiner", Active: active, Conditions: JSONMap{"department": "IT"}, Actions: []JSONMap{{"connector_id": "c1", "action": TaskActionCreateAccount, "userName": "ignored"}}}
	if err := repo.CreatePolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(repo)
	tasks, err := svc.Resolve(context.Background(), "org", IdentityChange{Event: "joiner", UserID: "u1", Attributes: JSONMap{"department": "IT"}})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d err=%v", len(tasks), err)
	}
	listed, total, _ := repo.ListTasks(context.Background(), "org", "", api.PaginationParams{Limit: 10})
	if total != 1 || listed[0].Action != TaskActionCreateAccount {
		t.Fatalf("task not persisted")
	}
}
func TestTaskRunnerRetryBackoff(t *testing.T) {
	repo := NewMemoryRepository()
	_ = repo.CreateConnector(context.Background(), &ConnectorConfig{OrganizationID: "org", Name: "bad", Type: ConnectorTypeSCIM, BaseURL: "https://bad.invalid"})
	conns, _, _ := repo.ListConnectors(context.Background(), "org", api.PaginationParams{Limit: 1})
	task := &ProvisioningTask{OrganizationID: "org", ConnectorID: conns[0].ID, Action: TaskActionDisableAccount, ExternalID: "u1", MaxAttempts: 3}
	_ = repo.CreateTask(context.Background(), task)
	runner := NewTaskRunner(repo, NewRegistry(nil, &http.Client{Timeout: time.Millisecond}), nil)
	_ = runner.RunTask(context.Background(), task)
	stored, _ := repo.GetTask(context.Background(), "org", task.ID)
	if stored.Status != TaskStatusPending || stored.Attempts != 1 || !stored.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("expected retry with backoff, got %#v", stored)
	}
}
func TestDetectDrift(t *testing.T) {
	findings := DetectDrift("org", "c", []Account{{ID: "expected", UserName: "e"}}, []Account{{ID: "orphan", UserName: "o"}})
	if len(findings) != 2 {
		t.Fatalf("got %d findings", len(findings))
	}
	kinds := map[string]bool{}
	for _, f := range findings {
		kinds[f.DriftType] = true
	}
	if !kinds["orphan_account"] || !kinds["missing_account"] {
		t.Fatalf("bad drift types %#v", kinds)
	}
}

// failingWorkflowLookup makes the lookup of workflow definitions fail with
// a database text.
type failingWorkflowLookup struct{ *workflow.MemoryRepository }

func (failingWorkflowLookup) ListDefinitions(context.Context, string, bool, api.PaginationParams) ([]workflow.Definition, int, error) {
	return nil, 0, errors.New(`ERROR: permission denied for table workflow_def (SQLSTATE 42501)`)
}

// failingRunStart is a RunStarter whose run cannot be stored.
type failingRunStart struct{ *workflow.Executor }

func (failingRunStart) Start(context.Context, string, *workflow.Definition, string, workflow.JSONMap) (*workflow.Run, error) {
	return nil, errors.New(`ERROR: insert into table "workflow_run" failed (SQLSTATE 40001)`)
}

// TestCreateAccessRequestStartsWorkflowRun covers WP-603 (WFL-04, IGA-01)
// with the memory repositories: the active definition for the event
// iga.access_request starts a run that carries the request's data, and
// workflow_run_id names that run, never the definition; without a
// definition or without the executor no run starts. A failed definition
// lookup and a run that cannot be stored answer the sanitized 500 and store
// no request; a run whose action fails keeps the request (201).
// TestAccessRequestStartsWorkflowRun shows it on PostgreSQL.
func TestCreateAccessRequestStartsWorkflowRun(t *testing.T) {
	const org = "00000000-0000-4000-8000-0000000000a1"
	const requester = "00000000-0000-4000-8000-0000000000b1"
	const subject = "00000000-0000-4000-8000-0000000000b2"
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: requester})
	event := workflow.JSONMap{"type": "event", "event": AccessRequestEvent}
	repos := func(defs ...*workflow.Definition) (*MemoryRepository, *workflow.MemoryRepository) {
		t.Helper()
		workflows := workflow.NewMemoryRepository()
		for _, d := range defs {
			d.OrganizationID = org
			if err := workflows.CreateDefinition(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		return NewMemoryRepository(), workflows
	}
	executor := func(repo workflow.Repository) *workflow.Executor {
		return workflow.NewExecutor(repo, nil, nil, nil, nil)
	}
	post := func(h *Handler) (int, AccessRequest, string) {
		t.Helper()
		r := chi.NewRouter()
		h.RegisterRoutes(r)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/iga/access-requests",
			strings.NewReader(`{"subject_user_id":"`+subject+`","entitlement":"grp-admins","reason":"on call"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var ar AccessRequest
		if rec.Code == http.StatusCreated {
			if err := json.Unmarshal(rec.Body.Bytes(), &ar); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
		}
		return rec.Code, ar, rec.Body.String()
	}
	stored := func(repo *MemoryRepository) int {
		t.Helper()
		_, total, err := repo.ListAccessRequests(ctx, org, "", api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	runsOf := func(repo *workflow.MemoryRepository) []workflow.Run {
		t.Helper()
		runs, _, err := repo.ListRuns(ctx, org, "", "", api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return runs
	}

	// The active definition for the event starts the run; a manual one, one
	// of another event and an inactive one are passed over.
	approval := &workflow.Definition{Name: "c-approval", Trigger: event, Active: true, Actions: []workflow.JSONMap{{"type": "require_approval"}}}
	requests, workflows := repos(
		&workflow.Definition{Name: "a-manual", Trigger: workflow.JSONMap{"type": "manual", "event": AccessRequestEvent}, Active: true, Actions: []workflow.JSONMap{{"type": "noop"}}},
		&workflow.Definition{Name: "a-other", Trigger: workflow.JSONMap{"type": "event", "event": "ticket.created"}, Active: true, Actions: []workflow.JSONMap{{"type": "noop"}}},
		&workflow.Definition{Name: "b-inactive", Trigger: event, Active: false, Actions: []workflow.JSONMap{{"type": "noop"}}},
		approval,
	)
	code, ar, body := post(NewHandler(requests, nil, nil, nil, workflows).WithWorkflowRuns(executor(workflows)))
	runs := runsOf(workflows)
	if code != http.StatusCreated || len(runs) != 1 || ar.WorkflowRunID != runs[0].ID {
		t.Fatalf("access request with workflow: %d %s, runs %+v; want 201 naming the one run", code, body, runs)
	}
	run := runs[0]
	if run.WorkflowID != approval.ID || run.Trigger != AccessRequestEvent || run.Status != workflow.StatusWaitingApproval {
		t.Errorf("run: workflow %s trigger %q status %q, want %s %q %q", run.WorkflowID, run.Trigger, run.Status, approval.ID, AccessRequestEvent, workflow.StatusWaitingApproval)
	}
	if run.Context["access_request_id"] != ar.ID || run.Context["subject_user_id"] != subject || run.Context["requester_id"] != requester ||
		run.Context["entitlement"] != "grp-admins" || run.Context["reason"] != "on call" {
		t.Errorf("run context %v, want the data of request %s", run.Context, ar.ID)
	}

	// Without a definition, and with a definition but without the executor,
	// no run starts and workflow_run_id stays empty.
	requests, workflows = repos()
	code, ar, body = post(NewHandler(requests, nil, nil, nil, workflows).WithWorkflowRuns(executor(workflows)))
	if code != http.StatusCreated || ar.WorkflowRunID != "" || len(runsOf(workflows)) != 0 {
		t.Errorf("access request without workflow: %d %s, want 201 without workflow_run_id", code, body)
	}
	requests, workflows = repos(&workflow.Definition{Name: "approval", Trigger: event, Active: true, Actions: []workflow.JSONMap{{"type": "noop"}}})
	code, ar, body = post(NewHandler(requests, nil, nil, nil, workflows))
	if code != http.StatusCreated || ar.WorkflowRunID != "" || len(runsOf(workflows)) != 0 {
		t.Errorf("access request without executor: %d %s, want 201 without workflow_run_id", code, body)
	}

	// A failed definition lookup and a run that cannot be stored answer the
	// sanitized 500 and store no request.
	requests, workflows = repos()
	code, _, body = post(NewHandler(requests, nil, nil, nil, failingWorkflowLookup{workflows}).WithWorkflowRuns(executor(workflows)))
	if code != http.StatusInternalServerError || strings.Contains(body, "SQLSTATE") || strings.Contains(body, "workflow_def") || stored(requests) != 0 {
		t.Errorf("failed workflow lookup: %d %s, %d requests; want the sanitized 500 and none", code, body, stored(requests))
	}
	requests, workflows = repos(&workflow.Definition{Name: "approval", Trigger: event, Active: true, Actions: []workflow.JSONMap{{"type": "noop"}}})
	code, _, body = post(NewHandler(requests, nil, nil, nil, workflows).WithWorkflowRuns(failingRunStart{executor(workflows)}))
	if code != http.StatusInternalServerError || strings.Contains(body, "SQLSTATE") || strings.Contains(body, "workflow_run\"") || stored(requests) != 0 {
		t.Errorf("run not stored: %d %s, %d requests; want the sanitized 500 and none", code, body, stored(requests))
	}

	// An action that fails marks the run failed; the request stays and
	// names the run.
	requests, workflows = repos(&workflow.Definition{Name: "broken", Trigger: event, Active: true, Actions: []workflow.JSONMap{{"type": 42}}})
	code, ar, body = post(NewHandler(requests, nil, nil, nil, workflows).WithWorkflowRuns(executor(workflows)))
	runs = runsOf(workflows)
	if code != http.StatusCreated || len(runs) != 1 || ar.WorkflowRunID != runs[0].ID || runs[0].Status != workflow.StatusFailed || stored(requests) != 1 {
		t.Errorf("failing workflow: %d %s, runs %+v; want 201 naming the failed run", code, body, runs)
	}
}
