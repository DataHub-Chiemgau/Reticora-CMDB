package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Discovery job types and statuses (mirrors the discovery_job table CHECKs).
const (
	JobTypeSweep = "sweep"
	JobTypePoll  = "poll"
	JobTypeFull  = "full"

	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
)

// Job represents a discovery/ingest job backed by the discovery_job table.
type Job struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	CollectorID    string         `json:"collector_id,omitempty"`
	JobType        string         `json:"job_type"`
	Status         string         `json:"status"`
	Config         map[string]any `json:"config"`
	ResultSummary  map[string]any `json:"result_summary,omitempty"`
	// Error is the cause of a failed job (DIS-08).
	Error       *ErrorState `json:"error,omitempty"`
	StartedAt   string      `json:"started_at,omitempty"`
	CompletedAt string      `json:"completed_at,omitempty"`
	CreatedAt   string      `json:"created_at"`
}

// JobFilter narrows a discovery-job listing.
type JobFilter struct {
	Status      string
	CollectorID string
}

// CreateJobRequest is the payload for creating a discovery job.
type CreateJobRequest struct {
	CollectorID string         `json:"collector_id"`
	JobType     string         `json:"job_type"`
	Config      map[string]any `json:"config,omitempty"`
}

// jobMirrorStatus maps a discovery job status to the status of its generic
// job (JOB-01).
var jobMirrorStatus = map[string]jobs.Status{
	JobStatusPending:   jobs.StatusQueued,
	JobStatusRunning:   jobs.StatusRunning,
	JobStatusCompleted: jobs.StatusSucceeded,
	JobStatusFailed:    jobs.StatusFailed,
}

// jobTrackingRepository is a repository whose UpdateJobStatus mirrors the
// status into the generic job model; the PG repository implements it.
type jobTrackingRepository interface {
	setJobTracker(tracker jobs.Tracker)
}

var validJobTypes = map[string]bool{
	JobTypeSweep: true,
	JobTypePoll:  true,
	JobTypeFull:  true,
}

// ---- MemoryRepository job methods ----

func (r *MemoryRepository) CreateJob(_ context.Context, j *Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	j.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	if j.Status == "" {
		j.Status = JobStatusPending
	}
	if j.Config == nil {
		j.Config = map[string]any{}
	}
	j.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	stored := *j
	r.jobs[j.ID] = &stored
	return nil
}

func (r *MemoryRepository) ListJobs(_ context.Context, orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Job
	for _, j := range r.jobs {
		if j.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && j.Status != filter.Status {
			continue
		}
		if filter.CollectorID != "" && j.CollectorID != filter.CollectorID {
			continue
		}
		result = append(result, *j)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) GetJob(_ context.Context, orgID, id string) (*Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	j, ok := r.jobs[id]
	if !ok || j.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	clone := *j
	return &clone, nil
}

// ---- Handlers ----

// ListJobs handles GET /api/v1/discovery/jobs
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := JobFilter{
		Status:      r.URL.Query().Get("status"),
		CollectorID: r.URL.Query().Get("collector_id"),
	}
	jobs, total, err := h.repo.ListJobs(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	for i := range jobs {
		jobs[i].Error = JobErrorOf(&jobs[i])
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Job]{
		Data:    jobs,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateJob handles POST /api/v1/discovery/jobs
func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateJobRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.CollectorID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "collector_id is required")
		return
	}
	if req.JobType == "" {
		req.JobType = JobTypeSweep
	}
	if !validJobTypes[req.JobType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid job_type")
		return
	}

	job := &Job{
		OrganizationID: t.OrganizationID,
		CollectorID:    req.CollectorID,
		JobType:        req.JobType,
		Status:         JobStatusPending,
		Config:         req.Config,
	}
	if err := h.repo.CreateJob(r.Context(), job); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if h.jobs != nil {
		// The generic job model lists the discovery job too (JOB-01); a
		// failure leaves the discovery job in place.
		if err := h.jobs.Track(r.Context(), t.OrganizationID, "discovery."+job.JobType, "discovery_job", job.ID, t.UserID); err != nil {
			slog.Warn("track discovery job", "job", job.ID, "error", err)
		}
	}
	api.WriteJSON(w, http.StatusCreated, job)
}

// WithJobTracker registers discovery jobs in the generic job model: a
// created job is tracked as discovery.<job_type>, and the repository's
// UpdateJobStatus mirrors its status changes (JOB-01).
func (h *Handler) WithJobTracker(tracker jobs.Tracker) *Handler {
	h.jobs = tracker
	if repo, ok := h.repo.(jobTrackingRepository); ok {
		repo.setJobTracker(tracker)
	}
	return h
}

