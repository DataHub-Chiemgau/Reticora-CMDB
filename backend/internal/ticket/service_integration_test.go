package ticket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The tests of this file cover TKT-01 of WP-766: ticket.Service as the only
// way tickets and comments are created, with the PostgreSQL repository
// (TEST_DATABASE_URL, see scopetest.Seed) and the memory repository.

// countingHooks counts the SLA hook calls and passes them on; fail makes
// ApplyForTicket fail after the inner hook wrote its state.
type countingHooks struct {
	inner          ticket.SLAHooks
	fail           atomic.Bool
	applied        atomic.Int32
	firstResponses atomic.Int32
}

func (h *countingHooks) ApplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket) error {
	h.applied.Add(1)
	if err := h.inner.ApplyForTicket(ctx, orgID, t); err != nil {
		return err
	}
	if h.fail.Load() {
		return errors.New("sla hook failed")
	}
	return nil
}

func (h *countingHooks) MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error {
	h.firstResponses.Add(1)
	return h.inner.MarkFirstResponse(ctx, orgID, ticketID, at)
}

func (h *countingHooks) MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return h.inner.MarkResolved(ctx, orgID, ticketID, at)
}

// serviceWorld is one backend of the service tests.
type serviceWorld struct {
	ctx   context.Context // org-wide request of organization A
	org   string
	user  string
	repo  ticket.Repository
	slas  sla.Repository
	hooks *countingHooks
	svc   *ticket.Service
	mux   chi.Router
	// target returns a new review item, a link target.
	target func(t *testing.T) string
	// env is the PostgreSQL fixture, nil for the memory repository.
	env *ticketEnv
}

func newServiceWorld(repo ticket.Repository, slas sla.Repository, ctx context.Context, org, user string) *serviceWorld {
	w := &serviceWorld{ctx: ctx, org: org, user: user, repo: repo, slas: slas}
	w.hooks = &countingHooks{inner: sla.TicketHooks{Repo: slas}}
	w.svc = ticket.NewService(repo, w.hooks)
	w.mux = chi.NewRouter()
	ticket.NewHandler(w.svc).RegisterRoutes(w.mux)
	return w
}

// memoryWorld runs the service on the memory repositories.
func memoryWorld(t *testing.T) *serviceWorld {
	t.Helper()
	const org, user = "00000000-0000-4000-8000-0000000000aa", "00000000-0000-4000-8000-0000000000ab"
	slas := sla.NewMemoryRepository()
	if err := slas.CreatePolicy(context.Background(), &sla.Policy{OrganizationID: org, Name: "svc-medium", Priority: "medium",
		ResponseTargetMinutes: 30, ResolutionTargetMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: user})
	w := newServiceWorld(ticket.NewMemoryRepository(), slas, ctx, org, user)
	var n atomic.Int32
	w.target = func(*testing.T) string {
		return fmt.Sprintf("00000000-0000-4000-a000-%012d", n.Add(1))
	}
	return w
}

// pgWorld runs the service on the PostgreSQL repositories of organization A
// with an org-wide SLA policy for priority medium.
func pgWorld(t *testing.T) *serviceWorld {
	t.Helper()
	e := newTicketEnv(t)
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO sla (organization_id, name, priority, response_target_minutes, resolution_target_minutes)
		VALUES ($1, 'svc-medium', 'medium', 30, 60)`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
	w := newServiceWorld(e.repo, sla.NewPGRepository(e.f.App), ctx, e.f.OrgA, e.user)
	w.env = e
	ciID := e.f.CI(t, e.f.OrgA, e.f.Client1, "svc-candidate")
	w.target = func(t *testing.T) string {
		t.Helper()
		var id string
		if err := e.f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids)
			VALUES ($1, 'ambiguous_identity', '{}', ARRAY[$2::uuid]) RETURNING id::text`, e.f.OrgA, ciID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	return w
}

// worlds runs fn for the memory repository and, with TEST_DATABASE_URL, for
// PostgreSQL.
func worlds(t *testing.T, fn func(t *testing.T, w *serviceWorld)) {
	t.Run("memory", func(t *testing.T) { fn(t, memoryWorld(t)) })
	t.Run("postgresql", func(t *testing.T) { fn(t, pgWorld(t)) })
}

