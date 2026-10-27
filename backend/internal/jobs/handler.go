package jobs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Generic job API (API-09, JOB-03): GET /jobs, GET /jobs/{id} with the
// result, DELETE /jobs/{id} (cancel) and POST /jobs/{id}/revert where the
// kind defines a revert (E-25). Reading needs job:read and covers the jobs
// whose scope snapshot lies in the caller's scope (TEN-09); every route
// answers 404 for any other job. Cancelling and reverting a job needs the
// permission of its kind, or being its creator.

// Reverter undoes a finished job of one kind. A refusal with a domain text
// (409) is a *RevertRefusedError; any other error is a repository error and
// reaches the client sanitized (API-03).
type Reverter func(ctx context.Context, job *Job) error

// RevertRefusedError is a reverter's refusal, e.g. an expired merge
// snapshot (409).
type RevertRefusedError struct{ Reason error }

// validation: the reverter passes a fixed domain sentinel (server/router.go).
func (e *RevertRefusedError) Error() string { return e.Reason.Error() }

func (e *RevertRefusedError) Unwrap() error { return e.Reason }

// ErrNotRevertible: the kind defines no revert (E-25) or the job did not
// succeed.
var ErrNotRevertible = errors.New("the job cannot be reverted")

// APIHandler serves the job API.
type APIHandler struct {
	svc *Service
	// kindPermissions names the permission that controls the jobs of a
	// kind; a kind without one is controlled by its creator only.
	kindPermissions map[string]identity.Permission
	reverters       map[string]Reverter
}

// NewAPIHandler creates the job API on svc.
func NewAPIHandler(svc *Service) *APIHandler {
	return &APIHandler{svc: svc, kindPermissions: map[string]identity.Permission{}, reverters: map[string]Reverter{}}
}

// WithKind sets the permission that controls the jobs of kind and its
// revert (nil: none).
func (h *APIHandler) WithKind(kind string, perm identity.Permission, revert Reverter) *APIHandler {
	h.kindPermissions[kind] = perm
	if revert != nil {
		h.reverters[kind] = revert
	}
	return h
}

// RegisterRoutes registers the job routes.
func (h *APIHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/jobs", h.List)
	r.Get("/api/v1/jobs/{id}", h.Get)
	r.Get("/api/v1/jobs/{id}/result", h.Result)
	r.Delete("/api/v1/jobs/{id}", h.Cancel)
	r.Post("/api/v1/jobs/{id}/revert", h.Revert)
}

func orgOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

