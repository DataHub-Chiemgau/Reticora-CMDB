package ticket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/jackc/pgx/v5/pgconn"
)

// The tests of this file cover the foreign references of tickets and ticket
// comments and the internal ticket provider on PostgreSQL (TKT-03 [O],
// WP-293, migration 000239). They run only with TEST_DATABASE_URL (see
// scopetest.Seed).

// extTitle returns a pointer to title.
func extTitle(title string) *string { return &title }

// sqlState returns the SQLSTATE of err, "" for another error.
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestTicketExternalRefPG(t *testing.T) {
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	repo := ticket.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	p := ticket.NewInternalProvider(ticket.NewService(repo, nil), f.App)
	userA := f.AppUser(t, f.OrgA, "ext-a")
	userB := f.AppUser(t, f.OrgB, "ext-b")
	ctxA := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userA})
	ctxB := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: userB})

	// Saving and reading the reference of a ticket and a comment.
	tk := &ticket.Ticket{OrganizationID: f.OrgA, Title: "Extern", ReporterID: userA}
	if err := p.Create(ctxA, tk, &ticket.ExternalRef{Provider: "jira", Ref: "EXT-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctxA, f.OrgA, tk.ID)
	if err != nil || got.ExternalProvider != "jira" || got.ExternalRef != "EXT-1" {
		t.Fatalf("GetByID: %+v, %v", got, err)
	}
	if got, err = repo.GetByExternalRef(ctxA, f.OrgA, "jira", "EXT-1"); err != nil || got.ID != tk.ID {
		t.Fatalf("GetByExternalRef: %+v, %v", got, err)
	}
	c, err := p.Comment(ctxA, &ticket.Comment{OrganizationID: f.OrgA, TicketID: tk.ID, AuthorID: userA, Content: "extern"},
		&ticket.ExternalRef{Provider: "jira", Ref: "EXT-1-c1"})
	if err != nil {
		t.Fatal(err)
	}
	comments, _, err := repo.ListComments(ctxA, f.OrgA, tk.ID, api.PaginationParams{Limit: 10})
	if err != nil || len(comments) != 1 || comments[0].ExternalProvider != "jira" || comments[0].ExternalRef != "EXT-1-c1" {
		t.Fatalf("ListComments: %+v, %v", comments, err)
	}
	// The repetition returns the stored comment.
	again, err := p.Comment(ctxA, &ticket.Comment{OrganizationID: f.OrgA, TicketID: tk.ID, AuthorID: userA, Content: "extern"},
		&ticket.ExternalRef{Provider: "jira", Ref: "EXT-1-c1"})
	if err != nil || again.ID != c.ID {
		t.Fatalf("repeated comment: %+v, %v", again, err)
	}

	// A duplicate (org, provider, ref) is 23505, reported as a conflict.
	_, err = f.Admin.Exec(bg, `INSERT INTO ticket (organization_id, title, reporter_id, external_provider, external_ref)
		VALUES ($1, 'dup', $2, 'jira', 'EXT-1')`, f.OrgA, userA)
	if sqlState(err) != "23505" {
		t.Fatalf("duplicate ticket reference: %v", err)
	}
	var conflict *api.ConflictError
	if err = p.Create(ctxA, &ticket.Ticket{OrganizationID: f.OrgA, Title: "Dublette", ReporterID: userA},
		&ticket.ExternalRef{Provider: "jira", Ref: "EXT-1"}); !errors.As(err, &conflict) || conflict.Field != "external_ref" {
		t.Fatalf("duplicate through the provider: %v", err)
	}
	if err = repo.AddComment(ctxA, &ticket.Comment{OrganizationID: f.OrgA, TicketID: tk.ID, AuthorID: userA, Content: "dup",
		ExternalProvider: "jira", ExternalRef: "EXT-1-c1"}); !errors.As(err, &conflict) {
		t.Fatalf("duplicate comment reference: %v", err)
	}
	other := &ticket.Ticket{OrganizationID: f.OrgA, Title: "Anderes", ReporterID: userA}
	if err = p.Create(ctxA, other, nil); err != nil {
		t.Fatal(err)
	}
	if err = repo.SetExternalRef(ctxA, f.OrgA, other.ID, "jira", "EXT-1"); !errors.As(err, &conflict) {
		t.Fatalf("SetExternalRef with a taken reference: %v", err)
	}
	if err = repo.SetExternalRef(ctxA, f.OrgA, other.ID, "jira", "EXT-2"); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.GetByExternalRef(ctxA, f.OrgA, "jira", "EXT-2"); err != nil || got.ID != other.ID {
		t.Fatalf("reference set later: %+v, %v", got, err)
	}

	// The same reference in another organization is allowed, and the lookup
	// does not cross organizations.
	tkB := &ticket.Ticket{OrganizationID: f.OrgB, Title: "Extern B", ReporterID: userB}
	if err = p.Create(ctxB, tkB, &ticket.ExternalRef{Provider: "jira", Ref: "EXT-1"}); err != nil {
		t.Fatalf("same reference in organization B: %v", err)
	}
	if got, err = repo.GetByExternalRef(ctxA, f.OrgA, "jira", "EXT-1"); err != nil || got.ID != tk.ID {
		t.Fatalf("lookup in organization A: %+v, %v", got, err)
	}
	if got, err = repo.GetByExternalRef(ctxB, f.OrgB, "jira", "EXT-2"); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("lookup of a reference of organization A from B: %+v, %v", got, err)
	}

	// Only one of the two columns violates the CHECK (23514).
	_, err = f.Admin.Exec(bg, `INSERT INTO ticket (organization_id, title, reporter_id, external_provider)
		VALUES ($1, 'half', $2, 'jira')`, f.OrgA, userA)
	if sqlState(err) != "23514" {
		t.Fatalf("ticket with provider only: %v", err)
	}
	_, err = f.Admin.Exec(bg, `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content, external_ref)
		VALUES ($1, $2, $3, 'half', 'x')`, f.OrgA, tk.ID, userA)
	if sqlState(err) != "23514" {
		t.Fatalf("comment with ref only: %v", err)
	}
}

