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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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

// creatorScope is the scope snapshot of a job queued with ctx: the tenant
// scope of the request (TEN-09, F-163); nil outside a request (system jobs
// and workers), which leaves the job to org-wide principals.
func creatorScope(ctx context.Context) *Scope {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return nil
	}
	return ScopeOf(&scope)
}

// CreatorContext returns ctx with the tenant and the scope of the job's
// creator, the snapshot creatorScope took when the job was queued: a queue
// worker runs the job of a request like that request (TEN-09), so row level
// security shows it the creator's rows only. A job without snapshot (queued
// outside a request) runs organization-wide, the rule of its visibility
// (E-09).
func CreatorContext(ctx context.Context, job *Job) context.Context {
	scope := database.OrgWideScope(job.OrganizationID, job.CreatedBy)
	if s := job.Scope; s != nil {
		set := func(ids []string) database.ScopeSet {
			if ids == nil {
				return database.AllScopes()
			}
			return database.ScopeIDs(ids...)
		}
		scope.Clients, scope.Sites, scope.Teams = set(s.Clients), set(s.Sites), set(s.Teams)
	}
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: job.OrganizationID, UserID: job.CreatedBy})
	return database.ContextWithTenantScope(ctx, &scope)
}

// Enqueue creates a queued job of a registered kind with the scope of its
// creator (creatorScope). With an idempotency key a second call returns the
// first job unchanged (created false).
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
		Scope: creatorScope(ctx),
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

// Due lists the due queued jobs of the given kinds in every organization
// (workers that serve several organizations claim them with ClaimJob).
func (s *Service) Due(ctx context.Context, limit int, kinds ...string) ([]Job, error) {
	all, err := s.repo.QueuedDue(ctx, s.now(), limit)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for i := range all {
		for _, k := range kinds {
			if all[i].Kind == k {
				out = append(out, all[i])
				break
			}
		}
	}
	return out, nil
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
	job, err := s.repo.Transition(ctx, orgID, id, []Status{StatusRunning},
		&Update{Status: StatusSucceeded, Progress: &done, Result: raw, FinishedAt: &now})
	if err == nil {
		observeEnd(job)
	}
	return job, err
}

// Fail records a failed attempt: the job goes back to the queue after the
// backoff of its kind while attempts remain, otherwise it fails.
func (s *Service) Fail(ctx context.Context, orgID, id string, cause error) (*Job, error) {
	return s.fail(ctx, orgID, id, cause, true, 0)
}

// failAttempt fails the given attempt of a running job (reaper): retry as
// in Fail, or at once without a further attempt, whatever attempts remain,
// when no executor would run the job again. A job that started another
// attempt meanwhile is left alone (ErrTransition).
func (s *Service) failAttempt(ctx context.Context, orgID, id string, attempt int, cause error, retry bool) (*Job, error) {
	return s.fail(ctx, orgID, id, cause, retry, attempt)
}

func (s *Service) fail(ctx context.Context, orgID, id string, cause error, retry bool, attempt int) (*Job, error) {
	job, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if attempt > 0 && job.Attempts != attempt {
		return nil, fmt.Errorf("%w: attempt %d of the job ended, attempt %d runs", ErrTransition, attempt, job.Attempts)
	}
	msg := cause.Error()
	now := s.now()
	JobFailures.WithLabelValues(job.Kind).Inc()
	if retry && job.Attempts < job.MaxAttempts {
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
	if err == nil {
		observeEnd(failed)
	}
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
	job, err := s.repo.Transition(ctx, orgID, id, []Status{StatusQueued, StatusRunning},
		&Update{Status: StatusCancelled, FinishedAt: &now})
	if err == nil {
		observeEnd(job)
	}
	return job, err
}

// ErrTimeout is the error of an attempt that exceeded its timeout.
var ErrTimeout = errors.New("job timed out")

// errWorkerLost is the error of an attempt whose worker did not end it
// within its timeout: the worker died or was stopped (crash, rolling
// deploy) and left the job running.
func errWorkerLost(timeout time.Duration) error {
	return fmt.Errorf("%w after %s (worker lost)", ErrTimeout, timeout)
}

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
		if _, ferr := s.Fail(ctx, orgID, j.ID, errWorkerLost(timeout)); ferr != nil {
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
// (idempotent per row) with the scope of its creator (creatorScope); a job
// tracked outside a request takes the scope of its row where the row keeps
// one (export). A row already tracked by a job outside the caller's scope
// counts as tracked. Tracked jobs are executed by their own worker; the
// kind needs no registration.
func (s *Service) Track(ctx context.Context, orgID, kind, refTable, refID, createdBy string) error {
	job := &Job{
		OrganizationID: orgID, Kind: kind, Status: StatusQueued, Params: []byte("{}"),
		CreatedBy: createdBy, MaxAttempts: 1, RefTable: refTable, RefID: refID, RunAfter: s.now(),
		Scope: creatorScope(ctx),
	}
	_, err := s.repo.Insert(ctx, job)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
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
	moved, err := s.repo.Transition(ctx, orgID, job.ID, from, u)
	if errors.Is(err, ErrTransition) {
		return nil
	}
	if err == nil {
		if status == StatusFailed {
			JobFailures.WithLabelValues(moved.Kind).Inc()
		}
		observeEnd(moved)
	}
	return err
}

// RunDetached enqueues a job of kind and runs it right away in the
// background of this process with the caller's context values (tenant,
// scope); run's result becomes the job result, its error fails the job. A
// process ending meanwhile leaves the job running until its timeout reaps
// it (JOB-01).
func (s *Service) RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
	run func(ctx context.Context) (any, error)) (*Job, error) {
	job, _, err := s.Enqueue(ctx, orgID, kind, params, EnqueueOptions{CreatedBy: createdBy})
	if err != nil {
		return nil, err
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		claimed, cerr := s.ClaimJob(detached, orgID, job.ID)
		if cerr != nil || claimed == nil {
			slog.ErrorContext(detached, "claim detached job", "job", job.ID, "kind", kind, "error", cerr)
			return
		}
		result, rerr := run(detached)
		if rerr != nil {
			if _, ferr := s.Fail(detached, orgID, job.ID, rerr); ferr != nil {
				slog.ErrorContext(detached, "record failed detached job", "job", job.ID, "error", ferr)
			}
			return
		}
		if _, serr := s.Succeed(detached, orgID, job.ID, result); serr != nil {
			slog.ErrorContext(detached, "record detached job", "job", job.ID, "error", serr)
		}
	}()
	return job, nil
}
