package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// DefaultJobTTL is how long a completed export remains downloadable.
const DefaultJobTTL = 24 * time.Hour

// DefaultStaleAfter is how long a running export may go without heartbeat
// before the worker assumes its replica died and queues it again (TEC-14).
const DefaultStaleAfter = 15 * time.Minute

// ExportBucket is the blob bucket export objects are written to.
const ExportBucket = "exports"

var exportContentTypes = map[string]string{
	"csv":   "text/csv",
	"datev": "text/csv; charset=windows-1252",
	"json":  "application/json",
}

var exportExtensions = map[string]string{
	"csv":   "csv",
	"datev": "csv",
	"json":  "json",
}

// ObjectKey returns the blob key the rendered job is stored under (below
// JobPrefix of its organization).
func ObjectKey(job *Job) string {
	ext := exportExtensions[job.Format]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("%s%s.%s", JobPrefix(job.OrganizationID), job.ID, ext)
}

// JobPrefix is the prefix of the keys of the CI exports of an organization
// in ExportBucket (file, manifest, DATEV error report): outside org/<id>/,
// so the purge of a deleted organization removes it separately (WP-1155).
func JobPrefix(orgID string) string { return orgID + "/" }

// JobWorker processes pending export jobs: it renders the CI set into blob
// storage and marks the job completed with the object key and expiry. All
// replicas share the queue through ClaimPending (FOR UPDATE SKIP LOCKED), so
// jobs survive restarts and are never processed twice. While a job runs,
// its worker renews the heartbeat (started_at) every HeartbeatEvery; a job
// whose replica died, and so has no heartbeat for StaleAfter, is queued
// again and resumed, a long export of a live replica is not (F-315). Only a
// running job can be completed, so a late original run cannot complete it
// a second time. A job whose mirrored job was cancelled (DELETE
// /api/v1/jobs/{id}) stops before rendering or before completing (cancelled).
type JobWorker struct {
	Jobs   JobRepository
	CIs    ci.Repository
	Blobs  blob.Store
	Bucket string
	JobTTL time.Duration
	// StaleAfter is how long a running job may go without heartbeat before
	// it is queued again.
	StaleAfter time.Duration
	// HeartbeatEvery is how often the worker renews the heartbeat of the
	// job it runs; zero means StaleAfter/3.
	HeartbeatEvery time.Duration
	Now            func() time.Time
	onError        func(jobID string, err error)
	// failures counts the errors of the current cycle (OPS-05).
	failures int
	// DATEV checks the module and the entitlements again before a DATEV
	// job is rendered (WP-790); nil refuses DATEV jobs (fail closed).
	DATEV *DATEVGate
	// Tracker mirrors the export job into the generic job model (JOB-01);
	// optional. export.completed (NTF-05) is written by Jobs.CompleteJob
	// in the transaction that completes the job (WP-550).
	Tracker jobs.Tracker
	// Policies resolves the field policy of the job's creator when the job
	// is rendered (RBA-07, WP-729): the roles at render time, never rights
	// stored with the job. Without it the export reads no field with field
	// permissions (fail-closed).
	Policies permission.PolicyResolver
	// Assets reads the assets of a DATEV job (WP-310); without it (memory
	// profile) a DATEV job fails with ErrDATEVUnavailable.
	Assets AssetSource
	// Orgs reads the currency and the mapping override of the job's
	// organization for a DATEV job; without it the default mapping applies.
	Orgs OrganizationReader
	// AssetRows reads the assets of a csv or json job with entity asset
	// (WP-782, asset.PGRepository); without it such a job fails with
	// ErrAssetExportUnavailable (fail closed). Separate from Assets, the
	// source of DATEV.
	AssetRows AssetRows
	// IPAMRows reads the subnets and addresses of a csv job with entity
	// subnet or ip_address (WP-921, ipam.PGRepository); without it (memory
	// profile) such a job fails with ErrIPAMExportUnavailable.
	IPAMRows IPAMRows
}

// ManifestKey returns the blob key of the manifest of a rendered job, next
// to its object in the export bucket (WP-729).
func ManifestKey(job *Job) string {
	return ObjectKey(job) + ".manifest.json"
}

