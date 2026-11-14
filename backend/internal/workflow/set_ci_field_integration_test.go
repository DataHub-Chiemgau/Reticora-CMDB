package workflow_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
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

// setFieldEnv is set_ci_field through the CI service (WP-1097): field
// rights of the initiator, attribute and catalog rules, the audit with the
// run as correlation.
type setFieldEnv struct {
	f        *scopetest.Fixture
	ctx      context.Context
	runs     *workflow.PGRepository
	exec     *workflow.Executor
	overs    *override.PGRepository
	types    *citype.PGRepository
	sequence int
}

func newSetFieldEnv(t *testing.T) *setFieldEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	types := citype.NewPGRepository(f.App)
	svc := ci.NewService(repo).WithFieldResolver(types)
	overs := override.NewPGRepository(f.App)
	runs := workflow.NewPGRepository(f.App)
	queue := discovery.NewHandler(discovery.NewPGRepository(f.App), repo).ReviewService()
	exec := workflow.NewExecutor(runs, nil, repo, nil, nil).WithCIService(svc).WithFieldGuard(override.NewGuard(overs)).
		WithConflictReviews(queue).WithPolicies(permission.NewFieldResolver(permission.NewPGRepository(f.App), nil, permission.NewPGFieldGrants(f.App), nil),
		user.NewPGRepository(f.App))
	return &setFieldEnv{f: f, ctx: ctx, runs: runs, exec: exec, overs: overs, types: types}
}

// run executes a run of a definition with action in ctx and returns it
// with its only step.
func (e *setFieldEnv) run(t *testing.T, ctx context.Context, action workflow.JSONMap) (*workflow.Run, workflow.Step) {
	t.Helper()
	e.sequence++
	def := &workflow.Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-1097-%d", e.sequence), Trigger: workflow.JSONMap{"type": "manual"},
		Actions: []workflow.JSONMap{action}, Active: true}
	if err := e.runs.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	r, err := e.exec.Trigger(ctx, e.f.OrgA, def, "manual", workflow.JSONMap{})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if len(r.Steps) != 1 {
		t.Fatalf("steps of run %s: %+v, want one", r.ID, r.Steps)
	}
	return r, r.Steps[0]
}

// grantRole gives the user of f a new role of the organization with the
// permissions perms and returns the role id.
func grantRole(t *testing.T, f *scopetest.Fixture, userID string, perms string) string {
	t.Helper()
	bg := context.Background()
	var role string
	if err := f.Admin.QueryRow(bg, `INSERT INTO role (organization_id, name, permissions) VALUES ($1, $2, $3::jsonb) RETURNING id::text`,
		f.OrgA, fmt.Sprintf("wf-1097-%s", f.ID()), perms).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userID, role); err != nil {
		t.Fatal(err)
	}
	return role
}

