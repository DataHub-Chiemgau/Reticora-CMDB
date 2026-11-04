package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// JobKind is the job kind of an import run (API-09, JOB-04).
const JobKind = "import.run"

// RunTimeout bounds a run; it ends before the reaper (jobs.reap) fails the
// job after the timeout of its kind.
const RunTimeout = 30 * time.Minute

// progressEvery is the number of rows after which a run reports its
// progress (and notices a cancel).
const progressEvery = 1000

// runSlots lets at most two runs of this instance parse at the same time;
// further runs wait with their job queued.
var runSlots = make(chan struct{}, 2)

// JobParams are the parameters of an import job: the document version it
// reads (a later version is not read), the profile, the write mode, the
// dry run flag and the sheet of an XLSX file (empty: the first).
type JobParams struct {
	DocumentID      string `json:"document_id"`
	DocumentVersion int    `json:"document_version"`
	ProfileID       string `json:"profile_id"`
	Mode            string `json:"mode"`
	DryRun          bool   `json:"dry_run"`
	Sheet           string `json:"sheet,omitempty"`
}

// JobResult is the result of a succeeded import job: the data rows read,
// the rows with an error and the blob key of the report.
type JobResult struct {
	RowsTotal  int    `json:"rows_total"`
	RowsFailed int    `json:"rows_failed"`
	ReportKey  string `json:"report_key"`
}

// ReportKey is the fixed blob key of the report of an import job in the
// bucket export.ExportBucket.
func ReportKey(orgID, jobID string) string {
	return blob.OrgKey(orgID, "imports", jobID, "report.csv")
}

// errStopped ends a run that was cancelled: it records nothing further.
var errStopped = errors.New("import cancelled")

// codePattern is the form of an error code at the start of job.error.
var codePattern = regexp.MustCompile(`^([a-z][a-z-]*[a-z]): `)

// errorCode returns the code of a job error "<code>: <message>"; empty for
// an error without one (the reaper's timeout).
func errorCode(msg string) string {
	if m := codePattern.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

// visible reports whether the tenant scope of ctx covers the job (the rule
// of job_isolation, Job.VisibleTo); without scope no job is visible.
func visible(ctx context.Context, job *jobs.Job) bool {
	scope, ok := database.TenantScopeFromContext(ctx)
	return ok && job.VisibleTo(&scope)
}

// report collects the errors of a run (line, column, code, message).
type report struct {
	buf bytes.Buffer
	w   *csv.Writer
}

func newReport() *report {
	r := &report{}
	r.w = csv.NewWriter(&r.buf)
	_ = r.w.Write([]string{"line", "column", "code", "message"})
	return r
}

func (r *report) add(e *ParseError) {
	cell := func(n int) string {
		if n <= 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	_ = r.w.Write([]string{cell(e.Line), cell(e.Column), e.Code, e.Message})
}

func (r *report) bytes() []byte {
	r.w.Flush()
	return r.buf.Bytes()
}

// run executes an import job in this process: it waits for a parse slot
// (the job stays queued), claims the job and reads the file. ctx carries
// the tenant and the scope of the caller (context.WithoutCancel of the
// request), so documents, versions and profiles are read under the
// caller's row level security (database.WithRequestTenant of the
// repositories), never with WithSystem.
func (h *ImportHandler) run(ctx context.Context, orgID, jobID string, p *JobParams) {
	runSlots <- struct{}{}
	defer func() { <-runSlots }()
	claimed, err := h.jobs.ClaimJob(ctx, orgID, jobID)
	if err != nil || claimed == nil {
		// Cancelled while it waited, or claimed elsewhere.
		if err != nil {
			slog.ErrorContext(ctx, "import: claim job", "job", jobID, "error", err)
		}
		return
	}
	// A panic of the parser on a hostile file fails the job instead of the
	// process.
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "import: run panicked", "job", jobID, "panic", rec)
			_, _ = h.jobs.Fail(ctx, orgID, jobID, &ParseError{Code: "import-failed", Message: "the import could not be completed", Fatal: true})
		}
	}()
	runCtx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	result, rep, runErr := h.execute(runCtx, orgID, jobID, p)
	if errors.Is(runErr, errStopped) {
		return
	}
	if cancelled, cerr := h.jobs.IsCancelled(ctx, orgID, jobID); cerr == nil && cancelled {
		return
	}
	if errors.Is(runErr, context.DeadlineExceeded) || (runErr == nil && runCtx.Err() != nil) {
		runErr = &ParseError{Code: "import-timeout", Message: fmt.Sprintf("the import did not end within %s", RunTimeout), Fatal: true}
	}
	if runErr != nil {
		var pe *ParseError
		if !errors.As(runErr, &pe) {
			slog.ErrorContext(ctx, "import: run failed", "job", jobID, "error", runErr)
			pe = &ParseError{Code: "import-failed", Message: "the import could not be completed", Fatal: true}
			runErr = pe
		}
		rep.add(pe)
	}
	// The report is written before the status, outside any transaction
	// (K20).
	key := ReportKey(orgID, jobID)
	content := rep.bytes()
	if perr := h.blobs.Put(ctx, export.ExportBucket, key, bytes.NewReader(content), int64(len(content)), "text/csv"); perr != nil {
		slog.ErrorContext(ctx, "import: write report", "job", jobID, "error", perr)
		if runErr == nil {
			runErr = &ParseError{Code: "import-failed", Message: "the import report could not be stored", Fatal: true}
		}
	}
	if runErr != nil {
		if _, ferr := h.jobs.Fail(ctx, orgID, jobID, runErr); ferr != nil && !errors.Is(ferr, jobs.ErrTransition) {
			slog.ErrorContext(ctx, "import: record failure", "job", jobID, "error", ferr)
		}
		return
	}
	result.ReportKey = key
	if _, serr := h.jobs.Succeed(ctx, orgID, jobID, result); serr != nil && !errors.Is(serr, jobs.ErrTransition) {
		slog.ErrorContext(ctx, "import: record result", "job", jobID, "error", serr)
	}
}

