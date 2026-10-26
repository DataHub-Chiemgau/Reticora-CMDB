package history

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for unified history and point-in-time state.
type Handler struct {
	repo  Repository
	jobs  SnapshotJobs
	queue SnapshotQueue
}

// SnapshotJobKind is the job kind of a state snapshot (JOB-04).
const SnapshotJobKind = "history.snapshot"

// SnapshotJobs runs state snapshots as jobs; jobs.Service implements it.
type SnapshotJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
}

// WithJobs makes POST /history/snapshots a job (JOB-04).
func (h *Handler) WithJobs(j SnapshotJobs) *Handler {
	h.jobs = j
	return h
}

// SnapshotQueue submits state snapshots to the job queue on NATS (TEC-14),
// whose workers run them with RunSnapshotJob; jobs.Queue implements it.
type SnapshotQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithQueue runs the snapshot jobs on the job queue instead of in this
// process: a job outlives the replica that accepted it.
func (h *Handler) WithQueue(q SnapshotQueue) *Handler {
	h.queue = q
	return h
}

// NewHandler creates a new history handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers history routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/history/{entityType}/{id}", h.List)
	r.Get("/api/v1/assets/{id}/state", h.AssetState)
	r.Get("/api/v1/cis/{id}/state", h.CIState)
	r.Post("/api/v1/history/snapshots", h.CreateSnapshot)
}

// SnapshotRequest asks for the state of an entity at a point in time.
type SnapshotRequest struct {
	EntityType string     `json:"entity_type"`
	EntityID   string     `json:"entity_id"`
	At         *time.Time `json:"at,omitempty"`
}

