package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Defaults of a kind that sets none.
const (
	DefaultMaxAttempts = 3
	DefaultTimeout     = 15 * time.Minute
)

// DefaultBackoff waits 30 s after the first failure, doubling up to 15 min.
var DefaultBackoff = ExponentialBackoff(30*time.Second, 15*time.Minute)

// Service runs the job lifecycle of JOB-01 on a repository: enqueue,
// claim, progress, success, failure with retry and backoff, timeout,
// cancel.
type Service struct {
	repo   Repository
	now    func() time.Time
	mu     sync.RWMutex
	kinds  map[string]Kind
	events events.Emitter
}

// WithEvents reports finally failed jobs as job.failed (NTF-05).
func (s *Service) WithEvents(e events.Emitter) *Service {
	s.events = e
	return s
}

// NewService creates a service on repo.
func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }, kinds: map[string]Kind{}}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Register adds or replaces a kind; missing settings take the defaults.
func (s *Service) Register(k Kind) {
	if k.MaxAttempts < 1 {
		k.MaxAttempts = DefaultMaxAttempts
	}
	if k.Timeout <= 0 {
		k.Timeout = DefaultTimeout
	}
	if k.Backoff == nil {
		k.Backoff = DefaultBackoff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kinds[k.Name] = k
}

func (s *Service) kind(name string) (Kind, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.kinds[name]
	if !ok {
		return Kind{}, fmt.Errorf("%w: %s", ErrUnknownKind, name)
	}
	return k, nil
}

// EnqueueOptions are the optional settings of a new job.
type EnqueueOptions struct {
	IdempotencyKey string
	CreatedBy      string
	// RunAfter delays the first attempt.
	RunAfter time.Time
}

// Enqueue creates a queued job of a registered kind. With an idempotency
// key a second call returns the first job unchanged (created false).
func (s *Service) Enqueue(ctx context.Context, orgID, kind string, params any, opts EnqueueOptions) (*Job, bool, error) {
	k, err := s.kind(kind)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, false, fmt.Errorf("encode job params: %w", err)
	}
	if params == nil {
		raw = []byte("{}")
	}
	job := &Job{
		OrganizationID: orgID, Kind: kind, Status: StatusQueued, Params: raw,
		IdempotencyKey: opts.IdempotencyKey, CreatedBy: opts.CreatedBy,
		MaxAttempts: k.MaxAttempts, TimeoutSeconds: int(k.Timeout / time.Second), RunAfter: opts.RunAfter,
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = s.now()
	}
	created, err := s.repo.Insert(ctx, job)
	return job, created, err
}

// Claim starts the next due queued job of the given kinds; nil when none
// is due.
func (s *Service) Claim(ctx context.Context, orgID string, kinds ...string) (*Job, error) {
	return s.repo.ClaimNext(ctx, orgID, kinds, s.now())
}

// ClaimJob starts the given job when it is queued and due; nil when it is
// not (another worker holds it, it is finished or not yet due).
func (s *Service) ClaimJob(ctx context.Context, orgID, id string) (*Job, error) {
	return s.repo.ClaimID(ctx, orgID, id, s.now())
}

// Progress records the progress (0–100) of a running job.
func (s *Service) Progress(ctx context.Context, orgID, id string, percent int) error {
	percent = max(0, min(100, percent))
	_, err := s.repo.Transition(ctx, orgID, id, []Status{StatusRunning}, &Update{Status: StatusRunning, Progress: &percent})
	return err
}

// Succeed ends a running job with its result.
func (s *Service) Succeed(ctx context.Context, orgID, id string, result any) (*Job, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode job result: %w", err)
	}
	done, now := 100, s.now()
	return s.repo.Transition(ctx, orgID, id, []Status{StatusRunning},
		&Update{Status: StatusSucceeded, Progress: &done, Result: raw, FinishedAt: &now})
}

// Fail records a failed attempt: the job goes back to the queue after the
// backoff of its kind while attempts remain, otherwise it fails.
func (s *Service) Fail(ctx context.Context, orgID, id string, cause error) (*Job, error) {
	job, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	msg := cause.Error()
	now := s.now()
	if job.Attempts < job.MaxAttempts {
		backoff := DefaultBackoff
		if k, kerr := s.kind(job.Kind); kerr == nil {
			backoff = k.Backoff
		}
		next := now.Add(backoff(job.Attempts))
		return s.repo.Transition(ctx, orgID, id, []Status{StatusRunning},
			&Update{Status: StatusQueued, Error: &msg, RunAfter: &next})
	}
	failed, err := s.repo.Transition(ctx, orgID, id, []Status{StatusRunning},
		&Update{Status: StatusFailed, Error: &msg, FinishedAt: &now})
	if err == nil && s.events != nil {
		if eerr := s.events.Emit(ctx, &events.Event{OrgID: orgID, Type: events.JobFailed, EntityID: failed.ID,
			Data: map[string]any{"id": failed.ID, "kind": failed.Kind, "error": msg, "attempts": failed.Attempts,
				"created_by": failed.CreatedBy}}); eerr != nil {
			slog.Error("report failed job", "job", failed.ID, "error", eerr)
		}
	}
	return failed, err
}

