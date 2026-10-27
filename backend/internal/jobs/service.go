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
	repo  Repository
	now   func() time.Time
	mu    sync.RWMutex
	kinds map[string]Kind
	// cancelPoll is how often the run context of a job asks for a cancel
	// made by another replica (runContext).
	cancelPoll time.Duration
}

// localRuns ends the run context of a job running in this process when a
// service of this process cancels the job (runContext); the job queue and
// the job API may hold services of their own.
var localRuns = struct {
	sync.Mutex
	cancel map[string]context.CancelCauseFunc
}{cancel: map[string]context.CancelCauseFunc{}}

// DefaultCancelPoll is how often the run context of a job asks whether the
// job was cancelled on another replica.
const DefaultCancelPoll = 5 * time.Second

// NewService creates a service on repo.
func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }, kinds: map[string]Kind{},
		cancelPoll: DefaultCancelPoll}
}

// WithCancelPoll sets how often the run context of a job asks for a cancel
// made elsewhere (tests).
func (s *Service) WithCancelPoll(d time.Duration) *Service {
	s.cancelPoll = d
	return s
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
	// The job fails for good: job.failed (NTF-05) is written with the
	// change (WP-550).
	failed, err := s.repo.Transition(ctx, orgID, id, []Status{StatusRunning},
		&Update{Status: StatusFailed, Error: &msg, FinishedAt: &now, ReportFailed: true})
	if err == nil {
		observeEnd(failed)
	}
	return failed, err
}

// MarkReverted records that user by reverted the succeeded job (F-329):
// ErrAlreadyReverted when its revert is recorded already.
func (s *Service) MarkReverted(ctx context.Context, orgID, id, by string) (*Job, error) {
	return s.repo.MarkReverted(ctx, orgID, id, by, s.now())
}

// Cancellable reports whether Cancel would cancel the job now: a queued
// job of every kind, a running job of a kind registered as Cancellable
// (JOB-03, F-162); a finished job never.
func (s *Service) Cancellable(job *Job) bool {
	switch job.Status {
	case StatusQueued:
		return true
	case StatusRunning:
		k, err := s.kind(job.Kind)
		return err == nil && k.Cancellable
	}
	return false
}

// Cancel stops a queued job, or a running job of a cancellable kind: its
// run context in this process ends at once (runContext), its worker stops
// at its next checkpoint (IsCancelled) and records no further status. A
// running job of any other kind is refused with ErrNotCancellable, a
// finished one with ErrTransition.
func (s *Service) Cancel(ctx context.Context, orgID, id string) (*Job, error) {
	job, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	from := []Status{StatusQueued}
	if k, kerr := s.kind(job.Kind); kerr == nil && k.Cancellable {
		from = append(from, StatusRunning)
	}
	now := s.now()
	cancelled, err := s.repo.Transition(ctx, orgID, id, from, &Update{Status: StatusCancelled, FinishedAt: &now})
	if errors.Is(err, ErrTransition) {
		// Running already or started meanwhile, and the kind cannot be
		// cancelled while running.
		if current, gerr := s.repo.Get(ctx, orgID, id); gerr == nil && current.Status == StatusRunning {
			return nil, fmt.Errorf("%w: %s", ErrNotCancellable, job.Kind)
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	observeEnd(cancelled)
	localRuns.Lock()
	stop := localRuns.cancel[id]
	localRuns.Unlock()
	if stop != nil {
		stop(ErrCancelled)
	}
	return cancelled, nil
}

// ErrCancelled is the cause of the run context of a job that was
// cancelled (runContext).
var ErrCancelled = errors.New("job cancelled")

// IsCancelled reports whether the job was cancelled: the checkpoint of the
// worker of a cancellable kind, which then stops and records no status.
func (s *Service) IsCancelled(ctx context.Context, orgID, id string) (bool, error) {
	job, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return false, err
	}
	return job.Status == StatusCancelled, nil
}

// SaveCancelledResult stores the result of a job that was cancelled while
// it ran, e.g. the partial report of a bulk change (BLK-01); the job stays
// cancelled with its finish time. A job in any other status is
// ErrTransition.
func (s *Service) SaveCancelledResult(ctx context.Context, orgID, id string, result any) (*Job, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode job result: %w", err)
	}
	job, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	finished := job.FinishedAt
	if finished == nil {
		now := s.now()
		finished = &now
	}
	return s.repo.Transition(ctx, orgID, id, []Status{StatusCancelled},
		&Update{Status: StatusCancelled, Result: raw, FinishedAt: finished})
}

