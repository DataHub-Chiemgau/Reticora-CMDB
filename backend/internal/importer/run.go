package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Outcome is the result of one row.
type Outcome string

// Outcomes of a row; they are also the codes of the report lines of rows
// without error.
const (
	OutcomeCreated Outcome = "created"
	OutcomeUpdated Outcome = "updated"
	OutcomeSkipped Outcome = "skipped"
	OutcomeFailed  Outcome = "failed"
)

// RowResult is what a writer reports for a row: the outcome, a message for
// the report line of a written or skipped row and the issues of a failed
// row (its report lines). The issues of a written or skipped row are notes
// of it (fields kept or left out, reviews opened, WP-302) and follow its
// outcome line.
type RowResult struct {
	Outcome Outcome
	Message string
	Issues  []Issue
}

// WriteRow is a mapped row handed to a writer with its run.
type WriteRow struct {
	Line   int
	Values *MappedRow
	Run    *RunInfo
}

// Writer writes the rows of one entity (ci: WP-302; location: this
// package; asset, contract, consumable, relationship: WP-776; subnet and
// ip_address: WP-920). Write runs
// in the transaction of the row (the context carries it, so the
// repositories join it); a failed row and a dry run roll it back. An
// error is a failure the report does not explain (write_failed); a refusal
// is a RowResult with outcome failed and its issues. The modes
// create_missing, update_existing and both (RunInfo.Mode) apply to every
// writer alike.
type Writer interface {
	Entity() string
	Write(ctx context.Context, row *WriteRow) (RowResult, error)
}

// Finisher is the optional part of a writer that runs once after the last
// row, outside the row transactions (e.g. links between rows).
type Finisher interface {
	Finish(ctx context.Context, run *RunInfo) error
}

// RunInfo describes the run a row belongs to; State keeps the per-run
// state of the writers (caches).
type RunInfo struct {
	JobID          string
	OrganizationID string
	Entity         string
	Mode           string
	DryRun         bool
	Profile        *Profile
	InitiatedBy    string
	// StartedAt is the start of the run: the observation time of the
	// values a row does not date itself (WP-302).
	StartedAt time.Time

	mu    sync.Mutex
	state map[any]any
}

// State returns the value of key for this run, created with init on first
// use.
func (r *RunInfo) State(key any, init func() any) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		r.state = map[any]any{}
	}
	v, ok := r.state[key]
	if !ok {
		v = init()
		r.state[key] = v
	}
	return v
}

// RowTx runs fn in one transaction of the request tenant whose context the
// repositories join (database.WithRequestTenantTx, K20). PGRepository
// implements it; without it (in-memory profile) rows run without a
// transaction and a dry run is refused.
type RowTx interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// ModuleGate is the release gate of the modules (server.ModuleSet).
type ModuleGate interface {
	RouteAllowed(pattern string) bool
}

// FeatureGate is the org gate of the features (entitlement.Service).
type FeatureGate interface {
	Status(ctx context.Context, orgID, featureKey string) (entitlement.Entitlement, entitlement.Status, error)
}

// JobQueue submits the import jobs to the job queue on NATS (TEC-14);
// jobs.Queue implements it.
type JobQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// EntityPaths are the REST paths of the entities of a profile: module and
// feature of an import follow from them through the tables of the release
// and the org gate.
var EntityPaths = map[string]string{
	"ci":           "/api/v1/cis",
	"location":     "/api/v1/locations",
	"asset":        "/api/v1/assets",
	"contract":     "/api/v1/contracts",
	"consumable":   "/api/v1/consumables",
	"relationship": "/api/v1/relationships",
	// IPAM (IPM-09, WP-920): module and feature ipam.
	"subnet":     "/api/v1/subnets",
	"ip_address": "/api/v1/ips",
}

// EntityWritePermissions are the write rights of the entities (the right
// of the create route, WP-245); starting an import needs it beside
// import:run.
var EntityWritePermissions = map[string]identity.Permission{
	"ci":           identity.PermCIWrite,
	"location":     identity.PermLocationWrite,
	"asset":        identity.PermAssetWrite,
	"contract":     identity.PermContractWrite,
	"consumable":   identity.PermStockWrite,
	"relationship": identity.PermRelationshipWrite,
	"subnet":       identity.PermIPAMWrite,
	"ip_address":   identity.PermIPAMWrite,
}

