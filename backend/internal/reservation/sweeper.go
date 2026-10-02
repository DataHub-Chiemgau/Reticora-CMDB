package reservation

import (
	"context"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// Sweeper releases expired reservations on an interval and emits a
// reservation_release movement per expiry (spec §10: expiration support).
type Sweeper struct {
	repo       Repository
	dispatcher EventDispatcher
	interval   time.Duration
}

// NewSweeper creates a sweeper; interval defaults to one minute.
func NewSweeper(repo Repository, dispatcher EventDispatcher, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Sweeper{repo: repo, dispatcher: dispatcher, interval: interval}
}

// Run starts the expiry loop; it stops when the context is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepOnce(ctx)
		}
	}
}

// SweepOnce expires all overdue reservations. Called by the worker loop and
// directly by tests.
func (s *Sweeper) SweepOnce(ctx context.Context) {
	if s.repo == nil {
		return
	}
	expired, err := s.repo.ExpireDue(ctx, time.Now().UTC())
	if err != nil {
		observability.WorkerErrors.WithLabelValues("reservation_sweeper").Inc()
		slog.Error("reservation sweep failed", "expired", expired, "error", err)
	}
}
