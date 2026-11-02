package observability

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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

// probeTimeout bounds each check of a pass of Cycles.
const probeTimeout = 10 * time.Second

// Cycles records the cycles of a worker that answers requests instead of
// working off a queue (nats_edge, credential_bundles, discovery_scopes;
// OPS-05). Its cycle is a pass: Run makes one when the worker starts and
// one every interval. A pass runs every check of a dependency the answers
// need (the round trip of a NATS connection, the database, a service of the
// worker that must keep running) and adds the requests that failed on the
// worker's side since the previous pass; each failed check is one failure.
// An idle worker therefore records a success only while its dependencies
// answer. A refused request (an unknown or disabled collector) is an
// answer, not a failure. The methods of a nil *Cycles do nothing.
type Cycles struct {
	worker   string
	failures atomic.Int64

	mu     sync.Mutex
	checks []cycleCheck
}

type cycleCheck struct {
	name  string
	check func(ctx context.Context) error
}

// NewCycles creates the cycles of worker (a name of config.WorkerNames).
func NewCycles(worker string) *Cycles { return &Cycles{worker: worker} }

// Check adds a check to every pass; a check of the same name is replaced.
func (c *Cycles) Check(name string, check func(ctx context.Context) error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.checks {
		if c.checks[i].name == name {
			c.checks[i].check = check
			return
		}
	}
	c.checks = append(c.checks, cycleCheck{name: name, check: check})
}

// Stopped makes every following pass fail: the service name of the worker
// ended with err.
func (c *Cycles) Stopped(name string, err error) {
	c.Check(name, func(context.Context) error { return fmt.Errorf("stopped: %w", err) })
}

// Fail counts a request that failed on the worker's side.
func (c *Cycles) Fail() {
	if c != nil {
		c.failures.Add(1)
	}
}

// Pass records one cycle and returns its failures.
func (c *Cycles) Pass(ctx context.Context) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	checks := append([]cycleCheck(nil), c.checks...)
	c.mu.Unlock()
	failures := int(c.failures.Swap(0))
	errs := make([]error, len(checks))
	for i, ch := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		errs[i] = ch.check(checkCtx)
		cancel()
	}
	if ctx.Err() != nil {
		// Shutdown: the checks failed with the context.
		return 0
	}
	for i, err := range errs {
		if err != nil {
			failures++
			slog.Warn("worker dependency check failed", "worker", c.worker, "check", checks[i].name, "error", err)
		}
	}
	ObserveWorkerRun(c.worker, failures)
	return failures
}

// Run makes a pass now and every interval until ctx ends.
func (c *Cycles) Run(ctx context.Context, interval time.Duration) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		c.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