// Cancel stops a queued or running job.
func (s *Service) Cancel(ctx context.Context, orgID, id string) (*Job, error) {
	now := s.now()
	return s.repo.Transition(ctx, orgID, id, []Status{StatusQueued, StatusRunning},
		&Update{Status: StatusCancelled, FinishedAt: &now})
}

// ErrTimeout is the error of an attempt that exceeded its timeout.
var ErrTimeout = errors.New("job timed out")

// ReapTimeouts fails the running jobs whose attempt exceeded the timeout
// of the job; it returns how many it failed.
func (s *Service) ReapTimeouts(ctx context.Context, orgID string) (int, error) {
	now := s.now()
	running, err := s.repo.RunningSince(ctx, orgID, now)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for i := range running {
		j := &running[i]
		timeout := time.Duration(j.TimeoutSeconds) * time.Second
		if timeout <= 0 || j.StartedAt == nil || now.Sub(*j.StartedAt) <= timeout {
			continue
		}
		if _, ferr := s.Fail(ctx, orgID, j.ID, fmt.Errorf("%w after %s", ErrTimeout, timeout)); ferr != nil {
			if errors.Is(ferr, ErrTransition) {
				continue
			}
			return reaped, ferr
		}
		reaped++
	}
	return reaped, nil
}

// Get returns a job of the organization.
func (s *Service) Get(ctx context.Context, orgID, id string) (*Job, error) {
	return s.repo.Get(ctx, orgID, id)
}

// List returns the jobs of the organization, newest first.
func (s *Service) List(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Job, int, error) {
	return s.repo.List(ctx, orgID, filter, page)
}

// Tracker lets a specialised job table (export, discovery) register its
// jobs in the generic model (JOB-01 adapter): Track creates the generic
// job of a row, Mirror follows its status.
type Tracker interface {
	Track(ctx context.Context, orgID, kind, refTable, refID, createdBy string) error
	Mirror(ctx context.Context, orgID, refTable, refID string, status Status, errMsg string) error
}

// Track implements Tracker: a queued job of kind for the row refTable/refID
// (idempotent per row). Tracked jobs are executed by their own worker; the
// kind needs no registration.
func (s *Service) Track(ctx context.Context, orgID, kind, refTable, refID, createdBy string) error {
	job := &Job{
		OrganizationID: orgID, Kind: kind, Status: StatusQueued, Params: []byte("{}"),
		CreatedBy: createdBy, MaxAttempts: 1, RefTable: refTable, RefID: refID, RunAfter: s.now(),
	}
	_, err := s.repo.Insert(ctx, job)
	return err
}

// Mirror implements Tracker: the tracked job takes the status of its row.
// Unknown rows (created before tracking) are tracked first; a change the
// status model does not allow is ignored.
func (s *Service) Mirror(ctx context.Context, orgID, refTable, refID string, status Status, errMsg string) error {
	job, err := s.repo.GetByRef(ctx, orgID, refTable, refID)
	if errors.Is(err, ErrNotFound) {
		if terr := s.Track(ctx, orgID, refTable, refTable, refID, ""); terr != nil {
			return terr
		}
		job, err = s.repo.GetByRef(ctx, orgID, refTable, refID)
	}
	if err != nil {
		return err
	}
	if job.Status == status {
		return nil
	}
	now := s.now()
	u := &Update{Status: status}
	from := []Status{job.Status}
	switch {
	case status == StatusRunning:
		u.StartedAt, u.IncAttempts = &now, true
	case status.Terminal():
		u.FinishedAt = &now
		if status == StatusSucceeded {
			done := 100
			u.Progress = &done
		}
		if job.Status == StatusQueued && status != StatusCancelled {
			// The worker went straight to the end: count the attempt.
			u.StartedAt, u.IncAttempts = &now, true
		}
	}
	if errMsg != "" {
		u.Error = &errMsg
	}
	if !CanTransition(job.Status, status) && (job.Status != StatusQueued || !status.Terminal()) {
		return nil
	}
	_, err = s.repo.Transition(ctx, orgID, job.ID, from, u)
	if errors.Is(err, ErrTransition) {
		return nil
	}
	return err
}
