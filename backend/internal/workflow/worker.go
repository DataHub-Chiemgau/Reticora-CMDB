package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// A workflow run is executed outside the request as the job workflow.run
// (WFL-02, WP-1088, E-226 option 2): retries with backoff come from the job
// queue, idempotence per step from the key (run_id, step_index) of
// workflow_step (Executor.resume).

// RunJobKind is the job kind that executes one workflow run.
const RunJobKind = "workflow.run"

// RunJobKindConfig is the registration of RunJobKind (server/jobkinds.go):
// five attempts, five minutes each, backoff from 10 s doubling to 5 min.
var RunJobKindConfig = jobs.Kind{Name: RunJobKind, MaxAttempts: 5, Timeout: 5 * time.Minute,
	Backoff: jobs.ExponentialBackoff(10*time.Second, 5*time.Minute)}

// RunJobParams are the parameters of a workflow.run job.
type RunJobParams struct {
	RunID string `json:"run_id"`
}

// RunJobKey is the idempotency key of the job of a run: a run is queued
// once, a second Execute returns the same job.
func RunJobKey(runID string) string { return "workflow.run:" + runID }

// idempotentActions may be executed again when an attempt ended while
// their step was running: a second execution has the effect of the first
// (noop, set_ci_field writes the same value, require_approval waits again).
var idempotentActions = map[string]bool{"noop": true, "set_ci_field": true, "require_approval": true}

// stepInterruptedText is the error of a non-idempotent step an attempt left
// running: whether its action took effect is unknown, it is not repeated.
const stepInterruptedText = "step interrupted, not repeated"

// attempt is the job attempt that executes a run; its zero value is the run
// in the request (no queue), which is not retried.
type attempt struct {
	job     bool
	number  int
	max     int
	backoff func(int) time.Duration
}

// retries reports whether a failed step is retried by a further attempt.
func (a attempt) retries() bool { return a.job && a.number < a.max && a.backoff != nil }

// RunRetryError ends an attempt whose step failed while attempts remain:
// the job queue retries the run after its backoff. Text is the sanitized
// step error.
type RunRetryError struct{ Text string }

// validation: the sanitized step error (stepErrorText).
func (e *RunRetryError) Error() string { return e.Text }

// RunFailedError ends the last attempt of a run whose step failed: the run
// and the job fail for good.
type RunFailedError struct{ Text string }

// validation: the sanitized step error (stepErrorText).
func (e *RunFailedError) Error() string { return e.Text }

// errRunJobParams: the job carries no run id.
var errRunJobParams = errors.New("workflow.run job without run_id")

// Worker executes the jobs workflow.run.
type Worker struct {
	exec *Executor
}

// NewWorker returns the worker of the executor's runs.
func NewWorker(exec *Executor) *Worker { return &Worker{exec: exec} }

// RunResult is the result of a workflow.run job.
type RunResult struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// Run is the handler of RunJobKind: it loads the run under the scope
// snapshot of the job (TEN-09, jobs.CreatorContext) as the user who
// initiated it, and resumes it at its first step that has not succeeded. A
// run that ended or waits for an approval is left alone (a repeated
// delivery). An error asks the queue for a retry (RunRetryError) or fails
// the job on its last attempt (RunFailedError).
func (w *Worker) Run(ctx context.Context, job *jobs.Job) (any, error) {
	var p RunJobParams
	if err := json.Unmarshal(job.Params, &p); err != nil || p.RunID == "" {
		return nil, errRunJobParams
	}
	ctx = jobs.CreatorContext(ctx, job)
	repo := w.exec.repo
	run, err := repo.GetRun(ctx, job.OrganizationID, p.RunID)
	if err != nil {
		return nil, err
	}
	ctx = asInitiator(ctx, job.OrganizationID, run.InitiatedBy)
	switch run.Status {
	case StatusPending, StatusRunning:
	default:
		return RunResult{RunID: run.ID, Status: run.Status}, nil
	}
	at := attempt{job: true, number: job.Attempts, max: job.MaxAttempts, backoff: RunJobKindConfig.Backoff}
	if rerr := repo.RecordAttempt(ctx, job.OrganizationID, run.ID, RunAttempt{Attempts: at.number, LastError: run.LastError}); rerr != nil {
		return nil, w.exec.endAttempt(ctx, job.OrganizationID, run, at, rerr)
	}
	run.Attempts = at.number
	out, err := w.exec.resume(ctx, job.OrganizationID, run, at)
	if err != nil {
		return nil, w.exec.endAttempt(ctx, job.OrganizationID, run, at, err)
	}
	return RunResult{RunID: out.ID, Status: out.Status}, nil
}

// JobFailed is the jobs.Kind.Failed hook of RunJobKind: a job that failed
// for good without its worker closing the run (the worker of the last
// attempt died or ran past its timeout, the reaper or a takeover failed
// the job; the run could not be read) leaves the run pending or running.
// The run is marked failed with runLostText, so it does not look active
// forever (WP-1088 AK5). A run that ended already is left alone.
func (w *Worker) JobFailed(ctx context.Context, job *jobs.Job) {
	var p RunJobParams
	if err := json.Unmarshal(job.Params, &p); err != nil || p.RunID == "" {
		return
	}
	ctx = jobs.CreatorContext(ctx, job)
	run, err := w.exec.repo.GetRun(ctx, job.OrganizationID, p.RunID)
	if err != nil {
		if !errors.Is(err, ErrRunNotFound) {
			slog.Error("workflow: failed to read the run of a failed job", "run_id", p.RunID, "error", err)
		}
		return
	}
	if run.Status != StatusPending && run.Status != StatusRunning {
		return
	}
	ctx = asInitiator(ctx, job.OrganizationID, run.InitiatedBy)
	at := attempt{job: true, number: job.Attempts, max: job.MaxAttempts}
	if _, err := w.exec.finishFailed(ctx, job.OrganizationID, run, at, runLostText); err != nil {
		slog.Error("workflow: failed to close the run of a failed job", "run_id", run.ID, "error", err)
	}
}

// asInitiator sets the user of the run on the tenant and the scope of ctx:
// the actions write as the user who started the run (submitted_by,
// reporter), within the scope snapshot of the job.
func asInitiator(ctx context.Context, orgID, user string) context.Context {
	info := tenant.FromContext(ctx)
	info.OrganizationID, info.UserID = orgID, user
	ctx = tenant.WithTenant(ctx, info)
	if scope, ok := database.TenantScopeFromContext(ctx); ok {
		scope.UserID = user
		ctx = database.ContextWithTenantScope(ctx, &scope)
	}
	return ctx
}
