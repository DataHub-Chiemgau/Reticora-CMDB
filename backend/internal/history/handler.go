package history

import (
	"context"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for unified history and point-in-time state.
type Handler struct {
	repo Repository
	jobs SnapshotJobs
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
// the answer is 202 with the job. Without the job model the snapshot is
// answered at once (200).
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
	run := func(ctx context.Context) (any, error) {
		trail, err := h.repo.TrailUpTo(ctx, orgID, req.EntityType, req.EntityID, at)
		if err != nil {
			return nil, err
		}
		snapshot := Replay(trail)
		snapshot.EntityType, snapshot.EntityID, snapshot.At = req.EntityType, req.EntityID, at
		return snapshot, nil
	}
	if h.jobs == nil {
		snapshot, err := run(r.Context())
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, snapshot)
		return
	}
	params := SnapshotRequest{EntityType: req.EntityType, EntityID: req.EntityID, At: &at}
	job, err := h.jobs.RunDetached(r.Context(), orgID, SnapshotJobKind, params, t.UserID, run)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// List handles GET /api/v1/history/{entityType}/{id}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	entityType := chi.URLParam(r, "entityType")
	if !validEntityType(entityType) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "unsupported entity type")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, entityType, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
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

func validEntityType(entityType string) bool {
	switch entityType {
	case "ci", "asset", "relationship", "location_node", "reservation", "composition":
		return true
	}
	return false
}