// GetJob handles GET /api/v1/discovery/jobs/{id}
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	job, err := h.repo.GetJob(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "discovery job not found")
		return
	}
	job.Error = JobErrorOf(job)
	api.WriteJSON(w, http.StatusOK, job)
}

// ---- Execution on the collector (JOB-01, F-358) ----

// An ad-hoc discovery job runs on its collector (WP-632, decision of the
// client of 2026-10-27, option A): the collector asks for its open jobs over
// its scope channel (ServeCollectorScopes, JobsRequestSuffix), reports a job
// running before it runs it and the result afterwards (JobsStatusSuffix);
// the server sets the status with UpdateJobStatus, which mirrors it into the
// generic job model. Only the job's own collector receives and moves it. A
// job without a report within JobDeadline of its last status change fails
// with cause timeout (JobReaper). Jobs with a kind in their config
// (iga_relay) belong to other executors and are neither handed out nor
// reaped.

// JobDeadline is how long a job may wait for its collector, and run once
// started, before the reaper fails it with cause timeout.
const JobDeadline = time.Hour

// maxHandedJobs bounds the open jobs one jobs request hands out.
const maxHandedJobs = 20

// JobUpdateOption narrows or extends UpdateJobStatus.
type JobUpdateOption func(*jobUpdate)

type jobUpdate struct {
	from        []string
	collectorID string
	result      map[string]any
}

// JobFrom requires the job to have one of the statuses.
func JobFrom(statuses ...string) JobUpdateOption {
	return func(u *jobUpdate) { u.from = statuses }
}

// JobOfCollector requires the job to belong to the collector.
func JobOfCollector(collectorID string) JobUpdateOption {
	return func(u *jobUpdate) { u.collectorID = collectorID }
}

// JobResult merges result into the job's result_summary.
func JobResult(result map[string]any) JobUpdateOption {
	return func(u *jobUpdate) { u.result = result }
}

// JobRunRepository is a repository whose discovery jobs run on their
// collector; the PG repository implements it.
type JobRunRepository interface {
	// OpenCollectorJobs returns the oldest pending ad-hoc jobs of a
	// collector.
	OpenCollectorJobs(ctx context.Context, orgID, collectorID string, limit int) ([]Job, error)
	// OverdueJobs returns the pending or running ad-hoc jobs whose last
	// status change lies before before.
	OverdueJobs(ctx context.Context, orgID string, before time.Time, limit int) ([]Job, error)
	UpdateJobStatus(ctx context.Context, orgID, id, status, errMsg string, opts ...JobUpdateOption) (*Job, error)
}

// CollectorJob is an open job as the collector receives it.
type CollectorJob struct {
	ID      string         `json:"id"`
	JobType string         `json:"job_type"`
	Config  map[string]any `json:"config"`
}

// JobsResponse answers a jobs request.
type JobsResponse struct {
	Jobs  []CollectorJob `json:"jobs"`
	Error string         `json:"error,omitempty"`
}

