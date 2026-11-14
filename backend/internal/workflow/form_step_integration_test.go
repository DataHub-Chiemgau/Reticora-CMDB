package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Forms as trigger and step of workflows on PostgreSQL (WFL-01, WFL-02,
// WP-1102): a submission starts the runs of the trigger form_submitted in
// its transaction, request_form waits in waiting_input for the submission
// of its recipient, which continues the run with the values under form.
// A second submission, one to a foreign or not waiting run and one of
// another user are refused; the sweep and the cancel end a waiting run.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestFormSubmitted|TestRequestForm|TestCancelWaitingInput' ./internal/workflow/

type formEnv struct {
	*approvalEnv
	forms *form.PGRepository
	hook  *FormHook
	form  *form.Definition
}

var formPerms = []identity.Permission{identity.PermFormRead, identity.PermFormWrite, identity.PermWorkflowRead, identity.PermWorkflowWrite}

// newFormEnv is the approval env with a form (serial_number, readonly
// asset_tag when the prefill names one), the form routes with the hook of
// the executor and the workflow routes on one router.
func newFormEnv(t *testing.T) *formEnv {
	t.Helper()
	a := newApprovalEnv(t)
	e := &formEnv{approvalEnv: a, forms: form.NewPGRepository(a.f.App)}
	e.exec.forms = e.forms
	e.hook = NewFormHook(e.exec, permission.NewPGRepository(a.f.App))
	form.NewHandler(e.forms).WithSubmissionHook(e.hook).RegisterRoutes(e.api)
	e.form = &form.Definition{OrganizationID: a.f.OrgA, Name: fmt.Sprintf("wf-form-%d", time.Now().UnixNano()), Active: true,
		Schema: form.JSONMap{"type": "object", "properties": map[string]any{
			"serial_number": map[string]any{"type": "string"}, "asset_tag": map[string]any{"type": "string"}}},
		UIHints: form.JSONMap{"fields": map[string]any{"asset_tag": map[string]any{
			"readonly_when": map[string]any{"op": "not_empty", "field": "asset_tag"}}}}}
	if err := e.forms.CreateDefinition(e.as(e.initiator, formPerms), e.form); err != nil {
		t.Fatal(err)
	}
	return e
}

// define stores an active definition with trigger and actions.
func (e *formEnv) define(t *testing.T, trigger JSONMap, actions ...JSONMap) *Definition {
	t.Helper()
	if err := validateFormWrite(trigger, actions); err != nil {
		t.Fatalf("definition %v %v: %v", trigger, actions, err)
	}
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-form-def-%d", time.Now().UnixNano()), Trigger: trigger,
		Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.as(e.initiator, formPerms), def); err != nil {
		t.Fatal(err)
	}
	return def
}

