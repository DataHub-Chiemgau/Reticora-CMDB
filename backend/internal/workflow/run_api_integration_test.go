package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The run API (WFL-02, API-06, EVT-03, WP-1092): a pending or waiting run is
// cancelled, a failed one retried at its failed step; the run list filters
// by definition, CI, status, trigger and time with a keyset cursor; every
// status change writes its audit entry workflow_run.<status> and the event
// workflow.run_* in the transaction of the change.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestCancelPendingAndWaitingRun|TestCancelRunningRun409|TestRetryFailedRunResumesAtFailedStep|TestCancelRacesApproval|TestRunStatusChangesWriteOutboxEvents|TestRunStatusChangesWriteAudit|TestListRunsFiltersAndCursor|TestRunOfOtherOrganization404' ./internal/workflow/

// switchForms fails GetDefinition while broken is set, like a form store
// that is down until it is repaired.
type switchForms struct {
	form.Repository
	broken atomic.Bool
}

func (f *switchForms) GetDefinition(ctx context.Context, orgID, id string) (*form.Definition, error) {
	if f.broken.Load() {
		return nil, errTransient
	}
	return f.Repository.GetDefinition(ctx, orgID, id)
}

// runAPI serves the run API of exec in the request context of the user of
// e: tenant, scope and the audit request context (a route).
type runAPI struct {
	e      *workerEnv
	router chi.Router
}

func newRunAPI(e *workerEnv, exec *Executor) *runAPI {
	router := chi.NewRouter()
	NewHandler(e.repo, exec).RegisterRoutes(router)
	return &runAPI{e: e, router: router}
}

// call sends a request in ctx with the audit request context of a route.
func (a *runAPI) call(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: fmt.Sprintf("wp1092-%d", time.Now().UnixNano()), Source: audit.SourceAPI})
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) *Run {
	t.Helper()
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("run %s: %v", rec.Body.String(), err)
	}
	return &run
}

// syncExec is an executor of e without job queue: the run is executed in
// the request.
func (e *workerEnv) syncExec(forms form.Repository) *Executor {
	return NewExecutor(e.repo, nil, ci.NewPGRepository(e.f.App), forms, nil)
}

