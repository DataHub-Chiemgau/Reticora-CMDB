package citype

import (
	"context"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes CI type lifecycle events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// Handler provides HTTP handlers for the CI type system endpoints.
type Handler struct {
	repo       Repository
	dispatcher EventDispatcher
}

// NewHandler creates a new CI type handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

// RegisterRoutes registers CI type routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/ci-types", h.List)
	r.Post("/api/v1/ci-types", h.Create)
	r.Get("/api/v1/ci-types/templates", h.ListTemplates)
	r.Get("/api/v1/ci-types/{id}", h.Get)
	r.Patch("/api/v1/ci-types/{id}", h.Update)
	r.Post("/api/v1/ci-types/{id}/clone", h.Clone)
	r.Post("/api/v1/ci-types/{id}/deactivate", h.Deactivate)
	r.Post("/api/v1/ci-types/{id}/activate", h.Activate)
	r.Get("/api/v1/ci-types/{id}/versions", h.Versions)
	r.Get("/api/v1/ci-types/{id}/fields", h.ListFields)
	r.Put("/api/v1/ci-types/{id}/fields", h.UpsertField)
	r.Delete("/api/v1/ci-types/{id}/fields/{name}", h.DeleteField)
	r.Get("/api/v1/ci-fields/global", h.ListGlobalFields)
	r.Put("/api/v1/ci-fields/global", h.UpsertGlobalField)
	r.Delete("/api/v1/ci-fields/global/{name}", h.DeleteGlobalField)
	r.Get("/api/v1/cis/{id}/field-definitions", h.ListInstanceFields)
	r.Put("/api/v1/cis/{id}/field-definitions", h.UpsertInstanceField)
	r.Delete("/api/v1/cis/{id}/field-definitions/{name}", h.DeleteInstanceField)
}

func tenantOr401(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return t, false
	}
	return t, true
}

// List handles GET /api/v1/ci-types
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		IncludeInactive: r.URL.Query().Get("include_inactive") == "true",
		Category:        r.URL.Query().Get("category"),
		Search:          r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Type]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListTemplates handles GET /api/v1/ci-types/templates — the built-in type
// templates that initialize metadata for new types (spec §19).
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOr401(w, r); !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": Templates()})
}

// Get handles GET /api/v1/ci-types/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/ci-types
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	var req CreateTypeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	for _, f := range req.Fields {
		if err := validateField(f); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	typ := &Type{
		OrganizationID:           t.OrganizationID,
		Key:                      req.Key,
		Name:                     req.Name,
		DisplayName:              req.DisplayName,
		Icon:                     req.Icon,
		Description:              req.Description,
		Category:                 req.Category,
		IsLogical:                req.IsLogical,
		TemplateKey:              req.TemplateKey,
		LifecycleDefinitionID:    req.LifecycleDefinitionID,
		Capabilities:             req.Capabilities,
		AllowedRelationshipTypes: req.AllowedRelationshipTypes,
		UISchema:                 req.UISchema,
		ComplianceRules:          req.ComplianceRules,
		DiscoveryMappings:        req.DiscoveryMappings,
		Fields:                   req.Fields,
	}
	if err := h.repo.Create(r.Context(), typ); err != nil {
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_type.created", typ)
	api.WriteJSON(w, http.StatusCreated, typ)
}

// Update handles PATCH /api/v1/ci-types/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	var req UpdateTypeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_type.updated", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Clone handles POST /api/v1/ci-types/{id}/clone
func (h *Handler) Clone(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	var req CloneRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Clone(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if err.Error() == "not found" {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_type.created", item)
	api.WriteJSON(w, http.StatusCreated, item)
}

// Deactivate handles POST /api/v1/ci-types/{id}/deactivate
func (h *Handler) Deactivate(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	item, err := h.repo.SetActive(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), false)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_type.deactivated", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Activate handles POST /api/v1/ci-types/{id}/activate
func (h *Handler) Activate(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	item, err := h.repo.SetActive(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), true)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_type.updated", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Versions handles GET /api/v1/ci-types/{id}/versions
func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	items, err := h.repo.Versions(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// ListFields handles GET /api/v1/ci-types/{id}/fields
func (h *Handler) ListFields(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListFields(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// UpsertField handles PUT /api/v1/ci-types/{id}/fields
func (h *Handler) UpsertField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	var req UpsertFieldRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", f)
	api.WriteJSON(w, http.StatusOK, f)
}

// DeleteField handles DELETE /api/v1/ci-types/{id}/fields/{name}
func (h *Handler) DeleteField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "name")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "field definition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", map[string]string{"name": chi.URLParam(r, "name")})
	w.WriteHeader(http.StatusNoContent)
}

// ListGlobalFields handles GET /api/v1/ci-fields/global
func (h *Handler) ListGlobalFields(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListGlobalFields(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// UpsertGlobalField handles PUT /api/v1/ci-fields/global
func (h *Handler) UpsertGlobalField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	var req UpsertFieldRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	req.Scope = "global"
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, "", req)
	if err != nil {
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", f)
	api.WriteJSON(w, http.StatusOK, f)
}

// DeleteGlobalField handles DELETE /api/v1/ci-fields/global/{name}
func (h *Handler) DeleteGlobalField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteField(r.Context(), t.OrganizationID, "", chi.URLParam(r, "name")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "field definition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", map[string]string{"name": chi.URLParam(r, "name")})
	w.WriteHeader(http.StatusNoContent)
}

// ListInstanceFields handles GET /api/v1/cis/{id}/field-definitions
func (h *Handler) ListInstanceFields(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListInstanceFields(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// UpsertInstanceField handles PUT /api/v1/cis/{id}/field-definitions
func (h *Handler) UpsertInstanceField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	// Authorization is enforced by the route mapping (ci_instance_attribute:manage
	// on the write side); the handler trusts the middleware contract.
	var req UpsertInstanceFieldRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertInstanceField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", f)
	api.WriteJSON(w, http.StatusOK, f)
}

// DeleteInstanceField handles DELETE /api/v1/cis/{id}/field-definitions/{name}
func (h *Handler) DeleteInstanceField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteInstanceField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "name")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "instance field definition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", map[string]string{"name": chi.URLParam(r, "name")})
	w.WriteHeader(http.StatusNoContent)
}

func validateField(f Field) error {
	return fieldmeta.ValidateDefinition(f.Definition())
}
