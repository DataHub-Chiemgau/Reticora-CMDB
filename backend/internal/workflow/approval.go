package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The approval model of the action require_approval (WFL-02, RBA-05,
// NTF-05, WP-1098, E-230 option 2):
//
//	{"type": "require_approval", "message": "text",
//	 "approvers": {"users": [...], "teams": [...], "roles": [...]},
//	 "quorum": 2, "exclude_initiator": true,
//	 "deadline": "72h", "on_timeout": "reject" | "escalate",
//	 "escalate_to": {"users": [...], "teams": [...], "roles": [...]},
//	 "remind_before": "24h"}
//
// The approver circle are the active users who hold workflow:approve in
// client and site of the run and are named by user, team or role (none
// named: every holder of workflow:approve); with exclude_initiator (the
// default) the user who started the run is not one of them. The circle is
// resolved when the approval is requested (its notification) and again for
// every decision. Each decision is a row of workflow_approval; quorum
// approvals continue the run, one rejection cancels it. Several stages are
// several require_approval steps. Deadline, reminder and escalation are
// evaluated by the system job workflow.approval_sweep (approval_sweep.go,
// WP-1099).

const approvalActionType = "require_approval"

// Limits of a require_approval action (AK2).
const (
	maxApprovalQuorum   = 10
	maxApprovalDeadline = 30 * 24 * time.Hour
)

// Decisions of an approver; DecisionExpired (approval_sweep.go) is the
// decision of an expired deadline (WP-1099).
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

var (
	// ErrNotApprover: the caller is not in the approver circle of the
	// waiting step, or is its excluded initiator (403, AK4).
	ErrNotApprover = errors.New("you are not an approver of this workflow step")
	// ErrAlreadyDecided: the caller decided on this step before (409).
	ErrAlreadyDecided = errors.New("you have already decided on this workflow step")
	// ErrInvalidApproval: a stored require_approval action is invalid (a
	// definition stored before WP-1098; 400).
	ErrInvalidApproval = errors.New("invalid require_approval action")
)

// approvalCircle names users, teams and roles (approvers, escalate_to).
type approvalCircle struct {
	Users []string `json:"users,omitempty"`
	Teams []string `json:"teams,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

func (c approvalCircle) empty() bool {
	return len(c.Users) == 0 && len(c.Teams) == 0 && len(c.Roles) == 0
}

// approvalSpec is a parsed require_approval action.
type approvalSpec struct {
	Message          string
	Approvers        approvalCircle
	Quorum           int
	ExcludeInitiator bool
	Deadline         time.Duration
	OnTimeout        string
	EscalateTo       approvalCircle
	RemindBefore     time.Duration
}

// approvalSpecError is a refused field of a require_approval action.
type approvalSpecError struct {
	field, message string
}

func (e *approvalSpecError) Error() string { return e.field + " " + e.message }

// parseApprovalSpec reads a require_approval action; fields it does not
// name take their defaults (a definition stored before WP-1098 needs one
// approval of any holder of workflow:approve except the initiator).
func parseApprovalSpec(action JSONMap) (*approvalSpec, error) {
	s := &approvalSpec{Message: str(action, "message", "Approval required"), Quorum: 1, ExcludeInitiator: true, OnTimeout: "reject"}
	var err error
	if s.Approvers, err = parseCircle(action["approvers"], "approvers"); err != nil {
		return nil, err
	}
	if raw, has := action["quorum"]; has && raw != nil {
		n, ok := raw.(float64)
		if !ok || n != float64(int(n)) || n < 1 || n > maxApprovalQuorum {
			return nil, &approvalSpecError{"quorum", fmt.Sprintf("must be an integer from 1 to %d", maxApprovalQuorum)}
		}
		s.Quorum = int(n)
	}
	if raw, has := action["exclude_initiator"]; has && raw != nil {
		b, ok := raw.(bool)
		if !ok {
			return nil, &approvalSpecError{"exclude_initiator", "must be a boolean"}
		}
		s.ExcludeInitiator = b
	}
	if s.Deadline, err = parseApprovalDuration(action["deadline"], "deadline"); err != nil {
		return nil, err
	}
	if s.Deadline > maxApprovalDeadline {
		return nil, &approvalSpecError{"deadline", "must be at most 30 days"}
	}
	if raw, has := action["on_timeout"]; has && raw != nil {
		v, ok := raw.(string)
		if !ok || (v != "reject" && v != "escalate") {
			return nil, &approvalSpecError{"on_timeout", "must be reject or escalate"}
		}
		s.OnTimeout = v
	}
	if s.EscalateTo, err = parseCircle(action["escalate_to"], "escalate_to"); err != nil {
		return nil, err
	}
	if s.OnTimeout == "escalate" && s.EscalateTo.empty() {
		return nil, &approvalSpecError{"escalate_to", "is required with on_timeout escalate"}
	}
	if s.OnTimeout == "escalate" && s.Deadline == 0 {
		return nil, &approvalSpecError{"deadline", "is required with on_timeout escalate"}
	}
	if s.RemindBefore, err = parseApprovalDuration(action["remind_before"], "remind_before"); err != nil {
		return nil, err
	}
	if s.RemindBefore > 0 && (s.Deadline == 0 || s.RemindBefore >= s.Deadline) {
		return nil, &approvalSpecError{"remind_before", "needs a deadline and must be shorter than it"}
	}
	return s, nil
}

func parseCircle(raw any, field string) (approvalCircle, error) {
	var c approvalCircle
	if raw == nil {
		return c, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return c, &approvalSpecError{field, "must be an object with users, teams and roles"}
	}
	for key := range m {
		if key != "users" && key != "teams" && key != "roles" {
			return c, &approvalSpecError{field + "." + key, "is unknown; use users, teams or roles"}
		}
	}
	for key, dst := range map[string]*[]string{"users": &c.Users, "teams": &c.Teams, "roles": &c.Roles} {
		list, err := stringList(m[key])
		if err != nil {
			return c, &approvalSpecError{field + "." + key, "must be a list of non-empty strings"}
		}
		if key != "roles" {
			for _, id := range list {
				if !api.IsUUID(id) {
					return c, &approvalSpecError{field + "." + key, "must name ids (UUID)"}
				}
			}
		}
		*dst = list
	}
	return c, nil
}

// parseApprovalDuration reads a duration of a require_approval action: a
// Go duration ("90m", "72h") or whole days ("3d"); it must be positive.
// Absent is zero.
func parseApprovalDuration(raw any, field string) (time.Duration, error) {
	if raw == nil {
		return 0, nil
	}
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return 0, &approvalSpecError{field, `must be a duration such as "72h" or "3d"`}
	}
	var d time.Duration
	if days, isDays := strings.CutSuffix(s, "d"); isDays {
		n, err := strconv.Atoi(days)
		if err != nil || n > 3650 {
			return 0, &approvalSpecError{field, `must be a duration such as "72h" or "3d"`}
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, &approvalSpecError{field, `must be a duration such as "72h" or "3d"`}
		}
	}
	if d <= 0 {
		return 0, &approvalSpecError{field, "must be positive"}
	}
	return d, nil
}

// validateApprovalActions checks the require_approval actions of a
// definition that is written: an invalid value is 422 on
// actions[i].<field> (AK2).
func validateApprovalActions(actions []JSONMap) error {
	for i, action := range actions {
		if typ, _ := action["type"].(string); typ != approvalActionType {
			continue
		}
		if _, err := parseApprovalSpec(action); err != nil {
			var se *approvalSpecError
			if errors.As(err, &se) {
				return &api.FieldError{Field: fmt.Sprintf("actions[%d].%s", i, se.field), Message: se.message, Status: http.StatusUnprocessableEntity}
			}
			return err
		}
	}
	return nil
}

// WithApprovers resolves the approver circles with resolver (AK3); the
// router sets the resolver of the database. Without one (--no-db) the
// circle is everyone the route admits (workflow:approve), except the
// excluded initiator.
func (e *Executor) WithApprovers(resolver notify.Resolver) *Executor {
	e.approvers = resolver
	return e
}

// ApprovalResult is the answer of a decision: the run and the state of its
// approval step (AK4).
type ApprovalResult struct {
	*Run
	// Approvals counts the approvals of the step, Quorum the approvals it
	// needs.
	Approvals int `json:"approvals"`
	Quorum    int `json:"quorum"`
}

// approvalVote is one decision a store records.
type approvalVote struct {
	StepID   string
	Approver string
	Approved bool
	Comment  string
	Quorum   int
}

// approvalRequest is the change of a step into waiting_approval.
type approvalRequest struct {
	Step   *Step
	Output JSONMap
	DueAt  *time.Time
	Quorum int
}

// pendingApproval is a waiting approval step with what the inbox and the
// circle check need.
type pendingApproval struct {
	PendingApproval
	input           JSONMap
	output          JSONMap
	clientID        string
	siteID          string
	initiatedBy     string
	decidedByCaller bool
}

// PendingApproval is one entry of GET /api/v1/workflow-approvals (AK5).
type PendingApproval struct {
	RunID        string     `json:"run_id"`
	WorkflowID   string     `json:"workflow_id"`
	WorkflowName string     `json:"workflow_name"`
	StepID       string     `json:"step_id"`
	StepIndex    int        `json:"step_index"`
	Message      string     `json:"message"`
	DueAt        *time.Time `json:"due_at,omitempty"`
	Quorum       int        `json:"quorum"`
	Approvals    int        `json:"approvals"`
	CIID         string     `json:"ci_id,omitempty"`
	RequestedAt  time.Time  `json:"requested_at"`
}

// approvalStore keeps approvals; PGRepository and MemoryRepository
// implement it here, so repository.go and pg_repository.go keep their
// interface.
type approvalStore interface {
	// requestApproval sets the step waiting_approval with its output and
	// deadline and the run waiting_approval, in one transaction with
	// workflow.approval_requested.
	requestApproval(ctx context.Context, orgID string, run *Run, req *approvalRequest) (*Run, error)
	// decideVote records a decision under the row lock of the run and the
	// step (AK4): rejected cancels step and run, approved continues the run
	// once quorum approvals are stored (pending with its enqueues raised),
	// otherwise the run keeps waiting; workflow.approval_decided in the
	// same transaction.
	decideVote(ctx context.Context, orgID, runID string, v *approvalVote) (*ApprovalResult, error)
	// runScope returns client and site of a run visible in ctx.
	runScope(ctx context.Context, orgID, runID string) (clientID, siteID string, err error)
	// pendingApprovals lists the waiting approval steps visible in ctx,
	// newest request first; decidedByCaller marks those userID decided.
	pendingApprovals(ctx context.Context, orgID, userID string, mine bool) (steps []pendingApproval, capped bool, err error)
}

func (e *Executor) approvalStore() (approvalStore, error) {
	store, ok := e.repo.(approvalStore)
	if !ok {
		return nil, fmt.Errorf("workflow repository keeps no approvals")
	}
	return store, nil
}

// requestApproval moves step of run into waiting_approval (AK6): the
// output names message, quorum, the approvals so far and the deadline,
// the event workflow.approval_requested is written with it, and after the
// commit the approver circle is notified.
func (e *Executor) requestApproval(ctx context.Context, orgID string, run *Run, step *Step, action JSONMap) (*Run, error) {
	spec, err := parseApprovalSpec(action)
	if err != nil {
		// Only a definition stored before WP-1098 can hold an invalid
		// action; it is not repaired by a retry.
		return nil, fmt.Errorf("%w: %v", ErrInvalidApproval, err)
	}
	store, err := e.approvalStore()
	if err != nil {
		return nil, err
	}
	output := JSONMap{"message": spec.Message, "quorum": spec.Quorum, "approvals": 0}
	var due *time.Time
	if spec.Deadline > 0 {
		at := time.Now().UTC().Add(spec.Deadline)
		due = &at
		output["due_at"] = at.Format(time.RFC3339)
	}
	waiting, err := store.requestApproval(ctx, orgID, run, &approvalRequest{Step: step, Output: output, DueAt: due, Quorum: spec.Quorum})
	if err != nil {
		return nil, err
	}
	e.notifyApprovers(ctx, orgID, run, step.StepIndex, spec, "requested")
	return waiting, nil
}

// circle resolves the approver circle of spec for run (AK3): the users
// with workflow:approve in client and site of the run among those named
// (or all), without the initiator when it is excluded. all reports that
// no resolver exists and everyone the route admits is a member.
func (e *Executor) circle(ctx context.Context, orgID string, run *Run, spec *approvalSpec, extra approvalCircle) (members map[string]bool, all bool, err error) {
	if e.approvers == nil {
		return nil, true, nil
	}
	store, err := e.approvalStore()
	if err != nil {
		return nil, false, err
	}
	client, site, err := store.runScope(ctx, orgID, run.ID)
	if err != nil {
		return nil, false, err
	}
	return e.resolveCircle(ctx, orgID, run.InitiatedBy, client, site, spec, extra)
}

func (e *Executor) resolveCircle(ctx context.Context, orgID, initiator, client, site string, spec *approvalSpec, extra approvalCircle) (members map[string]bool, all bool, err error) {
	named := spec.Approvers
	if !named.empty() {
		named.Users = append(slices.Clone(named.Users), extra.Users...)
		named.Teams = append(slices.Clone(named.Teams), extra.Teams...)
		named.Roles = append(slices.Clone(named.Roles), extra.Roles...)
	}
	audience := notify.Audience{Permission: identity.PermWorkflowApprove, ClientID: client, SiteID: site,
		UserIDs: named.Users, TeamIDs: named.Teams, Roles: named.Roles, AllWithPermission: named.empty()}
	recipients, err := e.approvers.Resolve(ctx, orgID, &audience)
	if err != nil {
		return nil, false, fmt.Errorf("resolve the approvers: %w", err)
	}
	members = map[string]bool{}
	for _, r := range recipients {
		// Only users approve, never contacts.
		if r.UserID == "" || (spec.ExcludeInitiator && r.UserID == initiator) {
			continue
		}
		members[r.UserID] = true
	}
	return members, false, nil
}

// mayDecide reports whether user is in the circle of the waiting step.
func (e *Executor) mayDecide(ctx context.Context, orgID string, run *Run, step *Step, spec *approvalSpec, user string) (bool, error) {
	if spec.ExcludeInitiator && run.InitiatedBy != "" && user == run.InitiatedBy {
		return false, nil
	}
	members, all, err := e.circle(ctx, orgID, run, spec, escalatedCircle(step.Output))
	if err != nil {
		return false, err
	}
	return all || members[user], nil
}

// escalatedCircle is the circle a step was escalated to (escalated_to of
// its output, WP-1099); empty without one.
func escalatedCircle(output JSONMap) approvalCircle {
	c, err := parseCircle(output["escalated_to"], "escalated_to")
	if err != nil {
		return approvalCircle{}
	}
	return c
}

// notifyApprovers notifies the circle of the step after the commit in the
// category workflow (AK6, WP-1095); failures are logged, the approval
// stays requested.
func (e *Executor) notifyApprovers(ctx context.Context, orgID string, run *Run, stepIndex int, spec *approvalSpec, kind string) {
	if e.notifier == nil || e.approvers == nil {
		return
	}
	members, _, err := e.circle(ctx, orgID, run, spec, approvalCircle{})
	if err != nil {
		slog.WarnContext(ctx, "workflow: approvers not notified", "run_id", run.ID, "error", err)
		return
	}
	e.sendApprovalNotice(ctx, orgID, run, stepIndex, sortedMembers(members, nil), kind, &approvalNotice{
		subjectDE: "Genehmigung erforderlich: ", subjectEN: "Approval required: ", body: spec.Message,
	})
}

// sendApprovalNotice notifies the users ids about the approval step of run
// in the category workflow, under the dedup key <run_id>:<step_index>:
// <kind> (WP-1098, WP-1099); the subjects end with the workflow name.
// Failures are logged.
func (e *Executor) sendApprovalNotice(ctx context.Context, orgID string, run *Run, stepIndex int, ids []string, kind string, notice *approvalNotice) {
	resolver := e.noticeResolver()
	if e.notifier == nil || resolver == nil {
		return
	}
	if len(ids) == 0 {
		slog.WarnContext(ctx, "workflow: approval notice without recipient", "run_id", run.ID, "step_index", stepIndex, "kind", kind)
		return
	}
	recipients, err := resolver.Resolve(ctx, orgID, &notify.Audience{DirectUserIDs: ids})
	if err != nil {
		slog.WarnContext(ctx, "workflow: approval notice not sent", "run_id", run.ID, "kind", kind, "error", err)
		return
	}
	name := ""
	if def, derr := e.repo.GetDefinition(ctx, orgID, run.WorkflowID); derr == nil {
		name = def.Name
	}
	var brand *notify.Branding
	if source, ok := resolver.(notify.BrandingSource); ok {
		if brand, err = source.Branding(ctx, orgID); err != nil {
			slog.WarnContext(ctx, "workflow: branding of the approval notification", "error", err)
		}
	}
	orgLocale := ""
	if brand != nil {
		orgLocale = brand.Locale
	}
	orgTemplate := notify.TemplateLocale("", orgLocale)
	subjects := map[string]string{notify.LocaleDE: notice.subjectDE + name, notify.LocaleEN: notice.subjectEN + name}
	texts := map[string]notify.Text{}
	for _, locale := range []string{notify.LocaleDE, notify.LocaleEN} {
		texts[locale] = notify.Render(notify.WorkflowNotification, locale, map[string]any{
			"subject": oneLine(subjects[locale]), "body": notice.body, "workflow_name": name, "run_id": run.ID,
		})
	}
	if _, err := e.notifier.Notify(ctx, orgID, &notify.Request{Category: notify.CategoryWorkflow, Personal: true,
		Subject: texts[orgTemplate].Subject, Body: texts[orgTemplate].Body, Texts: texts, Branding: brand,
		Data:     map[string]any{"run_id": run.ID, "workflow_id": run.WorkflowID, "step_index": stepIndex},
		DedupKey: run.ID + ":" + strconv.Itoa(stepIndex) + ":" + kind, Recipients: recipients}); err != nil {
		slog.WarnContext(ctx, "workflow: approval notice not sent", "run_id", run.ID, "kind", kind, "error", err)
	}
}

// Decide records the decision of the calling user on the waiting approval
// step of a run (AK4). An unknown run or one outside the caller's scope is
// ErrRunNotFound (404), a run without a waiting approval ErrNotWaiting
// (400), a caller outside the circle ErrNotApprover (403), a second
// decision ErrAlreadyDecided (409). Quorum approvals continue the run over
// the job queue (or in the request without one), a rejection cancels it.
func (e *Executor) Decide(ctx context.Context, orgID, runID, decision, comment string) (*ApprovalResult, error) {
	run, err := e.repo.GetRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusWaitingApproval {
		return nil, ErrNotWaiting
	}
	if decision != DecisionApproved && decision != DecisionRejected {
		return nil, ErrInvalidDecision
	}
	var step *Step
	for i := range run.Steps {
		if run.Steps[i].Status == StatusWaitingApproval {
			step = &run.Steps[i]
		}
	}
	if step == nil {
		return nil, ErrNotWaiting
	}
	spec, err := parseApprovalSpec(step.Input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidApproval, err)
	}
	user := tenant.FromContext(ctx).UserID
	allowed, err := e.mayDecide(ctx, orgID, run, step, spec, user)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotApprover
	}
	store, err := e.approvalStore()
	if err != nil {
		return nil, err
	}
	res, err := store.decideVote(ctx, orgID, run.ID, &approvalVote{StepID: step.ID, Approver: user, Approved: decision == DecisionApproved,
		Comment: comment, Quorum: spec.Quorum})
	if err != nil {
		return nil, err
	}
	if res.Status != StatusPending {
		// Rejected (cancelled), or still waiting for more approvals.
		return res, nil
	}
	var next *Run
	if e.queue == nil {
		next, err = e.resume(ctx, orgID, res.Run, attempt{})
	} else {
		next, err = e.continueApproved(ctx, orgID, res.Run)
	}
	if err != nil {
		return nil, err
	}
	res.Run = next
	return res, nil
}

// approvalEventData is the data of workflow.approval_* (WorkflowApprovalData
// with the fields of AK6).
func approvalEventData(run *Run, stepID string, stepIndex int, client, site string) map[string]any {
	data := map[string]any{"id": stepID, "run_id": run.ID, "step_id": stepID, "step_key": strconv.Itoa(stepIndex),
		"step_index": stepIndex, "workflow_id": run.WorkflowID}
	if run.CIID != "" {
		data["ci_id"] = run.CIID
	}
	if client != "" {
		data["client_id"] = client
	}
	if site != "" {
		data["site_id"] = site
	}
	return data
}

// ─── PostgreSQL ───────────────────────────────────────────────────────────────

// requestApproval implements approvalStore.
func (r *PGRepository) requestApproval(ctx context.Context, orgID string, run *Run, req *approvalRequest) (*Run, error) {
	return r.changeRun(ctx, orgID, run.ID, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if prev != StatusWaitingApproval && endedStatus(prev) {
			return nil, ErrRunEnded
		}
		var client, site string
		var stepIndex int
		err := tx.QueryRow(ctx, `UPDATE workflow_step SET status='waiting_approval', output=$3, error='', due_at=$4, reminded_at=NULL, updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING step_index, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')`,
			orgID, req.Step.ID, req.Output, req.DueAt).Scan(&stepIndex, &client, &site)
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("approval step not found")
		}
		if err != nil {
			return nil, err
		}
		waiting, err := scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='waiting_approval', updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, run.ID))
		if err != nil {
			return nil, err
		}
		data := approvalEventData(waiting, req.Step.ID, stepIndex, client, site)
		data["quorum"] = req.Quorum
		if req.DueAt != nil {
			data["due_at"] = req.DueAt.UTC().Format(time.RFC3339)
		}
		return waiting, database.BeforeCommit(ctx, func() error {
			if _, aerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.WorkflowApprovalRequested, EntityID: req.Step.ID, Data: data}); aerr != nil {
				return fmt.Errorf("event of the approval request: %w", aerr)
			}
			return nil
		})
	})
}