// rowProgressEvery is the number of rows after which a run reports its
// progress (and notices a cancel).
const rowProgressEvery = 100

// errRolledBack ends the transaction of a failed row or of a dry run.
var errRolledBack = errors.New("import row rolled back")

// directTx runs a row without a transaction (in-memory profile).
type directTx struct{}

func (directTx) RunInTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func fatalCode(code, format string, args ...any) *ParseError {
	return &ParseError{Code: code, Message: fmt.Sprintf(format, args...), Fatal: true}
}

// checkGates checks module and features of the entity: the module of its
// REST path must be on (module_disabled), the feature of that path and the
// feature import entitled (feature-not-entitled, K12). A failed lookup of
// a feature is not entitled (fail-closed).
func (h *ImportHandler) checkGates(ctx context.Context, orgID, entity string) error {
	path, ok := EntityPaths[entity]
	if !ok {
		return fatalCode(CodeEntityNotSupported, "the entity %s cannot be imported", entity)
	}
	if h.modules != nil && !h.modules.RouteAllowed(path) {
		return fatalCode(CodeModuleDisabled, "the module of %s is switched off", path)
	}
	if h.features == nil {
		return nil
	}
	keys := entitlement.RequiredFeatures(http.MethodPost, path)
	if !slices.Contains(keys, entitlement.FeatureImport) {
		keys = append(slices.Clone(keys), entitlement.FeatureImport)
	}
	for _, key := range keys {
		_, status, err := h.features.Status(ctx, orgID, key)
		if err != nil {
			slog.WarnContext(ctx, "import: read entitlement", "feature", key, "error", err)
		}
		if err != nil || status != entitlement.StatusActive {
			return fatalCode(CodeFeatureNotEntitled, "the organization is not entitled to the feature %s", key)
		}
	}
	return nil
}

// precheck checks the run before its first row: a writer of the entity,
// the write right of the starter, module and features, and the dry run
// over a transactional store. A failed check writes no row.
func (h *ImportHandler) precheck(ctx context.Context, orgID string, profile *Profile, dryRun bool) (Writer, error) {
	w, ok := h.writers[profile.Entity]
	if !ok {
		return nil, fatalCode(CodeEntityNotSupported, "no import writer is registered for the entity %s", profile.Entity)
	}
	p, ok := identity.PrincipalFromContext(ctx)
	if !ok || !p.Has(EntityWritePermissions[profile.Entity]) {
		return nil, fatalCode(CodeForbidden, "the user who started the import lacks %s", EntityWritePermissions[profile.Entity])
	}
	if err := h.checkGates(ctx, orgID, profile.Entity); err != nil {
		return nil, err
	}
	if dryRun && !h.transactional {
		return nil, fatalCode(CodeDryRunUnavailable, "a dry run needs the database")
	}
	return w, nil
}

// pass is one reading of the file of a run.
type pass struct {
	h      *ImportHandler
	orgID  string
	jobID  string
	src    *source
	sheet  string
	weight int // progress share of the pass (percent)
	base   int // progress before the pass
}

// read reads the file once and calls row for every data row (or row
// error); it reports the progress every rowProgressEvery rows and stops
// with errStopped once the job is no longer running. A fatal parser error
// ends it. header is called with the header of the file first.
func (p *pass) read(ctx context.Context, total int, header func([]string), row func(Row, error) error) error {
	body, err := p.h.openSource(ctx, p.orgID, p.src)
	if err != nil {
		if errors.Is(err, errForeignKey) {
			return &ParseError{Code: "import-document-unavailable", Message: errForeignKey.Error(), Fatal: true}
		}
		return err
	}
	defer func() { _ = body.Close() }()
	counted := &countingReader{r: body}
	rd, err := Open(p.src.format, counted, p.sheet)
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	if header != nil {
		header(rd.Header())
	}
	n := 0
	for {
		if endErr := runEnded(ctx); endErr != nil {
			return endErr
		}
		r, rerr := rd.Next()
		if rerr != nil {
			if isEOF(rerr) {
				return nil
			}
			var pe *ParseError
			if !errors.As(rerr, &pe) || pe.Fatal {
				return rerr
			}
		}
		n++
		if rowErr := row(r, rerr); rowErr != nil {
			return rowErr
		}
		if n%rowProgressEvery == 0 {
			pct := progressOf(p.src, counted.n, n)
			if total > 0 {
				pct = n * 100 / total
			}
			if progErr := p.h.progress(ctx, p.orgID, p.jobID, p.base+min(99, pct)*p.weight/100); progErr != nil {
				return progErr
			}
		}
	}
}

