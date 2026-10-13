package reservation

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// ScheduleKind is the system job that releases expired reservations.
const ScheduleKind = "reservation.expire"

// Sweeper releases expired reservations and emits a reservation_release
// movement per expiry (spec §10: expiration support). It runs as a system
// job of the scheduler (JOB-02), once a minute on the leading replica.
type Sweeper struct {
	repo       Repository
	dispatcher EventDispatcher
}

// NewSweeper creates a sweeper.
func NewSweeper(repo Repository, dispatcher EventDispatcher) *Sweeper {
	return &Sweeper{repo: repo, dispatcher: dispatcher}
}

// Schedule is the sweeper's entry in the scheduler: every minute, a missed
// minute is not repeated (one sweep releases everything overdue).
func (s *Sweeper) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: ScheduleKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: time.Minute, Run: s.SweepOnce}
}

// SweepOnce expires all overdue reservations. Called by the scheduler and
// directly by tests.
func (s *Sweeper) SweepOnce(ctx context.Context) error {
	if s.repo == nil {
		return nil
	}
	expired, err := s.repo.ExpireDue(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("reservation sweep (%d expired): %w", expired, err)
	}
	return nil
}
