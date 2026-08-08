package export

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryJobRepository is an in-memory JobRepository for tests and the
// explicit --no-db development mode.
type MemoryJobRepository struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
	next  int
}

// NewMemoryJobRepository creates an empty in-memory job repository.
func NewMemoryJobRepository() *MemoryJobRepository {
	return &MemoryJobRepository{jobs: make(map[string]*Job)}
}

func (r *MemoryJobRepository) CreateJob(_ context.Context, orgID string, job *Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	job.ID = fmt.Sprintf("export-%d", r.next)
	job.OrganizationID = orgID
	job.Status = JobStatusPending
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	stored := *job
	r.jobs[job.ID] = &stored
	r.order = append(r.order, job.ID)
	return nil
}

func (r *MemoryJobRepository) GetJob(_ context.Context, orgID, id string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.OrganizationID != orgID {
		return nil, fmt.Errorf("export job not found")
	}
	cp := *job
	return &cp, nil
}

func (r *MemoryJobRepository) ListJobs(_ context.Context, orgID string, limit, offset int) ([]Job, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for i := len(r.order) - 1; i >= 0; i-- {
		job := r.jobs[r.order[i]]
		if job.OrganizationID == orgID {
			out = append(out, *job)
		}
	}
	total := len(out)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return out[offset:end], total, nil
}

func (r *MemoryJobRepository) MarkRunning(_ context.Context, orgID, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.OrganizationID != orgID {
		return fmt.Errorf("export job not found")
	}
	if job.Status != JobStatusPending {
		return fmt.Errorf("export job is not pending")
	}
	job.Status = JobStatusRunning
	at = at.UTC()
	job.StartedAt = &at
	job.UpdatedAt = at
	return nil
}

func (r *MemoryJobRepository) CompleteJob(_ context.Context, orgID, id string, objectKey string, rowCount int, fileSize int64, completedAt, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.OrganizationID != orgID {
		return fmt.Errorf("export job not found")
	}
	job.Status = JobStatusCompleted
	job.ObjectKey = objectKey
	job.RowCount = &rowCount
	job.FileSizeBytes = &fileSize
	completedAt = completedAt.UTC()
	expiresAt = expiresAt.UTC()
	job.CompletedAt = &completedAt
	job.ExpiresAt = &expiresAt
	job.UpdatedAt = completedAt
	return nil
}

func (r *MemoryJobRepository) FailJob(_ context.Context, orgID, id, message string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.OrganizationID != orgID {
		return fmt.Errorf("export job not found")
	}
	job.Status = JobStatusFailed
	job.ErrorMessage = message
	at = at.UTC()
	job.CompletedAt = &at
	job.UpdatedAt = at
	return nil
}

func (r *MemoryJobRepository) ClaimPending(_ context.Context, limit int) ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for _, id := range r.order {
		job := r.jobs[id]
		if job.Status != JobStatusPending {
			continue
		}
		now := time.Now().UTC()
		job.Status = JobStatusRunning
		job.StartedAt = &now
		job.UpdatedAt = now
		out = append(out, *job)
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
