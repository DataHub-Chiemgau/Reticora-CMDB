package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Timeout reaper (JOB-01, TEC-14): a job whose worker died or was stopped
// (crash, rolling deploy) stays running. Every minute the leading replica
// (scheduler kind jobs.reap) looks for running jobs whose attempt exceeded
// the timeout of the job plus a grace period, in every organization:
//   - a kind with an executor that runs queued jobs again (the job queue on
//     NATS, a polling worker) is failed like a failed attempt: it goes back
//     to the queue while attempts remain and fails after max_attempts;
//   - every other kind, among them the kinds of the job queue without NATS
//     (in-process jobs: nothing would run them again), is failed at once.
// The error names the lost worker. The job queue itself takes over a job
// whose worker died when the message comes back (Queue.notClaimed); the
// reaper covers the in-process jobs and messages that never come back.

// Reaper defaults.
const (
	ReapKind = "jobs.reap"
	// DefaultReapGrace lets a worker whose attempt reached its timeout
	// record the end itself before the reaper takes the job; it equals the
	// margin of the queue's AckWait.
	DefaultReapGrace = DefaultAckMargin
	// DefaultReapBatch bounds the jobs of one run.
	DefaultReapBatch = 500
)

// Reaper fails the running jobs whose worker was lost.
type Reaper struct {
	svc *Service
	// Requeues reports whether the jobs of a kind have an executor that
	// runs them again; nil: none has.
	Requeues func(kind string) bool
	Grace    time.Duration
	Batch    int
}

// NewReaper creates a reaper on svc; requeues may be nil.
func NewReaper(svc *Service, requeues func(kind string) bool) *Reaper {
	return &Reaper{svc: svc, Requeues: requeues, Grace: DefaultReapGrace, Batch: DefaultReapBatch}
}

// Schedule is the scheduler kind jobs.reap: every minute on the leading
// replica.
func (r *Reaper) Schedule() Schedule {
	return Schedule{Kind: ReapKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: 50 * time.Second,
		Run: func(ctx context.Context) error {
			reaped, err := r.Reap(ctx)
			if reaped > 0 {
				slog.Warn("jobs of lost workers reaped", "jobs", reaped)
			}
			return err
		}}
}

// Reap fails the overdue running jobs and returns how many it failed. A
// job that ended or was taken over meanwhile is skipped; the other jobs of
// the run continue after an error, which is returned.
func (r *Reaper) Reap(ctx context.Context) (int, error) {
	overdue, err := r.svc.repo.RunningOverdue(ctx, r.svc.now().Add(-r.Grace), r.Batch)
	if err != nil {
		return 0, err
	}
	reaped := 0
	var failed []error
	for i := range overdue {
		j := &overdue[i]
		cause := errWorkerLost(time.Duration(j.TimeoutSeconds) * time.Second)
		requeue := r.Requeues != nil && r.Requeues(j.Kind)
		_, ferr := r.svc.failAttempt(ctx, j.OrganizationID, j.ID, j.Attempts, cause, requeue)
		switch {
		case errors.Is(ferr, ErrTransition), errors.Is(ferr, ErrNotFound):
		case ferr != nil:
			failed = append(failed, fmt.Errorf("reap job %s: %w", j.ID, ferr))
		default:
			reaped++
		}
	}
	return reaped, errors.Join(failed...)
}
