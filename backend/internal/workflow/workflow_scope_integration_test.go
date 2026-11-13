package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// TestWorkflowRepositoryScope runs the workflow repository with the
// principal's tenant scope (TEN-06, WP-021). Workflow definitions belong to
// the organization: a principal of organization A manages A's workflows
// only, workflows of organization B stay invisible, and a call without scope
// fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestWorkflowRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	repo := workflow.NewPGRepository(f.App)
	defB := &workflow.Definition{OrganizationID: f.OrgB, Name: "b", Trigger: workflow.JSONMap{}}
	if err := repo.CreateDefinition(f.OrgCtx(f.OrgB), defB); err != nil {
		t.Fatalf("organization B workflow: %v", err)
	}
	ctx := f.ClientCtx(f.Client1)
	defA := &workflow.Definition{OrganizationID: f.OrgA, Name: "a", Trigger: workflow.JSONMap{}}
	if err := repo.CreateDefinition(ctx, defA); err != nil {
		t.Fatalf("organization A workflow: %v", err)
	}
	list, _, err := repo.ListDefinitions(ctx, f.OrgA, false, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != defA.ID {
		t.Fatalf("organization A workflows: %+v err=%v, want only the A workflow", list, err)
	}
	if err = repo.DeleteDefinition(ctx, f.OrgB, defB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B workflow: got %v, want ErrTenantMismatch", err)
	}
	if _, _, err = repo.ListDefinitions(context.Background(), f.OrgA, false, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestWorkflowRunCIScope covers WP-1089 AK1/AK6 and WP-1093 AK1/AK4: a run
// on a CI carries the CI and its client and site, which row level security
// checks (no longer rls.DerivedScopeTables and runVisible). A principal restricted to client 1 does not see a run on a
// CI of client 2 in the list or by id, and cannot start a run on that CI
// (404 like an unknown CI).
func TestWorkflowRunCIScope(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	repo := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(repo, nil, ci.NewPGRepository(f.App), nil, nil)
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: f.User})
	def := &workflow.Definition{OrganizationID: f.OrgA, Name: "wf-run-ci-scope", Trigger: workflow.JSONMap{"type": "manual"}, Active: true}
	if err := repo.CreateDefinition(org, def); err != nil {
		t.Fatal(err)
	}
	ci1 := f.CI(t, f.OrgA, f.Client1, "wf-run-scope-ci1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "wf-run-scope-ci2")
	run1, err := exec.Start(org, f.OrgA, def, "manual", workflow.JSONMap{"ci_id": ci1})
	if err != nil {
		t.Fatal(err)
	}
	run2, err := exec.Start(org, f.OrgA, def, "manual", workflow.JSONMap{"ci": map[string]any{"id": ci2}})
	if err != nil {
		t.Fatal(err)
	}
	if run2.CIID != ci2 || run2.SerialKey != "ci:"+ci2 {
		t.Fatalf("run on CI 2: ci_id %q serial_key %q", run2.CIID, run2.SerialKey)
	}
	list, _, err := repo.ListRuns(client1, f.OrgA, &workflow.RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != run1.ID {
		t.Errorf("runs of client 1: %+v err=%v, want only the run on CI 1", list, err)
	}
	if _, err = repo.GetRun(client1, f.OrgA, run2.ID); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("run on the CI of client 2 for client 1: %v, want ErrRunNotFound", err)
	}
	if got, gerr := repo.GetRun(org, f.OrgA, run2.ID); gerr != nil || got.CIID != ci2 {
		t.Errorf("run on CI 2 org-wide: %+v %v", got, gerr)
	}
	// A deleted CI sets ci_id NULL; the run stays hidden from client 1
	// (fail closed) and visible organization-wide.
	if _, err = f.Admin.Exec(context.Background(), `DELETE FROM ci WHERE id = $1`, ci2); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetRun(client1, f.OrgA, run2.ID); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("run on the deleted CI of client 2 for client 1: %v, want ErrRunNotFound", err)
	}
	if list, _, lerr := repo.ListRuns(client1, f.OrgA, &workflow.RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 100}); lerr != nil || len(list) != 1 {
		t.Errorf("runs of client 1 after the delete: %+v err=%v, want only the run on CI 1", list, lerr)
	}
	if got, gerr := repo.GetRun(org, f.OrgA, run2.ID); gerr != nil || got.CIID != "" || got.SerialKey != "ci:"+ci2 {
		t.Errorf("run on the deleted CI org-wide: %+v %v, want visible without ci_id", got, gerr)
	}
	if _, err = exec.Start(client1, f.OrgA, def, "manual", workflow.JSONMap{"ci_id": ci2}); !errors.Is(err, workflow.ErrCINotFound) {
		t.Errorf("start on the CI of client 2 for client 1: %v, want ErrCINotFound", err)
	}
	if _, err = exec.Start(org, f.OrgA, def, "manual", workflow.JSONMap{"ci_id": "not-a-uuid"}); !errors.Is(err, workflow.ErrCINotFound) {
		t.Errorf("start on a malformed CI id: %v, want ErrCINotFound", err)
	}
	var unseen string
	if err = f.Admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&unseen); err != nil {
		t.Fatal(err)
	}
	if _, err = exec.Start(org, f.OrgA, def, "manual", workflow.JSONMap{"ci_id": unseen}); !errors.Is(err, workflow.ErrCINotFound) {
		t.Errorf("start on an unknown CI: %v, want ErrCINotFound", err)
	}
}

