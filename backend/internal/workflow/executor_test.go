package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

func TestExecutorApprovalFlow(t *testing.T) {
	repo := NewMemoryRepository()
	tickets := ticket.NewMemoryRepository()
	forms := form.NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	exec := NewExecutor(repo, tickets, cis, forms, nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "require_approval"}, {"type": "create_ticket", "title": "Do it"}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusWaitingApproval {
		t.Fatalf("want waiting, got %s", run.Status)
	}
	run, err = exec.Approve(context.Background(), "org-1", run.ID, "approved", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusSucceeded {
		t.Fatalf("want succeeded, got %s", run.Status)
	}
	list, total, err := tickets.List(context.Background(), "org-1", &ticket.FilterParams{}, api.PaginationParams{Limit: 10})
	_ = list
	_ = total
	_ = err
}

// failingWorkflowRepository wraps MemoryRepository and lets tests inject
// failures into specific repository methods.
type failingWorkflowRepository struct {
	*MemoryRepository
	listStepsErr   error
	updateStepErr  error
	updateRunErr   error
	decideErr      error
	failOnRunCalls map[string]bool
}

func (f *failingWorkflowRepository) decideVote(ctx context.Context, orgID, id string, v *approvalVote) (*ApprovalResult, error) {
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	return f.MemoryRepository.decideVote(ctx, orgID, id, v)
}

func (f *failingWorkflowRepository) ListSteps(_ context.Context, orgID, runID string) ([]Step, error) {
	if f.listStepsErr != nil {
		return nil, f.listStepsErr
	}
	return f.MemoryRepository.ListSteps(context.Background(), orgID, runID)
}

func (f *failingWorkflowRepository) UpdateStep(_ context.Context, orgID, id, status string, output JSONMap, errText string) (*Step, error) {
	if f.updateStepErr != nil {
		return nil, f.updateStepErr
	}
	return f.MemoryRepository.UpdateStep(context.Background(), orgID, id, status, output, errText)
}

func (f *failingWorkflowRepository) UpdateRunStatus(_ context.Context, orgID, id, status string, finished *time.Time, runCtx JSONMap) (*Run, error) {
	if f.updateRunErr != nil && f.failOnRunCalls[status] {
		return nil, f.updateRunErr
	}
	return f.MemoryRepository.UpdateRunStatus(context.Background(), orgID, id, status, finished, runCtx)
}

func TestExecutorListStepsFailureFailsRun(t *testing.T) {
	repo := &failingWorkflowRepository{MemoryRepository: NewMemoryRepository(), listStepsErr: errors.New("db down")}
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	_, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{})
	if err == nil {
		t.Fatal("expected error when ListSteps fails")
	}
	runs, _, lerr := repo.ListRuns(context.Background(), "org-1", &RunFilter{}, api.PaginationParams{Limit: 10})
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(runs) != 1 || runs[0].Status != StatusFailed {
		t.Fatalf("expected run to be marked failed, got %+v", runs)
	}
}

func TestExecutorUpdateStepFailurePropagates(t *testing.T) {
	repo := &failingWorkflowRepository{MemoryRepository: NewMemoryRepository(), updateStepErr: errors.New("write denied")}
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	_, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{})
	if err == nil {
		t.Fatal("expected error when UpdateStep fails")
	}
	runs, _, _ := repo.ListRuns(context.Background(), "org-1", &RunFilter{}, api.PaginationParams{Limit: 10})
	if len(runs) != 1 || runs[0].Status != StatusFailed {
		t.Fatalf("expected run failed after UpdateStep failure, got %+v", runs)
	}
}

func TestExecutorApproveListStepsFailure(t *testing.T) {
	repo := &failingWorkflowRepository{MemoryRepository: NewMemoryRepository()}
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "require_approval"}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusWaitingApproval {
		t.Fatalf("want waiting, got %s", run.Status)
	}
	// The decision and the change of the run are one write
	// (decideVote, WP-1092, WP-1098): when it fails, the run still waits.
	repo.decideErr = errors.New("write denied")
	if _, approveErr := exec.Approve(context.Background(), "org-1", run.ID, "approved", ""); approveErr == nil {
		t.Fatal("expected error when the decision cannot be stored")
	}
	if got, _ := repo.GetRun(context.Background(), "org-1", run.ID); got == nil || got.Status != StatusWaitingApproval {
		t.Fatalf("run after the failed decision: %+v, want waiting", got)
	}
	repo.decideErr = nil
	run, err = exec.Approve(context.Background(), "org-1", run.ID, "rejected", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCancelled {
		t.Fatalf("want cancelled, got %s", run.Status)
	}
}

