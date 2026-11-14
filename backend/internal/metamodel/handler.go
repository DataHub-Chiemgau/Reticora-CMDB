package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Handler serves the metamodel drafts (API-06, MET-53). Every route needs
// citype:manage (server/authz.go); an item needs the write permission of
// its resource route as well (ItemPermission); the test run (WP-721)
// needs metamodel:publish.
type Handler struct {
	repo  Repository
	jobs  TestJobs
	queue TestQueue
	// apply runs the resource routes of the items of a publication
	// (WP-261); dispatcher sends their webhook events after the commit.
	apply      Applier
	dispatcher citype.EventDispatcher
	// fieldPolicy drops the cached field policy after a publication
	// (SR-58); nil: none.
	fieldPolicy citype.FieldPolicyInvalidator
}

// TestJobs runs the test jobs in this process and reads a job;
// jobs.Service implements it.
type TestJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
	Get(ctx context.Context, orgID, id string) (*jobs.Job, error)
}

// TestQueue submits the test jobs to the job queue on NATS (TEC-14),
// whose workers run them with RunTestJob; jobs.Queue implements it.
type TestQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithJobs sets the job service of the test run (WP-721): without it POST
// …/test answers 503.
func (h *Handler) WithJobs(j TestJobs) *Handler {
	h.jobs = j
	return h
}

// WithQueue runs the test jobs on the job queue instead of in this process:
// a job outlives the replica that accepted it.
func (h *Handler) WithQueue(q TestQueue) *Handler {
	h.queue = q
	return h
}

// WithPublish sets what the publication (WP-261) needs: catalog, the
// metamodel repository whose resource routes apply the items (the instance
// with the index scheduler, so the index jobs of the elements join the
// publication), and dispatcher, which sends the webhook events of the
// applied elements after the commit as the direct path does (ci_type.*,
// ci_attribute.changed). Without catalog POST …/publish answers 503.
func (h *Handler) WithPublish(catalog citype.Repository, dispatcher citype.EventDispatcher) *Handler {
	if catalog != nil {
		h.apply = newRouteApplier(catalog)
	}
	h.dispatcher = dispatcher
	return h
}

// WithFieldPolicyInvalidator drops the cached field policy of the
// organization after a publication (SR-58, WP-871).
func (h *Handler) WithFieldPolicyInvalidator(inv citype.FieldPolicyInvalidator) *Handler {
	h.fieldPolicy = inv
	return h
}

// NewHandler returns the draft handler; without a repository (no
// database) every route answers 503.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers the draft routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/metamodel/drafts", h.ListDrafts)
	r.Post("/api/v1/metamodel/drafts", h.CreateDraft)
	r.Get("/api/v1/metamodel/drafts/{id}", h.GetDraft)
	r.Patch("/api/v1/metamodel/drafts/{id}", h.UpdateDraft)
	r.Delete("/api/v1/metamodel/drafts/{id}", h.DiscardDraft)
	r.Get("/api/v1/metamodel/drafts/{id}/items", h.ListItems)
	r.Post("/api/v1/metamodel/drafts/{id}/items", h.AddItem)
	r.Delete("/api/v1/metamodel/drafts/{id}/items/{item_id}", h.RemoveItem)
	r.Post("/api/v1/metamodel/drafts/{id}/test", h.StartTest)
	r.Get("/api/v1/metamodel/drafts/{id}/test", h.GetTest)
	r.Post("/api/v1/metamodel/drafts/{id}/publish", h.Publish)
}

// store returns the repository and the tenant, or answers 503 (no
// database) or 401.
func (h *Handler) store(w http.ResponseWriter, r *http.Request) (Repository, tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, t, false
	}
	if h.repo == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "metamodel drafts need the database")
		return nil, t, false
	}
	return h.repo, t, true
}

// writeDraftError reports a failed draft request (RFC 7807).
func writeDraftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDraftNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrDraftNotFound.Error())
	case errors.Is(err, ErrNoTestReport):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNoTestReport.Error())
	case errors.Is(err, ErrItemNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
	case errors.Is(err, ErrTargetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTargetNotFound.Error())
	case errors.Is(err, ErrTargetReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrTargetReadOnly.Error())
	case errors.Is(err, ErrDraftImmutable):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrDraftImmutable.Error())
	case errors.Is(err, ErrItemReferenced):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrItemReferenced.Error())
	case errors.Is(err, ErrVersionMismatch):
		api.WritePreconditionFailed(w, ErrVersionMismatch.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// ifMatch reads an optional If-Match (API-07); a value that is not a
// version is answered with 412.
func ifMatch(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &version, true
}

// writeDraft answers a draft with its version as ETag.
func writeDraft(w http.ResponseWriter, status int, d *Draft) {
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, status, d)
}

