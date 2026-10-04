package location

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for asset location history.
type Handler struct {
	repo Repository
}

// NewHandler creates a new location handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers location routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/asset-locations", h.Record)
	r.Get("/api/v1/assets/{id}/locations", h.History)
}

// Record handles POST /api/v1/asset-locations — records an asset position
// (from a field scan, the endpoint agent, or manual entry).
func (h *Handler) Record(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateEntryRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.AssetID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id is required")
		return
	}
	if req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "lat/lon out of range")
		return
	}
	e := &Entry{
		OrganizationID: t.OrganizationID,
		AssetID:        req.AssetID,
		Lat:            req.Lat,
		Lon:            req.Lon,
		AccuracyM:      req.AccuracyM,
		Source:         req.Source,
	}
	if req.RecordedAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.RecordedAt)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "recorded_at must be RFC3339")
			return
		}
		e.RecordedAt = parsed
	}
	if err := h.repo.Record(r.Context(), e); err != nil {
		if errors.Is(err, ErrAssetNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, e)
}

// History handles GET /api/v1/assets/{id}/locations — the asset's GPS track.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.History(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Entry]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}