func TestTicketExternalRefScope(t *testing.T) {
	f := scopetest.Seed(t, "38")
	repo := ticket.NewPGRepository(f.App)
	p := ticket.NewInternalProvider(ticket.NewService(repo, nil), f.App)
	user := f.AppUser(t, f.OrgA, "ext-scope")
	ctxA := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})

	// A ticket about a CI of client 2 is outside the scope of client 1.
	foreignCI := f.CI(t, f.OrgA, f.Client2, "ext-c2")
	foreign := &ticket.Ticket{OrganizationID: f.OrgA, Title: "Client 2", ReporterID: user, RelatedCIID: foreignCI}
	if err := p.Create(ctxA, foreign, &ticket.ExternalRef{Provider: "jira", Ref: "SCOPE-2"}); err != nil {
		t.Fatal(err)
	}
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	if got, err := repo.GetByExternalRef(client1, f.OrgA, "jira", "SCOPE-2"); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("lookup outside the client scope: %+v, %v", got, err)
	}
	if _, err := p.StatusSync(client1, f.OrgA, ticket.ExternalRef{Provider: "jira", Ref: "SCOPE-2"}, ticket.StatusOpen); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("status sync outside the client scope: %v", err)
	}
	if got, err := repo.GetByExternalRef(ctxA, f.OrgA, "jira", "SCOPE-2"); err != nil || got.ID != foreign.ID {
		t.Fatalf("org-wide lookup: %+v, %v", got, err)
	}

	// Without a scope and with another organization the provider refuses.
	noScope := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	if err := p.Create(noScope, &ticket.Ticket{OrganizationID: f.OrgA, Title: "x", ReporterID: user}, nil); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("create without scope: %v", err)
	}
	if _, err := p.StatusSync(noScope, f.OrgA, ticket.ExternalRef{Provider: "jira", Ref: "SCOPE-2"}, ticket.StatusOpen); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("status sync without scope: %v", err)
	}
	if err := p.Create(ctxA, &ticket.Ticket{OrganizationID: f.OrgB, Title: "x", ReporterID: user}, nil); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("create in another organization: %v", err)
	}
	if _, err := p.StatusSync(ctxA, f.OrgB, ticket.ExternalRef{Provider: "jira", Ref: "SCOPE-2"}, ticket.StatusOpen); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("status sync in another organization: %v", err)
	}
}