func TestExecutorInvalidActionType(t *testing.T) {
	repo := NewMemoryRepository()
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": 42}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{}); err == nil {
		t.Fatal("expected error for non-string action type")
	}
	runs, _, _ := repo.ListRuns(context.Background(), "org-1", &RunFilter{}, api.PaginationParams{Limit: 10})
	if len(runs) != 1 || runs[0].Status != StatusFailed {
		t.Fatalf("expected run failed for invalid action type, got %+v", runs)
	}
}

func TestExecutorSubmitFormInvalidValues(t *testing.T) {
	repo := NewMemoryRepository()
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "submit_form", "form_id": "f1", "values": "not-an-object"}}, Active: true}
	if err := repo.CreateDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(context.Background(), "org-1", def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("expected run failed for non-object form values, got %s", run.Status)
	}
	steps, err := repo.ListSteps(context.Background(), "org-1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != StatusFailed || steps[0].Error == "" {
		t.Fatalf("expected failed step with error message, got %+v", steps)
	}
}

// TestExecutorStartThenExecute covers WP-603 (WFL-04): Start stores the
// pending run of a matching trigger and executes no action, Execute runs
// it; TriggerMatches applies the rule of Start.
func TestExecutorStartThenExecute(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	tickets := ticket.NewMemoryRepository()
	exec := NewExecutor(repo, tickets, ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "event", "event": "iga.access_request"},
		Actions: []JSONMap{{"type": "create_ticket", "title": "Grant", "reporter_id": "00000000-0000-4000-8000-0000000000aa"}}, Active: true}
	if err := repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	if !TriggerMatches(def, "iga.access_request") || TriggerMatches(def, "manual") || TriggerMatches(nil, "iga.access_request") ||
		TriggerMatches(&Definition{Trigger: JSONMap{"type": "manual", "event": "iga.access_request"}}, "iga.access_request") {
		t.Error("TriggerMatches differs from the rule of Start")
	}
	if _, err := exec.Start(ctx, "org-1", def, "manual", JSONMap{}); !errors.Is(err, ErrTriggerMismatch) {
		t.Fatalf("start with another trigger: %v, want ErrTriggerMismatch", err)
	}
	run, err := exec.Start(ctx, "org-1", def, "iga.access_request", JSONMap{"subject_user_id": "u-2"})
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := repo.ListSteps(ctx, "org-1", run.ID)
	_, created, _ := tickets.List(ctx, "org-1", &ticket.FilterParams{}, api.PaginationParams{Limit: 10})
	if run.ID == "" || run.Status != StatusPending || run.Trigger != "iga.access_request" || len(steps) != 0 || created != 0 {
		t.Fatalf("started run %+v with %d steps and %d tickets, want a stored pending run without action", run, len(steps), created)
	}
	if run, err = exec.Execute(ctx, "org-1", def, run); err != nil {
		t.Fatal(err)
	}
	steps, _ = repo.ListSteps(ctx, "org-1", run.ID)
	_, created, _ = tickets.List(ctx, "org-1", &ticket.FilterParams{}, api.PaginationParams{Limit: 10})
	if run.Status != StatusSucceeded || len(steps) != 1 || created != 1 || run.Context["subject_user_id"] != "u-2" {
		t.Fatalf("executed run %+v with %d steps and %d tickets, want succeeded with one ticket", run, len(steps), created)
	}
}

