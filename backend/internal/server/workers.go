package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// backlogInterval is how often the backlog of the running workers is
// sampled into reticora_worker_backlog (OPS-05).
const backlogInterval = 30 * time.Second

// Worker is a background runner with an operator switch (OPS-02). Run
// blocks until its context is cancelled.
type Worker struct {
	// Name is one of config.WorkerNames; it selects the switch
	// RETICORA_WORKER_<NAME>_ENABLED.
	Name string
	Run  func(ctx context.Context)
	// Backlog optionally reports the items waiting for the worker; it is
	// sampled into reticora_worker_backlog while the worker runs.
	Backlog func(ctx context.Context) (int, error)
}

// WorkerSet holds the background workers of this process.
type WorkerSet struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  []string
	disabled []string
	backlogs map[string]func(context.Context) (int, error)
}

// StartWorkers starts every worker whose switch is on and skips the others.
// Workers must be registered in config.WorkerNames and appear at most once.
func StartWorkers(enabled func(name string) bool, workers ...Worker) (*WorkerSet, error) {
	seen := map[string]bool{}
	for _, w := range workers {
		switch {
		case !config.IsWorker(w.Name):
			return nil, fmt.Errorf("worker %q is not registered in config.WorkerNames", w.Name)
		case seen[w.Name]:
			return nil, fmt.Errorf("worker %q is registered twice", w.Name)
		case w.Run == nil:
			return nil, fmt.Errorf("worker %q has no Run function", w.Name)
		}
		seen[w.Name] = true
	}

	ctx, cancel := context.WithCancel(context.Background())
	set := &WorkerSet{cancel: cancel, backlogs: map[string]func(context.Context) (int, error){}}
	for _, w := range workers {
		if !enabled(w.Name) {
			set.disabled = append(set.disabled, w.Name)
			observability.WorkerEnabled.WithLabelValues(w.Name).Set(0)
			observability.WorkerUp.WithLabelValues(w.Name).Set(0)
			slog.Info("worker disabled", "worker", w.Name, "switch", config.WorkerEnvKey(w.Name))
			continue
		}
		set.started = append(set.started, w.Name)
		if w.Backlog != nil {
			set.backlogs[w.Name] = w.Backlog
		}
		observability.WorkerEnabled.WithLabelValues(w.Name).Set(1)
		observability.WorkerUp.WithLabelValues(w.Name).Set(1)
		set.wg.Add(1)
		go func(name string, run func(context.Context)) {
			defer set.wg.Done()
			// A loop that returns before shutdown has stopped: worker_up
			// drops to 0 and the WorkerStopped alert fires.
			defer observability.WorkerUp.WithLabelValues(name).Set(0)
			run(ctx)
			if ctx.Err() == nil {
				slog.Error("worker stopped unexpectedly", "worker", name)
			}
		}(w.Name, w.Run)
		slog.Info("worker started", "worker", w.Name)
	}
	if len(set.backlogs) > 0 {
		set.wg.Add(1)
		go func() {
			defer set.wg.Done()
			ticker := time.NewTicker(backlogInterval)
			defer ticker.Stop()
			for {
				set.SampleBacklog(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	return set, nil
}

// SampleBacklog records the current backlog of every running worker that
// reports one; a failed probe counts as a worker error.
func (s *WorkerSet) SampleBacklog(ctx context.Context) {
	for name, backlog := range s.backlogs {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		n, err := backlog(probeCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				observability.WorkerErrors.WithLabelValues(name).Inc()
				slog.Warn("worker backlog probe failed", "worker", name, "error", err)
			}
			continue
		}
		observability.WorkerBacklog.WithLabelValues(name).Set(float64(n))
	}
}

// Started lists the running workers in registration order.
func (s *WorkerSet) Started() []string { return append([]string(nil), s.started...) }

// Disabled lists the workers switched off.
func (s *WorkerSet) Disabled() []string { return append([]string(nil), s.disabled...) }

// Stop cancels every worker and waits for them until ctx expires.
func (s *WorkerSet) Stop(ctx context.Context) error {
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("workers did not stop in time"), ctx.Err())
	}
}
