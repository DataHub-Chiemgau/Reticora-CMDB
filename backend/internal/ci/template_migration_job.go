package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The job template.migrate (WP-712, API-09, JOB-04): POST
// /api/v1/templates/{id}/migrations queues the migration of the CIs whose
// assignment (ci_template) names the template on from_version. A worker of
// the job queue (NATS) or, without NATS, the accepting process runs it in
// blocks of templateMigrationBlock CIs, keyset over ci.id, with the trigger
// restored (jobs.ScopeParams): row level security shows it only the
// trigger's CIs, and the trigger is the actor of every change. Each block is
// one transaction; each CI runs in it under a savepoint around
// TemplateMigrator.MigrateTemplate (WP-253) through the indexing decorator,
// so a failing CI is discarded alone. Between the blocks the job records
// its progress and stops when it was cancelled. The report is the job
// result. MigrateTemplate skips CIs no longer on from_version, so a retry or
// a redelivery writes nothing twice. There is no revert (E-25).

// Limits and names of the template migration job.
const (
	TemplateMigrateJobKind = "template.migrate"
	// MaxTemplateMigrationIDs bounds ci_ids like a bulk request (API-08).
	MaxTemplateMigrationIDs = MaxBulkIDs
	// templateMigrationBlock is the number of CIs of one block
	// transaction.
	templateMigrationBlock = 100
	// MaxTemplateMigrationItems bounds the items of the report; beyond it
	// the report is truncated.
	MaxTemplateMigrationItems = 10000
	// TemplateMigrationFailed is the report outcome of a CI whose
	// migration failed and was discarded.
	TemplateMigrationFailed = "failed"
)

// Errors of the template migration endpoint and job.
var (
	// ErrTemplateMigrationUnavailable: the migration needs PostgreSQL, the
	// template store and the job service (503).
	ErrTemplateMigrationUnavailable = errors.New("the template migration needs the database and the job service")
	// errTemplateMigrationStopped: the job was cancelled (or ended
	// otherwise) while it ran; it records nothing further.
	errTemplateMigrationStopped = errors.New("template migration job stopped")
)

// TemplateMigrationRequest is the body of POST
// /api/v1/templates/{id}/migrations. Without ci_ids and where every CI of
// the trigger with the assignment (template, from_version) is migrated;
// ci_ids or where narrow the selection, never both.
type TemplateMigrationRequest struct {
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	CIIDs       *[]string       `json:"ci_ids,omitempty"`
	Where       *savedview.Expr `json:"where,omitempty"`
	DryRun      bool            `json:"dry_run"`
}

// templateMigrationParams are the job parameters: the request, the
// template and the request facts of the trigger (jobs.ScopeParams; the
// scope itself is the snapshot of the job row).
type templateMigrationParams struct {
	jobs.ScopeParams
	TemplateID  string          `json:"template_id"`
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	CIIDs       []string        `json:"ci_ids,omitempty"`
	Where       *savedview.Expr `json:"where,omitempty"`
	DryRun      bool            `json:"dry_run"`
}

// TemplateMigrationReport is the result of a template.migrate job
// (TemplateMigrationReport in api/openapi.yaml). Items lists only the CIs
// with a change, a finding, an override or an error, at most
// MaxTemplateMigrationItems; beyond that Truncated is set.
type TemplateMigrationReport struct {
	DryRun    bool                    `json:"dry_run"`
	Totals    TemplateMigrationTotals `json:"totals"`
	Items     []TemplateMigrationItem `json:"items"`
	Truncated bool                    `json:"truncated"`
}

// TemplateMigrationTotals are the sums of a report: Selected is the number
// of CIs selected when the run started; Skipped also counts the ci_ids
// without the assignment (template, from_version).
type TemplateMigrationTotals struct {
	Selected     int `json:"selected"`
	Migrated     int `json:"migrated"`
	Skipped      int `json:"skipped"`
	Failed       int `json:"failed"`
	WithFindings int `json:"with_findings"`
}

