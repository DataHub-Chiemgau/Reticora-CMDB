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
	// lifecycleModels and roles feed the export v2 (WP-262); without them
	// it answers 503.
	lifecycleModels LifecycleModelReader
	roles           RoleReader
}

// WithLifecycleModels sets the reader of the lifecycle models for the
// metamodel export v2.
func (h *Handler) WithLifecycleModels(r LifecycleModelReader) *Handler {
	h.lifecycleModels = r
	return h
}

// WithRoles sets the reader of the role names for the layouts of the
// metamodel export v2.
func (h *Handler) WithRoles(r RoleReader) *Handler {
	h.roles = r
	return h
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
	r.Post("/api/v1/attribute-definitions/{id}/reindex", h.ReindexAttributeDefinition)
	// Attribute sets and their assignment to types (WP-250, API-06; module
	// metamodel, E-105).
	r.Get("/api/v1/attribute-sets", h.ListAttributeSets)
	r.Post("/api/v1/attribute-sets", h.CreateAttributeSet)
	r.Get("/api/v1/attribute-sets/{id}", h.GetAttributeSet)
	r.Patch("/api/v1/attribute-sets/{id}", h.UpdateAttributeSet)
	r.Delete("/api/v1/attribute-sets/{id}", h.DeleteAttributeSet)
	r.Get("/api/v1/ci-types/{id}/attribute-sets", h.GetTypeAttributeSets)
	r.Put("/api/v1/ci-types/{id}/attribute-sets", h.SetTypeAttributeSets)
	// Templates with versions (WP-251, MET-16, API-06; module metamodel,
	// E-105); GET /api/v1/ci-types/templates above stays the type template
	// catalog.
	h.registerTemplateRoutes(r)
	// Rule catalog metamodel_rule (WP-716, MET-45, API-06; module
	// metamodel, E-105).
	h.registerRuleRoutes(r)
	// Form layouts per type, template and role (WP-259, MET-50, API-06;
	// module metamodel, E-105).
	r.Get("/api/v1/layouts", h.ListLayouts)
	r.Post("/api/v1/layouts", h.CreateLayout)
	r.Get("/api/v1/layouts/{id}", h.GetLayout)
	r.Patch("/api/v1/layouts/{id}", h.UpdateLayout)
	r.Delete("/api/v1/layouts/{id}", h.DeleteLayout)
	// Resolved forms of a CI and of a type (WP-719, MET-50, MET-52; core
	// prefixes, without layouts the standard layout).
	h.registerFormRoutes(r)
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

// layoutStore returns the layout catalog or answers 503 (in-memory mode
// has none, like attributeStore).
func (h *Handler) layoutStore(w http.ResponseWriter, r *http.Request) (LayoutStore, tenant.TenantInfo, bool) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return nil, t, false
	}
	store, ok := h.repo.(LayoutStore)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "form layouts need the database")
	}
	return store, t, ok
}