// TestExecutorSubmitFormRules covers WP-603 (WFL-02) with the memory
// repositories: submit_form submits as the user who triggered the run,
// empty for a trigger without a user and never "workflow", and refuses an
// inactive form, values against the schema and an unknown form like POST
// /forms/{id}/submissions, with the fixed text of the rule and the fields of
// a schema violation in the step output. TestSubmitFormActionOnPostgres
// shows it on PostgreSQL.
func TestExecutorSubmitFormRules(t *testing.T) {
	ctx := context.Background()
	const user = "00000000-0000-4000-8000-0000000000aa"
	repo := NewMemoryRepository()
	forms := form.NewMemoryRepository()
	exec := NewExecutor(repo, nil, nil, forms, nil)
	schema := form.JSONMap{"type": "object", "required": []any{"name"}}
	active := &form.Definition{OrganizationID: "org-1", Name: "active", Schema: schema, Active: true}
	inactive := &form.Definition{OrganizationID: "org-1", Name: "inactive", Schema: schema}
	for _, d := range []*form.Definition{active, inactive} {
		if err := forms.CreateDefinition(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	run := func(ctx context.Context, action JSONMap) (*Run, Step) {
		t.Helper()
		def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{action}, Active: true}
		if err := repo.CreateDefinition(ctx, def); err != nil {
			t.Fatal(err)
		}
		r, err := exec.Trigger(ctx, "org-1", def, "manual", JSONMap{})
		if err != nil {
			t.Fatal(err)
		}
		steps, err := repo.ListSteps(ctx, "org-1", r.ID)
		if err != nil || len(steps) != 1 {
			t.Fatalf("steps of run %s: %+v, %v", r.ID, steps, err)
		}
		return r, steps[0]
	}

	userCtx := tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: "org-1", UserID: user})
	for _, c := range []struct {
		name   string
		ctx    context.Context
		wantBy string
	}{{"triggered by a user", userCtx, user}, {"triggered without a user", ctx, ""}} {
		r, step := run(c.ctx, JSONMap{"type": "submit_form", "form_id": active.ID, "values": map[string]any{"name": "x"}})
		id, _ := step.Output["submission_id"].(string)
		sub, err := forms.GetSubmission(ctx, "org-1", id)
		if r.Status != StatusSucceeded || err != nil || sub.SubmittedBy != c.wantBy {
			t.Errorf("%s: run %s %q, submission %+v %v; want succeeded, submitted_by %q", c.name, r.Status, step.Error, sub, err, c.wantBy)
		}
	}

	for _, c := range []struct {
		name   string
		action JSONMap
		want   string
		fields int
	}{
		{"inactive form", JSONMap{"type": "submit_form", "form_id": inactive.ID, "values": map[string]any{"name": "x"}}, "form definition is inactive", 0},
		{"required value missing", JSONMap{"type": "submit_form", "form_id": active.ID, "values": map[string]any{}}, form.ValidationError{}.Error(), 1},
		{"unknown form", JSONMap{"type": "submit_form", "form_id": "00000000-0000-4000-8000-0000000000ff"}, form.ErrNotFound.Error(), 0},
		{"form id no UUID", JSONMap{"type": "submit_form", "form_id": "f1"}, form.ErrNotFound.Error(), 0},
	} {
		r, step := run(userCtx, c.action)
		fields, _ := step.Output["fields"].([]form.FieldError)
		if r.Status != StatusFailed || step.Status != StatusFailed || step.Error != c.want || len(fields) != c.fields {
			t.Errorf("%s: run %s step %s %q fields %v, want failed %q with %d fields", c.name, r.Status, step.Status, step.Error, fields, c.want, c.fields)
		}
	}
	if _, total, _ := forms.ListSubmissions(ctx, "org-1", form.SubmissionFilter{}, api.PaginationParams{Limit: 10}); total != 2 {
		t.Errorf("submissions: %d, want the 2 accepted ones", total)
	}
}

// failingOverrides makes every read of the override state fail.
type failingOverrides struct{ *override.MemoryRepository }

func (failingOverrides) Get(context.Context, string, string, string) (*override.FieldValue, error) {
	return nil, errors.New("database down")
}

