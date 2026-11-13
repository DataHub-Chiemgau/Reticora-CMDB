package workflow

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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
	r.Get("/api/v1/workflows/{id}/versions", h.ListVersions)
	r.Get("/api/v1/workflows/{id}/versions/{version}", h.GetVersion)
	r.Post("/api/v1/workflows/{id}/runs", h.TriggerRun)
	r.Get("/api/v1/workflow-runs", h.ListRuns)
	r.Get("/api/v1/workflow-runs/{id}", h.GetRun)
	r.Post("/api/v1/workflow-runs/{id}/approval", h.ApproveRun)
	r.Post("/api/v1/workflow-runs/{id}/cancel", h.CancelRun)
	r.Post("/api/v1/workflow-runs/{id}/retry", h.RetryRun)
}
func org(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, 401, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}

// writeWorkflowError answers a failed workflow request: a definition or run
// that is unknown or of another organization is 404 (TEN-04), a rule of a
// trigger or approval 400 with its fixed message. Database errors go through
// api.WriteRepoError; no table, constraint, policy or SQLSTATE text reaches
// the client (API-03, F-047), the log keeps it.
func writeWorkflowError(w http.ResponseWriter, r *http.Request, err error) {
	for _, rule := range []error{ErrTriggerMismatch, ErrNotWaiting, ErrInvalidDecision, ErrInvalidAction} {
		if errors.Is(err, rule) {
			api.WriteError(w, 400, "Bad Request", rule.Error())
			return
		}
	}
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, 404, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrRunNotFound):
		api.WriteError(w, 404, "Not Found", ErrRunNotFound.Error())
	case errors.Is(err, ErrCINotFound):
		api.WriteError(w, 404, "Not Found", ErrCINotFound.Error())
	case errors.Is(err, ErrVersionNotFound):
		api.WriteError(w, 404, "Not Found", ErrVersionNotFound.Error())
	case errors.Is(err, ErrRunNotCancellable):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrRunNotCancellable.Error())
	case errors.Is(err, ErrRunNotRetryable):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrRunNotRetryable.Error())
	case errors.Is(err, ErrPreconditionFailed):
		api.WritePreconditionFailed(w, ErrPreconditionFailed.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "workflow request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// validateWrite checks trigger, conditions and actions a create or a PATCH
// sends: filter and conditions after MET-45 (WP-1087), cron and time zone
// of a schedule (WP-1091), the send_webhook actions (WP-1096); each
// refusal is a 422 on its field.
func validateWrite(trigger JSONMap, conditions, actions []JSONMap) error {
	if err := ValidateSchedule(trigger); err != nil {
		return err
	}
	if err := ValidateDefinition(trigger, conditions); err != nil {
		return err
	}
	// send_webhook names a topic and no target of its own (E-231,
	// WP-1096).
	return validateActions(actions)
}

func (h *Handler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListDefinitions(r.Context(), t.OrganizationID, r.URL.Query().Get("active") == "true", page)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Definition]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	w.Header().Set("ETag", api.ETag(int64(item.Version)))
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
	if err := validateWrite(req.Trigger, req.Conditions, req.Actions); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	item := &Definition{OrganizationID: t.OrganizationID, Name: req.Name, Description: req.Description, Trigger: req.Trigger, Conditions: req.Conditions, Actions: req.Actions, Active: active}
	if err := h.repo.CreateDefinition(r.Context(), item); err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	// If-Match names the version the change is based on (API-07); the
	// repository checks it on the locked row.
	version, sent, parseErr := api.ParseIfMatch(r)
	if parseErr != nil {
		api.WritePreconditionFailed(w, parseErr.Error())
		return
	}
	var req UpdateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if err := validateWrite(req.Trigger, req.Conditions, req.Actions); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if sent {
		req.IfMatch = &version
	}
	req.CreatedBy = t.UserID
	item, err := h.repo.UpdateDefinition(r.Context(), t.OrganizationID, id, &req)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	w.Header().Set("ETag", api.ETag(int64(item.Version)))
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteDefinition(r.Context(), t.OrganizationID, id); err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

// ListVersions handles GET /api/v1/workflows/{id}/versions: the stored
// versions of a definition, newest first (WFL-02, API-06). A definition of
// another organization is 404 (TEN-04).
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.ListVersions(r.Context(), t.OrganizationID, id, page)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[DefinitionVersion]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total})
}

