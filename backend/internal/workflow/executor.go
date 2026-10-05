package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

type Executor struct {
	repo       Repository
	tickets    ticket.Repository
	cis        ci.Repository
	forms      form.Repository
	dispatcher *webhook.Dispatcher
	guard      *override.Guard
}

func NewExecutor(repo Repository, tickets ticket.Repository, cis ci.Repository, forms form.Repository, dispatcher *webhook.Dispatcher) *Executor {
	return &Executor{repo: repo, tickets: tickets, cis: cis, forms: forms, dispatcher: dispatcher}
}

// WithFieldGuard sets the central write decision for set_ci_field
// (OVR-02). Without one the rank table alone decides.
func (e *Executor) WithFieldGuard(g *override.Guard) *Executor {
	e.guard = g
	return e
}

func (e *Executor) Trigger(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap) (*Run, error) {
	if def == nil {
		return nil, fmt.Errorf("workflow definition is required")
	}
	if !triggerMatches(def.Trigger, trigger) {
		return nil, fmt.Errorf("workflow trigger does not match")
	}
	run := &Run{OrganizationID: orgID, WorkflowID: def.ID, Status: StatusPending, Trigger: trigger, Context: payload, StartedAt: time.Now().UTC()}
	if err := e.repo.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	return e.resume(ctx, orgID, def, run, "")
}

func (e *Executor) Approve(ctx context.Context, orgID, runID, decision, comment string) (*Run, error) {
	run, err := e.repo.GetRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	def, err := e.repo.GetDefinition(ctx, orgID, run.WorkflowID)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusWaitingApproval {
		return nil, fmt.Errorf("workflow run is not waiting for approval")
	}
	steps, err := e.repo.ListSteps(ctx, orgID, runID)
	if err != nil {
		return nil, fmt.Errorf("list workflow steps: %w", err)
	}
	var waiting *Step
	for i := range steps {
		if steps[i].Status == StatusWaitingApproval {
			waiting = &steps[i]
			break
		}
	}
	if waiting == nil {
		return nil, fmt.Errorf("approval step not found")
	}
	out := JSONMap{"decision": decision, "comment": comment}
	if decision == "rejected" {
		if _, err := e.repo.UpdateStep(ctx, orgID, waiting.ID, StatusCancelled, out, "approval rejected"); err != nil {
			return nil, fmt.Errorf("cancel approval step: %w", err)
		}
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(ctx, orgID, runID, StatusCancelled, &now, run.Context)
	}
	if decision != "approved" {
		return nil, fmt.Errorf("decision must be approved or rejected")
	}
	if _, err := e.repo.UpdateStep(ctx, orgID, waiting.ID, StatusSucceeded, out, ""); err != nil {
		return nil, fmt.Errorf("complete approval step: %w", err)
	}
	return e.resume(ctx, orgID, def, run, waiting.ID)
}

func (e *Executor) resume(ctx context.Context, orgID string, def *Definition, run *Run, _ string) (*Run, error) {
	if !conditionsMet(def.Conditions, run.Context) {
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusCancelled, &now, run.Context)
	}
	if _, err := e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusRunning, nil, run.Context); err != nil {
		slog.Error("workflow: failed to mark run running", "run_id", run.ID, "error", err)
	}
	steps, err := e.repo.ListSteps(ctx, orgID, run.ID)
	if err != nil {
		e.failRun(ctx, orgID, run, fmt.Errorf("list workflow steps: %w", err))
		return nil, fmt.Errorf("list workflow steps: %w", err)
	}
	start := len(steps)
	for i := start; i < len(def.Actions); i++ {
		action := def.Actions[i]
		typ, err := actionType(action)
		if err != nil {
			e.failRun(ctx, orgID, run, err)
			return nil, err
		}
		step := &Step{OrganizationID: orgID, RunID: run.ID, StepIndex: i, ActionType: typ, Status: StatusRunning, Input: action, Output: JSONMap{}}
		if err := e.repo.AppendStep(ctx, step); err != nil {
			return nil, err
		}
		out, wait, err := e.executeAction(ctx, orgID, run, action)
		if wait {
			if _, err := e.repo.UpdateStep(ctx, orgID, step.ID, StatusWaitingApproval, out, ""); err != nil {
				slog.Error("workflow: failed to mark step waiting for approval", "step_id", step.ID, "error", err)
			}
			return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusWaitingApproval, nil, run.Context)
		}
		if err != nil {
			if _, uerr := e.repo.UpdateStep(ctx, orgID, step.ID, StatusFailed, out, err.Error()); uerr != nil {
				slog.Error("workflow: failed to mark step failed", "step_id", step.ID, "error", uerr)
			}
			now := time.Now().UTC()
			return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusFailed, &now, run.Context)
		}
		if _, err := e.repo.UpdateStep(ctx, orgID, step.ID, StatusSucceeded, out, ""); err != nil {
			e.failRun(ctx, orgID, run, fmt.Errorf("update step %s: %w", step.ID, err))
			return nil, fmt.Errorf("update workflow step: %w", err)
		}
	}
	now := time.Now().UTC()
	return e.repo.UpdateRunStatus(ctx, orgID, run.ID, StatusSucceeded, &now, run.Context)
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
		return "", fmt.Errorf("workflow action type must be a string, got %T", raw)
	}
	if typ == "" {
		return "noop", nil
	}
	return typ, nil
}

