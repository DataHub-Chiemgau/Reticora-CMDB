package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

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
	// Enqueue is the enqueues of the run the job was queued with
	// (WP-1089): only the job of the last enqueue executes the run.
	Enqueue int `json:"enqueue,omitempty"`
}

// RunJobKey is the idempotency key of the job of the enqueue-th queueing
// of a run, workflow.run:<run_id>:<enqueues> (WP-1089 AK3): a second
// Execute returns the same job, a run queued again (the next run of its
// CI, the continuation after an approval, the sweep) gets a new one.
func RunJobKey(runID string, enqueue int) string {
	return "workflow.run:" + runID + ":" + strconv.Itoa(enqueue)
}

// legacyRunJobKey is the key of the job of a run queued before WP-1089.
func legacyRunJobKey(runID string) string { return "workflow.run:" + runID }

// SerialSweepKind is the system job that queues the pending runs of a free
// serial key that waited longer than serialSweepAge (WP-1089 AK4): a run
// whose wake-up after the end of its predecessor was lost still runs.
const SerialSweepKind = "workflow.serial_sweep"

// serialSweepAge is how long a pending run of a free key waits before the
// sweep queues it again.
const serialSweepAge = 5 * time.Minute

// serialWait is the time from the creation of a run to the start of its
// execution (WP-1089 AK5): the wait for the runs before it on its CI and
// for the queue.
var serialWait = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "reticora_workflow_serial_wait_seconds",
	Help:    "Time from the creation of a workflow run to the start of its execution, including the wait for earlier runs on its CI.",
	Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900, 3600},
})

// RegisterMetrics registers the workflow metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) { reg.MustRegister(serialWait, scheduleSkipped) }

// idempotentActions may be executed again when an attempt ended while
// their step was running: a second execution has the effect of the first
// (noop, set_ci_field writes the same value, require_approval waits again,
// send_notification queues under the same dedup key, WP-1095; a running
// send_webhook step has written no event, since the event commits with the
// succeeded step, WP-1096).
var idempotentActions = map[string]bool{"noop": true, "set_ci_field": true, "require_approval": true, "send_notification": true,
	"send_webhook": true}

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
//
// The runs of one CI are executed one after the other (EVT-01, WP-1089,
// E-227 option 2): the job executes its run only when Repository.ClaimRun
// grants it, otherwise it ends without effect and the run stays pending.
// When the run ends or waits for an approval, the next pending run of its
// CI is queued after the commit.
func (w *Worker) Run(ctx context.Context, job *jobs.Job) (any, error) {
	var p RunJobParams
	if err := json.Unmarshal(job.Params, &p); err != nil || p.RunID == "" {
		return nil, errRunJobParams
	}
	ctx = jobs.CreatorContext(ctx, job)
	repo := w.exec.repo
	run, err := repo.GetRun(ctx, job.OrganizationID, p.RunID)
	if errors.Is(err, ErrRunNotFound) {
		// The CI of the run left the scope of the job (another client or
		// site): the run is not executed, it is closed and frees its key.
		return w.closeOutOfScope(ctx, job, p.RunID)
	}
	if err != nil {
		return nil, err
	}
	ctx = asInitiator(ctx, job.OrganizationID, run.InitiatedBy)
	switch run.Status {
	case StatusPending, StatusRunning:
	default:
		return RunResult{RunID: run.ID, Status: run.Status}, nil
	}
	claim, err := repo.ClaimRun(ctx, job.OrganizationID, run.ID, p.Enqueue)
	if err != nil {
		return nil, err
	}
	if !claim.Claimed {
		// An older job of the run, or a run of its CI is ahead of it:
		// the run stays pending until its turn queues it again.
		return RunResult{RunID: run.ID, Status: run.Status}, nil
	}
	if claim.Started && run.Attempts == 0 {
		serialWait.Observe(claim.Waited.Seconds())
	}
	at := attempt{job: true, number: job.Attempts, max: job.MaxAttempts, backoff: RunJobKindConfig.Backoff}
	if rerr := repo.RecordAttempt(ctx, job.OrganizationID, run.ID, RunAttempt{Attempts: at.number, LastError: run.LastError}); rerr != nil {
		return nil, w.exec.endRunAttempt(ctx, job.OrganizationID, run, at, rerr)
	}
	run.Attempts = at.number
	out, err := w.exec.resume(ctx, job.OrganizationID, run, at)
	if err != nil {
		return nil, w.exec.endRunAttempt(ctx, job.OrganizationID, run, at, err)
	}
	if out.Status != StatusRunning && out.Status != StatusPending {
		w.exec.releaseKey(ctx, job.OrganizationID, run.SerialKey)
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
	// Read organization-wide: a run whose CI left the scope of the job is
	// closed as well, so it does not block the runs of its CI.
	run, err := w.exec.repo.GetRunForJob(ctx, job.OrganizationID, p.RunID)
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
		return
	}
	w.exec.releaseKey(ctx, job.OrganizationID, run.SerialKey)
}

// runOutOfScopeText is the last_error of a run whose CI is outside the
// scope of its job when the job runs (WP-1089): it is not executed.
const runOutOfScopeText = "the CI of the run is outside the scope of its job"

