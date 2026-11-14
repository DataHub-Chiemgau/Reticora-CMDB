package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// The order approval as a workflow (WFL-04, WP-1108, E-230 option 2):
//
//   - The trigger {"type": "order_submitted"} starts, at most one active
//     definition per organization (uq_workflow_def_order_submitted, 409),
//     a run when an order is submitted, in the transaction of draft →
//     submitted (order.RunStarter, adapter OrderStarter, wired in the
//     router). Its context holds order_id, order_number, title,
//     requested_by, total_cost, currency, client_id and items. The order
//     remembers the run (approval_run_id); while it is open the direct
//     routes approve and reject are 409.
//   - The action {"type": "order_decision", "order_id": <optional, else the
//     context's>} applies the decision of the preceding require_approval
//     step with its decisive approver as actor (order.SetStatus, the
//     four-eyes rule included: approver = requester fails the step with
//     ErrFourEyes). It acts only in the approval run of the order (trigger
//     order_submitted, internal_order.approval_run_id = the run) and only
//     with an approver who holds order:approve; a definition with another
//     trigger may not name it (422). A rejection in an approval step that
//     an order_decision follows does not cancel the run: the step succeeds
//     with decision rejected, later approval steps are skipped, and
//     order_decision rejects the order. It puts the status of the order
//     into the run context (order_status), so the notification of the
//     requester can name the decision ("when").
//   - Every action may carry "when" (one MET-45 node over the run
//     context): an action whose condition does not hold is skipped, its
//     step succeeds with {"skipped": true}. The template order_approval
//     uses it for its quorum by total cost.

const (
	// TriggerOrderSubmitted is the trigger type and the run trigger of a
	// submitted order.
	TriggerOrderSubmitted = order.SubmittedEvent
	// orderDecisionActionType is the type of the action.
	orderDecisionActionType = "order_decision"
)

// Fixed texts of a failed order_decision step.
var (
	errOrdersUnavailable = errors.New("orders unavailable")
	errNoOrder           = errors.New("order_decision names no order")
	errNoApprovalStep    = errors.New("order_decision has no preceding approval decision")
	errNotApprovalRun    = errors.New("order_decision runs only in the approval run of its order")
	errNoOrderApprover   = errors.New("the deciding approver lacks order:approve")
)

// OrderStatusSetter reads an order and changes its status
// (order.Repository).
type OrderStatusSetter interface {
	GetByID(ctx context.Context, orgID, id string) (*order.Order, error)
	SetStatus(ctx context.Context, orgID, id, status, actorID string) (*order.Order, error)
}

// WithOrders makes order_decision change orders through orders (WP-1108).
func (e *Executor) WithOrders(orders OrderStatusSetter) *Executor {
	e.orders = orders
	return e
}

// orderStore is what the order approval needs of the repository; the
// PostgreSQL repository implements it.
type orderStore interface {
	// orderDefinitions are the active definitions with the trigger
	// order_submitted (at most one).
	orderDefinitions(ctx context.Context, orgID string) ([]Definition, error)
	// decisiveApprover is the user whose approval completed step stepID
	// (the last approval), rejecter the last rejection.
	decisiveApprover(ctx context.Context, orgID, stepID, decision string) (string, error)
}

// OrderStarter implements order.RunStarter with the executor (AK1).
type OrderStarter struct{ exec *Executor }

// NewOrderStarter returns the adapter the router hands the order routes.
func NewOrderStarter(exec *Executor) *OrderStarter { return &OrderStarter{exec: exec} }

var _ order.RunStarter = (*OrderStarter)(nil)

// StartApproval implements order.RunStarter: the run is stored pending in
// the transaction of ctx; after executes it once committed.
func (s *OrderStarter) StartApproval(ctx context.Context, orgID string, payload map[string]any) (runID string, after func(ctx context.Context), err error) {
	store, ok := s.exec.repo.(orderStore)
	if !ok {
		return "", nil, nil
	}
	var defs []Definition
	defs, err = store.orderDefinitions(ctx, orgID)
	if err != nil || len(defs) == 0 {
		return "", nil, err
	}
	def := &defs[0]
	run, err := s.exec.Start(ctx, orgID, def, TriggerOrderSubmitted, JSONMap(payload))
	if err != nil {
		return "", nil, err
	}
	return run.ID, func(ctx context.Context) {
		if _, xerr := s.exec.Execute(ctx, orgID, def, run); xerr != nil {
			slog.WarnContext(ctx, "workflow: order approval run failed", "run_id", run.ID, "error", xerr)
		}
	}, nil
}

