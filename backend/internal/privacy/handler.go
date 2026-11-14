package privacy

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the retention policy, the erasure workflow and the
// rights of a single person (report and erasure, SEC-05 [P3], WP-1158).
type Handler struct {
	svc      *Service
	subjects *SubjectService
}

// NewHandler creates the privacy handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// WithSubjects serves the report and the erasure of a person; without it
// the routes answer 503.
func (h *Handler) WithSubjects(subjects *SubjectService) *Handler {
	h.subjects = subjects
	return h
}

// RegisterRoutes mounts the privacy routes on the given router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/privacy/retention", h.GetRetention)
	r.Put("/api/v1/privacy/retention", h.PutRetention)
	r.Post("/api/v1/privacy/erasure", h.RunErasure)
	// The rights of a person (SEC-05 [P3], WP-1158): jobs with user:manage
	// through the segment privacy.
	r.Post("/api/v1/privacy/subjects/report", h.subjects.Report)
	r.Post("/api/v1/privacy/subjects/erase", h.subjects.Erase)
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
		api.WriteRepoError(w, err)
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
		// Field errors name the rule; repository errors never reach the
		// client as text (F-047).
		if !api.WriteFieldError(w, err) {
			api.WriteRepoError(w, err)
		}
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
	if errors.Is(err, ErrKeepsForever) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", ErrKeepsForever.Error())
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, summary)
}