// TemplateMigrationItem is the report of one CI.
type TemplateMigrationItem struct {
	CIID       string                  `json:"ci_id"`
	Outcome    string                  `json:"outcome"`
	Defaults   []string                `json:"defaults"`
	Overridden []string                `json:"overridden"`
	Findings   []Violation             `json:"findings"`
	Error      *TemplateMigrationError `json:"error,omitempty"`
}

// TemplateMigrationError names why the migration of a CI failed: a code
// (unique, validation, not_found, internal) and the attribute.
type TemplateMigrationError struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
}

// TemplateVersions reads a template and its versions (WP-251);
// citype.PGRepository implements it.
type TemplateVersions interface {
	GetTemplate(ctx context.Context, orgID, id string) (*citype.Template, error)
	GetTemplateVersion(ctx context.Context, orgID, id string, version int) (*citype.TemplateVersion, error)
}

// TemplateMigrationJobService runs the jobs of the migration; jobs.Service
// implements it. Without NATS the accepting process runs them on it, with
// NATS the queue's worker; progress and cancel go through it either way.
type TemplateMigrationJobService interface {
	Enqueue(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error)
	Succeed(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
	Fail(ctx context.Context, orgID, id string, cause error) (*jobs.Job, error)
	Progress(ctx context.Context, orgID, id string, percent int) error
	IsCancelled(ctx context.Context, orgID, id string) (bool, error)
}

// TemplateMigrationQueue submits the jobs to the job queue on NATS
// (TEC-14); jobs.Queue implements it.
type TemplateMigrationQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// TemplateMigrationJobs is the endpoint and the executor of the job
// template.migrate. repo is the CI repository of the server, decorated by
// bootstrap.NewCIRepository (WP-418), so the migrated CIs are indexed.
type TemplateMigrationJobs struct {
	repo      Repository
	templates TemplateVersions
	fields    TemplateFieldResolver
	jobs      TemplateMigrationJobService
	queue     TemplateMigrationQueue
	now       func() time.Time
}

// NewTemplateMigrationJobs creates the endpoint and executor on the CI
// repository repo and the metamodel repository types (template store and
// TemplateFieldResolver; citype.PGRepository). Without the job service
// (WithJobs) or the database the endpoint answers 503.
func NewTemplateMigrationJobs(repo Repository, types any) *TemplateMigrationJobs {
	m := &TemplateMigrationJobs{repo: repo, now: time.Now}
	m.templates, _ = types.(TemplateVersions)
	m.fields, _ = types.(TemplateFieldResolver)
	return m
}

// WithJobs sets the job service: progress, cancel and, without a queue,
// the in-process run.
func (m *TemplateMigrationJobs) WithJobs(svc TemplateMigrationJobService) *TemplateMigrationJobs {
	m.jobs = svc
	return m
}

// WithQueue submits the jobs to the job queue on NATS instead of running
// them in this process.
func (m *TemplateMigrationJobs) WithQueue(q TemplateMigrationQueue) *TemplateMigrationJobs {
	m.queue = q
	return m
}

// migrator returns the migration and transaction parts of the repository;
// false when the repository lacks them (in-memory mode).
func (m *TemplateMigrationJobs) migrator() (TemplateMigrator, TxRunner, bool) {
	if m == nil || m.repo == nil {
		return nil, nil, false
	}
	migrator, ok := m.repo.(TemplateMigrator)
	if !ok {
		return nil, nil, false
	}
	runner, ok := m.repo.(TxRunner)
	if !ok {
		return nil, nil, false
	}
	return migrator, runner, true
}

func (m *TemplateMigrationJobs) available() bool {
	_, _, ok := m.migrator()
	return ok && m.templates != nil && m.fields != nil && m.jobs != nil
}

// RegisterRoutes registers POST /api/v1/templates/{id}/migrations.
func (m *TemplateMigrationJobs) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/templates/{id}/migrations", m.Create)
}

