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
	"github.com/prometheus/client_golang/prometheus"
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
	runs        RunStarter
	tx          TxRunner
}

// AccessRequestEvent is the trigger event of the workflow run an access
// request starts (WFL-04).
const AccessRequestEvent = "iga.access_request"

// RunStarter starts the workflow run of an access request (WFL-04): Start
// stores the pending run, Execute runs its actions. The workflow executor
// implements it.
type RunStarter interface {
	Start(ctx context.Context, orgID string, def *workflow.Definition, trigger string, payload workflow.JSONMap) (*workflow.Run, error)
	Execute(ctx context.Context, orgID string, def *workflow.Definition, run *workflow.Run) (*workflow.Run, error)
}

// TxRunner runs fn in one transaction of the context's tenant scope: the
// repository calls fn makes with its context join it
// (database.ContextWithTx). The IGA PG repository implements it.
type TxRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// NewHandler creates the IGA handler. A repository that is a TxRunner stores
// an access request and the run of its workflow in one transaction.
func NewHandler(repo Repository, users user.Repository, creds *credential.Service, discoveryRepo discovery.Repository, workflows workflow.Repository) *Handler {
	registry := NewRegistry(discoveryRepo, nil)
	decrypt := func(ctx context.Context, orgID, id string) (JSONMap, error) {
		if creds == nil {
			return JSONMap{}, nil
		}
		return creds.Decrypt(ctx, orgID, id)
	}
	h := &Handler{repo: repo, users: users, credentials: creds, registry: registry, lifecycle: NewLifecycleService(repo), runner: NewTaskRunner(repo, registry, decrypt), drift: NewDriftDetector(repo, registry, decrypt), workflows: workflows}
	if runner, ok := repo.(TxRunner); ok {
		h.tx = runner
	}
	return h
}

// WithWorkflowRuns sets the executor that starts the workflow run of an
// access request (WFL-04). Without it or without the workflow repository an
// access request starts no run.
func (h *Handler) WithWorkflowRuns(runs RunStarter) *Handler {
	h.runs = runs
	return h
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

// WriteFailures counts the IGA writes that failed and were answered with an
// error, by operation (F-056). Before, these errors were dropped: an approval
// answered 200 without its provisioning task.
var WriteFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "iga_write_failures_total",
	Help:      "IGA writes (access approvals, review decisions, provisioning tasks, drift findings, connector sync state, SCIM group members) that failed; the request was answered with an error and nothing of it was kept.",
}, []string{"operation"})

// RegisterMetrics registers the IGA metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(WriteFailures)
}

// isClientError reports whether err names an object or field of the request
// that is unknown or of another organization; such a refusal is no failed
// write.
func isClientError(err error) bool {
	for _, missing := range notFoundErrors {
		if errors.Is(err, missing) {
			return true
		}
	}
	var fe *api.FieldError
	return errors.As(err, &fe)
}

// writeFailed answers a failed write of operation like writeIGAError; a
// failure that is not the client's is logged with the operation and counted
// in WriteFailures.
func writeFailed(w http.ResponseWriter, r *http.Request, operation string, err error) {
	if isClientError(err) {
		writeIGAError(w, r, err)
		return
	}
	WriteFailures.WithLabelValues(operation).Inc()
	slog.ErrorContext(r.Context(), "iga write failed", "operation", operation, "error", err)
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
	findings, err := h.drift.Reconcile(r.Context(), t.OrganizationID, id, nil)
	if err != nil {
		slog.WarnContext(r.Context(), "iga connector sync: reconciliation failed", "connector_id", id, "error", err)
		findings = nil
	}
	// The sync time and the findings are stored in one transaction; a
	// failing write is answered, not dropped (F-056).
	err = h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		if markErr := h.repo.MarkConnectorSynced(ctx, t.OrganizationID, id, now); markErr != nil {
			return markErr
		}
		for i := range findings {
			if driftErr := h.repo.CreateDrift(ctx, &findings[i]); driftErr != nil {
				return driftErr
			}
		}
		return nil
	})
	if err != nil {
		writeFailed(w, r, "connector_sync", err)
		return
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
	if err := h.repo.UpdateTask(r.Context(), task); err != nil {
		writeFailed(w, r, "task_retry", err)
		return
	}
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
	def, err := h.accessRequestWorkflow(r.Context(), t.OrganizationID)
	if err != nil {
		slog.WarnContext(r.Context(), "iga: list access request workflows failed", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	// The pending run of the workflow and the request are stored in one
	// transaction, the run first so the request names it: a refused request
	// leaves no run, a run that cannot be stored no request, and
	// workflow_run_id never names a missing run (WFL-04). The actions run
	// after the commit; a failing one marks the run failed, the request
	// stays. A subject or connector of another organization is 422 on its
	// field like an unknown one (TEN-04, F-047).
	var run *workflow.Run
	err = h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		if def != nil {
			var startErr error
			run, startErr = h.runs.Start(ctx, t.OrganizationID, def, AccessRequestEvent, workflow.JSONMap{
				"requester_id": ar.RequesterID, "subject_user_id": ar.SubjectUserID, "connector_id": ar.ConnectorID,
				"entitlement": ar.Entitlement, "reason": ar.Reason,
			})
			if startErr != nil {
				return startErr
			}
			ar.WorkflowRunID = run.ID
		}
		return h.repo.CreateAccessRequest(ctx, ar)
	})
	if err != nil {
		writeIGAError(w, r, err)
		return
	}
	if run != nil {
		// The first status update of the run stores the request id in its
		// context. The committed run executes even when the client goes
		// away.
		run.Context["access_request_id"] = ar.ID
		if _, err = h.runs.Execute(context.WithoutCancel(r.Context()), t.OrganizationID, def, run); err != nil {
			slog.WarnContext(r.Context(), "iga: workflow run of an access request failed", "access_request_id", ar.ID, "workflow_run_id", run.ID, "error", err)
		}
	}
	api.WriteJSON(w, 201, ar)
}

