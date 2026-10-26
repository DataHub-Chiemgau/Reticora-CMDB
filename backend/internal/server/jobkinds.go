package server

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
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
	bulkJobKind = jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute}
	// snapshotJobKind only reads and is idempotent: it is retried.
	snapshotJobKind = jobs.Kind{Name: history.SnapshotJobKind, MaxAttempts: 3, Timeout: 15 * time.Minute}
)

// jobKind is a submitted kind and the handler that runs its jobs.
type jobKind struct {
	kind    jobs.Kind
	handler jobs.Handler
}

// applicationJobKinds lists the kinds the application submits with their
// handlers; the bulk kind needs the CI service.
func applicationJobKinds(ciService *ci.Service, historyHandler *history.Handler) []jobKind {
	kinds := []jobKind{{kind: snapshotJobKind, handler: historyHandler.RunSnapshotJob}}
	if ciService != nil {
		kinds = append(kinds, jobKind{kind: bulkJobKind, handler: ciService.RunBulkJob})
	}
	return kinds
}

// registerJobKinds registers the submitted kinds with svc, the job service
// of the requests, and wires their submitters: with queue (NATS) the
// handlers run on the queue and the submitters submit there, without it
// the submitters run their jobs in this process on svc.
func registerJobKinds(svc *jobs.Service, queue *jobs.Queue, ciService *ci.Service, historyHandler *history.Handler) {
	for _, k := range applicationJobKinds(ciService, historyHandler) {
		svc.Register(k.kind)
		if queue != nil {
			queue.Handle(k.kind, k.handler)
		}
	}
	if queue != nil {
		historyHandler.WithQueue(queue)
		if ciService != nil {
			ciService.WithBulkQueue(queue)
		}
		return
	}
	historyHandler.WithJobs(svc)
	if ciService != nil {
		ciService.WithBulkJobs(svc)
	}
}
