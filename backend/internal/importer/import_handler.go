package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/go-chi/chi/v5"
)

// ImportHandler serves the import runs under /api/v1/imports (E-38,
// IMP-IO-01): the start of a run as a job of kind import.run, its
// projection, its report and the preview of a file. The right import:run,
// the module gate import and the route audit are applied by the router.
//
// The routes are registered in both profiles; without job service (no
// database) or blob store they answer 503.
//
// A run (WP-775) maps every row with the profile and hands it to the
// writer of the profile's entity, each row in a transaction of its own
// (RunInTx of the profile store); the writers are registered with
// WithWriter (router.go).
type ImportHandler struct {
	profiles Repository
	docs     document.Repository
	blobs    blob.Store
	jobs     *jobs.Service
	queue    JobQueue
	writers  map[string]Writer
	tx       RowTx
	// transactional: tx rolls a row back (dry run possible).
	transactional bool
	modules       ModuleGate
	features      FeatureGate
	events        events.Emitter
	// users and grants restore the starter of a run (jobContext); without
	// them every run fails with import-principal-missing.
	users  UserStatus
	grants GrantSource
}

// NewImportHandler returns the handler of the import runs over the
// profiles, the documents and the blob store (nil: content routes 503).
// A profile store with RunInTx (PGRepository) runs every row in a
// transaction of its own.
func NewImportHandler(profiles Repository, docs document.Repository, blobs blob.Store) *ImportHandler {
	h := &ImportHandler{profiles: profiles, docs: docs, blobs: blobs, writers: map[string]Writer{}, tx: directTx{}}
	if tx, ok := profiles.(RowTx); ok {
		h.tx, h.transactional = tx, true
	}
	return h
}

// WithJobs runs the imports as jobs of svc (registerJobKinds): without
// job queue in this process, with one the job service records progress,
// cancel and result.
func (h *ImportHandler) WithJobs(svc *jobs.Service) *ImportHandler {
	h.jobs = svc
	return h
}

// WithQueue submits the import jobs to the job queue (TEC-14), whose
// workers run them with RunJob.
func (h *ImportHandler) WithQueue(q JobQueue) *ImportHandler {
	h.queue = q
	return h
}

// WithWriter registers the writer of an entity.
func (h *ImportHandler) WithWriter(w Writer) *ImportHandler {
	h.writers[w.Entity()] = w
	return h
}

// WithGates sets the release gate of the modules and the org gate of the
// features a run checks before its first row; nil gates pass.
func (h *ImportHandler) WithGates(modules ModuleGate, features FeatureGate) *ImportHandler {
	h.modules, h.features = modules, features
	return h
}

// WithEvents reports import.completed through e after a run succeeded.
func (h *ImportHandler) WithEvents(e events.Emitter) *ImportHandler {
	h.events = e
	return h
}

// WithStarters restores the user who started a run from the job row: the
// status of the user (users) and their current role assignments (grants),
// read when the run begins (jobContext).
func (h *ImportHandler) WithStarters(users UserStatus, grants GrantSource) *ImportHandler {
	h.users, h.grants = users, grants
	return h
}

// RegisterRoutes registers the import routes.
func (h *ImportHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/imports", h.Start)
	r.Post("/api/v1/imports/preview", h.Preview)
	r.Get("/api/v1/imports/{id}", h.Get)
	r.Get("/api/v1/imports/{id}/report", h.Report)
}

// Problem types of the import routes beside the parser codes.
var (
	problemReportNotReady = ProblemType("import-report-not-ready")
	errImportNotFound     = errors.New("import not found")
	errDocumentNotFound   = errors.New("document not found")
	errNoContent          = errors.New("document has no stored content")
	errProfileNotFound    = ErrNotFound
)

