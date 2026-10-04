package sla_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// TestSLARepositoryClientScope runs the SLA repository with the principal's
// tenant scope (TEN-06, WP-020): a principal restricted to client 1 neither
// sees a client-2 SLA policy nor the SLA state of a ticket about a client-2
// CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSLARepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "39")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "sla-c2")
	user := f.AppUser(t, f.OrgA, "reporter")
	orgCtx := f.OrgCtx(f.OrgA)

	repo := sla.NewPGRepository(f.App)
	own := &sla.Policy{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "c1", Priority: "high", ResponseTargetMinutes: 30, ResolutionTargetMinutes: 240}
	foreign := &sla.Policy{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "c2", Priority: "high", ResponseTargetMinutes: 30, ResolutionTargetMinutes: 240}
	for _, p := range []*sla.Policy{own, foreign} {
		if err := repo.CreatePolicy(orgCtx, p); err != nil {
			t.Fatalf("org-wide policy %s: %v", p.Name, err)
		}
	}
	tk := &ticket.Ticket{OrganizationID: f.OrgA, Title: "c2", Status: "open", Priority: "high", Category: "incident", ReporterID: user, RelatedCIID: foreignCI}
	if err := ticket.NewPGRepository(f.App).Create(orgCtx, tk); err != nil {
		t.Fatalf("org-wide ticket: %v", err)
	}
	if _, err := repo.ApplyForTicket(orgCtx, f.OrgA, tk, foreign.ID); err != nil {
		t.Fatalf("org-wide SLA state: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	policies, _, err := repo.ListPolicies(ctx, f.OrgA, "", "", page)
	if err != nil || len(policies) != 1 || policies[0].ID != own.ID {
		t.Fatalf("client-1 policies: %+v err=%v, want only the own one", policies, err)
	}
	if _, err = repo.GetForTicket(ctx, f.OrgA, tk.ID); err == nil {
		t.Fatal("client-1 principal read the SLA state of a ticket about a client-2 CI")
	}
	breaches, total, err := repo.ListBreaches(ctx, f.OrgA, sla.BreachFilter{}, page)
	if err != nil || total != 0 || len(breaches) != 0 {
		t.Fatalf("client-1 SLA states: total=%d err=%v, want none", total, err)
	}
	if _, _, err = repo.ListPolicies(context.Background(), f.OrgA, "", "", page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