// writeLayoutError reports a failed layout request (RFC 7807).
func writeLayoutError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLayoutNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrLayoutNotFound.Error())
	case errors.Is(err, ErrLayoutVersionMismatch):
		api.WritePreconditionFailed(w, ErrLayoutVersionMismatch.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// layoutIfMatch reads an optional If-Match (API-07); a value that is not a
// version is answered with 412.
func layoutIfMatch(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &version, true
}

// ListLayouts handles GET /api/v1/layouts (?ci_type_id=, template_id=,
// role_id=): the organization's layouts newest first on (created_at, id)
// with limit and the signed cursor of API-02.
func (h *Handler) ListLayouts(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.layoutStore(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, total, err := store.ListLayoutPage(r.Context(), t.OrganizationID, &LayoutFilter{
		CITypeID: q.Get("ci_type_id"), TemplateID: q.Get("template_id"), RoleID: q.Get("role_id"),
	}, page)
	if err != nil {
		writeLayoutError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(l *FormLayout) (time.Time, string) {
		return l.CreatedAt, l.ID
	}))
}

// GetLayout handles GET /api/v1/layouts/{id}; the ETag is the version.
func (h *Handler) GetLayout(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.layoutStore(w, r)
	if !ok {
		return
	}
	l, err := store.GetLayout(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeLayoutError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(l.Version))
	api.WriteJSON(w, http.StatusOK, l)
}

// CreateLayout handles POST /api/v1/layouts.
func (h *Handler) CreateLayout(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.layoutStore(w, r)
	if !ok {
		return
	}
	var in LayoutInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	l, err := store.CreateLayout(r.Context(), t.OrganizationID, &in)
	if err != nil {
		writeLayoutError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(l.Version))
	api.WriteJSON(w, http.StatusCreated, l)
}

// UpdateLayout handles PATCH /api/v1/layouts/{id} with an optional
// If-Match (API-07): a stale version is 412.
func (h *Handler) UpdateLayout(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.layoutStore(w, r)
	if !ok {
		return
	}
	ifMatch, ok := layoutIfMatch(w, r)
	if !ok {
		return
	}
	var patch LayoutPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	l, err := store.UpdateLayout(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch, ifMatch)
	if err != nil {
		writeLayoutError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(l.Version))
	api.WriteJSON(w, http.StatusOK, l)
}

// DeleteLayout handles DELETE /api/v1/layouts/{id} with an optional
// If-Match (API-07).
func (h *Handler) DeleteLayout(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.layoutStore(w, r)
	if !ok {
		return
	}
	ifMatch, ok := layoutIfMatch(w, r)
	if !ok {
		return
	}
	if err := store.DeleteLayout(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), ifMatch); err != nil {
		writeLayoutError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAttributeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAttributeNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrAttributeReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
	case errors.Is(err, ErrAttributeSetReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrAttributeSetReadOnly.Error())
	case errors.Is(err, ErrAttributeNotIndexed):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrAttributeNotIndexed.Error())
	case errors.Is(err, ErrIndexJobsUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrIndexJobsUnavailable.Error())
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// withIndexStatus sets the index status of the definitions (DB-04, WP-796)
// with one query for all of them.
func withIndexStatus(r *http.Request, store AttributeDefinitionStore, orgID string, defs []AttributeDefinition) error {
	statuses, err := store.AttributeIndexStatuses(r.Context(), orgID, defs)
	if err != nil {
		return err
	}
	for i := range defs {
		if s, ok := statuses[defs[i].ID]; ok {
			defs[i].IndexStatus = &s
		}
	}
	return nil
}

// ListAttributeDefinitions handles GET /api/v1/attribute-definitions
// (?ci_type_id=, ?attribute_set_id=, ?scope=type|global|set, and
// ?applicable_to= for the definitions of a type with the global ones that
// apply to it, WP-255): a page newest first on (created_at,
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
		AttributeFilter{CITypeID: q.Get("ci_type_id"), AttributeSetID: q.Get("attribute_set_id"), Scope: q.Get("scope"),
			ApplicableTo: q.Get("applicable_to")}, page)
	if err == nil {
		err = withIndexStatus(r, store, t.OrganizationID, items)
	}
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
	if err == nil {
		one := []AttributeDefinition{*d}
		if err = withIndexStatus(r, store, t.OrganizationID, one); err == nil {
			d = &one[0]
		}
	}
	if err != nil {
		writeAttributeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// ReindexAttributeDefinition handles POST
// /api/v1/attribute-definitions/{id}/reindex (DB-04, WP-796): it queues an
// attribute.index job for the caller and answers 202 with the index status
// (Location names the job), or 200 with the status of the job of the
// definition that is queued or running, without a new one. A system
// definition is 403, one without searchable and unique 409, an unknown 404,
// a server without job service 503.
func (h *Handler) ReindexAttributeDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeStore(w)
	if !ok {
		return
	}
	s, created, err := store.ReindexAttributeDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), t.UserID)
	if err != nil {
		writeAttributeError(w, err)
		return
	}
	if !created {
		api.WriteJSON(w, http.StatusOK, s)
		return
	}
	if s.JobID != "" {
		w.Header().Set("Location", "/api/v1/jobs/"+s.JobID)
	}
	api.WriteJSON(w, http.StatusAccepted, s)
}

