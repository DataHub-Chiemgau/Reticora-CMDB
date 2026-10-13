package export

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// DefaultJobTTL is how long a completed export remains downloadable.
const DefaultJobTTL = 24 * time.Hour

// ExportBucket is the blob bucket export objects are written to.
const ExportBucket = "exports"

var exportContentTypes = map[string]string{
	"csv":   "text/csv",
	"datev": "text/csv",
	"json":  "application/json",
}

var exportExtensions = map[string]string{
	"csv":   "csv",
	"datev": "csv",
	"json":  "json",
}

// ObjectKey returns the blob key the rendered job is stored under.
func ObjectKey(job *Job) string {
	ext := exportExtensions[job.Format]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("%s/%s.%s", job.OrganizationID, job.ID, ext)
}

// JobWorker processes pending export jobs: it renders the CI set into blob
// storage and marks the job completed with the object key and expiry. All
// replicas share the queue through ClaimPending (FOR UPDATE SKIP LOCKED), so
// jobs survive restarts and are never processed twice.
type JobWorker struct {
	Jobs    JobRepository
	CIs     ci.Repository
	Blobs   blob.Store
	Bucket  string
	JobTTL  time.Duration
	Now     func() time.Time
	onError func(jobID string, err error)
	// failures counts the errors of the current cycle (OPS-05).
	failures int
	// Tracker mirrors the export job into the generic job model (JOB-01);
	// optional.
	Tracker jobs.Tracker
}

// mirror follows the status of an export job in the generic job model; a
// failure there does not stop the export.
func (w *JobWorker) mirror(ctx context.Context, job *Job, status jobs.Status, errMsg string) {
	if w.Tracker == nil {
		return
	}
	if err := w.Tracker.Mirror(ctx, job.OrganizationID, "export_job", job.ID, status, errMsg); err != nil {
		slog.Warn("mirror export job", "job", job.ID, "error", err)
	}
}

// NewJobWorker creates a worker. A nil blobs store disables the worker: the
// handler then refuses to accept new jobs instead of queueing work that can
// never complete.
func NewJobWorker(jobs JobRepository, cis ci.Repository, blobs blob.Store) *JobWorker {
	return &JobWorker{
		Jobs:   jobs,
		CIs:    cis,
		Blobs:  blobs,
		Bucket: ExportBucket,
		JobTTL: DefaultJobTTL,
		Now:    func() time.Time { return time.Now().UTC() },
		onError: func(jobID string, err error) {
			slog.Error("export job failed", "job", jobID, "error", err)
		},
	}
}

// report logs err through onError and counts it for the cycle metrics.
func (w *JobWorker) report(jobID string, err error) {
	w.failures++
	w.onError(jobID, err)
}

// Enabled reports whether the worker can persist rendered objects.
func (w *JobWorker) Enabled() bool {
	return w != nil && w.Jobs != nil && w.CIs != nil && w.Blobs != nil
}

// ProcessOne claims and processes a single pending job. It returns false
// when no job was pending, which lets callers stop polling without sleeping.
func (w *JobWorker) ProcessOne(ctx context.Context) bool {
	if !w.Enabled() {
		return false
	}
	jobs, err := w.Jobs.ClaimPending(ctx, 1)
	if err != nil {
		w.report("", fmt.Errorf("claim pending export jobs: %w", err))
		return false
	}
	if len(jobs) == 0 {
		return false
	}
	w.process(ctx, jobs[0])
	return true
}

func (w *JobWorker) process(ctx context.Context, job Job) {
	w.mirror(ctx, &job, jobs.StatusRunning, "")
	// The export runs with the scope its creator had when queueing it
	// (EXP-01, TEN-06): the CI repository reads through database.WithTenant
	// with exactly this scope. A job without a snapshot is refused rather
	// than exported org-wide.
	if job.Scope == nil {
		w.fail(ctx, &job, fmt.Errorf("export job has no scope snapshot"))
		return
	}
	scope := job.Scope.TenantScope(job.OrganizationID, job.InitiatedBy)
	renderCtx := database.ContextWithTenantScope(ctx, &scope)
	var buf bytes.Buffer
	rowCount, err := RenderFormat(renderCtx, w.CIs, job.OrganizationID, job.Format, job.Filters, &buf)
	if err != nil {
		w.fail(ctx, &job, err)
		return
	}

	key := ObjectKey(&job)
	contentType := exportContentTypes[job.Format]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := w.Blobs.Put(ctx, w.Bucket, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), contentType); err != nil {
		w.fail(ctx, &job, fmt.Errorf("store export object: %w", err))
		return
	}

	now := w.Now()
	if err := w.Jobs.CompleteJob(ctx, job.OrganizationID, job.ID, key, rowCount, int64(buf.Len()), now, now.Add(w.JobTTL)); err != nil {
		w.report(job.ID, fmt.Errorf("complete export job: %w", err))
		return
	}
	w.mirror(ctx, &job, jobs.StatusSucceeded, "")
}

func (w *JobWorker) fail(ctx context.Context, job *Job, err error) {
	w.report(job.ID, err)
	if ferr := w.Jobs.FailJob(ctx, job.OrganizationID, job.ID, err.Error(), w.Now()); ferr != nil {
		w.report(job.ID, fmt.Errorf("mark export job failed: %w", ferr))
	}
	w.mirror(ctx, job, jobs.StatusFailed, err.Error())
}

// Run polls for pending jobs until ctx is cancelled. It is the background
// entry point started by cmd/server.
func (w *JobWorker) Run(ctx context.Context, pollInterval time.Duration) {
	if !w.Enabled() {
		return
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		// Drain the queue before sleeping so bursts are processed back to
		// back.
		for w.ProcessOne(ctx) {
		}
		observability.ObserveWorkerRun("export", w.failures)
		w.failures = 0
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
