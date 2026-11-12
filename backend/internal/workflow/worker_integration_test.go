package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Durable, idempotent run execution as the job workflow.run (WFL-02,
// WP-1088, E-226 option 2): a run is executed by the job queue over NATS
// JetStream with retries; a succeeded step never runs twice, an
// interrupted non-idempotent step is not repeated, and the worker runs as
// the initiator within the scope snapshot of the job.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestWorkerAbortAfterStep1DoesNotRepeatStep1|TestInterruptedNonIdempotentStepFailsRun|TestRunRetriesWithBackoff|TestRunFailsAfterMaxAttempts|TestWorkerUsesInitiatorAndScope|TestTriggerRunReturnsPendingRun|TestRunRetriesAfterInfrastructureError|TestRunFailsWhenLastAttemptAborts|TestJobFailedClosesLostRun' ./internal/workflow/

// workerEnv is one organization's workflow engine on PostgreSQL with the
// job queue on NATS.
type workerEnv struct {
	f     *scopetest.Fixture
	repo  *PGRepository
	forms *flakyForms
	exec  *Executor
	jobs  *jobs.PGRepository
	queue *jobs.Queue
	user  string
	ctx   context.Context // the user, org-wide
}

// flakyForms fails GetDefinition for the first fail calls (all with
// fail < 0) like a transient error of the step.
type flakyForms struct {
	form.Repository
	fail  int32
	calls atomic.Int32
}

var errTransient = errors.New("form store temporarily unavailable")

func (f *flakyForms) GetDefinition(ctx context.Context, orgID, id string) (*form.Definition, error) {
	n := f.calls.Add(1)
	if f.fail < 0 || n <= f.fail {
		return nil, errTransient
	}
	return f.Repository.GetDefinition(ctx, orgID, id)
}

// flakyRuns fails ListSteps for the first fail calls like a passing
// database error outside a step.
type flakyRuns struct {
	Repository
	fail  int32
	calls atomic.Int32
}

func (f *flakyRuns) ListSteps(ctx context.Context, orgID, runID string) ([]Step, error) {
	if f.calls.Add(1) <= f.fail {
		return nil, errTransient
	}
	return f.Repository.ListSteps(ctx, orgID, runID)
}

// newWorkerEnv starts the job queue with the kind workflow.run of the
// configuration, maxAttempts and a short backoff, unless run is false (the
// queue then only stores jobs).
func newWorkerEnv(t *testing.T, maxAttempts int, run bool) *workerEnv {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wf-worker")
	scope := database.OrgWideScope(f.OrgA, user)
	e := &workerEnv{f: f, repo: NewPGRepository(f.App), forms: &flakyForms{Repository: form.NewPGRepository(f.App)},
		jobs: jobs.NewPGRepository(f.App), user: user,
		ctx: tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})}
	e.exec = NewExecutor(e.repo, nil, ci.NewPGRepository(f.App), e.forms, nil)

	nc, err := nats.Connect(url, nats.Name("wp1088"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	e.queue = jobs.NewQueue(jobs.NewService(e.jobs), js)
	e.queue.Stream, e.queue.SubjectPrefix = "JOBS_WP1088_"+suffix, "wp1088_"+suffix+"."
	e.queue.AckMargin, e.queue.SweepInterval = 500*time.Millisecond, time.Hour
	if err = e.queue.Setup(bg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(bg, e.queue.Stream) })
	kind := RunJobKindConfig
	kind.MaxAttempts = maxAttempts
	kind.Backoff = func(int) time.Duration { return 50 * time.Millisecond }
	worker := NewWorker(e.exec)
	kind.Failed = worker.JobFailed
	e.queue.Handle(kind, worker.Run)
	e.exec.WithQueue(e.queue)
	if run {
		ctx, cancel := context.WithCancel(bg)
		go e.queue.Run(ctx)
		t.Cleanup(cancel)
	}
	return e
}

