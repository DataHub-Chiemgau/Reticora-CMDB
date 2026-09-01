package consumable

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the consumable stock module.
type Handler struct {
	repo Repository
}

// NewHandler creates a new consumable handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers consumable routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/consumables", h.List)
	r.Post("/api/v1/consumables", h.Create)
	r.Get("/api/v1/consumables/{id}", h.Get)
	r.Patch("/api/v1/consumables/{id}", h.Update)
	r.Delete("/api/v1/consumables/{id}", h.Delete)
	r.Get("/api/v1/consumables/{id}/movements", h.ListMovements)
	r.Post("/api/v1/consumables/{id}/movements", h.AddMovement)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		Category: r.URL.Query().Get("category"),
		ClientID: r.URL.Query().Get("client_id"),
		LowStock: r.URL.Query().Get("low_stock") == "true",
		Search:   r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Consumable]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
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
	var req CreateConsumableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	c := &Consumable{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		SKU:            req.SKU,
		Category:       req.Category,
		Unit:           req.Unit,
		StockLevel:     req.StockLevel,
		MinLevel:       req.MinLevel,
		Location:       req.Location,
		Notes:          req.Notes,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateConsumableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListMovements(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Movement]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) AddMovement(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateMovementRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Direction != "in" && req.Direction != "out" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "direction must be in or out")
		return
	}
	if req.Quantity <= 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "quantity must be positive")
		return
	}
	m := &Movement{
		OrganizationID: t.OrganizationID,
		ConsumableID:   chi.URLParam(r, "id"),
		Direction:      req.Direction,
		Quantity:       req.Quantity,
		Reason:         req.Reason,
		Reference:      req.Reference,
		ActorID:        t.UserID,
	}
	c, err := h.repo.AddMovement(r.Context(), m)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient stock") {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"movement": m, "consumable": c})
}