// validate normalizes and checks a migration request.
//
// validation: the rule texts of the request fields and of the filter AST
// (API-08, SRC-03).
func (req *TemplateMigrationRequest) validate() error {
	unprocessable := func(field, msg string) error {
		return &api.FieldError{Field: field, Message: msg, Status: http.StatusUnprocessableEntity}
	}
	switch {
	case req.FromVersion < 1:
		return unprocessable("from_version", "must be a version of the template (1 or more)")
	case req.ToVersion < 1:
		return unprocessable("to_version", "must be a version of the template (1 or more)")
	case req.ToVersion == req.FromVersion:
		return unprocessable("to_version", "must differ from from_version")
	case req.CIIDs != nil && req.Where != nil:
		return unprocessable("ci_ids", "ci_ids and where cannot be combined")
	}
	if req.CIIDs != nil {
		seen := map[string]bool{}
		ids := make([]string, 0, len(*req.CIIDs))
		for _, id := range *req.CIIDs {
			id = strings.ToLower(strings.TrimSpace(id))
			if !api.IsUUID(id) {
				return unprocessable("ci_ids", "every entry must be the ID (UUID) of a CI")
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		switch {
		case len(ids) == 0:
			return unprocessable("ci_ids", "at least one id is required")
		case len(ids) > MaxTemplateMigrationIDs:
			return unprocessable("ci_ids", fmt.Sprintf("at most %d ids per request", MaxTemplateMigrationIDs))
		}
		*req.CIIDs = ids
	}
	if req.Where != nil {
		if err := req.Where.Validate("ci"); err != nil {
			var ae *savedview.ASTError
			if errors.As(err, &ae) {
				return unprocessable("where"+ae.Path, ae.Message)
			}
			return unprocessable("where", "not a valid filter (SRC-03)")
		}
	}
	return nil
}

// checkVersions refuses a migration whose versions do not fit (422): the
// target must be published, the source published or retired.
//
// validation: fixed rule texts of the version states (MET-53).
func checkVersions(from, to *citype.TemplateVersion) error {
	switch {
	case to.Status != citype.TemplateVersionPublished:
		return &api.FieldError{Field: "to_version", Message: "the target version must be published", Status: http.StatusUnprocessableEntity}
	case from.Status != citype.TemplateVersionPublished && from.Status != citype.TemplateVersionRetired:
		return &api.FieldError{Field: "from_version", Message: "the source version must be published or retired",
			Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// versionOf reads one version of the template; an unknown version is a
// 422 on field.
func (m *TemplateMigrationJobs) versionOf(ctx context.Context, orgID, templateID string, version int, field string) (*citype.TemplateVersion, error) {
	v, err := m.templates.GetTemplateVersion(ctx, orgID, templateID, version)
	if errors.Is(err, citype.ErrTemplateVersionNotFound) {
		return nil, &api.FieldError{Field: field, Message: "the template has no such version", Status: http.StatusUnprocessableEntity}
	}
	return v, err
}

// Create handles POST /api/v1/templates/{id}/migrations: it checks the
// template and the versions, queues the job template.migrate and answers
// 202 with Location /api/v1/jobs/{id}.
func (m *TemplateMigrationJobs) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if !m.available() {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrTemplateMigrationUnavailable.Error())
		return
	}
	templateID, ok := api.ParseUUIDParam(w, r, "id", citype.ErrTemplateNotFound.Error())
	if !ok {
		return
	}
	var req TemplateMigrationRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ctx := r.Context()
	if _, err := m.templates.GetTemplate(ctx, t.OrganizationID, templateID); err != nil {
		if errors.Is(err, citype.ErrTemplateNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", citype.ErrTemplateNotFound.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	if err := req.validate(); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	from, err := m.versionOf(ctx, t.OrganizationID, templateID, req.FromVersion, "from_version")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	to, err := m.versionOf(ctx, t.OrganizationID, templateID, req.ToVersion, "to_version")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if verr := checkVersions(from, to); verr != nil {
		api.WriteRepoError(w, verr)
		return
	}
	params := templateMigrationParams{ScopeParams: jobs.CaptureScope(ctx), TemplateID: templateID,
		FromVersion: req.FromVersion, ToVersion: req.ToVersion, Where: req.Where, DryRun: req.DryRun}
	if req.CIIDs != nil {
		params.CIIDs = *req.CIIDs
	}
	job, err := m.submit(ctx, t.OrganizationID, t.UserID, &params)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// submit queues the job: on the job queue with NATS, else in the job table
// and run in the background of this process (like runBulkJob). The job
// row keeps the trigger's scope snapshot (jobs.Service.Enqueue).
func (m *TemplateMigrationJobs) submit(ctx context.Context, orgID, userID string, params *templateMigrationParams) (*jobs.Job, error) {
	opts := jobs.EnqueueOptions{CreatedBy: userID}
	if m.queue != nil {
		job, _, err := m.queue.Submit(ctx, orgID, TemplateMigrateJobKind, params, opts)
		if err != nil {
			return nil, fmt.Errorf("submit template migration job: %w", err)
		}
		return job, nil
	}
	job, _, err := m.jobs.Enqueue(ctx, orgID, TemplateMigrateJobKind, params, opts)
	if err != nil {
		return nil, fmt.Errorf("enqueue template migration job: %w", err)
	}
	go m.runInProcess(context.WithoutCancel(ctx), orgID, job.ID)
	return job, nil
}

// runInProcess claims the job and runs it (without NATS): a run that
// succeeds records the report, a cancelled run records nothing, a failed
// attempt goes back to the queue after the backoff of the kind and is run
// again here while attempts remain. A lost run is failed by the reaper
// (jobs.reap).
func (m *TemplateMigrationJobs) runInProcess(ctx context.Context, orgID, jobID string) {
	claimed, err := m.jobs.ClaimJob(ctx, orgID, jobID)
	if err != nil || claimed == nil {
		if err != nil {
			slog.ErrorContext(ctx, "claim template migration job", "job", jobID, "error", err)
		}
		return
	}
	report, err := m.run(ctx, claimed)
	switch {
	case errors.Is(err, errTemplateMigrationStopped):
		return
	case err != nil:
		failed, ferr := m.jobs.Fail(ctx, orgID, jobID, err)
		if ferr != nil {
			slog.ErrorContext(ctx, "record template migration failure", "job", jobID, "error", ferr)
			return
		}
		if failed.Status == jobs.StatusQueued {
			time.AfterFunc(max(0, failed.RunAfter.Sub(m.now())), func() { m.runInProcess(ctx, orgID, jobID) })
		}
		return
	}
	if _, err = m.jobs.Succeed(ctx, orgID, jobID, report); err != nil && !errors.Is(err, jobs.ErrTransition) {
		slog.ErrorContext(ctx, "record template migration report", "job", jobID, "error", err)
	}
}

// RunJob runs a job of the job queue (handler of template.migrate). A job
// cancelled while it ran ends without result; the queue then records
// nothing (jobs.Queue.Process).
func (m *TemplateMigrationJobs) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	report, err := m.run(ctx, job)
	if errors.Is(err, errTemplateMigrationStopped) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return report, nil
}

// templateSelection is the selection of a job: the CIs with the
// assignment (template, from_version), narrowed by ids or a filter.
type templateSelection struct {
	params *templateMigrationParams
}

// sql returns the selection's FROM and WHERE clause with its arguments
// from $1 on.
func (s templateSelection) sql(orgID string) (from string, args []any, err error) {
	args = []any{orgID, s.params.TemplateID, s.params.FromVersion}
	q := `FROM ci c JOIN ci_template t ON t.ci_id = c.id AND t.organization_id = c.organization_id
		WHERE c.organization_id = $1 AND c.deleted_at IS NULL AND t.template_id = $2 AND t.version = $3`
	if s.params.CIIDs != nil {
		args = append(args, s.params.CIIDs)
		q += fmt.Sprintf(" AND c.id = ANY($%d::uuid[])", len(args))
	}
	if s.params.Where != nil {
		pred, out, _, cerr := s.params.Where.CompileSQL("ci", "c", args, len(args)+1)
		if cerr != nil {
			return "", nil, cerr
		}
		args = out
		q += " AND " + pred
	}
	return q, args, nil
}

// inTx runs fn with the transaction of ctx (a block of RunInTx).
func inTx(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !database.InTx(ctx) {
		return ErrTemplateMigrationUnavailable
	}
	// The context transaction is joined; no pool is needed.
	return database.WithRequestTenant(ctx, nil, orgID, fn)
}

// ids returns the selected CIs after the id after ("" from the start), at
// most limit, in id order (keyset).
func (s templateSelection) ids(ctx context.Context, orgID, after string, limit int) ([]string, error) {
	from, args, err := s.sql(orgID)
	if err != nil {
		return nil, err
	}
	var out []string
	err = inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		args = append(args, nilIfEmpty(after), limit)
		rows, qerr := tx.Query(ctx, fmt.Sprintf(`SELECT c.id::text %s AND ($%d::uuid IS NULL OR c.id > $%d::uuid)
			ORDER BY c.id LIMIT $%d`, from, len(args)-1, len(args)-1, len(args)), args...)
		if qerr != nil {
			return fmt.Errorf("select CIs of the template migration: %w", qerr)
		}
		out, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return fmt.Errorf("select CIs of the template migration: %w", qerr)
		}
		return nil
	})
	return out, err
}

// count returns the number of selected CIs and, for a selection by ids,
// the ids without the assignment (template, from_version) the trigger
// sees.
func (s templateSelection) count(ctx context.Context, orgID string) (n int, unmatched []string, err error) {
	from, args, err := s.sql(orgID)
	if err != nil {
		return 0, nil, err
	}
	err = inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if qerr := tx.QueryRow(ctx, "SELECT count(*) "+from, args...).Scan(&n); qerr != nil {
			return fmt.Errorf("count CIs of the template migration: %w", qerr)
		}
		if s.params.CIIDs == nil {
			return nil
		}
		rows, qerr := tx.Query(ctx, "SELECT c.id::text "+from, args...)
		if qerr != nil {
			return fmt.Errorf("select CIs of the template migration: %w", qerr)
		}
		matched, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return fmt.Errorf("select CIs of the template migration: %w", qerr)
		}
		found := make(map[string]bool, len(matched))
		for _, id := range matched {
			found[id] = true
		}
		for _, id := range s.params.CIIDs {
			if !found[id] {
				unmatched = append(unmatched, id)
			}
		}
		return nil
	})
	return n, unmatched, err
}