// count returns the number of tickets of the organization.
func (w *serviceWorld) count(t *testing.T) int {
	t.Helper()
	_, total, err := w.repo.List(w.ctx, w.org, &ticket.FilterParams{}, api.PaginationParams{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// request sends a request with a JSON body (nil: none) as the user.
func (w *serviceWorld) request(t *testing.T, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	return w.raw(t, method, url, raw)
}

// raw sends a request with a raw body as the user.
func (w *serviceWorld) raw(t *testing.T, method, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req.WithContext(w.ctx))
	return rec
}

func (w *serviceWorld) newTicket(title string) *ticket.Ticket {
	return &ticket.Ticket{OrganizationID: w.org, Title: title, ReporterID: w.user}
}

func reviewLink(id string) ticket.Link {
	return ticket.Link{EntityType: ticket.LinkReviewItem, EntityID: id}
}

func TestServiceCreateKeepsTheTicketContract(t *testing.T) {
	worlds(t, func(t *testing.T, w *serviceWorld) {
		// POST /api/v1/tickets: defaults, SLA assignment, 201.
		req := w.request(t, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Drucker"})
		if req.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", req.Code, req.Body)
		}
		var tk ticket.Ticket
		if err := json.Unmarshal(req.Body.Bytes(), &tk); err != nil {
			t.Fatal(err)
		}
		if tk.Priority != "medium" || tk.Category != "incident" || tk.Status != ticket.StatusNew || tk.TicketNumber < 1 {
			t.Fatalf("defaults: %+v", tk)
		}
		if _, err := w.slas.GetForTicket(w.ctx, w.org, tk.ID); err != nil {
			t.Fatalf("SLA of the new ticket: %v", err)
		}
		for body, want := range map[string]int{
			`{"description":"no title"}`:               http.StatusBadRequest,
			`{"title":"x","priority":"urgent"}`:        http.StatusUnprocessableEntity,
			`{"title":"Ohne Policy","priority":"low"}`: http.StatusCreated,
		} {
			if got := w.raw(t, http.MethodPost, "/api/v1/tickets", body).Code; got != want {
				t.Errorf("POST %s: %d, want %d", body, got, want)
			}
		}

		// A failing SLA hook leaves the ticket; in PostgreSQL its savepoint
		// takes the SLA state back.
		w.hooks.fail.Store(true)
		failed := w.newTicket("Hook schlägt fehl")
		if err := w.svc.Create(w.ctx, failed, nil, ticket.CreateOptions{}); err != nil {
			w.hooks.fail.Store(false)
			t.Fatalf("create with failing hook: %v", err)
		}
		w.hooks.fail.Store(false)
		if _, err := w.repo.GetByID(w.ctx, w.org, failed.ID); err != nil {
			t.Fatalf("ticket after failed hook: %v", err)
		}
		if w.env != nil {
			if _, err := w.slas.GetForTicket(w.ctx, w.org, failed.ID); !errors.Is(err, sla.ErrTicketSLANotFound) {
				t.Errorf("SLA state after failed hook: %v, want rolled back", err)
			}
		}
		if got := w.hooks.applied.Load(); got != 3 {
			t.Errorf("ApplyForTicket called %d times, want 3", got)
		}
	})
}

func TestServiceCreatesTicketAndLinksAtomically(t *testing.T) {
	worlds(t, func(t *testing.T, w *serviceWorld) {
		target := w.target(t)
		tk := w.newTicket("Mit Links")
		if err := w.svc.Create(w.ctx, tk, []ticket.Link{reviewLink(target)}, ticket.CreateOptions{}); err != nil {
			t.Fatalf("create with link: %v", err)
		}
		links, total, err := w.repo.ListLinks(w.ctx, w.org, tk.ID, api.PaginationParams{Limit: 10})
		if err != nil || total != 1 || links[0].EntityID != target {
			t.Fatalf("links: %+v total=%d err=%v", links, total, err)
		}
		before := w.count(t)

		// A link that fails (here the same object twice) prevents the ticket.
		other := w.target(t)
		dup := w.newTicket("Doppelter Link")
		err = w.svc.Create(w.ctx, dup, []ticket.Link{reviewLink(other), reviewLink(other)}, ticket.CreateOptions{})
		if !errors.Is(err, ticket.ErrLinkExists) {
			t.Fatalf("duplicate link: %v, want ErrLinkExists", err)
		}
		// An unknown entity type is refused before anything is written.
		err = w.svc.Create(w.ctx, w.newTicket("Falscher Typ"), []ticket.Link{{EntityType: "stocktake", EntityID: other}}, ticket.CreateOptions{})
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Status != http.StatusUnprocessableEntity {
			t.Fatalf("unknown entity type: %v, want 422", err)
		}
		if w.env != nil {
			// A target that does not exist rolls back the ticket.
			err = w.svc.Create(w.ctx, w.newTicket("Unbekanntes Ziel"), []ticket.Link{reviewLink(w.env.f.ID())}, ticket.CreateOptions{})
			if !errors.Is(err, ticket.ErrNotFound) {
				t.Fatalf("unknown target: %v, want ErrNotFound", err)
			}
		}
		if got := w.count(t); got != before {
			t.Fatalf("tickets after failed creations: %d, want %d", got, before)
		}
		if linked, _ := w.svc.LinkedTickets(w.ctx, w.org, ticket.LinkReviewItem, []string{other}); len(linked[other]) != 0 {
			t.Fatalf("links of the rolled back ticket: %+v", linked)
		}
	})
}

func TestServiceExclusiveTarget(t *testing.T) {
	worlds(t, func(t *testing.T, w *serviceWorld) {
		target := w.target(t)
		opts := ticket.CreateOptions{Exclusive: &ticket.Target{EntityType: ticket.LinkReviewItem, EntityID: target}}
		first := w.newTicket("Erstes")
		if err := w.svc.Create(w.ctx, first, nil, opts); err != nil {
			t.Fatalf("first exclusive ticket: %v", err)
		}
		// The exclusive target is linked.
		linked, err := w.svc.LinkedTickets(w.ctx, w.org, ticket.LinkReviewItem, []string{target})
		if err != nil || len(linked[target]) != 1 || linked[target][0].ID != first.ID {
			t.Fatalf("linked tickets: %+v err=%v", linked, err)
		}
		err = w.svc.Create(w.ctx, w.newTicket("Zweites"), nil, opts)
		var open *ticket.ErrOpenTicketExists
		if !errors.As(err, &open) || open.ID != first.ID || open.TicketNumber != first.TicketNumber {
			t.Fatalf("second exclusive ticket: %v, want ErrOpenTicketExists of %s #%d", err, first.ID, first.TicketNumber)
		}
		// A closed ticket does not count.
		for _, s := range []string{ticket.StatusOpen, ticket.StatusResolved, ticket.StatusClosed} {
			status := s
			if _, err = w.repo.Update(w.ctx, w.org, first.ID, &ticket.UpdateRequest{Status: &status}); err != nil {
				t.Fatalf("→ %s: %v", s, err)
			}
		}
		if err = w.svc.Create(w.ctx, w.newTicket("Nach dem Schließen"), nil, opts); err != nil {
			t.Fatalf("exclusive ticket after close: %v", err)
		}

		// Two concurrent creations for one target give one ticket.
		parallel := w.target(t)
		popts := ticket.CreateOptions{Exclusive: &ticket.Target{EntityType: ticket.LinkReviewItem, EntityID: parallel}}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = w.svc.Create(w.ctx, w.newTicket(fmt.Sprintf("Parallel %d", i)), nil, popts)
			}(i)
		}
		wg.Wait()
		created, refused := 0, 0
		for _, e := range errs {
			switch {
			case e == nil:
				created++
			case errors.As(e, &open):
				refused++
			default:
				t.Fatalf("parallel creation: %v", e)
			}
		}
		linked, err = w.svc.LinkedTickets(w.ctx, w.org, ticket.LinkReviewItem, []string{parallel})
		if created != 1 || refused != 1 || err != nil || len(linked[parallel]) != 1 {
			t.Fatalf("parallel: created %d, refused %d, linked %+v, err %v", created, refused, linked, err)
		}
	})
}

