package desk

import (
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for desk booking.
type Handler struct {
	repo Repository
}

// NewHandler creates a new desk handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers desk booking routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/desks", h.List)
	r.Post("/api/v1/desks", h.Create)
	r.Get("/api/v1/desks/{id}", h.Get)
	r.Patch("/api/v1/desks/{id}", h.Update)
	r.Delete("/api/v1/desks/{id}", h.Delete)
	r.Get("/api/v1/desks/{id}/bookings", h.ListBookings)
	r.Post("/api/v1/desks/{id}/bookings", h.Book)
	r.Post("/api/v1/desk-bookings/{id}/cancel", h.CancelBooking)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		RoomID: r.URL.Query().Get("room_id"),
		Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Desk]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "desk not found")
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
	var req CreateDeskRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	d := &Desk{
		OrganizationID: t.OrganizationID,
		RoomID:         req.RoomID,
		Name:           req.Name,
		Attributes:     req.Attributes,
	}
	if err := h.repo.Create(r.Context(), d); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, d)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateDeskRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "desk not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "desk not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListBookings(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.repo.ListBookings(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// Book handles POST /api/v1/desks/{id}/bookings.
func (h *Handler) Book(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateBookingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "starts_at must be RFC3339")
		return
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ends_at must be RFC3339")
		return
	}
	userID := req.UserID
	if userID == "" {
		userID = t.UserID
	}
	if userID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "user_id is required")
		return
	}
	b := &Booking{
		OrganizationID: t.OrganizationID,
		DeskID:         chi.URLParam(r, "id"),
		UserID:         userID,
		StartsAt:       startsAt,
		EndsAt:         endsAt,
	}
	created, err := h.repo.Book(r.Context(), b)
	if err != nil {
		if strings.Contains(err.Error(), "already booked") {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		if strings.Contains(err.Error(), "after starts_at") {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "desk not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

// CancelBooking handles POST /api/v1/desk-bookings/{id}/cancel.
func (h *Handler) CancelBooking(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.CancelBooking(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "booking not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}
