package sla

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

// Handler exposes SLA policy and ticket-state endpoints.
type Handler struct {
	repo    Repository
	tickets ticket.Repository
}

// NewHandler creates an SLA handler.
func NewHandler(repo Repository, tickets ticket.Repository) *Handler {
	return &Handler{repo: repo, tickets: tickets}
}

// RegisterRoutes registers SLA routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/slas", h.ListPolicies)
	r.Post("/api/v1/slas", h.CreatePolicy)
	r.Get("/api/v1/slas/breaches", h.ListBreaches)
	r.Get("/api/v1/slas/{id}", h.GetPolicy)
	r.Patch("/api/v1/slas/{id}", h.UpdatePolicy)
	r.Delete("/api/v1/slas/{id}", h.DeletePolicy)
	r.Get("/api/v1/tickets/{id}/sla", h.GetTicketSLA)
	r.Post("/api/v1/tickets/{id}/sla", h.AttachTicketSLA)
}

func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}

func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListPolicies(t.OrganizationID, r.URL.Query().Get("priority"), r.URL.Query().Get("client_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Policy]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetPolicy(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "sla policy not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req CreatePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Priority == "" || req.ResponseTargetMinutes <= 0 || req.ResolutionTargetMinutes <= 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name, priority and positive targets are required")
		return
	}
	item := &Policy{OrganizationID: t.OrganizationID, ClientID: req.ClientID, Name: req.Name, Priority: req.Priority, ResponseTargetMinutes: req.ResponseTargetMinutes, ResolutionTargetMinutes: req.ResolutionTargetMinutes, BusinessCalendar: req.BusinessCalendar}
	if err := h.repo.CreatePolicy(item); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdatePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.UpdatePolicy(t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "sla policy not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeletePolicy(t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "sla policy not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AttachTicketSLA(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req AttachTicketRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	ticketItem, err := h.tickets.GetByID(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
		return
	}
	state, err := h.repo.ApplyForTicket(t.OrganizationID, ticketItem, req.SLAID)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, state)
}

func (h *Handler) GetTicketSLA(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	state, err := h.repo.GetForTicket(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket sla not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, state)
}

func (h *Handler) ListBreaches(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListBreaches(t.OrganizationID, BreachFilter{Status: r.URL.Query().Get("status")}, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[TicketSLA]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

// TicketHooks lets the ticket handler keep SLA state consistent without taking
// a dependency on this package.
type TicketHooks struct{ Repo Repository }

func (h TicketHooks) ApplyForTicket(orgID string, t *ticket.Ticket) error {
	_, err := h.Repo.ApplyForTicket(orgID, t, "")
	return err
}
func (h TicketHooks) MarkFirstResponse(orgID, ticketID string, at time.Time) error {
	return h.Repo.MarkFirstResponse(orgID, ticketID, at)
}
func (h TicketHooks) MarkResolved(orgID, ticketID string, at time.Time) error {
	return h.Repo.MarkResolved(orgID, ticketID, at)
}
