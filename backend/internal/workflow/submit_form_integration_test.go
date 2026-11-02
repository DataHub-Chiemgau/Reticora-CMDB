package workflow_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// TestSubmitFormActionOnPostgres covers WP-603 (WFL-01, WFL-02): the
// workflow action submit_form wrote the literal "workflow" into the user id
// column form_submission.submitted_by and failed on PostgreSQL every time
// (22P02); it also skipped the schema and active checks of the form API. Now
// submitted_by is the user who triggered the run, NULL for a trigger without
// a user, and an inactive form, values against the schema, a form id that is
// no UUID and a form outside the caller's scope fail the step with the fixed
// text of the rule, without SQL text and without a submission.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//	  go test -count=1 -run '^TestSubmitFormActionOnPostgres$' ./internal/workflow/
func TestSubmitFormActionOnPostgres(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wf-submitter")
	runs := workflow.NewPGRepository(f.App)
	forms := form.NewPGRepository(f.App)
	exec := workflow.NewExecutor(runs, nil, nil, forms, nil)
	userScope := database.OrgWideScope(f.OrgA, user)
	userCtx := tenant.WithTenant(database.ContextWithTenantScope(bg, &userScope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	systemScope := database.OrgWideScope(f.OrgA, "")
	systemCtx := tenant.WithTenant(database.ContextWithTenantScope(bg, &systemScope), tenant.TenantInfo{OrganizationID: f.OrgA})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: user})

	schema := form.JSONMap{"type": "object", "required": []any{"hostname"},
		"properties": map[string]any{"hostname": map[string]any{"type": "string", "minLength": 3}}}
	active := &form.Definition{OrganizationID: f.OrgA, Name: "wf-active", Schema: schema, Active: true}
	inactive := &form.Definition{OrganizationID: f.OrgA, Name: "wf-inactive", Schema: schema, Active: false}
	client2 := &form.Definition{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "wf-client2", Schema: form.JSONMap{}, Active: true}
	for _, d := range []*form.Definition{active, inactive, client2} {
		if err := forms.CreateDefinition(userCtx, d); err != nil {
			t.Fatalf("form %s: %v", d.Name, err)
		}
	}

	seq := 0
	run := func(ctx context.Context, action workflow.JSONMap) (*workflow.Run, workflow.Step) {
		t.Helper()
		seq++
		def := &workflow.Definition{OrganizationID: f.OrgA, Name: fmt.Sprintf("wf-submit-%d", seq), Trigger: workflow.JSONMap{"type": "manual"},
			Actions: []workflow.JSONMap{action}, Active: true}
		if err := runs.CreateDefinition(ctx, def); err != nil {
			t.Fatalf("workflow: %v", err)
		}
		r, err := exec.Trigger(ctx, f.OrgA, def, "manual", workflow.JSONMap{})
		if err != nil {
			t.Fatalf("trigger %v: %v", action, err)
		}
		steps, err := runs.ListSteps(ctx, f.OrgA, r.ID)
		if err != nil || len(steps) != 1 {
			t.Fatalf("steps of run %s: %+v, %v; want one", r.ID, steps, err)
		}
		return r, steps[0]
	}
	submittedBy := func(step workflow.Step) *string {
		t.Helper()
		id, _ := step.Output["submission_id"].(string)
		var by *string
		var formID, hostname string
		if err := f.Admin.QueryRow(bg, `SELECT submitted_by::text, form_id::text, values->>'hostname' FROM form_submission WHERE id = $1`, id).
			Scan(&by, &formID, &hostname); err != nil {
			t.Fatalf("submission %q of the step: %v", id, err)
		}
		if formID != active.ID || hostname != "srv-01" {
			t.Errorf("submission %s: form %s hostname %q, want %s srv-01", id, formID, hostname, active.ID)
		}
		return by
	}
	submissions := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM form_submission WHERE organization_id = $1`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	valid := map[string]any{"hostname": "srv-01"}

	// Triggered by a user: the run succeeds, submitted_by is the user.
	r, step := run(userCtx, workflow.JSONMap{"type": "submit_form", "form_id": active.ID, "values": valid})
	if r.Status != workflow.StatusSucceeded || step.Status != workflow.StatusSucceeded {
		t.Fatalf("submit_form by a user: run %s step %s %q, want succeeded", r.Status, step.Status, step.Error)
	}
	if by := submittedBy(step); by == nil || *by != user {
		t.Errorf("submitted_by of a run triggered by %s: %v", user, by)
	}

	// Triggered without a user: submitted_by is NULL, never "workflow".
	r, step = run(systemCtx, workflow.JSONMap{"type": "submit_form", "form_id": active.ID, "values": valid})
	if r.Status != workflow.StatusSucceeded || step.Status != workflow.StatusSucceeded {
		t.Fatalf("submit_form without a user: run %s step %s %q, want succeeded", r.Status, step.Status, step.Error)
	}
	if by := submittedBy(step); by != nil {
		t.Errorf("submitted_by of a run without a user: %q, want NULL", *by)
	}

	// Refused submissions fail the step with the fixed text of the rule.
	before := submissions()
	for _, c := range []struct {
		name   string
		ctx    context.Context
		action workflow.JSONMap
		want   string
	}{
		{"inactive form", userCtx, workflow.JSONMap{"type": "submit_form", "form_id": inactive.ID, "values": valid}, "form definition is inactive"},
		{"value against the schema", userCtx, workflow.JSONMap{"type": "submit_form", "form_id": active.ID, "values": map[string]any{"hostname": "x"}},
			form.ValidationError{}.Error()},
		{"required value missing", userCtx, workflow.JSONMap{"type": "submit_form", "form_id": active.ID}, form.ValidationError{}.Error()},
		{"form id no UUID", userCtx, workflow.JSONMap{"type": "submit_form", "form_id": "not-a-uuid", "values": valid}, form.ErrNotFound.Error()},
		{"form of client 2 for a client-1 principal", client1Ctx, workflow.JSONMap{"type": "submit_form", "form_id": client2.ID, "values": valid},
			form.ErrNotFound.Error()},
	} {
		r, step = run(c.ctx, c.action)
		if r.Status != workflow.StatusFailed || step.Status != workflow.StatusFailed || step.Error != c.want {
			t.Errorf("%s: run %s step %s %q, want failed %q", c.name, r.Status, step.Status, step.Error, c.want)
		}
		for _, m := range []string{"SQLSTATE", "ERROR:", "invalid input syntax", "violates", "form_submission", "form_def"} {
			if strings.Contains(step.Error, m) {
				t.Errorf("%s: step error discloses database text %q: %s", c.name, m, step.Error)
			}
		}
	}
	if after := submissions(); after != before {
		t.Errorf("submissions after refused actions: %d, want %d", after, before)
	}
}