// TestSetCIFieldRespectsOverrides covers WP-059 (OVR-02, WFL-02): the
// workflow action writes as source workflow through the central decision,
// keeps a manual override, records the provenance of what it writes and
// fails when the override state cannot be read.
func TestSetCIFieldRespectsOverrides(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv", DiscoverySource: ci.SourceSNMP,
		Attributes: map[string]any{"owner": "ops", "rack": "R1"}}
	if err := cis.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	overrides := override.NewMemoryRepository()
	if _, err := overrides.SetOverride(ctx, "org-1", item.ID, "owner", "ops", "", "assigned by hand", true); err != nil {
		t.Fatal(err)
	}
	run := func(guard *override.Guard, field string, value any) (*Run, []Step) {
		t.Helper()
		exec := NewExecutor(repo, ticket.NewMemoryRepository(), cis, form.NewMemoryRepository(), nil).WithFieldGuard(guard)
		def := &Definition{OrganizationID: "org-1", Name: "set", Trigger: JSONMap{"type": "manual"}, Active: true,
			Actions: []JSONMap{{"type": "set_ci_field", "ci_id": item.ID, "field": field, "value": value}}}
		if err := repo.CreateDefinition(ctx, def); err != nil {
			t.Fatal(err)
		}
		r, err := exec.Trigger(ctx, "org-1", def, "manual", JSONMap{})
		if err != nil {
			t.Fatal(err)
		}
		steps, err := repo.ListSteps(ctx, "org-1", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r, steps
	}
	guard := override.NewGuard(overrides)

	r, steps := run(guard, "owner", "workflow-team")
	if r.Status != StatusSucceeded || steps[0].Output["written"] != false || steps[0].Output["reason"] != override.ReasonOverrideConflict {
		t.Errorf("set an overridden field: run %s output %v", r.Status, steps[0].Output)
	}
	r, steps = run(guard, "rack", "R2")
	if r.Status != StatusSucceeded || steps[0].Output["written"] != true {
		t.Errorf("set a field without override: run %s output %v", r.Status, steps[0].Output)
	}
	got, err := cis.GetByID(ctx, "org-1", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["owner"] != "ops" || got.Attributes["rack"] != "R2" {
		t.Errorf("attributes after the workflow: %v", got.Attributes)
	}
	if fv, err := overrides.Get(ctx, "org-1", item.ID, "rack"); err != nil || fv.DiscoveredSource != "workflow" || fv.DiscoveredValue != "R2" {
		t.Errorf("provenance of the written field: %+v, %v", fv, err)
	}

	// Unreadable override state: nothing written, the run fails.
	r, _ = run(override.NewGuard(failingOverrides{overrides}), "rack", "R3")
	if r.Status != StatusFailed {
		t.Errorf("run with unreadable overrides: %s, want failed", r.Status)
	}
	if got, _ = cis.GetByID(ctx, "org-1", item.ID); got.Attributes["rack"] != "R2" {
		t.Errorf("rack after a refused write: %v", got.Attributes["rack"])
	}
}

// instanceOverrides marks the named fields as instance attributes of every
// CI, as override.PGRepository.Get does from ci_instance_field_definition.
type instanceOverrides struct {
	*override.MemoryRepository
	instance map[string]bool
}

func (o instanceOverrides) Get(ctx context.Context, orgID, ciID, field string) (*override.FieldValue, error) {
	fv, err := o.MemoryRepository.Get(ctx, orgID, ciID, field)
	if !o.instance[field] {
		return fv, err
	}
	if errors.Is(err, override.ErrNotFound) {
		return &override.FieldValue{OrganizationID: orgID, CIID: ciID, FieldName: field, InstanceAttribute: true}, nil
	}
	if err != nil {
		return nil, err
	}
	fv.InstanceAttribute = true
	return fv, nil
}

// TestSetCIFieldRefusesInstanceAttributes covers WP-714 (MET-14, OVR-02):
// set_ci_field on an instance attribute of the CI, with a value or with
// null, writes nothing and reports written false with the reason
// instance_attribute; the value stays and no provenance is recorded.
func TestSetCIFieldRefusesInstanceAttributes(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv", DiscoverySource: ci.SourceSNMP,
		Attributes: map[string]any{"contract": "C-1", "rack": "R1"}}
	if err := cis.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	overrides := override.NewMemoryRepository()
	guard := override.NewGuard(instanceOverrides{overrides, map[string]bool{"contract": true}})
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), cis, form.NewMemoryRepository(), nil).WithFieldGuard(guard)
	for _, value := range []any{"C-2", nil} {
		def := &Definition{OrganizationID: "org-1", Name: "set", Trigger: JSONMap{"type": "manual"}, Active: true,
			Actions: []JSONMap{{"type": "set_ci_field", "ci_id": item.ID, "field": "contract", "value": value}}}
		if err := repo.CreateDefinition(ctx, def); err != nil {
			t.Fatal(err)
		}
		r, err := exec.Trigger(ctx, "org-1", def, "manual", JSONMap{})
		if err != nil {
			t.Fatal(err)
		}
		steps, err := repo.ListSteps(ctx, "org-1", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusSucceeded || steps[0].Output["written"] != false || steps[0].Output["reason"] != override.ReasonInstanceAttribute {
			t.Errorf("set_ci_field contract=%v: run %s output %v, want written false reason instance_attribute", value, r.Status, steps[0].Output)
		}
		got, err := cis.GetByID(ctx, "org-1", item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := got.Attributes["contract"]; !ok || v != "C-1" {
			t.Errorf("contract after set_ci_field %v: %v (present %v), want C-1", value, v, ok)
		}
		if _, err := overrides.Get(ctx, "org-1", item.ID, "contract"); !errors.Is(err, override.ErrNotFound) {
			t.Errorf("provenance of the refused instance attribute: %v, want none", err)
		}
	}
}

// TestExecutorConditionFalseCancelsRun: a MET-45 condition of the version
// that does not hold for the run context cancels the run before any step
// (WP-1087); one that holds runs the actions.
func TestExecutorConditionFalseCancelsRun(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	exec := NewExecutor(repo, ticket.NewMemoryRepository(), ci.NewMemoryRepository(), form.NewMemoryRepository(), nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"},
		Conditions: []JSONMap{{"op": "any", "rules": []any{
			map[string]any{"op": "eq", "field": "data.to_state", "value": "retired"},
			map[string]any{"op": "gte", "field": "ci.attributes.rack_units", "value": 4},
		}}},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(ctx, "org-1", def, "manual", JSONMap{"data": map[string]any{"to_state": "active"}, "ci": map[string]any{"attributes": map[string]any{"rack_units": 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCancelled {
		t.Fatalf("run with a false condition: %s, want cancelled", run.Status)
	}
	if steps, _ := repo.ListSteps(ctx, "org-1", run.ID); len(steps) != 0 {
		t.Fatalf("cancelled run has steps: %+v", steps)
	}
	run, err = exec.Trigger(ctx, "org-1", def, "manual", JSONMap{"ci": map[string]any{"attributes": map[string]any{"rack_units": "4"}}})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("run with a true condition: %+v %v, want succeeded", run, err)
	}
}

// TestCreateDefinitionInvalidCondition422: POST and PATCH refuse a
// condition that is no MET-45 node with 422 and the field of the path.
func TestCreateDefinitionInvalidCondition422(t *testing.T) {
	repo := NewMemoryRepository()
	router := chi.NewRouter()
	NewHandler(repo, NewExecutor(repo, nil, nil, nil, nil)).RegisterRoutes(router)
	tctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-1"})
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tctx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct {
		name       string
		conditions []any
		field      string
	}{
		{"unknown operator", []any{map[string]any{"op": "matches", "field": "name", "value": "^sw"}}, "conditions[0].op"},
		{"value rule pattern", []any{map[string]any{"op": "eq", "field": "a", "value": 1}, map[string]any{"op": "pattern", "pattern": "^a"}}, "conditions[1].op"},
		{"missing field", []any{map[string]any{"op": "all", "rules": []any{map[string]any{"op": "eq", "value": "x"}}}}, "conditions[0].rules[0].field"},
	} {
		rec := do(http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf", "trigger": map[string]any{"type": "manual"}, "conditions": c.conditions, "actions": []any{map[string]any{"type": "noop"}}})
		var p api.FieldProblem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusUnprocessableEntity || p.Field != c.field {
			t.Errorf("%s: %d %s, want 422 on %s", c.name, rec.Code, rec.Body.String(), c.field)
		}
	}
	rec := do(http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf", "trigger": map[string]any{"type": "event", "event": "ci.created", "filter": map[string]any{"op": "exists", "field": "x"}}, "actions": []any{map[string]any{"type": "noop"}}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"trigger.filter.op"`) {
		t.Errorf("trigger filter: %d %s, want 422 on trigger.filter.op", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf", "trigger": map[string]any{"type": "manual"}, "conditions": []any{map[string]any{"op": "eq", "field": "ci.status", "value": "active"}}, "actions": []any{map[string]any{"type": "noop"}}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid definition: %d %s", rec.Code, rec.Body.String())
	}
	var d Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	rec = do(http.MethodPatch, "/api/v1/workflows/"+d.ID, map[string]any{"conditions": []any{map[string]any{"op": "gte", "field": "n", "value": "x"}}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"conditions[0].value"`) {
		t.Errorf("PATCH invalid condition: %d %s, want 422 on conditions[0].value", rec.Code, rec.Body.String())
	}
	if got, _ := repo.GetDefinition(tctx, "org-1", d.ID); got == nil || got.Version != 1 {
		t.Errorf("refused PATCH changed the definition: %+v", got)
	}
	// A definition migration 000330 deactivated keeps its G1 condition: a
	// PATCH checks the conditions in effect, so it is neither switched on
	// again nor carried into a new version; a rename stays possible.
	legacy := &Definition{OrganizationID: "org-1", Name: "legacy", Trigger: JSONMap{"type": "manual"},
		Conditions: []JSONMap{{"field": "name", "op": "matches", "value": "^sw"}}, Actions: []JSONMap{{"type": "noop"}}}
	if err := repo.CreateDefinition(tctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]any{
		{"active": true},
		{"actions": []any{map[string]any{"type": "noop", "n": 2}}},
	} {
		rec = do(http.MethodPatch, "/api/v1/workflows/"+legacy.ID, patch)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"conditions[0].op"`) {
			t.Errorf("PATCH %v of a deactivated legacy definition: %d %s, want 422 on conditions[0].op", patch, rec.Code, rec.Body.String())
		}
	}
	if got, _ := repo.GetDefinition(tctx, "org-1", legacy.ID); got == nil || got.Active || got.Version != 1 {
		t.Errorf("refused PATCH changed the legacy definition: %+v", got)
	}
	if rec = do(http.MethodPatch, "/api/v1/workflows/"+legacy.ID, map[string]any{"name": "legacy-renamed"}); rec.Code != http.StatusOK {
		t.Errorf("rename of a deactivated legacy definition: %d %s", rec.Code, rec.Body.String())
	}
}

// submitFormDocuments is the document reader of TestExecutorSubmitFormConditions.
type submitFormDocuments map[string]*document.Document

func (d submitFormDocuments) GetByID(_ context.Context, orgID, id string) (*document.Document, error) {
	if doc := d[id]; doc != nil && doc.OrganizationID == orgID {
		return doc, nil
	}
	return nil, api.ErrNotFound
}

// TestExecutorSubmitFormConditions: submit_form applies the conditional
// logic of the form like POST /forms/{id}/submissions (WP-1101): a hidden
// field is dropped before the submission is stored, and a signature naming
// a document of another organization fails the step with 422 on the field.
func TestExecutorSubmitFormConditions(t *testing.T) {
	const (
		own     = "11111111-1111-4111-8111-111111111111"
		foreign = "33333333-3333-4333-8333-333333333333"
	)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "00000000-0000-4000-8000-0000000000aa"})
	repo := NewMemoryRepository()
	forms := form.NewMemoryRepository()
	exec := NewExecutor(repo, nil, nil, forms, nil).WithFormDocuments(submitFormDocuments{
		own:     {ID: own, OrganizationID: "org-1", MimeType: "image/png"},
		foreign: {ID: foreign, OrganizationID: "org-2", MimeType: "image/png"},
	})
	def := &form.Definition{OrganizationID: "org-1", Name: "handover", Active: true,
		Schema: form.JSONMap{"type": "object", "properties": map[string]any{
			"kind":      map[string]any{"type": "string"},
			"serial":    map[string]any{"type": "string"},
			"signature": map[string]any{"type": "string", "format": form.FormatSignature},
		}},
		UIHints: form.JSONMap{"fields": map[string]any{"serial": map[string]any{form.VisibleWhen: map[string]any{"op": "eq", "field": "kind", "value": "router"}}}}}
	if err := forms.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	run := func(values map[string]any) Step {
		t.Helper()
		wf := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Active: true,
			Actions: []JSONMap{{"type": "submit_form", "form_id": def.ID, "values": values}}}
		if err := repo.CreateDefinition(ctx, wf); err != nil {
			t.Fatal(err)
		}
		r, err := exec.Trigger(ctx, "org-1", wf, "manual", JSONMap{})
		if err != nil {
			t.Fatal(err)
		}
		steps, err := repo.ListSteps(ctx, "org-1", r.ID)
		if err != nil || len(steps) != 1 {
			t.Fatalf("steps: %+v %v", steps, err)
		}
		return steps[0]
	}
	step := run(map[string]any{"kind": "switch", "serial": "SN-1", "signature": own})
	id, _ := step.Output["submission_id"].(string)
	sub, err := forms.GetSubmission(ctx, "org-1", id)
	if step.Status != StatusSucceeded || err != nil {
		t.Fatalf("submission with a hidden field: step %s %q, %v", step.Status, step.Error, err)
	}
	if _, kept := sub.Values["serial"]; kept || sub.Values["signature"] != own {
		t.Errorf("stored values %v, want serial dropped and the signature kept", sub.Values)
	}
	step = run(map[string]any{"kind": "router", "serial": "SN-1", "signature": foreign})
	fields, _ := step.Output["fields"].([]form.FieldError)
	if step.Status != StatusFailed || len(fields) != 1 || fields[0].Field != "values.signature" {
		t.Errorf("foreign signature: step %s %q fields %v, want failed on values.signature", step.Status, step.Error, fields)
	}
}
