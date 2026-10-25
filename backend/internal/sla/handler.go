package sla

import (
	"context"
	"errors"
	"log/slog"
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

// writeSLAError answers a failed SLA request: a policy, ticket or ticket SLA
// state that is unknown, of another organization or outside the caller's
// scope is 404 with a fixed text (TEN-04). Database errors go through
// api.WriteRepoError; no table, constraint, policy or SQLSTATE text reaches
// the client (API-03, F-047), the log keeps it.
func writeSLAError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrTicketSLANotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTicketSLANotFound.Error())
	case errors.Is(err, ticket.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ticket.ErrNotFound.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "sla request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListPolicies(r.Context(), t.OrganizationID, r.URL.Query().Get("priority"), r.URL.Query().Get("client_id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Policy]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetPolicy(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeSLAError(w, r, err)
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
	req.Normalize()
	if req.Name == "" || req.Priority == "" || req.ResponseTargetMinutes <= 0 || req.ResolutionTargetMinutes <= 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name, priority and positive targets are required")
		return
	}
	if req.ClientID != "" {
		if err := api.RequireUUID("client_id", req.ClientID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	item := &Policy{OrganizationID: t.OrganizationID, ClientID: req.ClientID, Name: req.Name, Priority: req.Priority, ResponseTargetMinutes: req.ResponseTargetMinutes, ResolutionTargetMinutes: req.ResolutionTargetMinutes, BusinessCalendar: req.BusinessCalendar}
	if err := h.repo.CreatePolicy(r.Context(), item); err != nil {
		writeSLAError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpdatePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.ClientID != nil && *req.ClientID != "" {
		if err := api.RequireUUID("client_id", *req.ClientID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	item, err := h.repo.UpdatePolicy(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		writeSLAError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeletePolicy(r.Context(), t.OrganizationID, id); err != nil {
		writeSLAError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AttachTicketSLA(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ticket.ErrNotFound.Error())
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
	// sla_id reaches SQL only as a UUID (F-047).
	if req.SLAID != "" {
		if err := api.RequireUUID("sla_id", req.SLAID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	ticketItem, err := h.tickets.GetByID(r.Context(), t.OrganizationID, ticketID)
	if err != nil {
		writeSLAError(w, r, err)
		return
	}
	state, err := h.repo.ApplyForTicket(r.Context(), t.OrganizationID, ticketItem, req.SLAID)
	switch {
	case errors.Is(err, ErrNotFound) && req.SLAID != "":
		// A named policy that is unknown, of another organization or
		// outside the caller's scope: 422 on sla_id, alike (TEN-04).
		api.WriteFieldError(w, &api.FieldError{Field: "sla_id", Message: ErrNotFound.Error(), Status: http.StatusUnprocessableEntity})
		return
	case errors.Is(err, ErrNotFound):
		// No policy matches the priority of the ticket.
		api.WriteError(w, http.StatusBadRequest, "Bad Request", ErrNotFound.Error())
		return
	case err != nil:
		writeSLAError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, state)
}

func (h *Handler) GetTicketSLA(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "id", ErrTicketSLANotFound.Error())
	if !ok {
		return
	}
	state, err := h.repo.GetForTicket(r.Context(), t.OrganizationID, ticketID)
	if err != nil {
		writeSLAError(w, r, err)
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
	items, total, err := h.repo.ListBreaches(r.Context(), t.OrganizationID, BreachFilter{Status: r.URL.Query().Get("status")}, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[TicketSLA]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

// TicketHooks lets the ticket handler keep SLA state consistent without taking
// a dependency on this package.
type TicketHooks struct{ Repo Repository }

func (h TicketHooks) ApplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket) error {
	_, err := h.Repo.ApplyForTicket(ctx, orgID, t, "")
	return err
}
func (h TicketHooks) MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return h.Repo.MarkFirstResponse(ctx, orgID, ticketID, at)
}
func (h TicketHooks) MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return h.Repo.MarkResolved(ctx, orgID, ticketID, at)
}
