package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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
	// keys reloads the API key of a run started with one (WP-779).
	keys KeyReloader
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

// StartRequest is the body of POST /api/v1/imports: a document or, for
// the API import (WP-303), the JSON rows themselves, exactly one of both.
type StartRequest struct {
	DocumentID string          `json:"document_id,omitempty"`
	Rows       json.RawMessage `json:"rows,omitempty"`
	ProfileID  string          `json:"profile_id"`
	Mode       string          `json:"mode,omitempty"`
	DryRun     bool            `json:"dry_run,omitempty"`
}

// hasRows reports whether the request carries rows (null counts as none).
func (r *StartRequest) hasRows() bool {
	raw := bytes.TrimSpace(r.Rows)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
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
	// staged are the JSON rows of an API run (WP-303), checked against
	// rows_sha256, and rowCount their number; no document then.
	staged   []byte
	rowCount int
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
// included); the rows of an API run were read and checked already
// (apiSource). The caller closes the returned reader.
func (h *ImportHandler) openSource(ctx context.Context, orgID string, src *source) (io.ReadCloser, error) {
	if src.staged != nil {
		return io.NopCloser(bytes.NewReader(src.staged)), nil
	}
	if !document.OwnsStorageKey(orgID, src.doc.ID, src.version.StorageKey) {
		return nil, errForeignKey
	}
	return h.blobs.Get(ctx, document.Bucket, src.version.StorageKey)
}

var errForeignKey = errors.New("the document content is stored outside the organization")

// Start handles POST /api/v1/imports: it checks document scope (404),
// profile (404), the write right of the profile's entity (403), scan
// status (409), size (422) and format (422) in this order, queues a job of
// kind import.run with the caller as creator and their identity
// reference (StarterRef; the run reloads their rights, jobContext) and
// answers 202 with the job. With a job queue the job is
// submitted there, otherwise it runs in this process.
//
// Instead of document_id the body may carry the JSON rows themselves
// (rows, WP-303; startRows); exactly one of both, else 422. The body is
// limited to MaxAPIBodyBytes (413). With the header Idempotency-Key the
// job gets the key jobIdempotencyKey, so a repeated start returns the
// first job (202, no second run), also when the HTTP idempotency of the
// cache fails open.
func (h *ImportHandler) Start(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	if h.jobs == nil || h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "imports require the job service and blob storage")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAPIBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large",
				fmt.Sprintf("the request body exceeds %d bytes (16 MiB)", MaxAPIBodyBytes))
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "could not read request body")
		return
	}
	var req StartRequest
	if err = decodeStart(body, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	withRows := req.hasRows()
	switch {
	case withRows && req.DocumentID != "":
		api.WriteFieldError(w, invalid("rows", "excludes document_id; send either a document or rows"))
		return
	case !withRows && req.DocumentID == "":
		api.WriteFieldError(w, invalid("document_id", "is required unless rows are sent"))
		return
	case req.ProfileID == "":
		api.WriteFieldError(w, invalid("profile_id", "is required"))
		return
	case req.Mode != "" && !slices.Contains(Modes, req.Mode):
		api.WriteFieldError(w, invalid("mode", "must be create_missing, update_existing or both"))
		return
	}
	key := jobIdempotencyKey(r.Context(), r.Header.Get("Idempotency-Key"), body)
	if withRows {
		h.startRows(w, r, &t, &req, key)
		return
	}
	ctx := r.Context()
	doc, f := h.lookupDocument(ctx, t.OrganizationID, req.DocumentID)
	if f != nil {
		f.write(w)
		return
	}
	profile, f := h.startProfile(ctx, t.OrganizationID, req.ProfileID)
	if f != nil {
		f.write(w)
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
		Mode: profile.Mode, DryRun: req.DryRun, Starter: captureStarter(ctx), ScopeParams: jobs.CaptureScope(ctx)}
	if req.Mode != "" {
		params.Mode = req.Mode
	}
	if profile.Sheet != nil {
		params.Sheet = *profile.Sheet
	}
	job, created, err := h.enqueue(ctx, t.OrganizationID, t.UserID, &params, key)
	if err != nil {
		slog.ErrorContext(ctx, "import: enqueue job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	h.accepted(ctx, w, job, created)
}

// decodeStart decodes the buffered body of a start like api.ReadJSON: an
// unknown field or malformed JSON is an error naming it.
// validation: the JSON decoder names the malformed or unknown field of the request body.
func decodeStart(body []byte, req *StartRequest) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(req)
}

// startProfile reads the profile of a start (404) and checks the write
// right of its entity (403).
func (h *ImportHandler) startProfile(ctx context.Context, orgID, profileID string) (*Profile, *refusal) {
	profile, err := h.profiles.Get(ctx, orgID, profileID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			slog.WarnContext(ctx, "import: read profile", "profile", profileID, "error", err)
		}
		return nil, notFoundRefusal(errProfileNotFound.Error())
	}
	principal, hasPrincipal := identity.PrincipalFromContext(ctx)
	if perm := EntityWritePermissions[profile.Entity]; !hasPrincipal || perm == "" || !principal.Has(perm) {
		return nil, &refusal{status: http.StatusForbidden, title: "Forbidden", detail: "importing " + profile.Entity + " requires " + string(perm)}
	}
	return profile, nil
}

