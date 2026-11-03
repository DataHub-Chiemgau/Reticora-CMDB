package sla_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// slaWorld is the PostgreSQL fixture of the TKT-02 tests (WP-292):
// organization A in Europe/Berlin, client 1 in Pacific/Auckland, client 2
// without own zone, and the SLA and ticket repositories on the
// application pool.
type slaWorld struct {
	f       *scopetest.Fixture
	repo    *sla.PGRepository
	tickets *ticket.PGRepository
	user    string
	ci1     string
	ci2     string
	mux     chi.Router
}

func newSLAWorld(t *testing.T) *slaWorld {
	t.Helper()
	f := scopetest.Seed(t, "39")
	ctx := context.Background()
	if _, err := f.Admin.Exec(ctx, `UPDATE organization SET timezone = 'Europe/Berlin' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatalf("org timezone: %v", err)
	}
	if _, err := f.Admin.Exec(ctx, `UPDATE client SET timezone = 'Pacific/Auckland' WHERE id = $1`, f.Client1); err != nil {
		t.Fatalf("client timezone: %v", err)
	}
	w := &slaWorld{f: f, repo: sla.NewPGRepository(f.App), tickets: ticket.NewPGRepository(f.App)}
	w.user = f.AppUser(t, f.OrgA, "sla-reporter")
	w.ci1 = f.CI(t, f.OrgA, f.Client1, "sla-cal-c1")
	w.ci2 = f.CI(t, f.OrgA, f.Client2, "sla-cal-c2")
	w.mux = chi.NewRouter()
	sla.NewHandler(w.repo, w.tickets).RegisterRoutes(w.mux)
	return w
}

// ticket creates a ticket of the priority about ciID (empty: no client)
// and sets its creation time for the due computation.
func (w *slaWorld) ticket(t *testing.T, priority, ciID string, created time.Time) *ticket.Ticket {
	t.Helper()
	tk := &ticket.Ticket{OrganizationID: w.f.OrgA, Title: "sla " + priority, Status: "open", Priority: priority, Category: "incident", ReporterID: w.user, RelatedCIID: ciID}
	if err := w.tickets.Create(w.f.OrgCtx(w.f.OrgA), tk); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	tk.CreatedAt = created
	return tk
}

func (w *slaWorld) policy(t *testing.T, p *sla.Policy) *sla.Policy {
	t.Helper()
	p.OrganizationID = w.f.OrgA
	if p.ResponseTargetMinutes == 0 {
		p.ResponseTargetMinutes, p.ResolutionTargetMinutes = 120, 600
	}
	if err := w.repo.CreatePolicy(w.f.OrgCtx(w.f.OrgA), p); err != nil {
		t.Fatalf("create policy %s: %v", p.Name, err)
	}
	return p
}

// calendar inserts a business calendar through the maintenance pool (the
// REST maintenance follows in WP-768).
func (w *slaWorld) calendar(t *testing.T, clientID, name, timezone, weekly, holidays string, isDefault bool) string {
	t.Helper()
	var id string
	if err := w.f.Admin.QueryRow(context.Background(), `
		INSERT INTO business_calendar (organization_id, client_id, name, timezone, weekly_hours, holidays, is_default)
		VALUES ($1, NULLIF($2, '')::uuid, $3, NULLIF($4, ''), $5::jsonb, $6::jsonb, $7) RETURNING id::text`,
		w.f.OrgA, clientID, name, timezone, weekly, holidays, isDefault).Scan(&id); err != nil {
		t.Fatalf("insert calendar %s: %v", name, err)
	}
	return id
}

func (w *slaWorld) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	ctx := tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

func (w *slaWorld) apply(t *testing.T, tk *ticket.Ticket, slaID string) *sla.TicketSLA {
	t.Helper()
	state, err := w.repo.ApplyForTicket(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, tk, slaID)
	if err != nil {
		t.Fatalf("apply sla: %v", err)
	}
	return state
}

func calendarOf(s *sla.TicketSLA) string {
	if s.CalendarID == nil {
		return ""
	}
	return *s.CalendarID
}

const weekdays = `[{"weekday":1,"start":"08:00","end":"18:00"},{"weekday":2,"start":"08:00","end":"18:00"},{"weekday":3,"start":"08:00","end":"18:00"},{"weekday":4,"start":"08:00","end":"18:00"},{"weekday":5,"start":"08:00","end":"18:00"}]`

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSLAPolicyChoiceByClient (TKT-02): the policy follows ticket.client_id,
// a client policy before the org policy; every ticket priority finds its
// policy, as sla.priority and ticket.priority share one value list.
func TestSLAPolicyChoiceByClient(t *testing.T) {
	w := newSLAWorld(t)
	created := mustTime(t, "2026-08-10T08:00:00Z")
	orgPolicies := map[string]*sla.Policy{}
	for _, prio := range ticket.Priorities {
		orgPolicies[prio] = w.policy(t, &sla.Policy{Name: "org " + prio, Priority: prio})
	}
	clientPolicy := w.policy(t, &sla.Policy{Name: "c1 high", Priority: "high", ClientID: w.f.Client1})

	if s := w.apply(t, w.ticket(t, "high", w.ci1, created), ""); s.SLAID != clientPolicy.ID {
		t.Fatalf("client-1 ticket: sla %s, want the client policy %s", s.SLAID, clientPolicy.ID)
	}
	if s := w.apply(t, w.ticket(t, "high", w.ci2, created), ""); s.SLAID != orgPolicies["high"].ID {
		t.Fatalf("client-2 ticket: sla %s, want the org policy", s.SLAID)
	}
	for _, prio := range ticket.Priorities {
		if s := w.apply(t, w.ticket(t, prio, "", created), ""); s.SLAID != orgPolicies[prio].ID {
			t.Fatalf("priority %s: sla %s, want %s", prio, s.SLAID, orgPolicies[prio].ID)
		}
	}
}

// TestSLACalendarChain (TKT-02, LOC-01/02): policy calendar → default of
// the ticket's client → default of the organization → built-in standard;
// zone: calendar → client.timezone → organization.timezone. The chosen
// calendar is stored; deleting it keeps the due times.
func TestSLACalendarChain(t *testing.T) {
	w := newSLAWorld(t)
	ctx := context.Background()
	// Friday, 07.08.2026, 15:00 UTC: 17:00 in Berlin, Saturday 03:00 in Auckland.
	created := mustTime(t, "2026-08-07T15:00:00Z")
	orgPolicy := w.policy(t, &sla.Policy{Name: "org", Priority: "medium", BusinessCalendar: true})
	clientPolicy := w.policy(t, &sla.Policy{Name: "c1", Priority: "medium", ClientID: w.f.Client1, BusinessCalendar: true})
	orgTicket := w.ticket(t, "medium", "", created)
	clientTicket := w.ticket(t, "medium", w.ci1, created)

	check := func(step string, tk *ticket.Ticket, wantPolicy, wantCalendar, wantDue string) *sla.TicketSLA {
		t.Helper()
		s := w.apply(t, tk, "")
		if s.SLAID != wantPolicy || calendarOf(s) != wantCalendar || !s.ResponseDueAt.Equal(mustTime(t, wantDue)) {
			t.Fatalf("%s: sla %s calendar %q due %s, want sla %s calendar %q due %s",
				step, s.SLAID, calendarOf(s), s.ResponseDueAt.UTC().Format(time.RFC3339), wantPolicy, wantCalendar, wantDue)
		}
		return s
	}

	// Built-in standard in the organization's zone: Friday 17–18 and
	// Monday 08–09 CEST.
	check("built-in, org zone", orgTicket, orgPolicy.ID, "", "2026-08-10T07:00:00Z")
	// Built-in standard in client 1's zone: Monday 08–10 NZST.
	check("built-in, client zone", clientTicket, clientPolicy.ID, "", "2026-08-09T22:00:00Z")

	// Org default with Monday 10.08. as holiday, without own zone.
	orgDefault := w.calendar(t, "", "org default", "", weekdays, `[{"date":"2026-08-10","name":"Betriebsruhe"}]`, true)
	check("org default, org zone", orgTicket, orgPolicy.ID, orgDefault, "2026-08-11T07:00:00Z")
	check("org default, client zone", clientTicket, clientPolicy.ID, orgDefault, "2026-08-10T22:00:00Z")

	// Client 1's default in UTC with Saturday 10:00–12:00 only.
	clientDefault := w.calendar(t, w.f.Client1, "c1 default", "UTC", `[{"weekday":6,"start":"10:00","end":"12:00"}]`, `[]`, true)
	check("client default", clientTicket, clientPolicy.ID, clientDefault, "2026-08-08T12:00:00Z")
	check("org ticket keeps org default", orgTicket, orgPolicy.ID, orgDefault, "2026-08-11T07:00:00Z")

	// The policy's own calendar in New York: Friday 11:00 EDT + 2 h.
	newYork := w.calendar(t, w.f.Client1, "c1 new york", "America/New_York", weekdays, `[]`, false)
	if _, err := w.repo.UpdatePolicy(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, clientPolicy.ID, sla.UpdatePolicyRequest{CalendarID: sla.OptionalID{Set: true, Value: newYork}}); err != nil {
		t.Fatalf("set policy calendar: %v", err)
	}
	state := check("policy calendar", clientTicket, clientPolicy.ID, newYork, "2026-08-07T17:00:00Z")

	// Deleting the calendar clears both references; the due times stay.
	if _, err := w.f.Admin.Exec(ctx, `DELETE FROM business_calendar WHERE id = $1`, newYork); err != nil {
		t.Fatalf("delete calendar: %v", err)
	}
	var policyCalendar, stateCalendar *string
	var due time.Time
	if err := w.f.Admin.QueryRow(ctx, `SELECT s.calendar_id::text, ts.calendar_id::text, ts.response_due_at FROM ticket_sla ts JOIN sla s ON s.id = ts.sla_id WHERE ts.id = $1`, state.ID).Scan(&policyCalendar, &stateCalendar, &due); err != nil {
		t.Fatal(err)
	}
	if policyCalendar != nil || stateCalendar != nil || !due.Equal(state.ResponseDueAt) {
		t.Fatalf("after delete: policy calendar %v, state calendar %v, due %s (was %s)", policyCalendar, stateCalendar, due, state.ResponseDueAt)
	}
	// Without the policy calendar the client default applies again.
	check("after delete", clientTicket, clientPolicy.ID, clientDefault, "2026-08-08T12:00:00Z")

	// Elapsed time ignores every calendar.
	if _, err := w.repo.UpdatePolicy(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, orgPolicy.ID, sla.UpdatePolicyRequest{BusinessCalendar: new(bool)}); err != nil {
		t.Fatal(err)
	}
	check("elapsed", orgTicket, orgPolicy.ID, "", "2026-08-07T17:00:00Z")
}

// TestBusinessCalendarConstraints: one default per organization and
// client (partial unique index), the array checks of the jsonb columns,
// and the client-scoped principal sees its client's and the org-wide
// calendars, not those of another client.
func TestBusinessCalendarConstraints(t *testing.T) {
	w := newSLAWorld(t)
	ctx := context.Background()
	orgDefault := w.calendar(t, "", "org default", "", weekdays, `[]`, true)
	c1Default := w.calendar(t, w.f.Client1, "c1 default", "", weekdays, `[]`, true)
	c2Default := w.calendar(t, w.f.Client2, "c2 default", "", weekdays, `[]`, true)
	w.calendar(t, "", "org other", "", weekdays, `[]`, false)

	for _, clientID := range []string{"", w.f.Client1} {
		_, err := w.f.Admin.Exec(ctx, `INSERT INTO business_calendar (organization_id, client_id, name, is_default) VALUES ($1, NULLIF($2, '')::uuid, 'second', true)`, w.f.OrgA, clientID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "business_calendar_default" {
			t.Fatalf("second default for client %q: got %v, want unique violation of business_calendar_default", clientID, err)
		}
	}
	for _, col := range []string{"weekly_hours", "holidays"} {
		if _, err := w.f.Admin.Exec(ctx, `INSERT INTO business_calendar (organization_id, name, `+col+`) VALUES ($1, 'object', '{}'::jsonb)`, w.f.OrgA); err == nil {
			t.Fatalf("%s as object was accepted", col)
		}
	}
	if _, err := w.f.Admin.Exec(ctx, `INSERT INTO business_calendar (organization_id, name, timezone) VALUES ($1, 'tz', 'Mars/Olympus')`, w.f.OrgA); err == nil {
		t.Fatal("unknown time zone was accepted")
	}

	seen := map[string]bool{}
	err := database.WithRequestTenant(w.f.ClientCtx(w.f.Client1), w.f.App, w.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM business_calendar`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen[orgDefault] || !seen[c1Default] || seen[c2Default] || len(seen) != 3 {
		t.Fatalf("client-1 principal sees %v, want org-wide and client-1 calendars only", seen)
	}
}

// TestSLAPolicyCalendarBinding: calendar_id of a policy names a visible
// calendar whose client is NULL or the policy's; otherwise 400 on
// calendar_id, also when only client_id of the policy changes.
func TestSLAPolicyCalendarBinding(t *testing.T) {
	w := newSLAWorld(t)
	c1Cal := w.calendar(t, w.f.Client1, "c1", "", weekdays, `[]`, false)
	c2Cal := w.calendar(t, w.f.Client2, "c2", "", weekdays, `[]`, false)
	orgCal := w.calendar(t, "", "org", "", weekdays, `[]`, false)

	expectField := func(step string, rec *httptest.ResponseRecorder) {
		t.Helper()
		var problem api.FieldProblem
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		if rec.Code != http.StatusBadRequest || problem.Field != "calendar_id" {
			t.Fatalf("%s: got %d %s, want 400 on calendar_id", step, rec.Code, rec.Body.String())
		}
	}
	expectField("org policy with client calendar", w.do(t, http.MethodPost, "/api/v1/slas",
		`{"name":"org","priority":"low","response_target_minutes":10,"resolution_target_minutes":20,"business_calendar":true,"calendar_id":"`+c1Cal+`"}`))
	expectField("client-1 policy with client-2 calendar", w.do(t, http.MethodPost, "/api/v1/slas",
		`{"client_id":"`+w.f.Client1+`","name":"c1","priority":"low","response_target_minutes":10,"resolution_target_minutes":20,"business_calendar":true,"calendar_id":"`+c2Cal+`"}`))
	expectField("unknown calendar", w.do(t, http.MethodPost, "/api/v1/slas",
		`{"name":"x","priority":"low","response_target_minutes":10,"resolution_target_minutes":20,"calendar_id":"`+w.f.ID()+`"}`))

	rec := w.do(t, http.MethodPost, "/api/v1/slas",
		`{"client_id":"`+w.f.Client1+`","name":"c1","priority":"low","response_target_minutes":10,"resolution_target_minutes":20,"business_calendar":true,"calendar_id":"`+c1Cal+`"}`)
	var policy sla.Policy
	if err := json.Unmarshal(rec.Body.Bytes(), &policy); rec.Code != http.StatusCreated || err != nil || policy.CalendarID == nil || *policy.CalendarID != c1Cal {
		t.Fatalf("client-1 policy with its calendar: %d %s", rec.Code, rec.Body.String())
	}
	expectField("move policy to client 2", w.do(t, http.MethodPatch, "/api/v1/slas/"+policy.ID, `{"client_id":"`+w.f.Client2+`"}`))
	expectField("patch to client-2 calendar", w.do(t, http.MethodPatch, "/api/v1/slas/"+policy.ID, `{"calendar_id":"`+c2Cal+`"}`))
	if rec = w.do(t, http.MethodGet, "/api/v1/slas/"+policy.ID, ""); !bytes.Contains(rec.Body.Bytes(), []byte(`"client_id":"`+w.f.Client1+`"`)) {
		t.Fatalf("a refused update changed the policy: %s", rec.Body.String())
	}
	if rec = w.do(t, http.MethodPatch, "/api/v1/slas/"+policy.ID, `{"calendar_id":"`+orgCal+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("org-wide calendar on a client policy: %d %s", rec.Code, rec.Body.String())
	}
	if rec = w.do(t, http.MethodPatch, "/api/v1/slas/"+policy.ID, `{"calendar_id":null}`); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"calendar_id":null`)) {
		t.Fatalf("clear calendar: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTicketSLARemainingAndRecompute: GET and POST /api/v1/tickets/{id}/sla
// and the breach list carry the remaining minutes and paused; sla_id sets
// sla_explicit, a new computation resets the breach notifications.
func TestTicketSLARemainingAndRecompute(t *testing.T) {
	w := newSLAWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	allWeek := `[{"weekday":1,"start":"00:00","end":"24:00"},{"weekday":2,"start":"00:00","end":"24:00"},{"weekday":3,"start":"00:00","end":"24:00"},{"weekday":4,"start":"00:00","end":"24:00"},{"weekday":5,"start":"00:00","end":"24:00"},{"weekday":6,"start":"00:00","end":"24:00"},{"weekday":7,"start":"00:00","end":"24:00"}]`
	open := w.calendar(t, "", "always", "UTC", allWeek, `[]`, false)
	var holidays []string
	for d := -1; d <= 1; d++ {
		holidays = append(holidays, `{"date":"`+now.AddDate(0, 0, d).Format(time.DateOnly)+`"}`)
	}
	closed := w.calendar(t, "", "closed today", "UTC", allWeek, `[`+holidays[0]+`,`+holidays[1]+`,`+holidays[2]+`]`, false)
	openPolicy := w.policy(t, &sla.Policy{Name: "open", Priority: "high", BusinessCalendar: true, CalendarID: &open, ResponseTargetMinutes: 60, ResolutionTargetMinutes: 600})
	closedPolicy := w.policy(t, &sla.Policy{Name: "closed", Priority: "low", BusinessCalendar: true, CalendarID: &closed})
	elapsedPolicy := w.policy(t, &sla.Policy{Name: "elapsed", Priority: "medium", ResponseTargetMinutes: 30, ResolutionTargetMinutes: 60})

	tk, err := w.tickets.GetByID(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, w.ticket(t, "high", "", now).ID)
	if err != nil {
		t.Fatal(err)
	}
	type body struct {
		SLAID              string  `json:"sla_id"`
		CalendarID         *string `json:"calendar_id"`
		ResponseRemaining  *int    `json:"response_remaining_minutes"`
		ResolutionRemainig *int    `json:"resolution_remaining_minutes"`
		Paused             *bool   `json:"paused"`
	}
	read := func(step string, rec *httptest.ResponseRecorder) body {
		t.Helper()
		var b body
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &b) != nil || b.Paused == nil {
			t.Fatalf("%s: %d %s", step, rec.Code, rec.Body.String())
		}
		return b
	}
	path := "/api/v1/tickets/" + tk.ID + "/sla"

	b := read("attach open", w.do(t, http.MethodPost, path, `{"sla_id":"`+openPolicy.ID+`"}`))
	if b.CalendarID == nil || *b.CalendarID != open || b.ResponseRemaining == nil || *b.ResponseRemaining < 55 || *b.ResponseRemaining > 60 || *b.Paused {
		t.Fatalf("open calendar: %+v", b)
	}
	var explicit bool
	if err = w.f.Admin.QueryRow(ctx, `SELECT sla_explicit FROM ticket_sla WHERE ticket_id = $1`, tk.ID).Scan(&explicit); err != nil || !explicit {
		t.Fatalf("sla_id given: sla_explicit=%v err=%v, want true", explicit, err)
	}

	b = read("attach closed", w.do(t, http.MethodPost, path, `{"sla_id":"`+closedPolicy.ID+`"}`))
	if !*b.Paused || *b.CalendarID != closed {
		t.Fatalf("closed calendar: %+v, want paused", b)
	}

	// Recomputation without sla_id: the best policy for "high" (open),
	// sla_explicit false and the breach notifications reset.
	if _, err = w.f.Admin.Exec(ctx, `UPDATE ticket_sla SET response_breach_notified_at = now(), resolution_breach_notified_at = now() WHERE ticket_id = $1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	b = read("recompute", w.do(t, http.MethodPost, path, ``))
	if b.SLAID != openPolicy.ID {
		t.Fatalf("recompute chose %s, want %s", b.SLAID, openPolicy.ID)
	}
	var notified int
	if err = w.f.Admin.QueryRow(ctx, `SELECT num_nonnulls(response_breach_notified_at, resolution_breach_notified_at), sla_explicit::int FROM ticket_sla WHERE ticket_id = $1`, tk.ID).Scan(&notified, new(int)); err != nil || notified != 0 {
		t.Fatalf("breach notifications after recompute: %d err=%v, want reset", notified, err)
	}
	if err = w.f.Admin.QueryRow(ctx, `SELECT sla_explicit FROM ticket_sla WHERE ticket_id = $1`, tk.ID).Scan(&explicit); err != nil || explicit {
		t.Fatalf("without sla_id: sla_explicit=%v err=%v, want false", explicit, err)
	}

	// Elapsed time: minutes, never paused; after the first response the
	// response remainder is null.
	read("attach elapsed", w.do(t, http.MethodPost, path, `{"sla_id":"`+elapsedPolicy.ID+`"}`))
	if err = w.repo.MarkFirstResponse(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, tk.ID, now); err != nil {
		t.Fatal(err)
	}
	b = read("get elapsed", w.do(t, http.MethodGet, path, ""))
	if b.CalendarID != nil || *b.Paused || b.ResponseRemaining != nil || b.ResolutionRemainig == nil || *b.ResolutionRemainig < 55 || *b.ResolutionRemainig > 60 {
		t.Fatalf("elapsed: %+v", b)
	}

	// An overdue ticket has a negative remainder in the breach list.
	if _, err = w.f.Admin.Exec(ctx, `UPDATE ticket_sla SET resolution_due_at = now() - interval '2 hours' WHERE ticket_id = $1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	rec := w.do(t, http.MethodGet, "/api/v1/slas/breaches?status=breached", "")
	var list struct {
		Data []body `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Data) != 1 ||
		list.Data[0].ResolutionRemainig == nil || *list.Data[0].ResolutionRemainig > -119 || list.Data[0].Paused == nil {
		t.Fatalf("breaches: %d %s", rec.Code, rec.Body.String())
	}
}
