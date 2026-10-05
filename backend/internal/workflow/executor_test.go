package workflow

import (
	"context"
	"errors"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"testing"
	"time"
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
	list, total, err := tickets.List(context.Background(), "org-1", ticket.FilterParams{}, api.PaginationParams{Limit: 10})
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
	failOnRunCalls map[string]bool
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
	runs, _, lerr := repo.MemoryRepository.ListRuns(context.Background(), "org-1", "", "", api.PaginationParams{Limit: 10})
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
	runs, _, _ := repo.MemoryRepository.ListRuns(context.Background(), "org-1", "", "", api.PaginationParams{Limit: 10})
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
	repo.listStepsErr = errors.New("db down")
	if _, err := exec.Approve(context.Background(), "org-1", run.ID, "approved", ""); err == nil {
		t.Fatal("expected error when ListSteps fails during approval")
	}
	repo.listStepsErr = nil
	repo.updateStepErr = errors.New("write denied")
	if _, err := exec.Approve(context.Background(), "org-1", run.ID, "approved", ""); err == nil {
		t.Fatal("expected error when UpdateStep fails during approval")
	}
	repo.updateStepErr = nil
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
	runs, _, _ := repo.ListRuns(context.Background(), "org-1", "", "", api.PaginationParams{Limit: 10})
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
