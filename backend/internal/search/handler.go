package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	backend     Backend
	permissions permission.Repository
	jobs        ReindexJobs
	queue       ReindexQueue
	features    FeatureChecker
	modules     func(key string) bool
}

// FeatureChecker reports whether a feature is enabled for an organization;
// *entitlement.Service implements it.
type FeatureChecker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// WithGates sets the entitlement and the module check of the hits (SRC-02,
// WP-788): a hit of asset, reservation, document or ticket needs its
// feature and its module (HitAllowed), the holder fields assets_basic.
// Without them these hits are dropped (fail-closed).
func (h *Handler) WithGates(features FeatureChecker, modules func(key string) bool) *Handler {
	h.features, h.modules = features, modules
	return h
}

// featureCheck returns the feature check of the organization for one
// request, each feature asked once; nil without entitlements.
func (h *Handler) featureCheck(ctx context.Context, orgID string) func(key string) bool {
	if h.features == nil {
		return nil
	}
	seen := map[string]bool{}
	return func(key string) bool {
		on, ok := seen[key]
		if !ok {
			on = h.features.IsEnabled(ctx, orgID, key)
			seen[key] = on
		}
		return on
	}
}

// moduleCheck returns the module check of the hits: never without one
// (fail-closed).
func (h *Handler) moduleCheck() func(key string) bool {
	if h.modules == nil {
		return func(string) bool { return false }
	}
	return h.modules
}

func NewHandler(backend Backend, permissions permission.Repository) *Handler {
	return &Handler{backend: backend, permissions: permissions}
}

// ReindexJobs runs the rebuild of the index as a job in this process;
// jobs.Service implements it.
type ReindexJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
}

// ReindexQueue submits the rebuild of the index to the job queue on NATS
// (TEC-14), whose workers run it with RunReindexJob; jobs.Queue implements
// it.
type ReindexQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithJobs makes POST /api/v1/search/reindex a job (WP-786).
func (h *Handler) WithJobs(j ReindexJobs) *Handler {
	h.jobs = j
	return h
}

// WithQueue runs the rebuild on the job queue instead of in this process:
// the job outlives the replica that accepted it.
func (h *Handler) WithQueue(q ReindexQueue) *Handler {
	h.queue = q
	return h
}

// ReindexResponse is the answer of POST /api/v1/search/reindex: the job of
// the rebuild (WP-786). Indexed is deprecated (API-01): it is 0 for a job,
// whose result carries the count; only without the job model, when the
// rebuild runs in the request, it is the count.
type ReindexResponse struct {
	Indexed int         `json:"indexed"`
	JobID   string      `json:"job_id,omitempty"`
	Status  jobs.Status `json:"status,omitempty"`
	Kind    string      `json:"kind,omitempty"`
}

// reindexJobParams are the parameters of a job search.reindex: none. The
// rebuild runs organization-wide (E-08); the job keeps no permissions or
// groups of its creator.
type reindexJobParams struct{}

// errReindexFailed is the error a failed rebuild records on its job; the
// cause goes to the log (API-03).
var errReindexFailed = errors.New("the search index could not be rebuilt; the documents written before stay")

// runReindex rebuilds the index of the organization for a job.
func (h *Handler) runReindex(ctx context.Context, orgID string) (any, error) {
	res, err := h.backend.ReindexTenant(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "search reindex failed", "organization_id", orgID, "indexed", res.Indexed, "error", err)
		return nil, errReindexFailed
	}
	return res, nil
}

