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

// TestWorkflowRunCIScope covers WP-1089 AK1/AK6: a run on a CI carries
// the CI; until WP-1093 its scope follows the CI (rls.DerivedScopeTables,
// runVisible). A principal restricted to client 1 does not see a run on a
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