// request starts a run whose first step requests the form from recipient,
// followed by a notification that names form.serial_number.
func (e *formEnv) request(t *testing.T, recipient map[string]any, extra JSONMap) *Run {
	t.Helper()
	action := JSONMap{"type": requestFormActionType, "form_id": e.form.ID, "recipient": recipient}
	for k, v := range extra {
		action[k] = v
	}
	def := e.define(t, JSONMap{"type": "manual"}, action,
		JSONMap{"type": "send_notification", "recipients": map[string]any{"users": []any{e.a}},
			"subject": "Seriennummer {{form.serial_number}}", "body": "Eingabe {{form.serial_number}}"})
	run, err := e.exec.Trigger(e.as(e.initiator, formPerms), e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusWaitingInput {
		t.Fatalf("run %s steps %+v, want waiting_input", run.Status, run.Steps)
	}
	return run
}

// submit posts values of the form as user, with runID if set.
func (e *formEnv) submit(t *testing.T, user, runID string, values map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"values": values}
	if runID != "" {
		body["run_id"] = runID
	}
	raw, _ := json.Marshal(body)
	rec := e.call(t, e.as(user, formPerms), http.MethodPost, "/api/v1/forms/"+e.form.ID+"/submissions", string(raw))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *formEnv) runOf(t *testing.T, id string) *Run {
	t.Helper()
	run, err := e.repo.GetRun(e.as(e.initiator, formPerms), e.f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestFormSubmittedStartsRun(t *testing.T) {
	e := newFormEnv(t)
	def := e.define(t, JSONMap{"type": TriggerFormSubmitted, "form_id": e.form.ID,
		"filter": map[string]any{"op": "eq", "field": "form.serial_number", "value": "SN-1"}}, JSONMap{"type": "noop"})
	code, sub := e.submit(t, e.a, "", map[string]any{"serial_number": "SN-1"})
	if code != http.StatusCreated {
		t.Fatalf("submission: %d %v", code, sub)
	}
	runs, _, err := e.repo.ListRuns(e.as(e.initiator, formPerms), e.f.OrgA, &RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 50})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs of the definition: %d, %v; want 1", len(runs), err)
	}
	run := runs[0]
	if run.Trigger != TriggerFormSubmitted || run.InitiatedBy != e.a || run.Status != StatusSucceeded {
		t.Errorf("run trigger %s, initiated_by %s, status %s", run.Trigger, run.InitiatedBy, run.Status)
	}
	values, _ := run.Context["form"].(map[string]any)
	if values["serial_number"] != "SN-1" || run.Context["submission_id"] != sub["id"] || run.Context["form_id"] != e.form.ID ||
		run.Context["form_version_id"] != sub["form_version_id"] {
		t.Errorf("run context %v, submission %v", run.Context, sub)
	}
	// The filter holds only for SN-1.
	if code, _ = e.submit(t, e.a, "", map[string]any{"serial_number": "SN-2"}); code != http.StatusCreated {
		t.Fatalf("second submission: %d", code)
	}
	// A rolled-back submission leaves no run.
	ctx := e.as(e.a, formPerms)
	rollback := errors.New("rollback")
	err = e.forms.RunInTx(ctx, e.f.OrgA, func(ctx context.Context) error {
		s := &form.Submission{OrganizationID: e.f.OrgA, FormID: e.form.ID, Values: form.JSONMap{"serial_number": "SN-1"}, SubmittedBy: e.a,
			Status: "submitted"}
		if cerr := e.forms.CreateSubmission(ctx, s); cerr != nil {
			return cerr
		}
		if _, herr := e.hook.Submitted(ctx, s); herr != nil {
			return herr
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rolled-back submission: %v", err)
	}
	if runs, _, _ = e.repo.ListRuns(e.as(e.initiator, formPerms), e.f.OrgA, &RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 50}); len(runs) != 1 {
		t.Errorf("runs after the second and the rolled-back submission: %d, want 1", len(runs))
	}
}

func TestRequestFormWaitsAndResumes(t *testing.T) {
	e := newFormEnv(t)
	run := e.request(t, map[string]any{"type": "user", "id": e.a}, JSONMap{"prefill": map[string]any{"asset_tag": "INV-7"}})
	if code, body := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-9", "asset_tag": "INV-8"}); code != http.StatusBadRequest {
		t.Fatalf("submission that changes the readonly prefill: %d %v, want 400", code, body)
	}
	code, sub := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-9", "asset_tag": "INV-7"})
	if code != http.StatusCreated || sub["run_id"] != run.ID {
		t.Fatalf("submission: %d %v", code, sub)
	}
	got := e.runOf(t, run.ID)
	if got.Status != StatusSucceeded || len(got.Steps) != 2 || got.Steps[0].Output["submission_id"] != sub["id"] {
		t.Fatalf("run %s steps %+v", got.Status, got.Steps)
	}
	if values, _ := got.Context["form"].(map[string]any); values["serial_number"] != "SN-9" {
		t.Errorf("run context %v, want form.serial_number", got.Context)
	}
	var subject string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT subject FROM notification_delivery
		WHERE organization_id = $1 AND dedup_key = $2 LIMIT 1`, e.f.OrgA, "workflow:"+run.ID+":1").Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subject, "SN-9") {
		t.Errorf("the next step reads %q, want form.serial_number", subject)
	}
	// The recipient was notified with the link to the form and the run.
	if n := e.noticeRecipients(t, "workflow:"+run.ID+":0:form"); !n[e.a] {
		t.Errorf("form request notified %v, want the recipient", n)
	}
}

func TestRequestFormSecondSubmissionConflict(t *testing.T) {
	e := newFormEnv(t)
	run := e.request(t, map[string]any{"type": "user", "id": e.a}, nil)
	if code, _ := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusCreated {
		t.Fatalf("first submission: %d", code)
	}
	code, body := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-2"})
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(body["detail"]), "run_not_waiting") {
		t.Fatalf("second submission: %d %v, want 409 run_not_waiting", code, body)
	}
}

// TestRequestFormTwoStepsInOneRun: a run with two request_form steps takes
// one submission per step; the second step's submission continues the run,
// a third one is refused (409).
func TestRequestFormTwoStepsInOneRun(t *testing.T) {
	e := newFormEnv(t)
	ask := JSONMap{"type": requestFormActionType, "form_id": e.form.ID, "recipient": map[string]any{"type": "user", "id": e.a}}
	def := e.define(t, JSONMap{"type": "manual"}, ask, ask)
	run, err := e.exec.Trigger(e.as(e.initiator, formPerms), e.f.OrgA, def, "manual", JSONMap{})
	if err != nil || run.Status != StatusWaitingInput {
		t.Fatalf("run %+v, %v; want waiting_input", run, err)
	}
	if code, body := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusCreated {
		t.Fatalf("first step: %d %v", code, body)
	}
	if got := e.runOf(t, run.ID); got.Status != StatusWaitingInput {
		t.Fatalf("run after the first step: %s, want waiting_input for the second", got.Status)
	}
	code, sub := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-2"})
	if code != http.StatusCreated {
		t.Fatalf("second step: %d %v, want 201", code, sub)
	}
	got := e.runOf(t, run.ID)
	if got.Status != StatusSucceeded || len(got.Steps) != 2 || got.Steps[1].Output["submission_id"] != sub["id"] {
		t.Fatalf("run %s steps %+v", got.Status, got.Steps)
	}
	if code, _ := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-3"}); code != http.StatusConflict {
		t.Errorf("third submission: %d, want 409", code)
	}
}

func TestRequestFormWrongRunConflict(t *testing.T) {
	e := newFormEnv(t)
	done := e.define(t, JSONMap{"type": "manual"}, JSONMap{"type": "noop"})
	ended, err := e.exec.Trigger(e.as(e.initiator, formPerms), e.f.OrgA, done, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	// A run of another organization that waits for input.
	foreignDef, foreignVersion, foreignRun := e.f.ID(), e.f.ID(), e.f.ID()
	if _, err := e.f.Admin.Exec(context.Background(), `WITH d AS (INSERT INTO workflow_def (id, organization_id, name, current_version_id)
		VALUES ($1, $2, 'wf-foreign', $3))
		INSERT INTO workflow_def_version (id, organization_id, workflow_id, version) VALUES ($3, $2, $1, 1)`, foreignDef, e.f.OrgB, foreignVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO workflow_run (id, organization_id, workflow_id, definition_version_id, status, trigger)
		VALUES ($1, $2, $3, $4, 'waiting_input', 'manual')`, foreignRun, e.f.OrgB, foreignDef, foreignVersion); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"ended": ended.ID, "foreign": foreignRun, "unknown": e.f.ID()} {
		if code, body := e.submit(t, e.a, id, map[string]any{"serial_number": "SN-1"}); code != http.StatusConflict {
			t.Errorf("%s run: %d %v, want 409", name, code, body)
		}
	}
}

