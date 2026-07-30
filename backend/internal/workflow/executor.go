package workflow

import (
	"context"
	"fmt"
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
	steps, _ := e.repo.ListSteps(orgID, runID)
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
		_, _ = e.repo.UpdateStep(orgID, waiting.ID, StatusCancelled, out, "approval rejected")
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(orgID, runID, StatusCancelled, &now, run.Context)
	}
	if decision != "approved" {
		return nil, fmt.Errorf("decision must be approved or rejected")
	}
	_, _ = e.repo.UpdateStep(orgID, waiting.ID, StatusSucceeded, out, "")
	return e.resume(ctx, orgID, def, run, waiting.ID)
}

func (e *Executor) resume(ctx context.Context, orgID string, def *Definition, run *Run, _ string) (*Run, error) {
	if !conditionsMet(def.Conditions, run.Context) {
		now := time.Now().UTC()
		return e.repo.UpdateRunStatus(orgID, run.ID, StatusCancelled, &now, run.Context)
	}
	_, _ = e.repo.UpdateRunStatus(orgID, run.ID, StatusRunning, nil, run.Context)
	steps, _ := e.repo.ListSteps(orgID, run.ID)
	start := len(steps)
	for i := start; i < len(def.Actions); i++ {
		action := def.Actions[i]
		typ, _ := action["type"].(string)
		if typ == "" {
			typ = "noop"
		}
		step := &Step{OrganizationID: orgID, RunID: run.ID, StepIndex: i, ActionType: typ, Status: StatusRunning, Input: action, Output: JSONMap{}}
		if err := e.repo.AppendStep(step); err != nil {
			return nil, err
		}
		out, wait, err := e.executeAction(ctx, orgID, run, action)
		if wait {
			_, _ = e.repo.UpdateStep(orgID, step.ID, StatusWaitingApproval, out, "")
			return e.repo.UpdateRunStatus(orgID, run.ID, StatusWaitingApproval, nil, run.Context)
		}
		if err != nil {
			_, _ = e.repo.UpdateStep(orgID, step.ID, StatusFailed, out, err.Error())
			now := time.Now().UTC()
			return e.repo.UpdateRunStatus(orgID, run.ID, StatusFailed, &now, run.Context)
		}
		_, _ = e.repo.UpdateStep(orgID, step.ID, StatusSucceeded, out, "")
	}
	now := time.Now().UTC()
	return e.repo.UpdateRunStatus(orgID, run.ID, StatusSucceeded, &now, run.Context)
}

func (e *Executor) executeAction(ctx context.Context, orgID string, run *Run, action JSONMap) (JSONMap, bool, error) {
	typ, _ := action["type"].(string)
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
			e.dispatcher.Dispatch(orgID, event, map[string]any{"run_id": run.ID, "context": run.Context})
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
		vals, _ := action["values"].(map[string]any)
		sub := &form.Submission{OrganizationID: orgID, FormID: formID, Values: form.JSONMap(vals), SubmittedBy: "workflow", Status: "submitted"}
		if err := e.forms.CreateSubmission(sub); err != nil {
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
