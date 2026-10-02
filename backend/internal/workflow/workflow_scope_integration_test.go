package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
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
