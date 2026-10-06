package entitlement

import (
	"errors"
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

// RegisterRoutes registers the tenant entitlement routes. They are
// read-only: entitlements are written by the operator only, under
// /api/v1/admin/orgs/{id}/entitlements (ENT-04, E-12).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/entitlements", h.List)
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

// GrantRequest is the body of an entitlement change (operator path).
type GrantRequest struct {
	FeatureKey string           `json:"feature_key"`
	Plan       Plan             `json:"plan"`
	Enabled    *bool            `json:"enabled,omitempty"`
	Limits     map[string]int64 `json:"limits,omitempty"`
	ValidUntil *time.Time       `json:"valid_until,omitempty"`
	Source     string           `json:"source,omitempty"`
}

// Entitlement returns the entitlement the request stores for orgID.
func (req *GrantRequest) Entitlement(orgID string) (Entitlement, error) {
	if req.FeatureKey == "" {
		return Entitlement{}, invalid(errors.New("feature_key is required"))
	}
	plan := req.Plan
	if plan == "" {
		plan = PlanEssential
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return Entitlement{OrganizationID: orgID, FeatureKey: req.FeatureKey, Plan: plan, Enabled: enabled,
		Limits: req.Limits, ValidUntil: req.ValidUntil, Source: req.Source}, nil
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
		"limits":  ent.Limits,
	})
}
