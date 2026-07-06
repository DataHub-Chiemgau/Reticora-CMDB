package entitlement

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/entitlements", h.List)
	mux.HandleFunc("POST /api/v1/entitlements", h.Grant)
	mux.HandleFunc("GET /api/v1/entitlements/check/{feature}", h.Check)
}

// List handles GET /api/v1/entitlements.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	items := h.service.List(t.OrganizationID)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Entitlement]{
		Data:    items,
		Total:   len(items),
		Limit:   len(items),
		Offset:  0,
		HasMore: false,
	})
}

type grantRequest struct {
	FeatureKey string `json:"feature_key"`
	Plan       Plan   `json:"plan"`
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

	h.service.Grant(t.OrganizationID, req.FeatureKey, req.Plan)
	items := h.service.List(t.OrganizationID)
	var granted Entitlement
	for _, item := range items {
		if item.FeatureKey == req.FeatureKey {
			granted = item
			break
		}
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

	feature := r.PathValue("feature")
	if feature == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "feature is required")
		return
	}

	api.WriteJSON(w, http.StatusOK, map[string]any{
		"feature": feature,
		"enabled": h.service.IsEnabled(r.Context(), t.OrganizationID, feature),
	})
}