// runContext returns the run context of a job that runs in this process:
// it ends with the cause ErrCancelled when the job is cancelled, at once
// when a service of this process cancels it (Cancel), within cancelPoll
// when another replica does. stop releases it once the run ended.
func (s *Service) runContext(ctx context.Context, orgID, id string) (run context.Context, stop func()) {
	run, cancel := context.WithCancelCause(ctx)
	localRuns.Lock()
	localRuns.cancel[id] = cancel
	localRuns.Unlock()
	done := make(chan struct{})
	if s.cancelPoll > 0 {
		go func() {
			ticker := time.NewTicker(s.cancelPoll)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-run.Done():
					return
				case <-ticker.C:
					if cancelled, err := s.IsCancelled(run, orgID, id); err == nil && cancelled {
						cancel(ErrCancelled)
						return
					}
				}
			}
		}()
	}
	return run, func() {
		close(done)
		localRuns.Lock()
		delete(localRuns.cancel, id)
		localRuns.Unlock()
		cancel(context.Canceled)
	}
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

// CancelTracker is the optional part of a Tracker whose worker stops a
// mirrored job that was cancelled through the job API (JOB-03, F-162): the
// checkpoint IsCancelled of WP-514, addressed by the row the job mirrors.
type CancelTracker interface {
	IsMirrorCancelled(ctx context.Context, orgID, refTable, refID string) (bool, error)
}

// ResultTracker is the optional part of a Tracker that stores the result of
// a mirrored job once it succeeded (GET /jobs/{id}/result, F-347).
type ResultTracker interface {
	MirrorResult(ctx context.Context, orgID, refTable, refID string, result any) error
}

// IsMirrorCancelled implements CancelTracker: whether the job mirroring the
// row refTable/refID was cancelled; a row without job is not.
func (s *Service) IsMirrorCancelled(ctx context.Context, orgID, refTable, refID string) (bool, error) {
	job, err := s.repo.GetByRef(ctx, orgID, refTable, refID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return job.Status == StatusCancelled, nil
}

// MirrorResult implements ResultTracker: the succeeded job mirroring the row
// refTable/refID keeps result, its status and finish time stay. A job in any
// other status is ErrTransition.
func (s *Service) MirrorResult(ctx context.Context, orgID, refTable, refID string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode job result: %w", err)
	}
	job, err := s.repo.GetByRef(ctx, orgID, refTable, refID)
	if err != nil {
		return err
	}
	_, err = s.repo.Transition(ctx, orgID, job.ID, []Status{StatusSucceeded},
		&Update{Status: StatusSucceeded, Result: raw, FinishedAt: job.FinishedAt})
	return err
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
// status model does not allow is ignored. A failed job whose row is tried
// again (the retried merge of a review item, F-330) runs again: failed →
// running counts the attempt and clears the error of the failed one
// (mirrorRetry).
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
	retry := mirrorRetry(job, status)
	switch {
	case retry:
		// A new attempt: no error and no progress of the failed one.
		none, zero := "", 0
		u.StartedAt, u.IncAttempts, u.Error, u.Progress = &now, true, &none, &zero
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
	if !retry && !CanTransition(job.Status, status) && (job.Status != StatusQueued || !status.Terminal()) {
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

// mirrorRetry reports whether Mirror starts a new attempt of a failed job:
// only a mirrored job, which follows a row of its reference, goes from
// failed back to running, when its row is tried again (F-330). The status
// model keeps failed terminal for every other job (CanTransition): a job
// of the queue that failed for good is never run again.
func mirrorRetry(job *Job, to Status) bool {
	return job.Status == StatusFailed && to == StatusRunning && job.RefTable != "" && job.RefID != ""
}

// RunDetached enqueues a job of kind and runs it right away in the
// background of this process with the caller's context values (tenant,
// scope); run's result becomes the job result, its error fails the job at
// once, whatever attempts remain: nothing would run a queued attempt again,
// as the reaper knows (WP-516). A process ending meanwhile leaves the job
// running until its timeout reaps it (JOB-01). run's context ends when the
// job is cancelled (runContext); the result or error of a cancelled job is
// dropped and the job stays cancelled (JOB-03, F-162).
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
		runCtx, stop := s.runContext(detached, orgID, job.ID)
		result, rerr := run(runCtx)
		stop()
		if cancelled, gerr := s.IsCancelled(detached, orgID, job.ID); gerr == nil && cancelled {
			return
		}
		if rerr != nil {
			if _, ferr := s.fail(detached, orgID, job.ID, rerr, false, 0); ferr != nil {
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