// RunReindexJob runs a job search.reindex of the job queue with the tenant
// and the scope of its creator (jobs.CreatorContext); the rebuild itself
// runs organization-wide (E-08).
func (h *Handler) RunReindexJob(ctx context.Context, job *jobs.Job) (any, error) {
	return h.runReindex(jobs.CreatorContext(ctx, job), job.OrganizationID)
}
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/search", h.Search)
	r.Post("/api/v1/search/reindex", h.Reindex)
}
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	q := Query{OrganizationID: t.OrganizationID, UserID: t.UserID, Text: r.URL.Query().Get("q"), EntityTypes: splitTypes(r.URL.Query().Get("type")), Limit: page.Limit, Offset: page.Offset, Highlight: true}
	// The text and the types are checked before any backend sees them: a
	// NUL is no text PostgreSQL stores (SQLSTATE 22021, F-047).
	if err := validateRequest(&q); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	// The hits match and show the fields the caller may read (RBA-07,
	// WP-266), the addresses and MACs of the interfaces with ipam:read
	// (WP-308), the users with user:read, their holders with assets_basic
	// (WP-788); a policy that cannot be resolved answers 500 (fail-closed).
	enabled := h.featureCheck(r.Context(), t.OrganizationID)
	if err := ApplyPrincipal(r.Context(), &q, func(perm string) bool { return h.allowed(r.Context(), t, perm) }, enabled); err != nil {
		slog.ErrorContext(r.Context(), "search: field permissions could not be resolved", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the field permissions could not be resolved")
		return
	}
	res, err := h.backend.Query(r.Context(), &q)
	if err != nil {
		// A checked query leaves backend failures only; their text goes to
		// the log, the client gets the sanitized answer (API-03).
		slog.ErrorContext(r.Context(), "search failed", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	// The page is filtered by the caller's permissions after the query, so
	// the next page continues at the backend offset, not after the hits
	// left over (API-02).
	more := res.HasMore
	res.Data = h.filterAllowed(r.Context(), t, res.Data, enabled)
	res.Total = len(res.Data)
	res.HasMore = more
	if more {
		res.NextCursor = api.EncodeOffsetCursor(page.Offset + page.Limit)
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// Reindex handles POST /api/v1/search/reindex. The route is gated by the
// search:write permission in the router's authorization layer; the handler
// only needs the tenant context. The rebuild is the job search.reindex
// (WP-786): on the job queue when there is one, otherwise in the
// background of this process, each with the scope of the caller; the
// answer is 202 with the job and its Location. Without the job model
// (profile without a database) the rebuild runs in the request.
func (h *Handler) Reindex(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	orgID := t.OrganizationID
	if h.jobs == nil && h.queue == nil {
		out, err := h.backend.ReindexTenant(r.Context(), orgID)
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusAccepted, ReindexResponse{Indexed: out.Indexed})
		return
	}
	var job *jobs.Job
	var err error
	if h.queue != nil {
		job, _, err = h.queue.Submit(r.Context(), orgID, ReindexJobKind, reindexJobParams{}, jobs.EnqueueOptions{CreatedBy: t.UserID})
	} else {
		job, err = h.jobs.RunDetached(r.Context(), orgID, ReindexJobKind, reindexJobParams{}, t.UserID,
			func(ctx context.Context) (any, error) { return h.runReindex(ctx, orgID) })
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "search reindex: submit the job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, ReindexResponse{JobID: job.ID, Status: job.Status, Kind: job.Kind})
}

// filterAllowed keeps the hits whose entity type the caller may read
// (SRC-01) and whose feature and module are enabled (HitAllowed, WP-788).
// It fails closed: without a permission repository or a user every hit is
// dropped, as are hits of types without a read permission. The result is
// never nil: an empty page answers "data": [] like every list, also when
// the backend found nothing (API-02, F-401).
func (h *Handler) filterAllowed(ctx context.Context, t tenant.TenantInfo, hits []Hit, enabled func(key string) bool) []Hit {
	out := make([]Hit, 0, len(hits))
	if h.permissions == nil || t.UserID == "" {
		return out
	}
	allowed := map[string]bool{}
	for i := range hits {
		hit := &hits[i]
		key, ok := ReadPermission[hit.EntityType]
		if !ok || !HitAllowed(hit.EntityType, enabled, h.moduleCheck()) {
			continue
		}
		granted, seen := allowed[key]
		if !seen {
			ok, err := h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, key)
			granted = err == nil && ok
			allowed[key] = granted
		}
		if granted {
			out = append(out, *hit)
		}
	}
	return out
}

// allowed reports whether the caller holds the permission; without a
// permission repository or a user never (fail-closed).
func (h *Handler) allowed(ctx context.Context, t tenant.TenantInfo, perm string) bool {
	if h.permissions == nil || t.UserID == "" {
		return false
	}
	ok, err := h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, perm)
	return err == nil && ok
}

// validateRequest checks the search text and the type filter: a 400 field
// error for a NUL character or a text beyond MaxQueryLength.
func validateRequest(q *Query) error {
	if err := api.RejectNUL("q", q.Text); err != nil {
		return err
	}
	if err := validateQuery(q.Text); err != nil {
		return &api.FieldError{Field: "q", Message: fmt.Sprintf("must not be longer than %d characters", MaxQueryLength)}
	}
	return api.RejectNUL("type", q.EntityTypes...)
}

func splitTypes(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
