package compliance

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo      Repository
	evaluator *Evaluator
	reports   *ReportService
	jobs      *Jobs
}

// WithJobs makes POST /api/v1/compliance/evaluations queue the job
// compliance.evaluate (WP-1126).
func (h *Handler) WithJobs(j *Jobs) *Handler {
	h.jobs = j
	return h
}

func NewHandler(repo Repository, evaluator *Evaluator) *Handler {
	return &Handler{repo: repo, evaluator: evaluator}
}

// WithReports attaches the security report service so the handler can serve
// GET /api/v1/compliance/report.
func (h *Handler) WithReports(reports *ReportService) *Handler {
	h.reports = reports
	return h
}
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/compliance/rules", h.ListRules)
	r.Post("/api/v1/compliance/rules", h.CreateRule)
	r.Get("/api/v1/compliance/rules/{id}", h.GetRule)
	r.Patch("/api/v1/compliance/rules/{id}", h.UpdateRule)
	r.Delete("/api/v1/compliance/rules/{id}", h.DeleteRule)
	r.Post("/api/v1/compliance/evaluations", h.Evaluate)
	r.Get("/api/v1/compliance/results", h.ListResults)
	r.Get("/api/v1/compliance/score", h.Score)
	r.Get("/api/v1/compliance/score/history", h.ScoreHistory)
	r.Get("/api/v1/compliance/settings", h.GetSettings)
	r.Put("/api/v1/compliance/settings", h.PutSettings)
	r.Get("/api/v1/compliance/rule-templates", h.ListRuleTemplates)
	r.Post("/api/v1/compliance/rule-templates/{key}/instantiate", h.InstantiateRuleTemplate)
	r.Get("/api/v1/compliance/report", h.Report)
	// The reports as PDF or CSV through the job compliance.report
	// (SEC-04, WP-1129).
	r.Post("/api/v1/compliance/reports", h.CreateReport)
	r.Get("/api/v1/compliance/reports", h.ListReports)
	r.Get("/api/v1/compliance/controls", h.ListControls)
	r.Get("/api/v1/compliance/controls/coverage", h.ControlCoverage)
	r.Put("/api/v1/compliance/rules/{id}/controls", h.PutRuleControls)
}
func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, 401, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}

// writeComplianceError answers a failed compliance request: a rule that is
// unknown or of another organization is 404 (TEN-04), a CI type that is no
// UUID or not visible 422 on ci_type_id. Database errors go through
// api.WriteRepoError; no table, constraint, policy or SQLSTATE text reaches
// the client (API-03, F-047), the log keeps it.
func writeComplianceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrControlsUnavailable):
		api.WriteUnavailable(w, ErrControlsUnavailable.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "compliance request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// ciTypeFilter returns the ci_type_id filter of r; a value that is set but
// no UUID is answered 400 before it reaches SQL (API-03).
func ciTypeFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("ci_type_id")
	if id != "" && !api.IsUUID(id) {
		api.WriteFieldError(w, &api.FieldError{Field: "ci_type_id", Message: "must be a UUID"})
		return "", false
	}
	return id, true
}