// StartRequest is the body of POST /api/v1/imports.
type StartRequest struct {
	DocumentID string `json:"document_id"`
	ProfileID  string `json:"profile_id"`
	Mode       string `json:"mode,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

// PreviewRequest is the body of POST /api/v1/imports/preview.
type PreviewRequest struct {
	DocumentID string `json:"document_id"`
	Sheet      string `json:"sheet,omitempty"`
}

// Preview is the answer of POST /api/v1/imports/preview: the format of the
// file, its sheets (XLSX), the header and at most PreviewRows data rows.
type Preview struct {
	Format     string     `json:"format"`
	Sheets     []string   `json:"sheets"`
	Columns    []string   `json:"columns"`
	SampleRows [][]string `json:"sample_rows"`
}

// refusal is a check of the start or the preview that failed with its
// answer.
type refusal struct {
	status  int
	problem string
	title   string
	detail  string
}

func (f *refusal) write(w http.ResponseWriter) {
	if f.problem == "" {
		api.WriteError(w, f.status, f.title, f.detail)
		return
	}
	api.WriteProblem(w, f.status, f.problem, f.title, f.detail)
}

func notFoundRefusal(detail string) *refusal {
	return &refusal{status: http.StatusNotFound, title: "Not Found", detail: detail}
}

// parseRefusal is the 422 of a parser code.
func parseRefusal(code, detail string) *refusal {
	return &refusal{status: http.StatusUnprocessableEntity, problem: ProblemType(code), title: "Import File Refused", detail: detail}
}

// scanRefusal answers a version that is not scanned clean (WP-733, E-42,
// fail-closed): infected is document-quarantined, any other status
// document-pending-scan; nil for clean.
func scanRefusal(status string) *refusal {
	switch status {
	case document.ScanClean:
		return nil
	case document.ScanInfected:
		return &refusal{status: http.StatusConflict, problem: document.ProblemQuarantined, title: "Document Quarantined",
			detail: "the document content was found infected and is quarantined"}
	}
	return &refusal{status: http.StatusConflict, problem: document.ProblemPendingScan, title: "Document Pending Scan",
		detail: "the document content has not been scanned clean yet"}
}

// source is the checked document version an import reads and, for a
// run, its profile.
type source struct {
	doc     *document.Document
	version *document.Version
	format  string
	profile *Profile
}

// lookupDocument returns the document when it lies in the caller's scope
// (document RLS of GetByID), else a 404 refusal.
func (h *ImportHandler) lookupDocument(ctx context.Context, orgID, id string) (*document.Document, *refusal) {
	if id == "" {
		return nil, notFoundRefusal(errDocumentNotFound.Error())
	}
	doc, err := h.docs.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, notFoundRefusal(errDocumentNotFound.Error())
	}
	return doc, nil
}

// checkVersion checks the current version of doc: stored content, scan
// status clean (409), size within MaxFileBytes (422) and a verified type an
// import reads (422). want, when set, is the format the profile demands.
func (h *ImportHandler) checkVersion(ctx context.Context, orgID string, doc *document.Document, want string) (*source, *refusal) {
	v, err := h.docs.GetVersion(ctx, orgID, doc.ID, doc.Version)
	if err != nil {
		if !errors.Is(err, api.ErrNotFound) {
			slog.WarnContext(ctx, "import: read document version", "document", doc.ID, "error", err)
		}
		return nil, notFoundRefusal(errNoContent.Error())
	}
	if v.StorageKey == "" {
		return nil, notFoundRefusal(errNoContent.Error())
	}
	if f := scanRefusal(v.ScanStatus); f != nil {
		return nil, f
	}
	if v.Size > MaxFileBytes {
		return nil, parseRefusal(CodeFileTooLarge, "the document version is larger than 50 MB (52428800 bytes)")
	}
	format, ok := FormatOfMime(v.MimeType)
	if !ok || (want != "" && format != want) {
		return nil, parseRefusal(CodeFormatUnsupported,
			"the import reads CSV (text/csv, text/plain) and XLSX files of the format of its profile")
	}
	return &source{doc: doc, version: v, format: format}, nil
}

// openSource reads the blob of a checked version outside any database
// transaction (K20), after the key check of the download (legacy keys
// included). The caller closes the returned reader.
func (h *ImportHandler) openSource(ctx context.Context, orgID string, src *source) (io.ReadCloser, error) {
	if !document.OwnsStorageKey(orgID, src.doc.ID, src.version.StorageKey) {
		return nil, errForeignKey
	}
	return h.blobs.Get(ctx, document.Bucket, src.version.StorageKey)
}

var errForeignKey = errors.New("the document content is stored outside the organization")

// Start handles POST /api/v1/imports: it checks document scope (404),
// profile (404), the write right of the profile's entity (403), scan
// status (409), size (422) and format (422) in this order, queues a job of
// kind import.run with the caller as creator (the run reloads their
// rights, jobContext) and answers 202 with the job. With a job queue the job is
// submitted there, otherwise it runs in this process.
func (h *ImportHandler) Start(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil || h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "imports require the job service and blob storage")
		return
	}
	var req StartRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	switch {
	case req.DocumentID == "":
		api.WriteFieldError(w, invalid("document_id", "is required"))
		return
	case req.ProfileID == "":
		api.WriteFieldError(w, invalid("profile_id", "is required"))
		return
	case req.Mode != "" && !slices.Contains(Modes, req.Mode):
		api.WriteFieldError(w, invalid("mode", "must be create_missing, update_existing or both"))
		return
	}
	ctx := r.Context()
	doc, f := h.lookupDocument(ctx, t.OrganizationID, req.DocumentID)
	if f != nil {
		f.write(w)
		return
	}
	profile, err := h.profiles.Get(ctx, t.OrganizationID, req.ProfileID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			slog.WarnContext(ctx, "import: read profile", "profile", req.ProfileID, "error", err)
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", errProfileNotFound.Error())
		return
	}
	principal, hasPrincipal := identity.PrincipalFromContext(ctx)
	if perm := EntityWritePermissions[profile.Entity]; !hasPrincipal || perm == "" || !principal.Has(perm) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "importing "+profile.Entity+" requires "+string(perm))
		return
	}
	want := profile.Format
	if want != FormatCSV && want != FormatXLSX {
		// JSON rows come through the API import (WP-303); a document
		// cannot be imported with such a profile. The scan check runs
		// first: an unclean version stays 409.
		want = "-"
	}
	src, f := h.checkVersion(ctx, t.OrganizationID, doc, want)
	if f != nil {
		f.write(w)
		return
	}
	params := JobParams{DocumentID: doc.ID, DocumentVersion: src.version.Version, ProfileID: profile.ID,
		Mode: profile.Mode, DryRun: req.DryRun, ScopeParams: jobs.CaptureScope(ctx)}
	if req.Mode != "" {
		params.Mode = req.Mode
	}
	if profile.Sheet != nil {
		params.Sheet = *profile.Sheet
	}
	var job *jobs.Job
	if h.queue != nil {
		job, _, err = h.queue.Submit(ctx, t.OrganizationID, JobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	} else {
		job, _, err = h.jobs.Enqueue(ctx, t.OrganizationID, JobKind, params, jobs.EnqueueOptions{CreatedBy: t.UserID})
	}
	if err != nil {
		slog.ErrorContext(ctx, "import: enqueue job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	if h.queue == nil {
		// The run restores the caller from the job, after the request.
		go h.run(context.WithoutCancel(ctx), t.OrganizationID, job.ID)
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// Preview handles POST /api/v1/imports/preview: the checks of the start
// without profile (the format follows from the verified type), then the
// header and at most PreviewRows data rows, read synchronously with the
// parser and its limits. No job is created.
func (h *ImportHandler) Preview(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the import preview requires blob storage")
		return
	}
	var req PreviewRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.DocumentID == "" {
		api.WriteFieldError(w, invalid("document_id", "is required"))
		return
	}
	ctx := r.Context()
	doc, f := h.lookupDocument(ctx, t.OrganizationID, req.DocumentID)
	if f != nil {
		f.write(w)
		return
	}
	src, f := h.checkVersion(ctx, t.OrganizationID, doc, "")
	if f != nil {
		f.write(w)
		return
	}
	body, err := h.openSource(ctx, t.OrganizationID, src)
	if err != nil {
		if errors.Is(err, errForeignKey) {
			api.WriteError(w, http.StatusConflict, "Conflict", errForeignKey.Error())
			return
		}
		slog.WarnContext(ctx, "import preview: read blob", "document", doc.ID, "error", err)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the document content cannot be read")
		return
	}
	defer func() { _ = body.Close() }()
	preview, perr := readPreview(src.format, body, req.Sheet)
	if perr != nil {
		parseRefusal(perr.Code, perr.Message).write(w)
		return
	}
	api.WriteJSON(w, http.StatusOK, preview)
}

// readPreview reads the header and at most PreviewRows data rows; rows the
// reader cannot split are left out. A fatal parser error is returned.
func readPreview(format string, body io.ReadCloser, sheet string) (*Preview, *ParseError) {
	rd, err := Open(format, body, sheet)
	if err != nil {
		return nil, asParseError(err)
	}
	defer func() { _ = rd.Close() }()
	p := &Preview{Format: format, Sheets: rd.Sheets(), Columns: slices.Clone(rd.Header()), SampleRows: [][]string{}}
	if p.Sheets == nil {
		p.Sheets = []string{}
	}
	for len(p.SampleRows) < PreviewRows {
		row, rerr := rd.Next()
		if rerr != nil {
			var pe *ParseError
			if errors.As(rerr, &pe) && !pe.Fatal {
				continue
			}
			if isEOF(rerr) {
				break
			}
			return nil, asParseError(rerr)
		}
		p.SampleRows = append(p.SampleRows, slices.Clone(row.Cells))
	}
	return p, nil
}

// asParseError returns err as a fatal parser error; a read error of the
// content becomes CodeFormatUnsupported without its text.
func asParseError(err error) *ParseError {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe
	}
	return &ParseError{Code: CodeFormatUnsupported, Message: "the file cannot be read", Fatal: true}
}

// ImportRun is the projection of an import job (GET /api/v1/imports/{id}):
// parameters, status, progress, counters and error code of the job; there
// is no import table.
type ImportRun struct {
	ID              string      `json:"id"`
	Kind            string      `json:"kind"`
	Status          jobs.Status `json:"status"`
	Progress        int         `json:"progress"`
	DocumentID      string      `json:"document_id"`
	DocumentVersion int         `json:"document_version"`
	ProfileID       string      `json:"profile_id"`
	Mode            string      `json:"mode"`
	DryRun          bool        `json:"dry_run"`
	Sheet           *string     `json:"sheet"`
	RowsTotal       *int        `json:"rows_total"`
	RowsFailed      *int        `json:"rows_failed"`
	ErrorCode       *string     `json:"error_code"`
	Error           *string     `json:"error"`
	Cancellable     bool        `json:"cancellable"`
	CreatedBy       string      `json:"created_by,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	StartedAt       *time.Time  `json:"started_at"`
	FinishedAt      *time.Time  `json:"finished_at"`
}

