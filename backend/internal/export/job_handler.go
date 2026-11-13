package export

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// jobResponse is the API representation of a Job. The signed download URL is
// minted on read and never persisted.
type jobResponse struct {
	Job
	DownloadURL string `json:"download_url,omitempty"`
	// ErrorReportURL is the signed URL of the error report of a completed,
	// unexpired DATEV job (WP-310).
	ErrorReportURL string `json:"error_report_url,omitempty"`
	// Manifest lists the fields of the file and the omitted ones (EXP-01).
	Manifest Manifest `json:"manifest"`
}

// JobHandler provides HTTP handlers for asynchronous export jobs.
type JobHandler struct {
	jobs   JobRepository
	worker *JobWorker
	blobs  blob.Store
	bucket string
	// datev guards the format datev (WP-790); nil refuses it.
	datev *DATEVGate
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

// WithDATEVGate sets the gate of the format datev; without one DATEV is
// refused (fail closed).
func (h *JobHandler) WithDATEVGate(gate *DATEVGate) *JobHandler {
	h.datev = gate
	return h
}

// RegisterRoutes registers the export job routes.
func (h *JobHandler) RegisterRoutes(r chi.Router) {
	// exports is the catalog path (API-05); export/jobs stays until its
	// sunset (API-01).
	for _, base := range []string{"/api/v1/exports", "/api/v1/export/jobs"} {
		r.Post(base, h.CreateJob)
		r.Get(base, h.ListJobs)
		r.Get(base+"/{id}", h.GetJob)
	}
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
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrExportUnavailable.Error())
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
	// The asset export runs through POST /api/v1/assets/bulk only, behind
	// the module inventory and the entitlement of /api/v1/assets (WP-782).
	if req.Filters.IsAsset() {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", ErrAssetEntityRefused.Error())
		return
	}
	if err := req.Validate(); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	// Subnets and addresses need ipam:read beside the route's export:run
	// (WP-921): the export reads what GET /api/v1/subnets and /ips read.
	if req.Filters.IsIPAM() && !canReadIPAM(r.Context()) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrIPAMReadRequired)
		return
	}
	if !guardDATEV(w, r, h.datev, t.OrganizationID, req.Format) {
		return
	}

	job, err := h.Enqueue(r.Context(), t.OrganizationID, &req)
	switch {
	case errors.Is(err, ErrNoTenantScope):
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", ErrNoTenantScope.Error())
		return
	case err != nil:
		api.WriteRepoError(w, err)
		return
	}

	resp, err := h.toResponse(r, job, &datevPreview{})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, resp)
}

// canReadIPAM reports whether the principal of ctx holds ipam:read.
func canReadIPAM(ctx context.Context) bool {
	principal, ok := identity.PrincipalFromContext(ctx)
	return ok && principal.Has(identity.PermIPAMRead)
}

// Errors of Enqueue.
var (
	// ErrExportUnavailable: the asynchronous export needs blob storage
	// (503).
	ErrExportUnavailable = errors.New("asynchronous export requires blob storage; use GET /api/v1/export/cis for a streaming export")
	// ErrNoTenantScope: the context carries no tenant scope to snapshot
	// (401).
	ErrNoTenantScope = errors.New("missing tenant scope")
	// ErrAssetEntityRefused: POST /api/v1/exports takes no asset export
	// (filters.entity asset, 400); it runs through POST
	// /api/v1/assets/bulk (WP-782).
	ErrAssetEntityRefused = errors.New("filters.entity asset is created through POST /api/v1/assets/bulk with the action export")
)

// ExportJobKind is the kind of an export job in the generic job model.
const ExportJobKind = "export"

// Enqueue creates the export job of a checked request (Validate) for the
// caller of ctx, the path of POST /api/v1/exports and of the bulk export
// of CIs (WP-780): the job records its creator (the principal's subject,
// otherwise the user of the scope) and the snapshot of the creator's scope
// (SnapshotScope), with which the worker renders it and only the creator
// reads it; the generic job model lists it with kind export (JOB-01).
func (h *JobHandler) Enqueue(ctx context.Context, orgID string, req *CreateJobRequest) (*Job, error) {
	if h.worker == nil || !h.worker.Enabled() {
		return nil, ErrExportUnavailable
	}
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return nil, ErrNoTenantScope
	}
	job := &Job{Format: req.Format, Filters: req.Filters, InitiatedBy: scope.UserID, Scope: SnapshotScope(&scope)}
	if principal, found := identity.PrincipalFromContext(ctx); found {
		job.InitiatedBy = principal.Subject
	}
	if err := h.jobs.CreateJob(ctx, orgID, job); err != nil {
		return nil, err
	}
	if h.worker.Tracker != nil {
		// The generic job model lists the export too (JOB-01).
		if err := h.worker.Tracker.Track(ctx, orgID, ExportJobKind, "export_job", job.ID, job.InitiatedBy); err != nil {
			slog.Warn("track export job", "job", job.ID, "error", err)
		}
	}
	return job, nil
}

