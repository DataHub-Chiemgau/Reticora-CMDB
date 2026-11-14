package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Deadlines, reminders and escalation of approval steps (WFL-02, WP-1099,
// E-230 option 2): the system job workflow.approval_sweep runs every minute
// on one instance (the scheduler holds the leader lock) and reads, per
// organization in batches of approvalSweepBatch, the waiting approval steps
// with a deadline. Each step is handled in a transaction of its own that
// locks run and step with FOR UPDATE SKIP LOCKED: a decision the API is
// writing at that moment keeps its locks and wins, the sweep leaves the
// step to its next run, which sees the decided step and skips it. A
// decision after the sweep finds the step no longer waiting (409/400 as for
// any ended step).
//
//   - on_timeout reject (and the second expiry of an escalated step): step
//     and run become cancelled, the step error is approvalExpiredText, the
//     output decision expired; workflow.approval_decided with decision
//     expired, and the initiator is notified.
//   - on_timeout escalate: the circle is extended by escalate_to (output
//     escalated_at, escalated_to), due_at moves once by the original
//     deadline (counted from the sweep when it runs late), the extended
//     circle (approvers plus escalate_to, without those who decided) gets
//     workflow.approval_requested and a notification; approvals so far
//     keep counting.
//
// The sweep writes only its own fields into the step output: it merges them
// into the output read under the lock, so a vote committed between the
// candidate query and the lock keeps its approvals.
//
// Until the sweep has handled an expired step, a decision on it is still
// accepted (the deadline is evaluated by the sweep only, at most about a
// minute late); afterwards a rejected step is no longer waiting (400).
//   - remind_before: once due_at - remind_before is reached, the approvers
//     without a decision are notified once (dedup key
//     <run_id>:<step_index>:remind) and reminded_at is set.

// ApprovalSweepKind is the system job of the approval deadlines.
const ApprovalSweepKind = "workflow.approval_sweep"

// approvalSweepBatch is the number of waiting steps read per query.
const approvalSweepBatch = 200

// approvalExpiredText is the error of an approval step whose deadline
// expired (AK2).
const approvalExpiredText = "approval deadline expired"

// DecisionExpired is the decision of an expired deadline in the output of
// the step and in workflow.approval_decided (AK2).
const DecisionExpired = "expired"

// Actions of an expired deadline (reticora_workflow_approvals_expired_total
// {action}).
const (
	expiredActionReject   = "reject"
	expiredActionEscalate = "escalate"
)

// Metrics of the sweep (AK5).
var (
	approvalSweepRuns = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reticora_workflow_approval_sweep_runs_total",
		Help: "Runs of the system job workflow.approval_sweep.",
	})
	approvalSweepErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reticora_workflow_approval_sweep_errors_total",
		Help: "Runs of the system job workflow.approval_sweep that ended with an error.",
	})
	approvalsExpired = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_workflow_approvals_expired_total",
		Help: "Approval steps whose deadline expired, by the action taken (reject, escalate).",
	}, []string{"action"})
)

// approvalSweepCollectors are the collectors RegisterMetrics registers.
func approvalSweepCollectors() []prometheus.Collector {
	return []prometheus.Collector{approvalSweepRuns, approvalSweepErrors, approvalsExpired}
}

// ApprovalSweep is the system job workflow.approval_sweep.
type ApprovalSweep struct {
	exec *Executor
	orgs func(ctx context.Context) ([]string, error)
	now  func() time.Time
}

// NewApprovalSweep returns the sweep over the approval steps of exec (its
// repository, notifier and approver circles) in the organizations orgs
// lists.
func NewApprovalSweep(exec *Executor, orgs func(ctx context.Context) ([]string, error)) *ApprovalSweep {
	return &ApprovalSweep{exec: exec, orgs: orgs, now: time.Now}
}

// WithClock sets the clock the sweep compares deadlines with (tests).
func (s *ApprovalSweep) WithClock(now func() time.Time) *ApprovalSweep {
	s.now = now
	return s
}