func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ciTypeID, ok := ciTypeFilter(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRules(r.Context(), t.OrganizationID, ciTypeID, r.URL.Query().Get("category"), r.URL.Query().Get("active") == "true", page)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Rule]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetRule(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	// The controls of the rule, additive (WP-1128).
	out := RuleWithControls{Rule: item, ControlIDs: []string{}}
	if out.ControlIDs, err = h.repo.RuleControlIDs(r.Context(), t.OrganizationID, id); err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, out)
}
func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateRuleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Severity == "" || req.Category == "" || req.Expression == nil {
		api.WriteError(w, 400, "Bad Request", "name, severity, category and expression are required")
		return
	}
	// expression and applies_when are MET-45 conditions over the inputs
	// of a CI (SEC-03, CMP-02): 422 on the path of the error.
	if err := ValidateCondition("expression", req.Expression); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if req.AppliesWhen != nil {
		if err := ValidateCondition("applies_when", req.AppliesWhen); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	item := &Rule{OrganizationID: t.OrganizationID, CITypeID: req.CITypeID, Name: req.Name, Description: req.Description, Severity: req.Severity, Category: req.Category, Expression: req.Expression, AppliesWhen: req.AppliesWhen, RemediationHint: req.RemediationHint, Active: active}
	// An unknown CI type and a private type of another organization answer
	// alike, 422 on ci_type_id (F-047, TEN-04).
	if err := h.repo.CreateRule(r.Context(), item); err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpdateRuleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Expression != nil {
		if err := ValidateCondition("expression", req.Expression); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	if err := req.decodeAppliesWhen(); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	item, err := h.repo.UpdateRule(r.Context(), t.OrganizationID, id, &req)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteRule(r.Context(), t.OrganizationID, id); err != nil {
		writeComplianceError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	// The evaluation covers the whole organization: a restricted principal
	// is refused before a job exists (E-09).
	err := requireOrgWide(r.Context(), t.OrganizationID)
	if errors.Is(err, ErrOrgWideScopeRequired) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the compliance evaluation covers the whole organization and requires an org-wide principal")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	job, err := h.jobs.Submit(r.Context(), t.OrganizationID, t.UserID)
	if errors.Is(err, ErrJobsUnavailable) {
		api.WriteUnavailable(w, ErrJobsUnavailable.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "compliance evaluation: submit the job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, job)
}
func (h *Handler) ListResults(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ciTypeID, ok := ciTypeFilter(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListResults(r.Context(), t.OrganizationID, ciTypeID, r.URL.Query().Get("status"), page)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Result]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}

// CreateReportRequest is the body of POST /api/v1/compliance/reports.
type CreateReportRequest struct {
	Standard string `json:"standard"`
	Format   string `json:"format"`
	ClientID string `json:"client_id,omitempty"`
}

// CreateReport handles POST /api/v1/compliance/reports (SEC-04, WP-1129
// AK1): 202 with the job compliance.report. standard is iso27001, nis2 or
// kritis, format pdf or csv (422 otherwise); without client_id the report
// covers the organization and needs an org-wide principal (403, E-09), a
// client that is no UUID or not visible is 404.
func (h *Handler) CreateReport(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil || !h.jobs.reports.available() {
		api.WriteUnavailable(w, ErrReportsUnavailable.Error())
		return
	}
	var req CreateReportRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if !slices.Contains(ReportStandards, req.Standard) {
		api.WriteFieldError(w, &api.FieldError{Field: "standard", Message: "must be iso27001, nis2 or kritis", Status: http.StatusUnprocessableEntity})
		return
	}
	if req.Format != FormatPDF && req.Format != FormatCSV {
		api.WriteFieldError(w, &api.FieldError{Field: "format", Message: "must be pdf or csv", Status: http.StatusUnprocessableEntity})
		return
	}
	if req.ClientID == "" {
		err := requireOrgWide(r.Context(), t.OrganizationID)
		if errors.Is(err, ErrOrgWideScopeRequired) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "a report of the whole organization requires an org-wide principal")
			return
		}
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
	} else {
		visible := false
		if api.IsUUID(req.ClientID) {
			var err error
			if visible, err = h.jobs.reports.ClientVisible(r.Context(), t.OrganizationID, req.ClientID); err != nil {
				api.WriteRepoError(w, err)
				return
			}
		}
		if !visible {
			api.WriteError(w, http.StatusNotFound, "Not Found", ErrReportClientNotFound.Error())
			return
		}
	}
	job, err := h.jobs.SubmitReport(r.Context(), t.OrganizationID, t.UserID, &ReportParams{Standard: req.Standard, Format: req.Format, ClientID: req.ClientID})
	if errors.Is(err, ErrReportsUnavailable) {
		api.WriteUnavailable(w, ErrReportsUnavailable.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "compliance report: submit the job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, job)
}

// ListReports handles GET /api/v1/compliance/reports (AK5): the report
// documents visible in the caller's scope, newest first, offset-paged.
func (h *Handler) ListReports(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil || !h.jobs.reports.available() {
		api.WriteUnavailable(w, ErrReportsUnavailable.Error())
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, capped, err := h.jobs.reports.ListReports(r.Context(), t.OrganizationID)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	resp := api.Page(items, page)
	resp.TotalCapped = capped
	api.WriteJSON(w, http.StatusOK, resp)
}

// Report handles GET /api/v1/compliance/report: the preview of a report as
// JSON (the stored reports are POST /api/v1/compliance/reports, WP-1129).
// The `standard` query parameter selects the framing label (default
// iso27001).
func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.reports == nil {
		api.WriteError(w, 503, "Service Unavailable", "security reporting is not configured")
		return
	}
	report, err := h.reports.Generate(r.Context(), t.OrganizationID, r.URL.Query().Get("standard"))
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, report)
}