// creatorPolicy resolves the field policy of the job's creator with its
// current role assignments: the principal caps nothing, so its levels are
// those of the creator's roles at render time (as if it had a session
// without IdP groups); a creator that is no user (service account) holds
// no role and reads no field with field permissions (fail-closed). The
// scope of the export stays the snapshot of the job (TenantScope).
func (w *JobWorker) creatorPolicy(ctx context.Context, job *Job) (*permission.FieldPolicy, error) {
	if w.Policies == nil {
		slog.Warn("export job without field policy resolver: fields with field permissions are left out", "job", job.ID)
		return nil, nil
	}
	return w.Policies.Resolve(ctx, identity.Principal{Subject: job.InitiatedBy, OrganizationID: job.OrganizationID,
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
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

// CancelledByUser is the error message of an export whose mirrored job
// was cancelled through the job API (DELETE /api/v1/jobs/{id}, JOB-03).
const CancelledByUser = "cancelled by user"

// cancelled reports whether the mirrored job of the export was cancelled:
// the worker's checkpoint before rendering and before completing (JOB-03,
// F-162). A tracker without the checkpoint, or one that fails to answer,
// lets the export go on.
func (w *JobWorker) cancelled(ctx context.Context, job *Job) bool {
	tracker, ok := w.Tracker.(jobs.CancelTracker)
	if !ok {
		return false
	}
	cancelled, err := tracker.IsMirrorCancelled(ctx, job.OrganizationID, "export_job", job.ID)
	if err != nil {
		slog.Warn("check export job for cancel", "job", job.ID, "error", err)
		return false
	}
	return cancelled
}

// stop ends an export whose mirrored job was cancelled: the export job
// fails with CancelledByUser, an object already stored (key not empty)
// is deleted, and nothing is reported; the mirrored job stays cancelled.
func (w *JobWorker) stop(ctx context.Context, job *Job, key string) {
	slog.Info("export job cancelled", "job", job.ID)
	if key != "" {
		w.deleteObjects(ctx, job, objectKeys(job, key)...)
	}
	if err := w.Jobs.FailJob(ctx, job.OrganizationID, job.ID, CancelledByUser, w.Now()); err != nil {
		w.report(job.ID, fmt.Errorf("mark export job cancelled: %w", err))
	}
}

// objectKeys are the stored objects of a rendered job: the export, its
// manifest and, for DATEV, its error report (WP-310).
func objectKeys(job *Job, key string) []string {
	keys := []string{key, ManifestKey(job)}
	if job.Format == FormatDATEV {
		keys = append(keys, ErrorReportKey(job))
	}
	return keys
}

// deleteObjects deletes the stored objects (export, manifest) of a job
// that does not complete (WP-729).
func (w *JobWorker) deleteObjects(ctx context.Context, job *Job, keys ...string) {
	for _, k := range keys {
		if err := w.Blobs.Delete(ctx, w.Bucket, k); err != nil {
			w.report(job.ID, fmt.Errorf("delete object of unfinished export: %w", err))
		}
	}
}

// NewJobWorker creates a worker. A nil blobs store disables the worker: the
// handler then refuses to accept new jobs instead of queueing work that can
// never complete.
func NewJobWorker(jobRepo JobRepository, cis ci.Repository, blobs blob.Store) *JobWorker {
	return &JobWorker{
		Jobs:       jobRepo,
		CIs:        cis,
		Blobs:      blobs,
		Bucket:     ExportBucket,
		JobTTL:     DefaultJobTTL,
		StaleAfter: DefaultStaleAfter,
		Now:        func() time.Time { return time.Now().UTC() },
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
	claimed, err := w.Jobs.ClaimPending(ctx, 1)
	if err != nil {
		w.report("", fmt.Errorf("claim pending export jobs: %w", err))
		return false
	}
	if len(claimed) == 0 {
		return false
	}
	w.process(ctx, &claimed[0])
	return true
}

// keepAlive renews the heartbeat of job until the returned stop is called
// or ctx ends (TEC-14, F-315). A failed renewal is logged; the next one
// tries again.
func (w *JobWorker) keepAlive(ctx context.Context, job *Job) (stop func()) {
	every := w.HeartbeatEvery
	if every <= 0 {
		every = w.StaleAfter / 3
	}
	if every <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.Jobs.Heartbeat(ctx, job.OrganizationID, job.ID, w.Now()); err != nil {
					slog.Warn("renew export job heartbeat", "job", job.ID, "error", err)
				}
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

func (w *JobWorker) process(ctx context.Context, job *Job) {
	// The heartbeat runs until the job is completed or failed.
	defer w.keepAlive(ctx, job)()
	w.mirror(ctx, job, jobs.StatusRunning, "")
	// The export runs with the scope its creator had when queueing it
	// (EXP-01, TEN-06): the CI repository reads through database.WithTenant
	// with exactly this scope. A job without a snapshot is refused rather
	// than exported org-wide.
	if job.Scope == nil {
		w.fail(ctx, job, fmt.Errorf("export job has no scope snapshot"))
		return
	}
	if w.cancelled(ctx, job) {
		w.stop(ctx, job, "")
		return
	}
	scope := job.Scope.TenantScope(job.OrganizationID, job.InitiatedBy)
	renderCtx := database.ContextWithTenantScope(ctx, &scope)
	// A plan or module may have changed since the job was queued; the
	// entitlements are read in the scope of the job (WithRequestTenant).
	// asset:read counts as checked when the job was created.
	if job.Format == FormatDATEV {
		if err := w.DATEV.Allow(renderCtx, job.OrganizationID); err != nil {
			w.fail(ctx, job, err)
			return
		}
	}
	// The field policy of the creator is resolved now, with its roles at
	// render time (WP-729); a resolution error fails the job.
	policy, err := w.creatorPolicy(ctx, job)
	if err != nil {
		w.fail(ctx, job, fmt.Errorf("resolve field permissions of the export: %w", err))
		return
	}
	// DATEV renders assets with the mapping of the organization and an
	// error report (WP-310); CSV and JSON render CIs (render.go) or, with
	// entity asset, the selected assets (asset_render.go, WP-782).
	var buf bytes.Buffer
	var report *bytes.Buffer
	var rowCount int
	var manifest Manifest
	switch {
	case job.Format == FormatDATEV:
		report = &bytes.Buffer{}
		rowCount, manifest, err = w.renderDATEV(renderCtx, job, policy, &buf, report)
	case job.Filters.IsAsset():
		rowCount, manifest, err = w.renderAssets(renderCtx, job, policy, &buf)
	case job.Filters.IsIPAM():
		rowCount, manifest, err = RenderIPAM(renderCtx, w.IPAMRows, job, policy, &buf)
	default:
		rowCount, manifest, err = RenderProjected(renderCtx, w.CIs, job.OrganizationID, job.Format, job.Filters, policy, &buf)
	}
	if err != nil {
		w.fail(ctx, job, err)
		return
	}

	key := ObjectKey(job)
	contentType := exportContentTypes[job.Format]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if perr := w.Blobs.Put(ctx, w.Bucket, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), contentType); perr != nil {
		w.fail(ctx, job, fmt.Errorf("store export object: %w", perr))
		return
	}
	if report != nil {
		if rerr := w.Blobs.Put(ctx, w.Bucket, ErrorReportKey(job), bytes.NewReader(report.Bytes()), int64(report.Len()),
			errorReportContentType); rerr != nil {
			w.deleteObjects(ctx, job, key)
			w.fail(ctx, job, fmt.Errorf("store datev error report: %w", rerr))
			return
		}
	}
	// The manifest of the file lies next to it (WP-729): GET
	// /api/v1/exports/{id} answers it for the completed job.
	rawManifest, err := json.Marshal(&manifest)
	if err != nil {
		w.fail(ctx, job, fmt.Errorf("encode export manifest: %w", err))
		return
	}
	if merr := w.Blobs.Put(ctx, w.Bucket, ManifestKey(job), bytes.NewReader(rawManifest), int64(len(rawManifest)), "application/json"); merr != nil {
		stored := []string{key}
		if report != nil {
			stored = append(stored, ErrorReportKey(job))
		}
		w.deleteObjects(ctx, job, stored...)
		w.fail(ctx, job, fmt.Errorf("store export manifest: %w", merr))
		return
	}

	if w.cancelled(ctx, job) {
		w.stop(ctx, job, key)
		return
	}
	now := w.Now()
	if cerr := w.Jobs.CompleteJob(ctx, job.OrganizationID, job.ID, key, rowCount, int64(buf.Len()), now, now.Add(w.JobTTL)); cerr != nil {
		w.report(job.ID, fmt.Errorf("complete export job: %w", cerr))
		return
	}
	w.mirror(ctx, job, jobs.StatusSucceeded, "")
	// The mirrored job names the export and its size (GET
	// /api/v1/jobs/{id}/result, F-347).
	if tracker, ok := w.Tracker.(jobs.ResultTracker); ok {
		if rerr := tracker.MirrorResult(ctx, job.OrganizationID, "export_job", job.ID,
			map[string]any{"export_id": job.ID, "row_count": rowCount}); rerr != nil {
			slog.Warn("mirror export job result", "job", job.ID, "error", rerr)
		}
	}
}

// fail ends an export with err: the export job and its mirrored job carry
// the sanitized message of jobs.ErrorMessage, the raw error is logged with
// the job (report, F-372).
func (w *JobWorker) fail(ctx context.Context, job *Job, err error) {
	w.report(job.ID, err)
	if ferr := w.Jobs.FailJob(ctx, job.OrganizationID, job.ID, jobs.ErrorMessage(err), w.Now()); ferr != nil {
		w.report(job.ID, fmt.Errorf("mark export job failed: %w", ferr))
	}
	if w.Tracker == nil {
		return
	}
	if merr := jobs.MirrorFailed(ctx, w.Tracker, job.OrganizationID, "export_job", job.ID, err); merr != nil {
		slog.Warn("mirror export job", "job", job.ID, "error", merr)
	}
}

// ResumeStale queues the jobs again whose replica died while they ran
// (status running without heartbeat for StaleAfter); it returns how many.
func (w *JobWorker) ResumeStale(ctx context.Context) int {
	if !w.Enabled() || w.StaleAfter <= 0 {
		return 0
	}
	n, err := w.Jobs.ReclaimStale(ctx, w.Now().Add(-w.StaleAfter))
	if err != nil {
		w.report("", fmt.Errorf("resume stale export jobs: %w", err))
		return 0
	}
	if n > 0 {
		slog.Info("resumed stale export jobs", "count", n)
	}
	return n
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
		w.ResumeStale(ctx)
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
