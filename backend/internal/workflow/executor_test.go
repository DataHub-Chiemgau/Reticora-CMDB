package workflow

import (
	"context"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"testing"
)

func TestExecutorApprovalFlow(t *testing.T) {
	repo := NewMemoryRepository()
	tickets := ticket.NewMemoryRepository()
	forms := form.NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	exec := NewExecutor(repo, tickets, cis, forms, nil)
	def := &Definition{OrganizationID: "org-1", Name: "wf", Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "require_approval"}, {"type": "create_ticket", "title": "Do it"}}, Active: true}
	if err := repo.CreateDefinition(def); err != nil {
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
	list, total, err := tickets.List("org-1", ticket.FilterParams{}, api.PaginationParams{Limit: 10})
	_ = list
	_ = total
	_ = err
}