func TestTicketProviderAudit(t *testing.T) {
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	repo := ticket.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	p := ticket.NewInternalProvider(ticket.NewService(repo, nil), f.App)
	user := f.AppUser(t, f.OrgA, "ext-audit")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	count := func(action, ticketID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = $2
			AND resource_type = 'ticket' AND resource_id = $3`, f.OrgA, action, ticketID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	tk := &ticket.Ticket{OrganizationID: f.OrgA, Title: "Audit", ReporterID: user}
	if err := p.Create(ctx, tk, &ticket.ExternalRef{Provider: "jira", Ref: "AUD-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update(ctx, f.OrgA, tk.ID, &ticket.UpdateRequest{Title: extTitle("Audit 2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Comment(ctx, &ticket.Comment{OrganizationID: f.OrgA, TicketID: tk.ID, AuthorID: user, Content: "geheim"},
		&ticket.ExternalRef{Provider: "jira", Ref: "AUD-1-c"}); err != nil {
		t.Fatal(err)
	}
	ciID := f.CI(t, f.OrgA, f.Client1, "ext-audit-ci")
	if err := p.Link(ctx, &ticket.Link{OrganizationID: f.OrgA, TicketID: tk.ID, EntityType: ticket.LinkCI, EntityID: ciID}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StatusSync(ctx, f.OrgA, ticket.ExternalRef{Provider: "jira", Ref: "AUD-1"}, ticket.StatusOpen); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "update", "comment", "link", "status_sync"} {
		if n := count("ticket.provider."+op, tk.ID); n != 1 {
			t.Errorf("audit entries of %s: %d, want 1", op, n)
		}
	}
	// The entries name the reference, no contents.
	var changes map[string]any
	if err := f.Admin.QueryRow(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = 'ticket.provider.comment'
		AND resource_id = $2`, f.OrgA, tk.ID).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes["external_provider"] != "jira" || changes["external_ref"] != "AUD-1-c" {
		t.Errorf("audit changes: %+v", changes)
	}
	for _, v := range changes {
		if v == "geheim" {
			t.Errorf("audit entry carries the comment content: %+v", changes)
		}
	}
	// The idempotent repetition writes nothing.
	if _, err := p.Comment(ctx, &ticket.Comment{OrganizationID: f.OrgA, TicketID: tk.ID, AuthorID: user, Content: "geheim"},
		&ticket.ExternalRef{Provider: "jira", Ref: "AUD-1-c"}); err != nil {
		t.Fatal(err)
	}
	if n := count("ticket.provider.comment", tk.ID); n != 1 {
		t.Errorf("audit entries after the repetition: %d, want 1", n)
	}

	// With the request context of the route audit the provider writes none.
	routed := audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp293-route"})
	if _, err := p.Update(routed, f.OrgA, tk.ID, &ticket.UpdateRequest{Title: extTitle("Audit 3")}); err != nil {
		t.Fatal(err)
	}
	if n := count("ticket.provider.update", tk.ID); n != 1 {
		t.Errorf("audit entries of update with request context: %d, want 1", n)
	}

	// A failing operation leaves no entry.
	if _, err := p.StatusSync(ctx, f.OrgA, ticket.ExternalRef{Provider: "jira", Ref: "AUD-1"}, ticket.StatusNew); !errors.Is(err, ticket.ErrInvalidTransition) {
		t.Fatalf("invalid transition: %v", err)
	}
	if n := count("ticket.provider.status_sync", tk.ID); n != 1 {
		t.Errorf("audit entries after a refused transition: %d, want 1", n)
	}
}