// add adds the report of one CI: to the totals always, to the items when
// it has a change, a finding, an override or an error, or when it was
// named in ci_ids without the assignment (named).
func (r *TemplateMigrationReport) add(item *TemplateMigrationItem, named bool) {
	switch item.Outcome {
	case TemplateMigrated:
		r.Totals.Migrated++
	case TemplateSkipped:
		r.Totals.Skipped++
	default:
		r.Totals.Failed++
	}
	if len(item.Findings) > 0 {
		r.Totals.WithFindings++
	}
	listed := named || item.Outcome != TemplateSkipped || len(item.Findings) > 0 || len(item.Overridden) > 0 || item.Error != nil
	if !listed {
		return
	}
	if len(r.Items) >= MaxTemplateMigrationItems {
		r.Truncated = true
		return
	}
	r.Items = append(r.Items, *item)
}

// templateMigrationItemError reports the failure of one CI with a code and
// the attribute; internal errors stay generic.
func templateMigrationItemError(err error) *TemplateMigrationError {
	var ue *UniqueError
	var ve *ValidationError
	switch {
	case errors.As(err, &ue):
		out := &TemplateMigrationError{Code: "unique", Detail: "another CI holds the value of the unique attribute"}
		if len(ue.Fields) > 0 {
			out.Field = ue.Fields[0]
		}
		return out
	case errors.As(err, &ve):
		out := &TemplateMigrationError{Code: "validation", Detail: "the definitions of the target version do not compose"}
		if len(ve.Violations) > 0 {
			out.Field, out.Detail = ve.Violations[0].Field, ve.Violations[0].Detail
		}
		return out
	case errors.Is(err, ErrNotFound):
		return &TemplateMigrationError{Code: "not_found", Detail: "CI not found"}
	}
	return &TemplateMigrationError{Code: "internal", Detail: "the CI could not be migrated"}
}