// CreateAttributeDefinition handles POST /api/v1/attribute-definitions; a
// definition without ci_type_id and attribute_set_id is global, one with
// attribute_set_id a set attribute (WP-250).
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
		CITypeID       string `json:"ci_type_id,omitempty"`
		AttributeSetID string `json:"attribute_set_id,omitempty"`
		Key            string `json:"key"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d := &AttributeDefinition{CITypeID: req.CITypeID, AttributeSetID: req.AttributeSetID, Key: req.Key,
		UniqueScope: "org", Inheritable: true}
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

// attributeSetStore returns the attribute-sets store or answers 503
// (in-memory mode has none).
func (h *Handler) attributeSetStore(w http.ResponseWriter) (AttributeSetStore, bool) {
	store, ok := h.repo.(AttributeSetStore)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "attribute sets need the database")
	}
	return store, ok
}

// writeAttributeSetError reports a failed attribute set request (RFC 7807).
func writeAttributeSetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAttributeSetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrAttributeSetNotFound.Error())
	case errors.Is(err, ErrCITypeNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrCITypeNotFound.Error())
	case errors.Is(err, ErrAttributeSetReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrAttributeSetReadOnly.Error())
	case errors.Is(err, ErrAttributeSetInUse):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrAttributeSetInUse.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// ListAttributeSets handles GET /api/v1/attribute-sets: the organization's
// and the system sets newest first on (created_at, id) with limit and the
// signed cursor of API-02.
func (h *Handler) ListAttributeSets(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	items, total, err := store.ListAttributeSetPage(r.Context(), t.OrganizationID, page)
	if err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(s *AttributeSet) (time.Time, string) {
		return s.CreatedAt, s.ID
	}))
}

// GetAttributeSet handles GET /api/v1/attribute-sets/{id}.
func (h *Handler) GetAttributeSet(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	s, err := store.GetAttributeSet(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// CreateAttributeSet handles POST /api/v1/attribute-sets: a set of the
// organization, active unless is_active is false.
func (h *Handler) CreateAttributeSet(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	var req struct {
		Key         string        `json:"key"`
		DisplayName LocalizedText `json:"display_name,omitempty"`
		Description string        `json:"description,omitempty"`
		IsActive    *bool         `json:"is_active,omitempty"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s := &AttributeSet{Key: req.Key, DisplayName: req.DisplayName, Description: req.Description, IsActive: true}
	if req.IsActive != nil {
		s.IsActive = *req.IsActive
	}
	if err := store.CreateAttributeSet(r.Context(), t.OrganizationID, s); err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// UpdateAttributeSet handles PATCH /api/v1/attribute-sets/{id}.
