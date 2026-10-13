package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Update describes a status change: the new status and the fields it sets.
// Nil fields stay as they are.
type Update struct {
	Status     Status
	Progress   *int
	Result     json.RawMessage
	Error      *string
	RunAfter   *time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// IncAttempts counts an attempt (when a job starts).
	IncAttempts bool
}

// Repository stores jobs; every method works within one organization.
type Repository interface {
	// Insert stores a new job; a job with the same idempotency key or
	// reference is returned instead (created false).
	Insert(ctx context.Context, job *Job) (created bool, err error)
	Get(ctx context.Context, orgID, id string) (*Job, error)
	GetByRef(ctx context.Context, orgID, refTable, refID string) (*Job, error)
	List(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Job, int, error)
	// ClaimNext moves the oldest due queued job of the kinds to running.
	ClaimNext(ctx context.Context, orgID string, kinds []string, now time.Time) (*Job, error)
	// Transition applies u when the job's status is one of from.
	Transition(ctx context.Context, orgID, id string, from []Status, u *Update) (*Job, error)
	// RunningSince lists running jobs started before t.
	RunningSince(ctx context.Context, orgID string, t time.Time) ([]Job, error)
	// ClaimID moves the given job to running when it is queued and due;
	// nil when it is not (claimed elsewhere, finished or not yet due).
	ClaimID(ctx context.Context, orgID, id string, now time.Time) (*Job, error)
	// QueuedDue lists due queued jobs of all organizations, oldest first.
	QueuedDue(ctx context.Context, now time.Time, limit int) ([]Job, error)
}

// MemoryRepository keeps jobs in memory (tests and --no-db mode).
type MemoryRepository struct {
	mu   sync.Mutex
	jobs map[string]*Job
	seq  int
}

// NewMemoryRepository creates an empty memory repository.
func NewMemoryRepository() *MemoryRepository { return &MemoryRepository{jobs: map[string]*Job{}} }

// Insert implements Repository.
func (r *MemoryRepository) Insert(_ context.Context, job *Job) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.OrganizationID != job.OrganizationID {
			continue
		}
		if (job.IdempotencyKey != "" && j.Kind == job.Kind && j.IdempotencyKey == job.IdempotencyKey) ||
			(job.RefID != "" && j.RefTable == job.RefTable && j.RefID == job.RefID) {
			*job = *j
			return false, nil
		}
	}
	r.seq++
	job.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	if job.RunAfter.IsZero() {
		job.RunAfter = now
	}
	stored := *job
	r.jobs[job.ID] = &stored
	return true, nil
}

func (r *MemoryRepository) get(orgID, id string) (*Job, error) {
	j, ok := r.jobs[id]
	if !ok || j.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return j, nil
}

// Get implements Repository.
func (r *MemoryRepository) Get(_ context.Context, orgID, id string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	cp := *j
	return &cp, nil
}

// GetByRef implements Repository.
func (r *MemoryRepository) GetByRef(_ context.Context, orgID, refTable, refID string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.OrganizationID == orgID && j.RefTable == refTable && j.RefID == refID {
			cp := *j
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// List implements Repository.
func (r *MemoryRepository) List(_ context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Job, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for _, j := range r.jobs {
		if j.OrganizationID == orgID && (filter.Kind == "" || j.Kind == filter.Kind) &&
			(filter.Status == "" || j.Status == filter.Status) {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) || out[a].ID > out[b].ID })
	total := len(out)
	start := min(page.Offset, total)
	end := total
	if page.Limit > 0 {
		end = min(start+page.Limit, total)
	}
	return out[start:end], total, nil
}

// ClaimNext implements Repository.
func (r *MemoryRepository) ClaimNext(_ context.Context, orgID string, kinds []string, now time.Time) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next *Job
	for _, j := range r.jobs {
		if j.OrganizationID != orgID || j.Status != StatusQueued || j.RunAfter.After(now) || !contains(kinds, j.Kind) {
			continue
		}
		if next == nil || j.RunAfter.Before(next.RunAfter) {
			next = j
		}
	}
	if next == nil {
		return nil, nil
	}
	next.Status, next.Attempts, next.StartedAt, next.UpdatedAt = StatusRunning, next.Attempts+1, &now, now
	cp := *next
	return &cp, nil
}

// Transition implements Repository.
func (r *MemoryRepository) Transition(_ context.Context, orgID, id string, from []Status, u *Update) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	if !containsStatus(from, j.Status) {
		return nil, fmt.Errorf("%w: %s → %s", ErrTransition, j.Status, u.Status)
	}
	applyUpdate(j, u)
	j.UpdatedAt = time.Now().UTC()
	cp := *j
	return &cp, nil
}

// RunningSince implements Repository.
func (r *MemoryRepository) RunningSince(_ context.Context, orgID string, t time.Time) ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for _, j := range r.jobs {
		if j.OrganizationID == orgID && j.Status == StatusRunning && j.StartedAt != nil && j.StartedAt.Before(t) {
			out = append(out, *j)
		}
	}
	return out, nil
}

// ClaimID implements Repository.
func (r *MemoryRepository) ClaimID(_ context.Context, orgID, id string, now time.Time) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	if j.Status != StatusQueued || j.RunAfter.After(now) {
		return nil, nil
	}
	j.Status, j.Attempts, j.StartedAt, j.FinishedAt, j.UpdatedAt = StatusRunning, j.Attempts+1, &now, nil, now
	cp := *j
	return &cp, nil
}

// QueuedDue implements Repository.
func (r *MemoryRepository) QueuedDue(_ context.Context, now time.Time, limit int) ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for _, j := range r.jobs {
		if j.Status == StatusQueued && !j.RunAfter.After(now) {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RunAfter.Before(out[b].RunAfter) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func applyUpdate(j *Job, u *Update) {
	j.Status = u.Status
	if u.Progress != nil {
		j.Progress = *u.Progress
	}
	if u.Result != nil {
		j.Result = u.Result
	}
	if u.Error != nil {
		j.Error = *u.Error
	}
	if u.RunAfter != nil {
		j.RunAfter = *u.RunAfter
	}
	if u.StartedAt != nil {
		j.StartedAt = u.StartedAt
	}
	if u.IncAttempts {
		j.Attempts++
	}
	if u.Status.Terminal() {
		j.FinishedAt = u.FinishedAt
	} else {
		j.FinishedAt = nil
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func containsStatus(list []Status, v Status) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