func (e *setFieldEnv) version(t *testing.T, ciID string) int64 {
	t.Helper()
	var v int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT version FROM ci WHERE id = $1`, ciID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSetCIFieldRaisesVersionOnce: three fields in fields are written in
// one update: the version rises by one, one audit entry ci.updated.
func TestSetCIFieldRaisesVersionOnce(t *testing.T) {
	e := newSetFieldEnv(t)
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-once")
	before := e.version(t, device)
	run, step := e.run(t, e.ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device,
		"fields": map[string]any{"rack": "R7", "hostname": "srv-once", "status": "maintenance"}})
	if run.Status != workflow.StatusSucceeded {
		t.Fatalf("run %s: %q", run.Status, step.Error)
	}
	fields, _ := step.Output["fields"].(map[string]any)
	for _, name := range []string{"rack", "hostname", "status"} {
		if f, _ := fields[name].(map[string]any); f["written"] != true {
			t.Errorf("output of %s: %v, want written", name, fields[name])
		}
	}
	if after := e.version(t, device); after != before+1 {
		t.Errorf("version %d after %d, want one step", after, before)
	}
	var rack, hostname, status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT attributes->>'rack', hostname, status FROM ci WHERE id = $1`, device).
		Scan(&rack, &hostname, &status); err != nil || rack != "R7" || hostname != "srv-once" || status != "maintenance" {
		t.Errorf("CI after the action: %q %q %q %v", rack, hostname, status, err)
	}
	var entries int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		AND resource_type = 'ci' AND correlation_id = $3`, e.f.OrgA, device, run.ID).Scan(&entries); err != nil || entries != 1 {
		t.Errorf("audit entries of the CI in the run: %d %v, want 1", entries, err)
	}
}

// TestSetCIFieldAuditCarriesRun: the audit entry and the history rows of
// the CI name the initiator, source system and the run as correlation;
// a run without initiator writes without actor.
func TestSetCIFieldAuditCarriesRun(t *testing.T) {
	e := newSetFieldEnv(t)
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-audit")
	initiator := e.f.AppUser(t, e.f.OrgA, "wf-1097-initiator")
	grantRole(t, e.f, initiator, `["ci:read","ci:write"]`)
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: initiator})
	run, step := e.run(t, ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R1"})
	if run.Status != workflow.StatusSucceeded || run.InitiatedBy != initiator {
		t.Fatalf("run %s initiated by %q: %q", run.Status, run.InitiatedBy, step.Error)
	}
	audited := func(runID string) (actor, source string) {
		t.Helper()
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(actor_id::text, ''), source FROM audit_log
			WHERE organization_id = $1 AND resource_id = $2 AND resource_type = 'ci' AND correlation_id = $3`, e.f.OrgA, device, runID).
			Scan(&actor, &source); err != nil {
			t.Fatalf("audit entry of run %s: %v", runID, err)
		}
		return actor, source
	}
	if actor, source := audited(run.ID); actor != initiator || source != audit.SourceSystem {
		t.Errorf("audit entry: actor %q source %q, want the initiator and system", actor, source)
	}
	// The history rows carry the initiator as actor, source system and the
	// run as correlation; a run without initiator has rows without actor.
	changes := func(runID string) (total, byActor int) {
		t.Helper()
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE COALESCE(actor_id::text, '') = $3)
			FROM ci_change WHERE ci_id = $1 AND correlation_id = $2 AND source = 'system'`, device, runID, initiator).Scan(&total, &byActor); err != nil {
			t.Fatal(err)
		}
		return total, byActor
	}
	if total, byActor := changes(run.ID); total == 0 || byActor != total {
		t.Errorf("history rows of the run: %d, %d by the initiator, want some, all with the initiator as actor", total, byActor)
	}
	// Without initiator (a system trigger): no actor.
	scope := database.OrgWideScope(e.f.OrgA, "")
	system := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: e.f.OrgA})
	anonymous, step := e.run(t, system, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R2"})
	if anonymous.Status != workflow.StatusSucceeded || anonymous.InitiatedBy != "" {
		t.Fatalf("run without initiator: %s %q %q", anonymous.Status, anonymous.InitiatedBy, step.Error)
	}
	if actor, source := audited(anonymous.ID); actor != "" || source != audit.SourceSystem {
		t.Errorf("audit entry without initiator: actor %q source %q, want none and system", actor, source)
	}
	var total, withActor int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*), count(actor_id) FROM ci_change WHERE ci_id = $1 AND correlation_id = $2
		AND source = 'system'`, device, anonymous.ID).Scan(&total, &withActor); err != nil || total == 0 || withActor != 0 {
		t.Errorf("history rows without initiator: %d, %d with an actor, %v; want some, none with an actor", total, withActor, err)
	}
}

// TestSetCIFieldNeedsInitiatorActionRight: in an organization without
// field permissions an initiator whose role only reads CIs (no ci:write)
// fails the step with the text of the action check; the value stays. The
// rights are those the initiator holds, not all permissions (AK2).
func TestSetCIFieldNeedsInitiatorActionRight(t *testing.T) {
	e := newSetFieldEnv(t)
	bg := context.Background()
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-action-right")
	if _, err := e.f.Admin.Exec(bg, `UPDATE ci SET attributes = '{"rack":"R1"}' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	reader := e.f.AppUser(t, e.f.OrgA, "wf-1097-ci-reader")
	grantRole(t, e.f, reader, `["ci:read"]`)
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: reader})
	run, step := e.run(t, ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R5"})
	if run.Status != workflow.StatusFailed || step.Status != workflow.StatusFailed || step.Error == "" {
		t.Fatalf("run of the reader: %s step %s %q, want failed with the text of the check", run.Status, step.Status, step.Error)
	}
	var rack string
	if err := e.f.Admin.QueryRow(bg, `SELECT attributes->>'rack' FROM ci WHERE id = $1`, device).Scan(&rack); err != nil || rack != "R1" {
		t.Errorf("rack after the refused write: %q %v, want R1", rack, err)
	}
	// A deactivated initiator writes nothing either.
	writer := e.f.AppUser(t, e.f.OrgA, "wf-1097-inactive")
	grantRole(t, e.f, writer, `["ci:read","ci:write"]`)
	if _, err := e.f.Admin.Exec(bg, `UPDATE app_user SET is_active = false WHERE id = $1`, writer); err != nil {
		t.Fatal(err)
	}
	ctx = tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: writer})
	if run, step = e.run(t, ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R6"}); run.Status != workflow.StatusFailed {
		t.Errorf("run of the deactivated initiator: %s %q, want failed", run.Status, step.Error)
	}
	if err := e.f.Admin.QueryRow(bg, `SELECT attributes->>'rack' FROM ci WHERE id = $1`, device).Scan(&rack); err != nil || rack != "R1" {
		t.Errorf("rack after the run of the deactivated initiator: %q %v, want R1", rack, err)
	}
}

// TestSetCIFieldKeepsOverride: a field with a manual override stays and
// its review opens; another field of the same action is written.
func TestSetCIFieldKeepsOverride(t *testing.T) {
	e := newSetFieldEnv(t)
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-override")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET discovery_source = 'snmp', attributes = '{"owner":"ops","rack":"R1"}' WHERE id = $1`,
		device); err != nil {
		t.Fatal(err)
	}
	if _, err := e.overs.SetOverride(e.ctx, e.f.OrgA, device, "owner", "ops", e.f.User, "by hand", true); err != nil {
		t.Fatal(err)
	}
	run, step := e.run(t, e.ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device,
		"fields": map[string]any{"owner": "wf-team", "rack": "R9"}})
	if run.Status != workflow.StatusSucceeded {
		t.Fatalf("run %s: %q", run.Status, step.Error)
	}
	fields, _ := step.Output["fields"].(map[string]any)
	owner, _ := fields["owner"].(map[string]any)
	rack, _ := fields["rack"].(map[string]any)
	if owner["written"] != false || owner["reason"] != override.ReasonOverrideConflict || rack["written"] != true {
		t.Errorf("output %v, want owner kept as conflict and rack written", fields)
	}
	var gotOwner, gotRack string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT attributes->>'owner', attributes->>'rack' FROM ci WHERE id = $1`, device).
		Scan(&gotOwner, &gotRack); err != nil || gotOwner != "ops" || gotRack != "R9" {
		t.Errorf("CI: owner %q rack %q %v, want ops and R9", gotOwner, gotRack, err)
	}
	var open int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
		AND status = 'open' AND object_key = $2`, e.f.OrgA, "ci:"+device+"/owner").Scan(&open); err != nil || open != 1 {
		t.Errorf("open override_conflict reviews: %d %v, want 1", open, err)
	}
}