func TestRequestFormOnlyRecipient(t *testing.T) {
	e := newFormEnv(t)
	run := e.request(t, map[string]any{"type": "user", "id": e.a}, nil)
	if code, body := e.submit(t, e.b, run.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusForbidden {
		t.Fatalf("another user: %d %v, want 403", code, body)
	}
	if got := e.runOf(t, run.ID); got.Status != StatusWaitingInput {
		t.Errorf("run after the refused submission: %s", got.Status)
	}
	team := e.request(t, map[string]any{"type": "team", "id": e.team}, nil)
	if code, _ := e.submit(t, e.outsider, team.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusForbidden {
		t.Errorf("non-member of the team: %d, want 403", code)
	}
	if code, body := e.submit(t, e.b, team.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusCreated {
		t.Errorf("member of the team: %d %v, want 201", code, body)
	}
}

func TestRequestFormDeadlineCancels(t *testing.T) {
	e := newFormEnv(t)
	run := e.request(t, map[string]any{"type": "user", "id": e.a}, JSONMap{"deadline": "1h"})
	if res := e.sweepAt(t, time.Now().Add(30*time.Minute)); res.InputsExpired != 0 || e.runStatus(t, run.ID) != StatusWaitingInput {
		t.Fatalf("sweep before the deadline: %+v, run %s", res, e.runStatus(t, run.ID))
	}
	if res := e.sweepAt(t, time.Now().Add(2*time.Hour)); res.InputsExpired != 1 {
		t.Fatalf("sweep after the deadline: %+v", res)
	}
	status, errText, _, _, _ := e.approvalStep(t, run.ID)
	if e.runStatus(t, run.ID) != StatusCancelled || status != StatusCancelled || errText != formDeadlineExpiredText {
		t.Errorf("run %s, step %s %q", e.runStatus(t, run.ID), status, errText)
	}
	// continue completes the step without values and runs on.
	cont := e.request(t, map[string]any{"type": "user", "id": e.a}, JSONMap{"deadline": "1h", "on_timeout": "continue"})
	e.sweepAt(t, time.Now().Add(2*time.Hour))
	if got := e.runOf(t, cont.ID); got.Status != StatusSucceeded || got.Steps[0].Output["timed_out"] != true {
		t.Errorf("continued run %s steps %+v", got.Status, got.Steps)
	}
}

func TestCancelWaitingInputRun(t *testing.T) {
	e := newFormEnv(t)
	run := e.request(t, map[string]any{"type": "user", "id": e.a}, nil)
	rec := e.call(t, e.as(e.initiator, formPerms), http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	status, _, _, _, _ := e.approvalStep(t, run.ID)
	if e.runStatus(t, run.ID) != StatusCancelled || status != StatusCancelled {
		t.Errorf("run %s, step %s after the cancel", e.runStatus(t, run.ID), status)
	}
	if code, body := e.submit(t, e.a, run.ID, map[string]any{"serial_number": "SN-1"}); code != http.StatusConflict {
		t.Errorf("submission to the cancelled run: %d %v, want 409", code, body)
	}
}

func TestRequestFormActionValidation(t *testing.T) {
	for _, c := range []struct {
		action JSONMap
		field  string
	}{
		{JSONMap{"type": requestFormActionType}, "actions[0].form_id"},
		{JSONMap{"type": requestFormActionType, "form_id": "8b0f2a9c-1d3e-4f5a-9b6c-7d8e9f0a1b2c"}, "actions[0].recipient"},
		{JSONMap{"type": requestFormActionType, "form_id": "8b0f2a9c-1d3e-4f5a-9b6c-7d8e9f0a1b2c",
			"recipient": map[string]any{"type": "role", "id": "8b0f2a9c-1d3e-4f5a-9b6c-7d8e9f0a1b2c"}}, "actions[0].recipient.type"},
		{JSONMap{"type": requestFormActionType, "form_id": "8b0f2a9c-1d3e-4f5a-9b6c-7d8e9f0a1b2c",
			"recipient": map[string]any{"type": "user", "id": "8b0f2a9c-1d3e-4f5a-9b6c-7d8e9f0a1b2c"}, "on_timeout": "reject"}, "actions[0].on_timeout"},
	} {
		err := validateFormWrite(JSONMap{"type": "manual"}, []JSONMap{c.action})
		if err == nil || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%v: %v, want a 422 on %s", c.action, err, c.field)
		}
	}
	if err := validateFormWrite(JSONMap{"type": TriggerFormSubmitted}, nil); err == nil || !strings.Contains(err.Error(), "trigger.form_id") {
		t.Errorf("form_submitted without form_id: %v", err)
	}
}
