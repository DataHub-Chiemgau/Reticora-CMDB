package history

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for unified history and point-in-time state.
type Handler struct {
	repo Repository
}

// NewHandler creates a new history handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers history routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/history/{entityType}/{id}", h.List)
	r.Get("/api/v1/assets/{id}/state", h.AssetState)
	r.Get("/api/v1/cis/{id}/state", h.CIState)
}

// List handles GET /api/v1/history/{entityType}/{id}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	entityType := chi.URLParam(r, "entityType")
	if !validEntityType(entityType) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "unsupported entity type")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, entityType, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// AssetState handles GET /api/v1/assets/{id}/state?at=...
func (h *Handler) AssetState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "asset")
}

// CIState handles GET /api/v1/cis/{id}/state?at=...
func (h *Handler) CIState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "ci")
}

// state reconstructs the entity state at the requested point in time
// (spec §16). Without an "at" parameter the current replay of the full trail
// is returned.
func (h *Handler) state(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	at := time.Now().UTC()
	if raw := r.URL.Query().Get("at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid at timestamp (RFC3339 expected)")
			return
		}
		at = parsed
	}
	entityID := chi.URLParam(r, "id")
	trail, err := h.repo.TrailUpTo(r.Context(), t.OrganizationID, entityType, entityID, at)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	snapshot := Replay(trail)
	snapshot.EntityType = entityType
	snapshot.EntityID = entityID
	snapshot.At = at
	api.WriteJSON(w, http.StatusOK, snapshot)
}

func validEntityType(entityType string) bool {
	switch entityType {
	case "ci", "asset", "relationship", "location_node", "reservation", "composition":
		return true
	}
	return false
}