// JobStatusReport is a status the collector reports for one of its jobs:
// running before the run, completed or failed with the result after it.
type JobStatusReport struct {
	JobID  string         `json:"job_id"`
	Status string         `json:"status"`
	Error  string         `json:"error,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

func (h *Handler) jobRunRepo() (JobRunRepository, error) {
	repo, ok := h.repo.(JobRunRepository)
	if !ok {
		return nil, errors.New("discovery jobs are not available")
	}
	return repo, nil
}

// CollectorJobs returns the open jobs of a collector.
func (h *Handler) CollectorJobs(ctx context.Context, orgID, collectorID string) ([]CollectorJob, error) {
	repo, err := h.jobRunRepo()
	if err != nil {
		return nil, err
	}
	open, err := repo.OpenCollectorJobs(ctx, orgID, collectorID, maxHandedJobs)
	if err != nil {
		return nil, err
	}
	out := make([]CollectorJob, 0, len(open))
	for i := range open {
		out = append(out, CollectorJob{ID: open[i].ID, JobType: open[i].JobType, Config: open[i].Config})
	}
	return out, nil
}

// RecordCollectorJobStatus sets the status a collector reports for one of
// its jobs: running only for a pending job (the collector runs it only
// once that is recorded), completed or failed only for a running one. A
// result whose run errors left no record fails the job; a failed job keeps
// the first error and its cause (DIS-08). A job of another collector, or
// one in another status, is not found.
func (h *Handler) RecordCollectorJobStatus(ctx context.Context, orgID, collectorID string, rep *JobStatusReport) (*Job, error) {
	repo, err := h.jobRunRepo()
	if err != nil {
		return nil, err
	}
	if !isUUIDText(rep.JobID) {
		return nil, errors.New("not found")
	}
	own := JobOfCollector(collectorID)
	switch rep.Status {
	case JobStatusRunning:
		return repo.UpdateJobStatus(ctx, orgID, rep.JobID, JobStatusRunning, "", own, JobFrom(JobStatusPending))
	case JobStatusCompleted, JobStatusFailed:
	default:
		return nil, &api.FieldError{Field: "status", Message: "invalid discovery job status"}
	}
	result, errs := normalizeRunResult(rep.Result)
	countRunErrors(errs)
	status, errMsg := rep.Status, truncate(rep.Error, maxErrorMessage)
	if result["status"] == ScopeStateFailed {
		status = JobStatusFailed
	}
	if status == JobStatusFailed {
		cause := CauseUnknown
		if len(errs) > 0 {
			cause = errs[0].Cause
			if errMsg == "" {
				errMsg = errs[0].Message
			}
		} else if errMsg != "" {
			cause = ClassifyCause(errMsg)
		}
		if errMsg == "" {
			errMsg = "the collector reported the job failed"
		}
		result["cause"] = cause
	}
	return repo.UpdateJobStatus(ctx, orgID, rep.JobID, status, errMsg, own, JobFrom(JobStatusRunning), JobResult(result))
}

// JobReaper fails the ad-hoc jobs without a report from their collector
// within the deadline, with cause timeout (JOB-01, F-358).
type JobReaper struct {
	pool     *pgxpool.Pool
	repo     JobRunRepository
	Deadline time.Duration
	now      func() time.Time
}

// JobReapKind is the scheduler kind of the reaper.
const JobReapKind = "discovery.jobs.reap"

// NewJobReaper creates the reaper of the jobs of repo; the pool lists the
// organizations. Without a JobRunRepository it is nil.
func NewJobReaper(pool *pgxpool.Pool, repo Repository) *JobReaper {
	jobsRepo, ok := repo.(JobRunRepository)
	if !ok {
		return nil
	}
	return &JobReaper{pool: pool, repo: jobsRepo, Deadline: JobDeadline, now: func() time.Time { return time.Now().UTC() }}
}

// WithJobTracker mirrors the failed jobs into the generic job model
// (JOB-01), like Handler.WithJobTracker.
func (r *JobReaper) WithJobTracker(tracker jobs.Tracker) *JobReaper {
	if repo, ok := r.repo.(jobTrackingRepository); ok {
		repo.setJobTracker(tracker)
	}
	return r
}

// Schedule runs the reaper every minute on the leading replica.
func (r *JobReaper) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: JobReapKind, Spec: "@every 1m", Run: func(ctx context.Context) error {
		reaped, err := r.Run(ctx)
		if reaped > 0 {
			slog.Warn("discovery jobs without collector report failed", "event", "discovery.job.timeout", "jobs", reaped)
		}
		return err
	}}
}

// Run fails the overdue jobs of every organization and returns how many it
// failed. A failing organization does not stop the others; a job that
// moved on meanwhile is skipped.
func (r *JobReaper) Run(ctx context.Context) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	reaped := 0
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		n, oerr := r.RunOrg(ctx, org)
		reaped += n
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return reaped, errors.Join(errs...)
}

// RunOrg fails the overdue jobs of one organization.
func (r *JobReaper) RunOrg(ctx context.Context, orgID string) (int, error) {
	ctx = systemContext(ctx, orgID)
	overdue, err := r.repo.OverdueJobs(ctx, orgID, r.now().Add(-r.Deadline), jobs.DefaultReapBatch)
	if err != nil {
		return 0, err
	}
	reaped := 0
	var errs []error
	for i := range overdue {
		j := &overdue[i]
		msg := fmt.Sprintf("the collector did not take the job within %s", r.Deadline)
		if j.Status == JobStatusRunning {
			msg = fmt.Sprintf("the collector reported no result within %s", r.Deadline)
		}
		_, uerr := r.repo.UpdateJobStatus(ctx, orgID, j.ID, JobStatusFailed, msg, JobFrom(j.Status),
			JobResult(map[string]any{"cause": CauseTimeout}))
		switch {
		case uerr == nil:
			reaped++
		case uerr.Error() != "not found":
			errs = append(errs, fmt.Errorf("reap discovery job %s: %w", j.ID, uerr))
		}
	}
	return reaped, errors.Join(errs...)
}