// ListDrafts handles GET /api/v1/metamodel/drafts (?status=): newest first
// with limit and the keyset cursor of API-02.
func (h *Handler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", StatusDraft, StatusTested, StatusPublished, StatusDiscarded:
	default:
		api.WriteRepoError(w, errStatusFilter())
		return
	}
	rows, total, err := repo.ListDrafts(r.Context(), t.OrganizationID, status, page)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(rows, total, page, func(d *Draft) (time.Time, string) {
		return d.CreatedAt, d.ID
	}))
}

// CreateDraft handles POST /api/v1/metamodel/drafts.
func (h *Handler) CreateDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	var in DraftInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d, err := repo.CreateDraft(r.Context(), t.OrganizationID, &in)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusCreated, d)
}

// GetDraft handles GET /api/v1/metamodel/drafts/{id}.
func (h *Handler) GetDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	d, err := repo.GetDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// UpdateDraft handles PATCH /api/v1/metamodel/drafts/{id}: the name.
func (h *Handler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var patch DraftPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	d, err := repo.UpdateDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch, match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// DiscardDraft handles DELETE /api/v1/metamodel/drafts/{id}: the draft
// stays as discarded.
func (h *Handler) DiscardDraft(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	d, err := repo.DiscardDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// ListItems handles GET /api/v1/metamodel/drafts/{id}/items: newest first
// with limit and the keyset cursor of API-02.
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	d, err := repo.GetDraft(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	key := func(it *Item) (time.Time, string) { return it.CreatedAt, it.ID }
	rows, total, err := api.NewestFirstSlice(d.Items, page, key)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(rows, total, page, key))
}

// AddItem handles POST /api/v1/metamodel/drafts/{id}/items: the item is
// checked (kind, op, strict payload, target), the caller needs the write
// permission of its resource route (403), and it is captured with the
// base of its target.
func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in ItemInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	// The payload is checked like the resource route without the stored
	// metamodel (WP-720, citype/draft_payload.go).
	c, err := checkItemPayload(&in)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	perms, _ := identity.PermissionsFromContext(r.Context())
	for _, need := range ItemPermissions(c.Kind, c.Op, c.Payload) {
		if !identity.HasPermission(perms, need) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "the item needs the permission "+string(need))
			return
		}
	}
	d, it, err := repo.AddItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), c, match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, http.StatusCreated, it)
}

// RemoveItem handles DELETE /api/v1/metamodel/drafts/{id}/items/{item_id}.
func (h *Handler) RemoveItem(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	d, err := repo.RemoveItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), match)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeDraft(w, http.StatusOK, d)
}

