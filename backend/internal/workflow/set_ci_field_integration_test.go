package workflow_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// TestSetCIFieldOnPostgreSQL covers WP-576 (WFL-02, REC-12, OVR-02, F-231)
// with the production wiring on PostgreSQL: set_ci_field writes a field
// without override as source workflow with rank 92, keeps the provenance and
// raises the CI version (API-07); a value that differs from a manual
// override keeps the override, is kept as the observed value and opens
// exactly one override_conflict review through the review queue of
// discovery (payload with source, rank and reported value), deduplicated
// while it is open. A dismissed value stays suppressed until the workflow
// writes another one (F-248).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//	  go test -count=1 -run '^TestSetCIFieldOnPostgreSQL$' ./internal/workflow/
func TestSetCIFieldOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	device := f.CI(t, f.OrgA, "", "wf-set-field")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET discovery_source = 'snmp', attributes = '{"owner":"ops","rack":"R1"}' WHERE id = $1`,
		device); err != nil {
		t.Fatal(err)
	}
	overrides := override.NewPGRepository(f.App)
	if _, err := overrides.SetOverride(ctx, f.OrgA, device, "owner", "ops", f.User, "assigned by hand", true); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	queue := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).ReviewService()
	runs := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(runs, nil, cis, nil, nil).WithFieldGuard(override.NewGuard(overrides)).WithConflictReviews(queue)

	seq := 0
	run := func(field string, value any) workflow.Step {
		t.Helper()
		seq++
		def := &workflow.Definition{OrganizationID: f.OrgA, Name: fmt.Sprintf("wf-set-%d", seq), Trigger: workflow.JSONMap{"type": "manual"},
			Actions: []workflow.JSONMap{{"type": "set_ci_field", "ci_id": device, "field": field, "value": value}}, Active: true}
		if err := runs.CreateDefinition(ctx, def); err != nil {
			t.Fatalf("workflow: %v", err)
		}
		r, err := exec.Trigger(ctx, f.OrgA, def, "manual", workflow.JSONMap{})
		if err != nil {
			t.Fatalf("trigger: %v", err)
		}
		steps, err := runs.ListSteps(ctx, f.OrgA, r.ID)
		if err != nil || len(steps) != 1 {
			t.Fatalf("steps of run %s: %+v, %v; want one", r.ID, steps, err)
		}
		if r.Status != workflow.StatusSucceeded {
			t.Fatalf("set_ci_field %s=%v: run %s step %q", field, value, r.Status, steps[0].Error)
		}
		return steps[0]
	}
	attribute := func(field string) (value string, version int64) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(attributes->>$2, ''), version FROM ci WHERE id = $1`, device, field).
			Scan(&value, &version); err != nil {
			t.Fatal(err)
		}
		return value, version
	}
	provenance := func(field string) (value, source string, rank int, overridden bool) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(discovered_value #>> '{}', ''), COALESCE(discovered_source, ''),
			COALESCE(discovered_rank, 0), override_at IS NOT NULL FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`, device, field).
			Scan(&value, &source, &rank, &overridden); err != nil {
			t.Fatalf("provenance of %s: %v", field, err)
		}
		return value, source, rank, overridden
	}
	items := func(status string) []reviews.Item {
		t.Helper()
		list, _, err := queue.List(ctx, f.OrgA, reviews.Filter{Kind: reviews.KindOverrideConflict, ObjectKey: "ci:" + device + "/owner",
			Status: status}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	// A field without override: written with rank 92, the version rises.
	_, v0 := attribute("rack")
	if step := run("rack", "R2"); step.Output["written"] != true || step.Output["reason"] != override.ReasonAllowed {
		t.Errorf("set rack: output %v", step.Output)
	}
	if rack, v1 := attribute("rack"); rack != "R2" || v1 <= v0 {
		t.Errorf("rack after the workflow: %q version %d (before %d)", rack, v1, v0)
	}
	if value, source, rank, _ := provenance("rack"); value != "R2" || source != "workflow" || rank != 92 {
		t.Errorf("provenance of rack: %q %q %d, want R2 workflow 92", value, source, rank)
	}

	// A value against the override: the override stays, one review.
	if step := run("owner", "workflow-team"); step.Output["written"] != false || step.Output["reason"] != override.ReasonOverrideConflict {
		t.Errorf("set owner: output %v", step.Output)
	}
	if owner, _ := attribute("owner"); owner != "ops" {
		t.Errorf("owner after the workflow: %q, want the override ops", owner)
	}
	if value, source, rank, overridden := provenance("owner"); value != "workflow-team" || source != "workflow" || rank != 92 || !overridden {
		t.Errorf("provenance of owner: %q %q %d overridden %v", value, source, rank, overridden)
	}
	open := items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("override_conflict items: %+v, want one", open)
	}
	if p := open[0].Payload; p["observed_value"] != "workflow-team" || p["source"] != "workflow" || p["rank"] != float64(92) ||
		p["field"] != "owner" || p["ci_id"] != device {
		t.Errorf("payload: %+v", p)
	}
	// While it is open, the same value opens no second item.
	run("owner", "workflow-team")
	if all := items(""); len(all) != 1 {
		t.Errorf("items after a repeated conflict: %d, want 1", len(all))
	}

	// Dismissed: the value stays suppressed until the workflow writes
	// another one (here the override value), then it opens a new item.
	if _, err := queue.Resolve(ctx, f.OrgA, open[0].ID, &reviews.Resolution{Action: reviews.ActionDismiss, ResolvedBy: f.User}); err != nil {
		t.Fatal(err)
	}
	run("owner", "workflow-team")
	if all := items(""); len(all) != 1 {
		t.Errorf("items after the dismissed value was written again: %d, want 1", len(all))
	}
	if step := run("owner", "ops"); step.Output["reason"] != override.ReasonOverride {
		t.Errorf("set the override value: output %v", step.Output)
	}
	run("owner", "workflow-team")
	if open = items(reviews.StatusOpen); len(open) != 1 || open[0].Payload["observed_value"] != "workflow-team" {
		t.Errorf("items after another value in between: %+v, want one open", open)
	}
	// Another value while that item is open opens no second one.
	run("owner", "other-team")
	if all := items(""); len(all) != 2 {
		t.Errorf("items at the end: %d, want 2 (one dismissed, one open)", len(all))
	}
	if owner, _ := attribute("owner"); owner != "ops" {
		t.Errorf("owner at the end: %q, want the override ops", owner)
	}
}
