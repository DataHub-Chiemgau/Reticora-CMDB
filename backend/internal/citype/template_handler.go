package citype

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// registerTemplateRoutes registers the templates of MET-16 with their
// versions (WP-251, API-06; module metamodel, E-105). Reading requires
// ci:read, writing citype:manage, publishing metamodel:publish (E-106,
// server/authz.go).
func (h *Handler) registerTemplateRoutes(r chi.Router) {
	r.Get("/api/v1/templates", h.ListTemplateResources)
	r.Post("/api/v1/templates", h.CreateTemplate)
	r.Get("/api/v1/templates/{id}", h.GetTemplate)
	r.Patch("/api/v1/templates/{id}", h.UpdateTemplate)
	r.Delete("/api/v1/templates/{id}", h.DeleteTemplate)
	r.Post("/api/v1/templates/{id}/clone", h.CloneTemplate)
	r.Get("/api/v1/templates/{id}/versions", h.ListTemplateVersions)
	r.Post("/api/v1/templates/{id}/versions", h.CreateTemplateVersion)
	r.Get("/api/v1/templates/{id}/versions/{version}", h.GetTemplateVersion)
	r.Put("/api/v1/templates/{id}/versions/{version}", h.SetTemplateVersionItems)
	r.Post("/api/v1/templates/{id}/versions/{version}/publish", h.PublishTemplateVersion)
}

// templateStore returns the templates store or answers 503 (in-memory mode
// has none, like attributeStore).
func (h *Handler) templateStore(w http.ResponseWriter, r *http.Request) (TemplateStore, tenant.TenantInfo, bool) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return nil, t, false
	}
	store, ok := h.repo.(TemplateStore)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "templates need the database")
	}
	return store, t, ok
}

// writeTemplateError reports a failed template request (RFC 7807).
func writeTemplateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTemplateNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTemplateNotFound.Error())
	case errors.Is(err, ErrTemplateVersionNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTemplateVersionNotFound.Error())
	case errors.Is(err, ErrTemplateReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrTemplateReadOnly.Error())
	case errors.Is(err, ErrTemplateVersionImmutable):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrTemplateVersionImmutable.Error())
	case errors.Is(err, ErrTemplateVersionNotDraft):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrTemplateVersionNotDraft.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// templateVersionParam reads {version}; anything but a positive number
// names no version.
func templateVersionParam(r *http.Request) (int, error) {
	v, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || v < 1 {
		return 0, ErrTemplateVersionNotFound
	}
	return v, nil
}

// ListTemplateResources handles GET /api/v1/templates (?ci_type_id=): the
// organization's and the system templates newest first on (created_at, id)
// with limit and the signed cursor of API-02. ListTemplates is the type
// template catalog of GET /api/v1/ci-types/templates.
func (h *Handler) ListTemplateResources(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	items, total, err := store.ListTemplatePage(r.Context(), t.OrganizationID,
		TemplateFilter{CITypeID: r.URL.Query().Get("ci_type_id")}, page)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(tpl *Template) (time.Time, string) {
		return tpl.CreatedAt, tpl.ID
	}))
}

// GetTemplate handles GET /api/v1/templates/{id}.
func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	tpl, err := store.GetTemplate(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, tpl)
}

// CreateTemplate handles POST /api/v1/templates: a template of the
// organization with version 1 as an empty draft.
func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	var req struct {
		Key         string        `json:"key"`
		CITypeID    string        `json:"ci_type_id"`
		DisplayName LocalizedText `json:"display_name,omitempty"`
		Description string        `json:"description,omitempty"`
		IsActive    *bool         `json:"is_active,omitempty"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	tpl := &Template{Key: req.Key, CITypeID: req.CITypeID, DisplayName: req.DisplayName, Description: req.Description,
		IsActive: req.IsActive == nil || *req.IsActive}
	if err := store.CreateTemplate(r.Context(), t.OrganizationID, tpl); err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, tpl)
}

// UpdateTemplate handles PATCH /api/v1/templates/{id}.
func (h *Handler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	var patch TemplatePatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	tpl, err := store.UpdateTemplate(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, tpl)
}

// DeleteTemplate handles DELETE /api/v1/templates/{id}.
func (h *Handler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	if err := store.DeleteTemplate(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeTemplateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CloneTemplate handles POST /api/v1/templates/{id}/clone.
func (h *Handler) CloneTemplate(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	var req TemplateClone
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	tpl, err := store.CloneTemplate(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, tpl)
}

// ListTemplateVersions handles GET /api/v1/templates/{id}/versions: the
// versions newest first with the cursor of API-02.
func (h *Handler) ListTemplateVersions(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	items, total, err := store.ListTemplateVersionPage(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(v *TemplateVersion) (time.Time, string) {
		return v.CreatedAt, v.ID
	}))
}

// CreateTemplateVersion handles POST /api/v1/templates/{id}/versions: the
// next version as a draft copy of the current one.
func (h *Handler) CreateTemplateVersion(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	v, err := store.CreateTemplateVersion(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, v)
}

// GetTemplateVersion handles GET /api/v1/templates/{id}/versions/{version}.
func (h *Handler) GetTemplateVersion(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	version, err := templateVersionParam(r)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	v, err := store.GetTemplateVersion(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), version)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// SetTemplateVersionItems handles PUT /api/v1/templates/{id}/versions/{version}:
// the item list replaces the items of a draft.
func (h *Handler) SetTemplateVersionItems(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	version, err := templateVersionParam(r)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	var req struct {
		Items []TemplateItemInput `json:"items"`
	}
	if err = api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	items, err := TemplateItems(req.Items)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	v, err := store.SetTemplateVersionItems(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), version, items)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// PublishTemplateVersion handles POST
// /api/v1/templates/{id}/versions/{version}/publish (MET-53).
func (h *Handler) PublishTemplateVersion(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.templateStore(w, r)
	if !ok {
		return
	}
	version, err := templateVersionParam(r)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	v, err := store.PublishTemplateVersion(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), version)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}