func (e *Executor) executeAction(ctx context.Context, orgID string, run *Run, action JSONMap) (JSONMap, bool, error) {
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
	case "send_webhook":
		event := str(action, "event", "workflow.event")
		if e.dispatcher != nil {
			e.dispatcher.Dispatch(ctx, orgID, event, map[string]any{"run_id": run.ID, "context": run.Context})
		}
		return JSONMap{"event": event}, false, nil
	case "set_ci_field":
		if e.cis == nil {
			return nil, false, fmt.Errorf("ci repository unavailable")
		}
		return e.setCIField(ctx, orgID, action)
	case "require_approval":
		return JSONMap{"message": str(action, "message", "Approval required")}, true, nil
	case "submit_form":
		if e.forms == nil {
			return nil, false, fmt.Errorf("form repository unavailable")
		}
		formID := str(action, "form_id", "")
		var vals map[string]any
		if raw, exists := action["values"]; exists && raw != nil {
			v, ok := raw.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("submit_form action values must be an object, got %T", raw)
			}
			vals = v
		}
		sub := &form.Submission{OrganizationID: orgID, FormID: formID, Values: form.JSONMap(vals), SubmittedBy: "workflow", Status: "submitted"}
		if err := e.forms.CreateSubmission(ctx, sub); err != nil {
			return nil, false, err
		}
		return JSONMap{"submission_id": sub.ID}, false, nil
	case "noop", "":
		return JSONMap{}, false, nil
	default:
		return nil, false, fmt.Errorf("unsupported workflow action %q", typ)
	}
}

func triggerMatches(trigger JSONMap, event string) bool {
	typ := str(trigger, "type", "")
	if typ == "manual" || typ == "schedule" {
		return event == typ || event == "manual"
	}
	return str(trigger, "event", "") == event
}
func conditionsMet(conditions []JSONMap, ctx JSONMap) bool {
	for _, c := range conditions {
		if !eval(c, ctx) {
			return false
		}
	}
	return true
}
func eval(cond JSONMap, ctx JSONMap) bool {
	field := str(cond, "field", "")
	op := str(cond, "op", "eq")
	want := cond["value"]
	got := lookup(ctx, field)
	switch op {
	case "eq":
		return fmt.Sprint(got) == fmt.Sprint(want)
	case "ne":
		return fmt.Sprint(got) != fmt.Sprint(want)
	case "exists":
		return got != nil
	case "not_empty":
		return fmt.Sprint(got) != ""
	case "contains":
		return strings.Contains(fmt.Sprint(got), fmt.Sprint(want))
	case "matches":
		re, err := regexp.Compile(fmt.Sprint(want))
		return err == nil && re.MatchString(fmt.Sprint(got))
	default:
		return false
	}
}
func lookup(m map[string]any, path string) any {
	cur := any(m)
	for _, p := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}
func str(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// setCIField writes one CI field as source workflow (rank 92) through the
// central decision (OVR-02, WFL-02): a manual override is never replaced,
// a refused write is reported in the action output, and an unreadable
// override state fails the action.
func (e *Executor) setCIField(ctx context.Context, orgID string, action JSONMap) (JSONMap, bool, error) {
	ciID := str(action, "ci_id", "")
	field := str(action, "field", "")
	value := action["value"]
	if field == "" {
		return nil, false, fmt.Errorf("set_ci_field needs a field")
	}
	item, err := e.cis.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, false, err
	}
	// Workflow writes rank 92 and raise the CI version (API-07).
	req := ci.UpdateRequest{Authoritative: true}
	var current any
	switch field {
	case "status":
		v := fmt.Sprint(value)
		value, current, req.Status = v, item.Status, &v
	case "name":
		v := fmt.Sprint(value)
		value, current, req.Name = v, item.Name, &v
	default:
		current = item.Attributes[field]
		req.Attributes = map[string]any{field: value}
	}
	w := &override.Write{
		OrganizationID: orgID, CIID: item.ID, Field: field, Value: value, Source: sourceWorkflow,
		ObservedAt: time.Now().UTC(), Current: current, FallbackSource: item.DiscoverySource,
	}
	d := e.guard.Decide(ctx, w)
	if d.Reason == override.ReasonError {
		return nil, false, fmt.Errorf("set_ci_field %s: override state unreadable, nothing written", field)
	}
	if d.Write {
		if _, err = e.cis.Update(ctx, orgID, item.ID, req); err != nil {
			return nil, false, err
		}
	}
	if d.Write || d.Conflict || d.Reason == override.ReasonOverride {
		if err = e.guard.Record(ctx, w); err != nil {
			return nil, false, fmt.Errorf("record provenance: %w", err)
		}
	}
	return JSONMap{"ci_id": item.ID, "field": field, "written": d.Write, "reason": d.Reason}, false, nil
}

// sourceWorkflow is the automation source of workflow writes (REC-03 rank 92).
const sourceWorkflow = "workflow"