// TestWorkflowStepFollowsRunScope covers WP-1093 AK2: a step carries the
// client and site of its run; a principal of client 1 reads no step of a
// run on a CI of client 2, and a CI moved to client 1 carries its runs and
// their steps along.
func TestWorkflowStepFollowsRunScope(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	repo := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(repo, nil, ci.NewPGRepository(f.App), nil, nil)
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: f.User})
	def := &workflow.Definition{OrganizationID: f.OrgA, Name: "wf-step-scope", Trigger: workflow.JSONMap{"type": "manual"},
		Actions: []workflow.JSONMap{{"type": "noop"}, {"type": "require_approval"}}, Active: true}
	if err := repo.CreateDefinition(org, def); err != nil {
		t.Fatal(err)
	}
	ci2 := f.CI(t, f.OrgA, f.Client2, "wf-step-scope-ci2")
	run, err := exec.Trigger(org, f.OrgA, def, "manual", workflow.JSONMap{"ci_id": ci2})
	if err != nil || run.Status != workflow.StatusWaitingApproval || len(run.Steps) != 2 {
		t.Fatalf("run on CI 2: %+v %v", run, err)
	}
	var stepClients, mismatched int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE s.client_id = $2), count(*) FILTER (WHERE s.client_id IS DISTINCT FROM r.client_id)
		FROM workflow_step s JOIN workflow_run r ON r.id = s.run_id WHERE s.run_id = $1`, run.ID, f.Client2).Scan(&stepClients, &mismatched); err != nil {
		t.Fatal(err)
	}
	if stepClients != 2 || mismatched != 0 {
		t.Errorf("steps with client 2: %d, steps unlike their run: %d; want 2 and 0", stepClients, mismatched)
	}
	if steps, lerr := repo.ListSteps(client1, f.OrgA, run.ID); lerr != nil || len(steps) != 0 {
		t.Errorf("steps of the client-2 run for client 1: %+v %v, want none", steps, lerr)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, ci2, f.Client1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRun(client1, f.OrgA, run.ID)
	if err != nil || len(got.Steps) != 2 {
		t.Fatalf("run after the move of its CI to client 1: %+v %v, want visible with both steps", got, err)
	}
}

// TestRunWithoutCIOnlyForOrgWidePrincipal covers WP-1093 AK1 (E-09): a run
// an org-wide principal starts without CI has neither client nor site and
// is visible to org-wide principals only; a principal restricted to client
// 1 starts its runs without CI within its client.
func TestRunWithoutCIOnlyForOrgWidePrincipal(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	repo := workflow.NewPGRepository(f.App)
	exec := workflow.NewExecutor(repo, nil, ci.NewPGRepository(f.App), nil, nil)
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: f.User})
	def := &workflow.Definition{OrganizationID: f.OrgA, Name: "wf-no-ci-scope", Trigger: workflow.JSONMap{"type": "manual"},
		Actions: []workflow.JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(org, def); err != nil {
		t.Fatal(err)
	}
	orgRun, err := exec.Trigger(org, f.OrgA, def, "manual", workflow.JSONMap{})
	if err != nil || orgRun.Status != workflow.StatusSucceeded {
		t.Fatalf("org-wide run: %+v %v", orgRun, err)
	}
	var client, site *string
	if err = f.Admin.QueryRow(bg, `SELECT client_id::text, site_id::text FROM workflow_run WHERE id = $1`, orgRun.ID).Scan(&client, &site); err != nil {
		t.Fatal(err)
	}
	if client != nil || site != nil {
		t.Errorf("org-wide run without CI: client %v site %v, want none", client, site)
	}
	if _, err = repo.GetRun(client1, f.OrgA, orgRun.ID); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("org-wide run for client 1: %v, want ErrRunNotFound", err)
	}
	if got, gerr := repo.GetRun(org, f.OrgA, orgRun.ID); gerr != nil || got.ID != orgRun.ID {
		t.Errorf("org-wide run org-wide: %+v %v", got, gerr)
	}
	own, err := exec.Trigger(client1, f.OrgA, def, "manual", workflow.JSONMap{})
	if err != nil || own.Status != workflow.StatusSucceeded {
		t.Fatalf("run of client 1 without CI: %+v %v", own, err)
	}
	if err = f.Admin.QueryRow(bg, `SELECT client_id::text FROM workflow_run WHERE id = $1`, own.ID).Scan(&client); err != nil || client == nil || *client != f.Client1 {
		t.Errorf("run of client 1 without CI: client %v %v, want client 1", client, err)
	}
	// Its events carry the client of the run row, so webhook scopes of
	// client 1 receive them; those of the org-wide run carry none.
	var evClient string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(data->>'client_id', '') FROM event_outbox WHERE entity_id = $1 AND type = 'workflow.run_succeeded'`,
		own.ID).Scan(&evClient); err != nil || evClient != f.Client1 {
		t.Errorf("event of the client-1 run: client %q %v, want client 1", evClient, err)
	}
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(data->>'client_id', '') FROM event_outbox WHERE entity_id = $1 AND type = 'workflow.run_succeeded'`,
		orgRun.ID).Scan(&evClient); err != nil || evClient != "" {
		t.Errorf("event of the org-wide run: client %q %v, want none", evClient, err)
	}
	list, _, err := repo.ListRuns(client1, f.OrgA, &workflow.RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Errorf("runs of client 1: %+v %v, want only its own", list, err)
	}
}
