package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5/pgconn"
)

// Rules of a trigger or approval (400 with the fixed message; the handler
// shows no other error text, API-03, F-047).
var (
	// ErrTriggerMismatch: the trigger does not match the definition.
	ErrTriggerMismatch = errors.New("workflow trigger does not match")
	// ErrNotWaiting: the run waits for no approval.
	ErrNotWaiting = errors.New("workflow run is not waiting for approval")
	// ErrInvalidDecision: an approval decision is approved or rejected.
	ErrInvalidDecision = errors.New("decision must be approved or rejected")
	// ErrInvalidAction: an action of the definition has a type that is no
	// string.
	ErrInvalidAction = errors.New("workflow action type must be a string")
)

type Executor struct {
	repo    Repository
	tickets ticket.Repository
	cis     ci.Repository
	forms   form.Repository
	// formDocs reads the signature documents of submit_form (WP-1101);
	// nil without the module documents (WithFormDocuments).
	formDocs   form.DocumentReader
	dispatcher *webhook.Dispatcher
	guard      *override.Guard
	conflicts  ConflictReviews
	queue      RunQueue
	// notifier and recipients send the notifications of
	// send_notification (WithNotifier, WP-1095).
	notifier   *notify.Notifier
	recipients notify.Resolver
	// approvers resolves the approver circles of require_approval
	// (WithApprovers, WP-1098); nil: everyone the route admits.
	approvers notify.Resolver
	// eventOutbox makes send_webhook write workflow.webhook to the event
	// outbox of the step's transaction (WithEventOutbox, WP-1096);
	// without it the event goes to the dispatcher after the step.
	eventOutbox bool
	// policies resolves the action and field rights of the initiator of
	// set_ci_field and transition_lifecycle from the initiator's current
	// role assignments, users whether the initiator is active
	// (WithPolicies, WP-1097); lifecycle moves the entity of
	// transition_lifecycle (WithLifecycle).
	policies  InitiatorPolicies
	users     permission.CreatorStatus
	lifecycle LifecycleTransitioner
	// ciWriter writes the CIs of set_ci_field (WithCIService).
	ciWriter CIWriter
	// runQuota is max_workflow_runs_per_hour (WithRunQuota, WP-1112).
	runQuota RunQuota
	// assets reads the asset of a lifecycle.changed for the run context of
	// the event triggers (WithAssets, WP-1104).
	assets AssetReader
	// orders changes the orders of order_decision (WithOrders, WP-1108).
	orders OrderStatusSetter
	// afterStep is a test hook called after each succeeded step; an error
	// ends the attempt there like a worker that dies.
	afterStep func(index int) error
}

// RunQueue submits the job workflow.run (jobs.Queue).
type RunQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithQueue makes Execute submit the run as the job workflow.run instead
// of executing it in the request (WFL-02, WP-1088, E-226): Execute
// returns the run pending, the worker executes it with retries.
func (e *Executor) WithQueue(q RunQueue) *Executor {
	e.queue = q
	return e
}

// ConflictReviews opens the override_conflict review of a field
// (reviews.Service, the call of discovery as well, REC-12).
type ConflictReviews interface {
	OpenOverrideConflict(ctx context.Context, c *reviews.OverrideConflict) (bool, error)
}

func NewExecutor(repo Repository, tickets ticket.Repository, cis ci.Repository, forms form.Repository, dispatcher *webhook.Dispatcher) *Executor {
	return &Executor{repo: repo, tickets: tickets, cis: cis, forms: forms, dispatcher: dispatcher}
}

// WithFieldGuard sets the central write decision for set_ci_field
// (OVR-02). Without one the rank table alone decides.
// WithFormDocuments sets the reader of the signature documents submit_form
// checks (WP-1101 AK5); the router sets it with the module documents only.
func (e *Executor) WithFormDocuments(docs form.DocumentReader) *Executor {
	e.formDocs = docs
	return e
}