// ListJobs handles GET /api/v1/export/jobs and returns the caller's own
// export jobs newest first.
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	jobs, total, err := h.jobs.ListJobs(r.Context(), t.OrganizationID, page.Limit, page.Offset)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	data := make([]jobResponse, 0, len(jobs))
	// The mapping override is read once for the page (WP-310).
	preview := &datevPreview{}
	for i := range jobs {
		resp, rerr := h.toResponse(r, &jobs[i], preview)
		if rerr != nil {
			api.WriteRepoError(w, rerr)
			return
		}
		data = append(data, resp)
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[jobResponse]{
		Data:    data,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// GetJob handles GET /api/v1/export/jobs/{id}. Only the creator reads a job;
// completed, unexpired jobs carry a time-limited signed download URL.
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
	resp, err := h.toResponse(r, job, &datevPreview{})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// toResponse attaches the manifest and, to completed, unexpired jobs, a
// fresh signed URL (and for DATEV one of the error report, WP-310). An
// expired or failed job never exposes its object key as a URL. The
// manifest of a completed job is the one its worker stored with the file
// (WP-729); any other job shows the preview with the field permissions of
// the caller (GetJob and ListJobs return its own jobs only). A policy that
// cannot be resolved is an error (fail-closed).
func (h *JobHandler) toResponse(r *http.Request, job *Job, preview *datevPreview) (jobResponse, error) {
	manifest, err := h.manifest(r, job, preview)
	if err != nil {
		return jobResponse{}, err
	}
	resp := jobResponse{Job: *job, Manifest: manifest}
	if job.Status != JobStatusCompleted || job.ObjectKey == "" || h.blobs == nil {
		return resp, nil
	}
	if job.ExpiresAt != nil && !job.ExpiresAt.After(time.Now().UTC()) {
		return resp, nil
	}
	url, err := h.blobs.PresignedGetURL(r.Context(), h.bucket, job.ObjectKey,
		blob.Download{FileName: path.Base(job.ObjectKey), ContentType: exportContentTypes[job.Format]})
	if err != nil {
		return resp, nil
	}
	resp.DownloadURL = url
	if job.Format == FormatDATEV {
		reportKey := ErrorReportKey(job)
		if reportURL, rerr := h.blobs.PresignedGetURL(r.Context(), h.bucket, reportKey,
			blob.Download{FileName: path.Base(reportKey), ContentType: errorReportContentType}); rerr == nil {
			resp.ErrorReportURL = reportURL
		}
	}
	return resp, nil
}

// manifest returns the stored manifest of a completed job or the preview
// for the caller. A completed job without a readable manifest (rendered
// before WP-729) shows the preview as well.
func (h *JobHandler) manifest(r *http.Request, job *Job, preview *datevPreview) (Manifest, error) {
	if job.Status == JobStatusCompleted && job.ObjectKey != "" && h.blobs != nil {
		if m, ok := h.storedManifest(r.Context(), job); ok {
			return m, nil
		}
	}
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		return Manifest{}, err
	}
	if job.Format == FormatDATEV {
		return h.datevPreviewManifest(r, job.OrganizationID, preview, policy)
	}
	// An IPAM export lists its columns, nothing omitted (WP-921).
	if job.Filters.IsIPAM() {
		return IPAMManifest(job.Filters.Entity), nil
	}
	// An asset export lists the fields of the asset DTO (WP-782).
	if job.Filters.IsAsset() {
		return AssetManifestForPolicy(job.Format, policy), nil
	}
	return ManifestForPolicy(job.Format, policy, typeFilter(job.Filters.CITypeID)), nil
}

// datevPreview holds the effective DATEV mapping of the organization,
// read at most once per request (WP-310).
type datevPreview struct {
	read    bool
	mapping *DATEVMapping
	err     error
}

// datevPreviewManifest is the manifest of an open DATEV job: the effective
// mapping with the policy of the caller, its creator. An invalid override
// (the job will fail) shows no fields.
func (h *JobHandler) datevPreviewManifest(r *http.Request, orgID string, preview *datevPreview,
	policy *permission.FieldPolicy) (Manifest, error) {
	if !preview.read {
		preview.read = true
		var orgs OrganizationReader
		if h.worker != nil {
			orgs = h.worker.Orgs
		}
		preview.mapping, _, preview.err = datevSettings(r.Context(), orgs, orgID)
	}
	switch {
	case preview.err == nil:
		return DATEVManifest(preview.mapping, policy), nil
	case isDATEVMappingError(preview.err):
		return Manifest{Format: FormatDATEV, Fields: []string{}, Omitted: []OmittedField{}}, nil
	default:
		return Manifest{}, preview.err
	}
}

// maxManifestBytes bounds the stored manifest read back.
const maxManifestBytes = 1 << 20

// storedManifest reads the manifest the worker stored next to the object.
func (h *JobHandler) storedManifest(ctx context.Context, job *Job) (Manifest, bool) {
	rc, err := h.blobs.Get(ctx, h.bucket, ManifestKey(job))
	if err != nil {
		return Manifest{}, false
	}
	defer func() { _ = rc.Close() }()
	var m Manifest
	if derr := json.NewDecoder(io.LimitReader(rc, maxManifestBytes)).Decode(&m); derr != nil || m.Format != job.Format {
		slog.WarnContext(ctx, "read export manifest", "job", job.ID, "error", derr)
		return Manifest{}, false
	}
	if m.Fields == nil {
		m.Fields = []string{}
	}
	if m.Omitted == nil {
		m.Omitted = []OmittedField{}
	}
	return m, true
}
