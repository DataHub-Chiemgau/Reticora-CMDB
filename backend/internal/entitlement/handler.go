package entitlement

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides entitlement HTTP endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a new entitlement handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes registers entitlement routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/entitlements", h.List)
	r.Post("/api/v1/entitlements", h.Grant)
	r.Get("/api/v1/entitlements/check/{feature}", h.Check)
}

// List handles GET /api/v1/entitlements.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	items, err := h.service.List(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Entitlement]{
		Data:    items,
		Total:   len(items),
		Limit:   len(items),
		Offset:  0,
		HasMore: false,
	})
}

type grantRequest struct {
	FeatureKey string     `json:"feature_key"`
	Plan       Plan       `json:"plan"`
	Enabled    *bool      `json:"enabled,omitempty"`
	Limit      *int64     `json:"limit,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Grant handles POST /api/v1/entitlements.
func (h *Handler) Grant(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req grantRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.FeatureKey == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "feature_key is required")
		return
	}
	if req.Plan == "" {
		req.Plan = PlanEssential
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	granted, err := h.service.Grant(r.Context(), Entitlement{
		OrganizationID: t.OrganizationID,
		FeatureKey:     req.FeatureKey,
		Plan:           req.Plan,
		Enabled:        enabled,
		Limit:          req.Limit,
		ExpiresAt:      req.ExpiresAt,
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, granted)
}

// Check handles GET /api/v1/entitlements/check/{feature}.
func (h *Handler) Check(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	feature := chi.URLParam(r, "feature")
	if feature == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "feature is required")
		return
	}

	ent, enabled, err := h.service.Check(r.Context(), t.OrganizationID, feature)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, map[string]any{
		"feature": feature,
		"enabled": enabled,
		"plan":    ent.Plan,
		"limit":   ent.Limit,
	})
}