// Schedule is the registration of the sweep with the scheduler: every
// minute, at most one run caught up.
func (s *ApprovalSweep) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: ApprovalSweepKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: 50 * time.Second,
		Run: func(ctx context.Context) error {
			res, err := s.Run(ctx)
			if res.changed() {
				slog.InfoContext(ctx, "workflow approvals swept", "rejected", res.Rejected, "escalated", res.Escalated, "reminded", res.Reminded,
					"inputs_expired", res.InputsExpired)
			}
			return err
		}}
}

// ApprovalSweepResult counts what one run of the sweep did.
type ApprovalSweepResult struct {
	Rejected, Escalated, Reminded int
	// InputsExpired counts the request_form steps ended after their
	// on_timeout (WP-1102).
	InputsExpired int
}

func (r ApprovalSweepResult) changed() bool {
	return r.Rejected+r.Escalated+r.Reminded+r.InputsExpired > 0
}

// approvalSweepStore is what the sweep needs of the repository; the
// PostgreSQL repository implements it (the sweep runs with a database only).
type approvalSweepStore interface {
	dueApprovals(ctx context.Context, orgID string, now time.Time, after *approvalCursor, limit int) ([]dueApproval, error)
	sweepApproval(ctx context.Context, orgID string, step *dueApproval, now time.Time, change *approvalChange) (*Run, bool, error)
	approvalDeciders(ctx context.Context, orgID, stepID string) (map[string]bool, error)
}

// approvalCursor is the keyset position of the candidate query.
type approvalCursor struct {
	dueAt time.Time
	id    string
}

// dueApproval is a waiting approval step with a deadline the sweep reads.
type dueApproval struct {
	StepID     string
	RunID      string
	StepIndex  int
	Input      JSONMap
	Output     JSONMap
	DueAt      time.Time
	RemindedAt *time.Time
	ClientID   string
	SiteID     string
}

// approvalChange is what the sweep writes for one step.
type approvalChange struct {
	kind string // expiredActionReject, expiredActionEscalate or "remind"
	// set are the output fields the sweep writes; sweepApproval merges them
	// into the output read under the lock.
	set      JSONMap
	newDueAt time.Time
}

const sweepRemind = "remind"

// Run handles the due approval steps of every organization. Each
// organization and each step run in transactions of their own; a failing
// one does not stop the others.
func (s *ApprovalSweep) Run(ctx context.Context) (ApprovalSweepResult, error) {
	approvalSweepRuns.Inc()
	res, err := s.run(ctx)
	if err != nil {
		approvalSweepErrors.Inc()
	}
	return res, err
}

func (s *ApprovalSweep) run(ctx context.Context) (ApprovalSweepResult, error) {
	var res ApprovalSweepResult
	store, ok := s.exec.repo.(approvalSweepStore)
	if !ok {
		return res, nil
	}
	orgs, err := s.orgs(ctx)
	if err != nil {
		return res, fmt.Errorf("%s: list organizations: %w", ApprovalSweepKind, err)
	}
	now := s.now().UTC()
	var errs []error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		octx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
		if oerr := s.sweepOrg(octx, store, orgID, now, &res); oerr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", ApprovalSweepKind, orgID, oerr))
		}
	}
	return res, errors.Join(errs...)
}

func (s *ApprovalSweep) sweepOrg(ctx context.Context, store approvalSweepStore, orgID string, now time.Time, res *ApprovalSweepResult) error {
	var cursor *approvalCursor
	var errs []error
	for {
		batch, err := store.dueApprovals(ctx, orgID, now, cursor, approvalSweepBatch)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for i := range batch {
			if serr := s.sweepStep(ctx, store, orgID, &batch[i], now, res); serr != nil {
				errs = append(errs, fmt.Errorf("step %s: %w", batch[i].StepID, serr))
			}
		}
		if len(batch) < approvalSweepBatch {
			// The expired form requests (request_form, WP-1102).
			if ierr := s.sweepInputs(ctx, orgID, now, res); ierr != nil {
				errs = append(errs, ierr)
			}
			return errors.Join(errs...)
		}
		last := batch[len(batch)-1]
		cursor = &approvalCursor{dueAt: last.DueAt, id: last.StepID}
	}
}

