package form

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct{ repo Repository }

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/forms", h.ListDefinitions)
	r.Post("/api/v1/forms", h.CreateDefinition)
	r.Get("/api/v1/forms/{id}", h.GetDefinition)
	r.Patch("/api/v1/forms/{id}", h.UpdateDefinition)
	r.Delete("/api/v1/forms/{id}", h.DeleteDefinition)
	r.Get("/api/v1/forms/{id}/submissions", h.ListFormSubmissions)
	r.Post("/api/v1/forms/{id}/submissions", h.CreateSubmission)
	r.Get("/api/v1/form-submissions", h.ListSubmissions)
	r.Get("/api/v1/form-submissions/{id}", h.GetSubmission)
}

func tenantInfo(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}
func activeOnly(r *http.Request) bool { return r.URL.Query().Get("active") == "true" }

func (h *Handler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListDefinitions(r.Context(), t.OrganizationID, r.URL.Query().Get("client_id"), activeOnly(r), page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Definition]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "form definition not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) CreateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Schema == nil {
		api.WriteError(w, 400, "Bad Request", "name and schema are required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	item := &Definition{OrganizationID: t.OrganizationID, ClientID: req.ClientID, Name: req.Name, Description: req.Description, Schema: req.Schema, UIHints: req.UIHints, Active: active}
	if item.UIHints == nil {
		item.UIHints = JSONMap{}
	}
	if err := h.repo.CreateDefinition(r.Context(), item); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req UpdateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.UpdateDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, 404, "Not Found", "form definition not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, 404, "Not Found", "form definition not found")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) ListFormSubmissions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	filter := SubmissionFilter{FormID: chi.URLParam(r, "id"), Status: r.URL.Query().Get("status")}
	items, total, err := h.repo.ListSubmissions(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Submission]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) ListSubmissions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	filter := SubmissionFilter{FormID: r.URL.Query().Get("form_id"), Status: r.URL.Query().Get("status"), TicketID: r.URL.Query().Get("ticket_id"), CIID: r.URL.Query().Get("ci_id")}
	items, total, err := h.repo.ListSubmissions(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Submission]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetSubmission(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetSubmission(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "form submission not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) CreateSubmission(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	def, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "form definition not found")
		return
	}
	if !def.Active {
		api.WriteError(w, 400, "Bad Request", "form definition is inactive")
		return
	}
	var req CreateSubmissionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Values == nil {
		req.Values = JSONMap{}
	}
	if err := Validate(def.Schema, req.Values); err != nil {
		var ve ValidationError
		if errors.As(err, &ve) {
			writeValidationProblem(w, ve)
			return
		}
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	status := req.Status
	if status == "" {
		status = "submitted"
	}
	sub := &Submission{OrganizationID: t.OrganizationID, FormID: def.ID, Values: req.Values, SubmittedBy: t.UserID, CIID: req.CIID, TicketID: req.TicketID, Status: status}
	if err := h.repo.CreateSubmission(r.Context(), sub); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, sub)
}

func writeValidationProblem(w http.ResponseWriter, ve ValidationError) {
	api.WriteJSON(w, http.StatusBadRequest, struct {
		Type   string       `json:"type"`
		Title  string       `json:"title"`
		Status int          `json:"status"`
		Detail string       `json:"detail"`
		Fields []FieldError `json:"fields"`
	}{Type: "https://reticora.io/problems/validation", Title: "Validation Failed", Status: http.StatusBadRequest, Detail: "submitted values do not match the form schema", Fields: ve.Fields})
}
