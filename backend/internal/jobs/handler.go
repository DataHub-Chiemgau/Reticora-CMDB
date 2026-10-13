package jobs

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Generic job API (API-09, JOB-03): GET /jobs, GET /jobs/{id} with the
// result, DELETE /jobs/{id} (cancel) and POST /jobs/{id}/revert where the
// kind defines a revert (E-25). Reading needs job:read; cancelling and
// reverting a job needs the permission of its kind, or being its creator.

// Reverter undoes a finished job of one kind.
type Reverter func(ctx context.Context, job *Job) error

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

// job loads the job of the path or writes 404.
func (h *APIHandler) job(w http.ResponseWriter, r *http.Request, orgID string) (*Job, bool) {
	job, err := h.svc.Get(r.Context(), orgID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "job not found")
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
		api.WriteJSON(w, http.StatusOK, job)
	}
}

// Result handles GET /api/v1/jobs/{id}/result: the result of a succeeded
// job; 409 while it has none.
func (h *APIHandler) Result(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	if job.Status != StatusSucceeded || len(job.Result) == 0 {
		api.WriteError(w, http.StatusConflict, "Conflict", "the job has no result (status "+string(job.Status)+")")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(job.Result)
}

// mayControl reports whether the caller may cancel or revert the job: its
// creator, or a principal with the permission of its kind.
func (h *APIHandler) mayControl(r *http.Request, job *Job) bool {
	if user := tenant.FromContext(r.Context()).UserID; user != "" && user == job.CreatedBy {
		return true
	}
	perm, ok := h.kindPermissions[job.Kind]
	if !ok || perm == "" {
		return false
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	return ok && p.Has(perm)
}

// Cancel handles DELETE /api/v1/jobs/{id}: a queued or running job is
// cancelled; a finished one is 409.
func (h *APIHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	if !h.mayControl(r, job) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only the creator or a holder of the permission of the job kind may cancel it")
		return
	}
	cancelled, err := h.svc.Cancel(r.Context(), orgID, job.ID)
	switch {
	case errors.Is(err, ErrTransition):
		api.WriteError(w, http.StatusConflict, "Conflict", "the job is "+string(job.Status)+" and cannot be cancelled")
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, cancelled)
	}
}

// Revert handles POST /api/v1/jobs/{id}/revert: only for kinds with a
// defined revert (E-25) and succeeded jobs; otherwise 409.
func (h *APIHandler) Revert(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	job, found := h.job(w, r, orgID)
	if !found {
		return
	}
	revert, defined := h.reverters[job.Kind]
	if !defined || job.Status != StatusSucceeded {
		api.WriteError(w, http.StatusConflict, "Conflict", ErrNotRevertible.Error()+": kind "+job.Kind+", status "+string(job.Status))
		return
	}
	if !h.mayControl(r, job) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only the creator or a holder of the permission of the job kind may revert it")
		return
	}
	if err := revert(r.Context(), job); err != nil {
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "reverted": true})
}
