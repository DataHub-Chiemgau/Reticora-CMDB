package reservation

import (
	"context"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes reservation events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// AvailabilityProvider computes the availability projection for an item.
type AvailabilityProvider interface {
	Availability(ctx context.Context, orgID string, filter AvailabilityFilter) ([]Availability, error)
}

// Handler provides HTTP handlers for reservations and availability.
type Handler struct {
	repo         Repository
	dispatcher   EventDispatcher
	availability AvailabilityProvider
}

// NewHandler creates a new reservation handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// WithAvailability attaches the availability projection provider.
func (h *Handler) WithAvailability(p AvailabilityProvider) *Handler {
	h.availability = p
	return h
}

// RegisterRoutes registers reservation routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/reservations", h.List)
	r.Post("/api/v1/reservations", h.Create)
	r.Get("/api/v1/reservations/{id}", h.Get)
	r.Post("/api/v1/reservations/{id}/release", h.Release)
	r.Get("/api/v1/inventory/availability", h.Availability)
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, r.URL.Query().Get("state"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Reservation]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "reservation not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.AssetID == "" && req.QuantityItemID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or quantity_item_id is required")
		return
	}
	res := &Reservation{
		OrganizationID: t.OrganizationID,
		ItemKind:       req.ItemKind,
		AssetID:        req.AssetID,
		QuantityItemID: req.QuantityItemID,
		Quantity:       1,
		AssigneeID:     req.AssigneeID,
		ProjectRef:     req.ProjectRef,
		OrderID:        req.OrderID,
		TicketID:       req.TicketID,
		Reason:         req.Reason,
	}
	if res.ItemKind == "" {
		if req.AssetID != "" {
			res.ItemKind = "asset"
		} else {
			res.ItemKind = "quantity_item"
		}
	}
	if req.Quantity != nil {
		if *req.Quantity <= 0 {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "quantity must be positive")
			return
		}
		res.Quantity = *req.Quantity
	}
	if req.ReservedFrom != "" {
		ts, err := time.Parse(time.RFC3339, req.ReservedFrom)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid reserved_from")
			return
		}
		res.ReservedFrom = ts
	}
	if req.ReservedUntil != "" {
		ts, err := time.Parse(time.RFC3339, req.ReservedUntil)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid reserved_until")
			return
		}
		res.ReservedUntil = &ts
	}
	if req.ExpiresAt != "" {
		ts, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid expires_at")
			return
		}
		res.ExpiresAt = &ts
	}
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		res.CreatedBy = p.Subject
	}
	if err := h.repo.Create(r.Context(), res); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "reservation.created", res)
	api.WriteJSON(w, http.StatusCreated, res)
}

// Release handles POST /api/v1/reservations/{id}/release
func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.Transition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), "released")
	if err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "reservation.released", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Availability handles GET /api/v1/inventory/availability
func (h *Handler) Availability(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.availability == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "availability projection is not configured")
		return
	}
	items, err := h.availability.Availability(r.Context(), t.OrganizationID, AvailabilityFilter{
		ItemKind: r.URL.Query().Get("item_kind"),
		ItemID:   r.URL.Query().Get("item_id"),
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}
