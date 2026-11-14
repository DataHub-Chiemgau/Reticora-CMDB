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
	r.Get("/api/v1/entitlements/usage", h.Usage)
	r.Get("/api/v1/entitlements/plans", h.Plans)
}

// Usage handles GET /api/v1/entitlements/usage (PKG-05, WP-885): use, limit
// and origin of every quota; a principal without an org-wide scope gets
// used null (F-414).
func (h *Handler) Usage(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.service.Usage(r.Context(), t.OrganizationID, UsageVisible(r.Context()))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	plan, _, err := h.service.OrganizationPlan(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, QuotaUsageResponse{Data: items, Plan: plan})
}

// Plans handles GET /api/v1/entitlements/plans: the plan matrix and the
// add-ons for the plan comparison (WP-885, WP-886).
func (h *Handler) Plans(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, PlanMatrixResponse{Data: PlanMatrix(), Addons: AddonCatalog()})
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
	plan, changed, err := h.service.OrganizationPlan(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	if items == nil {
		items = []Entitlement{}
	}
	api.WriteJSON(w, http.StatusOK, ListResponse{
		Data: items, Total: len(items), Limit: len(items), Offset: 0, HasMore: false,
		Plan: plan, PlanChangedAt: changed,
	})
}

// ListResponse is the answer of GET /api/v1/entitlements: the envelope of
// api.ListResponse (not embedded: its MarshalJSON would drop the plan) with
// the rows and their origin, and the leading plan of the organization
// (organization.plan) with the time of its last change (PKG-01, WP-880).
type ListResponse struct {
	Data          []Entitlement `json:"data"`
	Total         int           `json:"total"`
	Limit         int           `json:"limit"`
	Offset        int           `json:"offset"`
	HasMore       bool          `json:"has_more"`
	Plan          Plan          `json:"plan"`
	PlanChangedAt *time.Time    `json:"plan_changed_at"`
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
		return Entitlement{}, invalid("feature_key", errors.New("feature_key is required"))
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