// closeOutOfScope closes the pending or running run runID that the scope
// of its job no longer sees: it fails with runOutOfScopeText and frees its
// serial key for the next run of its CI. Without it, the run would stay
// the head of its key and block the runs after it for good.
func (w *Worker) closeOutOfScope(ctx context.Context, job *jobs.Job, runID string) (any, error) {
	run, err := w.exec.repo.GetRunForJob(ctx, job.OrganizationID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusPending && run.Status != StatusRunning {
		return RunResult{RunID: run.ID, Status: run.Status}, nil
	}
	ctx = asInitiator(ctx, job.OrganizationID, run.InitiatedBy)
	at := attempt{job: true, number: job.Attempts, max: job.MaxAttempts}
	out, err := w.exec.finishFailed(ctx, job.OrganizationID, run, at, runOutOfScopeText)
	if err != nil {
		return nil, err
	}
	w.exec.releaseKey(ctx, job.OrganizationID, run.SerialKey)
	return RunResult{RunID: out.ID, Status: out.Status}, nil
}

// endRunAttempt is endAttempt for the worker: a run that failed for good
// frees its serial key for the next run of its CI.
func (e *Executor) endRunAttempt(ctx context.Context, orgID string, run *Run, at attempt, err error) error {
	err = e.endAttempt(ctx, orgID, run, at, err)
	var failed *RunFailedError
	if errors.As(err, &failed) {
		e.releaseKey(context.WithoutCancel(ctx), orgID, run.SerialKey)
	}
	return err
}

// releaseKey queues the next pending run of the serial key key once the
// run that held it ended or waits for an approval (WP-1089 AK3). Without
// a key or a queue nothing is queued; an error is logged, the sweep queues
// the run later (AK4).
func (e *Executor) releaseKey(ctx context.Context, orgID, key string) {
	if key == "" || e.queue == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if err := e.repo.NextSerialRun(ctx, orgID, key, func(ctx context.Context, next *Run) {
		e.requeue(ctx, orgID, next)
	}); err != nil {
		slog.Error("workflow: failed to queue the next run of a CI", "serial_key", key, "error", err)
	}
}

// requeue queues run, whose enqueues was raised, as a new job
// workflow.run. The job runs like the first job of the run under the
// scope snapshot and as the creator of that job (TEN-09), whichever job
// queues it; a run without a first job gets an empty scope (fail closed).
func (e *Executor) requeue(ctx context.Context, orgID string, run *Run) {
	if e.queue == nil {
		return
	}
	origin, err := e.repo.RunJobOrigin(ctx, orgID, run.ID)
	if err != nil {
		slog.Error("workflow: failed to read the first job of a run", "run_id", run.ID, "error", err)
		return
	}
	if origin == nil {
		origin = &jobs.Job{OrganizationID: orgID, CreatedBy: run.InitiatedBy,
			Scope: &jobs.Scope{Clients: []string{}, Sites: []string{}, Teams: []string{}}}
	}
	jctx := jobs.CreatorContext(database.WithoutTx(context.WithoutCancel(ctx)), origin)
	if _, _, err = e.queue.Submit(jctx, orgID, RunJobKind, RunJobParams{RunID: run.ID, Enqueue: run.Enqueues},
		jobs.EnqueueOptions{IdempotencyKey: RunJobKey(run.ID, run.Enqueues), CreatedBy: origin.CreatedBy}); err != nil {
		slog.Error("workflow: failed to queue a run", "run_id", run.ID, "error", err)
	}
}

// SerialSweep is the system job workflow.serial_sweep (WP-1089 AK4):
// every minute it queues, per organization, the next run of each serial
// key whose pending run waited longer than five minutes while no run of
// the key is running.
type SerialSweep struct {
	exec *Executor
	orgs func(ctx context.Context) ([]string, error)
}

// NewSerialSweep returns the sweep over the runs of exec (with its queue)
// in the organizations orgs lists.
func NewSerialSweep(exec *Executor, orgs func(ctx context.Context) ([]string, error)) *SerialSweep {
	return &SerialSweep{exec: exec, orgs: orgs}
}

// Schedule is the registration of the sweep with the scheduler: every
// minute, at most one run caught up.
func (s *SerialSweep) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: SerialSweepKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: 50 * time.Second,
		Run: func(ctx context.Context) error {
			queued, err := s.Run(ctx)
			if queued > 0 {
				slog.InfoContext(ctx, "workflow runs queued by the serial sweep", "runs", queued)
			}
			return err
		}}
}

// Run queues the next run of every stranded serial key and returns how
// many keys it found. Each organization runs in its own transactions; a
// failing one does not stop the others.
func (s *SerialSweep) Run(ctx context.Context) (int, error) {
	if s.exec.queue == nil {
		return 0, nil
	}
	orgs, err := s.orgs(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", SerialSweepKind, err)
	}
	total := 0
	var errs []error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		octx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
		keys, kerr := s.exec.repo.StrandedSerialKeys(octx, orgID, serialSweepAge)
		if kerr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", SerialSweepKind, orgID, kerr))
			continue
		}
		for _, key := range keys {
			if nerr := s.exec.repo.NextSerialRun(octx, orgID, key, func(ctx context.Context, next *Run) {
				s.exec.requeue(ctx, orgID, next)
			}); nerr != nil {
				errs = append(errs, fmt.Errorf("%s: organization %s: %w", SerialSweepKind, orgID, nerr))
				continue
			}
			total++
		}
	}
	return total, errors.Join(errs...)
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