// decideVote implements approvalStore: the run row (changeRun) and the step
// row are locked; the unique key (organization_id, step_id, approver_id)
// refuses a second decision of the same user.
func (r *PGRepository) decideVote(ctx context.Context, orgID, runID string, v *approvalVote) (*ApprovalResult, error) {
	var approvals int
	run, err := r.changeRun(ctx, orgID, runID, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if prev != StatusWaitingApproval {
			return nil, ErrNotWaiting
		}
		var stepIndex int
		var client, site string
		var output JSONMap
		var outRaw []byte
		err := tx.QueryRow(ctx, `SELECT step_index, output, COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM workflow_step
			WHERE organization_id=$1 AND id=$2 AND run_id=$3 AND status='waiting_approval' FOR UPDATE`, orgID, v.StepID, runID).Scan(&stepIndex, &outRaw, &client, &site)
		if err == pgx.ErrNoRows {
			return nil, ErrNotWaiting
		}
		if err != nil {
			return nil, err
		}
		output = asMap(outRaw)
		decision := DecisionRejected
		if v.Approved {
			decision = DecisionApproved
		}
		// The approver is a user of the organization: a NULL approver_id
		// would escape the unique key (NULLs are distinct) and let the same
		// principal vote any number of times.
		var approverID string
		err = tx.QueryRow(ctx, `SELECT u.id::text FROM app_user u WHERE u.organization_id=$1 AND u.id::text=NULLIF($2, '')`,
			orgID, v.Approver).Scan(&approverID)
		if err == pgx.ErrNoRows {
			return nil, ErrNotApprover
		}
		if err != nil {
			return nil, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO workflow_approval (organization_id, run_id, step_id, approver_id, decision, comment)
			VALUES ($1, $2, $3, $4::uuid, $5, $6)
			ON CONFLICT (organization_id, step_id, approver_id) DO NOTHING
			RETURNING approver_id::text`, orgID, runID, v.StepID, approverID, decision, v.Comment).Scan(&approverID)
		if err == pgx.ErrNoRows {
			return nil, ErrAlreadyDecided
		}
		if err != nil {
			return nil, err
		}
		if cerr := tx.QueryRow(ctx, `SELECT count(*) FROM workflow_approval WHERE organization_id=$1 AND step_id=$2 AND decision='approved'`,
			orgID, v.StepID).Scan(&approvals); cerr != nil {
			return nil, cerr
		}
		output["approvals"] = approvals
		output["quorum"] = v.Quorum
		var next *Run
		switch {
		case !v.Approved:
			output["decision"] = DecisionRejected
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET status='cancelled', output=$3, error=$4, updated_at=now() WHERE organization_id=$1 AND id=$2`,
				orgID, v.StepID, output, approvalRejectedText); err != nil {
				return nil, err
			}
			next, err = scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='cancelled', finished_at=now(), next_attempt_at=NULL, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, runID))
		case approvals >= v.Quorum:
			output["decision"] = DecisionApproved
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET status='succeeded', output=$3, error='', updated_at=now() WHERE organization_id=$1 AND id=$2`,
				orgID, v.StepID, output); err != nil {
				return nil, err
			}
			next, err = scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', enqueues=enqueues+1, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, runID))
		default:
			if _, err = tx.Exec(ctx, `UPDATE workflow_step SET output=$3, updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, v.StepID, output); err != nil {
				return nil, err
			}
			next, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, runID))
		}
		if err != nil {
			return nil, err
		}
		data := approvalEventData(next, v.StepID, stepIndex, client, site)
		data["decision"], data["approvals"], data["quorum"] = decision, approvals, v.Quorum
		if approverID != "" {
			data["approver_id"] = approverID
		}
		return next, database.BeforeCommit(ctx, func() error {
			if _, aerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.WorkflowApprovalDecided, EntityID: v.StepID, Data: data}); aerr != nil {
				return fmt.Errorf("event of the approval decision: %w", aerr)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return &ApprovalResult{Run: run, Approvals: approvals, Quorum: v.Quorum}, nil
}

