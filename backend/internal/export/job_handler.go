package export

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// jobResponse is the API representation of a Job. The signed download URL is
// minted on read and never persisted.
type jobResponse struct {
	Job
	DownloadURL string `json:"download_url,omitempty"`
}

// JobHandler provides HTTP handlers for asynchronous export jobs.
type JobHandler struct {
	jobs   JobRepository
	worker *JobWorker
	blobs  blob.Store
	bucket string
}

// NewJobHandler creates the handler. worker may be nil (or disabled) when no
// blob storage is configured; creating jobs then returns 503.
func NewJobHandler(jobs JobRepository, worker *JobWorker, blobs blob.Store) *JobHandler {
	bucket := ExportBucket
	if worker != nil && worker.Bucket != "" {
		bucket = worker.Bucket
	}
	return &JobHandler{jobs: jobs, worker: worker, blobs: blobs, bucket: bucket}
}

// RegisterRoutes registers the export job routes.
func (h *JobHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/export/jobs", h.CreateJob)
	r.Get("/api/v1/export/jobs", h.ListJobs)
	r.Get("/api/v1/export/jobs/{id}", h.GetJob)
}

// CreateJob handles POST /api/v1/export/jobs and queues an asynchronous
// export. The result is rendered by the background worker into blob storage
// and becomes downloadable once the job is completed.
func (h *JobHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.worker == nil || !h.worker.Enabled() {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "asynchronous export requires blob storage; use GET /api/v1/export/cis for a streaming export")
		return
	}

	var req CreateJobRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Format == "" {
		req.Format = "json"
	}
	if err := req.Validate(); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	job := &Job{Format: req.Format, Filters: req.Filters}
	if principal, ok := identity.PrincipalFromContext(r.Context()); ok {
		job.InitiatedBy = principal.Subject
	}
	if err := h.jobs.CreateJob(r.Context(), t.OrganizationID, job); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusAccepted, h.toResponse(r, job))
}

// ListJobs handles GET /api/v1/export/jobs and returns the tenant's export
// jobs newest first.
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	jobs, total, err := h.jobs.ListJobs(r.Context(), t.OrganizationID, page.Limit, page.Offset)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	data := make([]jobResponse, 0, len(jobs))
	for i := range jobs {
		data = append(data, h.toResponse(r, &jobs[i]))
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[jobResponse]{
		Data:    data,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// GetJob handles GET /api/v1/export/jobs/{id}. Completed, unexpired jobs
// carry a time-limited signed download URL.
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	job, err := h.jobs.GetJob(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "export job not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, h.toResponse(r, job))
}

// toResponse attaches a fresh signed URL to completed, unexpired jobs. An
// expired or failed job never exposes its object key as a URL.
func (h *JobHandler) toResponse(r *http.Request, job *Job) jobResponse {
	resp := jobResponse{Job: *job}
	if job.Status != JobStatusCompleted || job.ObjectKey == "" || h.blobs == nil {
		return resp
	}
	if job.ExpiresAt != nil && !job.ExpiresAt.After(time.Now().UTC()) {
		return resp
	}
	url, err := h.blobs.PresignedGetURL(r.Context(), h.bucket, job.ObjectKey)
	if err != nil {
		return resp
	}
	resp.DownloadURL = url
	return resp
}