// form stores an active form of clientID with a required hostname.
func (e *workerEnv) form(t *testing.T, ctx context.Context, clientID string) *form.Definition {
	t.Helper()
	d := &form.Definition{OrganizationID: e.f.OrgA, ClientID: clientID, Name: fmt.Sprintf("wf-worker-form-%d", time.Now().UnixNano()), Active: true,
		Schema: form.JSONMap{"type": "object", "required": []any{"hostname"}, "properties": map[string]any{"hostname": map[string]any{"type": "string"}}}}
	if err := e.forms.CreateDefinition(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}

// start stores a definition with actions and starts a run of it in ctx.
func (e *workerEnv) start(t *testing.T, ctx context.Context, actions ...JSONMap) *Run {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-worker-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"}, Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Start(ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// await waits until the run ends (succeeded, failed, cancelled).
func (e *workerEnv) await(t *testing.T, runID string) *Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		run, err := e.repo.GetRun(e.ctx, e.f.OrgA, runID)
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case StatusSucceeded, StatusFailed, StatusCancelled:
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s (attempts %d, last error %q)", runID, run.Status, run.Attempts, run.LastError)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// job returns the job workflow.run of a run.
func (e *workerEnv) job(t *testing.T, runID string) *jobs.Job {
	t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM job WHERE organization_id = $1 AND idempotency_key = $2`,
		e.f.OrgA, RunJobKey(runID)).Scan(&id); err != nil {
		t.Fatalf("job of run %s: %v", runID, err)
	}
	j, err := e.jobs.Get(context.Background(), e.f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// awaitJob waits until the job of a run has status want (the queue records
// the end of the job after the handler closed the run) and returns it.
func (e *workerEnv) awaitJob(t *testing.T, runID string, want jobs.Status) *jobs.Job {
	t.Helper()
	j := e.job(t, runID)
	deadline := time.Now().Add(10 * time.Second)
	for j.Status != want && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		j = e.job(t, runID)
	}
	return j
}

// submissions counts the submissions of a form.
func (e *workerEnv) submissions(t *testing.T, formID string) (n int, by []string) {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT COALESCE(submitted_by::text, '') FROM form_submission WHERE form_id = $1`, formID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		by = append(by, s)
	}
	return len(by), by
}

// TestWorkerAbortAfterStep1DoesNotRepeatStep1: the worker dies after step
// 1 (submit_form, not idempotent); the retry continues at step 2 and step
// 1 has stored exactly one submission.
func TestWorkerAbortAfterStep1DoesNotRepeatStep1(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	d := e.form(t, e.ctx, "")
	var aborted atomic.Bool
	e.exec.afterStep = func(i int) error {
		if i == 0 && aborted.CompareAndSwap(false, true) {
			return errors.New("worker died after step 1")
		}
		return nil
	}
	run := e.start(t, e.ctx, JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-1"}}, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusSucceeded || done.Attempts != 2 {
		t.Fatalf("run after the abort: %s attempts %d %q, want succeeded after 2 attempts", done.Status, done.Attempts, done.LastError)
	}
	if n, _ := e.submissions(t, d.ID); n != 1 {
		t.Errorf("submissions of step 1: %d, want 1 (step 1 repeated)", n)
	}
	if len(done.Steps) != 2 || done.Steps[0].Status != StatusSucceeded || done.Steps[1].Status != StatusSucceeded {
		t.Errorf("steps: %+v, want two succeeded", done.Steps)
	}
	if j := e.awaitJob(t, run.ID, jobs.StatusSucceeded); j.Status != jobs.StatusSucceeded || j.Attempts != 2 {
		t.Errorf("job: %s attempts %d, want succeeded after 2", j.Status, j.Attempts)
	}
}

// TestInterruptedNonIdempotentStepFailsRun: a create_ticket step an
// attempt left running is not executed again; the step fails with the
// fixed text and so does the run. An interrupted idempotent step (noop)
// is repeated.
func TestInterruptedNonIdempotentStepFailsRun(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	run := e.start(t, e.ctx, JSONMap{"type": "create_ticket", "title": "once"}, JSONMap{"type": "noop"})
	if err := e.repo.AppendStep(e.ctx, &Step{OrganizationID: e.f.OrgA, RunID: run.ID, StepIndex: 0, ActionType: "create_ticket", Status: StatusRunning, Input: JSONMap{"type": "create_ticket"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusFailed || done.LastError != stepInterruptedText {
		t.Fatalf("run: %s %q, want failed %q", done.Status, done.LastError, stepInterruptedText)
	}
	if len(done.Steps) != 1 || done.Steps[0].Status != StatusFailed || done.Steps[0].Error != stepInterruptedText {
		t.Errorf("steps: %+v, want step 0 failed %q and no step 1", done.Steps, stepInterruptedText)
	}

	idem := e.start(t, e.ctx, JSONMap{"type": "noop"}, JSONMap{"type": "noop"})
	if err := e.repo.AppendStep(e.ctx, &Step{OrganizationID: e.f.OrgA, RunID: idem.ID, StepIndex: 0, ActionType: "noop", Status: StatusRunning, Input: JSONMap{"type": "noop"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, idem); err != nil {
		t.Fatal(err)
	}
	if done = e.await(t, idem.ID); done.Status != StatusSucceeded || len(done.Steps) != 2 {
		t.Errorf("run with an interrupted noop: %s steps %+v, want succeeded with 2 steps", done.Status, done.Steps)
	}
}

// TestRunRetriesWithBackoff: the step fails in attempt 1 (transient) and
// succeeds in attempt 2; the run records attempts = 2 and stays running
// in between.
func TestRunRetriesWithBackoff(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	d := e.form(t, e.ctx, "")
	e.forms.fail = 1
	run := e.start(t, e.ctx, JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-2"}})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusSucceeded || done.Attempts != 2 {
		t.Fatalf("run: %s attempts %d %q, want succeeded with attempts 2", done.Status, done.Attempts, done.LastError)
	}
	if done.LastError != errTransient.Error() || done.NextAttemptAt != nil {
		t.Errorf("last_error %q next_attempt_at %v, want the error of attempt 1 and no next attempt", done.LastError, done.NextAttemptAt)
	}
	if len(done.Steps) != 1 || done.Steps[0].Status != StatusSucceeded {
		t.Errorf("steps: %+v, want the failed step executed again", done.Steps)
	}
	if n, _ := e.submissions(t, d.ID); n != 1 {
		t.Errorf("submissions: %d, want 1", n)
	}
}

// TestRunFailsAfterMaxAttempts: a step that fails in every attempt fails
// the run after the last one, with the sanitized error; the job fails.
func TestRunFailsAfterMaxAttempts(t *testing.T) {
	e := newWorkerEnv(t, 3, true)
	d := e.form(t, e.ctx, "")
	e.forms.fail = -1
	run := e.start(t, e.ctx, JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-3"}})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusFailed || done.Attempts != 3 || done.LastError != errTransient.Error() || done.FinishedAt == nil {
		t.Fatalf("run: %s attempts %d %q, want failed after 3 attempts", done.Status, done.Attempts, done.LastError)
	}
	j := e.job(t, run.ID)
	deadline := time.Now().Add(10 * time.Second)
	for j.Status != jobs.StatusFailed && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		j = e.job(t, run.ID)
	}
	if j.Status != jobs.StatusFailed || j.Attempts != 3 || j.Error != errTransient.Error() {
		t.Errorf("job: %s attempts %d %q, want failed after 3", j.Status, j.Attempts, j.Error)
	}
}

// TestWorkerUsesInitiatorAndScope: the worker executes as the user who
// started the run (submitted_by) within the scope snapshot of the job: a
// principal of client 1 does not reach a CI of client 2.
func TestWorkerUsesInitiatorAndScope(t *testing.T) {
	e := newWorkerEnv(t, 2, true)
	scope := database.OrgWideScope(e.f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(e.f.Client1)
	client1 := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: e.f.Client1, UserID: e.user})
	d := e.form(t, client1, e.f.Client1)
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wf-worker-ci-client2")
	run := e.start(t, client1, JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-4"}},
		JSONMap{"type": "set_ci_field", "ci_id": ci2, "field": "status", "value": "maintenance"})
	if run.InitiatedBy != e.user {
		t.Fatalf("initiated_by %q, want %s", run.InitiatedBy, e.user)
	}
	if _, err := e.exec.Execute(client1, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if j := e.job(t, run.ID); j.Scope == nil || len(j.Scope.Clients) != 1 || j.Scope.Clients[0] != e.f.Client1 || j.CreatedBy != e.user {
		t.Errorf("job scope %+v created_by %q, want client 1 and the initiator", j.Scope, j.CreatedBy)
	}
	if n, by := e.submissions(t, d.ID); n != 1 || by[0] != e.user {
		t.Errorf("submissions %d by %v, want one by the initiator %s", n, by, e.user)
	}
	if done.Status != StatusFailed || len(done.Steps) != 2 || done.Steps[1].Status != StatusFailed {
		t.Fatalf("run: %s steps %+v, want set_ci_field on the CI of client 2 failed", done.Status, done.Steps)
	}
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ci WHERE id = $1`, ci2).Scan(&status); err != nil || status != "active" {
		t.Errorf("CI of client 2: %q %v, want unchanged active", status, err)
	}
}

// TestTriggerRunReturnsPendingRun: POST /workflows/{id}/runs answers 201
// with the run pending and queues one job workflow.run with the key of the
// run; a second Execute of the run queues no second job.
func TestTriggerRunReturnsPendingRun(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	router := chi.NewRouter()
	NewHandler(e.repo, e.exec).RegisterRoutes(router)
	def := &Definition{OrganizationID: e.f.OrgA, Name: "wf-worker-pending", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+def.ID+"/runs", bytes.NewReader([]byte(`{"trigger":"manual","context":{}}`))).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST runs: %d %s", rec.Code, rec.Body.String())
	}
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusPending || run.InitiatedBy != e.user {
		t.Errorf("run: %s initiated_by %q, want pending by %s", run.Status, run.InitiatedBy, e.user)
	}
	j := e.job(t, run.ID)
	if j.Kind != RunJobKind || j.Status != jobs.StatusQueued || j.CreatedBy != e.user {
		t.Errorf("job: %+v, want queued %s by the initiator", j, RunJobKind)
	}
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, &run); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2 AND idempotency_key = $3`,
		e.f.OrgA, RunJobKind, RunJobKey(run.ID)).Scan(&n); err != nil || n != 1 {
		t.Errorf("jobs of the run after a second Execute: %d %v, want 1", n, err)
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID); got == nil || got.Status != StatusPending || len(got.Steps) != 0 {
		t.Errorf("run without a worker: %+v, want pending without steps", got)
	}
}

// TestRunRetriesAfterInfrastructureError: an error outside a step
// (ListSteps fails once) does not end the run: the job retries it, attempt
// 2 succeeds, last_error holds the fixed text of attempt 1.
func TestRunRetriesAfterInfrastructureError(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	e.exec.repo = &flakyRuns{Repository: e.repo, fail: 1}
	run := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusSucceeded || done.Attempts != 2 || done.LastError != runInterruptedText {
		t.Fatalf("run: %s attempts %d %q, want succeeded in attempt 2 with %q", done.Status, done.Attempts, done.LastError, runInterruptedText)
	}
	if j := e.awaitJob(t, run.ID, jobs.StatusSucceeded); j.Status != jobs.StatusSucceeded || j.Attempts != 2 {
		t.Errorf("job: %s attempts %d, want succeeded after 2", j.Status, j.Attempts)
	}
}

// TestRunFailsWhenLastAttemptAborts: with one attempt, an attempt that
// ends outside a failed step (the worker stops after step 1) fails the
// run for good with the fixed text; the job fails as well.
func TestRunFailsWhenLastAttemptAborts(t *testing.T) {
	e := newWorkerEnv(t, 1, true)
	e.exec.afterStep = func(int) error { return errors.New("worker stopped") }
	run := e.start(t, e.ctx, JSONMap{"type": "noop"}, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := e.await(t, run.ID)
	if done.Status != StatusFailed || done.Attempts != 1 || done.LastError != runInterruptedText || done.FinishedAt == nil {
		t.Fatalf("run: %s attempts %d %q, want failed after 1 attempt with %q", done.Status, done.Attempts, done.LastError, runInterruptedText)
	}
	if j := e.awaitJob(t, run.ID, jobs.StatusFailed); j.Status != jobs.StatusFailed {
		t.Errorf("job: %s, want failed", j.Status)
	}
}

// TestJobFailedClosesLostRun: a job that failed for good while its run is
// still running (the worker of the last attempt was lost) closes the run
// with the fixed text; a run that ended is left alone.
func TestJobFailedClosesLostRun(t *testing.T) {
	e := newWorkerEnv(t, 1, false)
	run := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.repo.UpdateRunStatus(e.ctx, e.f.OrgA, run.ID, StatusRunning, nil, run.Context); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(RunJobParams{RunID: run.ID})
	job := &jobs.Job{ID: "lost", OrganizationID: e.f.OrgA, Kind: RunJobKind, CreatedBy: e.user, Params: params, Attempts: 1, MaxAttempts: 1, Status: jobs.StatusFailed}
	worker := NewWorker(e.exec)
	worker.JobFailed(context.Background(), job)
	got, err := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID)
	if err != nil || got.Status != StatusFailed || got.LastError != runLostText || got.FinishedAt == nil {
		t.Fatalf("run after the failed job: %+v %v, want failed with %q", got, err, runLostText)
	}
	finished := *got.FinishedAt
	worker.JobFailed(context.Background(), job)
	if again, _ := e.repo.GetRun(e.ctx, e.f.OrgA, run.ID); again == nil || !again.FinishedAt.Equal(finished) {
		t.Errorf("a second call changed the ended run: %+v", again)
	}
}
