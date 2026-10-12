package server

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
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
