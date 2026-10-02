package maintenance

import (
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the maintenance window module.
type Handler struct {
	repo Repository
}

// NewHandler creates a new maintenance handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers maintenance routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/maintenance-windows", h.List)
	r.Post("/api/v1/maintenance-windows", h.Create)
	r.Get("/api/v1/maintenance-windows/{id}", h.Get)
	r.Patch("/api/v1/maintenance-windows/{id}", h.Update)
	r.Delete("/api/v1/maintenance-windows/{id}", h.Delete)
	r.Post("/api/v1/maintenance-windows/{id}/notify", h.Notify)
	r.Get("/api/v1/maintenance-windows/{id}/notifications", h.ListNotifications)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, FilterParams{Status: r.URL.Query().Get("status")}, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Window]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "maintenance window not found")
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
	var req CreateWindowRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
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
	if !endsAt.After(startsAt) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ends_at must be after starts_at")
		return
	}
	win := &Window{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		StartsAt:       startsAt,
		EndsAt:         endsAt,
		Status:         "scheduled",
		CreatedBy:      t.UserID,
		CIIDs:          req.CIIDs,
	}
	if err := h.repo.Create(r.Context(), win); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, win)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateWindowRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case "scheduled", "in_progress", "completed", "cancelled":
		default:
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid status")
			return
		}
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "maintenance window not found")
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "maintenance window not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Notify handles POST /api/v1/maintenance-windows/{id}/notify — derives the
// affected clients from the window's CIs and records one notification each.
func (h *Handler) Notify(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	notifications, err := h.repo.NotifyClients(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "maintenance window not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"notified": len(notifications), "notifications": notifications})
}

// ListNotifications handles GET /api/v1/maintenance-windows/{id}/notifications.
func (h *Handler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	notifications, err := h.repo.ListNotifications(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": notifications})
}
