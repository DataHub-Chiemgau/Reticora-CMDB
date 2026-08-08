package workflow

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo Repository
	exec *Executor
}

func NewHandler(repo Repository, exec *Executor) *Handler { return &Handler{repo: repo, exec: exec} }
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/workflows", h.ListDefinitions)
	r.Post("/api/v1/workflows", h.CreateDefinition)
	r.Get("/api/v1/workflows/{id}", h.GetDefinition)
	r.Patch("/api/v1/workflows/{id}", h.UpdateDefinition)
	r.Delete("/api/v1/workflows/{id}", h.DeleteDefinition)
	r.Post("/api/v1/workflows/{id}/runs", h.TriggerRun)
	r.Get("/api/v1/workflow-runs", h.ListRuns)
	r.Get("/api/v1/workflow-runs/{id}", h.GetRun)
	r.Post("/api/v1/workflow-runs/{id}/approval", h.ApproveRun)
}
func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, 401, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}
func (h *Handler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListDefinitions(r.Context(), t.OrganizationID, r.URL.Query().Get("active") == "true", page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Definition]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	item, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "workflow definition not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) CreateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Trigger == nil || len(req.Actions) == 0 {
		api.WriteError(w, 400, "Bad Request", "name, trigger and actions are required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	item := &Definition{OrganizationID: t.OrganizationID, Name: req.Name, Description: req.Description, Trigger: req.Trigger, Conditions: req.Conditions, Actions: req.Actions, Active: active}
	if err := h.repo.CreateDefinition(r.Context(), item); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
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
		api.WriteError(w, 404, "Not Found", "workflow definition not found")
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, 404, "Not Found", "workflow definition not found")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) TriggerRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	def, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "workflow definition not found")
		return
	}
	var req TriggerRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}
	if req.Context == nil {
		req.Context = JSONMap{}
	}
	run, err := h.exec.Trigger(r.Context(), t.OrganizationID, def, req.Trigger, req.Context)
	if err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, 201, run)
}
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRuns(r.Context(), t.OrganizationID, r.URL.Query().Get("workflow_id"), r.URL.Query().Get("status"), page)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Run]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	run, err := h.repo.GetRun(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "workflow run not found")
		return
	}
	api.WriteJSON(w, 200, run)
}
func (h *Handler) ApproveRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req ApprovalRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	run, err := h.exec.Approve(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.Decision, req.Comment)
	if err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, 200, run)
}