// startRows starts an API import over the JSON rows of the request
// (WP-303): 1 to MaxAPIRows objects (422), the profile (404) with the
// write right of its entity (403) and the format json (422). The rows go
// to a staging object in the blob store before the job is queued; the job
// parameters name it with the number and the SHA-256 of the rows, never a
// value. When queueing fails or returns an existing job (created false,
// the same idempotency key), the object is deleted again.
func (h *ImportHandler) startRows(w http.ResponseWriter, r *http.Request, t *tenant.TenantInfo, req *StartRequest, key string) {
	ctx := r.Context()
	count, ferr := countRows(req.Rows)
	if ferr != nil {
		api.WriteFieldError(w, ferr)
		return
	}
	profile, f := h.startProfile(ctx, t.OrganizationID, req.ProfileID)
	if f != nil {
		f.write(w)
		return
	}
	if profile.Format != FormatJSON {
		parseRefusal(CodeFormatUnsupported, "rows are imported with a profile of format json").write(w)
		return
	}
	rows := bytes.TrimSpace(req.Rows)
	stagingKey, err := h.stageRows(ctx, t.OrganizationID, rows)
	if err != nil {
		slog.ErrorContext(ctx, "import: stage rows", "error", err)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the rows cannot be staged in the blob storage")
		return
	}
	params := JobParams{ImportSource: SourceAPI, StagingKey: stagingKey, RowCount: count, RowsSHA256: rowsDigest(rows),
		ProfileID: profile.ID, Mode: profile.Mode, DryRun: req.DryRun, Starter: captureStarter(ctx), ScopeParams: jobs.CaptureScope(ctx)}
	if req.Mode != "" {
		params.Mode = req.Mode
	}
	job, created, err := h.enqueue(ctx, t.OrganizationID, t.UserID, &params, key)
	if err != nil || !created {
		// No run reads this object: the job was not queued, or the first
		// job of the key runs with its own.
		h.dropStagedLogged(context.WithoutCancel(ctx), t.OrganizationID, stagingKey)
	}
	if err != nil {
		slog.ErrorContext(ctx, "import: enqueue job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	h.accepted(ctx, w, job, created)
}

// countRows checks the JSON rows of a request: an array of 1 to
// MaxAPIRows objects (E-49), else 422. The values are checked by the run:
// a nested value is a row error of the report.
func countRows(raw json.RawMessage) (int, *api.FieldError) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return 0, invalid("rows", "must be an array of objects")
	}
	switch {
	case len(elems) == 0:
		return 0, invalid("rows", "must hold at least one row")
	case len(elems) > MaxAPIRows:
		return 0, invalid("rows", fmt.Sprintf("holds more than %d rows", MaxAPIRows))
	}
	for i := range elems {
		if e := bytes.TrimSpace(elems[i]); len(e) == 0 || e[0] != '{' {
			return 0, invalid(fmt.Sprintf("rows[%d]", i), "must be an object of column name and value")
		}
	}
	return len(elems), nil
}

// enqueue queues the import job with the caller as creator and the job
// key of the request (jobIdempotencyKey, empty without header): with a
// job queue through Submit (never RunDetached, which takes no key),
// otherwise in the job service. created is false when the key names an
// existing job.
func (h *ImportHandler) enqueue(ctx context.Context, orgID, userID string, params *JobParams, key string) (*jobs.Job, bool, error) {
	opts := jobs.EnqueueOptions{CreatedBy: userID, IdempotencyKey: key}
	if h.queue != nil {
		return h.queue.Submit(ctx, orgID, JobKind, params, opts)
	}
	return h.jobs.Enqueue(ctx, orgID, JobKind, params, opts)
}

// accepted answers 202 with the job; without job queue a new job runs in
// this process. An existing job (created false) is not started again.
func (h *ImportHandler) accepted(ctx context.Context, w http.ResponseWriter, job *jobs.Job, created bool) {
	if h.queue == nil && created {
		// The run restores the caller from the job, after the request.
		go h.run(context.WithoutCancel(ctx), job.OrganizationID, job.ID)
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
// is no import table. A run over JSON rows (source api) has no document
// but the number of its rows.
type ImportRun struct {
	ID              string      `json:"id"`
	Kind            string      `json:"kind"`
	Status          jobs.Status `json:"status"`
	Progress        int         `json:"progress"`
	Source          string      `json:"source"`
	DocumentID      *string     `json:"document_id"`
	DocumentVersion *int        `json:"document_version"`
	RowCount        *int        `json:"row_count"`
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
	if !h.runReadable(r.Context(), orgID, job, &params) {
		api.WriteError(w, http.StatusNotFound, "Not Found", errImportNotFound.Error())
		return nil, nil, false
	}
	return job, &params, true
}

// runReadable reports whether the caller may read the run: a run of a
// document while the document lies in their scope; a run over JSON rows
// (source api, WP-303), which has no document, only for the principal who
// started it (subject = created_by, like the export jobs).
func (h *ImportHandler) runReadable(ctx context.Context, orgID string, job *jobs.Job, params *JobParams) bool {
	if params.ImportSource == SourceAPI {
		p, ok := identity.PrincipalFromContext(ctx)
		return ok && p.Subject != "" && p.Subject == job.CreatedBy
	}
	_, f := h.lookupDocument(ctx, orgID, params.DocumentID)
	return f == nil
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
	run := ImportRun{ID: job.ID, Kind: job.Kind, Status: job.Status, Progress: job.Progress, Source: "document",
		ProfileID: params.ProfileID, Mode: params.Mode, DryRun: params.DryRun, Cancellable: h.jobs.Cancellable(job),
		CreatedBy: job.CreatedBy, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt}
	if params.ImportSource == SourceAPI {
		run.Source, run.RowCount = SourceAPI, &params.RowCount
	} else {
		run.DocumentID, run.DocumentVersion = &params.DocumentID, &params.DocumentVersion
	}
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