func (e *Executor) WithFieldGuard(g *override.Guard) *Executor {
	e.guard = g
	return e
}

// WithConflictReviews makes set_ci_field open an override_conflict review
// when its value differs from a manual override (REC-12).
func (e *Executor) WithConflictReviews(r ConflictReviews) *Executor {
	e.conflicts = r
	return e
}

// Trigger starts a run of def for trigger with payload as its context and
// executes its actions (Start, then Execute).
func (e *Executor) Trigger(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap) (*Run, error) {
	run, err := e.Start(ctx, orgID, def, trigger, payload)
	if err != nil {
		return nil, err
	}
	return e.Execute(ctx, orgID, def, run)
}

// Start stores a pending run of def for trigger with payload as its context
// and executes no action. A caller that stores the run together with its own
// rows in one transaction (database.ContextWithTx) executes it with Execute
// after the commit, so a failing action never rolls back the run that
// records the failure (WFL-04).
//
// A context that names a CI (ci_id, or id in ci) binds the run to it: the
// CI is read in the scope of ctx, the trigger's, and one that is unknown,
// of another organization or outside that scope is ErrCINotFound (404,
// TEN-04). The runs of one CI are executed one after the other (EVT-01,
// WP-1089).
func (e *Executor) Start(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap) (*Run, error) {
	return e.start(ctx, orgID, def, trigger, payload, nil)
}

// StartSequenced is Start for a run started by the event with sequence
// number entitySeq of its object (Envelope.EntitySeq): the runs of a CI
// are executed in the order of these numbers (EVT-01, WP-1090).
func (e *Executor) StartSequenced(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap, entitySeq int64) (*Run, error) {
	return e.start(ctx, orgID, def, trigger, payload, &entitySeq)
}