// sweepStep decides what is due for step and writes it.
func (s *ApprovalSweep) sweepStep(ctx context.Context, store approvalSweepStore, orgID string, step *dueApproval, now time.Time, res *ApprovalSweepResult) error {
	spec, err := parseApprovalSpec(step.Input)
	if err != nil {
		// A definition stored before WP-1098 with an invalid action: such a
		// step expires like on_timeout reject.
		spec = &approvalSpec{OnTimeout: "reject", Quorum: 1, ExcludeInitiator: true}
	}
	output := JSONMap{}
	change := &approvalChange{set: output}
	switch {
	case !step.DueAt.After(now):
		_, escalated := step.Output["escalated_at"]
		if spec.OnTimeout == "escalate" && !escalated && spec.Deadline > 0 {
			change.kind = expiredActionEscalate
			// The deadline moves once by the original one; from now when the
			// sweep runs late (a caught-up run), so the extended circle gets
			// the full deadline and a second run in the same minute changes
			// nothing.
			base := step.DueAt
			if now.After(base) {
				base = now
			}
			change.newDueAt = base.Add(spec.Deadline)
			output["escalated_at"] = now.Format(time.RFC3339)
			output["escalated_to"] = spec.EscalateTo
			output["due_at"] = change.newDueAt.UTC().Format(time.RFC3339)
		} else {
			change.kind = expiredActionReject
			output["decision"] = DecisionExpired
		}
	case spec.RemindBefore > 0 && step.RemindedAt == nil && !step.DueAt.Add(-spec.RemindBefore).After(now):
		change.kind = sweepRemind
	default:
		return nil
	}
	run, done, err := store.sweepApproval(ctx, orgID, step, now, change)
	if err != nil || !done {
		// Not done: the step is locked by a decision or no longer due.
		return err
	}
	switch change.kind {
	case expiredActionReject:
		res.Rejected++
		approvalsExpired.WithLabelValues(expiredActionReject).Inc()
		s.exec.notifyInitiatorExpired(ctx, orgID, run, step.StepIndex, spec)
	case expiredActionEscalate:
		res.Escalated++
		approvalsExpired.WithLabelValues(expiredActionEscalate).Inc()
		s.exec.notifyEscalation(ctx, orgID, run, step, spec, store)
	case sweepRemind:
		res.Reminded++
		s.exec.notifyReminder(ctx, orgID, run, step, spec, store)
	}
	return nil
}

// notifyInitiatorExpired tells the initiator of run that the deadline of
// its approval step expired (AK2); a run without an initiator (an event or
// a schedule started it) notifies no one.
func (e *Executor) notifyInitiatorExpired(ctx context.Context, orgID string, run *Run, stepIndex int, spec *approvalSpec) {
	if run.InitiatedBy == "" {
		return
	}
	e.sendApprovalNotice(ctx, orgID, run, stepIndex, []string{run.InitiatedBy}, DecisionExpired, &approvalNotice{
		subjectDE: "Genehmigung abgelaufen: ", subjectEN: "Approval expired: ",
		body: "The approval deadline expired; the workflow run was cancelled. " + spec.Message,
	})
}

// notifyEscalation notifies the extended circle of an escalated step (AK3):
// the approvers of the action plus escalate_to, without the users who
// decided already.
func (e *Executor) notifyEscalation(ctx context.Context, orgID string, run *Run, step *dueApproval, spec *approvalSpec, store approvalSweepStore) {
	if e.approvers == nil {
		return
	}
	members, _, err := e.resolveCircle(ctx, orgID, run.InitiatedBy, step.ClientID, step.SiteID, spec, spec.EscalateTo)
	if err != nil {
		slog.WarnContext(ctx, "workflow: escalation not notified", "run_id", run.ID, "error", err)
		return
	}
	decided, err := store.approvalDeciders(ctx, orgID, step.StepID)
	if err != nil {
		slog.WarnContext(ctx, "workflow: escalation not notified", "run_id", run.ID, "error", err)
		return
	}
	e.sendApprovalNotice(ctx, orgID, run, step.StepIndex, sortedMembers(members, decided), "escalated", &approvalNotice{
		subjectDE: "Genehmigung erforderlich (eskaliert): ", subjectEN: "Approval required (escalated): ", body: spec.Message,
	})
}

