package relationshiptype

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for relationship type metadata.
type Handler struct {
	repo Repository
}

// NewHandler creates a new relationship type handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers relationship type routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/relationship-types", h.List)
	r.Post("/api/v1/relationship-types", h.Create)
	r.Get("/api/v1/relationship-types/{key}", h.Get)
	r.Patch("/api/v1/relationship-types/{key}", h.Update)
	r.Delete("/api/v1/relationship-types/{key}", h.Delete)
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
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Type]{
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
	item, err := h.repo.GetByKey(r.Context(), t.OrganizationID, chi.URLParam(r, "key"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found")
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
	if req.Key == "" || req.DisplayName == "" || req.ReverseName == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "key, display_name and reverse_name are required")
		return
	}
	// Defaults of a custom type: directed, logical, the source depends on
	// the target.
	typ := &Type{
		OrganizationID: t.OrganizationID, Key: req.Key,
		Directed: true, Category: "logical", ImpactDirection: ImpactSourceDependsOnTarget,
	}
	req.apply(typ)
	if req.Directed != nil && !*req.Directed && req.ImpactDirection == "" {
		typ.ImpactDirection = ImpactUndirected
	}
	if err := h.repo.Create(r.Context(), typ); err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, typ)
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
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "key"), req)
	if err != nil {
		writeError(w, err)
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
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "key")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found or protected")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps repository errors: an invalid type is 400 (the REL-01
// rule as text), an unknown CI type key 400 on allowed_source_types or
// allowed_target_types (E-95, F-313), a missing or system type 404, a taken
// key 409; database errors go through api.WriteRepoError (23505 is 409
// without the constraint name, F-047).
func writeError(w http.ResponseWriter, err error) {
	if api.WriteFieldError(w, err) {
		return
	}
	switch {
	case errors.Is(err, ErrInvalid):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found or protected")
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
	default:
		api.WriteRepoError(w, err)
	}
}