// CreateSnapshot handles POST /api/v1/history/snapshots: the state
// snapshot runs as a job (JOB-04, E-25) whose result is the replayed state;
// the answer is 202 with the job. The job runs on the job queue when there
// is one (TEC-14), otherwise in the background of this process. Without the
// job model the snapshot is answered at once (200).
func (h *Handler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req SnapshotRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.EntityType != "ci" && req.EntityType != "asset" {
		api.WriteFieldError(w, &api.FieldError{Field: "entity_type", Message: "must be ci or asset", Status: http.StatusUnprocessableEntity})
		return
	}
	if req.EntityID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "entity_id", Message: "is required"})
		return
	}
	at := time.Now().UTC()
	if req.At != nil {
		at = req.At.UTC()
	}
	orgID := t.OrganizationID
	params := SnapshotRequest{EntityType: req.EntityType, EntityID: req.EntityID, At: &at}
	run := func(ctx context.Context) (any, error) { return h.snapshot(ctx, orgID, &params) }
	if h.jobs == nil && h.queue == nil {
		snapshot, err := run(r.Context())
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, snapshot)
		return
	}
	var job *jobs.Job
	var err error
	if h.queue != nil {
		job, _, err = h.queue.Submit(r.Context(), orgID, SnapshotJobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	} else {
		job, err = h.jobs.RunDetached(r.Context(), orgID, SnapshotJobKind, params, t.UserID, run)
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// snapshot replays the state of the entity of req at req.At.
func (h *Handler) snapshot(ctx context.Context, orgID string, req *SnapshotRequest) (any, error) {
	trail, err := h.repo.TrailUpTo(ctx, orgID, req.EntityType, req.EntityID, *req.At)
	if err != nil {
		return nil, err
	}
	snapshot := Replay(trail)
	snapshot.EntityType, snapshot.EntityID, snapshot.At = req.EntityType, req.EntityID, *req.At
	return snapshot, nil
}

// RunSnapshotJob runs a snapshot job of the job queue (handler of
// history.snapshot) with the tenant and scope of its creator
// (jobs.CreatorContext); it only reads, so a repeated attempt is harmless.
func (h *Handler) RunSnapshotJob(ctx context.Context, job *jobs.Job) (any, error) {
	var req SnapshotRequest
	if err := json.Unmarshal(job.Params, &req); err != nil {
		return nil, fmt.Errorf("decode snapshot job parameters: %w", err)
	}
	if (req.EntityType != "ci" && req.EntityType != "asset") || req.EntityID == "" || req.At == nil {
		return nil, fmt.Errorf("invalid snapshot job parameters: %s/%s", req.EntityType, req.EntityID)
	}
	return h.snapshot(jobs.CreatorContext(ctx, job), job.OrganizationID, &req)
}

// ListResponse is a page of a history, api.ListResponse plus
// AuditIncluded: whether the audit entries of the entity are part of it
// (E-78). It sets next_cursor itself, since WriteJSON fills it only for an
// api.ListResponse.
type ListResponse struct {
	Data          []Change `json:"data"`
	Total         int      `json:"total"`
	Limit         int      `json:"limit"`
	Offset        int      `json:"offset"`
	HasMore       bool     `json:"has_more"`
	NextCursor    string   `json:"next_cursor,omitempty"`
	AuditIncluded bool     `json:"audit_included"`
}

// List handles GET /api/v1/history/{entityType}/{id}. The route needs
// ci:read; the history of an entity type needs its read permission, and the
// audit entries of a CI are part of it only with audit:read (CI-06, E-78).
// The trail is read under the caller's tenant scope. A CI change is one
// entry per change set with its field_changes; change_kind keeps the CI
// changes of one kind, an unknown kind or another entity type is 400
// (AUD-03).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	entityType := chi.URLParam(r, "entityType")
	required, ok := readPermissions[entityType]
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "unsupported entity type")
		return
	}
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !principal.Has(required) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(required))
		return
	}
	opts := ListOptions{IncludeAudit: entityType == "ci" && principal.Has(identity.PermAuditRead)}
	if q := r.URL.Query(); q.Has("change_kind") {
		opts.ChangeKind = q.Get("change_kind")
		if !ChangeKinds[opts.ChangeKind] {
			api.WriteFieldError(w, &api.FieldError{Field: "change_kind",
				Message: "must be one of created, updated, deleted, restored, merged, type_changed, relationship_added, relationship_removed, status_changed"})
			return
		}
		if entityType != "ci" {
			api.WriteFieldError(w, &api.FieldError{Field: "change_kind", Message: "filters the history of a ci only"})
			return
		}
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, entityType, chi.URLParam(r, "id"), page, opts)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	resp := ListResponse{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total, AuditIncluded: opts.IncludeAudit,
	}
	if resp.HasMore && len(items) > 0 {
		resp.NextCursor = api.EncodeOffsetCursor(page.Offset + len(items))
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// AssetState handles GET /api/v1/assets/{id}/state?at=...
func (h *Handler) AssetState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "asset")
}

// CIState handles GET /api/v1/cis/{id}/state?at=...
func (h *Handler) CIState(w http.ResponseWriter, r *http.Request) {
	h.state(w, r, "ci")
}

// state reconstructs the entity state at the requested point in time
// (spec §16). Without an "at" parameter the current replay of the full trail
// is returned.
func (h *Handler) state(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	at := time.Now().UTC()
	if raw := r.URL.Query().Get("at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid at timestamp (RFC3339 expected)")
			return
		}
		at = parsed
	}
	entityID := chi.URLParam(r, "id")
	trail, err := h.repo.TrailUpTo(r.Context(), t.OrganizationID, entityType, entityID, at)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	snapshot := Replay(trail)
	snapshot.EntityType = entityType
	snapshot.EntityID = entityID
	snapshot.At = at
	api.WriteJSON(w, http.StatusOK, snapshot)
}

// readPermissions lists the entity types with a history and the read
// permission each one's history needs (E-78); the locations are the common
// tree of type location (E-19), the retired location_node has none (F-226).
var readPermissions = map[string]identity.Permission{
	"ci":           identity.PermCIRead,
	"relationship": identity.PermRelationshipRead,
	"location":     identity.PermLocationRead,
	"asset":        identity.PermAssetRead,
	"reservation":  identity.PermAssetRead,
	"composition":  identity.PermAssetRead,
}