func (e *Executor) start(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap, entitySeq *int64) (*Run, error) {
	if def == nil {
		return nil, fmt.Errorf("workflow definition is required")
	}
	if !triggerMatches(def.Trigger, trigger) {
		return nil, ErrTriggerMismatch
	}
	versionID := def.CurrentVersionID
	if versionID == "" {
		// A definition the caller built without its stored state: the run
		// binds the current version (WFL-02).
		stored, err := e.repo.GetDefinition(ctx, orgID, def.ID)
		if err != nil {
			return nil, err
		}
		versionID = stored.CurrentVersionID
	}
	ciID, err := e.runCI(ctx, orgID, payload)
	if err != nil {
		return nil, err
	}
	run := &Run{OrganizationID: orgID, WorkflowID: def.ID, DefinitionVersionID: versionID, Status: StatusPending, Trigger: trigger, Context: payload,
		InitiatedBy: tenant.FromContext(ctx).UserID, CIID: ciID, EntitySeq: entitySeq}
	// A run executed in the request starts now; a queued one with the
	// first claim of its job (WP-1112).
	if e.queue == nil {
		run.StartedAt = time.Now().UTC()
	}
	if err := e.repo.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// Execute runs the actions of a run that Start stored: the conditions and
// actions of the version the run is bound to, not those of def, which may
// have changed since (WFL-02, E-228).
func (e *Executor) Execute(ctx context.Context, orgID string, _ *Definition, run *Run) (*Run, error) {
	if e.queue == nil {
		// Without a job queue (--no-db, tests without NATS) the run is
		// executed in the request, as before (parity).
		return e.resume(ctx, orgID, run, attempt{})
	}
	if _, _, err := e.queue.Submit(ctx, orgID, RunJobKind, RunJobParams{RunID: run.ID, Enqueue: run.Enqueues},
		jobs.EnqueueOptions{IdempotencyKey: RunJobKey(run.ID, run.Enqueues), CreatedBy: run.InitiatedBy}); err != nil {
		e.failRun(ctx, orgID, run, err)
		return nil, fmt.Errorf("queue workflow run: %w", err)
	}
	return run, nil
}

// TriggerMatches reports whether event starts runs of def under the rule of
// Trigger: the event of the trigger, or "manual" for a manual or scheduled
// definition.
func TriggerMatches(def *Definition, event string) bool {
	return def != nil && triggerMatches(def.Trigger, event)
}

// Approve records decision of the calling user on the approval step of a
// waiting run and returns the run (Decide, WP-1098): the decision and the
// change of the run happen in one transaction under the row locks of run and
// step, so a run cancelled meanwhile stays cancelled and the decision is
// ErrNotWaiting (WP-1092 AK1).
func (e *Executor) Approve(ctx context.Context, orgID, runID, decision, comment string) (*Run, error) {
	res, err := e.Decide(ctx, orgID, runID, decision, comment)
	if err != nil {
		return nil, err
	}
	return res.Run, nil
}

// Cancel ends a pending or waiting run as cancelled (WP-1092 AK1): its open
// step is cancelled with it, a job still queued for it finds it ended and
// ends without effect. A running or ended run is ErrRunNotCancellable. The
// serial key of the run is released, so the next run of its CI does not
// wait for the sweep.
func (e *Executor) Cancel(ctx context.Context, orgID, runID string) (*Run, error) {
	run, err := e.repo.CancelRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	e.releaseKey(ctx, orgID, run.SerialKey)
	return run, nil
}

// Retry continues a failed run at its failed step (WP-1092 AK2): the run
// is pending again with attempts 0 and is queued like after an approval,
// under the scope snapshot of its first job (TEN-09); without one, or
// without a queue, as Execute does. Any other run is ErrRunNotRetryable.
func (e *Executor) Retry(ctx context.Context, orgID, runID string) (*Run, error) {
	run, err := e.repo.RetryRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	if e.queue == nil {
		return e.resume(ctx, orgID, run, attempt{})
	}
	origin, err := e.repo.RunJobOrigin(ctx, orgID, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read the first job of the run: %w", err)
	}
	if origin == nil {
		return e.Execute(ctx, orgID, nil, run)
	}
	if err := e.submitRun(ctx, orgID, run, origin, time.Time{}); err != nil {
		// No job runs the pending run: it is failed again, retryable, and
		// the route answers 500 instead of a run nobody executes.
		e.failRun(ctx, orgID, run, err)
		return nil, fmt.Errorf("queue workflow run: %w", err)
	}
	return e.repo.GetRun(ctx, orgID, run.ID)
}

// continueApproved queues an approved run, which DecideApproval set
// pending with its enqueues raised, with the job queue like a new run
// (WP-1089 AK3): it waits for the runs ahead of it on its CI.
func (e *Executor) continueApproved(ctx context.Context, orgID string, run *Run) (*Run, error) {
	e.requeue(ctx, orgID, run)
	return e.repo.GetRun(ctx, orgID, run.ID)
}

// runCI returns the CI a run's context names (ci_id, or id in ci), read in
// the scope of ctx; empty without one (WP-1089 AK1).
func (e *Executor) runCI(ctx context.Context, orgID string, payload JSONMap) (string, error) {
	id := str(payload, "ci_id", "")
	if id == "" {
		switch c := payload["ci"].(type) {
		case map[string]any:
			id = str(c, "id", "")
		case JSONMap:
			id = str(c, "id", "")
		}
	}
	if id == "" {
		return "", nil
	}
	if !api.IsUUID(id) {
		return "", ErrCINotFound
	}
	if e.cis == nil {
		return "", fmt.Errorf("ci repository unavailable")
	}
	item, err := e.cis.GetByID(ctx, orgID, id)
	if errors.Is(err, ci.ErrNotFound) {
		return "", ErrCINotFound
	}
	if err != nil {
		return "", fmt.Errorf("read the CI of the run: %w", err)
	}
	return item.ID, nil
}

// resume executes the actions of the run's definition version (WFL-02): a
// waiting run keeps the version it started with, whatever the definition
// says now. Steps are idempotent by (run_id, step_index) (WP-1088): a
// succeeded step is never executed again; a step still running from an
// interrupted attempt is repeated only when its action is idempotent
// (idempotentActions), otherwise it fails with stepInterruptedText and so
// does the run; a step that failed in an earlier attempt of the job is set
// back to running and executed again. at is the job attempt that executes
// the run; its zero value is the run in the request, which is not retried.
func (e *Executor) resume(ctx context.Context, orgID string, run *Run, at attempt) (*Run, error) {
	// The events the run writes name it as their origin (WP-1090): they
	// start runs only of definitions that allow it, up to a depth.
	ctx = events.WithOrigin(ctx, events.Origin{Kind: events.OriginWorkflowRun, RunID: run.ID, Depth: run.TriggerDepth})
	def, err := e.repo.GetVersionByID(ctx, orgID, run.DefinitionVersionID)
	if err != nil {
		return e.abort(ctx, orgID, run, at, fmt.Errorf("load workflow definition version: %w", err))
	}
	hold, err := conditionsHold(def.Conditions, run.Context)
	if err != nil {
		return e.abort(ctx, orgID, run, at, err)
	}
	if !hold {
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusCancelled, &now, run.Context)
	}
	if _, err = e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusRunning, nil, run.Context); err != nil {
		// The start writes its audit entry and workflow.run_started with
		// it (WP-1092); a run that could not start executes no step.
		return e.abort(ctx, orgID, run, at, fmt.Errorf("mark the run running: %w", err))
	}
	steps, err := e.repo.ListSteps(ctx, orgID, run.ID)
	if err != nil {
		return e.abort(ctx, orgID, run, at, fmt.Errorf("list workflow steps: %w", err))
	}
	stored := make(map[int]Step, len(steps))
	for i := range steps {
		stored[steps[i].StepIndex] = steps[i]
	}
	for i := range def.Actions {
		action := def.Actions[i]
		typ, err := actionType(action)
		if err != nil {
			return e.abort(ctx, orgID, run, at, err)
		}
		if prev, ok := stored[i]; ok && prev.Status == StatusSucceeded {
			if typ == orderDecisionActionType {
				rememberOrderStatus(run, prev.Output)
			}
			continue
		}
		skip, err := actionSkipped(action, run.Context)
		if err != nil {
			return e.abort(ctx, orgID, run, at, err)
		}
		if typ == approvalActionType && rejectedBefore(stored, i) {
			// An earlier approval rejected the order (WP-1108): no further
			// approval, order_decision rejects it.
			skip = true
		}
		step, end, out, err := e.prepareStep(ctx, orgID, run, at, i, typ, action, stored)
		if err != nil || end {
			return out, err
		}
		if skip {
			// The "when" of the action does not hold (WP-1108).
			if _, uerr := e.repo.UpdateStep(ctx, orgID, step.ID, StatusSucceeded, JSONMap{"skipped": true}, ""); uerr != nil {
				return e.abort(ctx, orgID, run, at, fmt.Errorf("update workflow step: %w", uerr))
			}
			continue
		}
		var output JSONMap
		var wait, completed bool
		if typ == webhookActionType {
			// The step is stored succeeded together with its event
			// (WP-1096).
			output, err = e.sendWebhook(ctx, orgID, run, step, action)
			completed = err == nil
		} else {
			output, wait, err = e.executeAction(ctx, orgID, run, i, action)
		}
		if wait && err == nil && typ == requestFormActionType {
			// The form is requested from its recipient (WP-1102); a request
			// that cannot be stored fails the step.
			waiting, rerr := e.requestInput(ctx, orgID, run, step, action)
			if rerr == nil {
				return waiting, nil
			}
			output, err = nil, rerr
			wait = false
		}
		if wait && err == nil && typ == approvalActionType {
			// The approval is requested with its circle, quorum and
			// deadline (WP-1098); a request that cannot be stored fails
			// the step like a failed action.
			waiting, rerr := e.requestApproval(ctx, orgID, run, step, action)
			if rerr == nil {
				return waiting, nil
			}
			output, err = nil, rerr
			wait = false
		}
		if wait {
			if _, markErr := e.repo.UpdateStep(ctx, orgID, step.ID, StatusWaitingApproval, output, ""); markErr != nil {
				slog.Error("workflow: failed to mark step waiting for approval", "step_id", step.ID, "error", markErr)
			}
			return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusWaitingApproval, nil, run.Context)
		}
		if err != nil {
			text := stepErrorText(step.ID, err)
			if _, uerr := e.repo.UpdateStep(ctx, orgID, step.ID, StatusFailed, output, text); uerr != nil {
				slog.Error("workflow: failed to mark step failed", "step_id", step.ID, "error", uerr)
			}
			return e.failAttempt(ctx, orgID, run, at, text)
		}
		if !completed {
			if _, err := e.repo.UpdateStep(ctx, orgID, step.ID, StatusSucceeded, output, ""); err != nil {
				return e.abort(ctx, orgID, run, at, fmt.Errorf("update workflow step: %w", err))
			}
		}
		if typ == orderDecisionActionType {
			rememberOrderStatus(run, output)
		}
		if e.afterStep != nil {
			// Test hook: the attempt ends after step i like a worker that
			// dies (WP-1088).
			if herr := e.afterStep(i); herr != nil {
				return nil, herr
			}
		}
	}
	now := time.Now().UTC()
	return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusSucceeded, &now, run.Context)
}