// runEnded returns errStopped when the run context was cancelled with the
// job (queue run context), the context error on a timeout, else nil.
func runEnded(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	if errors.Is(context.Cause(ctx), jobs.ErrCancelled) {
		return errStopped
	}
	return ctx.Err()
}

// progress records the progress; a job no longer running (cancelled, or
// failed by the reaper) stops the run.
func (h *ImportHandler) progress(ctx context.Context, orgID, jobID string, pct int) error {
	if err := h.jobs.Progress(context.WithoutCancel(ctx), orgID, jobID, max(0, min(99, pct))); err != nil {
		if errors.Is(err, jobs.ErrTransition) {
			return errStopped
		}
		slog.WarnContext(ctx, "import: record progress", "job", jobID, "error", err)
	}
	return nil
}

// execute runs an import job in ctx, the context of its starter (tenant,
// scope, principal, request info with source import): it checks document,
// version and profile again, the prechecks, counts the rows in a first
// reading (import-row-limit before any write) and then maps and writes
// every row in its own transaction. A fatal error ends it; a row error is
// reported and the run goes on (partial failure).
func (h *ImportHandler) execute(ctx context.Context, job *jobs.Job, p *JobParams, rep *report) (*RunInfo, JobResult, error) {
	orgID := job.OrganizationID
	result := JobResult{DryRun: p.DryRun}
	src, err := h.jobSource(ctx, orgID, p)
	if err != nil {
		return nil, result, err
	}
	run := &RunInfo{JobID: job.ID, OrganizationID: orgID, Entity: src.profile.Entity, Mode: p.Mode, DryRun: p.DryRun,
		Profile: src.profile, InitiatedBy: job.CreatedBy, StartedAt: time.Now().UTC()}
	if job.StartedAt != nil {
		run.StartedAt = job.StartedAt.UTC()
	}
	writer, err := h.precheck(ctx, orgID, src.profile, p.DryRun)
	if err != nil {
		return run, result, err
	}
	// First reading: the row limit (E-49) and the fatal parser errors
	// before the first write.
	total := 0
	first := &pass{h: h, orgID: orgID, jobID: job.ID, src: src, sheet: p.Sheet, weight: 30}
	if countErr := first.read(ctx, 0, nil, func(Row, error) error { total++; return nil }); countErr != nil {
		return run, result, countErr
	}
	// Second reading: mapping and rows.
	second := &pass{h: h, orgID: orgID, jobID: job.ID, src: src, sheet: p.Sheet, weight: 70, base: 30}
	var mp *mapper
	header := func(cols []string) {
		rep.withHeader(cols)
		var issues []Issue
		mp, issues = newMapper(&src.profile.Mapping, cols)
		for i := range issues {
			rep.addIssue(&issues[i])
		}
	}
	err = second.read(ctx, total, header, func(r Row, rerr error) error {
		result.Total++
		if rerr != nil {
			var pe *ParseError
			errors.As(rerr, &pe)
			result.Failed++
			rep.add(pe)
			return nil
		}
		res := h.processRow(ctx, writer, run, mp, &r)
		result.count(res.Outcome)
		writeOutcome(rep, r.Line, &res)
		return nil
	})
	if err != nil {
		return run, result, err
	}
	if f, ok := writer.(Finisher); ok && !p.DryRun {
		if ferr := f.Finish(context.WithoutCancel(ctx), run); ferr != nil {
			return run, result, ferr
		}
	}
	return run, result, nil
}

