package agent

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestAgentRegisterHeartbeatKillSwitch(t *testing.T) {
	repo := NewMemoryRepository()
	a := &Agent{OrganizationID: "org-1", AgentID: "host-1", Hostname: "srv-01", OS: "linux"}
	if err := repo.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if a.Policy.IntervalSeconds != 60 || !a.Policy.MetricsEnabled {
		t.Fatalf("expected default policy, got %+v", a.Policy)
	}
	// re-enroll refreshes
	if err := repo.Register(context.Background(), &Agent{OrganizationID: "org-1", AgentID: "host-1", Hostname: "srv-01", Version: "1.1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByAgentID(context.Background(), "org-1", "host-1")
	if got.Version != "1.1" {
		t.Fatalf("expected re-enroll to refresh version, got %s", got.Version)
	}
	// kill-switch
	if _, err := repo.SetStatus(context.Background(), "org-1", got.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByAgentID(context.Background(), "org-1", "host-1")
	if got.Status != "disabled" {
		t.Fatalf("expected disabled, got %s", got.Status)
	}
	// heartbeat re-onlines
	if err := repo.Heartbeat(context.Background(), "org-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	// policy update
	interval := 120
	if _, err := repo.UpdatePolicy(context.Background(), "org-1", got.ID, UpdatePolicyRequest{IntervalSeconds: &interval}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByAgentID(context.Background(), "org-1", "host-1")
	if got.Policy.IntervalSeconds != 120 {
		t.Fatalf("expected 120s, got %d", got.Policy.IntervalSeconds)
	}
	// list
	_, total, _ := repo.List(context.Background(), "org-1", api.PaginationParams{Limit: 10})
	if total != 1 {
		t.Fatalf("expected 1 agent, got %d", total)
	}
}