// prepareStep returns the step that executes action i, which has not
// succeeded: a new one, or the stored one of an earlier attempt set back
// to running. end reports that resume ends here with out and err: the
// step still waits for its approval, was cancelled or was interrupted.
func (e *Executor) prepareStep(ctx context.Context, orgID string, run *Run, at attempt, i int, typ string, action JSONMap, stored map[int]Step) (*Step, bool, *Run, error) {
	prev, ok := stored[i]
	if !ok {
		step := &Step{OrganizationID: orgID, RunID: run.ID, StepIndex: i, ActionType: typ, Status: StatusRunning, Input: action, Output: JSONMap{}}
		if err := e.repo.AppendStep(ctx, step); err != nil {
			return nil, true, nil, err
		}
		return step, false, nil, nil
	}
	switch prev.Status {
	case StatusWaitingApproval:
		r, err := e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusWaitingApproval, nil, run.Context)
		return nil, true, r, err
	case StatusWaitingInput:
		// The step still waits for its form (WP-1102).
		r, err := e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusWaitingInput, nil, run.Context)
		return nil, true, r, err
	case StatusCancelled:
		now := time.Now().UTC()
		r, err := e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusCancelled, &now, run.Context)
		return nil, true, r, err
	case StatusRunning:
		if !idempotentActions[prev.ActionType] {
			// Whether the action took effect before the interruption is
			// unknown: it is not repeated, the run fails for good.
			if _, err := e.repo.UpdateStep(ctx, orgID, prev.ID, StatusFailed, prev.Output, stepInterruptedText); err != nil {
				slog.Error("workflow: failed to mark interrupted step failed", "step_id", prev.ID, "error", err)
			}
			r, err := e.finishFailed(ctx, orgID, run, at, stepInterruptedText)
			return nil, true, r, err
		}
	default:
		// failed in an earlier attempt of the job: executed again.
		if _, err := e.repo.UpdateStep(ctx, orgID, prev.ID, StatusRunning, JSONMap{}, ""); err != nil {
			return nil, true, nil, fmt.Errorf("reset workflow step: %w", err)
		}
	}
	step := prev
	step.Status = StatusRunning
	return &step, false, nil, nil
}

