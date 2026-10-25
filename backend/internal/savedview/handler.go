package savedview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// PermissionChecker answers whether a user holds a permission key.
type PermissionChecker interface {
	HasPermission(ctx context.Context, orgID, userID, key string) (bool, error)
}

// Handler provides HTTP handlers for saved views.
type Handler struct {
	repo        Repository
	query       QueryEngine
	permissions PermissionChecker
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

// WithPermissions attaches the permission check of the query endpoint: the
// caller must hold the read permission of the queried entity kind (SRC-03).
// Without a checker the endpoint answers 403 (fail closed).
func (h *Handler) WithPermissions(permissions PermissionChecker) *Handler {
	h.permissions = permissions
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
	api.WriteJSON(w, http.StatusOK, api.CompleteList(Presets()))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id, ownerID(r))
	if err != nil {
		writeViewError(w, r, err)
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
	if err := ValidateView(req.EntityKind, &req); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	view := &View{
		OrganizationID:  t.OrganizationID,
		OwnerID:         ownerID(r),
		Name:            req.Name,
		EntityKind:      req.EntityKind,
		FilterSpec:      req.FilterSpec,
		Sort:            req.Sort,
		Columns:         req.Columns,
		Shared:          req.Shared,
		SharedWithRoles: req.SharedWithRoles,
	}
	if err := h.repo.Create(r.Context(), view); err != nil {
		writeViewError(w, r, err)
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	current, err := h.repo.GetByID(r.Context(), t.OrganizationID, id, ownerID(r))
	if err != nil {
		writeViewError(w, r, err)
		return
	}
	kind := req.EntityKind
	if kind == "" {
		kind = current.EntityKind
	}
	if verr := ValidateView(kind, &req); verr != nil {
		api.WriteFieldError(w, verr)
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, ownerID(r), req)
	if err != nil {
		// A name the owner uses for another view is 409, not 404 (F-047).
		writeViewError(w, r, err)
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id, ownerID(r)); err != nil {
		writeViewError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeViewError answers a failed saved-view request: a view that is
// unknown, of another organization or not the caller's to read or change is
// 404 (VIE-01), a name the owner uses for another view 409 (in memory
// ErrConflict, in PostgreSQL 23505). Database errors go through
// api.WriteRepoError like the engine errors of Query: no constraint name or
// SQLSTATE reaches the client (API-03, F-047), the log keeps the text.
func writeViewError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
	default:
		slog.WarnContext(r.Context(), "saved view request failed", "error", err)
		api.WriteRepoError(w, err)
	}
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
	key, ok := readPermissions[spec.entityKind()]
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("unsupported entity_kind %q", spec.entityKind()))
		return
	}
	if !h.mayRead(r.Context(), t, key) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+key)
		return
	}
	page := api.ParsePagination(r)
	// Values are checked before any SQL runs: 422 naming the field.
	if err := spec.Validate(spec.entityKind()); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	items, total, err := h.query.Query(r.Context(), t.OrganizationID, spec, page)
	if err != nil {
		// A validated spec compiles to SQL whose casts cannot fail; any
		// error left is internal and its text goes to the log only (API-03).
		slog.ErrorContext(r.Context(), "structured search failed", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[QueryResult]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// readPermissions maps each queryable entity kind to its read permission.
var readPermissions = map[string]string{
	"ci":    "ci:read",
	"asset": "asset:read",
}

// mayRead reports whether the caller holds key; errors and a missing checker
// or user deny.
func (h *Handler) mayRead(ctx context.Context, t tenant.TenantInfo, key string) bool {
	if h.permissions == nil || t.UserID == "" {
		return false
	}
	ok, err := h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, key)
	return err == nil && ok
}
