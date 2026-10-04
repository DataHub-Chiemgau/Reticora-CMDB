package ticket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// TestTicketRepositoryClientScope runs the ticket repository with the
// principal's tenant scope (TEN-06, WP-020): a principal restricted to client
// 1 neither sees nor comments a ticket about a client-2 CI and cannot open
// one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTicketRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "38")
	ownCI := f.CI(t, f.OrgA, f.Client1, "ticket-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "ticket-c2")
	user := f.AppUser(t, f.OrgA, "reporter")

	repo := ticket.NewPGRepository(f.App)
	mk := func(ciID string) *ticket.Ticket {
		return &ticket.Ticket{OrganizationID: f.OrgA, Title: "Ausfall", Status: "open", Priority: "high", Category: "incident", ReporterID: user, RelatedCIID: ciID}
	}
	foreign := mk(foreignCI)
	if err := repo.Create(f.OrgCtx(f.OrgA), foreign); err != nil {
		t.Fatalf("org-wide ticket: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, mk(foreignCI)); err == nil {
		t.Fatal("client-1 principal opened a ticket about a client-2 CI")
	}
	own := mk(ownCI)
	if err := repo.Create(ctx, own); err != nil {
		t.Fatalf("client-1 ticket about own CI: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, ticket.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 tickets: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a ticket about a client-2 CI")
	}
	if err = repo.AddComment(ctx, &ticket.Comment{OrganizationID: f.OrgA, TicketID: foreign.ID, AuthorID: user, Content: "intruder"}); err == nil {
		t.Fatal("client-1 principal commented a ticket about a client-2 CI")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal deleted a ticket about a client-2 CI")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ticket WHERE id = $1`, foreign.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 ticket: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, ticket.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