// failAttempt ends an attempt whose step failed with text: the job retries
// it while attempts remain (the run stays running, last_error and
// next_attempt_at are recorded, RunRetryError tells the job), otherwise
// the run fails for good (in a job with RunFailedError).
func (e *Executor) failAttempt(ctx context.Context, orgID string, run *Run, at attempt, text string) (*Run, error) {
	if at.retries() {
		next := time.Now().UTC().Add(at.backoff(at.number))
		if err := e.repo.RecordAttempt(ctx, orgID, run.ID, RunAttempt{Attempts: at.number, NextAttemptAt: &next, LastError: text}); err != nil {
			slog.Error("workflow: failed to record the attempt", "run_id", run.ID, "error", err)
		}
		return nil, &RunRetryError{Text: text}
	}
	r, err := e.finishFailed(ctx, orgID, run, at, text)
	if err != nil || !at.job {
		return r, err
	}
	// The last attempt of the job: the job fails as well.
	return r, &RunFailedError{Text: text}
}

// finishFailed marks the run failed for good with last_error text.
func (e *Executor) finishFailed(ctx context.Context, orgID string, run *Run, at attempt, text string) (*Run, error) {
	if at.job {
		if err := e.repo.RecordAttempt(ctx, orgID, run.ID, RunAttempt{Attempts: at.number, LastError: text}); err != nil {
			slog.Error("workflow: failed to record the attempt", "run_id", run.ID, "error", err)
		}
	}
	now := time.Now().UTC()
	return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusFailed, &now, run.Context)
}

