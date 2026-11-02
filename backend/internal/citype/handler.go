package citype

import (
	"context"
	"errors"
	"net/http"
	"time"

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
	r.Get("/api/v1/ci-types/export", h.Export)
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
	// attribute_definition as a resource (API-05, MET-10).
	r.Get("/api/v1/attribute-definitions", h.ListAttributeDefinitions)
	r.Post("/api/v1/attribute-definitions", h.CreateAttributeDefinition)
	r.Get("/api/v1/attribute-definitions/{id}", h.GetAttributeDefinition)
	r.Patch("/api/v1/attribute-definitions/{id}", h.UpdateAttributeDefinition)
	r.Delete("/api/v1/attribute-definitions/{id}", h.DeleteAttributeDefinition)
}

// attributeStore returns the attribute-definitions store or answers 503
// (in-memory mode has none).
func (h *Handler) attributeStore(w http.ResponseWriter) (AttributeDefinitionStore, bool) {
	store, ok := h.repo.(AttributeDefinitionStore)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "attribute definitions need the database")
	}
	return store, ok
}

func writeAttributeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAttributeNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrAttributeReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// ListAttributeDefinitions handles GET /api/v1/attribute-definitions
// (?ci_type_id=, ?scope=type|global): a page newest first on (created_at,
// id) with limit (default 50, at most 200) and the signed cursor of API-02
// (F-215).
func (h *Handler) ListAttributeDefinitions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, total, err := store.ListAttributeDefinitionPage(r.Context(), t.OrganizationID,
		AttributeFilter{CITypeID: q.Get("ci_type_id"), Scope: q.Get("scope")}, page)
	if err != nil {
		writeAttributeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(d *AttributeDefinition) (time.Time, string) {
		return d.CreatedAt, d.ID
	}))
}

// GetAttributeDefinition handles GET /api/v1/attribute-definitions/{id}.
func (h *Handler) GetAttributeDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	d, err := store.GetAttributeDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeAttributeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// CreateAttributeDefinition handles POST /api/v1/attribute-definitions; a
// definition without ci_type_id is global.
func (h *Handler) CreateAttributeDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	var req struct {
		AttributeDefinitionPatch
		CITypeID string `json:"ci_type_id,omitempty"`
		Key      string `json:"key"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d := &AttributeDefinition{CITypeID: req.CITypeID, Key: req.Key, UniqueScope: "org", Inheritable: true}
	if err := req.apply(d); err != nil {
		writeAttributeError(w, err)
		return
	}
	if err := store.CreateAttributeDefinition(r.Context(), t.OrganizationID, d); err != nil {
		writeAttributeError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", d)
	api.WriteJSON(w, http.StatusCreated, d)
}

// UpdateAttributeDefinition handles PATCH /api/v1/attribute-definitions/{id}.
func (h *Handler) UpdateAttributeDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	var patch AttributeDefinitionPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d, err := store.UpdateAttributeDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		writeAttributeError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", d)
	api.WriteJSON(w, http.StatusOK, d)
}

// DeleteAttributeDefinition handles DELETE /api/v1/attribute-definitions/{id}.
func (h *Handler) DeleteAttributeDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := store.DeleteAttributeDefinition(r.Context(), t.OrganizationID, id); err != nil {
		writeAttributeError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", map[string]string{"id": id})
	w.WriteHeader(http.StatusNoContent)
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
		api.WriteRepoError(w, err)
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
	api.WriteJSON(w, http.StatusOK, api.CompleteList(Templates()))
}

// Export handles GET /api/v1/ci-types/export: the metamodel as a JSON
// download (MET-54).
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	out, err := BuildExport(r.Context(), h.repo, t.OrganizationID, now)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="metamodel-`+now.Format("20060102")+`.json"`)
	api.WriteJSON(w, http.StatusOK, out)
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
	if err := req.DisplayName.Validate(); err != nil {
		api.WriteFieldError(w, &api.FieldError{Field: "display_name", Message: err.Error()})
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
		DisplayName:              req.DisplayName.WithFallback(req.Name),
		Icon:                     req.Icon,
		Description:              req.Description,
		Category:                 req.Category,
		IsLogical:                req.IsLogical,
		IsObservable:             observableDefault(req.IsObservable, req.IsLogical),
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
		writeTypeError(w, err)
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
	if err := req.DisplayName.Validate(); err != nil {
		api.WriteFieldError(w, &api.FieldError{Field: "display_name", Message: err.Error()})
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
		writeTypeError(w, err)
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
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
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
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
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
	if err := fieldmeta.ValidateDefinition(fieldFromUpsert(req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeFieldDefinitionError(w, err)
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
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
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
	if err := fieldmeta.ValidateDefinition(fieldFromUpsert(req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, "", req)
	if err != nil {
		writeFieldDefinitionError(w, err)
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
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
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
	if err := fieldmeta.ValidateDefinition(instanceFieldFromUpsert(req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertInstanceField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeFieldDefinitionError(w, err)
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
	// The CI scopes the event for bound webhook subscriptions (RBA-08).
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", map[string]string{"ci_id": chi.URLParam(r, "id"), "name": chi.URLParam(r, "name")})
	w.WriteHeader(http.StatusNoContent)
}

// validateField checks a field of a CI type.
// validation: the texts of fieldmeta.ValidateDefinition.
func validateField(f Field) error {
	return fieldmeta.ValidateDefinition(f.Definition())
}

// writeTypeError reports a failed create or clone of a CI type: a taken key
// or name is 409 (in memory ErrConflict, in PostgreSQL 23505), every other
// repository error goes through api.WriteRepoError, so no SQLSTATE,
// constraint or driver text reaches the client (F-047).
func writeTypeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrConflict) {
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
		return
	}
	api.WriteRepoError(w, err)
}

// writeFieldDefinitionError reports a failed field upsert after the handler
// validated the definition: field errors of the attribute definition with
// their field, an unknown type or CI 404, everything else through
// api.WriteRepoError (F-047).
func writeFieldDefinitionError(w http.ResponseWriter, err error) {
	if api.WriteFieldError(w, err) {
		return
	}
	if err.Error() == "not found" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type or ci not found")
		return
	}
	api.WriteRepoError(w, err)
}
