package security

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

var validSeverities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
var validStatuses = map[string]bool{StatusOpen: true, StatusAcknowledged: true, StatusResolved: true, StatusFalsePositive: true}

// Handler provides HTTP handlers for security findings (patch posture).
// The routes need finding:read and finding:write (authz, WP-866).
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

// List handles GET /api/v1/security/findings with the filters ci_id,
// kind, severity, status, source and cve (an element of cve_ids).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := FilterParams{
		CIID:     q.Get("ci_id"),
		Kind:     q.Get("kind"),
		Severity: q.Get("severity"),
		Status:   q.Get("status"),
		Source:   q.Get("source"),
		CVE:      q.Get("cve"),
	}
	if filter.Source != "" && !slices.Contains(Sources, filter.Source) {
		api.WriteFieldError(w, &api.FieldError{Field: "source", Message: "source must be one of " + strings.Join(Sources, ", "), Status: 422})
		return
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Finding]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(items) < total,
	})
}

// Get handles GET /api/v1/security/findings/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeFindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/security/findings: a manual finding (source
// manual) through the upsert; 201 for a new finding, 200 when the same
// finding (fingerprint) exists and was updated.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateFindingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if !slices.Contains(Kinds, req.Kind) {
		api.WriteFieldError(w, &api.FieldError{Field: "kind", Message: "kind must be one of " + strings.Join(Kinds, ", ")})
		return
	}
	if req.Severity != "" && !validSeverities[req.Severity] {
		api.WriteFieldError(w, &api.FieldError{Field: "severity", Message: "severity must be one of low, medium, high, critical"})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "title", Message: "title is required"})
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
		Status:           StatusOpen,
		Source:           SourceManual,
	}
	res, err := h.repo.Upsert(r.Context(), f)
	if err != nil {
		writeFindingError(w, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	api.WriteJSON(w, status, f)
}

// Update handles PATCH /api/v1/security/findings/{id}: a status change
// along the allowed transitions (422 on status otherwise), with
// status_reason (required for false_positive).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateFindingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if req.Status == nil {
		api.WriteFieldError(w, errNoStatus)
		return
	}
	if !validStatuses[*req.Status] {
		api.WriteFieldError(w, &api.FieldError{Field: "status", Message: "status must be one of open, acknowledged, resolved, false_positive", Status: 422})
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeFindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// writeFindingError answers a repository error: 404 for an unknown or
// invisible finding, the field problem of a refused value, else the
// sanitized repository error.
func writeFindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "finding not found")
	case isFieldError(err):
		api.WriteFieldError(w, err)
	default:
		api.WriteRepoError(w, err)
	}
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