// count adds an outcome to the counters.
func (r *JobResult) count(o Outcome) {
	switch o {
	case OutcomeCreated:
		r.Created++
	case OutcomeUpdated:
		r.Updated++
	case OutcomeSkipped:
		r.Skipped++
	default:
		r.Failed++
	}
}

// writeOutcome writes the report lines of a row: its issues, or one line
// with its outcome.
func writeOutcome(rep *report, line int, res *RowResult) {
	if res.Outcome == OutcomeFailed {
		for i := range res.Issues {
			is := res.Issues[i]
			is.Line = line
			rep.addIssue(&is)
		}
		return
	}
	rep.addIssue(&Issue{Line: line, Code: string(res.Outcome), Message: res.Message})
	for i := range res.Issues {
		is := res.Issues[i]
		is.Line = line
		rep.addIssue(&is)
	}
}

// processRow maps a row and writes it in its own transaction: a failed row
// and a dry run roll it back, so a dry run reports what the real run does
// and writes nothing (no review, no event of the row, no index call:
// database.AfterCommit callbacks are dropped with the rollback). The row
// is not interrupted by a cancel; the run stops before the next one.
func (h *ImportHandler) processRow(ctx context.Context, w Writer, run *RunInfo, mp *mapper, r *Row) RowResult {
	mapped, issues := mp.apply(r)
	if len(issues) > 0 {
		return RowResult{Outcome: OutcomeFailed, Issues: issues}
	}
	row := &WriteRow{Line: r.Line, Values: mapped, Run: run}
	var res RowResult
	var werr error
	txErr := h.tx.RunInTx(context.WithoutCancel(ctx), run.OrganizationID, func(ctx context.Context) error {
		res, werr = w.Write(ctx, row)
		switch {
		case werr != nil:
			return werr
		case res.Outcome == OutcomeFailed, res.Outcome == "":
			return errRolledBack
		case run.DryRun:
			return errRolledBack
		}
		return nil
	})
	if werr == nil && (txErr == nil || errors.Is(txErr, errRolledBack)) {
		if res.Outcome == "" || (res.Outcome == OutcomeFailed && len(res.Issues) == 0) {
			return RowResult{Outcome: OutcomeFailed, Issues: []Issue{{Code: CodeWriteFailed, Message: "the row could not be written"}}}
		}
		return res
	}
	cause := werr
	if cause == nil {
		cause = txErr
	}
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) && pgErr.Code == "42501" {
		// Row level security refused the row: outside the starter's scope.
		return RowResult{Outcome: OutcomeFailed, Issues: []Issue{{Code: CodeOutOfScope,
			Message: "the row concerns an object outside the scope of the user who started the import"}}}
	}
	slog.ErrorContext(ctx, "import: write row", "job", run.JobID, "line", r.Line, "error", cause)
	return RowResult{Outcome: OutcomeFailed, Issues: []Issue{{Code: CodeWriteFailed, Message: "the row could not be written"}}}
}

// storeReport writes the report outside any transaction (K20).
func (h *ImportHandler) storeReport(ctx context.Context, orgID, jobID string, rep *report) (string, error) {
	key := ReportKey(orgID, jobID)
	content := rep.bytes()
	if err := h.blobs.Put(ctx, export.ExportBucket, key, bytes.NewReader(content), int64(len(content)), "text/csv"); err != nil {
		return "", err
	}
	return key, nil
}

// emitCompleted reports import.completed after the job succeeded, also
// for a dry run.
func (h *ImportHandler) emitCompleted(ctx context.Context, job *jobs.Job, run *RunInfo, result *JobResult) {
	if h.events == nil || run == nil {
		return
	}
	data := events.ImportCompletedData{ID: job.ID, ProfileID: run.Profile.ID, Entity: run.Entity, Mode: run.Mode,
		DryRun: result.DryRun, Total: result.Total, Created: result.Created, Updated: result.Updated,
		Skipped: result.Skipped, Failed: result.Failed, InitiatedBy: job.CreatedBy}
	if err := h.events.Emit(ctx, &events.Event{OrgID: job.OrganizationID, Type: events.ImportCompleted, EntityID: job.ID, Data: data}); err != nil {
		slog.ErrorContext(ctx, "import: emit import.completed", "job", job.ID, "error", err)
	}
}
