package lifecycle_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
)

// TestStatusAndLifecycleAreSeparate covers LCY-01 (WP-122) against
// PostgreSQL: ci.status stays the operative status and lifecycle_state is
// a separate state space. A status change leaves the lifecycle state as it
// is and a lifecycle transition leaves the status as it is — both
// persisted; neither column accepts a value of the other; a CI type
// without a lifecycle model has a status but no lifecycle state.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStatusAndLifecycleAreSeparate(t *testing.T) {
	f := scopetest.Seed(t, "8c")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)

	// A software lifecycle (LCY-02 states) on a CI type of the organization.
	defs := lifecycle.NewPGRepository(f.App)
	software := &lifecycle.Definition{OrganizationID: f.OrgA, Key: "lcy01_software", Name: "Software", AppliesTo: "ci",
		States: []lifecycle.State{
			{Key: "evaluation", Label: "Evaluation", IsInitial: true},
			{Key: "approved", Label: "Approved"},
			{Key: "production", Label: "Production"},
		},
		Transitions: []lifecycle.Transition{
			{FromStateKey: "evaluation", ToStateKey: "approved", Key: "approve"},
			{FromStateKey: "approved", ToStateKey: "production", Key: "go_live"},
		}}
	if err := defs.Create(ctx, software); err != nil {
		t.Fatalf("create lifecycle: %v", err)
	}
	withModel := f.CI(t, f.OrgA, "", "lcy01-app")
	withoutModel := f.CI(t, f.OrgA, "", "lcy01-switch")
	if _, err := f.Admin.Exec(bg, `
		WITH t AS (INSERT INTO ci_type (organization_id, key, name, lifecycle_definition_id)
		           VALUES ($1, 'lcy01-application', 'LCY-01 application', $2) RETURNING id)
		UPDATE ci SET ci_type_id = (SELECT id FROM t) WHERE id = $3`, f.OrgA, software.ID, withModel); err != nil {
		t.Fatalf("lifecycle type: %v", err)
	}

	cis := ci.NewPGRepository(f.App)
	service := lifecycle.NewService(defs, lifecycle.NewPGStateStore(f.App), nil)
	stored := func(id string) (status string, state *string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT status, lifecycle_state FROM ci WHERE id = $1`, id).Scan(&status, &state); err != nil {
			t.Fatalf("read ci: %v", err)
		}
		return status, state
	}
	setStatus := func(id, status string) {
		t.Helper()
		if _, err := cis.Update(ctx, f.OrgA, id, ci.UpdateRequest{Status: &status}); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	transition := func(to string) {
		t.Helper()
		if _, err := service.Transition(ctx, f.OrgA, "ci", withModel, software.Key, lifecycle.TransitionRequest{ToState: to}); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}

	// Lifecycle transitions keep the status.
	setStatus(withModel, ci.StatusMaintenance)
	transition("evaluation")
	transition("approved")
	if status, state := stored(withModel); status != ci.StatusMaintenance || state == nil || *state != "approved" {
		t.Fatalf("after transitions: status %q state %v, want maintenance/approved", status, state)
	}

	// Status changes keep the lifecycle state, through every status.
	for _, status := range ci.Statuses() {
		setStatus(withModel, status)
		if got, state := stored(withModel); got != status || state == nil || *state != "approved" {
			t.Fatalf("status %s: stored %q state %v, want the lifecycle state approved kept", status, got, state)
		}
	}
	transition("production")
	if status, state := stored(withModel); status != ci.StatusUnknown || *state != "production" {
		t.Fatalf("after go-live: status %q state %q", status, *state)
	}
	item, err := cis.GetByID(ctx, f.OrgA, withModel)
	if err != nil || item.Status != ci.StatusUnknown || item.LifecycleState != "production" {
		t.Fatalf("API view: %+v %v", item, err)
	}

	// Separate value spaces: neither column takes a value of the other.
	for _, state := range []string{"evaluation", "approved", "production"} {
		if slices.Contains(ci.Statuses(), state) {
			t.Errorf("lifecycle state %q is also a status", state)
		}
		if _, err = f.Admin.Exec(bg, `UPDATE ci SET status = $1 WHERE id = $2`, state, withModel); err == nil {
			t.Errorf("status accepted the lifecycle state %q", state)
		}
	}
	for _, status := range ci.Statuses() {
		if _, err = f.Admin.Exec(bg, `UPDATE ci SET lifecycle_state = $1 WHERE id = $2`, status, withModel); err == nil {
			t.Errorf("lifecycle_state accepted the status %q", status)
		}
	}
	if _, err = service.Transition(ctx, f.OrgA, "ci", withModel, software.Key,
		lifecycle.TransitionRequest{ToState: ci.StatusActive}); err == nil {
		t.Error("a transition to a status was accepted")
	}

	// Without a lifecycle model: status only.
	setStatus(withoutModel, ci.StatusInactive)
	if status, state := stored(withoutModel); status != ci.StatusInactive || state != nil {
		t.Errorf("CI without a model: status %q state %v", status, state)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET lifecycle_state = 'approved' WHERE id = $1`, withoutModel); err == nil {
		t.Error("a CI of a type without a lifecycle model got a lifecycle state")
	}
}