func TestServiceOriginAudit(t *testing.T) {
	w := pgWorld(t)
	bg := context.Background()
	entries := func(ticketID string) []map[string]any {
		t.Helper()
		rows, err := w.env.f.Admin.Query(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = 'ticket.create' AND resource_type = 'ticket' AND resource_id = $2`,
			w.org, ticketID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (map[string]any, error) {
			m := map[string]any{}
			scanErr := row.Scan(&m)
			return m, scanErr
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// With an origin: one entry ticket.create, the request counts as
	// audited (the route audit is dropped).
	target := w.target(t)
	ctx := audit.WithRequestInfo(w.ctx, audit.RequestInfo{RequestID: "wp766-origin"})
	tk := w.newTicket("Aus Review")
	origin := &ticket.Target{EntityType: ticket.LinkReviewItem, EntityID: target}
	if err := w.svc.Create(ctx, tk, nil, ticket.CreateOptions{Exclusive: origin, Origin: origin}); err != nil {
		t.Fatal(err)
	}
	if !audit.Recorded(ctx) {
		t.Error("request with origin not marked as audited")
	}
	got := entries(tk.ID)
	if len(got) != 1 || got[0]["origin_type"] != ticket.LinkReviewItem || got[0]["origin_id"] != target ||
		got[0]["ticket_number"] != float64(tk.TicketNumber) {
		t.Fatalf("audit entries with origin: %+v", got)
	}

	// Without an origin the route audit stays.
	plain := audit.WithRequestInfo(w.ctx, audit.RequestInfo{RequestID: "wp766-plain"})
	tk2 := w.newTicket("Ohne Herkunft")
	if err := w.svc.Create(plain, tk2, nil, ticket.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if audit.Recorded(plain) || len(entries(tk2.ID)) != 0 {
		t.Fatalf("creation without origin: recorded %v, entries %+v", audit.Recorded(plain), entries(tk2.ID))
	}

	// A failing link rolls back the audit entry with the ticket.
	failed := w.newTicket("Scheitert")
	err := w.svc.Create(w.ctx, failed, []ticket.Link{reviewLink(w.env.f.ID())}, ticket.CreateOptions{Origin: origin})
	if !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("failing link: %v", err)
	}
	var n int
	if err = w.env.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'ticket.create'`, w.org).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ticket.create entries: %d err=%v, want 1", n, err)
	}
}

