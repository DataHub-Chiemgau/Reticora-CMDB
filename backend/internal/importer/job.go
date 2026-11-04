package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// JobKind is the job kind of an import run (API-09, JOB-04).
const JobKind = "import.run"

// RunTimeout bounds a run; it ends before the reaper (jobs.reap) fails the
// job after the timeout of its kind.
const RunTimeout = 30 * time.Minute

// runSlots lets at most two runs of this instance parse at the same time;
// further runs wait (in process: with their job queued).
var runSlots = make(chan struct{}, 2)

// JobParams are the parameters of an import job: the document version it
// reads (a later version is not read), the profile, the write mode, the
// dry run flag, the sheet of an XLSX file (empty: the first) and the
// request facts of the audit (jobs.ScopeParams). They hold no rights,
// scopes or IdP groups of the starter: every reader of the job sees its
// parameters (GET /api/v1/jobs, job:read), and the run reloads the
// starter's current rights instead (jobContext).
type JobParams struct {
	DocumentID      string `json:"document_id"`
	DocumentVersion int    `json:"document_version"`
	ProfileID       string `json:"profile_id"`
	Mode            string `json:"mode"`
	DryRun          bool   `json:"dry_run"`
	Sheet           string `json:"sheet,omitempty"`
	jobs.ScopeParams
}

// UserStatus reports whether a user exists in the organization and is
// active (user.PGRepository, as the session refresh asks it).
type UserStatus interface {
	UserActive(ctx context.Context, orgID, userID string) (bool, error)
}

// GrantSource reads the role and custom role assignments of a user
// (permission.PGRepository, as login and refresh resolve them).
type GrantSource interface {
	AccessGrants(ctx context.Context, orgID, userID string) ([]identity.Grant, error)
}