// TestSetCIFieldHonoursFieldRights: an initiator whose role reads but does
// not write the field fails the step; the value stays.
func TestSetCIFieldHonoursFieldRights(t *testing.T) {
	e := newSetFieldEnv(t)
	bg := context.Background()
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-rights")
	if _, err := e.f.Admin.Exec(bg, `UPDATE ci SET attributes = '{"rack":"R1"}' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO role (organization_id, name, permissions) VALUES ($1, $2, '["ci:read","ci:write"]'::jsonb)
		RETURNING id::text`, e.f.OrgA, "wf-1097-reader-"+e.f.ID()).Scan(&role); err != nil {
		t.Fatal(err)
	}
	user := e.f.AppUser(t, e.f.OrgA, "wf-1097-reader")
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, e.f.OrgA, user, role); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'attributes.rack', 'read')`,
		e.f.OrgA, role); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: user})
	run, step := e.run(t, ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R5"})
	if run.Status != workflow.StatusFailed || step.Status != workflow.StatusFailed || step.Error == "" {
		t.Fatalf("run of the reader: %s step %s %q, want failed with the text of the check", run.Status, step.Status, step.Error)
	}
	var rack string
	if err := e.f.Admin.QueryRow(bg, `SELECT attributes->>'rack' FROM ci WHERE id = $1`, device).Scan(&rack); err != nil || rack != "R1" {
		t.Errorf("rack after the refused write: %q %v, want R1", rack, err)
	}
}

// TestSetCIFieldCatalogRule: a readonly catalog rule on the field fails the
// step with violations in its output.
func TestSetCIFieldCatalogRule(t *testing.T) {
	e := newSetFieldEnv(t)
	typ := &citype.Type{OrganizationID: e.f.OrgA, Key: "wf1097-type", Name: "wf1097-type", IsObservable: true}
	if err := e.types.Create(e.ctx, typ); err != nil {
		t.Fatal(err)
	}
	def := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wf1097_rack", DataType: "text"}
	if err := e.types.CreateAttributeDefinition(e.ctx, e.f.OrgA, def); err != nil {
		t.Fatal(err)
	}
	if _, err := e.types.CreateRule(e.ctx, e.f.OrgA, &citype.RuleInput{Key: "wf1097-readonly", AttributeKey: "wf1097_rack",
		Kind: citype.RuleKindReadonly, CITypeID: typ.ID}); err != nil {
		t.Fatal(err)
	}
	device := e.f.CI(t, e.f.OrgA, "", "wf-1097-rule")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET ci_type_id = $2, attributes = '{"wf1097_rack":"R1"}' WHERE id = $1`,
		device, typ.ID); err != nil {
		t.Fatal(err)
	}
	run, step := e.run(t, e.ctx, workflow.JSONMap{"type": "set_ci_field", "ci_id": device, "field": "wf1097_rack", "value": "R2"})
	if run.Status != workflow.StatusFailed || step.Status != workflow.StatusFailed {
		t.Fatalf("run against the readonly rule: %s step %s %q, want failed", run.Status, step.Status, step.Error)
	}
	violations, _ := step.Output["violations"].([]any)
	if len(violations) == 0 {
		t.Fatalf("step output %v, want violations", step.Output)
	}
	if v, _ := violations[0].(map[string]any); v["field"] != "wf1097_rack" || v["code"] != ci.CodeReadOnly {
		t.Errorf("violation %v, want wf1097_rack with code %s", v, ci.CodeReadOnly)
	}
}