// List handles GET /api/v1/jobs?kind=&status=.
func (h *APIHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	filter := Filter{Kind: r.URL.Query().Get("kind"), Status: Status(r.URL.Query().Get("status"))}
	if filter.Status != "" && !validStatus(filter.Status) {
		api.WriteFieldError(w, &api.FieldError{Field: "status", Message: "unknown job status", Status: http.StatusUnprocessableEntity})
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.svc.List(r.Context(), orgID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	for i := range items {
		items[i].Cancellable = h.svc.Cancellable(&items[i])
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Job]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}

func validStatus(s Status) bool {
	for _, known := range []Status{StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled} {
		if s == known {
			return true
		}
	}
	return false
}

// visibleTo reports whether the caller sees the job: its tenant scope
// covers the scope snapshot of the job (Job.VisibleTo, TEN-09, F-163). The
// PostgreSQL repository reads under the same rule; a request without tenant
// scope sees no job.
func visibleTo(r *http.Request, job *Job) bool {
	scope, ok := database.TenantScopeFromContext(r.Context())
	return ok && job.VisibleTo(&scope)
}

func notFound(w http.ResponseWriter) {
	api.WriteError(w, http.StatusNotFound, "Not Found", "job not found")
}

// job loads the job of the path or writes 404, also for a job outside the
// caller's scope.
func (h *APIHandler) job(w http.ResponseWriter, r *http.Request, orgID string) (*Job, bool) {
	job, err := h.svc.Get(r.Context(), orgID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrNotFound) || (err == nil && !visibleTo(r, job)):
		notFound(w)
		return nil, false
	case err != nil:
		api.WriteRepoError(w, err)
		return nil, false
	}
	return job, true
}

// Get handles GET /api/v1/jobs/{id}.
func (h *APIHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	if job, found := h.job(w, r, orgID); found {
		job.Cancellable = h.svc.Cancellable(job)
		api.WriteJSON(w, http.StatusOK, job)
	}
}

// Result handles GET /api/v1/jobs/{id}/result: the result of a succeeded
// job, or the partial result a job cancelled while running kept (the
// report of a bulk change, BLK-01); 409 while it has none.
func (h *APIHandler) Result(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	if (job.Status != StatusSucceeded && job.Status != StatusCancelled) || len(job.Result) == 0 {
		api.WriteError(w, http.StatusConflict, "Conflict", "the job has no result (status "+string(job.Status)+")")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(job.Result)
}

// mayControl decides whether the caller may cancel or revert the job: a job
// outside the caller's scope is not found (404, before 403 and 409,
// F-163); otherwise its creator and a principal with the permission of its
// kind may (0), anyone else is refused (403).
func (h *APIHandler) mayControl(r *http.Request, job *Job) int {
	if !visibleTo(r, job) {
		return http.StatusNotFound
	}
	if user := tenant.FromContext(r.Context()).UserID; user != "" && user == job.CreatedBy {
		return 0
	}
	if perm, ok := h.kindPermissions[job.Kind]; ok && perm != "" {
		if p, ok := identity.PrincipalFromContext(r.Context()); ok && p.Has(perm) {
			return 0
		}
	}
	return http.StatusForbidden
}

// Cancel handles DELETE /api/v1/jobs/{id}: a queued job, or a running job
// of a kind that can be cancelled while running (Kind.Cancellable), is
// cancelled; a running job of another kind and a finished job are 409
// (JOB-03, F-162).
func (h *APIHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	switch h.mayControl(r, job) {
	case http.StatusNotFound:
		notFound(w)
		return
	case http.StatusForbidden:
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only the creator or a holder of the permission of the job kind may cancel it")
		return
	}
	cancelled, err := h.svc.Cancel(r.Context(), orgID, job.ID)
	switch {
	case errors.Is(err, ErrNotCancellable):
		api.WriteError(w, http.StatusConflict, "Conflict", "the job kind "+job.Kind+" cannot be cancelled while running")
	case errors.Is(err, ErrTransition):
		api.WriteError(w, http.StatusConflict, "Conflict", "the job is "+string(job.Status)+" and cannot be cancelled")
	case errors.Is(err, ErrNotFound):
		notFound(w)
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, cancelled)
	}
}

// revertResponse is the answer of a revert: the job and the revert it
// records.
type revertResponse struct {
	JobID      string     `json:"job_id"`
	Reverted   bool       `json:"reverted"`
	RevertedAt *time.Time `json:"reverted_at,omitempty"`
	RevertedBy string     `json:"reverted_by,omitempty"`
}

// Revert handles POST /api/v1/jobs/{id}/revert: only for kinds with a
// defined revert (E-25) and succeeded jobs; otherwise 409. A successful
// revert is recorded on the job (reverted_at, reverted_by); a job reverted
// already is 409 (F-329).
func (h *APIHandler) Revert(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	control := h.mayControl(r, job)
	if control == http.StatusNotFound {
		notFound(w)
		return
	}
	revert, defined := h.reverters[job.Kind]
	if !defined || job.Status != StatusSucceeded {
		api.WriteError(w, http.StatusConflict, "Conflict", ErrNotRevertible.Error()+": kind "+job.Kind+", status "+string(job.Status))
		return
	}
	if job.RevertedAt != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", ErrAlreadyReverted.Error())
		return
	}
	if control == http.StatusForbidden {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only the creator or a holder of the permission of the job kind may revert it")
		return
	}
	if err := revert(r.Context(), job); err != nil {
		var refused *RevertRefusedError
		switch {
		case api.WriteFieldError(w, err):
		case errors.As(err, &refused):
			api.WriteError(w, http.StatusConflict, "Conflict", refused.Error())
		default:
			slog.WarnContext(r.Context(), "job revert failed", "job_id", job.ID, "error", err)
			api.WriteRepoError(w, err)
		}
		return
	}
	reverted, err := h.svc.MarkReverted(r.Context(), orgID, job.ID, tenant.FromContext(r.Context()).UserID)
	switch {
	case errors.Is(err, ErrAlreadyReverted):
		// A concurrent revert recorded it first.
		api.WriteError(w, http.StatusConflict, "Conflict", ErrAlreadyReverted.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "job reverted, recording the revert failed", "job_id", job.ID, "error", err)
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, revertResponse{JobID: job.ID, Reverted: true,
			RevertedAt: reverted.RevertedAt, RevertedBy: reverted.RevertedBy})
	}
}
