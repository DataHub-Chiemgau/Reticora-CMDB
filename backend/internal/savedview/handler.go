package savedview

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for saved views.
type Handler struct {
	repo  Repository
	query QueryEngine
}

// NewHandler creates a new saved view handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithQueryEngine attaches the filter-DSL query engine (spec §17). Without
// one, the query endpoint returns 501.
func (h *Handler) WithQueryEngine(engine QueryEngine) *Handler {
	h.query = engine
	return h
}

// RegisterRoutes registers saved view routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/saved-views", h.List)
	r.Post("/api/v1/saved-views", h.Create)
	r.Get("/api/v1/saved-views/presets", h.PresetList)
	r.Get("/api/v1/saved-views/{id}", h.Get)
	r.Patch("/api/v1/saved-views/{id}", h.Update)
	r.Delete("/api/v1/saved-views/{id}", h.Delete)
	r.Post("/api/v1/search/query", h.Query)
}

func ownerID(r *http.Request) string {
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		return p.Subject
	}
	return ""
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, ownerID(r), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[View]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// PresetList handles GET /api/v1/saved-views/presets
func (h *Handler) PresetList(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": Presets()})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "saved view not found")
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
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	view := &View{
		OrganizationID: t.OrganizationID,
		OwnerID:        ownerID(r),
		Name:           req.Name,
		EntityKind:     req.EntityKind,
		FilterSpec:     req.FilterSpec,
		Shared:         req.Shared,
	}
	if err := h.repo.Create(r.Context(), view); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, view)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), ownerID(r), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "saved view not found")
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
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), ownerID(r)); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "saved view not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Query handles POST /api/v1/search/query — executes a structured filter spec
// (the saved-view DSL, spec §17) against CIs or assets. The body is the
// filter spec itself; entity_kind selects the entity ("ci" default, "asset").
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.query == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "filter query engine is not configured")
		return
	}
	var spec FilterSpec
	if err := api.ReadJSON(r, &spec); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.query.Query(r.Context(), t.OrganizationID, spec, page)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[QueryResult]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}