// JobResult is the result of an import job: the blob key of the report,
// whether it was a dry run, the data rows read and the rows per outcome.
type JobResult struct {
	ReportKey string `json:"report_key"`
	DryRun    bool   `json:"dry_run"`
	Total     int    `json:"total"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Skipped   int    `json:"skipped"`
	Failed    int    `json:"failed"`
}

// ReportKey is the fixed blob key of the report of an import job in the
// bucket export.ExportBucket.
func ReportKey(orgID, jobID string) string {
	return blob.OrgKey(orgID, "imports", jobID, "report.csv")
}

// errStopped ends a run that was cancelled: it writes no further row.
var errStopped = errors.New("import cancelled")

// codePattern is the form of an error code at the start of job.error.
var codePattern = regexp.MustCompile(`^([a-z][a-z_-]*[a-z]): `)

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

// jobContext restores the starter of a job in ctx (fail-closed): the
// creator of the job row must be an active user of the organization, the
// job must have been started by a session (source ui of its request
// facts) and hold a scope snapshot. The principal is rebuilt from the
// creator's current role assignments (GrantSource, identity.ResolveAccess
// as at login and refresh), so a right revoked after the start takes
// effect; its scope and the tenant scope of the run are the current scope
// narrowed to the snapshot of the job row (jobs.CreatorContext), so the run
// never reaches beyond the request that started it. Standard roles of IdP
// groups are not part of it: the groups of the session are not stored. The
// request info of the audit carries source import and the job as
// correlation id (AUD-07).
func (h *ImportHandler) jobContext(ctx context.Context, job *jobs.Job, p *JobParams) (context.Context, error) {
	missing := &ParseError{Code: CodePrincipalMissing, Message: "the user who started the import cannot be restored", Fatal: true}
	if job.CreatedBy == "" || job.Scope == nil || p.Source != audit.SourceUI || h.users == nil || h.grants == nil {
		// An API key acts with the intersection of its own permissions
		// and its owner's: neither is in the job, so it is not restored.
		return nil, missing
	}
	active, err := h.users.UserActive(ctx, job.OrganizationID, job.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("read the status of the user who started the import: %w", err)
	}
	if !active {
		return nil, missing
	}
	grants, err := h.grants.AccessGrants(ctx, job.OrganizationID, job.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("read the role assignments of the user who started the import: %w", err)
	}
	access := identity.ResolveAccess(grants)
	scope := narrowScope(&access.Scope, job.Scope)
	var permScopes map[identity.Permission]identity.Scope
	for perm := range access.PermissionScopes {
		if permScopes == nil {
			permScopes = make(map[identity.Permission]identity.Scope, len(access.PermissionScopes))
		}
		held := access.PermissionScopes[perm]
		permScopes[perm] = narrowScope(&held, job.Scope)
	}
	request := jobs.ScopeParams{RequestID: p.RequestID, Source: audit.SourceImport}
	runCtx, err := request.Context(ctx, job)
	if err != nil {
		return nil, missing
	}
	tenantScope := database.OrgWideScope(job.OrganizationID, job.CreatedBy)
	tenantScope.Clients, tenantScope.Sites, tenantScope.Teams = dbSet(scope.Clients), dbSet(scope.Sites), dbSet(scope.Teams)
	runCtx = tenant.WithTenant(runCtx, tenant.TenantInfo{OrganizationID: job.OrganizationID, ClientID: scope.LegacyClientScope(),
		UserID: job.CreatedBy})
	runCtx = database.ContextWithTenantScope(runCtx, &tenantScope)
	principal := identity.Principal{Subject: job.CreatedBy, OrganizationID: job.OrganizationID, Type: identity.PrincipalTypeUser,
		ClientScope: scope.LegacyClientScope(), Scope: &scope, PermissionScopes: permScopes, Permissions: access.Permissions}
	runCtx = identity.WithPrincipal(runCtx, principal)
	return identity.WithPermissions(runCtx, principal.Permissions), nil
}

// narrowScope is held limited to the scope snapshot of a job row: a
// dimension without snapshot ids (nil) stays as held, otherwise it keeps
// the snapshot ids held covers.
func narrowScope(held *identity.Scope, snap *jobs.Scope) identity.Scope {
	return identity.Scope{Clients: narrowSet(held.Clients, snap.Clients), Sites: narrowSet(held.Sites, snap.Sites),
		Teams: narrowSet(held.Teams, snap.Teams)}
}

func narrowSet(held identity.ScopeSet, snap []string) identity.ScopeSet {
	if snap == nil {
		return identity.ScopeSet{All: held.All, IDs: append([]string(nil), held.IDs...)}
	}
	out := identity.ScopeSet{IDs: []string{}}
	for _, id := range snap {
		if held.All || slices.Contains(held.IDs, id) {
			out.IDs = append(out.IDs, id)
		}
	}
	return out
}

// dbSet is the database form of a scope dimension.
func dbSet(s identity.ScopeSet) database.ScopeSet {
	if s.All {
		return database.AllScopes()
	}
	return database.ScopeIDs(s.IDs...)
}

// run executes an import job in this process (without job queue): it
// waits for a parse slot (the job stays queued) and claims the job. The
// run restores its starter from the job (jobContext), so it reads and
// writes under the starter's row level security
// (database.WithRequestTenant of the repositories), never with WithSystem.
func (h *ImportHandler) run(ctx context.Context, orgID, jobID string) {
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
	if ferr := h.perform(ctx, claimed); ferr != nil {
		if _, err = h.jobs.Fail(ctx, orgID, jobID, ferr); err != nil && !errors.Is(err, jobs.ErrTransition) {
			slog.ErrorContext(ctx, "import: record failure", "job", jobID, "error", err)
		}
	}
}

// RunJob runs an import job of the job queue (handler of import.run,
// TEC-14). It records success, cancel and report itself; a fatal error is
// returned and fails the job (MaxAttempts 1: no second attempt, job.failed
// from the job repository, WP-550). The success is recorded already when
// it returns, so the queue's second record is refused as a transition and
// ignored.
func (h *ImportHandler) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	if h.jobs == nil || h.blobs == nil {
		return nil, &ParseError{Code: "import-failed", Message: "imports require the job service and blob storage", Fatal: true}
	}
	runSlots <- struct{}{}
	defer func() { <-runSlots }()
	if err := h.perform(ctx, job); err != nil {
		return nil, err
	}
	return nil, nil
}

// perform runs a claimed job and records its outcome except a failure,
// which it returns: the report is stored first (outside any transaction,
// K20), then a success and import.completed after it, or a cancel with the
// counters of the rows processed so far (their report is kept). A panic of
// the parser on a hostile file fails the job instead of the process.
func (h *ImportHandler) perform(ctx context.Context, job *jobs.Job) (failure error) {
	orgID, jobID := job.OrganizationID, job.ID
	rep := newReport()
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "import: run panicked", "job", jobID, "panic", rec)
			failure = &ParseError{Code: "import-failed", Message: "the import could not be completed", Fatal: true}
		}
	}()
	var p JobParams
	var run *RunInfo
	var result JobResult
	var err error
	if json.Unmarshal(job.Params, &p) != nil {
		err = &ParseError{Code: "import-failed", Message: "the import job has malformed parameters", Fatal: true}
	}
	result.DryRun = p.DryRun
	var runCtx context.Context
	if err == nil {
		runCtx, err = h.jobContext(ctx, job, &p)
	}
	if err == nil {
		timed, cancel := context.WithTimeout(runCtx, RunTimeout)
		run, result, err = h.execute(timed, job, &p, rep)
		if errors.Is(err, context.DeadlineExceeded) || (err == nil && errors.Is(timed.Err(), context.DeadlineExceeded)) {
			err = &ParseError{Code: "import-timeout", Message: fmt.Sprintf("the import did not end within %s", RunTimeout), Fatal: true}
		}
		cancel()
	}
	// The records go with a context detached from the run: a cancel of the
	// queue's run context must not stop them.
	rec := context.WithoutCancel(ctx)
	cancelled := errors.Is(err, errStopped)
	if !cancelled {
		if c, cerr := h.jobs.IsCancelled(rec, orgID, jobID); cerr == nil && c {
			cancelled = true
		}
	}
	if cancelled {
		key, serr := h.storeReport(rec, orgID, jobID, rep)
		if serr != nil {
			slog.ErrorContext(ctx, "import: write report", "job", jobID, "error", serr)
		}
		result.ReportKey = key
		if _, serr = h.jobs.SaveCancelledResult(rec, orgID, jobID, result); serr != nil && !errors.Is(serr, jobs.ErrTransition) {
			slog.ErrorContext(ctx, "import: record cancelled result", "job", jobID, "error", serr)
		}
		return nil
	}
	if err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) {
			slog.ErrorContext(ctx, "import: run failed", "job", jobID, "error", err)
			pe = &ParseError{Code: "import-failed", Message: "the import could not be completed", Fatal: true}
			err = pe
		}
		rep.add(pe)
	}
	key, perr := h.storeReport(rec, orgID, jobID, rep)
	if perr != nil {
		slog.ErrorContext(ctx, "import: write report", "job", jobID, "error", perr)
		if err == nil {
			err = &ParseError{Code: "import-failed", Message: "the import report could not be stored", Fatal: true}
		}
	}
	if err != nil {
		return err
	}
	result.ReportKey = key
	succeeded, serr := h.jobs.Succeed(rec, orgID, jobID, result)
	if serr != nil {
		if !errors.Is(serr, jobs.ErrTransition) {
			slog.ErrorContext(ctx, "import: record result", "job", jobID, "error", serr)
		}
		return nil
	}
	h.emitCompleted(rec, succeeded, run, &result)
	return nil
}

// progressOf is the progress of a reading (0–99): the bytes read of a CSV
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
	profile, err := h.profiles.Get(ctx, orgID, p.ProfileID)
	if err != nil {
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
	return &source{doc: doc, version: v, format: format, profile: profile}, nil
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