// runScope implements approvalStore.
func (r *PGRepository) runScope(ctx context.Context, orgID, runID string) (client, site string, err error) {
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM workflow_run WHERE organization_id=$1 AND id=$2`,
			orgID, runID).Scan(&client, &site)
		if qerr == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		return qerr
	})
	return client, site, err
}

// maxPendingApprovals bounds the waiting approval steps the inbox reads;
// beyond it the list is marked total_capped.
const maxPendingApprovals = 5000

// pendingApprovals implements approvalStore: the waiting approval steps of
// waiting runs visible in the scope of ctx (row level security). With mine
// the steps the user decided on, and those of runs the user initiated whose
// action excludes the initiator (the default), are left out in SQL already.
// capped reports more than maxPendingApprovals steps.
func (r *PGRepository) pendingApprovals(ctx context.Context, orgID, userID string, mine bool) ([]pendingApproval, bool, error) {
	var out []pendingApproval
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, s.run_id::text, s.step_index, s.input, s.output, s.due_at, s.updated_at,
			       r.workflow_id::text, d.name, COALESCE(r.ci_id::text, ''), COALESCE(r.initiated_by::text, ''),
			       COALESCE(r.client_id::text, ''), COALESCE(r.site_id::text, ''),
			       (SELECT count(*) FROM workflow_approval a WHERE a.organization_id = s.organization_id AND a.step_id = s.id AND a.decision = 'approved'),
			       EXISTS (SELECT 1 FROM workflow_approval a WHERE a.organization_id = s.organization_id AND a.step_id = s.id
			                 AND a.approver_id::text = NULLIF($2, ''))
			FROM workflow_step s
			JOIN workflow_run r ON r.id = s.run_id AND r.organization_id = s.organization_id
			JOIN workflow_def d ON d.id = r.workflow_id AND d.organization_id = r.organization_id
			WHERE s.organization_id = $1 AND s.status = 'waiting_approval' AND r.status = 'waiting_approval'
			  AND (NOT $4::boolean OR (
			        NOT EXISTS (SELECT 1 FROM workflow_approval a WHERE a.organization_id = s.organization_id AND a.step_id = s.id
			                      AND a.approver_id::text = NULLIF($2, ''))
			        AND NOT (r.initiated_by IS NOT NULL AND r.initiated_by::text = NULLIF($2, '')
			                 AND (s.input -> 'exclude_initiator') IS DISTINCT FROM 'false'::jsonb)))
			ORDER BY s.updated_at DESC, s.id DESC
			LIMIT $3`, orgID, userID, maxPendingApprovals+1, mine)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p pendingApproval
			var in, outRaw []byte
			var due *time.Time
			if err := rows.Scan(&p.StepID, &p.RunID, &p.StepIndex, &in, &outRaw, &due, &p.RequestedAt, &p.WorkflowID, &p.WorkflowName,
				&p.CIID, &p.initiatedBy, &p.clientID, &p.siteID, &p.Approvals, &p.decidedByCaller); err != nil {
				return err
			}
			if due != nil {
				d := due.UTC()
				p.DueAt = &d
			}
			p.input, p.output = asMap(in), asMap(outRaw)
			p.Message = str(p.output, "message", "")
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxPendingApprovals {
		return out[:maxPendingApprovals], true, nil
	}
	return out, false, nil
}

