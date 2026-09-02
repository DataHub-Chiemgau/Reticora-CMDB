package composition

import (
	"context"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes composition events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// Handler provides HTTP handlers for parent-asset/child composition.
type Handler struct {
	repo       Repository
	dispatcher EventDispatcher
}

// NewHandler creates a new composition handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// RegisterRoutes registers composition routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assets/{id}/children", h.ListChildren)
	r.Get("/api/v1/compositions", h.List)
	r.Post("/api/v1/compositions", h.Create)
	r.Get("/api/v1/compositions/{id}", h.Get)
	r.Patch("/api/v1/compositions/{id}", h.Update)
	r.Delete("/api/v1/compositions/{id}", h.Delete)
}

// List handles GET /api/v1/compositions
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, r.URL.Query().Get("parent_asset_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Composition]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

// ListChildren handles GET /api/v1/assets/{id}/children
func (h *Handler) ListChildren(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Composition]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Create handles POST /api/v1/compositions
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
	if req.ParentAssetID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "parent_asset_id is required")
		return
	}
	if (req.ChildCIID == "") == (req.ChildAssetID == "") {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "exactly one of child_ci_id or child_asset_id is required")
		return
	}
	configurationOnly := true
	if req.ConfigurationOnly != nil {
		configurationOnly = *req.ConfigurationOnly
	}
	c := &Composition{
		OrganizationID:                t.OrganizationID,
		ParentAssetID:                 req.ParentAssetID,
		ChildCIID:                     req.ChildCIID,
		ChildAssetID:                  req.ChildAssetID,
		Role:                          req.Role,
		Position:                      req.Position,
		ConfigurationOnly:             configurationOnly,
		IndependentlySerialized:       req.IndependentlySerialized,
		IndependentlyAssignable:       req.IndependentlyAssignable,
		IndependentlyLocatable:        req.IndependentlyLocatable,
		IndependentlyLifecycleManaged: req.IndependentlyLifecycleManaged,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.created", c)
	api.WriteJSON(w, http.StatusCreated, c)
}

// Get handles GET /api/v1/compositions/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Update handles PATCH /api/v1/compositions/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.updated", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/compositions/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.deleted", map[string]string{"id": chi.URLParam(r, "id")})
	w.WriteHeader(http.StatusNoContent)
}
