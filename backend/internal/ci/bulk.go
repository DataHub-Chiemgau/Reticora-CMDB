package ci

import (
	"context"
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
	// bulkStatusReason is the reason of the overrides a bulk status
	// change creates.
	bulkStatusReason = "bulk set_status"
)

// BulkRequest is the body of POST /cis/bulk.
type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	// Status is the target of set_status.
	Status string `json:"status,omitempty"`
}

// BulkItemResult is the outcome of one id.
type BulkItemResult struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	// Code is the HTTP status the single operation would have answered.
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

// BulkResult is the partial failure report of a bulk operation.
type BulkResult struct {
	Action    string           `json:"action"`
	Total     int              `json:"total"`
	Succeeded int              `json:"succeeded"`
	Failed    int              `json:"failed"`
	Items     []BulkItemResult `json:"items"`
}

// BulkJobs runs large bulk operations as jobs; jobs.Service implements it.
type BulkJobs interface {
	Enqueue(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error)
	Succeed(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
	Fail(ctx context.Context, orgID, id string, cause error) (*jobs.Job, error)
}

// WithBulkJobs runs bulk operations from BulkJobFromIDs ids on as jobs.
func (s *Service) WithBulkJobs(j BulkJobs) *Service {
	s.bulkJobs = j
	return s
}

// validateBulk normalizes and checks a bulk request: known action, 1–500
// distinct ids, a valid status for set_status.
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
	if !IsValidStatus(req.Status) {
		return &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(Statuses(), ", "), Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// Bulk runs the bulk operation synchronously below BulkJobFromIDs ids; from
// there on it enqueues a job, runs it in the background and returns the
// job (the result is the job result).
func (s *Service) Bulk(ctx context.Context, orgID string, req *BulkRequest) (*BulkResult, *jobs.Job, error) {
	if err := validateBulk(req); err != nil {
		return nil, nil, err
	}
	if len(req.IDs) < BulkJobFromIDs || s.bulkJobs == nil {
		result := s.runBulk(ctx, orgID, tenant.FromContext(ctx).UserID, req)
		return result, nil, nil
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
// The creator of the job is the author of its changes.
func (s *Service) runBulkJob(ctx context.Context, orgID, jobID string, req *BulkRequest) {
	claimed, err := s.bulkJobs.ClaimJob(ctx, orgID, jobID)
	if err != nil || claimed == nil {
		slog.ErrorContext(ctx, "claim bulk job", "job", jobID, "error", err)
		return
	}
	result := s.runBulk(ctx, orgID, claimed.CreatedBy, req)
	if _, err = s.bulkJobs.Succeed(ctx, orgID, jobID, result); err != nil {
		slog.ErrorContext(ctx, "record bulk job", "job", jobID, "error", err)
	}
}

// bulkWorkers bounds the CIs a bulk changes at once: each change is its own
// transaction, so a few run side by side (NFR-02: 500 ids < 30 s).
const bulkWorkers = 8

// runBulk applies the operation to every id as a manual change of author
// and reports each, in the order of the ids. An id is ok only when the CI
// stores the requested status afterwards.
func (s *Service) runBulk(ctx context.Context, orgID, author string, req *BulkRequest) *BulkResult {
	result := &BulkResult{Action: req.Action, Total: len(req.IDs), Items: make([]BulkItemResult, len(req.IDs))}
	status := req.Status
	var wg sync.WaitGroup
	slots := make(chan struct{}, bulkWorkers)
	for i, id := range req.IDs {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, id string) {
			defer func() { <-slots; wg.Done() }()
			anyVersion := api.IfMatchAny
			item, err := s.Update(ctx, orgID, id, UpdateRequest{Status: &status, ChangeReason: bulkStatusReason,
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
		if result.Items[i].Result == bulkResultOK {
			result.Succeeded++
		} else {
			result.Failed++
		}
	}
	return result
}

// bulkError maps the error of one id onto the status and message the
// single operation would answer; internal errors stay generic.
func bulkError(err error) (code int, msg string) {
	var fe *api.FieldError
	var ve *ValidationError
	switch {
	case err.Error() == "not found":
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
	return http.StatusInternalServerError, "the CI could not be updated"
}