// RunOpen implements order.RunStarter. The status is read
// organization-wide, whatever the scope of the caller: a run the caller
// cannot see (a client-scoped approver, a run without client) is still
// open for the direct routes. Only a run that no longer exists counts as
// ended (the foreign key clears approval_run_id with it).
func (s *OrderStarter) RunOpen(ctx context.Context, orgID, runID string) (bool, error) {
	run, err := s.exec.repo.GetRunForJob(ctx, orgID, runID)
	if errors.Is(err, ErrRunNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !endedStatus(run.Status), nil
}

// actionSkipped reports whether the "when" of an action does not hold over
// the run context.
func actionSkipped(action, runCtx JSONMap) (bool, error) {
	raw, has := action["when"]
	if !has || raw == nil {
		return false, nil
	}
	cond, err := parseCondition(raw)
	if err != nil {
		return false, errConditionInvalid
	}
	return !rules.EvaluateCondition(&cond, EvaluationValues(runCtx)), nil
}

// validateActionConditions checks the "when" of each action that is
// written: 422 on actions[i].when.
func validateActionConditions(actions []JSONMap) error {
	for i, action := range actions {
		raw, has := action["when"]
		if !has || raw == nil {
			continue
		}
		if _, err := parseCondition(raw); err != nil {
			return conditionFieldError(fmt.Sprintf("actions[%d].when", i), err.Error())
		}
	}
	return nil
}

// validateOrderWrite checks order_decision of a definition that is
// written: only a definition with the trigger order_submitted may name it
// (422 on actions[i].type).
func validateOrderWrite(trigger JSONMap, actions []JSONMap) error {
	if str(trigger, "type", "") == TriggerOrderSubmitted {
		return nil
	}
	for i, action := range actions {
		if typ, _ := action["type"].(string); typ == orderDecisionActionType {
			return conditionFieldError(fmt.Sprintf("actions[%d].type", i), "order_decision requires the trigger order_submitted")
		}
	}
	return nil
}

// orderDecisionFollows reports whether an order_decision follows the step
// at stepIndex in the actions of a run of the trigger order_submitted: a
// rejection there continues the run instead of cancelling it.
func orderDecisionFollows(run *Run, actions []JSONMap, stepIndex int) bool {
	if run == nil || run.Trigger != TriggerOrderSubmitted {
		return false
	}
	for i := stepIndex + 1; i < len(actions); i++ {
		if typ, _ := actions[i]["type"].(string); typ == orderDecisionActionType {
			return true
		}
	}
	return false
}

// rejectedBefore reports whether an approval step before index was
// rejected and continued the run (orderDecisionFollows): later approval
// steps are skipped, the order is rejected.
func rejectedBefore(stored map[int]Step, index int) bool {
	for i := range stored {
		s := stored[i]
		if i < index && s.ActionType == approvalActionType && s.Status == StatusSucceeded && s.Output["decision"] == DecisionRejected {
			return true
		}
	}
	return false
}

// rememberOrderStatus puts the status order_decision set into the run
// context (order_status), for the "when" of the following actions.
func rememberOrderStatus(run *Run, output JSONMap) {
	if status, ok := output["status"].(string); ok && status != "" {
		if run.Context == nil {
			run.Context = JSONMap{}
		}
		run.Context["order_status"] = status
	}
}

// mayDecideOrder reports whether user may decide an approval step of the
// approval run of an order: the circle holds workflow:approve, the order
// needs order:approve as well, in the scope of the run (the right of the
// direct routes).
func (e *Executor) mayDecideOrder(ctx context.Context, orgID string, run *Run, user string) (bool, error) {
	if run.Trigger != TriggerOrderSubmitted || e.approvers == nil {
		return true, nil
	}
	client, site := "", ""
	if store, ok := e.repo.(approvalStore); ok {
		var err error
		if client, site, err = store.runScope(ctx, orgID, run.ID); err != nil {
			return false, err
		}
	}
	recipients, err := e.approvers.Resolve(ctx, orgID, &notify.Audience{Permission: identity.PermOrderApprove,
		ClientID: client, SiteID: site, UserIDs: []string{user}})
	if err != nil {
		return false, fmt.Errorf("resolve the order approver: %w", err)
	}
	for _, r := range recipients {
		if r.UserID == user {
			return true, nil
		}
	}
	return false, nil
}

// lastApproval is the last approval step before index that was decided
// (not skipped): its id and decision.
func lastApproval(steps []Step, index int) (step *Step, decision string) {
	var found *Step
	for i := range steps {
		s := &steps[i]
		if s.StepIndex >= index || s.ActionType != approvalActionType || s.Output["skipped"] == true {
			continue
		}
		if found == nil || s.StepIndex > found.StepIndex {
			found = s
		}
	}
	if found == nil {
		return nil, ""
	}
	decision, _ = found.Output["decision"].(string)
	return found, decision
}

// orderDecision executes order_decision as step index of run (AK3).
func (e *Executor) orderDecision(ctx context.Context, orgID string, run *Run, index int, action JSONMap) (JSONMap, error) {
	if e.orders == nil {
		return nil, errOrdersUnavailable
	}
	store, ok := e.repo.(orderStore)
	if !ok {
		return nil, errOrdersUnavailable
	}
	orderID := str(action, "order_id", str(run.Context, "order_id", ""))
	if orderID == "" {
		return nil, errNoOrder
	}
	// Only the approval run of the order decides it: another definition
	// with its own approval would bypass threshold and quorum.
	if run.Trigger != TriggerOrderSubmitted {
		return nil, errNotApprovalRun
	}
	current, err := e.orders.GetByID(ctx, orgID, orderID)
	if err != nil {
		return nil, err
	}
	if current.ApprovalRunID != run.ID {
		return nil, errNotApprovalRun
	}
	steps, err := e.repo.ListSteps(ctx, orgID, run.ID)
	if err != nil {
		return nil, fmt.Errorf("list workflow steps: %w", err)
	}
	step, decision := lastApproval(steps, index)
	if step == nil || (decision != DecisionApproved && decision != DecisionRejected) {
		return nil, errNoApprovalStep
	}
	actor, err := store.decisiveApprover(ctx, orgID, step.ID, decision)
	if err != nil {
		return nil, err
	}
	if ok, aerr := e.mayDecideOrder(ctx, orgID, run, actor); aerr != nil || !ok {
		if aerr != nil {
			return nil, aerr
		}
		return nil, errNoOrderApprover
	}
	status := order.StatusApproved
	if decision == DecisionRejected {
		status = order.StatusRejected
	}
	o, err := e.orders.SetStatus(ctx, orgID, orderID, status, actor)
	if err != nil {
		return nil, err
	}
	return JSONMap{"order_id": o.ID, "status": o.Status, "approved_by": o.ApprovedBy}, nil
}

// ─── PostgreSQL ───────────────────────────────────────────────────────────────

// orderDefinitions implements orderStore.
func (r *PGRepository) orderDefinitions(ctx context.Context, orgID string) ([]Definition, error) {
	out := []Definition{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+defCols+` FROM workflow_def WHERE organization_id=$1 AND active AND trigger->>'type' = $2
			ORDER BY id LIMIT 1`, orgID, TriggerOrderSubmitted)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDef(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, err
}

// decisiveApprover implements orderStore.
func (r *PGRepository) decisiveApprover(ctx context.Context, orgID, stepID, decision string) (string, error) {
	var user string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT COALESCE(approver_id::text, '') FROM workflow_approval
			WHERE organization_id=$1 AND step_id=$2 AND decision=$3 ORDER BY decided_at DESC, id DESC LIMIT 1`, orgID, stepID, decision).Scan(&user)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return user, err
}
