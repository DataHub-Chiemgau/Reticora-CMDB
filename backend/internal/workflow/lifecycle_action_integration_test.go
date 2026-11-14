package workflow_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// The action transition_lifecycle (LCY-04, WP-1097) moves a CI through the
// lifecycle service with the guards of its model: history, audit and the
// event lifecycle.changed come from the service; a guard violation fails the
// step with violations, a type without model with a fixed text.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestTransitionLifecycle' ./internal/workflow/

// lifecycleEnv is a run engine with transition_lifecycle and a CI type
// whose model has the states deployed (initial) and retired; the edge
// deployed → retired requires retire_reason in the context.
type lifecycleEnv struct {
	f        *scopetest.Fixture
	ctx      context.Context
	runs     *workflow.PGRepository
	exec     *workflow.Executor
	typeID   string
	sequence int
}

func newLifecycleEnv(t *testing.T) *lifecycleEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	models := lifecycle.NewPGRepository(f.App)
	tag := time.Now().UnixNano()
	model := &lifecycle.Definition{OrganizationID: f.OrgA, Key: fmt.Sprintf("wf1097-%d", tag), Name: "wf1097", AppliesTo: "ci",
		States: []lifecycle.State{{Key: "deployed", Label: "Deployed", IsInitial: true}, {Key: "retired", Label: "Retired", IsTerminal: true, SortOrder: 1}},
		Transitions: []lifecycle.Transition{{FromStateKey: "deployed", ToStateKey: "retired", Key: "retire", Label: "Retire",
			RequiredFields: []string{"retire_reason"}}}}
	if err := models.Create(ctx, model); err != nil {
		t.Fatalf("lifecycle model: %v", err)
	}
	typ := &citype.Type{OrganizationID: f.OrgA, Key: fmt.Sprintf("wf1097-lc-%d", tag), Name: "wf1097-lc", IsObservable: true, LifecycleModelID: model.ID}
	if err := citype.NewPGRepository(f.App).Create(ctx, typ); err != nil {
		t.Fatalf("type: %v", err)
	}
	states := lifecycle.NewPGStateStore(f.App)
	svc := lifecycle.NewService(models, states, lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	runs := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(runs, nil, ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()), nil, nil).
		WithLifecycle(workflow.NewLifecycleTransitioner(svc, lifecycle.NewResolver(f.App))).
		WithPolicies(permission.NewFieldResolver(permission.NewPGRepository(f.App), nil, permission.NewPGFieldGrants(f.App), nil), user.NewPGRepository(f.App))
	return &lifecycleEnv{f: f, ctx: ctx, runs: runs, exec: exec, typeID: typ.ID}
}

// ciInState returns a CI of the model's type in state.
func (e *lifecycleEnv) ciInState(t *testing.T, name, state string) string {
	t.Helper()
	id := e.f.CI(t, e.f.OrgA, e.f.Client1, name)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET ci_type_id = $2, lifecycle_state = $3 WHERE id = $1`, id, e.typeID, state); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *lifecycleEnv) run(t *testing.T, payload, action workflow.JSONMap) (*workflow.Run, workflow.Step) {
	t.Helper()
	return e.runAs(t, e.ctx, payload, action)
}

// runAs executes a run of action with payload started in ctx (its user is
// the initiator).
func (e *lifecycleEnv) runAs(t *testing.T, ctx context.Context, payload, action workflow.JSONMap) (*workflow.Run, workflow.Step) {
	t.Helper()
	e.sequence++
	def := &workflow.Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-lc-%d-%d", time.Now().UnixNano(), e.sequence),
		Trigger: workflow.JSONMap{"type": "manual"}, Actions: []workflow.JSONMap{action}, Active: true}
	if err := e.runs.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	r, err := e.exec.Trigger(ctx, e.f.OrgA, def, "manual", payload)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if len(r.Steps) != 1 {
		t.Fatalf("steps %+v, want one", r.Steps)
	}
	return r, r.Steps[0]
}

func (e *lifecycleEnv) state(t *testing.T, ciID string) string {
	t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(lifecycle_state, '') FROM ci WHERE id = $1`, ciID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTransitionLifecycleAction: deployed → retired of the CI of the run
// with the required context; afterwards there is a history row and the
// event lifecycle.changed.
func TestTransitionLifecycleAction(t *testing.T) {
	e := newLifecycleEnv(t)
	device := e.ciInState(t, "wf-lc-retire", "deployed")
	run, step := e.run(t, workflow.JSONMap{"ci_id": device}, workflow.JSONMap{"type": "transition_lifecycle", "to_state": "retired",
		"reason": "end of life", "context": map[string]any{"retire_reason": "replaced"}})
	if run.Status != workflow.StatusSucceeded || step.Output["from_state"] != "deployed" || step.Output["to_state"] != "retired" {
		t.Fatalf("run %s output %v %q, want succeeded deployed → retired", run.Status, step.Output, step.Error)
	}
	if got := e.state(t, device); got != "retired" {
		t.Errorf("state %q, want retired", got)
	}
	bg := context.Background()
	var changes, events int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM entity_change WHERE entity_id = $1 AND field_name = 'lifecycle_state'`, device).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes == 0 {
		t.Error("no history row of the transition")
	}
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND entity_id = $2 AND type = 'lifecycle.changed'`,
		e.f.OrgA, device).Scan(&events); err != nil || events != 1 {
		t.Errorf("lifecycle.changed events: %d %v, want 1", events, err)
	}
}

