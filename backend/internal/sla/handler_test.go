package sla

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