// stepErrorText is the error text a failed step stores, which the run API
// shows: a database error becomes its sanitized classification without
// table, constraint, policy or SQLSTATE text (API-03, F-047), the log keeps
// it; the errors of the actions themselves keep their text.
func stepErrorText(stepID string, err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err.Error()
	}
	slog.Warn("workflow: step failed", "step_id", stepID, "error", err)
	if _, _, detail, ok := api.ClassifyDBError(err); ok {
		return detail
	}
	return "the action could not be completed"
}

// abort ends resume on an error that is not a failed step. In the request
// (no job) the run fails at once, as before. In a job the run is left as it
// is: Worker.Run decides with endAttempt whether the job retries the run or
// it fails for good, so a passing database error does not end a run that
// has attempts left.
func (e *Executor) abort(ctx context.Context, orgID string, run *Run, at attempt, cause error) (*Run, error) {
	if !at.job {
		e.failRun(ctx, orgID, run, cause)
	}
	return nil, cause
}

// runInterruptedText is the last_error of an attempt that ended with an
// error that is neither a failed step nor one of the workflow's own: its
// text (network, database, timeout) is logged, not shown.
const runInterruptedText = "the run could not be continued"

// runLostText is the last_error of a run whose job failed for good without
// the worker closing the run (the worker of the last attempt was lost or
// timed out).
const runLostText = "the run was interrupted on its last attempt"

// permanentRunError reports whether err fails the run whatever attempts
// remain: a stored condition or action that is invalid, or a version that
// is gone, do not heal by a retry.
func permanentRunError(err error) bool {
	return errors.Is(err, errConditionInvalid) || errors.Is(err, ErrInvalidAction) || errors.Is(err, ErrVersionNotFound)
}

// runErrorText is the sanitized last_error of an attempt that ended with
// err outside a step (AK5): the workflow's own errors keep their fixed
// text, a database error its classification (stepErrorText), any other
// error the fixed runInterruptedText.
func runErrorText(err error) string {
	for _, known := range []error{errConditionInvalid, ErrInvalidAction, ErrVersionNotFound} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return stepErrorText("", err)
	}
	slog.Warn("workflow: attempt ended with an error", "error", err)
	return runInterruptedText
}

