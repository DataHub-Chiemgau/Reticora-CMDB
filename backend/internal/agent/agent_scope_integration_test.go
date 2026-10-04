package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestAgentRepositoryClientScope runs the endpoint agent repository with the
// principal's tenant scope (TEN-06, WP-021): a principal restricted to client
// 1 neither sees nor controls the agent of a client-2 CI and cannot bind an
// agent to a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAgentRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "3d")
	ownCI := f.CI(t, f.OrgA, f.Client1, "agent-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "agent-c2")
	repo := agent.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &agent.Agent{OrganizationID: f.OrgA, AgentID: "agent-3d-own", Hostname: "own", Status: "online"}
	foreign := &agent.Agent{OrganizationID: f.OrgA, AgentID: "agent-3d-foreign", Hostname: "foreign", Status: "online"}
	for _, a := range []*agent.Agent{own, foreign} {
		if err := repo.Register(orgCtx, a); err != nil {
			t.Fatalf("org-wide agent %s: %v", a.Hostname, err)
		}
	}
	if err := repo.SetCI(orgCtx, f.OrgA, own.AgentID, ownCI); err != nil {
		t.Fatalf("bind own agent: %v", err)
	}
	if err := repo.SetCI(orgCtx, f.OrgA, foreign.AgentID, foreignCI); err != nil {
		t.Fatalf("bind foreign agent: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, total, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].AgentID != own.AgentID {
		t.Fatalf("client-1 agents: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.SetStatus(ctx, f.OrgA, foreign.ID, "disabled"); err == nil {
		t.Fatal("client-1 principal disabled the agent of a client-2 CI")
	}
	if err = repo.SetCI(ctx, f.OrgA, own.AgentID, foreignCI); err == nil {
		t.Fatal("client-1 principal bound an agent to a client-2 CI")
	}
	var status, ci string
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT status, (SELECT ci_id::text FROM endpoint_agent WHERE id = $2) FROM endpoint_agent WHERE id = $1`, foreign.ID, own.ID).Scan(&status, &ci); err != nil {
		t.Fatalf("read agents: %v", err)
	}
	if status != "online" || ci != ownCI {
		t.Fatalf("agents changed: foreign status=%q own ci=%q", status, ci)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
