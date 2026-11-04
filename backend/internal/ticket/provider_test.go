package ticket_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// The tests of this file cover TKT-03 [O] (WP-293, CH17, E-47): the ticket
// provider interface with the internal module as reference provider, on
// the memory repository.

var _ ticket.Provider = (*ticket.InternalProvider)(nil)

// recordingHooks records the SLA hook calls per name.
type recordingHooks struct {
	mu    sync.Mutex
	calls map[string]int
}

func (h *recordingHooks) add(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.calls == nil {
		h.calls = map[string]int{}
	}
	h.calls[name]++
}

func (h *recordingHooks) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[name]
}

func (h *recordingHooks) ApplyForTicket(context.Context, string, *ticket.Ticket) error {
	h.add("apply")
	return nil
}

func (h *recordingHooks) MarkFirstResponse(context.Context, string, string, time.Time) error {
	h.add("first_response")
	return nil
}

func (h *recordingHooks) MarkResolved(context.Context, string, string, time.Time) error {
	h.add("resolved")
	return nil
}

func (h *recordingHooks) ReapplyForTicket(context.Context, string, *ticket.Ticket) error {
	h.add("reapply")
	return nil
}

func (h *recordingHooks) MarkReopened(context.Context, string, string, time.Time) error {
	h.add("reopened")
	return nil
}

const (
	providerOrg  = "00000000-0000-4000-8000-0000000000c1"
	providerUser = "00000000-0000-4000-8000-0000000000c2"
)

// memoryProvider returns the internal provider over the service and memory
// repository, the repository and the recording SLA hooks.
func memoryProvider() (context.Context, *ticket.InternalProvider, *ticket.MemoryRepository, *recordingHooks) {
	repo := ticket.NewMemoryRepository()
	hooks := &recordingHooks{}
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: providerOrg, UserID: providerUser})
	return ctx, ticket.NewInternalProvider(ticket.NewService(repo, hooks), nil), repo, hooks
}

func providerTicket(title string) *ticket.Ticket {
	return &ticket.Ticket{OrganizationID: providerOrg, Title: title, ReporterID: providerUser}
}

func TestInternalProviderCreate(t *testing.T) {
	ctx, p, repo, hooks := memoryProvider()
	tk := providerTicket("Drucker defekt")
	ref := &ticket.ExternalRef{Provider: "jira", Ref: " OPS-1 "}
	if err := p.Create(ctx, tk, ref); err != nil {
		t.Fatal(err)
	}
	// The service applied the defaults and the SLA hook.
	if tk.Status != ticket.StatusNew || tk.Priority != ticket.DefaultPriority || tk.Category != ticket.DefaultCategory {
		t.Errorf("defaults: status %q priority %q category %q", tk.Status, tk.Priority, tk.Category)
	}
	if hooks.count("apply") != 1 {
		t.Errorf("ApplyForTicket calls: %d, want 1", hooks.count("apply"))
	}
	got, err := repo.GetByExternalRef(ctx, providerOrg, "jira", "OPS-1")
	if err != nil || got.ID != tk.ID || got.ExternalProvider != "jira" || got.ExternalRef != "OPS-1" {
		t.Fatalf("GetByExternalRef: %+v, %v", got, err)
	}
	// Another organization does not see the reference.
	if _, err = repo.GetByExternalRef(ctx, "00000000-0000-4000-8000-0000000000c9", "jira", "OPS-1"); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("reference of another organization: %v", err)
	}

	// A reference taken in the organization is a conflict.
	var conflict *api.ConflictError
	if err = p.Create(ctx, providerTicket("Dublette"), &ticket.ExternalRef{Provider: "jira", Ref: "OPS-1"}); !errors.As(err, &conflict) || conflict.Field != "external_ref" {
		t.Fatalf("duplicate reference: %v", err)
	}
	// The same ref of another provider is free.
	if err = p.Create(ctx, providerTicket("Anderer Provider"), &ticket.ExternalRef{Provider: "servicenow", Ref: "OPS-1"}); err != nil {
		t.Fatalf("same ref of another provider: %v", err)
	}
	// A reference without its id is refused.
	var fieldErr *api.FieldError
	if err = p.Create(ctx, providerTicket("Halb"), &ticket.ExternalRef{Provider: "jira"}); !errors.As(err, &fieldErr) {
		t.Fatalf("reference without ref: %v", err)
	}
	// Without a reference the ticket carries none.
	plain := providerTicket("Ohne Referenz")
	if err = p.Create(ctx, plain, nil); err != nil || plain.ExternalRef != "" || plain.ExternalProvider != "" {
		t.Fatalf("creation without reference: %+v, %v", plain, err)
	}
}

