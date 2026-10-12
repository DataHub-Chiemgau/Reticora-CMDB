package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
)

// Worker is a background runner with an operator switch (OPS-02). Run
// blocks until its context is cancelled.
type Worker struct {
	// Name is one of config.WorkerNames; it selects the switch
	// RETICORA_WORKER_<NAME>_ENABLED.
	Name string
	Run  func(ctx context.Context)
}

// WorkerSet holds the background workers of this process.
type WorkerSet struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  []string
	disabled []string
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
	set := &WorkerSet{cancel: cancel}
	for _, w := range workers {
		if !enabled(w.Name) {
			set.disabled = append(set.disabled, w.Name)
			slog.Info("worker disabled", "worker", w.Name, "switch", config.WorkerEnvKey(w.Name))
			continue
		}
		set.started = append(set.started, w.Name)
		set.wg.Add(1)
		go func(run func(context.Context)) {
			defer set.wg.Done()
			run(ctx)
		}(w.Run)
		slog.Info("worker started", "worker", w.Name)
	}
	return set, nil
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