// execute reads the version of the job: it checks document, version and
// profile again (clean before reading), reads the blob and parses it,
// reporting the progress every progressEvery rows. A fatal parser error
// ends it; a row error is reported and the run goes on. Mapping and
// writing follow with WP-775, WP-302 and WP-776.
func (h *ImportHandler) execute(ctx context.Context, orgID, jobID string, p *JobParams) (JobResult, *report, error) {
	rep := newReport()
	var result JobResult
	src, err := h.jobSource(ctx, orgID, p)
	if err != nil {
		return result, rep, err
	}
	body, err := h.openSource(ctx, orgID, src)
	if err != nil {
		if errors.Is(err, errForeignKey) {
			return result, rep, &ParseError{Code: "import-document-unavailable", Message: errForeignKey.Error(), Fatal: true}
		}
		return result, rep, err
	}
	defer func() { _ = body.Close() }()
	counted := &countingReader{r: body}
	rd, err := Open(src.format, counted, p.Sheet)
	if err != nil {
		return result, rep, err
	}
	defer func() { _ = rd.Close() }()
	for {
		if ctx.Err() != nil {
			return result, rep, ctx.Err()
		}
		_, rerr := rd.Next()
		if rerr != nil {
			if isEOF(rerr) {
				return result, rep, nil
			}
			var pe *ParseError
			if !errors.As(rerr, &pe) || pe.Fatal {
				return result, rep, rerr
			}
			result.RowsFailed++
			rep.add(pe)
			result.RowsTotal++
			continue
		}
		result.RowsTotal++
		if result.RowsTotal%progressEvery == 0 {
			if perr := h.jobs.Progress(ctx, orgID, jobID, progressOf(src, counted.n, result.RowsTotal)); perr != nil {
				if errors.Is(perr, jobs.ErrTransition) {
					// The job is no longer running: cancelled.
					return result, rep, errStopped
				}
				slog.WarnContext(ctx, "import: record progress", "job", jobID, "error", perr)
			}
		}
	}
}

// progressOf is the progress of a run (0–99): the bytes read of a CSV
// file, the rows read of a workbook (which is unpacked at once) against
// MaxRows.
func progressOf(src *source, read int64, rows int) int {
	pct := rows * 100 / MaxRows
	if src.format == FormatCSV && src.version.Size > 0 {
		pct = int(read * 100 / src.version.Size)
	}
	return max(0, min(99, pct))
}

// jobSource checks the source of a job again: the document in the scope
// of the creator, exactly the version of the job, scanned clean, within
// the size limit and of a readable type, and the profile of the job.
func (h *ImportHandler) jobSource(ctx context.Context, orgID string, p *JobParams) (*source, error) {
	unavailable := func(msg string) error {
		return &ParseError{Code: "import-document-unavailable", Message: msg, Fatal: true}
	}
	doc, err := h.docs.GetByID(ctx, orgID, p.DocumentID)
	if err != nil {
		return nil, unavailable("the document is not found")
	}
	if _, err = h.profiles.Get(ctx, orgID, p.ProfileID); err != nil {
		return nil, &ParseError{Code: "import-profile-unavailable", Message: "the import profile is not found", Fatal: true}
	}
	v, err := h.docs.GetVersion(ctx, orgID, doc.ID, p.DocumentVersion)
	if err != nil || v.StorageKey == "" {
		return nil, unavailable("the document version is not found")
	}
	switch v.ScanStatus {
	case document.ScanClean:
	case document.ScanInfected:
		return nil, &ParseError{Code: "document-quarantined", Message: "the document version is quarantined", Fatal: true}
	default:
		return nil, &ParseError{Code: "document-pending-scan", Message: "the document version is not scanned clean", Fatal: true}
	}
	if v.Size > MaxFileBytes {
		return nil, &ParseError{Code: CodeFileTooLarge, Message: "the document version is larger than 50 MB", Fatal: true}
	}
	format, ok := FormatOfMime(v.MimeType)
	if !ok {
		return nil, &ParseError{Code: CodeFormatUnsupported, Message: "the document version is no CSV or XLSX file", Fatal: true}
	}
	return &source{doc: doc, version: v, format: format}, nil
}

// countingReader counts the bytes read.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }
