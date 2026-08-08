package iga

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo        Repository
	users       user.Repository
	credentials *credential.Service
	registry    *Registry
	lifecycle   *LifecycleService
	runner      *TaskRunner
	drift       *DriftDetector
	workflows   workflow.Repository
}

func NewHandler(repo Repository, users user.Repository, creds *credential.Service, discoveryRepo discovery.Repository, workflows workflow.Repository) *Handler {
	registry := NewRegistry(discoveryRepo, nil)
	decrypt := func(ctx context.Context, orgID, id string) (JSONMap, error) {
		if creds == nil {
			return JSONMap{}, nil
		}
		return creds.Decrypt(ctx, orgID, id)
	}
	return &Handler{repo: repo, users: users, credentials: creds, registry: registry, lifecycle: NewLifecycleService(repo), runner: NewTaskRunner(repo, registry, decrypt), drift: NewDriftDetector(repo, registry, decrypt), workflows: workflows}
}
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/iga/connectors", h.ListConnectors)
	r.Post("/api/v1/iga/connectors", h.CreateConnector)
	r.Get("/api/v1/iga/connectors/{id}", h.GetConnector)
	r.Patch("/api/v1/iga/connectors/{id}", h.UpdateConnector)
	r.Delete("/api/v1/iga/connectors/{id}", h.DeleteConnector)
	r.Post("/api/v1/iga/connectors/{id}/test", h.TestConnector)
	r.Post("/api/v1/iga/connectors/{id}/sync", h.SyncConnector)
	r.Get("/api/v1/iga/tasks", h.ListTasks)
	r.Get("/api/v1/iga/tasks/{id}", h.GetTask)
	r.Post("/api/v1/iga/tasks/{id}/retry", h.RetryTask)
	r.Get("/api/v1/iga/jml-policies", h.ListPolicies)
	r.Post("/api/v1/iga/jml-policies", h.CreatePolicy)
	r.Post("/api/v1/iga/jml/resolve", h.ResolveLifecycle)
	r.Get("/api/v1/iga/access-requests", h.ListAccessRequests)
	r.Post("/api/v1/iga/access-requests", h.CreateAccessRequest)
	r.Post("/api/v1/iga/access-requests/{id}/approve", h.ApproveAccessRequest)
	r.Post("/api/v1/iga/access-requests/{id}/reject", h.RejectAccessRequest)
	r.Get("/api/v1/iga/access-reviews", h.ListReviews)
	r.Post("/api/v1/iga/access-reviews", h.CreateReview)
	r.Get("/api/v1/iga/access-reviews/{id}/items", h.ListReviewItems)
	r.Post("/api/v1/iga/access-reviews/{id}/items/{itemId}/decision", h.DecideReviewItem)
	r.Get("/api/v1/iga/drift", h.ListDrift)
	r.Post("/api/v1/iga/drift/reconcile", h.ReconcileDrift)
	r.Post("/api/v1/iga/drift/{id}/remediate", h.RemediateDrift)
	r.Get("/scim/v2/ServiceProviderConfig", h.ServiceProviderConfig)
	r.Get("/scim/v2/Users", h.SCIMListUsers)
	r.Post("/scim/v2/Users", h.SCIMCreateUser)
	r.Get("/scim/v2/Users/{id}", h.SCIMGetUser)
	r.Put("/scim/v2/Users/{id}", h.SCIMPutUser)
	r.Patch("/scim/v2/Users/{id}", h.SCIMPatchUser)
	r.Delete("/scim/v2/Users/{id}", h.SCIMDeleteUser)
	r.Get("/scim/v2/Groups", h.SCIMListGroups)
	r.Post("/scim/v2/Groups", h.SCIMCreateGroup)
	r.Get("/scim/v2/Groups/{id}", h.SCIMGetGroup)
	r.Put("/scim/v2/Groups/{id}", h.SCIMPutGroup)
	r.Patch("/scim/v2/Groups/{id}", h.SCIMPatchGroup)
	r.Delete("/scim/v2/Groups/{id}", h.SCIMDeleteGroup)
}
func tenantInfo(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, 401, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}
func (h *Handler) ListConnectors(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListConnectors(r.Context(), t.OrganizationID, p)
	respondList(w, items, total, p, err)
}
func (h *Handler) GetConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetConnector(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	respondOne(w, v, err, "connector not found")
}
func (h *Handler) CreateConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateConnectorRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Type == "" {
		api.WriteError(w, 400, "Bad Request", "name and type are required")
		return
	}
	credID := req.CredentialID
	if credID == "" && req.Secret != nil && h.credentials != nil {
		c, err := h.credentials.Create(r.Context(), credential.CreateRequest{OrganizationID: t.OrganizationID, Name: req.Name + " token", Kind: credential.KindAPIToken, Scope: "iga", Secret: req.Secret})
		if err != nil {
			api.WriteError(w, 400, "Bad Request", err.Error())
			return
		}
		credID = c.ID
	}
	c := &ConnectorConfig{OrganizationID: t.OrganizationID, Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, CredentialID: credID, CollectorID: req.CollectorID, Capabilities: req.Capabilities, Config: req.Config, Status: "active"}
	if c.Capabilities == (ConnectorCapabilities{}) {
		c.Capabilities = DefaultCapabilities(c.Type)
	}
	if c.Type == ConnectorTypeSCIM {
		if _, err := NewSCIMConnector(c.BaseURL, "", nil); err != nil {
			api.WriteError(w, 400, "Bad Request", err.Error())
			return
		}
	}
	if err := h.repo.CreateConnector(r.Context(), c); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, c)
}
func (h *Handler) UpdateConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req UpdateConnectorRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Secret != nil && h.credentials != nil {
		c, err := h.credentials.Create(r.Context(), credential.CreateRequest{OrganizationID: t.OrganizationID, Name: "IGA connector token", Kind: credential.KindAPIToken, Scope: "iga", Secret: req.Secret})
		if err != nil {
			api.WriteError(w, 400, "Bad Request", err.Error())
			return
		}
		req.CredentialID = &c.ID
	}
	v, err := h.repo.UpdateConnector(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	respondOne(w, v, err, "connector not found")
}
func (h *Handler) DeleteConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteConnector(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, 404, "Not Found", "connector not found")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) TestConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	c, err := h.repo.GetConnector(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "connector not found")
		return
	}
	if c.Type == ConnectorTypeSCIM {
		_, err = NewSCIMConnector(c.BaseURL, "", nil)
	}
	if err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, 200, JSONMap{"status": "ok", "capabilities": c.Capabilities})
}
func (h *Handler) SyncConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	now := time.Now().UTC()
	_ = h.repo.MarkConnectorSynced(r.Context(), t.OrganizationID, id, now)
	findings, err := h.drift.Reconcile(r.Context(), t.OrganizationID, id, nil)
	if err == nil {
		for i := range findings {
			_ = h.repo.CreateDrift(r.Context(), &findings[i])
		}
	}
	api.WriteJSON(w, 202, JSONMap{"status": "queued", "findings": len(findings)})
}
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListTasks(r.Context(), t.OrganizationID, r.URL.Query().Get("status"), p)
	respondList(w, items, total, p, err)
}
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetTask(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	respondOne(w, v, err, "task not found")
}
func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	task, err := h.repo.GetTask(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "task not found")
		return
	}
	task.Status = TaskStatusPending
	task.NextRunAt = time.Now().UTC()
	task.Error = ""
	_ = h.repo.UpdateTask(r.Context(), task)
	api.WriteJSON(w, 200, task)
}
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListPolicies(r.Context(), t.OrganizationID, r.URL.Query().Get("event"), false, p)
	respondList(w, items, total, p, err)
}
func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateLifecyclePolicyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	p := &LifecyclePolicy{OrganizationID: t.OrganizationID, Name: req.Name, Event: req.Event, Priority: req.Priority, Active: active, Conditions: req.Conditions, Actions: req.Actions}
	if err := h.repo.CreatePolicy(r.Context(), p); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, p)
}
func (h *Handler) ResolveLifecycle(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var change IdentityChange
	if err := api.ReadJSON(r, &change); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	tasks, err := h.lifecycle.Resolve(r.Context(), t.OrganizationID, change)
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 202, tasks)
}
func (h *Handler) ListAccessRequests(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListAccessRequests(r.Context(), t.OrganizationID, r.URL.Query().Get("status"), p)
	respondList(w, items, total, p, err)
}
func (h *Handler) CreateAccessRequest(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateAccessRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	ar := &AccessRequest{OrganizationID: t.OrganizationID, RequesterID: t.UserID, SubjectUserID: req.SubjectUserID, ConnectorID: req.ConnectorID, Entitlement: req.Entitlement, Reason: req.Reason, Status: "pending"}
	if h.workflows != nil {
		defs, _, _ := h.workflows.ListDefinitions(r.Context(), t.OrganizationID, true, api.PaginationParams{Limit: 50})
		for _, d := range defs {
			if m, ok := d.Trigger["event"].(string); ok && m == "iga.access_request" {
				ar.WorkflowRunID = d.ID
				break
			}
		}
	}
	if err := h.repo.CreateAccessRequest(r.Context(), ar); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, ar)
}
func (h *Handler) ApproveAccessRequest(w http.ResponseWriter, r *http.Request) {
	h.decideAccess(w, r, "approved")
}
func (h *Handler) RejectAccessRequest(w http.ResponseWriter, r *http.Request) {
	h.decideAccess(w, r, "rejected")
}
func (h *Handler) decideAccess(w http.ResponseWriter, r *http.Request, status string) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req DecisionRequest
	_ = api.ReadJSON(r, &req)
	ar, err := h.repo.DecideAccessRequest(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), status, t.UserID, req.Comment)
	if err != nil {
		api.WriteError(w, 404, "Not Found", "access request not found")
		return
	}
	if status == "approved" && ar.ConnectorID != "" {
		_ = h.repo.CreateTask(r.Context(), &ProvisioningTask{OrganizationID: t.OrganizationID, ConnectorID: ar.ConnectorID, UserID: ar.SubjectUserID, Action: TaskActionAddGroupMember, Payload: JSONMap{"entitlement": ar.Entitlement, "group_id": ar.Entitlement, "account_id": ar.SubjectUserID}})
	}
	api.WriteJSON(w, 200, ar)
}
func (h *Handler) ListReviews(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListReviews(r.Context(), t.OrganizationID, r.URL.Query().Get("status"), p)
	respondList(w, items, total, p, err)
}
func (h *Handler) CreateReview(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateReviewRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	rv := &AccessReview{OrganizationID: t.OrganizationID, Name: req.Name, Description: req.Description, DueAt: req.DueAt, Status: "active"}
	if err := h.repo.CreateReview(r.Context(), rv, req.Items); err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 201, rv)
}
func (h *Handler) ListReviewItems(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListReviewItems(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), p)
	respondList(w, items, total, p, err)
}
func (h *Handler) DecideReviewItem(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req DecisionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Decision == "" {
		req.Decision = ReviewDecisionApprove
	}
	it, err := h.repo.DecideReviewItem(r.Context(), t.OrganizationID, chi.URLParam(r, "itemId"), req.Decision, t.UserID)
	if err != nil {
		api.WriteError(w, 404, "Not Found", "review item not found")
		return
	}
	if req.Decision == ReviewDecisionRevoke && it.ConnectorID != "" {
		_ = h.repo.CreateTask(r.Context(), &ProvisioningTask{OrganizationID: t.OrganizationID, ConnectorID: it.ConnectorID, UserID: it.UserID, Action: TaskActionRemoveGroupMember, Payload: JSONMap{"entitlement": it.Entitlement, "group_id": it.Entitlement, "account_id": it.UserID}})
	}
	api.WriteJSON(w, 200, it)
}
func (h *Handler) ListDrift(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListDrift(r.Context(), t.OrganizationID, r.URL.Query().Get("status"), p)
	respondList(w, items, total, p, err)
}
func (h *Handler) ReconcileDrift(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req ReconcileRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	findings, err := h.drift.Reconcile(r.Context(), t.OrganizationID, req.ConnectorID, nil)
	if err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	for i := range findings {
		_ = h.repo.CreateDrift(r.Context(), &findings[i])
	}
	api.WriteJSON(w, 202, findings)
}
func (h *Handler) RemediateDrift(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	f, err := h.repo.GetDrift(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, 404, "Not Found", "drift not found")
		return
	}
	action := TaskActionCreateAccount
	if strings.Contains(f.DriftType, "orphan") {
		action = TaskActionDisableAccount
	}
	task := &ProvisioningTask{OrganizationID: t.OrganizationID, ConnectorID: f.ConnectorID, UserID: f.UserID, ExternalID: f.ExternalID, Action: action, Payload: JSONMap{"drift_id": f.ID}}
	_ = h.repo.CreateTask(r.Context(), task)
	f.Status = "remediating"
	f.RemediationTaskID = task.ID
	_ = h.repo.UpdateDrift(r.Context(), f)
	api.WriteJSON(w, 202, task)
}
func respondList[T any](w http.ResponseWriter, items []T, total int, p api.PaginationParams, err error) {
	if err != nil {
		api.WriteError(w, 500, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[T]{Data: items, Total: total, Limit: p.Limit, Offset: p.Offset, HasMore: p.Offset+p.Limit < total})
}
func respondOne(w http.ResponseWriter, v any, err error, msg string) {
	if err != nil {
		api.WriteError(w, 404, "Not Found", msg)
		return
	}
	api.WriteJSON(w, 200, v)
}
