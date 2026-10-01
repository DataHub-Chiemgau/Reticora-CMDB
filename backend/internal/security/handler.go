package security

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

var validKinds = map[string]bool{
	"vulnerability": true, "outdated_software": true, "outdated_firmware": true, "missing_patch": true,
}
var validSeverities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
var validStatuses = map[string]bool{"open": true, "acknowledged": true, "resolved": true, "false_positive": true}

// Handler provides HTTP handlers for security findings (patch posture).
type Handler struct {
	repo Repository
}

// NewHandler creates a new security handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers security finding routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/security/findings", h.List)
	r.Post("/api/v1/security/findings", h.Create)
	r.Get("/api/v1/security/findings/summary", h.Summary)
	r.Get("/api/v1/security/findings/{id}", h.Get)
	r.Patch("/api/v1/security/findings/{id}", h.Update)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		CIID:     r.URL.Query().Get("ci_id"),
		Kind:     r.URL.Query().Get("kind"),
		Severity: r.URL.Query().Get("severity"),
		Status:   r.URL.Query().Get("status"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Finding]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "finding not found")
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
	var req CreateFindingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if !validKinds[req.Kind] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "kind must be one of vulnerability, outdated_software, outdated_firmware, missing_patch")
		return
	}
	if req.Severity != "" && !validSeverities[req.Severity] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid severity")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}
	f := &Finding{
		OrganizationID:   t.OrganizationID,
		CIID:             req.CIID,
		Kind:             req.Kind,
		Severity:         req.Severity,
		Title:            req.Title,
		Detail:           req.Detail,
		PackageName:      req.PackageName,
		InstalledVersion: req.InstalledVersion,
		FixedVersion:     req.FixedVersion,
		Reference:        req.Reference,
		Status:           "open",
	}
	if err := h.repo.Create(r.Context(), f); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, f)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateFindingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Status != nil && !validStatuses[*req.Status] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid status")
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "finding not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Summary handles GET /api/v1/security/findings/summary — open findings by
// severity for the security cockpit.
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	summary, err := h.repo.Summary(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	total := 0
	for _, n := range summary {
		total += n
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"by_severity": summary, "open_total": total})
}