// GetVersion handles GET /api/v1/workflows/{id}/versions/{version}.
func (h *Handler) GetVersion(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	// The version column is an integer: a number outside 1..MaxInt32
	// names no version (404, not a database error).
	number, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 32)
	if err != nil || number < 1 {
		api.WriteError(w, 404, "Not Found", ErrVersionNotFound.Error())
		return
	}
	item, err := h.repo.GetVersion(r.Context(), t.OrganizationID, id, int(number))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, item)
}

func (h *Handler) TriggerRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	def, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	var req TriggerRequest
	if readErr := api.ReadJSON(r, &req); readErr != nil {
		api.WriteError(w, 400, "Bad Request", readErr.Error())
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
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, run)
}

// ListRuns handles GET /api/v1/workflow-runs (WP-1092 AK3): filters
// workflow_id, ci_id, status, trigger and from/to on created_at (RFC 3339),
// newest first with the keyset cursor on (created_at, id).
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	filter, err := parseRunFilter(r.URL.Query())
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.ListRuns(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.NewestFirstPage(items, total, page, runPosition))
}

// parseRunFilter reads the filters of the run list. The filters reach SQL
// only as UUIDs, a known status or a time (F-047); an unknown definition or
// CI, or one of another organization, lists nothing alike. workflow_id
// keeps its 400 of WP-021, the filters of WP-1092 answer 422.
func parseRunFilter(q url.Values) (RunFilter, error) {
	f := RunFilter{WorkflowID: q.Get("workflow_id"), CIID: q.Get("ci_id"), Status: q.Get("status"), Trigger: q.Get("trigger")}
	if f.WorkflowID != "" && !api.IsUUID(f.WorkflowID) {
		return f, &api.FieldError{Field: "workflow_id", Message: "must be a UUID"}
	}
	if f.CIID != "" && !api.IsUUID(f.CIID) {
		return f, &api.FieldError{Field: "ci_id", Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
	}
	if f.Status != "" && !slices.Contains(runStatuses, f.Status) {
		return f, &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(runStatuses, ", "), Status: http.StatusUnprocessableEntity}
	}
	if err := api.RejectNUL("trigger", f.Trigger); err != nil {
		return f, err
	}
	for _, bound := range []struct {
		name string
		into **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		raw := q.Get(bound.name)
		if raw == "" {
			continue
		}
		at, perr := time.Parse(time.RFC3339Nano, raw)
		if perr != nil {
			return f, &api.FieldError{Field: bound.name, Message: "must be an RFC 3339 time", Status: http.StatusUnprocessableEntity}
		}
		at = at.UTC()
		*bound.into = &at
	}
	return f, nil
}

func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrRunNotFound.Error())
	if !ok {
		return
	}
	run, err := h.repo.GetRun(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, run)
}

// CancelRun handles POST /api/v1/workflow-runs/{id}/cancel (WP-1092 AK1):
// a pending or waiting run ends as cancelled; a running or ended run is
// 409.
func (h *Handler) CancelRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrRunNotFound.Error())
	if !ok {
		return
	}
	run, err := h.exec.Cancel(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, run)
}

// RetryRun handles POST /api/v1/workflow-runs/{id}/retry (WP-1092 AK2): a
// failed run continues at its failed step; any other run is 409.
func (h *Handler) RetryRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrRunNotFound.Error())
	if !ok {
		return
	}
	run, err := h.exec.Retry(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, run)
}

func (h *Handler) ApproveRun(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrRunNotFound.Error())
	if !ok {
		return
	}
	var req ApprovalRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	run, err := h.exec.Approve(r.Context(), t.OrganizationID, id, req.Decision, req.Comment)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, run)
}