func (h *Handler) UpdateAttributeSet(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	var patch AttributeSetPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s, err := store.UpdateAttributeSet(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteAttributeSet handles DELETE /api/v1/attribute-sets/{id}; an
// assigned set is 409.
func (h *Handler) DeleteAttributeSet(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	if err := store.DeleteAttributeSet(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeAttributeSetError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetTypeAttributeSets handles GET /api/v1/ci-types/{id}/attribute-sets:
// the assignment document of the type.
func (h *Handler) GetTypeAttributeSets(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	doc, err := store.GetTypeAttributeSets(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, doc)
}

// SetTypeAttributeSets handles PUT /api/v1/ci-types/{id}/attribute-sets:
// the ordered list replaces the type's sets as a whole.
func (h *Handler) SetTypeAttributeSets(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	store, ok := h.attributeSetStore(w)
	if !ok {
		return
	}
	var req struct {
		CITypeID      string                   `json:"ci_type_id,omitempty"`
		AttributeSets []AttributeSetAssignment `json:"attribute_sets"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	typeID := chi.URLParam(r, "id")
	if req.CITypeID != "" && req.CITypeID != typeID {
		api.WriteFieldError(w, &api.FieldError{Field: "ci_type_id", Message: "ci_type_id differs from the path",
			Status: http.StatusUnprocessableEntity})
		return
	}
	doc, err := store.SetTypeAttributeSets(r.Context(), t.OrganizationID, typeID, req.AttributeSets)
	if err != nil {
		writeAttributeSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, doc)
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
// download (MET-54). format_version 1 (default) is BuildExport, 2 the
// complete export of BuildExportV2 (WP-262); another value is 400, since
// the OpenAPI validation does not check enums. The export v2 needs the
// database and the readers of lifecycle models and roles (503 otherwise).
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	var (
		out  any
		err  error
		name = "metamodel-"
	)
	switch r.URL.Query().Get("format_version") {
	case "", "1":
		out, err = BuildExport(r.Context(), h.repo, t.OrganizationID, now)
	case "2":
		src, isSource := h.repo.(ExportV2Source)
		if !isSource || h.lifecycleModels == nil || h.roles == nil {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the metamodel export v2 needs the database")
			return
		}
		out, err = BuildExportV2(r.Context(), src, h.lifecycleModels, h.roles, t.OrganizationID, now)
		name = "metamodel-v2-"
	default:
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "format_version must be 1 or 2")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+now.Format("20060102")+`.json"`)
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
	for i := range req.Fields {
		if err := validateField(&req.Fields[i]); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	model, err := req.LifecycleModel()
	if err != nil {
		api.WriteFieldError(w, err)
		return
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
		ParentTypeID:             req.ParentTypeID,
		TemplateKey:              req.TemplateKey,
		LifecycleModelID:         model,
		LifecycleDefinitionID:    model,
		Capabilities:             req.Capabilities,
		AllowedRelationshipTypes: req.AllowedRelationshipTypes,
		UISchema:                 req.UISchema,
		ComplianceRules:          req.ComplianceRules,
		DiscoveryMappings:        req.DiscoveryMappings,
		Fields:                   req.Fields,
	}
	if err = h.repo.Create(r.Context(), typ); err != nil {
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
	// lifecycle_model_id and its deprecated name lifecycle_definition_id
	// with different values are a 422 (API-01).
	model, err := req.LifecycleModel()
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	req.LifecycleModelID, req.LifecycleDefinitionID = model, nil
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeTypeUpdateError(w, err)
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
		writeSetActiveError(w, err)
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
		writeSetActiveError(w, err)
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
	if err := fieldmeta.ValidateDefinition(fieldFromUpsert(&req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeFieldDefinitionError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "ci_attribute.changed", f)
	api.WriteJSON(w, http.StatusOK, f)
}

// DeleteField handles DELETE /api/v1/ci-types/{id}/fields/{name}: a field
// of the type only; a global field of the same name stays (404, WP-255).
func (h *Handler) DeleteField(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	typeID := chi.URLParam(r, "id")
	if typeID == "" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "field definition not found")
		return
	}
	if err := h.repo.DeleteField(r.Context(), t.OrganizationID, typeID, chi.URLParam(r, "name")); err != nil {
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
	if err := fieldmeta.ValidateDefinition(fieldFromUpsert(&req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertField(r.Context(), t.OrganizationID, "", &req)
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
	if err := fieldmeta.ValidateDefinition(instanceFieldFromUpsert(&req)); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	f, err := h.repo.UpsertInstanceField(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &req)
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
func validateField(f *Field) error {
	return fieldmeta.ValidateDefinition(f.Definition())
}

// writeTypeError reports a failed create or clone of a CI type: a taken key
// or name is 409 (in memory ErrConflict, in PostgreSQL 23505), an error of
// the parent or of the composition of the chain an RFC 7807 problem with
// field parent_type_id (422, a cycle 409), an invisible lifecycle model a
// 422 on lifecycle_model_id, CIs whose lifecycle states the new effective
// model lacks a 409 with the counts per state, every other repository error
// goes through api.WriteRepoError, so no SQLSTATE, constraint or driver
// text reaches the client (F-047).
func writeTypeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrConflict) {
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
		return
	}
	var states *LifecycleStatesConflict
	if errors.As(err, &states) {
		writeLifecycleStatesConflict(w, states)
		return
	}
	if api.WriteFieldError(w, err) {
		return
	}
	api.WriteRepoError(w, err)
}

// writeLifecycleStatesConflict answers a refused change of the lifecycle
// model or of the parent of a type (409): the field and the number of CIs
// per lifecycle state the new effective model does not contain (MET-06,
// LCY-02).
func writeLifecycleStatesConflict(w http.ResponseWriter, c *LifecycleStatesConflict) {
	api.WriteProblemBody(w, http.StatusConflict, struct {
		api.FieldProblem
		StateCounts []LifecycleStateCount `json:"state_counts"`
	}{
		FieldProblem: api.FieldProblem{
			ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: lifecycleStatesConflictDetail},
			Field: c.Field,
		},
		StateCounts: c.Counts,
	})
}

// writeSetActiveError reports a refused deactivation or activation (MET-05):
// an unknown type 404, a system type 403 (ErrTypeReadOnly), active subtypes
// or a deactivated parent 409; everything else goes through
// api.WriteRepoError (F-047). Nothing is dispatched.
func writeSetActiveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, api.ErrNotFound) || err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
	case errors.Is(err, ErrTypeReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrTypeReadOnly.Error())
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// writeTypeUpdateError reports a failed update of a CI type: an unknown
// type (or a system type, which tenants cannot change) is 404, parent and
// composition errors carry field parent_type_id, everything else goes
// through api.WriteRepoError (F-047).
func writeTypeUpdateError(w http.ResponseWriter, err error) {
	if errors.Is(err, api.ErrNotFound) || err.Error() == "not found" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci type not found")
		return
	}
	writeTypeError(w, err)
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