// ─── memory (--no-db) ─────────────────────────────────────────────────────────

// The memory repository keeps the decisions of a step in its output under
// memoryVotes (approver → decision); it writes no event (no outbox).
const memoryVotes = "votes"

// requestApproval implements approvalStore.
func (r *MemoryRepository) requestApproval(_ context.Context, orgID string, run *Run, req *approvalRequest) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.runs[run.ID]
	s := r.steps[req.Step.ID]
	if cur == nil || cur.OrganizationID != orgID || s == nil || s.OrganizationID != orgID {
		return nil, ErrRunNotFound
	}
	if cur.Status != StatusWaitingApproval && endedStatus(cur.Status) {
		return nil, ErrRunEnded
	}
	now := time.Now().UTC()
	out := JSONMap{}
	for k, v := range req.Output {
		out[k] = v
	}
	out[memoryVotes] = map[string]any{}
	s.Status, s.Output, s.Error, s.UpdatedAt = StatusWaitingApproval, out, "", now
	cur.Status, cur.UpdatedAt = StatusWaitingApproval, now
	cp := *cur
	cp.Steps = r.stepsForLocked(orgID, run.ID)
	return &cp, nil
}

// decideVote implements approvalStore.
func (r *MemoryRepository) decideVote(_ context.Context, orgID, runID string, v *approvalVote) (*ApprovalResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[runID]
	if run == nil || run.OrganizationID != orgID {
		return nil, ErrRunNotFound
	}
	s := r.steps[v.StepID]
	if run.Status != StatusWaitingApproval || s == nil || s.RunID != runID || s.Status != StatusWaitingApproval {
		return nil, ErrNotWaiting
	}
	out := JSONMap{}
	for k, val := range s.Output {
		out[k] = val
	}
	votes := map[string]any{}
	if prev, ok := out[memoryVotes].(map[string]any); ok {
		for k, val := range prev {
			votes[k] = val
		}
	}
	if _, decided := votes[v.Approver]; decided {
		return nil, ErrAlreadyDecided
	}
	decision := DecisionRejected
	if v.Approved {
		decision = DecisionApproved
	}
	votes[v.Approver] = decision
	approvals := 0
	for _, d := range votes {
		if d == DecisionApproved {
			approvals++
		}
	}
	out[memoryVotes], out["approvals"], out["quorum"] = votes, approvals, v.Quorum
	now := time.Now().UTC()
	s.Output, s.UpdatedAt = out, now
	switch {
	case !v.Approved:
		out["decision"] = DecisionRejected
		s.Status, s.Error = StatusCancelled, approvalRejectedText
		run.Status, run.FinishedAt, run.NextAttemptAt = StatusCancelled, &now, nil
	case approvals >= v.Quorum:
		out["decision"] = DecisionApproved
		s.Status, s.Error = StatusSucceeded, ""
		run.Status = StatusPending
		run.Enqueues++
	}
	run.UpdatedAt = now
	cp := *run
	cp.Steps = r.stepsForLocked(orgID, runID)
	return &ApprovalResult{Run: &cp, Approvals: approvals, Quorum: v.Quorum}, nil
}