// endAttempt decides about a job attempt that ended with err (WP-1088
// AK3/AK5). RunRetryError and RunFailedError were decided by failAttempt.
// Any other error is no failed step: a database or worker error, also the
// timeout of the attempt, is retried while attempts remain with the
// sanitized text in last_error; a permanent error (permanentRunError) and
// the last attempt fail the run for good (RunFailedError). The writes use
// a context without the attempt's deadline, so a timed-out attempt still
// closes its run.
func (e *Executor) endAttempt(ctx context.Context, orgID string, run *Run, at attempt, err error) error {
	var retry *RunRetryError
	var failed *RunFailedError
	if errors.As(err, &retry) || errors.As(err, &failed) {
		return err
	}
	text := runErrorText(err)
	ctx = context.WithoutCancel(ctx)
	if at.retries() && !permanentRunError(err) {
		next := time.Now().UTC().Add(at.backoff(at.number))
		if rerr := e.repo.RecordAttempt(ctx, orgID, run.ID, RunAttempt{Attempts: at.number, NextAttemptAt: &next, LastError: text}); rerr != nil {
			slog.Error("workflow: failed to record the attempt", "run_id", run.ID, "error", rerr)
		}
		return &RunRetryError{Text: text}
	}
	if _, ferr := e.finishFailed(ctx, orgID, run, at, text); ferr != nil {
		// The run stays open; the job fails or retries with this error,
		// and Worker.JobFailed closes the run once the job failed for good.
		slog.Error("workflow: failed to mark run failed", "run_id", run.ID, "error", ferr)
		return &RunRetryError{Text: text}
	}
	return &RunFailedError{Text: text}
}

// failRun best-effort marks the run failed; the original error is returned to
// the caller by resume, so persistence failures here are only logged.
func (e *Executor) failRun(ctx context.Context, orgID string, run *Run, cause error) {
	now := time.Now().UTC()
	if _, err := e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusFailed, &now, run.Context); err != nil {
		slog.Error("workflow: failed to mark run failed", "run_id", run.ID, "cause", cause, "error", err)
	}
}

// actionType extracts the action type, rejecting non-string type values.
func actionType(action JSONMap) (string, error) {
	raw, exists := action["type"]
	if !exists || raw == nil {
		return "noop", nil
	}
	typ, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w, got %T", ErrInvalidAction, raw)
	}
	if typ == "" {
		return "noop", nil
	}
	return typ, nil
}

func (e *Executor) executeAction(ctx context.Context, orgID string, run *Run, index int, action JSONMap) (JSONMap, bool, error) {
	typ, err := actionType(action)
	if err != nil {
		return nil, false, err
	}
	switch typ {
	case "create_ticket":
		// reporter_id references app_user and defaults to the acting user (the
		// tenant context carries the requester); the literal "workflow" is not a
		// valid app_user id and would violate the FK. As a last resort (purely
		// synthetic/system triggers without user context) the memory/test
		// repository accepts the "workflow" marker; the PG repository requires a
		// real user and will surface the FK violation as a step failure.
		reporter := str(action, "reporter_id", "")
		if reporter == "" {
			reporter = tenant.FromContext(ctx).UserID
		}
		if reporter == "" {
			reporter = "workflow"
		}
		t := &ticket.Ticket{OrganizationID: orgID, Title: str(action, "title", "Workflow task"), Description: str(action, "description", ""), Status: "open", Priority: str(action, "priority", "medium"), Category: str(action, "category", "task"), ReporterID: reporter, RelatedCIID: str(action, "ci_id", "")}
		if e.tickets == nil {
			return nil, false, fmt.Errorf("ticket repository unavailable")
		}
		if err := e.tickets.Create(ctx, t); err != nil {
			return nil, false, err
		}
		return JSONMap{"ticket_id": t.ID}, false, nil
	case "set_ci_field":
		return e.setCIFields(ctx, orgID, run, action)
	case "transition_lifecycle":
		return e.transitionLifecycle(ctx, orgID, run, action)
	case approvalActionType:
		return JSONMap{"message": str(action, "message", "Approval required")}, true, nil
	case requestFormActionType:
		// Waits for the submission of the form (requestInput, WP-1102).
		return JSONMap{}, true, nil
	case orderDecisionActionType:
		out, err := e.orderDecision(ctx, orgID, run, index, action)
		return out, false, err
	case "send_notification":
		out, err := e.sendNotification(ctx, orgID, run, index, action)
		return out, false, err
	case "submit_form":
		if e.forms == nil {
			return nil, false, fmt.Errorf("form repository unavailable")
		}
		return e.submitForm(ctx, orgID, action)
	case "noop", "":
		return JSONMap{}, false, nil
	default:
		return nil, false, fmt.Errorf("unsupported workflow action %q", typ)
	}
}