// runEvents lists the types of the outbox events of a run in sequence
// order.
func (e *workerEnv) runEvents(t *testing.T, runID string) []string {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT type FROM event_outbox WHERE organization_id = $1 AND entity_id = $2 ORDER BY sequence`,
		e.f.OrgA, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

// TestCancelPendingAndWaitingRun: a pending run with a queued job and a run
// waiting for its approval are cancelled with their open step; the queued
// job then finds the run ended and executes nothing. waiting_input (WP-1102)
// is cancellable through cancellableStatuses without a change of the
// route.
func TestCancelPendingAndWaitingRun(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	api := newRunAPI(e, e.exec)

	pending := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, pending); err != nil {
		t.Fatal(err)
	}
	rec := api.call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+pending.ID+"/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel pending: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRun(t, rec); got.Status != StatusCancelled || got.FinishedAt == nil {
		t.Errorf("cancelled run: %s finished %v, want cancelled with finished_at", got.Status, got.FinishedAt)
	}
	out, err := NewWorker(e.exec).Run(e.ctx, e.job(t, pending.ID))
	if err != nil {
		t.Fatalf("job of the cancelled run: %v", err)
	}
	if res, _ := out.(RunResult); res.Status != StatusCancelled {
		t.Errorf("job result %+v, want the run cancelled", out)
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, pending.ID); got == nil || got.Status != StatusCancelled || len(got.Steps) != 0 {
		t.Errorf("run after its job: %+v, want cancelled without steps", got)
	}

	sync := e.syncExec(e.forms)
	waiting := e.start(t, e.ctx, JSONMap{"type": "require_approval"}, JSONMap{"type": "noop"})
	if run, err := sync.Execute(e.ctx, e.f.OrgA, nil, waiting); err != nil || run.Status != StatusWaitingApproval {
		t.Fatalf("waiting run: %+v %v", run, err)
	}
	rec = newRunAPI(e, sync).call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+waiting.ID+"/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel waiting: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRun(t, rec)
	if got.Status != StatusCancelled || len(got.Steps) != 1 || got.Steps[0].Status != StatusCancelled {
		t.Errorf("cancelled waiting run: %s steps %+v, want cancelled with the approval step cancelled", got.Status, got.Steps)
	}

	if !slices.Contains(cancellableStatuses, StatusWaitingInput) {
		t.Errorf("cancellableStatuses %v lacks %s", cancellableStatuses, StatusWaitingInput)
	}
	mem := NewMemoryRepository()
	def := &Definition{OrganizationID: "org-1", Name: "wf-input", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := mem.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	run := &Run{OrganizationID: "org-1", WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusWaitingInput, Trigger: "manual"}
	if err := mem.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if out, err := mem.CancelRun(context.Background(), "org-1", run.ID); err != nil || out.Status != StatusCancelled {
		t.Errorf("cancel waiting_input: %+v %v, want cancelled", out, err)
	}
}

// TestCancelRunningRun409: a running and an ended run are not cancelled.
func TestCancelRunningRun409(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	api := newRunAPI(e, e.exec)
	running := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.repo.UpdateRunStatus(e.ctx, e.f.OrgA, running.ID, StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec := api.call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+running.ID+"/cancel", "")
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(ErrRunNotCancellable.Error())) {
		t.Errorf("cancel running: %d %s, want 409 with the fixed text", rec.Code, rec.Body.String())
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, running.ID); got == nil || got.Status != StatusRunning {
		t.Errorf("run after the refused cancel: %+v, want running", got)
	}
	done, err := e.syncExec(e.forms).Trigger(e.ctx, e.f.OrgA, e.definition(t, JSONMap{"type": "noop"}), "manual", JSONMap{})
	if err != nil || done.Status != StatusSucceeded {
		t.Fatalf("succeeded run: %+v %v", done, err)
	}
	if rec = api.call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+done.ID+"/cancel", ""); rec.Code != http.StatusConflict {
		t.Errorf("cancel succeeded: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec = api.call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+done.ID+"/retry", ""); rec.Code != http.StatusConflict ||
		!bytes.Contains(rec.Body.Bytes(), []byte(ErrRunNotRetryable.Error())) {
		t.Errorf("retry succeeded: %d %s, want 409 with the fixed text", rec.Code, rec.Body.String())
	}
}

// definition stores an active manual definition with actions.
func (e *workerEnv) definition(t *testing.T, actions ...JSONMap) *Definition {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-run-api-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"}, Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	return def
}

// TestRetryFailedRunResumesAtFailedStep: step 1 succeeded, step 2
// (submit_form) failed on its only attempt; the retry sets the run pending
// with attempts 0 and queues it, the job continues at step 2 and does not
// repeat step 1.
func TestRetryFailedRunResumesAtFailedStep(t *testing.T) {
	e := newWorkerEnv(t, 1, false)
	forms := &switchForms{Repository: form.NewPGRepository(e.f.App)}
	exec := NewExecutor(e.repo, nil, ci.NewPGRepository(e.f.App), forms, nil).WithQueue(e.queue)
	kind := RunJobKindConfig
	kind.MaxAttempts = 1
	worker := NewWorker(exec)
	kind.Failed = worker.JobFailed
	e.queue.Handle(kind, worker.Run)
	ctx, cancel := context.WithCancel(context.Background())
	go e.queue.Run(ctx)
	t.Cleanup(cancel)

	d := e.form(t, e.ctx, "")
	forms.broken.Store(true)
	run := e.start(t, e.ctx, JSONMap{"type": "noop"}, JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-retry"}})
	if _, err := exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	failed := e.await(t, run.ID)
	if failed.Status != StatusFailed || len(failed.Steps) != 2 || failed.Steps[0].Status != StatusSucceeded || failed.Steps[1].Status != StatusFailed {
		t.Fatalf("run before the retry: %s steps %+v, want failed at step 2", failed.Status, failed.Steps)
	}
	first := failed.Steps[0]

	forms.broken.Store(false)
	rec := newRunAPI(e, exec).call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRun(t, rec); got.Status != StatusPending || got.Attempts != 0 || got.FinishedAt != nil {
		t.Errorf("retried run: %s attempts %d finished %v, want pending with attempts 0", got.Status, got.Attempts, got.FinishedAt)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusSucceeded || len(done.Steps) != 2 {
		t.Fatalf("run after the retry: %s %q steps %+v, want succeeded", done.Status, done.LastError, done.Steps)
	}
	if s := done.Steps[0]; s.ID != first.ID || !s.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("step 1 after the retry: %+v, want it untouched (%+v)", s, first)
	}
	if done.Steps[1].Status != StatusSucceeded {
		t.Errorf("step 2 after the retry: %+v, want succeeded", done.Steps[1])
	}
	if n, _ := e.submissions(t, d.ID); n != 1 {
		t.Errorf("submissions: %d, want 1", n)
	}
	if got := e.runEvents(t, run.ID); !slices.Equal(got, []string{"workflow.run_started", "workflow.run_failed", "workflow.run_succeeded"}) {
		t.Errorf("events of the run: %v, want started, failed, succeeded (the retry is no new start)", got)
	}

	// A run that fails before its first step (an action type that is no
	// string) has no step after it started either: its retry is still no
	// new start, exactly one run_started (AK4).
	sync := e.syncExec(e.forms)
	early := e.start(t, e.ctx, JSONMap{"type": 5})
	if out, err := sync.Execute(e.ctx, e.f.OrgA, nil, early); err == nil && out.Status != StatusFailed {
		t.Fatalf("run with an invalid action: %+v, want failed", out)
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, early.ID); got == nil || got.Status != StatusFailed || len(got.Steps) != 0 {
		t.Fatalf("run before its retry: %+v, want failed without steps", got)
	}
	// Without a queue the retry executes the run in the request: it fails
	// again on its action (400 with the rule's text).
	if rec = newRunAPI(e, sync).call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+early.ID+"/retry", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("retry of the early failed run: %d %s", rec.Code, rec.Body.String())
	}
	if got := e.runEvents(t, early.ID); !slices.Equal(got, []string{"workflow.run_started", "workflow.run_failed", "workflow.run_failed"}) {
		t.Errorf("events of the early failed run: %v, want one run_started and a run_failed per attempt", got)
	}
}

// TestCancelRacesApproval: a cancel and an approval of the same waiting
// run do not both win (WP-1092 AK1). The decision is taken under the row
// lock of the run: after a committed cancel the approval is ErrNotWaiting,
// an ended run keeps its status against a late status write, and with both
// requests at once the run ends cancelled without executing its next step.
func TestCancelRacesApproval(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	sync := e.syncExec(e.forms)
	waiting := func() *Run {
		run := e.start(t, e.ctx, JSONMap{"type": "require_approval", "exclude_initiator": false}, JSONMap{"type": "noop"})
		if out, err := sync.Execute(e.ctx, e.f.OrgA, nil, run); err != nil || out.Status != StatusWaitingApproval {
			t.Fatalf("waiting run: %+v %v", out, err)
		}
		return run
	}

	run := waiting()
	if _, err := e.exec.Cancel(e.ctx, e.f.OrgA, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.DecideApproval(e.ctx, e.f.OrgA, run.ID, true, JSONMap{}); !errors.Is(err, ErrNotWaiting) {
		t.Errorf("approval after the cancel: %v, want ErrNotWaiting", err)
	}
	if _, err := e.repo.UpdateRunStatus(e.ctx, e.f.OrgA, run.ID, StatusPending, nil, nil); !errors.Is(err, ErrRunEnded) {
		t.Errorf("late status write on the cancelled run: %v, want ErrRunEnded", err)
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID); got == nil || got.Status != StatusCancelled || len(got.Steps) != 1 || got.Steps[0].Status != StatusCancelled {
		t.Errorf("cancelled run: %+v, want cancelled with its approval step cancelled", got)
	}

	for i := 0; i < 5; i++ {
		run := waiting()
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-start
			_, err := e.exec.Cancel(e.ctx, e.f.OrgA, run.ID)
			errs <- err
		}()
		go func() {
			<-start
			_, err := e.exec.Approve(e.ctx, e.f.OrgA, run.ID, "approved", "")
			errs <- err
		}()
		close(start)
		for range 2 {
			if err := <-errs; err != nil && !errors.Is(err, ErrNotWaiting) {
				t.Fatalf("cancel or approval: %v", err)
			}
		}
		// A job queued by the approval finds the run cancelled.
		if job := e.jobOf(t, run.ID); job != nil {
			if _, err := NewWorker(e.exec).Run(e.ctx, job); err != nil {
				t.Fatalf("job of the run: %v", err)
			}
		}
		got, err := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusCancelled || len(got.Steps) != 1 {
			t.Errorf("run %d after cancel and approval: %s steps %+v, want cancelled without its second step", i, got.Status, got.Steps)
		}
	}
}

// jobOf returns the last job workflow.run of a run, nil without one.
func (e *workerEnv) jobOf(t *testing.T, runID string) *jobs.Job {
	t.Helper()
	var id string
	err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM job WHERE organization_id = $1 AND kind = $2 AND params->>'run_id' = $3
		ORDER BY created_at DESC LIMIT 1`, e.f.OrgA, RunJobKind, runID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	job, err := e.jobs.Get(context.Background(), e.f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// TestRunStatusChangesWriteOutboxEvents: a run executed in the request and
// one executed by the worker write workflow.run_started and then
// workflow.run_succeeded with the payload of the run; while the outbox
// refuses run_succeeded the change to succeeded is rolled back and leaves
// no event.
func TestRunStatusChangesWriteOutboxEvents(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	ciID := e.f.CI(t, e.f.OrgA, e.f.Client1, fmt.Sprintf("wp1092-ci-%d", time.Now().UnixNano()))
	run := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	if done := e.await(t, run.ID); done.Status != StatusSucceeded {
		t.Fatalf("worker run: %s", done.Status)
	}
	if got := e.runEvents(t, run.ID); !slices.Equal(got, []string{"workflow.run_started", "workflow.run_succeeded"}) {
		t.Errorf("events of the worker run: %v, want run_started, run_succeeded", got)
	}
	var raw []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT data FROM event_outbox WHERE organization_id = $1 AND entity_id = $2
		AND type = 'workflow.run_succeeded'`, e.f.OrgA, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d["run_id"] != run.ID || d["id"] != run.ID || d["workflow_id"] != run.WorkflowID || d["definition_version_id"] != run.DefinitionVersionID ||
		d["ci_id"] != ciID || d["client_id"] != e.f.Client1 || d["trigger"] != "manual" || d["status"] != StatusSucceeded {
		t.Errorf("payload %v", d)
	}

	sync := e.syncExec(e.forms)
	def := e.definition(t, JSONMap{"type": "noop"})
	direct, err := sync.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil || direct.Status != StatusSucceeded {
		t.Fatalf("run in the request: %+v %v", direct, err)
	}
	if got := e.runEvents(t, direct.ID); !slices.Equal(got, []string{"workflow.run_started", "workflow.run_succeeded"}) {
		t.Errorf("events of the run in the request: %v", got)
	}

	// The outbox refuses run_succeeded of the organization: the change
	// to succeeded is rolled back with it.
	fn := fmt.Sprintf("wp1092_refuse_%d", time.Now().UnixNano())
	bg := context.Background()
	if _, err = e.f.Admin.Exec(bg, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.organization_id = '`+e.f.OrgA+`' AND NEW.type = 'workflow.run_succeeded' THEN
		RAISE EXCEPTION 'wp1092: the outbox refuses %', NEW.type;
	END IF;
	RETURN NEW;
END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`) })
	if _, err = e.f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE INSERT ON event_outbox FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	refused, err := sync.Start(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sync.Execute(e.ctx, e.f.OrgA, nil, refused); err == nil {
		t.Fatal("the run succeeded while the outbox refused its event")
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, refused.ID); got == nil || got.Status != StatusRunning || got.FinishedAt != nil {
		t.Errorf("run after the refused event: %+v, want still running (the change rolled back)", got)
	}
	if got := e.runEvents(t, refused.ID); !slices.Equal(got, []string{"workflow.run_started"}) {
		t.Errorf("events after the refused change: %v, want only run_started", got)
	}
	var audited int
	if err = e.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = 'workflow_run.succeeded'`,
		e.f.OrgA, refused.ID).Scan(&audited); err != nil || audited != 0 {
		t.Errorf("audit entries of the rolled back change: %d %v, want 0", audited, err)
	}
}

// TestRunStatusChangesWriteAudit: the cancel route audits
// workflow_run.cancelled with origin workflow_run and the user of the
// route; the worker audits workflow_run.running and workflow_run.succeeded
// without a user.
func TestRunStatusChangesWriteAudit(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	run := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	if done := e.await(t, run.ID); done.Status != StatusSucceeded {
		t.Fatalf("worker run: %s", done.Status)
	}
	type entry struct{ action, actor, actorType, resource, old string }
	entries := func(runID string) []entry {
		t.Helper()
		rows, err := e.f.Admin.Query(context.Background(), `SELECT action, COALESCE(actor_id::text, ''), actor_type, resource_type,
			COALESCE(changes->'status'->>'old', '') FROM audit_log WHERE organization_id = $1 AND resource_id = $2 ORDER BY seq`, e.f.OrgA, runID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []entry
		for rows.Next() {
			var en entry
			if err := rows.Scan(&en.action, &en.actor, &en.actorType, &en.resource, &en.old); err != nil {
				t.Fatal(err)
			}
			out = append(out, en)
		}
		return out
	}
	want := []entry{{"workflow_run.running", "", "system", "workflow_run", "pending"}, {"workflow_run.succeeded", "", "system", "workflow_run", "running"}}
	if got := entries(run.ID); !slices.Equal(got, want) {
		t.Errorf("audit of the worker run: %+v, want %+v", got, want)
	}

	waiting := e.start(t, e.ctx, JSONMap{"type": "noop"})
	rec := newRunAPI(e, e.exec).call(t, e.ctx, http.MethodPost, "/api/v1/workflow-runs/"+waiting.ID+"/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	want = []entry{{"workflow_run.cancelled", e.user, "user", "workflow_run", "pending"}}
	if got := entries(waiting.ID); !slices.Equal(got, want) {
		t.Errorf("audit of the cancel route: %+v, want %+v", got, want)
	}
}

// TestListRunsFiltersAndCursor: the run list filters by ci_id, status,
// trigger and from/to, pages with next_cursor and refuses malformed
// filters with 422 on their field.
func TestListRunsFiltersAndCursor(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	api := newRunAPI(e, e.exec)
	sync := e.syncExec(e.forms)
	tag := time.Now().UnixNano()
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, fmt.Sprintf("wp1092-list-1-%d", tag))
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client1, fmt.Sprintf("wp1092-list-2-%d", tag))
	def := e.definition(t, JSONMap{"type": "noop"})
	eventDef := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-run-api-event-%d", tag), Trigger: JSONMap{"type": "event", "event": "ci.updated"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, eventDef); err != nil {
		t.Fatal(err)
	}
	start := func(d *Definition, trigger, ciID string) *Run {
		t.Helper()
		payload := JSONMap{}
		if ciID != "" {
			payload["ci_id"] = ciID
		}
		run, err := sync.Start(e.ctx, e.f.OrgA, d, trigger, payload)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	a := start(def, "manual", ci1)
	b := start(def, "manual", ci1)
	c := start(eventDef, "ci.updated", ci2)
	old := start(def, "manual", "")
	if _, err := sync.Execute(e.ctx, e.f.OrgA, nil, b); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE workflow_run SET created_at = now() - interval '10 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	list := func(query string) (ids []string, next string) {
		t.Helper()
		rec := api.call(t, e.ctx, http.MethodGet, "/api/v1/workflow-runs?"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
		}
		var page struct {
			Data       []Run  `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Data {
			ids = append(ids, r.ID)
		}
		return ids, page.NextCursor
	}
	q := func(kv ...string) string {
		v := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v.Encode()
	}
	if got, _ := list(q("ci_id", ci1)); !slices.Equal(got, []string{b.ID, a.ID}) {
		t.Errorf("ci_id: %v, want %v", got, []string{b.ID, a.ID})
	}
	if got, _ := list(q("ci_id", ci1, "status", StatusSucceeded)); !slices.Equal(got, []string{b.ID}) {
		t.Errorf("ci_id+status: %v, want [%s]", got, b.ID)
	}
	if got, _ := list(q("trigger", "ci.updated", "workflow_id", eventDef.ID)); !slices.Equal(got, []string{c.ID}) {
		t.Errorf("trigger: %v, want [%s]", got, c.ID)
	}
	from := time.Now().UTC().Add(-11 * 24 * time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(-9 * 24 * time.Hour).Format(time.RFC3339)
	if got, _ := list(q("workflow_id", def.ID, "from", from, "to", to)); !slices.Equal(got, []string{old.ID}) {
		t.Errorf("from/to: %v, want [%s]", got, old.ID)
	}
	first, next := list(q("workflow_id", def.ID, "limit", "1"))
	if len(first) != 1 || first[0] != b.ID || next == "" {
		t.Fatalf("first page: %v next %q, want [%s] with a cursor", first, next, b.ID)
	}
	second, _ := list(q("workflow_id", def.ID, "limit", "1", "cursor", next))
	if !slices.Equal(second, []string{a.ID}) {
		t.Errorf("second page: %v, want [%s]", second, a.ID)
	}
	for _, bad := range []struct{ query, field string }{
		{q("ci_id", "not-a-uuid"), "ci_id"},
		{q("status", "sleeping"), "status"},
		{q("from", "yesterday"), "from"},
		{q("to", "2026-13-01"), "to"},
	} {
		rec := api.call(t, e.ctx, http.MethodGet, "/api/v1/workflow-runs?"+bad.query, "")
		if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte(`"field":"`+bad.field+`"`)) {
			t.Errorf("%s: %d %s, want 422 on %s", bad.query, rec.Code, rec.Body.String(), bad.field)
		}
	}
}

// TestRunOfOtherOrganization404: a run of organization A is unknown to
// organization B for reading, cancelling and retrying (TEN-04).
func TestRunOfOtherOrganization404(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	api := newRunAPI(e, e.exec)
	run := e.start(t, e.ctx, JSONMap{"type": "noop"})
	userB := e.f.AppUser(t, e.f.OrgB, "wf-run-api-b")
	scope := database.OrgWideScope(e.f.OrgB, userB)
	ctxB := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: e.f.OrgB, UserID: userB})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/workflow-runs/" + run.ID},
		{http.MethodPost, "/api/v1/workflow-runs/" + run.ID + "/cancel"},
		{http.MethodPost, "/api/v1/workflow-runs/" + run.ID + "/retry"},
	} {
		if rec := api.call(t, ctxB, c.method, c.path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s from organization B: %d %s, want 404", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if got, err := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID); err != nil || got.Status != StatusPending {
		t.Errorf("run of A after the calls of B: %+v %v, want pending", got, err)
	}
	if _, err := e.repo.CancelRun(ctxB, e.f.OrgB, run.ID); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("CancelRun from B: %v, want ErrRunNotFound", err)
	}
}