// loadRun returns the import job of the path: a job of another kind, one
// outside the caller's scope and one whose document the caller does not
// see are 404 (the report shows no rows of an invisible document).
func (h *ImportHandler) loadRun(w http.ResponseWriter, r *http.Request, orgID string) (*jobs.Job, *JobParams, bool) {
	id := chi.URLParam(r, "id")
	job, err := h.jobs.Get(r.Context(), orgID, id)
	if err != nil || job.Kind != JobKind || !visible(r.Context(), job) {
		if err != nil && !errors.Is(err, jobs.ErrNotFound) {
			slog.WarnContext(r.Context(), "import: read job", "job", id, "error", err)
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", errImportNotFound.Error())
		return nil, nil, false
	}
	var params JobParams
	if json.Unmarshal(job.Params, &params) != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", errImportNotFound.Error())
		return nil, nil, false
	}
	if _, f := h.lookupDocument(r.Context(), orgID, params.DocumentID); f != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", errImportNotFound.Error())
		return nil, nil, false
	}
	return job, &params, true
}

// Get handles GET /api/v1/imports/{id}: the projection of the import job.
func (h *ImportHandler) Get(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "imports require the job service")
		return
	}
	job, params, found := h.loadRun(w, r, t.OrganizationID)
	if !found {
		return
	}
	run := ImportRun{ID: job.ID, Kind: job.Kind, Status: job.Status, Progress: job.Progress,
		DocumentID: params.DocumentID, DocumentVersion: params.DocumentVersion, ProfileID: params.ProfileID,
		Mode: params.Mode, DryRun: params.DryRun, Cancellable: h.jobs.Cancellable(job),
		CreatedBy: job.CreatedBy, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt}
	if params.Sheet != "" {
		run.Sheet = &params.Sheet
	}
	var result JobResult
	if len(job.Result) > 0 && json.Unmarshal(job.Result, &result) == nil {
		run.RowsTotal, run.RowsFailed = &result.Total, &result.Failed
	}
	if job.Error != "" {
		msg := job.Error
		run.Error = &msg
		if code := errorCode(msg); code != "" {
			run.ErrorCode = &code
		}
	}
	api.WriteJSON(w, http.StatusOK, run)
}

