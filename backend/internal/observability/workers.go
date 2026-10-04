package observability

import "github.com/prometheus/client_golang/prometheus"

// WorkerErrors counts failed runs of the background workers by worker name
// (WP-022, OPS-05). Workers log every failure and increment this counter
// instead of swallowing the error; WP-103 adds the alerting rules.
var WorkerErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "worker_errors_total",
	Help:      "Failed background worker runs by worker.",
}, []string{"worker"})

// RegisterWorkerMetrics registers the worker metrics on reg.
func RegisterWorkerMetrics(reg prometheus.Registerer) {
	reg.MustRegister(WorkerErrors)
}
