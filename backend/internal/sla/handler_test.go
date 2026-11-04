package sla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

func doReq(mux chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func setupSLA() (*MemoryRepository, *ticket.MemoryRepository, chi.Router) {
	slaRepo := NewMemoryRepository()
	ticketRepo := ticket.NewMemoryRepository()
	h := NewHandler(slaRepo, ticketRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return slaRepo, ticketRepo, mux
}

func TestSLAPolicyCRUDAndAttach(t *testing.T) {
	_, ticketRepo, mux := setupSLA()
	w := doReq(mux, http.MethodPost, "/api/v1/slas", `{"name":"High","priority":"high","response_target_minutes":30,"resolution_target_minutes":240,"business_calendar":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create policy: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var policy Policy
	json.Unmarshal(w.Body.Bytes(), &policy)
	if policy.ID == "" || !policy.BusinessCalendar {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	ticketItem := &ticket.Ticket{OrganizationID: "org-1", Title: "Router down", Status: "open", Priority: "high", Category: "incident", ReporterID: "user-1", CreatedAt: time.Now().UTC()}
	if err := ticketRepo.Create(context.Background(), ticketItem); err != nil {
		t.Fatal(err)
	}

	w = doReq(mux, http.MethodPost, "/api/v1/tickets/"+ticketItem.ID+"/sla", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("attach: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var state TicketSLA
	json.Unmarshal(w.Body.Bytes(), &state)
	if state.SLAID != policy.ID || state.TicketID != ticketItem.ID {
		t.Fatalf("unexpected ticket sla: %#v", state)
	}

	w = doReq(mux, http.MethodGet, "/api/v1/slas/breaches?status=breached", "")
	if w.Code != http.StatusOK {
		t.Fatalf("breaches: expected 200, got %d", w.Code)
	}
}

func TestSLAValidationAndTicketHooks(t *testing.T) {
	slaRepo, ticketRepo, mux := setupSLA()
	if w := doReq(mux, http.MethodPost, "/api/v1/slas", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid policy: expected 400, got %d", w.Code)
	}
	p := &Policy{OrganizationID: "org-1", Name: "Medium", Priority: "medium", ResponseTargetMinutes: 60, ResolutionTargetMinutes: 480}
	if err := slaRepo.CreatePolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ticketItem := &ticket.Ticket{OrganizationID: "org-1", Title: "Need help", Status: "open", Priority: "medium", Category: "request", ReporterID: "user-1", CreatedAt: time.Now().UTC()}
	if err := ticketRepo.Create(context.Background(), ticketItem); err != nil {
		t.Fatal(err)
	}
	hooks := TicketHooks{Repo: slaRepo}
	if err := hooks.ApplyForTicket(context.Background(), "org-1", ticketItem); err != nil {
		t.Fatal(err)
	}
	if err := hooks.MarkFirstResponse(context.Background(), "org-1", ticketItem.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state, err := slaRepo.GetForTicket(context.Background(), "org-1", ticketItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.FirstResponseAt == nil {
		t.Fatal("expected first response timestamp")
	}
}

// problemField returns status and field of a problem response.
func problemField(t *testing.T, w *httptest.ResponseRecorder) (status int, field string) {
	t.Helper()
	var p struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return w.Code, p.Field
}

// TestBusinessCalendarValidationMemory (TKT-02, WP-768): every rule of
// Calendar.Validate, the name, a client_id in PATCH and a second default
// answer with the field over the memory repository (--no-db).
func TestBusinessCalendarValidationMemory(t *testing.T) {
	_, _, mux := setupSLA()
	cases := []struct{ name, body, field string }{
		{"empty name", `{"name":" "}`, "name"},
		{"time zone", `{"name":"x","timezone":"Mars/Olympus"}`, "timezone"},
		{"abbreviation", `{"name":"x","timezone":"CET"}`, "timezone"},
		{"weekday", `{"name":"x","weekly_hours":[{"weekday":8,"start":"08:00","end":"10:00"}]}`, "weekly_hours"},
		{"start after end", `{"name":"x","weekly_hours":[{"weekday":1,"start":"10:00","end":"08:00"}]}`, "weekly_hours"},
		{"malformed time", `{"name":"x","weekly_hours":[{"weekday":1,"start":"8:00","end":"10:00"}]}`, "weekly_hours"},
		{"overlap", `{"name":"x","weekly_hours":[{"weekday":1,"start":"08:00","end":"12:00"},{"weekday":1,"start":"11:00","end":"13:00"}]}`, "weekly_hours"},
		{"empty windows", `{"name":"x","weekly_hours":[]}`, "weekly_hours"},
		{"duplicate holiday", `{"name":"x","holidays":[{"date":"2026-12-24"},{"date":"2026-12-24","name":"again"}]}`, "holidays"},
		{"malformed holiday", `{"name":"x","holidays":[{"date":"24.12.2026"}]}`, "holidays"},
		{"client id", `{"name":"x","client_id":"client-1"}`, "client_id"},
	}
	for _, c := range cases {
		if code, field := problemField(t, doReq(mux, http.MethodPost, "/api/v1/business-calendars", c.body)); code != http.StatusBadRequest || field != c.field {
			t.Errorf("%s: got %d on %q, want 400 on %s", c.name, code, field, c.field)
		}
	}

	w := doReq(mux, http.MethodPost, "/api/v1/business-calendars", `{"name":"Standard","is_default":true,"timezone":"Europe/Berlin","holidays":[{"date":"2026-12-24","name":"Heiligabend"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var cal BusinessCalendar
	if err := json.Unmarshal(w.Body.Bytes(), &cal); err != nil || cal.ClientID != nil || cal.Timezone == nil || *cal.Timezone != "Europe/Berlin" || len(cal.WeeklyHours) != 5 || !cal.IsDefault {
		t.Fatalf("created calendar: %s", w.Body.String())
	}
	if code, field := problemField(t, doReq(mux, http.MethodPost, "/api/v1/business-calendars", `{"name":"second","is_default":true}`)); code != http.StatusConflict || field != "is_default" {
		t.Fatalf("second default: %d on %q, want 409 on is_default", code, field)
	}
	path := "/api/v1/business-calendars/" + cal.ID
	for _, c := range []struct{ name, body, field string }{
		{"client id", `{"client_id":null}`, "client_id"},
		{"empty windows", `{"weekly_hours":[]}`, "weekly_hours"},
		{"time zone", `{"timezone":"Local"}`, "timezone"},
		{"duplicate holiday", `{"holidays":[{"date":"2026-01-01"},{"date":"2026-01-01"}]}`, "holidays"},
	} {
		if code, field := problemField(t, doReq(mux, http.MethodPatch, path, c.body)); code != http.StatusBadRequest || field != c.field {
			t.Errorf("patch %s: got %d on %q, want 400 on %s", c.name, code, field, c.field)
		}
	}
	w = doReq(mux, http.MethodPatch, path, `{"timezone":null,"weekly_hours":[{"weekday":6,"start":"10:00","end":"12:00"}]}`)
	if err := json.Unmarshal(w.Body.Bytes(), &cal); w.Code != http.StatusOK || err != nil || cal.Timezone != nil || len(cal.WeeklyHours) != 1 || len(cal.Holidays) != 1 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if w = doReq(mux, http.MethodGet, "/api/v1/business-calendars/00000000-0000-4000-8000-999999999999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

// TestBusinessCalendarPagingMemory: the list pages by name and id with the
// signed keyset cursor; a forged cursor and a cursor of another order are
// 400; deleting a calendar clears the policy's calendar_id (WP-768).
func TestBusinessCalendarPagingMemory(t *testing.T) {
	repo, _, mux := setupSLA()
	for _, name := range []string{"Echo", "Alpha", "Delta", "Bravo", "Charlie"} {
		if w := doReq(mux, http.MethodPost, "/api/v1/business-calendars", `{"name":"`+name+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	var names []string
	next := ""
	for pages := 0; pages < 5; pages++ {
		w := doReq(mux, http.MethodGet, "/api/v1/business-calendars?limit=2&cursor="+next, "")
		var list BusinessCalendarListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &list); w.Code != http.StatusOK || err != nil || list.Total != 5 {
			t.Fatalf("page %d: %d %s", pages, w.Code, w.Body.String())
		}
		for _, c := range list.Data {
			names = append(names, c.Name)
		}
		if next = list.NextCursor; next == "" {
			break
		}
	}
	if got := fmt.Sprint(names); got != "[Alpha Bravo Charlie Delta Echo]" {
		t.Fatalf("pages: %s", got)
	}
	if w := doReq(mux, http.MethodGet, "/api/v1/business-calendars?cursor=forged.cursor", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("forged cursor: %d", w.Code)
	}
	other := api.EncodeCursor(api.Cursor{Sort: "created_at", Dir: "desc", Value: "x", ID: "00000000-0000-4000-8000-000000000001"})
	if w := doReq(mux, http.MethodGet, "/api/v1/business-calendars?cursor="+other, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("cursor of another order: %d", w.Code)
	}
	if w := doReq(mux, http.MethodGet, "/api/v1/business-calendars?client_id=nope", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed client_id filter: %d", w.Code)
	}

	list, _, err := repo.ListCalendars(context.Background(), "org-1", BusinessCalendarFilter{}, api.PaginationParams{Limit: 1})
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	calID := list[0].ID
	p := &Policy{OrganizationID: "org-1", Name: "p", Priority: "low", ResponseTargetMinutes: 10, ResolutionTargetMinutes: 20, BusinessCalendar: true, CalendarID: &calID}
	if err = repo.CreatePolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if w := doReq(mux, http.MethodDelete, "/api/v1/business-calendars/"+calID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := doReq(mux, http.MethodDelete, "/api/v1/business-calendars/"+calID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", w.Code)
	}
	if got, _ := repo.GetPolicy(context.Background(), "org-1", p.ID); got.CalendarID != nil {
		t.Fatalf("policy calendar after delete: %v", *got.CalendarID)
	}
}

// featureSet is an entitlement.Checker with fixed features.
type featureSet map[string]bool

func (f featureSet) IsEnabled(_ context.Context, _, key string) bool { return f[key] }

// TestTicketHooksStandardMemory covers TKT-01 (E-39, WP-769) over the
// memory repository: without ticketing_standard the hooks apply no SLA,
// choose none again and reset none; with it a new priority chooses the
// policy again (from the ticket's creation, first response kept), a named
// policy stays, and a reopen clears the resolution of a resolved ticket
// only.
func TestTicketHooksStandardMemory(t *testing.T) {
	ctx := context.Background()
	slaRepo, ticketRepo, _ := setupSLA()
	medium := &Policy{OrganizationID: "org-1", Name: "Medium", Priority: "medium", ResponseTargetMinutes: 60, ResolutionTargetMinutes: 480}
	high := &Policy{OrganizationID: "org-1", Name: "High", Priority: "high", ResponseTargetMinutes: 15, ResolutionTargetMinutes: 120}
	for _, p := range []*Policy{medium, high} {
		if err := slaRepo.CreatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	newTicket := func(title string) *ticket.Ticket {
		t.Helper()
		tk := &ticket.Ticket{OrganizationID: "org-1", Title: title, Status: "open", Priority: "medium", Category: "incident", ReporterID: "user-1"}
		if err := ticketRepo.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		tk.CreatedAt = created
		return tk
	}

	essential := TicketHooks{Repo: slaRepo, Features: featureSet{}}
	tk := newTicket("essential")
	if err := essential.ApplyForTicket(ctx, "org-1", tk); err != nil {
		t.Fatal(err)
	}
	if err := essential.ReapplyForTicket(ctx, "org-1", tk); err != nil {
		t.Fatal(err)
	}
	if _, err := slaRepo.GetForTicket(ctx, "org-1", tk.ID); !errors.Is(err, ErrTicketSLANotFound) {
		t.Fatalf("SLA state without ticketing_standard: %v, want none", err)
	}

	standard := TicketHooks{Repo: slaRepo, Features: featureSet{"ticketing_standard": true}}
	tk = newTicket("standard")
	if err := standard.ApplyForTicket(ctx, "org-1", tk); err != nil {
		t.Fatal(err)
	}
	if err := standard.MarkFirstResponse(ctx, "org-1", tk.ID, created.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tk.Priority = "high"
	if err := standard.ReapplyForTicket(ctx, "org-1", tk); err != nil {
		t.Fatal(err)
	}
	state, err := slaRepo.GetForTicket(ctx, "org-1", tk.ID)
	if err != nil || state.SLAID != high.ID || !state.ResolutionDueAt.Equal(created.Add(120*time.Minute)) ||
		state.FirstResponseAt == nil || !state.ResolutionBreached {
		t.Fatalf("after the new priority: %+v err=%v, want the high policy from the creation, first response kept", state, err)
	}
	// Without a policy for the new priority the state stays.
	tk.Priority = "low"
	if err = standard.ReapplyForTicket(ctx, "org-1", tk); err != nil {
		t.Fatal(err)
	}
	if state, _ = slaRepo.GetForTicket(ctx, "org-1", tk.ID); state.SLAID != high.ID {
		t.Fatalf("without a policy of the priority: %s, want the high policy kept", state.SLAID)
	}

	// A named policy stays.
	named := newTicket("named")
	if _, err = slaRepo.ApplyForTicket(ctx, "org-1", named, medium.ID); err != nil {
		t.Fatal(err)
	}
	named.Priority = "high"
	if err = standard.ReapplyForTicket(ctx, "org-1", named); err != nil {
		t.Fatal(err)
	}
	if state, _ = slaRepo.GetForTicket(ctx, "org-1", named.ID); state.SLAID != medium.ID {
		t.Fatalf("named policy: %s, want %s", state.SLAID, medium.ID)
	}

	// Reopen: only a resolved state changes; without the feature none.
	if err = standard.MarkReopened(ctx, "org-1", tk.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if state, _ = slaRepo.GetForTicket(ctx, "org-1", tk.ID); state.ResolvedAt != nil {
		t.Fatal("reopen of an unresolved state set resolved_at")
	}
	resolvedAt := created.Add(30 * time.Minute)
	if err = standard.MarkResolved(ctx, "org-1", tk.ID, resolvedAt); err != nil {
		t.Fatal(err)
	}
	if err = essential.MarkReopened(ctx, "org-1", tk.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if state, _ = slaRepo.GetForTicket(ctx, "org-1", tk.ID); state.ResolvedAt == nil || state.ResolutionBreached {
		t.Fatalf("reopen without ticketing_standard changed the state: %+v", state)
	}
	if err = standard.MarkReopened(ctx, "org-1", tk.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if state, _ = slaRepo.GetForTicket(ctx, "org-1", tk.ID); state.ResolvedAt != nil || !state.ResolutionBreached ||
		!state.ResolutionDueAt.Equal(created.Add(120*time.Minute)) {
		t.Fatalf("after the reopen: %+v, want no resolution, the overdue due time kept", state)
	}
}