// accessRequestWorkflow returns the first active workflow definition, by
// name, whose trigger is AccessRequestEvent; nil when there is none or the
// handler starts no runs.
func (h *Handler) accessRequestWorkflow(ctx context.Context, orgID string) (*workflow.Definition, error) {
	if h.workflows == nil || h.runs == nil {
		return nil, nil
	}
	page := api.PaginationParams{Limit: 50}
	for {
		defs, total, err := h.workflows.ListDefinitions(ctx, orgID, true, page)
		if err != nil {
			return nil, err
		}
		for i := range defs {
			if workflow.TriggerMatches(&defs[i], AccessRequestEvent) {
				return &defs[i], nil
			}
		}
		page.Offset += page.Limit
		if len(defs) == 0 || page.Offset >= total {
			return nil, nil
		}
	}
}

// inTx runs fn in one transaction, or as it is without a TxRunner.
func (h *Handler) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if h.tx == nil {
		return fn(ctx)
	}
	return h.tx.RunInTx(ctx, orgID, fn)
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
	if status == "approved" {
		// The approval and its provisioning task are one unit: when the
		// task cannot be written the request stays pending and the client
		// gets the error (F-056).
		ar, err := h.repo.ApproveAccessRequest(r.Context(), t.OrganizationID, id, t.UserID, req.Comment)
		if err != nil {
			writeFailed(w, r, "access_approval", err)
			return
		}
		api.WriteJSON(w, 200, ar)
		return
	}
	ar, err := h.repo.DecideAccessRequest(r.Context(), t.OrganizationID, id, status, t.UserID, req.Comment)
	if err != nil {
		writeIGAError(w, r, err)
		return
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
	// A revocation and its deprovisioning task are stored in one
	// transaction, like an approval (F-056).
	var it *AccessReviewItem
	err := h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		var err error
		it, err = h.repo.DecideReviewItem(ctx, t.OrganizationID, itemID, req.Decision, t.UserID)
		if err != nil {
			return err
		}
		if req.Decision == ReviewDecisionRevoke && it.ConnectorID != "" {
			return h.repo.CreateTask(ctx, &ProvisioningTask{OrganizationID: t.OrganizationID, ConnectorID: it.ConnectorID, UserID: it.UserID, Action: TaskActionRemoveGroupMember, Payload: JSONMap{"entitlement": it.Entitlement, "group_id": it.Entitlement, "account_id": it.UserID}})
		}
		return nil
	})
	if err != nil {
		writeFailed(w, r, "review_decision", err)
		return
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
	// The findings are stored all or none (F-056).
	err = h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		for i := range findings {
			if driftErr := h.repo.CreateDrift(ctx, &findings[i]); driftErr != nil {
				return driftErr
			}
		}
		return nil
	})
	if err != nil {
		writeFailed(w, r, "drift_findings", err)
		return
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
	// The remediation task and the finding that names it are stored in one
	// transaction (F-056).
	err = h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		if taskErr := h.repo.CreateTask(ctx, task); taskErr != nil {
			return taskErr
		}
		f.Status = "remediating"
		f.RemediationTaskID = task.ID
		return h.repo.UpdateDrift(ctx, f)
	})
	if err != nil {
		writeFailed(w, r, "drift_remediation", err)
		return
	}
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
