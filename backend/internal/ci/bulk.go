package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Bulk operations on CIs (API-08, BLK-01, BLK-02): at most 500 ids per
// request; from 100 ids on the operation runs as a job (JOB-04) and the
// request answers 202 with the job. Below 100 ids it runs in the request,
// with a job service as a job ci.bulk the request claims and ends (WP-305):
// the 200 report names it (bulk_job_id), and every bulk job keeps its
// change set for the revert (bulk_revert.go). The result reports every id
// on its own,
// so one failing CI does not fail the others (partial failure report).
// The action names the operation (default set_status); its parameters
// other than the status stand in params (WP-304, bulk_ops.go).
//
// A bulk write of CI fields is a manual change like a PATCH (CI-04,
// OVR-01, OVR-03): it sets the fields over an existing override, gives
// each changed one a new protected override with the initiating user as
// author and raises the version (API-07). It names no version per id, so
// it is a deliberate overwrite with If-Match "*" and needs none where the
// organization requires one (E-66 option 1, transition rule of WP-368).
// The override reason is bulk:<job_id> (with a job service every bulk runs
// as a job, also below 100 ids), and the audit entry of each changed CI
// carries the job id as correlation id (WP-306). A revert of the job
// restores the state of the overrides before it (bulk_revert.go).

// Bulk limits of API-08.
const (
	MaxBulkIDs      = 500
	BulkJobFromIDs  = 100
	BulkJobKind     = "ci.bulk"
	BulkSetStatus   = "set_status"
	bulkResultOK    = "ok"
	bulkResultError = "error"
	// bulkResultSkipped: the job was cancelled before the id (JOB-03).
	bulkResultSkipped = "skipped"
	// bulkCancelEvery: a bulk job asks whether it was cancelled before
	// every bulkCancelEvery-th id, at most one query per 25 ids.
	bulkCancelEvery = 25
	// bulkReason is the reason of the overrides a bulk write creates;
	// a job adds its id (bulk:<job_id>, WP-304).
	bulkReason = "bulk"
	// bulkMalformedID is the report error of an id that is no UUID, a
	// client error like in the single change (400, F-327).
	bulkMalformedID = "invalid CI id"
)

// BulkRequest is the body of POST /cis/bulk.
type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	// Status is the target of set_status; no other action takes it.
	Status string `json:"status,omitempty"`
	// Params are the parameters of the actions other than set_status
	// (bulk_ops.go); kept as sent, so a job decodes them again.
	Params json.RawMessage `json:"params,omitempty"`
	// params are the decoded Params (validateBulk).
	params *bulkParams
	// malformed holds the ids that are no UUID (validateBulk): each is an
	// error of its own in the report and never reaches the database.
	malformed map[string]bool
}

// BulkItemResult is the outcome of one id.
type BulkItemResult struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	// Code is the HTTP status the single operation would have answered;
	// 0 for an id skipped after a cancel.
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
	// Type is the problem type of an error (WP-304): the type of an
	// api.TypedProblem, otherwise https://reticora.io/problems/<code>
	// like api.WriteError.
	Type string `json:"type,omitempty"`
	// Fields are the fields the error names (violations, a taken unique
	// attribute, a stale field, refused field permissions).
	Fields []string `json:"fields,omitempty"`
	// Findings are the findings of the revalidation of a template
	// migration by change_template (WP-780); they do not block.
	Findings []Violation `json:"findings,omitempty"`
}

