package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Bulk operations on CIs (API-08, BLK-01): at most 500 ids per request;
// from 100 ids on the operation runs as a job (JOB-04) and the request
// answers 202 with the job; the result reports every id on its own, so one
// failing CI does not fail the others (partial failure report).
//
// A bulk status change is a manual change like a PATCH (CI-04, OVR-01): it
// sets the status over an existing override, gives it a new protected
// override with the initiating user as author and raises the version
// (API-07). It names no version per id, so it is a deliberate overwrite
// with If-Match "*" and needs none where the organization requires one
// (E-66 option 1, transition rule of WP-368).

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
	// bulkStatusReason is the reason of the overrides a bulk status
	// change creates.
	bulkStatusReason = "bulk set_status"
	// bulkMalformedID is the report error of an id that is no UUID, a
	// client error like in the single change (400, F-327).
	bulkMalformedID = "invalid CI id"
)

// BulkRequest is the body of POST /cis/bulk.
type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	// Status is the target of set_status.
	Status string `json:"status,omitempty"`
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
}

// BulkResult is the partial failure report of a bulk operation.
type BulkResult struct {
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

// validateBulk normalizes and checks a bulk request: known action, 1–500
// distinct ids, a valid status for set_status. It checks every id as a
// UUID: a malformed id does not fail the request, it is reported on its own
// with 400 while the other ids are applied (API-08, F-327).
func validateBulk(req *BulkRequest) error {
	if req.Action == "" {
		req.Action = BulkSetStatus
	}
	if req.Action != BulkSetStatus {
		return &api.FieldError{Field: "action", Message: "must be " + BulkSetStatus, Status: http.StatusUnprocessableEntity}
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
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
	if !IsValidStatus(req.Status) {
		return &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(Statuses(), ", "), Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// Bulk runs the bulk operation synchronously below BulkJobFromIDs ids; from
// there on it returns a job whose result is the report: submitted to the
// job queue when there is one (TEC-14), otherwise enqueued and run in the
// background of this process.
func (s *Service) Bulk(ctx context.Context, orgID string, req *BulkRequest) (*BulkResult, *jobs.Job, error) {
	if err := validateBulk(req); err != nil {
		return nil, nil, err
	}
	if len(req.IDs) < BulkJobFromIDs || (s.bulkJobs == nil && s.bulkQueue == nil) {
		result := s.runBulk(ctx, orgID, tenant.FromContext(ctx).UserID, req, nil)
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
	result := s.runBulk(ctx, orgID, claimed.CreatedBy, req, s.cancelCheck(ctx, s.bulkJobs, orgID, jobID))
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
// (jobs.CreatorContext), who is the author of its changes. The report is
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
	result := s.runBulk(jobs.CreatorContext(ctx, job), job.OrganizationID, job.CreatedBy, &req,
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
// and reports each, in the order of the ids. An id is ok only when the CI
// stores the requested status afterwards. cancelled (nil: none) is asked
// before every bulkCancelEvery-th id; once it reports a cancel, or ctx
// ends, no further id is started, the changes under way finish and the
// remaining ids are reported skipped (JOB-03, F-162).
func (s *Service) runBulk(ctx context.Context, orgID, author string, req *BulkRequest, cancelled func() bool) *BulkResult {
	result := &BulkResult{Action: req.Action, Total: len(req.IDs), Items: make([]BulkItemResult, len(req.IDs))}
	status := req.Status
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
			result.Items[i] = BulkItemResult{ID: id, Result: bulkResultError, Code: http.StatusBadRequest, Error: bulkMalformedID}
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, id string) {
			defer func() { <-slots; wg.Done() }()
			anyVersion := api.IfMatchAny
			item, err := s.Update(itemCtx, orgID, id, UpdateRequest{Status: &status, ChangeReason: bulkStatusReason,
				IfMatch: &anyVersion, Manual: &ManualChange{Author: author}})
			switch {
			case err != nil:
				code, msg := bulkError(err)
				result.Items[i] = BulkItemResult{ID: id, Result: bulkResultError, Code: code, Error: msg}
			case item.Status != status:
				// A write that left the status is no success (API-08).
				result.Items[i] = BulkItemResult{ID: id, Result: bulkResultError, Code: http.StatusConflict,
					Error: fmt.Sprintf("the status was not applied, the CI keeps status %s", item.Status)}
			default:
				result.Items[i] = BulkItemResult{ID: id, Result: bulkResultOK, Code: http.StatusOK}
			}
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

// bulkError maps the error of one id onto the status and message the
// single operation would answer: database errors like the single PATCH
// (api.ClassifyDBError, sanitized details), an unreachable dependency 503;
// internal errors stay generic.
func bulkError(err error) (code int, msg string) {
	var fe *api.FieldError
	var ve *ValidationError
	switch {
	case errors.Is(err, ErrNotFound) || err.Error() == "not found":
		return http.StatusNotFound, "CI not found"
	case errors.As(err, &fe):
		if fe.Status != 0 {
			return fe.Status, fe.Error()
		}
		return http.StatusBadRequest, fe.Error()
	case errors.As(err, &ve):
		return http.StatusUnprocessableEntity, ve.Error()
	case errors.Is(err, ErrPreconditionRequired):
		return http.StatusPreconditionRequired, err.Error()
	case errors.Is(err, ErrPreconditionFailed):
		return http.StatusPreconditionFailed, err.Error()
	}
	if status, _, detail, ok := api.ClassifyDBError(err); ok {
		return status, detail
	}
	if api.IsUnavailable(err) {
		return http.StatusServiceUnavailable, "a required service is temporarily unavailable, try again later"
	}
	return http.StatusInternalServerError, "the CI could not be updated"
}
