package compliance

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo      Repository
	evaluator *Evaluator
	reports   *ReportService
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
	r.Get("/api/v1/compliance/report", h.Report)
}
func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, 401, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}
func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRules(r.Context(), t.OrganizationID, r.URL.Query().Get("ci_type_id"), r.URL.Query().Get("category"), r.URL.Query().Get("active") == "true", page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Rule]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "compliance rule not found")
		return
	}
	api.WriteJSON(w, 200, item)
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
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	item := &Rule{OrganizationID: t.OrganizationID, CITypeID: req.CITypeID, Name: req.Name, Description: req.Description, Severity: req.Severity, Category: req.Category, Expression: req.Expression, RemediationHint: req.RemediationHint, Active: active}
	if err := h.repo.CreateRule(r.Context(), item); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateRuleRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.UpdateRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, 404, "Not Found", "compliance rule not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, 404, "Not Found", "compliance rule not found")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	resp, err := h.evaluator.Evaluate(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, resp)
}
func (h *Handler) ListResults(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListResults(r.Context(), t.OrganizationID, r.URL.Query().Get("ci_type_id"), r.URL.Query().Get("status"), page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Result]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) Score(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	items, _, err := h.repo.ListResults(r.Context(), t.OrganizationID, r.URL.Query().Get("ci_type_id"), "", api.PaginationParams{Limit: 100})
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, summarize(items))
}

// Report handles GET /api/v1/compliance/report and produces the
// ISO 27001 / NIS2 evidence document for the tenant. The `standard` query
// parameter selects the framing label (default iso27001).
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
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, report)
}