// StartTest handles POST /api/v1/metamodel/drafts/{id}/test (WP-721): the
// test run of the draft against the inventory as a job of kind
// TestJobKind, on the job queue when there is one, otherwise in this
// process. The answer is 202 with the job. While a test of the draft is
// queued or running, a second request answers the same job. A published
// or discarded draft is 409; without the database or the job service 503.
func (h *Handler) StartTest(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	if h.jobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the test run needs the job service")
		return
	}
	orgID, id := t.OrganizationID, chi.URLParam(r, "id")
	job, err := repo.StartTest(r.Context(), orgID, id, func(ctx context.Context, running string, version int64) (*jobs.Job, bool, error) {
		if running != "" {
			if current, gerr := h.jobs.Get(ctx, orgID, running); gerr == nil && current != nil &&
				(current.Status == jobs.StatusQueued || current.Status == jobs.StatusRunning) {
				return current, true, nil
			}
		}
		params := TestJobParams{DraftID: id, Version: version}
		if h.queue != nil {
			submitted, _, serr := h.queue.Submit(ctx, orgID, TestJobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
			return submitted, false, serr
		}
		// The run needs the id of its job, which RunDetached returns after
		// it started the run.
		started := make(chan string, 1)
		detached, rerr := h.jobs.RunDetached(ctx, orgID, TestJobKind, params, t.UserID, func(runCtx context.Context) (any, error) {
			return repo.RunTest(testContext(runCtx, orgID), orgID, id, <-started, version)
		})
		if rerr != nil {
			return nil, false, rerr
		}
		started <- detached.ID
		return detached, false, nil
	})
	if err != nil {
		writeDraftError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// RunTestJob runs a test job of the job queue (handler of TestJobKind,
// server/jobkinds.go): organization-wide, not in the scope of its creator.
func (h *Handler) RunTestJob(ctx context.Context, job *jobs.Job) (any, error) {
	if h.repo == nil {
		return nil, errors.New("metamodel drafts need the database")
	}
	var params TestJobParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("decode test job parameters: %w", err)
	}
	if !api.IsUUID(params.DraftID) {
		return nil, fmt.Errorf("invalid test job parameters: draft %q", params.DraftID)
	}
	return h.repo.RunTest(testContext(ctx, job.OrganizationID), job.OrganizationID, params.DraftID, job.ID, params.Version)
}

// TestReportPage is the answer of GET …/test: the report without its
// violations and a page of them (API-02).
type TestReportPage struct {
	TestReport
	Data       []TestViolation `json:"data"`
	Total      int             `json:"total"`
	Limit      int             `json:"limit"`
	Offset     int             `json:"offset"`
	HasMore    bool            `json:"has_more"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// GetTest handles GET /api/v1/metamodel/drafts/{id}/test (WP-721): the
// counters per type and attribute and the violations of the last test run,
// paged with limit and the cursor next_cursor; total is the number of
// violations the run counted (the stored violations are bounded,
// truncated). It needs metamodel:publish (server/authz.go): the report
// names CIs of every client. A draft without a report is 404.
func (h *Handler) GetTest(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil || page.Cursor != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
		return
	}
	report, err := repo.TestReport(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	start := min(page.Offset, len(report.Violations))
	end := min(start+page.Limit, len(report.Violations))
	out := TestReportPage{TestReport: *report.Summary(), Data: api.NonNilSlice(report.Violations[start:end]),
		Total: report.Totals.Violations, Limit: page.Limit, Offset: page.Offset, HasMore: end < len(report.Violations)}
	if out.HasMore {
		out.NextCursor = api.EncodeOffsetCursor(end)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Publish handles POST /api/v1/metamodel/drafts/{id}/publish (WP-261,
// MET-53): the tested draft is applied in one transaction (publish.go) and
// answered with the published draft, the counters per kind and the
// affected types with their new schema_version. A draft that is not
// tested, or published or discarded, is 409, as is one whose items rest on
// a changed target (stale_items); a refused item answers with the status
// and problem of its resource route (422, 409, 403, …) and item_id; the
// webhook events of the applied elements follow the commit. Without the
// database 503.
func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := h.store(w, r)
	if !ok {
		return
	}
	publisher, isPublisher := repo.(Publisher)
	if !isPublisher || h.apply == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the publication needs the database")
		return
	}
	buffer := &webhookBuffer{}
	res, err := publisher.Publish(withWebhookBuffer(r.Context(), buffer), t.OrganizationID, chi.URLParam(r, "id"), h.apply)
	if err != nil {
		writePublishError(w, err)
		return
	}
	buffer.flush(r.Context(), t.OrganizationID, h.dispatcher)
	// The published attribute definitions may carry computed asset
	// sources (SR-58): drop the cached field policy after the commit.
	if h.fieldPolicy != nil {
		h.fieldPolicy.Invalidate(r.Context(), t.OrganizationID)
	}
	w.Header().Set("ETag", api.ETag(res.Draft.Version))
	api.WriteJSON(w, http.StatusOK, res)
}

// writePublishError reports a refused publication (RFC 7807).
func writePublishError(w http.ResponseWriter, err error) {
	var stale *StaleError
	var refused *ItemError
	switch {
	case errors.Is(err, ErrNotTested):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrNotTested.Error())
	case errors.As(err, &stale):
		api.WriteProblemBody(w, http.StatusConflict, map[string]any{
			"type": "urn:reticora:problem:conflict", "title": "Conflict", "status": http.StatusConflict,
			"detail":      "the targets of items of the draft changed since they were captured; nothing was published",
			"stale_items": stale.Items,
		})
	case errors.As(err, &refused):
		problem := map[string]any{}
		if json.Unmarshal(refused.Body, &problem) != nil {
			problem = map[string]any{"type": "https://reticora.io/problems/" + strconv.Itoa(refused.Status),
				"title": http.StatusText(refused.Status), "status": refused.Status}
		}
		problem["item_id"], problem["item_kind"], problem["item_op"] = refused.ItemID, refused.Kind, refused.Op
		api.WriteProblemBody(w, refused.Status, problem)
	default:
		writeDraftError(w, err)
	}
}
