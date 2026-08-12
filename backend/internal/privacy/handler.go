package privacy

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the retention policy and the erasure workflow.
type Handler struct {
	svc *Service
}

// NewHandler creates the privacy handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the privacy routes on the given router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/privacy/retention", h.GetRetention)
	r.Put("/api/v1/privacy/retention", h.PutRetention)
	r.Post("/api/v1/privacy/erasure", h.RunErasure)
}

func (h *Handler) org(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return orgID, true
}

// GetRetention handles GET /api/v1/privacy/retention.
func (h *Handler) GetRetention(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.org(w, r)
	if !ok {
		return
	}
	policy, err := h.svc.GetPolicy(r.Context(), orgID)
	if errors.Is(err, ErrNoPolicy) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "no retention policy configured")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, policy)
}

// PutRetention handles PUT /api/v1/privacy/retention.
func (h *Handler) PutRetention(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.org(w, r)
	if !ok {
		return
	}
	var req UpdatePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	policy, err := h.svc.Configure(r.Context(), orgID, req)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, policy)
}

// RunErasure handles POST /api/v1/privacy/erasure and enforces the configured
// retention policy on the tenant's personal data.
func (h *Handler) RunErasure(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.org(w, r)
	if !ok {
		return
	}
	summary, err := h.svc.RunErasure(r.Context(), orgID)
	if errors.Is(err, ErrNoPolicy) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "no retention policy configured")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, summary)
}
