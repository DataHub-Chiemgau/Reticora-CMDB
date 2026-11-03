package metamodel

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Handler serves the metamodel drafts (API-06, MET-53). Every route needs
// citype:manage (server/authz.go); an item needs the write permission of
// its resource route as well (ItemPermission).
type Handler struct {
	repo Repository
}

// NewHandler returns the draft handler; without a repository (no
// database) every route answers 503.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers the draft routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/metamodel/drafts", h.ListDrafts)
	r.Post("/api/v1/metamodel/drafts", h.CreateDraft)
	r.Get("/api/v1/metamodel/drafts/{id}", h.GetDraft)
	r.Patch("/api/v1/metamodel/drafts/{id}", h.UpdateDraft)
	r.Delete("/api/v1/metamodel/drafts/{id}", h.DiscardDraft)
	r.Get("/api/v1/metamodel/drafts/{id}/items", h.ListItems)
	r.Post("/api/v1/metamodel/drafts/{id}/items", h.AddItem)
	r.Delete("/api/v1/metamodel/drafts/{id}/items/{item_id}", h.RemoveItem)
}

// store returns the repository and the tenant, or answers 503 (no
// database) or 401.
func (h *Handler) store(w http.ResponseWriter, r *http.Request) (Repository, tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, t, false
	}
	if h.repo == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "metamodel drafts need the database")
		return nil, t, false
	}
	return h.repo, t, true
}

// writeDraftError reports a failed draft request (RFC 7807).
func writeDraftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDraftNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrDraftNotFound.Error())
	case errors.Is(err, ErrItemNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
	case errors.Is(err, ErrTargetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTargetNotFound.Error())
	case errors.Is(err, ErrTargetReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrTargetReadOnly.Error())
	case errors.Is(err, ErrDraftImmutable):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrDraftImmutable.Error())
	case errors.Is(err, ErrItemReferenced):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrItemReferenced.Error())
	case errors.Is(err, ErrVersionMismatch):
		api.WritePreconditionFailed(w, ErrVersionMismatch.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// ifMatch reads an optional If-Match (API-07); a value that is not a
// version is answered with 412.
func ifMatch(w http.ResponseWriter, r *http.Request) (*int64, bool) {
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

// writeDraft answers a draft with its version as ETag.
func writeDraft(w http.ResponseWriter, status int, d *Draft) {
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, status, d)
}

// ListDrafts handles GET /api/v1/metamodel/drafts (?status=): newest first
// with limit and the keyset cursor of API-02.
func (h *Handler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", StatusDraft, StatusTested, StatusPublished, StatusDiscarded:
	default:
		api.WriteRepoError(w, errStatusFilter())
		return
	}
	rows, total, err := repo.ListDrafts(r.Context(), t.OrganizationID, status, page)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(rows, total, page, func(d *Draft) (time.Time, string) {
		return d.CreatedAt, d.ID
	}))
}

// CreateDraft handles POST /api/v1/metamodel/drafts.
func (h *Handler) CreateDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	var in DraftInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d, err := repo.CreateDraft(r.Context(), t.OrganizationID, &in)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusCreated, d)
}

// GetDraft handles GET /api/v1/metamodel/drafts/{id}.
func (h *Handler) GetDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	d, err := repo.GetDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// UpdateDraft handles PATCH /api/v1/metamodel/drafts/{id}: the name.
func (h *Handler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var patch DraftPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d, err := repo.UpdateDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch, match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// DiscardDraft handles DELETE /api/v1/metamodel/drafts/{id}: the draft
// stays as discarded.
func (h *Handler) DiscardDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	d, err := repo.DiscardDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// ListItems handles GET /api/v1/metamodel/drafts/{id}/items: newest first
// with limit and the keyset cursor of API-02.
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	d, err := repo.GetDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	key := func(it *Item) (time.Time, string) { return it.CreatedAt, it.ID }
	rows, total, err := api.NewestFirstSlice(d.Items, page, key)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(rows, total, page, key))
}

// AddItem handles POST /api/v1/metamodel/drafts/{id}/items: the item is
// checked (kind, op, strict payload, target), the caller needs the write
// permission of its resource route (403), and it is captured with the
// base of its target.
func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in ItemInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	c, err := checkItem(&in)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	perms, _ := identity.PermissionsFromContext(r.Context())
	if need := ItemPermission(c.Kind, c.Op); !identity.HasPermission(perms, need) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the item needs the permission "+string(need))
		return
	}
	d, it, err := repo.AddItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), c, match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, http.StatusCreated, it)
}

// RemoveItem handles DELETE /api/v1/metamodel/drafts/{id}/items/{item_id}.
func (h *Handler) RemoveItem(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	d, err := repo.RemoveItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}
