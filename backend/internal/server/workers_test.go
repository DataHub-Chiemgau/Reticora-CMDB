package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestDisabledWorkerDoesNotStart covers WP-098 (OPS-02): a worker whose
// switch is off is never started, the others run until Stop; unregistered
// or duplicate workers are refused.
func TestDisabledWorkerDoesNotStart(t *testing.T) {
	var exportRuns, ingestRuns atomic.Int32
	running := make(chan struct{})
	worker := func(counter *atomic.Int32) func(context.Context) {
		return func(ctx context.Context) {
			counter.Add(1)
			running <- struct{}{}
			<-ctx.Done()
		}
	}
	enabled := func(name string) bool { return name != config.WorkerExport }

	set, err := StartWorkers(enabled,
		Worker{Name: config.WorkerExport, Run: worker(&exportRuns)},
		Worker{Name: config.WorkerIngest, Run: worker(&ingestRuns)},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("enabled worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = set.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if exportRuns.Load() != 0 || ingestRuns.Load() != 1 {
		t.Errorf("runs: export %d, ingest %d", exportRuns.Load(), ingestRuns.Load())
	}
	if !slices.Equal(set.Started(), []string{config.WorkerIngest}) || !slices.Equal(set.Disabled(), []string{config.WorkerExport}) {
		t.Errorf("started %v, disabled %v", set.Started(), set.Disabled())
	}

	noop := func(context.Context) {}
	all := func(string) bool { return true }
	if _, err = StartWorkers(all, Worker{Name: "unknown", Run: noop}); err == nil {
		t.Error("unregistered worker accepted")
	}
	if _, err = StartWorkers(all, Worker{Name: config.WorkerIngest, Run: noop}, Worker{Name: config.WorkerIngest, Run: noop}); err == nil {
		t.Error("duplicate worker accepted")
	}
	if _, err = StartWorkers(all, Worker{Name: config.WorkerIngest}); err == nil {
		t.Error("worker without Run accepted")
	}
}

// TestEveryRegisteredWorkerHasASwitch: each name in config.WorkerNames is
// switchable through its environment variable.
func TestEveryRegisteredWorkerHasASwitch(t *testing.T) {
	for _, name := range config.WorkerNames {
		t.Setenv(config.WorkerEnvKey(name), "false")
	}
	cfg := config.Load()
	for _, name := range config.WorkerNames {
		if cfg.WorkerEnabled(name) {
			t.Errorf("%s stays enabled with %s=false", name, config.WorkerEnvKey(name))
		}
	}
}

// TestWorkerMetricsAreRegisteredAndRecorded covers WP-103 (OPS-05): /metrics
// exports run, error, last-success, enabled, up and backlog series per
// worker; StartWorkers marks running and disabled workers, a stopped loop
// drops worker_up to 0 and the backlog probe is sampled.
func TestWorkerMetricsAreRegisteredAndRecorded(t *testing.T) {
	stopEarly := make(chan struct{})
	set, err := StartWorkers(func(name string) bool { return name != config.WorkerExport },
		Worker{Name: config.WorkerExport, Run: func(ctx context.Context) { <-ctx.Done() }},
		Worker{Name: config.WorkerIngest, Run: func(ctx context.Context) { <-ctx.Done() },
			Backlog: func(context.Context) (int, error) { return 42, nil }},
		Worker{Name: config.WorkerScheduler, Run: func(context.Context) { <-stopEarly }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = set.Stop(ctx)
	}()
	set.SampleBacklog(context.Background())
	observability.ObserveWorkerRun(config.WorkerIngest, 0)
	observability.ObserveWorkerRun(config.WorkerIngest, 2)
	close(stopEarly)
	deadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(observability.WorkerUp.WithLabelValues(config.WorkerScheduler)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	mux := chi.NewRouter()
	registerOperational(mux, "test", false, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	for _, want := range []string{
		`reticora_worker_enabled{worker="export"} 0`,
		`reticora_worker_up{worker="export"} 0`,
		`reticora_worker_enabled{worker="ingest"} 1`,
		`reticora_worker_up{worker="ingest"} 1`,
		`reticora_worker_up{worker="scheduler"} 0`,
		`reticora_worker_backlog{worker="ingest"} 42`,
		`reticora_worker_runs_total{worker="ingest"}`,
		`reticora_worker_errors_total{worker="ingest"}`,
		`reticora_worker_last_success_timestamp_seconds{worker="ingest"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

// TestWorkerAlertRulesCoverTheWorkerMetrics: deploy/monitoring alerts on
// stopped workers, errors, missing successful runs and backlog.
func TestWorkerAlertRulesCoverTheWorkerMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/monitoring/worker-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rules := string(raw)
	for _, want := range []string{
		"alert: ReticoraWorkerStopped", "reticora_worker_up", "reticora_worker_enabled",
		"alert: ReticoraWorkerErrors", "reticora_worker_errors_total",
		"alert: ReticoraWorkerNoSuccess", "reticora_worker_last_success_timestamp_seconds",
		"alert: ReticoraWorkerBacklog", "reticora_worker_backlog",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("worker-rules.yaml lacks %s", want)
		}
	}
}