// runScope implements approvalStore: the memory repository knows no scope.
func (r *MemoryRepository) runScope(_ context.Context, orgID, runID string) (client, site string, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if run := r.runs[runID]; run == nil || run.OrganizationID != orgID {
		return "", "", ErrRunNotFound
	}
	return client, site, nil
}

// pendingApprovals implements approvalStore.
func (r *MemoryRepository) pendingApprovals(_ context.Context, orgID, userID string, _ bool) ([]pendingApproval, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []pendingApproval
	for _, s := range r.steps {
		run := r.runs[s.RunID]
		if s.OrganizationID != orgID || s.Status != StatusWaitingApproval || run == nil || run.Status != StatusWaitingApproval {
			continue
		}
		p := pendingApproval{PendingApproval: PendingApproval{RunID: run.ID, WorkflowID: run.WorkflowID, StepID: s.ID, StepIndex: s.StepIndex,
			Message: str(s.Output, "message", ""), RequestedAt: s.UpdatedAt, CIID: run.CIID}, input: s.Input, output: s.Output, initiatedBy: run.InitiatedBy}
		if d := r.definitions[run.WorkflowID]; d != nil {
			p.WorkflowName = d.Name
		}
		if votes, ok := s.Output[memoryVotes].(map[string]any); ok {
			_, p.decidedByCaller = votes[userID]
			for _, d := range votes {
				if d == DecisionApproved {
					p.Approvals++
				}
			}
		}
		if raw, ok := s.Output["due_at"].(string); ok {
			if at, err := time.Parse(time.RFC3339, raw); err == nil {
				p.DueAt = &at
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b pendingApproval) int {
		if c := b.RequestedAt.Compare(a.RequestedAt); c != 0 {
			return c
		}
		return strings.Compare(b.StepID, a.StepID)
	})
	return out, false, nil
}

// ─── inbox ────────────────────────────────────────────────────────────────────

// ListApprovals handles GET /api/v1/workflow-approvals (AK5): with
// mine=true the waiting approval steps whose circle holds the caller and
// on which the caller has not decided yet; with mine=false (only with
// workflow:write as well) every waiting approval step in the caller's
// scope. Without mine a caller with workflow:write gets every step, any
// other caller the own inbox. Paged by offset cursor, limit at most 200
// (api.Page).
func (h *Handler) ListApprovals(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	mine := r.URL.Query().Get("mine")
	if mine != "" && mine != "true" && mine != "false" {
		api.WriteFieldError(w, &api.FieldError{Field: "mine", Message: "must be true or false", Status: http.StatusUnprocessableEntity})
		return
	}
	// Every waiting step needs workflow:write besides; without mine the
	// list is that of a caller with it and the own inbox otherwise, an
	// explicit mine=false without it is 403.
	principal, has := identity.PrincipalFromContext(r.Context())
	writer := has && principal.Has(identity.PermWorkflowWrite)
	if mine == "false" && !writer {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(identity.PermWorkflowWrite))
		return
	}
	own := mine == "true" || (mine == "" && !writer)
	items, capped, err := h.exec.pendingApprovals(r.Context(), t.OrganizationID, t.UserID, own)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	resp := api.Page(items, page)
	resp.TotalCapped = capped
	api.WriteJSON(w, http.StatusOK, resp)
}

