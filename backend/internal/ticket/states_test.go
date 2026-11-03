package ticket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// allowed is the transition table of WP-290 (TKT-01), written out once more
// so a change of states.go shows here.
var allowed = map[string][]string{
	"new":         {"open", "in_progress", "closed"},
	"open":        {"in_progress", "waiting", "resolved", "closed"},
	"in_progress": {"open", "waiting", "resolved"},
	"waiting":     {"in_progress", "resolved"},
	"resolved":    {"closed", "open"},
	"closed":      {"open"},
}

func TestTicketStateMachineCoversEveryPair(t *testing.T) {
	if len(Statuses) != len(allowed) {
		t.Fatalf("statuses %v, want the %d of the table", Statuses, len(allowed))
	}
	for _, from := range Statuses {
		for _, to := range Statuses {
			want := false
			for _, a := range allowed[from] {
				want = want || a == to
			}
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
			err := CheckTransition(from, to)
			if want != (err == nil) {
				t.Errorf("CheckTransition(%s, %s) = %v", from, to, err)
			}
			if err != nil && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("CheckTransition(%s, %s): %v is no ErrInvalidTransition", from, to, err)
			}
		}
	}
	if CanTransition("unknown", "open") || CanTransition("open", "unknown") {
		t.Error("an unknown state has a transition")
	}
}

func TestTicketInitialStatus(t *testing.T) {
	for _, status := range Statuses {
		err := CheckInitialStatus(status)
		want := status == StatusNew || status == StatusOpen
		if want != (err == nil) {
			t.Errorf("CheckInitialStatus(%s) = %v", status, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("CheckInitialStatus(%s): %v is no ErrInvalidTransition", status, err)
		}
	}

	repo := NewMemoryRepository()
	defaulted := &Ticket{OrganizationID: "org-1", Title: "a", Priority: "medium", Category: "task"}
	if err := repo.Create(context.Background(), defaulted); err != nil || defaulted.Status != StatusNew {
		t.Fatalf("create without status: status=%q err=%v, want new", defaulted.Status, err)
	}
	if err := repo.Create(context.Background(), &Ticket{OrganizationID: "org-1", Title: "b", Status: StatusOpen}); err != nil {
		t.Fatalf("create as open (workflow): %v", err)
	}
	if err := repo.Create(context.Background(), &Ticket{OrganizationID: "org-1", Title: "c", Status: StatusResolved}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("create as resolved: %v, want ErrInvalidTransition", err)
	}
}

func TestMemoryRepositoryNumbersPerOrganization(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	want := map[string]int{}
	for _, org := range []string{"org-a", "org-b", "org-a", "org-a", "org-b"} {
		tk := &Ticket{OrganizationID: org, Title: "x"}
		if err := repo.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		want[org]++
		if tk.TicketNumber != want[org] {
			t.Errorf("%s: number %d, want %d", org, tk.TicketNumber, want[org])
		}
	}
}

func TestMemoryRepositoryTransitionsAndReopen(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	tk := &Ticket{OrganizationID: "org-1", Title: "x"}
	if err := repo.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	set := func(status string) (*Ticket, error) {
		return repo.Update(ctx, "org-1", tk.ID, UpdateRequest{Status: &status})
	}
	if _, err := set(StatusWaiting); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("new → waiting: %v, want ErrInvalidTransition", err)
	}
	if got, _ := repo.GetByID(ctx, "org-1", tk.ID); got.Status != StatusNew {
		t.Fatalf("status after refused transition: %s, want new", got.Status)
	}
	for _, s := range []string{StatusOpen, StatusOpen, StatusResolved} {
		if _, err := set(s); err != nil {
			t.Fatalf("→ %s: %v", s, err)
		}
	}
	got, _ := repo.GetByID(ctx, "org-1", tk.ID)
	if got.ResolvedAt == "" {
		t.Fatal("resolved without resolved_at")
	}
	if _, err := set(StatusClosed); err != nil {
		t.Fatal(err)
	}
	if got.ClosedAt == "" {
		t.Fatal("closed without closed_at")
	}
	reopened, err := set(StatusOpen)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.ResolvedAt != "" || reopened.ClosedAt != "" {
		t.Fatalf("reopen kept resolved_at=%q closed_at=%q", reopened.ResolvedAt, reopened.ClosedAt)
	}
}

func TestMemoryRepositoryTeamMineIsEmpty(t *testing.T) {
	repo := NewMemoryRepository()
	if err := repo.Create(context.Background(), &Ticket{OrganizationID: "org-1", Title: "x", TeamID: "team-1"}); err != nil {
		t.Fatal(err)
	}
	items, total, err := repo.List(context.Background(), "org-1", FilterParams{TeamMemberOf: "user-1"}, api.PaginationParams{Limit: 10})
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("team_id=mine in memory: total=%d items=%v err=%v, want empty", total, items, err)
	}
}

// ticketRequest sends a request to the ticket routes of an organization.
func ticketRequest(t *testing.T, mux chi.Router, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "00000000-0000-4000-8000-0000000000aa"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestHandlerTicketStatusAndPriority(t *testing.T) {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository()).RegisterRoutes(mux)

	w := ticketRequest(t, mux, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Drucker"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusNew || created.TicketNumber != 1 {
		t.Fatalf("created: status=%s number=%d, want new and 1", created.Status, created.TicketNumber)
	}
	url := "/api/v1/tickets/" + created.ID

	if w = ticketRequest(t, mux, http.MethodPatch, url, map[string]any{"status": "waiting"}); w.Code != http.StatusConflict {
		t.Fatalf("new → waiting: %d %s, want 409", w.Code, w.Body)
	}
	var problem api.ProblemDetail
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Status != http.StatusConflict || problem.Detail == "" {
		t.Fatalf("409 problem: %+v err=%v", problem, err)
	}
	for name, body := range map[string]map[string]any{
		"unknown status":   {"status": "done"},
		"unknown priority": {"priority": "urgent"},
	} {
		if w = ticketRequest(t, mux, http.MethodPatch, url, body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, w.Code, w.Body)
		}
	}
	if w = ticketRequest(t, mux, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "x", "priority": "urgent"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("create with unknown priority: %d %s, want 422", w.Code, w.Body)
	}
	if w = ticketRequest(t, mux, http.MethodPatch, url, map[string]any{"status": "open"}); w.Code != http.StatusOK {
		t.Fatalf("new → open: %d %s", w.Code, w.Body)
	}
	if w = ticketRequest(t, mux, http.MethodGet, "/api/v1/tickets?team_id=mine", nil); w.Code != http.StatusOK {
		t.Fatalf("team_id=mine: %d %s", w.Code, w.Body)
	}
	var list api.ListResponse[Ticket]
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 0 {
		t.Fatalf("team_id=mine in memory: %+v err=%v, want empty", list, err)
	}
}