// notifyReminder reminds the approvers of step who have not decided (AK4).
func (e *Executor) notifyReminder(ctx context.Context, orgID string, run *Run, step *dueApproval, spec *approvalSpec, store approvalSweepStore) {
	if e.approvers == nil {
		return
	}
	members, _, err := e.resolveCircle(ctx, orgID, run.InitiatedBy, step.ClientID, step.SiteID, spec, escalatedCircle(step.Output))
	if err != nil {
		slog.WarnContext(ctx, "workflow: approval reminder not sent", "run_id", run.ID, "error", err)
		return
	}
	decided, err := store.approvalDeciders(ctx, orgID, step.StepID)
	if err != nil {
		slog.WarnContext(ctx, "workflow: approval reminder not sent", "run_id", run.ID, "error", err)
		return
	}
	e.sendApprovalNotice(ctx, orgID, run, step.StepIndex, sortedMembers(members, decided), sweepRemind, &approvalNotice{
		subjectDE: "Erinnerung: Genehmigung erforderlich: ", subjectEN: "Reminder: approval required: ", body: spec.Message,
	})
}

// sortedMembers returns the ids of members without those of skip, sorted.
func sortedMembers(members, skip map[string]bool) []string {
	ids := make([]string, 0, len(members))
	for id := range members {
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// ─── PostgreSQL ───────────────────────────────────────────────────────────────

// dueApprovals implements approvalSweepStore: the waiting approval steps of
// waiting runs with a deadline that is reached, or with a reminder not sent
// yet (remind_before is a duration of the action, compared in Go), in
// keyset order (due_at, id) after cursor.
func (r *PGRepository) dueApprovals(ctx context.Context, orgID string, now time.Time, after *approvalCursor, limit int) ([]dueApproval, error) {
	var out []dueApproval
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var afterDue *time.Time
		afterID := ""
		if after != nil {
			afterDue, afterID = &after.dueAt, after.id
		}
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, s.run_id::text, s.step_index, s.input, s.output, s.due_at, s.reminded_at,
			       COALESCE(s.client_id::text, ''), COALESCE(s.site_id::text, '')
			FROM workflow_step s
			JOIN workflow_run r ON r.id = s.run_id AND r.organization_id = s.organization_id
			WHERE s.organization_id = $1 AND s.status = 'waiting_approval' AND r.status = 'waiting_approval'
			  AND s.due_at IS NOT NULL
			  AND (s.due_at <= $2 OR (s.reminded_at IS NULL AND s.input ? 'remind_before'))
			  AND ($3::timestamptz IS NULL OR (s.due_at, s.id) > ($3::timestamptz, NULLIF($4, '')::uuid))
			ORDER BY s.due_at, s.id
			LIMIT $5`, orgID, now, afterDue, afterID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d dueApproval
			var in, outRaw []byte
			if err := rows.Scan(&d.StepID, &d.RunID, &d.StepIndex, &in, &outRaw, &d.DueAt, &d.RemindedAt, &d.ClientID, &d.SiteID); err != nil {
				return err
			}
			d.Input, d.Output = asMap(in), asMap(outRaw)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// sweepApproval implements approvalSweepStore: run and step are locked with
// FOR UPDATE SKIP LOCKED and checked again (still waiting, still due); done
// is false when either is locked or the step changed meanwhile.
func (r *PGRepository) sweepApproval(ctx context.Context, orgID string, step *dueApproval, now time.Time, change *approvalChange) (*Run, bool, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, `SELECT status FROM workflow_run WHERE organization_id=$1 AND id=$2 FOR UPDATE SKIP LOCKED`,
			orgID, step.RunID).Scan(&prev)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if prev != StatusWaitingApproval {
			return nil
		}
		var due time.Time
		var reminded *time.Time
		var outRaw []byte
		err = tx.QueryRow(ctx, `SELECT due_at, reminded_at, output FROM workflow_step
			WHERE organization_id=$1 AND id=$2 AND run_id=$3 AND status='waiting_approval' AND due_at IS NOT NULL FOR UPDATE SKIP LOCKED`,
			orgID, step.StepID, step.RunID).Scan(&due, &reminded, &outRaw)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		// The step must be in the state the decision was made on: a second
		// sweep (or an escalation since) changes nothing.
		if !due.Equal(step.DueAt) || (change.kind == sweepRemind && reminded != nil) {
			return nil
		}
		// The output as it is now (a vote may have committed since the
		// candidate query), with the fields of the sweep.
		output := asMap(outRaw)
		for k, v := range change.set {
			output[k] = v
		}
		var event string
		data := map[string]any{}
		switch change.kind {
		case expiredActionReject:
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET status='cancelled', output=$3, error=$4, updated_at=now() WHERE organization_id=$1 AND id=$2`,
				orgID, step.StepID, output, approvalExpiredText); err != nil {
				return err
			}
			run, err = scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='cancelled', finished_at=now(), next_attempt_at=NULL, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, step.RunID))
			event = events.WorkflowApprovalDecided
			data["decision"] = DecisionExpired
		case expiredActionEscalate:
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET output=$3, due_at=$4, updated_at=now() WHERE organization_id=$1 AND id=$2`,
				orgID, step.StepID, output, change.newDueAt); err != nil {
				return err
			}
			run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, step.RunID))
			event = events.WorkflowApprovalRequested
			data["escalated"] = true
			data["escalated_to"] = output["escalated_to"]
			data["due_at"] = change.newDueAt.UTC().Format(time.RFC3339)
		default:
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET reminded_at=$3 WHERE organization_id=$1 AND id=$2`,
				orgID, step.StepID, now); err != nil {
				return err
			}
			run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, step.RunID))
		}
		if err != nil {
			return err
		}
		if run.Steps, err = r.listStepsTx(ctx, tx, orgID, step.RunID); err != nil {
			return err
		}
		if err := recordRunTransition(ctx, tx, prev, run); err != nil {
			return err
		}
		if event == "" {
			return nil
		}
		payload := approvalEventData(run, step.StepID, step.StepIndex, step.ClientID, step.SiteID)
		for k, v := range data {
			payload[k] = v
		}
		if q, ok := output["quorum"]; ok {
			payload["quorum"] = q
		}
		return database.BeforeCommit(ctx, func() error {
			if _, aerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: event, EntityID: step.StepID, Data: payload}); aerr != nil {
				return fmt.Errorf("event of the approval deadline: %w", aerr)
			}
			return nil
		})
	})
	if err != nil {
		return nil, false, err
	}
	return run, run != nil, nil
}

// approvalDeciders implements approvalSweepStore: the users who decided on
// the step.
func (r *PGRepository) approvalDeciders(ctx context.Context, orgID, stepID string) (map[string]bool, error) {
	out := map[string]bool{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT approver_id::text FROM workflow_approval WHERE organization_id=$1 AND step_id=$2 AND approver_id IS NOT NULL`,
			orgID, stepID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out[id] = true
		}
		return rows.Err()
	})
	return out, err
}

// approvalNotice is the text of a notification about an approval step.
type approvalNotice struct {
	subjectDE, subjectEN, body string
}

// noticeResolver is the resolver of the direct recipients of a notice:
// the approver resolver, else the recipients of send_notification.
func (e *Executor) noticeResolver() notify.Resolver {
	if e.approvers != nil {
		return e.approvers
	}
	return e.recipients
}