// TestTransitionLifecycleGuardViolation: the context lacks retire_reason;
// the step fails with violations and the state stays.
func TestTransitionLifecycleGuardViolation(t *testing.T) {
	e := newLifecycleEnv(t)
	device := e.ciInState(t, "wf-lc-guard", "deployed")
	run, step := e.run(t, workflow.JSONMap{}, workflow.JSONMap{"type": "transition_lifecycle", "ci_id": device, "to_state": "retired"})
	if run.Status != workflow.StatusFailed || step.Status != workflow.StatusFailed {
		t.Fatalf("run %s step %s, want failed", run.Status, step.Status)
	}
	violations, _ := step.Output["violations"].([]any)
	if len(violations) != 1 {
		t.Fatalf("output %v, want one violation", step.Output)
	}
	if v, _ := violations[0].(map[string]any); v["field"] != "retire_reason" {
		t.Errorf("violation %v, want retire_reason", v)
	}
	if _, ok := step.Output["missing"]; !ok {
		t.Errorf("output %v lacks missing", step.Output)
	}
	if got := e.state(t, device); got != "deployed" {
		t.Errorf("state %q after the refused transition, want deployed", got)
	}
}

// TestTransitionLifecycleWithoutModel: a CI of a type without model fails
// the step with the fixed text.
func TestTransitionLifecycleWithoutModel(t *testing.T) {
	e := newLifecycleEnv(t)
	device := e.f.CI(t, e.f.OrgA, e.f.Client1, "wf-lc-nomodel")
	run, step := e.run(t, workflow.JSONMap{"ci_id": device}, workflow.JSONMap{"type": "transition_lifecycle", "to_state": "retired"})
	if run.Status != workflow.StatusFailed || step.Error != lifecycle.ErrNoLifecycleModel.Error() {
		t.Errorf("run %s step error %q, want failed with %q", run.Status, step.Error, lifecycle.ErrNoLifecycleModel.Error())
	}
}

// TestTransitionLifecycleNeedsInitiatorRight: an initiator whose role reads
// and writes CIs but lacks lifecycle:transition fails the step with the
// text of the action check and the state stays; with the right the same
// transition succeeds (the route's check, WP-728).
func TestTransitionLifecycleNeedsInitiatorRight(t *testing.T) {
	e := newLifecycleEnv(t)
	device := e.ciInState(t, "wf-lc-right", "deployed")
	action := workflow.JSONMap{"type": "transition_lifecycle", "ci_id": device, "to_state": "retired",
		"context": map[string]any{"retire_reason": "replaced"}}
	as := func(perms string) context.Context {
		u := e.f.AppUser(t, e.f.OrgA, "wf-lc-"+e.f.ID())
		grantRole(t, e.f, u, perms)
		return tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: u})
	}
	run, step := e.runAs(t, as(`["ci:read","ci:write"]`), workflow.JSONMap{}, action)
	if run.Status != workflow.StatusFailed || step.Error == "" {
		t.Fatalf("run without lifecycle:transition: %s %q, want failed with the text of the check", run.Status, step.Error)
	}
	if got := e.state(t, device); got != "deployed" {
		t.Errorf("state %q after the refused transition, want deployed", got)
	}
	run, step = e.runAs(t, as(`["ci:read","lifecycle:transition"]`), workflow.JSONMap{}, action)
	if run.Status != workflow.StatusSucceeded {
		t.Fatalf("run with lifecycle:transition: %s %q, want succeeded", run.Status, step.Error)
	}
	if got := e.state(t, device); got != "retired" {
		t.Errorf("state %q, want retired", got)
	}
}