func TestInternalProviderUpdateAndStatusSync(t *testing.T) {
	ctx, p, repo, hooks := memoryProvider()
	tk := providerTicket("Netz langsam")
	ref := ticket.ExternalRef{Provider: "jira", Ref: "NET-7"}
	if err := p.Create(ctx, tk, &ref); err != nil {
		t.Fatal(err)
	}

	// Update goes through the service: a new priority chooses the SLA again.
	high := "high"
	item, err := p.Update(ctx, providerOrg, tk.ID, &ticket.UpdateRequest{Priority: &high})
	if err != nil || item.Priority != high {
		t.Fatalf("update: %+v, %v", item, err)
	}
	if hooks.count("reapply") != 1 {
		t.Errorf("ReapplyForTicket calls: %d, want 1", hooks.count("reapply"))
	}
	// A priority off the list is a 422 field error.
	bad := "urgent"
	var fieldErr *api.FieldError
	if _, err = p.Update(ctx, providerOrg, tk.ID, &ticket.UpdateRequest{Priority: &bad}); !errors.As(err, &fieldErr) || fieldErr.Field != "priority" {
		t.Fatalf("invalid priority: %v", err)
	}

	// new → resolved is no transition of the state machine.
	if _, err = p.StatusSync(ctx, providerOrg, ref, ticket.StatusResolved); !errors.Is(err, ticket.ErrInvalidTransition) {
		t.Fatalf("invalid transition: %v", err)
	}
	if hooks.count("first_response") != 0 || hooks.count("resolved") != 0 {
		t.Fatalf("hooks after a refused transition: %v", hooks.calls)
	}
	// An unknown reference is ErrNotFound.
	if _, err = p.StatusSync(ctx, providerOrg, ticket.ExternalRef{Provider: "jira", Ref: "NET-404"}, ticket.StatusOpen); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("unknown reference: %v", err)
	}

	// new → open → in_progress → resolved over the state machine.
	for _, status := range []string{ticket.StatusOpen, ticket.StatusInProgress} {
		if _, err = p.StatusSync(ctx, providerOrg, ref, status); err != nil {
			t.Fatalf("status sync to %s: %v", status, err)
		}
	}
	firstBefore := hooks.count("first_response")
	item, err = p.StatusSync(ctx, providerOrg, ref, ticket.StatusResolved)
	if err != nil || item.Status != ticket.StatusResolved || item.ResolvedAt == "" {
		t.Fatalf("status sync to resolved: %+v, %v", item, err)
	}
	if got := hooks.count("first_response") - firstBefore; got != 1 {
		t.Errorf("MarkFirstResponse calls of resolved: %d, want 1", got)
	}
	if hooks.count("resolved") != 1 {
		t.Errorf("MarkResolved calls: %d, want 1", hooks.count("resolved"))
	}
	stored, err := repo.GetByID(ctx, providerOrg, tk.ID)
	if err != nil || stored.Status != ticket.StatusResolved {
		t.Fatalf("stored ticket: %+v, %v", stored, err)
	}
}

func TestInternalProviderComment(t *testing.T) {
	ctx, p, repo, hooks := memoryProvider()
	tk := providerTicket("Mail-Problem")
	if err := p.Create(ctx, tk, &ticket.ExternalRef{Provider: "jira", Ref: "MAIL-1"}); err != nil {
		t.Fatal(err)
	}
	ref := &ticket.ExternalRef{Provider: "jira", Ref: "comment-10001"}
	first, err := p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: tk.ID, AuthorID: providerUser, Content: "Bitte neu starten"}, ref)
	if err != nil || first.ID == "" || first.ExternalRef != "comment-10001" || first.AuthorType != ticket.AuthorUser {
		t.Fatalf("comment: %+v, %v", first, err)
	}
	// A public user comment is the first response (service, WP-766).
	if hooks.count("first_response") != 1 {
		t.Errorf("MarkFirstResponse calls: %d, want 1", hooks.count("first_response"))
	}
	// The same foreign id again returns the stored comment.
	again, err := p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: tk.ID, AuthorID: providerUser, Content: "Bitte neu starten"}, ref)
	if err != nil || again.ID != first.ID {
		t.Fatalf("repeated comment: %+v, %v", again, err)
	}
	comments, total, err := repo.ListComments(ctx, providerOrg, tk.ID, api.PaginationParams{Limit: 50})
	if err != nil || total != 1 || len(comments) != 1 {
		t.Fatalf("comments after the repetition: %d %+v %v", total, comments, err)
	}
	// The foreign id on another ticket is a conflict.
	other := providerTicket("Anderes Ticket")
	if err = p.Create(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
	var conflict *api.ConflictError
	if _, err = p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: other.ID, AuthorID: providerUser, Content: "x"}, ref); !errors.As(err, &conflict) {
		t.Fatalf("foreign id on another ticket: %v", err)
	}
	// Without a foreign id each call adds a comment; an empty one is 400.
	if _, err = p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: tk.ID, AuthorID: providerUser, Content: "Zweiter"}, nil); err != nil {
		t.Fatal(err)
	}
	var fieldErr *api.FieldError
	if _, err = p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: tk.ID, AuthorID: providerUser}, nil); !errors.As(err, &fieldErr) {
		t.Fatalf("empty comment: %v", err)
	}
	if _, err = p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: "00000000-0000-4000-8000-000000009999", Content: "x"}, nil); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("comment on an unknown ticket: %v", err)
	}
	got, err := repo.GetCommentByExternalRef(ctx, providerOrg, "jira", "comment-10001")
	if err != nil || got.ID != first.ID {
		t.Fatalf("GetCommentByExternalRef: %+v, %v", got, err)
	}
}

