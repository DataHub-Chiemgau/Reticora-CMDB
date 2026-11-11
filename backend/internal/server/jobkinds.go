package server

import (
	"context"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// The job kinds the application submits (JOB-01, TEC-14), registered in one
// place: with NATS each kind's handler runs on the JOBS work queue
// (jobQueue.Handle), so a job outlives the replica that accepted it, a
// replica that dies after the claim leaves the message unacknowledged and
// another one takes the job over; without NATS the jobs run in the process
// that accepted them and the reaper (jobs.reap) fails those whose process
// ended.
var (
	// bulkJobKind: a bulk change applied in part has no item progress, so
	// it is never repeated; after a lost worker it fails ("worker lost").
	// A running bulk job can be cancelled: it stops before its next
	// checkpoint and keeps the partial report (JOB-03, F-162).
	bulkJobKind = jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	// assetBulkJobKind (WP-781): the bulk on assets from 100 ids on, like
	// ci.bulk not repeated and cancellable while running.
	assetBulkJobKind = jobs.Kind{Name: asset.BulkJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	// bulkRevertJobKind (WP-305): the revert of a bulk job writes the old
	// values per CI and marks each change set row; it is not repeated and
	// not cancellable while running, and it has no revert itself.
	bulkRevertJobKind = jobs.Kind{Name: jobs.RevertKind, MaxAttempts: 1, Timeout: 30 * time.Minute}
	// snapshotJobKind only reads and is idempotent: it is retried. A
	// running snapshot can be cancelled; its result is dropped.
	snapshotJobKind = jobs.Kind{Name: history.SnapshotJobKind, MaxAttempts: 3, Timeout: 15 * time.Minute, Cancellable: true}
	// exportJobKind is the generic job the export worker mirrors (Track);
	// its worker runs on its own, the registration only declares that a
	// running export can be cancelled (the worker's checkpoint: WP-515).
	// Every other running kind, e.g. review.merge or the discovery
	// mirrors, refuses a cancel (409).
	exportJobKind = jobs.Kind{Name: "export", MaxAttempts: 1, Cancellable: true}
	// templateMigrateJobKind (WP-712): the template migration skips every
	// CI no longer on the source version, so a retry or a redelivery
	// writes nothing twice; it is retried. A running migration can be
	// cancelled between two blocks. There is no revert (E-25).
	templateMigrateJobKind = jobs.Kind{Name: ci.TemplateMigrateJobKind, MaxAttempts: 3, Timeout: time.Hour, Cancellable: true}
	// metamodelTestJobKind (WP-721): the test run of a metamodel draft
	// only reads and stores its report under the lock of the draft; it is
	// not repeated, a lost worker leaves the draft untested. A running
	// test can be cancelled; the draft stays draft.
	metamodelTestJobKind = jobs.Kind{Name: metamodel.TestJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	// importJobKind (WP-301, WP-775): an import run is not repeated, so a
	// second attempt writes no row twice; its run ends on its own within
	// importer.RunTimeout, before the reaper fails it after the timeout of
	// the kind. A holder of import:run cancels a running import (WP-514);
	// it stops before its next row. With NATS it runs on the job queue
	// (importer.ImportHandler.RunJob), without in the process that
	// accepted it.
	importJobKind = jobs.Kind{Name: importer.JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	// documentScanJobKind (WP-734, SEC-09, E-42): the malware scan of a
	// document version only reads the blob and stores its result once; it
	// is retried while clamd fails, its last attempt stores scan_failed and
	// ends without job.failed. It is not cancellable while running.
	documentScanJobKind = jobs.Kind{Name: document.ScanJobKind, MaxAttempts: 5, Timeout: 2 * time.Minute,
		Backoff: jobs.ExponentialBackoff(30*time.Second, 15*time.Minute)}
	// searchReindexJobKind (WP-786): the rebuild of the search index of an
	// organization writes in batches; a failed run keeps the batches written
	// and is not repeated (a new POST /search/reindex starts over). It is
	// not cancellable while running.
	searchReindexJobKind = jobs.Kind{Name: search.ReindexJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute}
	// labelBatchJobKind (WP-763, INV-03): a label batch only reads and
	// stores its PDF under the key of the job; it is not repeated. A
	// running batch can be cancelled; its result is dropped.
	labelBatchJobKind = jobs.Kind{Name: labels.BatchJobKind, MaxAttempts: 1, Timeout: 10 * time.Minute, Cancellable: true}
	// vaultTestJobKind (WP-896, CRD-03): the test of a vault connection
	// only logs in and reads the test entry; it is not repeated and not
	// cancellable while running. Its result is ok or the error class.
	vaultTestJobKind = jobs.Kind{Name: credential.VaultTestJobKind, MaxAttempts: 1, Timeout: 60 * time.Second}
)

// jobKind is a submitted kind and the handler that runs its jobs.
type jobKind struct {
	kind    jobs.Kind
	handler jobs.Handler
}

// applicationJobKinds lists the kinds the application submits with their
// handlers; the bulk kind needs the CI service, its revert the bulk revert
// (nil without the change set store, WP-305), the template migration its
// executor (nil while the module metamodel is off, E-105), the malware scan
// of documents its scanner (nil while the module documents is off). The
// test run of a draft runs on the draft handler (without a database its
// handler fails, the route answers 503 before), an import run on the
// import handler (WP-775), the rebuild of the search index on the search
// handler (WP-786), a label batch on the batch handler (WP-763; without
// blob storage or a database its handler fails, the route answers 503
// before), a bulk on assets on the asset handler (WP-781). The revert jobs
// of asset bulks run on the asset revert (nil while the module inventory
// is off or without the change set store, WP-784). The test of a vault
// connection runs on the credential handler (WP-896).
func applicationJobKinds(ciService *ci.Service, bulkRevert *ci.BulkRevert, historyHandler *history.Handler,
	templateJobs *ci.TemplateMigrationJobs, draftHandler *metamodel.Handler, importHandler *importer.ImportHandler,
	scanner *document.Scanner, searchHandler *search.Handler, labelBatch *labels.BatchHandler, assets *asset.Handler,
	assetRevert *asset.BulkRevert, credentials *credential.Handler) []jobKind {
	kinds := []jobKind{{kind: snapshotJobKind, handler: historyHandler.RunSnapshotJob},
		{kind: metamodelTestJobKind, handler: draftHandler.RunTestJob},
		{kind: importJobKind, handler: importHandler.RunJob},
		{kind: searchReindexJobKind, handler: searchHandler.RunReindexJob},
		{kind: labelBatchJobKind, handler: labelBatch.RunJob},
		{kind: assetBulkJobKind, handler: assets.RunBulkJob},
		{kind: vaultTestJobKind, handler: credentials.RunVaultTestJob}}
	if ciService != nil {
		kinds = append(kinds, jobKind{kind: bulkJobKind, handler: ciService.RunBulkJob})
	}
	if ciService != nil || assetRevert != nil {
		// Without the change set store no bulk job is revertible and no
		// revert job is submitted; a stray one fails. A revert job of an
		// asset bulk runs on the asset revert (asset.IsRevertJob).
		kinds = append(kinds, jobKind{kind: bulkRevertJobKind, handler: func(ctx context.Context, job *jobs.Job) (any, error) {
			switch {
			case asset.IsRevertJob(job) && assetRevert != nil:
				return assetRevert.RunJob(ctx, job)
			case !asset.IsRevertJob(job) && bulkRevert != nil:
				return bulkRevert.RunJob(ctx, job)
			}
			return nil, jobs.ErrNotRevertible
		}})
	}
	if templateJobs != nil {
		kinds = append(kinds, jobKind{kind: templateMigrateJobKind, handler: templateJobs.RunJob})
	}
	if scanner != nil {
		kinds = append(kinds, jobKind{kind: documentScanJobKind, handler: scanner.RunJob})
	}
	return kinds
}

// registerJobKinds registers the submitted kinds and the export kind with
// svc, the job service of the requests, and wires their
// submitters: with queue (NATS) the handlers run on the queue and the
// submitters submit there, without it the submitters run their jobs in
// this process on svc.
//
// Every bulk on CIs is a job of svc (WP-305): below BulkJobFromIDs ids it
// runs in the request, from there on on the queue when there is one. The
// revert of a bulk job is submitted to the queue as well.
//
// The template migration takes progress and cancel from svc in both cases,
// the test run of a draft the state of a running test, the import run its
// progress, cancel and result (WP-775), the label batch reads its jobs
// (WP-763). The malware
// scan of documents runs on the queue only: without it no submitter is
// wired and new versions stay pending_scan (fail-closed, E-42).
func registerJobKinds(svc *jobs.Service, queue *jobs.Queue, ciService *ci.Service, bulkRevert *ci.BulkRevert,
	historyHandler *history.Handler, templateJobs *ci.TemplateMigrationJobs, draftHandler *metamodel.Handler,
	importHandler *importer.ImportHandler, scanner *document.Scanner, searchHandler *search.Handler,
	labelBatch *labels.BatchHandler, assets *asset.Handler, assetRevert *asset.BulkRevert, credentials *credential.Handler) {
	svc.Register(exportJobKind)
	importHandler.WithJobs(svc)
	labelBatch.WithJobs(svc)
	assets.WithBulkJobs(svc)
	if templateJobs != nil {
		templateJobs.WithJobs(svc)
	}
	draftHandler.WithJobs(svc)
	if ciService != nil {
		ciService.WithBulkJobs(svc)
	}
	for _, k := range applicationJobKinds(ciService, bulkRevert, historyHandler, templateJobs, draftHandler, importHandler,
		scanner, searchHandler, labelBatch, assets, assetRevert, credentials) {
		svc.Register(k.kind)
		if queue != nil {
			queue.Handle(k.kind, k.handler)
		}
	}
	if queue != nil {
		historyHandler.WithQueue(queue)
		searchHandler.WithQueue(queue)
		draftHandler.WithQueue(queue)
		importHandler.WithQueue(queue)
		labelBatch.WithQueue(queue)
		assets.WithBulkQueue(queue)
		credentials.WithQueue(queue)
		if templateJobs != nil {
			templateJobs.WithQueue(queue)
		}
		if ciService != nil {
			ciService.WithBulkQueue(queue)
		}
		if bulkRevert != nil {
			bulkRevert.WithQueue(queue)
		}
		if assetRevert != nil {
			assetRevert.WithQueue(queue)
		}
		return
	}
	historyHandler.WithJobs(svc)
	searchHandler.WithJobs(svc)
	credentials.WithJobs(svc)
}
