package iga

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
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
// WithEgress sets the destination policy of connector URLs (SEC-08).
func (h *Handler) WithEgress(opts egress.Options) *Handler {
	h.registry.WithEgress(opts)
	return h
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

// notFoundErrors are the objects of the module that are unknown or of
// another organization (404 with the fixed text, TEN-04).
var notFoundErrors = []error{ErrConnectorNotFound, ErrTaskNotFound, ErrAccessRequestNotFound, ErrReviewItemNotFound, ErrDriftNotFound}

// writeIGAError answers a failed IGA request: an object that is unknown or
// of another organization is 404 with a fixed text (TEN-04), a user,
// connector, credential or collector a request names that is no UUID or not
// of the organization 422 on its field. Database errors go through
// api.WriteRepoError; no table, constraint, policy or SQLSTATE text reaches
// the client (API-03, F-047), the log keeps it.
func writeIGAError(w http.ResponseWriter, r *http.Request, err error) {
	for _, missing := range notFoundErrors {
		if errors.Is(err, missing) {
			api.WriteError(w, 404, "Not Found", missing.Error())
			return
		}
	}
	if api.WriteFieldError(w, err) {
		return
	}
	slog.WarnContext(r.Context(), "iga request failed", "error", err)
	api.WriteRepoError(w, err)
}

func (h *Handler) ListConnectors(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListConnectors(r.Context(), t.OrganizationID, p)
	respondList(w, r, items, total, p, err)
}
func (h *Handler) GetConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrConnectorNotFound.Error())
	if !ok {
		return
	}
	v, err := h.repo.GetConnector(r.Context(), t.OrganizationID, id)
	respondOne(w, r, v, err)
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
			writeIGAError(w, r, err)
			return
		}
		credID = c.ID
	}
	c := &ConnectorConfig{OrganizationID: t.OrganizationID, Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, CredentialID: credID, CollectorID: req.CollectorID, Capabilities: req.Capabilities, Config: req.Config, Status: "active"}
	if c.Capabilities == (ConnectorCapabilities{}) {
		c.Capabilities = DefaultCapabilities(c.Type)
	}
	if err := h.registry.ValidateBaseURL(c.Type, c.BaseURL); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	// A credential or collector of another organization answers like an
	// unknown one, 422 on its field (TEN-04, F-047).
	if err := h.repo.CreateConnector(r.Context(), c); err != nil {
		writeIGAError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, c)
}
func (h *Handler) UpdateConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrConnectorNotFound.Error())
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
			writeIGAError(w, r, err)
			return
		}
		req.CredentialID = &c.ID
	}
	v, err := h.repo.UpdateConnector(r.Context(), t.OrganizationID, id, req)
	respondOne(w, r, v, err)
}
func (h *Handler) DeleteConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrConnectorNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteConnector(r.Context(), t.OrganizationID, id); err != nil {
		writeIGAError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) TestConnector(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrConnectorNotFound.Error())
	if !ok {
		return
	}
	c, err := h.repo.GetConnector(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeIGAError(w, r, err)
		return
	}
	err = h.registry.ValidateBaseURL(c.Type, c.BaseURL)
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrConnectorNotFound.Error())
	if !ok {
		return
	}
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
	respondList(w, r, items, total, p, err)
}
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrTaskNotFound.Error())
	if !ok {
		return
	}
	v, err := h.repo.GetTask(r.Context(), t.OrganizationID, id)
	respondOne(w, r, v, err)
}
func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrTaskNotFound.Error())
	if !ok {
		return
	}
	task, err := h.repo.GetTask(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeIGAError(w, r, err)
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
	respondList(w, r, items, total, p, err)
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
		writeIGAError(w, r, err)
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
	if change.UserID != "" {
		if err := api.RequireUUID("user_id", change.UserID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	// A user or connector of another organization is 422 on its field like
	// an unknown one (TEN-04, F-047).
	tasks, err := h.lifecycle.Resolve(r.Context(), t.OrganizationID, change)
	if err != nil {
		writeIGAError(w, r, err)
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
	respondList(w, r, items, total, p, err)
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
	// The subject is required; ids reach SQL only as UUIDs (F-047).
	if err := api.RequireUUID("subject_user_id", req.SubjectUserID); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if req.ConnectorID != "" {
		if err := api.RequireUUID("connector_id", req.ConnectorID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
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
	// A subject or connector of another organization is 422 on its field
	// like an unknown one (TEN-04, F-047).
	if err := h.repo.CreateAccessRequest(r.Context(), ar); err != nil {
		writeIGAError(w, r, err)
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrAccessRequestNotFound.Error())
	if !ok {
		return
	}
	var req DecisionRequest
	_ = api.ReadJSON(r, &req)
	ar, err := h.repo.DecideAccessRequest(r.Context(), t.OrganizationID, id, status, t.UserID, req.Comment)
	if err != nil {
		writeIGAError(w, r, err)
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
	respondList(w, r, items, total, p, err)
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
		writeIGAError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, rv)
}
func (h *Handler) ListReviewItems(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	reviewID, ok := api.ParseUUIDParam(w, r, "id", "access review not found")
	if !ok {
		return
	}
	p := api.ParsePagination(r)
	items, total, err := h.repo.ListReviewItems(r.Context(), t.OrganizationID, reviewID, p)
	respondList(w, r, items, total, p, err)
}
func (h *Handler) DecideReviewItem(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	itemID, ok := api.ParseUUIDParam(w, r, "itemId", ErrReviewItemNotFound.Error())
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
	it, err := h.repo.DecideReviewItem(r.Context(), t.OrganizationID, itemID, req.Decision, t.UserID)
	if err != nil {
		writeIGAError(w, r, err)
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
	respondList(w, r, items, total, p, err)
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
	if err := api.RequireUUID("connector_id", req.ConnectorID); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	findings, err := h.drift.Reconcile(r.Context(), t.OrganizationID, req.ConnectorID, nil)
	switch {
	case errors.Is(err, ErrConnectorNotFound):
		// Unknown and of another organization alike (TEN-04).
		api.WriteFieldError(w, &api.FieldError{Field: "connector_id", Message: ErrConnectorNotFound.Error(), Status: 422})
		return
	case err != nil:
		if _, _, _, isDB := api.ClassifyDBError(err); isDB {
			writeIGAError(w, r, err)
			return
		}
		// The connector could not be built or read: the cause (egress
		// decision, remote answer) stays in the log (F-047).
		slog.WarnContext(r.Context(), "iga drift reconciliation failed", "connector_id", req.ConnectorID, "error", err)
		api.WriteError(w, 400, "Bad Request", "the connector could not be reconciled")
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrDriftNotFound.Error())
	if !ok {
		return
	}
	f, err := h.repo.GetDrift(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeIGAError(w, r, err)
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
func respondList[T any](w http.ResponseWriter, r *http.Request, items []T, total int, p api.PaginationParams, err error) {
	if err != nil {
		writeIGAError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[T]{Data: items, Total: total, Limit: p.Limit, Offset: p.Offset, HasMore: p.Offset+p.Limit < total})
}
func respondOne(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		writeIGAError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, v)
}
