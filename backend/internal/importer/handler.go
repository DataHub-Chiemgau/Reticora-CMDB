package importer

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler serves the import profiles under /api/v1/import-profiles (E-38).
// Rights (import:run to read, import:manage to write), the module gate
// import and the org gate are applied by the authorizing router and the
// entitlement middleware, the route audit by its middleware.
type Handler struct {
	repo Repository
}

// NewHandler returns the import profile handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers the profile routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/import-profiles", h.List)
	r.Post("/api/v1/import-profiles", h.Create)
	r.Get("/api/v1/import-profiles/{id}", h.Get)
	r.Patch("/api/v1/import-profiles/{id}", h.Update)
	r.Delete("/api/v1/import-profiles/{id}", h.Delete)
}

func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}

// writeError answers a failed profile request: an unknown or foreign
// profile is 404 with a fixed text (TEN-04), a validation error 422 and a
// taken name 409 with the field. Database errors go through
// api.WriteRepoError; no table, constraint or SQLSTATE text reaches the
// client (API-03), the log keeps it.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "import profile request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// List handles GET /api/v1/import-profiles: the profiles of the
// organization sorted by name and id, with limit (at most 200) and the
// signed keyset cursor (API-02).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	cursorErr := page.CursorError
	if cursorErr == nil && page.Cursor != nil {
		_, _, cursorErr = profilePosition(page.Cursor)
	}
	if cursorErr != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
		return
	}
	rows, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, profilePage(rows, total, page))
}

// Get handles GET /api/v1/import-profiles/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.Get(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/import-profiles (201). The profile is checked
// with Profile.Validate (422 with the field); a taken name is 409.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateProfileRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item := newProfile(t.OrganizationID, t.UserID, &req)
	if err := h.repo.Create(r.Context(), item); err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

// Update handles PATCH /api/v1/import-profiles/{id}: sent fields change,
// the merged profile is checked like a create.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpdateProfileRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/import-profiles/{id} (204).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