// pendingApprovals lists the waiting approval steps of the inbox; mine
// keeps those whose circle holds user and on which user has not decided.
// A circle is resolved once per request for each distinct circle and scope
// (circles), not once per step. capped: the store read its maximum.
func (e *Executor) pendingApprovals(ctx context.Context, orgID, user string, mine bool) ([]PendingApproval, bool, error) {
	store, err := e.approvalStore()
	if err != nil {
		return nil, false, err
	}
	all, capped, err := store.pendingApprovals(ctx, orgID, user, mine)
	if err != nil {
		return nil, false, err
	}
	circles := map[string]map[string]bool{}
	out := []PendingApproval{}
	for i := range all {
		p := &all[i]
		spec, perr := parseApprovalSpec(p.input)
		if perr != nil {
			continue
		}
		p.Quorum = spec.Quorum
		if mine {
			if p.decidedByCaller || (spec.ExcludeInitiator && p.initiatedBy != "" && p.initiatedBy == user) {
				continue
			}
			if e.approvers != nil {
				extra := escalatedCircle(p.output)
				key, kerr := json.Marshal([]any{spec.Approvers, extra, spec.ExcludeInitiator, p.initiatedBy, p.clientID, p.siteID})
				if kerr != nil {
					return nil, false, kerr
				}
				members, seen := circles[string(key)]
				if !seen {
					var cerr error
					if members, _, cerr = e.resolveCircle(ctx, orgID, p.initiatedBy, p.clientID, p.siteID, spec, extra); cerr != nil {
						return nil, false, cerr
					}
					circles[string(key)] = members
				}
				if !members[user] {
					continue
				}
			}
		}
		out = append(out, p.PendingApproval)
	}
	return out, capped, nil
}