// ReportLink is the answer of GET /api/v1/imports/{id}/report.
type ReportLink struct {
	DownloadURL string `json:"download_url"`
}

// Report handles GET /api/v1/imports/{id}/report: a presigned URL of the
// report of a finished run; 409 import-report-not-ready while the job is
// queued or running, 404 for a cancelled run.
func (h *ImportHandler) Report(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil || h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "import reports require the job service and blob storage")
		return
	}
	job, _, found := h.loadRun(w, r, t.OrganizationID)
	if !found {
		return
	}
	switch job.Status {
	case jobs.StatusQueued, jobs.StatusRunning:
		api.WriteProblem(w, http.StatusConflict, problemReportNotReady, "Import Report Not Ready",
			"the import is "+string(job.Status)+"; its report follows when it ends")
		return
	case jobs.StatusCancelled:
		api.WriteError(w, http.StatusNotFound, "Not Found", "a cancelled import has no report")
		return
	}
	url, err := h.blobs.PresignedGetURL(r.Context(), export.ExportBucket, ReportKey(t.OrganizationID, job.ID))
	if err != nil {
		slog.ErrorContext(r.Context(), "import: presign report", "job", job.ID, "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not create a download URL")
		return
	}
	api.WriteJSON(w, http.StatusOK, ReportLink{DownloadURL: url})
}