// errFormInactive: submit_form names an inactive form definition, which
// the form API refuses as well.
var errFormInactive = errors.New("form definition is inactive")

// submitForm submits the form of a submit_form action under the rules of
// POST /forms/{id}/submissions (WFL-01, WFL-02): the definition must be
// visible in the caller's scope and active, and the values must match its
// schema. A refused submission fails the step with the fixed text of the
// rule, the fields of a schema violation in the step output. submitted_by is
// the user who triggered the run, empty (NULL) for a trigger without a user:
// the column holds user ids, the origin "workflow" is the step that names
// the submission.
func (e *Executor) submitForm(ctx context.Context, orgID string, action JSONMap) (JSONMap, bool, error) {
	values := form.JSONMap{}
	if raw, exists := action["values"]; exists && raw != nil {
		v, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("submit_form action values must be an object, got %T", raw)
		}
		values = v
	}
	formID := str(action, "form_id", "")
	if !api.IsUUID(formID) {
		return nil, false, form.ErrNotFound
	}
	def, err := e.forms.GetDefinition(ctx, orgID, formID)
	if err != nil {
		return nil, false, err
	}
	if !def.Active {
		return nil, false, errFormInactive
	}
	// Schema and conditional logic of the current version like POST
	// /forms/{id}/submissions (WP-1101 AK2, AK3, AK5): hidden fields are
	// dropped, required_when and readonly_when hold, a signature names a
	// readable PNG or JPEG document; the repository binds that version.
	checked, err := form.ValidateSubmission(ctx, &form.SubmissionCheck{OrganizationID: orgID, Schema: def.Schema, UIHints: def.UIHints,
		Values: values, Documents: e.formDocs})
	if err != nil {
		var ve form.ValidationError
		if errors.As(err, &ve) {
			return JSONMap{"fields": ve.Fields}, false, err
		}
		var fe *api.FieldError
		if errors.As(err, &fe) {
			return JSONMap{"fields": []form.FieldError{{Field: fe.Field, Message: fe.Message}}}, false, err
		}
		return nil, false, err
	}
	sub := &form.Submission{OrganizationID: orgID, FormID: def.ID, Values: checked, SubmittedBy: tenant.FromContext(ctx).UserID, Status: "submitted",
		ExpectedVersionID: def.CurrentVersionID}
	if err := e.forms.CreateSubmission(ctx, sub); err != nil {
		return nil, false, err
	}
	return JSONMap{"submission_id": sub.ID}, false, nil
}

func triggerMatches(trigger JSONMap, event string) bool {
	typ := str(trigger, "type", "")
	if typ == "manual" || typ == "schedule" {
		return event == typ || event == "manual"
	}
	if typ == TriggerFormSubmitted || typ == TriggerOrderSubmitted {
		// Started by a submission of its form (WP-1102) or of an order
		// (WP-1108) only.
		return event == typ
	}
	return eventMatches(trigger, event)
}

// eventMatches reports whether the trigger names event in trigger.event: a
// type name or a list of type names (WP-1090).
func eventMatches(trigger JSONMap, event string) bool {
	switch v := trigger["event"].(type) {
	case string:
		return v != "" && v == event
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == event {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == event {
				return true
			}
		}
	}
	return false
}

func str(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// sourceWorkflow is the automation source of workflow writes (REC-03 rank 92).
const sourceWorkflow = "workflow"