func TestInternalProviderLink(t *testing.T) {
	ctx, p, repo, _ := memoryProvider()
	tk := providerTicket("Rack voll")
	if err := p.Create(ctx, tk, nil); err != nil {
		t.Fatal(err)
	}
	target := "00000000-0000-4000-a000-000000000001"
	link := &ticket.Link{OrganizationID: providerOrg, TicketID: tk.ID, EntityType: ticket.LinkCI, EntityID: target}
	if err := p.Link(ctx, link); err != nil || link.ID == "" {
		t.Fatalf("link: %+v, %v", link, err)
	}
	links, total, err := repo.ListLinks(ctx, providerOrg, tk.ID, api.PaginationParams{Limit: 50})
	if err != nil || total != 1 || links[0].EntityID != target {
		t.Fatalf("links: %d %+v %v", total, links, err)
	}
	if err = p.Link(ctx, &ticket.Link{OrganizationID: providerOrg, TicketID: tk.ID, EntityType: ticket.LinkCI, EntityID: target}); !errors.Is(err, ticket.ErrLinkExists) {
		t.Fatalf("second link: %v", err)
	}
	var fieldErr *api.FieldError
	if err = p.Link(ctx, &ticket.Link{OrganizationID: providerOrg, TicketID: tk.ID, EntityType: "server", EntityID: target}); !errors.As(err, &fieldErr) {
		t.Fatalf("unknown entity type: %v", err)
	}
	if err = p.Link(ctx, &ticket.Link{OrganizationID: providerOrg, TicketID: "00000000-0000-4000-8000-000000009999", EntityType: ticket.LinkCI, EntityID: target}); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("link of an unknown ticket: %v", err)
	}
}

func TestMemoryRepositoryExternalRefs(t *testing.T) {
	ctx, p, repo, _ := memoryProvider()
	a, b := providerTicket("A"), providerTicket("B")
	for _, tk := range []*ticket.Ticket{a, b} {
		if err := p.Create(ctx, tk, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetExternalRef(ctx, providerOrg, a.ID, "jira", "X-1"); err != nil {
		t.Fatal(err)
	}
	var conflict *api.ConflictError
	if err := repo.SetExternalRef(ctx, providerOrg, b.ID, "jira", "X-1"); !errors.As(err, &conflict) {
		t.Fatalf("taken reference: %v", err)
	}
	var fieldErr *api.FieldError
	if err := repo.SetExternalRef(ctx, providerOrg, b.ID, "jira", ""); !errors.As(err, &fieldErr) {
		t.Fatalf("half reference: %v", err)
	}
	if err := repo.SetExternalRef(ctx, providerOrg, "00000000-0000-4000-8000-000000009999", "jira", "X-2"); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("unknown ticket: %v", err)
	}
	c, err := p.Comment(ctx, &ticket.Comment{OrganizationID: providerOrg, TicketID: a.ID, AuthorID: providerUser, Content: "c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SetCommentExternalRef(ctx, providerOrg, c.ID, "jira", "C-1"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetCommentByExternalRef(ctx, providerOrg, "jira", "C-1")
	if err != nil || got.ID != c.ID {
		t.Fatalf("comment by reference: %+v, %v", got, err)
	}
	// Clearing the reference frees it.
	if err = repo.SetExternalRef(ctx, providerOrg, a.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = repo.SetExternalRef(ctx, providerOrg, b.ID, "jira", "X-1"); err != nil {
		t.Fatalf("freed reference: %v", err)
	}
}