// queryLog counts the statements of a pool that name a table.
type queryLog struct {
	mu   sync.Mutex
	stmt []string
}

func (l *queryLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmt = append(l.stmt, data.SQL)
	return ctx
}

func (l *queryLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (l *queryLog) naming(table string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.stmt {
		if strings.Contains(s, table) {
			n++
		}
	}
	return n
}

func TestServiceLinkedTickets(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		w := memoryWorld(t)
		a, b, none := w.target(t), w.target(t), w.target(t)
		t1, t2 := w.newTicket("A"), w.newTicket("B")
		if err := w.svc.Create(w.ctx, t1, []ticket.Link{reviewLink(a), reviewLink(b)}, ticket.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.Create(w.ctx, t2, []ticket.Link{reviewLink(a)}, ticket.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		got, err := w.svc.LinkedTickets(w.ctx, w.org, ticket.LinkReviewItem, []string{a, b, none})
		if err != nil || len(got[a]) != 2 || got[a][0].ID != t1.ID || got[a][1].ID != t2.ID || len(got[b]) != 1 || len(got[none]) != 0 {
			t.Fatalf("linked tickets: %+v err=%v", got, err)
		}
		if _, err = w.svc.LinkedTickets(w.ctx, w.org, "stocktake", []string{a}); err == nil {
			t.Fatal("unknown entity type accepted")
		}
	})
	t.Run("postgresql", func(t *testing.T) {
		w := pgWorld(t)
		f := w.env.f
		item, other := w.target(t), w.target(t)
		ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "svc-c1"), f.CI(t, f.OrgA, f.Client2, "svc-c2")
		visible, hidden := w.newTicket("Client 1"), w.newTicket("Client 2")
		visible.RelatedCIID, hidden.RelatedCIID = ci1, ci2
		for _, tk := range []*ticket.Ticket{visible, hidden} {
			if err := w.svc.Create(w.ctx, tk, []ticket.Link{reviewLink(item)}, ticket.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.svc.Create(w.ctx, w.newTicket("Anderes Item"), []ticket.Link{reviewLink(other)}, ticket.CreateOptions{}); err != nil {
			t.Fatal(err)
		}

		cfg := f.App.Config()
		trace := &queryLog{}
		cfg.ConnConfig.Tracer = trace
		cfg.MinConns = 0
		pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		svc := ticket.NewService(ticket.NewPGRepository(pool), nil)

		got, err := svc.LinkedTickets(w.ctx, w.org, ticket.LinkReviewItem, []string{item, other, "no-uuid"})
		if err != nil || len(got[item]) != 2 || len(got[other]) != 1 {
			t.Fatalf("org-wide linked tickets: %+v err=%v", got, err)
		}
		if n := trace.naming("ticket_link"); n != 1 {
			t.Fatalf("LinkedTickets sent %d statements on ticket_link, want 1", n)
		}
		// A principal restricted to client 1 sees the ticket of client 1, not
		// the one of client 2; the ticket without client is org-wide.
		got, err = svc.LinkedTickets(f.ClientCtx(f.Client1), w.org, ticket.LinkReviewItem, []string{item, other})
		if err != nil || len(got[item]) != 1 || got[item][0].ID != visible.ID || got[item][0].TicketNumber != visible.TicketNumber ||
			got[item][0].Status != ticket.StatusNew || len(got[other]) != 1 {
			t.Fatalf("client-scoped linked tickets: %+v err=%v", got, err)
		}
		// The exclusive check runs under the ticket RLS: the ticket of
		// client 2 does not count for the client-1 principal.
		hiddenOnly := w.target(t)
		foreign := w.newTicket("Nur Client 2")
		foreign.RelatedCIID = ci2
		if err = w.svc.Create(w.ctx, foreign, []ticket.Link{reviewLink(hiddenOnly)}, ticket.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		scoped := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: w.user})
		excl := ticket.CreateOptions{Exclusive: &ticket.Target{EntityType: ticket.LinkReviewItem, EntityID: hiddenOnly}}
		mine := w.newTicket("Client 1 exklusiv")
		mine.RelatedCIID = ci1
		if err = w.svc.Create(scoped, mine, nil, excl); err != nil {
			t.Fatalf("exclusive creation beside an invisible ticket: %v", err)
		}
	})
}

func TestServiceSystemComments(t *testing.T) {
	worlds(t, func(t *testing.T, w *serviceWorld) {
		tk := w.newTicket("Kommentare")
		if err := w.svc.Create(w.ctx, tk, nil, ticket.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		sys, err := w.svc.AddSystemComment(w.ctx, w.org, tk.ID, "Review-Item aufgelöst")
		if err != nil {
			t.Fatalf("system comment: %v", err)
		}
		if sys.AuthorID != "" || sys.AuthorType != ticket.AuthorSystem || !sys.IsInternal {
			t.Fatalf("system comment: %+v", sys)
		}
		if got := w.hooks.firstResponses.Load(); got != 0 {
			t.Fatalf("system comment called MarkFirstResponse %d times", got)
		}
		// A public comment of a user is the first response.
		if r := w.request(t, http.MethodPost, "/api/v1/tickets/"+tk.ID+"/comments", map[string]any{"content": "Schaue ich mir an"}); r.Code != http.StatusCreated {
			t.Fatalf("user comment: %d %s", r.Code, r.Body)
		}
		if got := w.hooks.firstResponses.Load(); got != 1 {
			t.Fatalf("user comment called MarkFirstResponse %d times, want 1", got)
		}

		// The DTO: author_id null and author_type system for the system
		// comment, the user and user for the other.
		r := w.request(t, http.MethodGet, "/api/v1/tickets/"+tk.ID+"/comments", nil)
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if err = json.Unmarshal(r.Body.Bytes(), &page); err != nil || len(page.Data) != 2 {
			t.Fatalf("comments: %s err=%v", r.Body, err)
		}
		types := map[string]any{}
		for _, c := range page.Data {
			author, present := c["author_id"]
			if !present {
				t.Fatalf("comment without author_id member: %+v", c)
			}
			types[c["author_type"].(string)] = author
		}
		if types[ticket.AuthorSystem] != nil || types[ticket.AuthorUser] != w.user {
			t.Fatalf("authors by type: %+v", types)
		}

		if w.env == nil {
			return
		}
		var authorNull bool
		var authorType string
		if err = w.env.f.Admin.QueryRow(context.Background(), `SELECT author_id IS NULL, author_type FROM ticket_comment WHERE id = $1`, sys.ID).
			Scan(&authorNull, &authorType); err != nil || !authorNull || authorType != ticket.AuthorSystem {
			t.Fatalf("stored system comment: author_id null %v, type %q, err %v", authorNull, authorType, err)
		}
		// The CHECK refuses a user comment without author and a system
		// comment with one.
		for _, stmt := range []string{
			`INSERT INTO ticket_comment (organization_id, ticket_id, content) VALUES ($1, $2, 'x')`,
			`INSERT INTO ticket_comment (organization_id, ticket_id, content, author_type, author_id) VALUES ($1, $2, 'x', 'system', $3)`,
		} {
			args := []any{w.org, tk.ID}
			if strings.Contains(stmt, "$3") {
				args = append(args, w.user)
			}
			_, err = w.env.f.Admin.Exec(context.Background(), stmt, args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ticket_comment_author_check" {
				t.Errorf("%s: %v, want check violation", stmt, err)
			}
		}
		err = w.repo.AddComment(w.ctx, &ticket.Comment{OrganizationID: w.org, TicketID: tk.ID, AuthorType: ticket.AuthorUser, Content: "anonymous"})
		if err == nil {
			t.Error("user comment without author_id accepted")
		}
	})
}