// run runs one attempt of a job: it restores the trigger (fail-closed
// without scope snapshot), counts the selection once and migrates it block
// by block. Between blocks it records the progress and stops when the job
// was cancelled (errTemplateMigrationStopped). An error of a block (its
// selection or commit) fails the attempt; the blocks before it are
// committed and skipped by a retry.
func (m *TemplateMigrationJobs) run(ctx context.Context, job *jobs.Job) (*TemplateMigrationReport, error) {
	migrator, runner, ok := m.migrator()
	if !ok || m.fields == nil || m.jobs == nil {
		return nil, ErrTemplateMigrationUnavailable
	}
	var params templateMigrationParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("decode template migration parameters: %w", err)
	}
	runCtx, err := params.Context(ctx, job)
	if err != nil {
		return nil, err
	}
	// The job service runs on the worker path, without the trigger's
	// scope; a cancel of ctx does not end its checkpoints.
	jobCtx := context.WithoutCancel(ctx)
	orgID := job.OrganizationID
	plan := TemplateMigrationPlan{TemplateID: params.TemplateID, FromVersion: params.FromVersion, ToVersion: params.ToVersion,
		JobID: job.ID, Fields: m.fields}
	sel := templateSelection{params: &params}
	report := &TemplateMigrationReport{DryRun: params.DryRun, Items: []TemplateMigrationItem{}}

	var unmatched []string
	countErr := runner.RunInTx(runCtx, orgID, func(ctx context.Context) error {
		var cerr error
		report.Totals.Selected, unmatched, cerr = sel.count(ctx, orgID)
		return cerr
	})
	if countErr != nil {
		return nil, countErr
	}
	for _, id := range unmatched {
		report.add(&TemplateMigrationItem{CIID: id, Outcome: TemplateSkipped, Defaults: []string{}, Overridden: []string{},
			Findings: []Violation{}}, true)
	}

	after, done := "", 0
	for first := true; ; first = false {
		if !first {
			if cancelled, cerr := m.jobs.IsCancelled(jobCtx, orgID, job.ID); cerr == nil && cancelled {
				return nil, errTemplateMigrationStopped
			}
		}
		var block []TemplateMigrationItem
		var ids []string
		err = runner.RunInTx(runCtx, orgID, func(ctx context.Context) error {
			var serr error
			if ids, serr = sel.ids(ctx, orgID, after, templateMigrationBlock); serr != nil {
				return serr
			}
			block = make([]TemplateMigrationItem, 0, len(ids))
			for _, id := range ids {
				block = append(block, m.migrateOne(ctx, migrator, orgID, id, &plan, params.DryRun))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		for i := range block {
			report.add(&block[i], false)
		}
		done += len(ids)
		if len(ids) < templateMigrationBlock {
			return report, nil
		}
		after = ids[len(ids)-1]
		if report.Totals.Selected > 0 {
			percent := min(99, done*100/report.Totals.Selected)
			if perr := m.jobs.Progress(jobCtx, orgID, job.ID, percent); perr != nil {
				if errors.Is(perr, jobs.ErrTransition) {
					return nil, errTemplateMigrationStopped
				}
				return nil, perr
			}
		}
	}
}

// migrateOne migrates one CI of a block under a savepoint: an error
// discards only this CI and is reported with it.
func (m *TemplateMigrationJobs) migrateOne(ctx context.Context, migrator TemplateMigrator, orgID, ciID string,
	plan *TemplateMigrationPlan, dryRun bool) TemplateMigrationItem {
	var result *TemplateMigrationResult
	err := database.Savepoint(ctx, func(ctx context.Context) error {
		var merr error
		result, merr = migrator.MigrateTemplate(ctx, orgID, ciID, *plan, dryRun)
		return merr
	})
	if err != nil {
		if mapped := templateMigrationItemError(err); mapped.Code == "internal" {
			slog.WarnContext(ctx, "template migration of a CI failed", "ci", ciID, "job", plan.JobID, "error", err)
		}
		return TemplateMigrationItem{CIID: ciID, Outcome: TemplateMigrationFailed, Defaults: []string{}, Overridden: []string{},
			Findings: []Violation{}, Error: templateMigrationItemError(err)}
	}
	return TemplateMigrationItem{CIID: ciID, Outcome: result.Outcome, Defaults: result.Defaults, Overridden: result.Overridden,
		Findings: result.Findings}
}