// BulkResult is the partial failure report of a bulk operation.
type BulkResult struct {
	// BulkJobID is the job ci.bulk of a bulk that ran in the request
	// (WP-305): POST /jobs/{id}/revert undoes it. Empty without a job
	// service.
	BulkJobID string `json:"bulk_job_id,omitempty"`
	Action    string `json:"action"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	// Skipped counts the ids not processed because the job was cancelled.
	Skipped int `json:"skipped,omitempty"`
	// Cancelled: the job was cancelled while it ran and stopped before
	// the skipped ids.
	Cancelled bool             `json:"cancelled,omitempty"`
	Items     []BulkItemResult `json:"items"`
}

// bulkCancels is the cancel checkpoint of a bulk job (JOB-03, F-162):
// whether the job was cancelled, and the store of the partial report of a
// cancelled job; jobs.Service and jobs.Queue implement it.
type bulkCancels interface {
	IsCancelled(ctx context.Context, orgID, id string) (bool, error)
	SaveCancelledResult(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
}

// BulkJobs runs large bulk operations as jobs; jobs.Service implements it.
type BulkJobs interface {
	Enqueue(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error)
	Succeed(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
	Fail(ctx context.Context, orgID, id string, cause error) (*jobs.Job, error)
	bulkCancels
}

// WithBulkJobs runs bulk operations from BulkJobFromIDs ids on as jobs.
func (s *Service) WithBulkJobs(j BulkJobs) *Service {
	s.bulkJobs = j
	return s
}

// BulkQueue submits bulk jobs to the job queue on NATS (TEC-14), whose
// workers run them with RunBulkJob; jobs.Queue implements it.
type BulkQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	bulkCancels
}

// WithBulkQueue runs the bulk jobs on the job queue instead of in this
// process: a job outlives the replica that accepted it.
func (s *Service) WithBulkQueue(q BulkQueue) *Service {
	s.bulkQueue = q
	return s
}

// CreatorPolicy resolves the field and action policy of the creator of a
// queue job (job.CreatedBy, the rights at the time of the run narrowed to
// the scope snapshot of the job row; permission.FieldResolver.
// PolicyForCreator). Nothing of it is stored in the job parameters.
type CreatorPolicy func(ctx context.Context, job *jobs.Job) (*permission.FieldPolicy, error)

// ErrNoCreatorPolicy fails a bulk job of the queue that cannot resolve the
// policy of its creator: without it the run would check no action and no
// field (fail-closed, WP-728).
var ErrNoCreatorPolicy = errors.New("bulk job: the rights of its creator cannot be resolved")

// WithCreatorPolicy sets how a bulk job of the queue resolves the policy
// of its creator (RunBulkJob, WP-728); without it such a job fails.
func (s *Service) WithCreatorPolicy(fn CreatorPolicy) *Service {
	s.creatorPolicy = fn
	return s
}

// CreatorPolicyOf is the CreatorPolicy of the field resolver: the creator
// must be an active user (users), its role assignments of the run time are
// narrowed to the scope snapshot of the job (a job without snapshot gets
// no right).
func CreatorPolicyOf(resolver *permission.FieldResolver, users permission.CreatorStatus) CreatorPolicy {
	return func(ctx context.Context, job *jobs.Job) (*permission.FieldPolicy, error) {
		var scope *permission.CreatorScope
		if job.Scope != nil {
			scope = &permission.CreatorScope{Clients: job.Scope.Clients, Sites: job.Scope.Sites, Teams: job.Scope.Teams}
		}
		return resolver.PolicyForCreator(ctx, users, job.OrganizationID, job.CreatedBy, scope)
	}
}

// validateBulk normalizes and checks a bulk request: known action, 1–500
// distinct ids, a valid status for set_status, the parameters of the other
// actions (validateParams, 422 on params.<name>). It checks every id as a
// UUID: a malformed id does not fail the request, it is reported on its own
// with 400 while the other ids are applied (API-08, F-327). A UUID is
// distinct in its canonical lower-case form: the same CI in another letter
// case is applied and reported once, under that form (F-419).
func validateBulk(req *BulkRequest) error {
	if req.Action == "" {
		req.Action = BulkSetStatus
	}
	if _, known := bulkActionPermissions[req.Action]; !known {
		return &api.FieldError{Field: "action", Message: "must be one of " + strings.Join(BulkActions(), ", "), Status: http.StatusUnprocessableEntity}
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); api.IsUUID(id) {
			id = strings.ToLower(id)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return &api.FieldError{Field: "ids", Message: "at least one id is required"}
	case len(ids) > MaxBulkIDs:
		return &api.FieldError{Field: "ids", Message: fmt.Sprintf("at most %d ids per request", MaxBulkIDs), Status: http.StatusUnprocessableEntity}
	}
	req.IDs = ids
	req.malformed = nil
	for _, id := range ids {
		if !api.IsUUID(id) {
			if req.malformed == nil {
				req.malformed = map[string]bool{}
			}
			req.malformed[id] = true
		}
	}
	if req.Action != BulkSetStatus {
		if req.Status != "" {
			return &api.FieldError{Field: "status", Message: "only " + BulkSetStatus + " takes a status", Status: http.StatusUnprocessableEntity}
		}
		return validateParams(req)
	}
	if !IsValidStatus(req.Status) {
		return &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(Statuses(), ", "), Status: http.StatusUnprocessableEntity}
	}
	return validateParams(req)
}

// Bulk runs the bulk operation synchronously below BulkJobFromIDs ids, with
// a job service as a job of the request (runSyncJob, WP-305); from there on
// it returns a job whose result is the report: submitted to the job queue
// when there is one (TEC-14), otherwise enqueued and run in the background
// of this process.
func (s *Service) Bulk(ctx context.Context, orgID string, req *BulkRequest) (*BulkResult, *jobs.Job, error) {
	if err := validateBulk(req); err != nil {
		return nil, nil, err
	}
	// The rights of the action beyond ci:write and the catalog keys of the
	// tags, for the whole request before any change and before a job.
	if err := checkBulkPermission(ctx, req.Action); err != nil {
		return nil, nil, err
	}
	if err := s.checkBulkTags(ctx, orgID, req); err != nil {
		return nil, nil, err
	}
	// The ports, the entitlement and the parameters of a delegating action
	// (WP-780), as well before any change.
	if err := s.prepareDelegate(ctx, orgID, req); err != nil {
		return nil, nil, err
	}
	if req.Action == BulkExport {
		// One export job for every id, whatever their number.
		job, err := s.bulkExport(ctx, orgID, req)
		return nil, job, err
	}
	if len(req.IDs) < BulkJobFromIDs && s.bulkJobs != nil {
		return s.runSyncJob(ctx, orgID, req)
	}
	if len(req.IDs) < BulkJobFromIDs || (s.bulkJobs == nil && s.bulkQueue == nil) {
		result := s.runBulk(ctx, orgID, tenant.FromContext(ctx).UserID, "", req, nil)
		return result, nil, nil
	}
	if s.bulkQueue != nil {
		job, _, err := s.bulkQueue.Submit(ctx, orgID, BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: tenant.FromContext(ctx).UserID})
		if err != nil {
			return nil, nil, fmt.Errorf("submit bulk job: %w", err)
		}
		return nil, job, nil
	}
	job, _, err := s.bulkJobs.Enqueue(ctx, orgID, BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: tenant.FromContext(ctx).UserID})
	if err != nil {
		return nil, nil, fmt.Errorf("enqueue bulk job: %w", err)
	}
	// The job runs with the caller's tenant and scope, after the request.
	runCtx := context.WithoutCancel(ctx)
	go s.runBulkJob(runCtx, orgID, job.ID, req)
	return nil, job, nil
}

// runSyncJob runs a bulk below BulkJobFromIDs ids as a job ci.bulk in the
// request (WP-305): it enqueues the job, claims it, runs the operation with
// the job's id (the reason bulk:<job_id> and the change set) and ends the
// job with the report. A job a worker of the queue claimed first (its
// sweep) runs there: the request answers with the job like from 100 ids on.
func (s *Service) runSyncJob(ctx context.Context, orgID string, req *BulkRequest) (*BulkResult, *jobs.Job, error) {
	job, _, err := s.bulkJobs.Enqueue(ctx, orgID, BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: tenant.FromContext(ctx).UserID})
	if err != nil {
		return nil, nil, fmt.Errorf("enqueue bulk job: %w", err)
	}
	claimed, err := s.bulkJobs.ClaimJob(ctx, orgID, job.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("claim bulk job: %w", err)
	}
	if claimed == nil {
		return nil, job, nil
	}
	// The report is recorded also when the client goes away.
	jobCtx := context.WithoutCancel(ctx)
	result := s.runBulk(ctx, orgID, claimed.CreatedBy, job.ID, req, s.cancelCheck(jobCtx, s.bulkJobs, orgID, job.ID))
	result.BulkJobID = job.ID
	if keepCancelled(jobCtx, s.bulkJobs, orgID, job.ID, result) {
		return result, nil, nil
	}
	if _, err = s.bulkJobs.Succeed(jobCtx, orgID, job.ID, result); err != nil {
		if errors.Is(err, jobs.ErrTransition) && keepCancelled(jobCtx, s.bulkJobs, orgID, job.ID, result) {
			return result, nil, nil
		}
		slog.ErrorContext(ctx, "record bulk job", "job", job.ID, "error", err)
	}
	return result, nil, nil
}

// runBulkJob claims the job, runs the operation and records the report.
// The creator of the job is the author of its changes. A job cancelled
// while it ran stops at the next checkpoint and keeps the partial report
// (keepCancelled).
func (s *Service) runBulkJob(ctx context.Context, orgID, jobID string, req *BulkRequest) {
	claimed, err := s.bulkJobs.ClaimJob(ctx, orgID, jobID)
	if err != nil || claimed == nil {
		slog.ErrorContext(ctx, "claim bulk job", "job", jobID, "error", err)
		return
	}
	result := s.runBulk(ctx, orgID, claimed.CreatedBy, jobID, req, s.cancelCheck(ctx, s.bulkJobs, orgID, jobID))
	if keepCancelled(ctx, s.bulkJobs, orgID, jobID, result) {
		return
	}
	if _, err = s.bulkJobs.Succeed(ctx, orgID, jobID, result); err != nil {
		if errors.Is(err, jobs.ErrTransition) && keepCancelled(ctx, s.bulkJobs, orgID, jobID, result) {
			// Cancelled after the last checkpoint.
			return
		}
		slog.ErrorContext(ctx, "record bulk job", "job", jobID, "error", err)
	}
}

// RunBulkJob runs a bulk job of the job queue (handler of ci.bulk): the
// operation of its parameters with the tenant and scope of its creator
// (jobs.CreatorContext), who is the author of its changes, and the policy
// of the creator (WithCreatorPolicy): each id is checked against the
// action and field permissions like a PATCH (403 in the report, WP-728);
// a creator that is no active user, or a service without creator policy,
// fails the job (fail-closed). The report is
// the job result; a job cancelled while it ran stops at the next
// checkpoint and keeps the partial report (keepCancelled), the queue then
// records nothing.
func (s *Service) RunBulkJob(ctx context.Context, job *jobs.Job) (any, error) {
	var req BulkRequest
	if err := json.Unmarshal(job.Params, &req); err != nil {
		return nil, fmt.Errorf("decode bulk job parameters: %w", err)
	}
	if err := validateBulk(&req); err != nil {
		return nil, fmt.Errorf("invalid bulk job parameters: %w", err)
	}
	var cancels bulkCancels
	if s.bulkQueue != nil {
		cancels = s.bulkQueue
	} else if s.bulkJobs != nil {
		cancels = s.bulkJobs
	}
	// ctx ends with a cancel (the queue's run context): the checkpoint
	// and the report of the cancelled job do not.
	jobCtx := context.WithoutCancel(ctx)
	if s.creatorPolicy == nil {
		return nil, ErrNoCreatorPolicy
	}
	runCtx := jobs.CreatorContext(ctx, job)
	policy, err := s.creatorPolicy(runCtx, job)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCreatorPolicy, err)
	}
	runCtx = permission.WithPolicy(runCtx, policy)
	// A delegating action resolves its parameters again (WP-780).
	if err = s.prepareDelegate(runCtx, job.OrganizationID, &req); err != nil {
		return nil, fmt.Errorf("prepare bulk job: %w", err)
	}
	result := s.runBulk(runCtx, job.OrganizationID, job.CreatedBy, job.ID, &req,
		s.cancelCheck(jobCtx, cancels, job.OrganizationID, job.ID))
	if cancels != nil && keepCancelled(jobCtx, cancels, job.OrganizationID, job.ID, result) {
		return nil, nil
	}
	return result, nil
}

// cancelCheck is the checkpoint of runBulk for a job: whether it was
// cancelled; nil without a job service.
func (s *Service) cancelCheck(ctx context.Context, c bulkCancels, orgID, jobID string) func() bool {
	if c == nil {
		return nil
	}
	return func() bool {
		cancelled, err := c.IsCancelled(ctx, orgID, jobID)
		if err != nil {
			slog.WarnContext(ctx, "check bulk job cancel", "job", jobID, "error", err)
		}
		return err == nil && cancelled
	}
}

// keepCancelled stores the report of a bulk job that was cancelled while it
// ran on the cancelled job (BLK-01: result per id) and reports whether it
// was cancelled: a run that stopped at a checkpoint, or a run that ended
// after a cancel its last checkpoint did not see.
func keepCancelled(ctx context.Context, c bulkCancels, orgID, jobID string, result *BulkResult) bool {
	if !result.Cancelled {
		if cancelled, err := c.IsCancelled(ctx, orgID, jobID); err != nil || !cancelled {
			return false
		}
	}
	if _, err := c.SaveCancelledResult(ctx, orgID, jobID, result); err != nil {
		slog.ErrorContext(ctx, "record the report of a cancelled bulk job", "job", jobID, "error", err)
	}
	return true
}

// bulkWorkers bounds the CIs a bulk changes at once: each change is its own
// transaction, so a few run side by side (NFR-02: 500 ids < 30 s).
const bulkWorkers = 8

// runBulk applies the operation to every id as a manual change of author
// and reports each, in the order of the ids (bulkApply). jobID names the
// job of the run ("" for a synchronous one) in the reason of the overrides.
// cancelled (nil: none) is asked before every bulkCancelEvery-th id; once
// it reports a cancel, or ctx ends, no further id is started, the changes
// under way finish and the remaining ids are reported skipped (JOB-03,
// F-162).
func (s *Service) runBulk(ctx context.Context, orgID, author, jobID string, req *BulkRequest, cancelled func() bool) *BulkResult {
	result := &BulkResult{Action: req.Action, Total: len(req.IDs), Items: make([]BulkItemResult, len(req.IDs))}
	op := bulkOp{orgID: orgID, author: author, reason: bulkReason, req: req}
	if jobID != "" {
		op.reason, op.jobID = bulkReason+":"+jobID, jobID
		// The writes of the run record the change set of the job (WP-305);
		// their audit entries carry the job as correlation id, the request
		// id stays the bulk request's, and an entry counts for the request
		// (no additional route entry, WP-306).
		ctx = audit.WithCorrelationID(jobs.WithChangeSet(ctx, jobID), jobID)
	}
	// A change under way finishes when ctx ends: its report is accurate.
	itemCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	slots := make(chan struct{}, bulkWorkers)
	for i, id := range req.IDs {
		if ctx.Err() != nil || (cancelled != nil && i > 0 && i%bulkCancelEvery == 0 && cancelled()) {
			result.Cancelled = errors.Is(context.Cause(ctx), jobs.ErrCancelled) || ctx.Err() == nil
			for j := i; j < len(req.IDs); j++ {
				result.Items[j] = BulkItemResult{ID: req.IDs[j], Result: bulkResultSkipped}
			}
			break
		}
		if req.malformed[id] {
			result.Items[i] = BulkItemResult{ID: id, Result: bulkResultError, Code: http.StatusBadRequest, Error: bulkMalformedID,
				Type: problemType(nil, http.StatusBadRequest)}
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, id string) {
			defer func() { <-slots; wg.Done() }()
			result.Items[i] = s.bulkApply(itemCtx, &op, id)
		}(i, id)
	}
	wg.Wait()
	for i := range result.Items {
		switch result.Items[i].Result {
		case bulkResultOK:
			result.Succeeded++
		case bulkResultSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
	}
	return result
}

// bulkOK is the report of an id the operation applied.
func bulkOK(id string) BulkItemResult {
	return BulkItemResult{ID: id, Result: bulkResultOK, Code: http.StatusOK}
}

// bulkFailed is the report of an id whose change failed with err.
func bulkFailed(id string, err error) BulkItemResult {
	f := bulkError(err)
	return BulkItemResult{ID: id, Result: bulkResultError, Code: f.code, Error: f.msg, Type: problemType(err, f.code), Fields: f.fields}
}

// bulkNotApplied is the report of an id whose CI does not hold the value
// the operation wrote afterwards: no success (API-08).
func bulkNotApplied(id, detail string) BulkItemResult {
	return BulkItemResult{ID: id, Result: bulkResultError, Code: http.StatusConflict, Error: detail,
		Type: problemType(nil, http.StatusConflict)}
}

// problemType is the problem type of the report of an id (WP-304): the
// type of an api.TypedProblem, otherwise the one api.WriteError gives the
// status.
func problemType(err error, code int) string {
	var typed api.TypedProblem
	if err != nil && errors.As(err, &typed) && typed.ProblemType() != "" {
		return typed.ProblemType()
	}
	return fmt.Sprintf("https://reticora.io/problems/%d", code)
}

// bulkFailure is the report of a failed id: status, message and the
// fields the error names.
type bulkFailure struct {
	code   int
	msg    string
	fields []string
}

// bulkError maps the error of one id onto the status, message and fields
// the single operation would answer: 403 for a refusal of the action or
// field permissions (with the fields), 409 for a taken unique attribute
// and a stale field (with the fields), 422 for violations (with their
// fields), database errors like the single PATCH (api.ClassifyDBError,
// sanitized details), an unreachable dependency 503; internal errors stay
// generic.
func bulkError(err error) bulkFailure {
	var fe *api.FieldError
	var ve *ValidationError
	var forbidden *api.FieldForbiddenError
	var unique *UniqueError
	var conflict *ConflictError
	var reserved *ReservedKeyError
	var legacy *LegacyTagsError
	var item *BulkItemError
	switch {
	case errors.As(err, &item):
		// The outcome a port of a delegating action names (WP-780).
		return bulkItemFailure(item)
	case errors.Is(err, ErrBulkActionUnavailable):
		return unavailableFailure()
	case errors.Is(err, ErrNotFound) || err.Error() == "not found":
		return bulkFailure{code: http.StatusNotFound, msg: "CI not found"}
	case errors.As(err, &forbidden):
		// An action grant or a field permission refuses the id (RBA-07,
		// WP-728); the other ids go on (BLK-01).
		return bulkFailure{code: http.StatusForbidden, msg: forbidden.Error(), fields: forbidden.Fields}
	case errors.Is(err, ErrInstanceAttributeForbidden):
		return bulkFailure{code: http.StatusForbidden, msg: instanceForbiddenDetail}
	case errors.As(err, &unique):
		return bulkFailure{code: http.StatusConflict, msg: unique.Error(), fields: unique.Fields}
	case errors.As(err, &conflict):
		return bulkFailure{code: http.StatusConflict, msg: conflict.Error(), fields: conflict.Fields}
	case errors.As(err, &reserved):
		return bulkFailure{code: http.StatusBadRequest, msg: reserved.Error(), fields: reserved.Fields}
	case errors.As(err, &legacy):
		return bulkFailure{code: http.StatusBadRequest, msg: legacy.Error(), fields: []string{"attributes." + LegacyTagsKey}}
	case errors.As(err, &fe):
		if fe.Status != 0 {
			return bulkFailure{code: fe.Status, msg: fe.Error(), fields: []string{fe.Field}}
		}
		return bulkFailure{code: http.StatusBadRequest, msg: fe.Error(), fields: []string{fe.Field}}
	case errors.As(err, &ve):
		return bulkFailure{code: http.StatusUnprocessableEntity, msg: ve.Error(), fields: violationFields(ve.Violations)}
	case errors.Is(err, ErrPreconditionRequired):
		return bulkFailure{code: http.StatusPreconditionRequired, msg: err.Error()}
	case errors.Is(err, ErrPreconditionFailed):
		return bulkFailure{code: http.StatusPreconditionFailed, msg: err.Error()}
	}
	if status, _, detail, ok := api.ClassifyDBError(err); ok {
		return bulkFailure{code: status, msg: detail}
	}
	if api.IsUnavailable(err) {
		return bulkFailure{code: http.StatusServiceUnavailable, msg: "a required service is temporarily unavailable, try again later"}
	}
	return bulkFailure{code: http.StatusInternalServerError, msg: "the CI could not be updated"}
}

// violationFields returns the distinct fields of violations in order.
func violationFields(violations []Violation) []string {
	var out []string
	for i := range violations {
		if !slices.Contains(out, violations[i].Field) {
			out = append(out, violations[i].Field)
		}
	}
	return out
}
