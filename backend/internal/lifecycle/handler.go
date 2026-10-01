package lifecycle

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EntityResolver resolves the lifecycle definition key applicable to an
// entity (asset: its type's lifecycle or the default physical_asset; ci: the
// CI type's lifecycle definition).
type EntityResolver interface {
	// LifecycleKeyFor returns the lifecycle definition key for an entity and
	// whether the entity exists in the tenant scope.
	LifecycleKeyFor(r *http.Request, orgID, entityType, entityID string) (string, error)
}

// Handler provides HTTP handlers for lifecycle definitions and transitions.
type Handler struct {
	repo     Repository
	service  *Service
	resolver EntityResolver
}

// NewHandler creates a lifecycle handler. resolver may be nil when transition
// endpoints are not served.
func NewHandler(repo Repository, service *Service, resolver EntityResolver) *Handler {
	return &Handler{repo: repo, service: service, resolver: resolver}
}

// RegisterRoutes registers lifecycle routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/lifecycle-definitions", h.List)
	r.Post("/api/v1/lifecycle-definitions", h.Create)
	r.Get("/api/v1/lifecycle-definitions/{id}", h.Get)
	r.Delete("/api/v1/lifecycle-definitions/{id}", h.Delete)
	r.Post("/api/v1/assets/{id}/lifecycle-transitions", h.TransitionAsset)
	r.Post("/api/v1/cis/{id}/lifecycle-transitions", h.TransitionCI)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Definition]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "lifecycle definition not found")
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
	var req CreateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Key == "" || req.Name == "" || len(req.States) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "key, name and at least one state are required")
		return
	}
	def := &Definition{
		OrganizationID: t.OrganizationID,
		Key:            req.Key,
		Name:           req.Name,
		AppliesTo:      req.AppliesTo,
		Description:    req.Description,
	}
	for _, s := range req.States {
		def.States = append(def.States, State{
			Key: s.Key, Label: s.Label, IsInitial: s.IsInitial,
			IsTerminal: s.IsTerminal, SortOrder: s.SortOrder, RequiredFields: s.RequiredFields,
		})
	}
	for _, tr := range req.Transitions {
		def.Transitions = append(def.Transitions, Transition{
			FromStateKey: tr.FromStateKey, ToStateKey: tr.ToStateKey,
			Key: tr.Key, Label: tr.Label, RequiredFields: tr.RequiredFields,
		})
	}
	if err := h.repo.Create(r.Context(), def); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, def)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "lifecycle definition not found or protected")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TransitionAsset handles POST /api/v1/assets/{id}/lifecycle-transitions
func (h *Handler) TransitionAsset(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "asset")
}

// TransitionCI handles POST /api/v1/cis/{id}/lifecycle-transitions
func (h *Handler) TransitionCI(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "ci")
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.service == nil || h.resolver == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "lifecycle transitions are not configured")
		return
	}
	var req TransitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	entityID := chi.URLParam(r, "id")
	defKey, err := h.resolver.LifecycleKeyFor(r, t.OrganizationID, entityType, entityID)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "entity not found")
		return
	}
	result, err := h.service.Transition(r.Context(), t.OrganizationID, entityType, entityID, defKey, req)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}
