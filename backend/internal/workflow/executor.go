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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

type Executor struct {
	repo       Repository
	tickets    ticket.Repository
	cis        ci.Repository
	forms      form.Repository
	dispatcher *webhook.Dispatcher
}

func NewExecutor(repo Repository, tickets ticket.Repository, cis ci.Repository, forms form.Repository, dispatcher *webhook.Dispatcher) *Executor {
	return &Executor{repo: repo, tickets: tickets, cis: cis, forms: forms, dispatcher: dispatcher}
}

func (e *Executor) Trigger(ctx context.Context, orgID string, def *Definition, trigger string, payload JSONMap) (*Run, error) {
	if def == nil {
		return nil, fmt.Errorf("workflow definition is required")
	}
	if !triggerMatches(def.Trigger, trigger) {
		return nil, fmt.Errorf("workflow trigger does not match")
	}
	run := &Run{OrganizationID: orgID, WorkflowID: def.ID, Status: StatusPending, Trigger: trigger, Context: payload, StartedAt: time.Now().UTC()}
	if err := e.repo.CreateRun(run); err != nil {
		return nil, err
	}
	return e.resume(ctx, orgID, def, run, "")
}

func (e *Executor) Approve(ctx context.Context, orgID, runID, decision, comment string) (*Run, error) {
	run, err := e.repo.GetRun(orgID, runID)
	if err != nil {
		return nil, err
	}
	def, err := e.repo.GetDefinition(orgID, run.WorkflowID)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusWaitingApproval {
		return nil, fmt.Errorf("workflow run is not waiting for approval")
	}
	steps, err := e.repo.ListSteps(orgID, runID)
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
		if _, err := e.repo.UpdateStep(orgID, waiting.ID, StatusCancelled, out, "approval rejected"); err != nil {
			return nil, fmt.Errorf("cancel approval step: %w", err)
		}
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(orgID, runID, StatusCancelled, &now, run.Context)
	}
	if decision != "approved" {
		return nil, fmt.Errorf("decision must be approved or rejected")
	}
	if _, err := e.repo.UpdateStep(orgID, waiting.ID, StatusSucceeded, out, ""); err != nil {
		return nil, fmt.Errorf("complete approval step: %w", err)
	}
	return e.resume(ctx, orgID, def, run, waiting.ID)
}

func (e *Executor) resume(ctx context.Context, orgID string, def *Definition, run *Run, _ string) (*Run, error) {
	if !conditionsMet(def.Conditions, run.Context) {
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(orgID, run.ID, StatusCancelled, &now, run.Context)
	}
	if _, err := e.repo.UpdateRunStatus(orgID, run.ID, StatusRunning, nil, run.Context); err != nil {
		slog.Error("workflow: failed to mark run running", "run_id", run.ID, "error", err)
	}
	steps, err := e.repo.ListSteps(orgID, run.ID)
	if err != nil {
		e.failRun(orgID, run, fmt.Errorf("list workflow steps: %w", err))
		return nil, fmt.Errorf("list workflow steps: %w", err)
	}
	start := len(steps)
	for i := start; i < len(def.Actions); i++ {
		action := def.Actions[i]
		typ, err := actionType(action)
		if err != nil {
			e.failRun(orgID, run, err)
			return nil, err
		}
		step := &Step{OrganizationID: orgID, RunID: run.ID, StepIndex: i, ActionType: typ, Status: StatusRunning, Input: action, Output: JSONMap{}}
		if err := e.repo.AppendStep(step); err != nil {
			return nil, err
		}
		out, wait, err := e.executeAction(ctx, orgID, run, action)
		if wait {
			if _, err := e.repo.UpdateStep(orgID, step.ID, StatusWaitingApproval, out, ""); err != nil {
				slog.Error("workflow: failed to mark step waiting for approval", "step_id", step.ID, "error", err)
			}
			return e.repo.UpdateRunStatus(orgID, run.ID, StatusWaitingApproval, nil, run.Context)
		}
		if err != nil {
			if _, uerr := e.repo.UpdateStep(orgID, step.ID, StatusFailed, out, err.Error()); uerr != nil {
				slog.Error("workflow: failed to mark step failed", "step_id", step.ID, "error", uerr)
			}
			now := time.Now().UTC()
			return e.repo.UpdateRunStatus(orgID, run.ID, StatusFailed, &now, run.Context)
		}
		if _, err := e.repo.UpdateStep(orgID, step.ID, StatusSucceeded, out, ""); err != nil {
			e.failRun(orgID, run, fmt.Errorf("update step %s: %w", step.ID, err))
			return nil, fmt.Errorf("update workflow step: %w", err)
		}
	}
	now := time.Now().UTC()
	return e.repo.UpdateRunStatus(orgID, run.ID, StatusSucceeded, &now, run.Context)
}

// failRun best-effort marks the run failed; the original error is returned to
// the caller by resume, so persistence failures here are only logged.
func (e *Executor) failRun(orgID string, run *Run, cause error) {
	now := time.Now().UTC()
	if _, err := e.repo.UpdateRunStatus(orgID, run.ID, StatusFailed, &now, run.Context); err != nil {
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
		t := &ticket.Ticket{OrganizationID: orgID, Title: str(action, "title", "Workflow task"), Description: str(action, "description", ""), Status: "open", Priority: str(action, "priority", "medium"), Category: str(action, "category", "workflow"), ReporterID: str(action, "reporter_id", "workflow"), RelatedCIID: str(action, "ci_id", "")}
		if e.tickets == nil {
			return nil, false, fmt.Errorf("ticket repository unavailable")
		}
		if err := e.tickets.Create(t); err != nil {
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
		ciID := str(action, "ci_id", "")
		field := str(action, "field", "")
		value := action["value"]
		req := ci.UpdateRequest{}
		switch field {
		case "status":
			v := fmt.Sprint(value)
			req.Status = &v
		case "name":
			v := fmt.Sprint(value)
			req.Name = &v
		default:
			req.Attributes = map[string]any{field: value}
		}
		item, err := e.cis.Update(ctx, orgID, ciID, req)
		if err != nil {
			return nil, false, err
		}
		return JSONMap{"ci_id": item.ID, "field": field}, false, nil
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
