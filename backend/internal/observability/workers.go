package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Worker metrics (WP-022, WP-103, OPS-05). The label is the worker name of
// its operator switch (config.WorkerNames). Workers log every failure and
// count it instead of swallowing the error; deploy/monitoring alerts on
// stopped workers, errors, missing successful runs and backlog.
var (
	// WorkerErrors counts failures inside background worker runs.
	WorkerErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "worker_errors_total",
		Help:      "Failed background worker runs by worker.",
	}, []string{"worker"})
	// WorkerRuns counts completed cycles (polls, sweeps, batches).
	WorkerRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "worker_runs_total",
		Help:      "Completed background worker cycles by worker.",
	}, []string{"worker"})
	// WorkerLastSuccess is the Unix time of the last cycle without error.
	WorkerLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "worker_last_success_timestamp_seconds",
		Help:      "Unix time of the last background worker cycle without error.",
	}, []string{"worker"})
	// WorkerEnabled is 1 when the worker's switch is on in this process.
	WorkerEnabled = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "worker_enabled",
		Help:      "1 when the background worker is switched on in this process (RETICORA_WORKER_<NAME>_ENABLED).",
	}, []string{"worker"})
	// WorkerUp is 1 while the worker's loop runs.
	WorkerUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "worker_up",
		Help:      "1 while the background worker loop runs; 0 when it is disabled or has stopped.",
	}, []string{"worker"})
	// WorkerBacklog is the number of items waiting for the worker.
	WorkerBacklog = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "worker_backlog",
		Help:      "Items waiting for the background worker (queued jobs, due deliveries, pending messages).",
	}, []string{"worker"})
)

// RegisterWorkerMetrics registers the worker metrics on reg.
func RegisterWorkerMetrics(reg prometheus.Registerer) {
	reg.MustRegister(WorkerErrors, WorkerRuns, WorkerLastSuccess, WorkerEnabled, WorkerUp, WorkerBacklog)
}

// ObserveWorkerRun records one completed cycle of worker with failures
// errors: the run is counted, failures add to the error counter and a cycle
// without failures moves the last-success time.
func ObserveWorkerRun(worker string, failures int) {
	WorkerRuns.WithLabelValues(worker).Inc()
	if failures > 0 {
		WorkerErrors.WithLabelValues(worker).Add(float64(failures))
		return
	}
	WorkerLastSuccess.WithLabelValues(worker).Set(float64(time.Now().Unix()))
}
