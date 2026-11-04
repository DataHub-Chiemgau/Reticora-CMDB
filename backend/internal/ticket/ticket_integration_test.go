package ticket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

// The tests of this file run only against PostgreSQL (TEST_DATABASE_URL,
// see scopetest.Seed). They cover TKT-01 (Essential: state machine,
// assignment, number per organization, team_id=mine) and EVT-03
// (ticket.updated) of WP-290.

// ticketEnv is a fixture with the PostgreSQL ticket repository (with
// outbox) behind its HTTP handler.
type ticketEnv struct {
	f    *scopetest.Fixture
	repo *ticket.PGRepository
	mux  chi.Router
	user string
}

func newTicketEnv(t *testing.T) *ticketEnv {
	t.Helper()
	f := scopetest.Seed(t, "38")
	repo := ticket.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	mux := chi.NewRouter()
	ticket.NewHandler(repo).RegisterRoutes(mux)
	return &ticketEnv{f: f, repo: repo, mux: mux, user: f.AppUser(t, f.OrgA, "reporter")}
}

// request sends an org-wide request of organization A as user.
func (e *ticketEnv) request(t *testing.T, h http.Handler, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func (e *ticketEnv) create(t *testing.T, body map[string]any) ticket.Ticket {
	t.Helper()
	w := e.request(t, e.mux, http.MethodPost, "/api/v1/tickets", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create ticket: %d %s", w.Code, w.Body)
	}
	var tk ticket.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

// ticketEvents returns the data of the ticket.updated events of a ticket in
// commit order.
func (e *ticketEnv) ticketEvents(t *testing.T, ticketID string) []map[string]any {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(),
		`SELECT data FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence`,
		e.f.OrgA, events.TicketUpdated, ticketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err = json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTicketStateMachineInPostgreSQL(t *testing.T) {
	e := newTicketEnv(t)
	tk := e.create(t, map[string]any{"title": "Drucker", "priority": "high"})
	if tk.Status != ticket.StatusNew {
		t.Fatalf("created as %s, want new", tk.Status)
	}
	url := "/api/v1/tickets/" + tk.ID
	patch := func(status string) *httptest.ResponseRecorder {
		return e.request(t, e.mux, http.MethodPatch, url, map[string]any{"status": status})
	}

	// A refused transition is 409 and leaves the status (read under FOR
	// UPDATE) as it was.
	if w := patch("waiting"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "new to waiting") {
		t.Fatalf("new → waiting: %d %s, want 409", w.Code, w.Body)
	}
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ticket WHERE id = $1`, tk.ID).Scan(&status); err != nil || status != "new" {
		t.Fatalf("status after 409: %q err=%v, want new", status, err)
	}
	if w := patch("done"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status: %d %s, want 422", w.Code, w.Body)
	}

	for _, s := range []string{"open", "resolved", "closed"} {
		if w := patch(s); w.Code != http.StatusOK {
			t.Fatalf("→ %s: %d %s", s, w.Code, w.Body)
		}
	}
	var resolved, closed bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT resolved_at IS NOT NULL, closed_at IS NOT NULL FROM ticket WHERE id = $1`, tk.ID).Scan(&resolved, &closed); err != nil || !resolved || !closed {
		t.Fatalf("closed: resolved_at set %v, closed_at set %v, err=%v", resolved, closed, err)
	}
	// Reopen clears both.
	w := patch("open")
	if w.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", w.Code, w.Body)
	}
	var reopened ticket.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || reopened.ResolvedAt != "" || reopened.ClosedAt != "" {
		t.Fatalf("reopen: %+v err=%v, want resolved_at and closed_at cleared", reopened, err)
	}

	// The workflow path creates tickets as open; any other state is refused.
	wf := &ticket.Ticket{OrganizationID: e.f.OrgA, Title: "Workflow task", Status: ticket.StatusOpen, Priority: "medium", Category: "task", ReporterID: e.user}
	if err := e.repo.Create(e.f.OrgCtx(e.f.OrgA), wf); err != nil || wf.Status != "open" {
		t.Fatalf("workflow ticket: status=%s err=%v", wf.Status, err)
	}
	bad := &ticket.Ticket{OrganizationID: e.f.OrgA, Title: "x", Status: ticket.StatusResolved, Priority: "medium", Category: "task", ReporterID: e.user}
	if err := e.repo.Create(e.f.OrgCtx(e.f.OrgA), bad); !errors.Is(err, ticket.ErrInvalidTransition) {
		t.Fatalf("create as resolved: %v, want ErrInvalidTransition", err)
	}
}

func TestTicketNumbersPerOrganization(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	ciC1 := f.CI(t, f.OrgA, f.Client1, "ticket-number-c1")
	userB := f.AppUser(t, f.OrgB, "reporter-b")

	// An existing ticket keeps its number; the counter continues after it.
	existing := f.ID()
	if _, err := f.Admin.Exec(context.Background(),
		`INSERT INTO ticket (id, organization_id, ticket_number, title, reporter_id) VALUES ($1, $2, 41, 'Bestand', $3)`,
		existing, f.OrgA, e.user); err != nil {
		t.Fatal(err)
	}

	const perPrincipal = 8
	type principal struct {
		org      string
		ctx      context.Context
		reporter string
		ciID     string
	}
	principals := []principal{
		{f.OrgA, f.OrgCtx(f.OrgA), e.user, ""},
		{f.OrgA, f.ClientCtx(f.Client1), e.user, ciC1}, // sees only client-1 tickets
		{f.OrgB, f.OrgCtx(f.OrgB), userB, ""},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(principals)*perPrincipal)
	for _, p := range principals {
		for i := 0; i < perPrincipal; i++ {
			wg.Add(1)
			go func(p principal) {
				defer wg.Done()
				errs <- e.repo.Create(p.ctx, &ticket.Ticket{OrganizationID: p.org, Title: "parallel", Priority: "low", Category: "task", ReporterID: p.reporter, RelatedCIID: p.ciID})
			}(p)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel create: %v", err)
		}
	}

	numbers := func(org string) []int {
		rows, err := f.Admin.Query(context.Background(), `SELECT ticket_number FROM ticket WHERE organization_id = $1 ORDER BY ticket_number`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int
		for rows.Next() {
			var n int
			if err = rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	wantA := []int{41}
	for n := 42; n < 42+2*perPrincipal; n++ {
		wantA = append(wantA, n)
	}
	if got := numbers(f.OrgA); !slices.Equal(got, wantA) {
		t.Errorf("organization A numbers %v, want %v", got, wantA)
	}
	var wantB []int
	for n := 1; n <= perPrincipal; n++ {
		wantB = append(wantB, n)
	}
	if got := numbers(f.OrgB); !slices.Equal(got, wantB) {
		t.Errorf("organization B numbers %v, want %v", got, wantB)
	}
}

func TestTicketTeamMineFilter(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	ctx := context.Background()
	team := func(name string) string {
		id := f.ID()
		if _, err := f.Admin.Exec(ctx, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, $3)`, id, f.OrgA, name); err != nil {
			t.Fatal(err)
		}
		return id
	}
	t1, t2, t3 := team("Netz"), team("Server"), team("Fremd")
	for _, tm := range []string{t1, t2} {
		if _, err := f.Admin.Exec(ctx, `INSERT INTO team_member (team_id, user_id, organization_id) VALUES ($1, $2, $3)`, tm, e.user, f.OrgA); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{}
	for _, tm := range []string{t1, t2, t1, t3, t2} {
		tk := e.create(t, map[string]any{"title": "queue", "team_id": tm})
		if tm != t3 {
			want[tk.ID] = true
		}
	}
	e.create(t, map[string]any{"title": "without team"})

	got := map[string]bool{}
	url := "/api/v1/tickets?team_id=mine&limit=2&sort_by=created_at&sort_dir=asc"
	total := -1
	for page := 0; url != "" && page < 10; page++ {
		w := e.request(t, e.mux, http.MethodGet, url, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("team_id=mine: %d %s", w.Code, w.Body)
		}
		var list api.ListResponse[ticket.Ticket]
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		total = list.Total
		for _, tk := range list.Data {
			got[tk.ID] = true
		}
		url = ""
		if list.NextCursor != "" {
			url = "/api/v1/tickets?team_id=mine&limit=2&sort_by=created_at&sort_dir=asc&cursor=" + list.NextCursor
		}
	}
	if total != len(want) || len(got) != len(want) {
		t.Fatalf("team_id=mine: total=%d got %d tickets, want %d", total, len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Errorf("ticket %s of an own team missing", id)
		}
	}

	// Without team membership the list is empty.
	e.user = f.AppUser(t, f.OrgA, "no-team")
	w := e.request(t, e.mux, http.MethodGet, "/api/v1/tickets?team_id=mine", nil)
	var empty api.ListResponse[ticket.Ticket]
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil || w.Code != http.StatusOK || empty.Total != 0 || len(empty.Data) != 0 {
		t.Fatalf("team_id=mine without team: %d %s", w.Code, w.Body)
	}
}

func TestTicketUpdatedEvents(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	ciC1 := f.CI(t, f.OrgA, f.Client1, "ticket-event-c1")
	tk := e.create(t, map[string]any{"title": "Ereignis", "related_ci_id": ciC1})
	url := "/api/v1/tickets/" + tk.ID

	steps := []struct {
		method, url string
		body        any
		code        int
	}{
		{http.MethodPatch, url, map[string]any{"title": "Ereignis 2", "priority": "high"}, http.StatusOK},
		{http.MethodPatch, url, map[string]any{"status": "open"}, http.StatusOK},
		{http.MethodPost, url + "/comments", map[string]any{"content": "geheimer Inhalt", "is_internal": true}, http.StatusCreated},
		{http.MethodPatch, url, map[string]any{"status": "new", "title": "x"}, http.StatusConflict},
		{http.MethodDelete, url, nil, http.StatusNoContent},
	}
	var commentID string
	for _, s := range steps {
		w := e.request(t, e.mux, s.method, s.url, s.body)
		if w.Code != s.code {
			t.Fatalf("%s %s: %d %s, want %d", s.method, s.url, w.Code, w.Body, s.code)
		}
		if strings.HasSuffix(s.url, "/comments") {
			var c ticket.Comment
			if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			commentID = c.ID
		}
	}

	evs := e.ticketEvents(t, tk.ID)
	changes := make([]string, len(evs))
	for i, ev := range evs {
		changes[i], _ = ev["change"].(string)
		if ev["id"] != tk.ID || ev["client_id"] != f.Client1 {
			t.Errorf("event %d: id=%v client_id=%v, want the ticket and client 1", i, ev["id"], ev["client_id"])
		}
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "geheimer Inhalt") || strings.Contains(string(raw), "Ereignis 2") {
			t.Errorf("event %d carries field values or comment content: %s", i, raw)
		}
	}
	// The refused transition wrote nothing.
	if want := []string{"created", "updated", "status", "comment", "deleted"}; !slices.Equal(changes, want) {
		t.Fatalf("ticket.updated changes %v, want %v", changes, want)
	}
	if got := evs[1]["changed_fields"]; !jsonStrings(got, "title", "priority") {
		t.Errorf("changed_fields of the update: %v, want title and priority", got)
	}
	if evs[2]["status_from"] != "new" || evs[2]["status_to"] != "open" || !jsonStrings(evs[2]["changed_fields"], "status") {
		t.Errorf("status event: %v", evs[2])
	}
	if evs[3]["comment_id"] != commentID || evs[3]["is_internal"] != true {
		t.Errorf("comment event: %v, want comment_id %s and is_internal", evs[3], commentID)
	}

	// After a rollback no event remains.
	tk2 := e.create(t, map[string]any{"title": "Rollback"})
	before := len(e.ticketEvents(t, tk2.ID))
	errRollback := errors.New("rollback")
	err := database.WithRequestTenantTx(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context) error {
		title := "nie"
		if _, updErr := e.repo.Update(ctx, f.OrgA, tk2.ID, ticket.UpdateRequest{Title: &title}); updErr != nil {
			return updErr
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("transaction: %v", err)
	}
	if after := len(e.ticketEvents(t, tk2.ID)); after != before {
		t.Fatalf("events after rollback: %d, want %d", after, before)
	}
}

// jsonStrings reports whether v is a JSON array of exactly want.
func jsonStrings(v any, want ...string) bool {
	arr, ok := v.([]any)
	if !ok || len(arr) != len(want) {
		return false
	}
	for i := range arr {
		if arr[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTicketRoutesNeedTicketingEssential(t *testing.T) {
	e := newTicketEnv(t)
	tk := e.create(t, map[string]any{"title": "Gate"})
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	gated := svc.Middleware(e.mux)
	url := "/api/v1/tickets/" + tk.ID

	w := e.request(t, gated, http.MethodPatch, url, map[string]any{"status": "open"})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
		t.Fatalf("PATCH without ticketing_essential: %d %s, want 403 feature-not-entitled", w.Code, w.Body)
	}
	if _, err := svc.Grant(context.Background(), &entitlement.Entitlement{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureTicketingEssential,
		Plan: entitlement.PlanEssential, Enabled: true, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	if w = e.request(t, gated, http.MethodPatch, url, map[string]any{"status": "open"}); w.Code != http.StatusOK {
		t.Fatalf("PATCH with ticketing_essential: %d %s", w.Code, w.Body)
	}
}
