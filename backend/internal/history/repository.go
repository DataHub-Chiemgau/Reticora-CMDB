package history

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for entity change rows.
type Repository interface {
	// Record appends a change row. The trail is append-only.
	Record(ctx context.Context, change *Change) error
	// List returns the changes of one entity, newest first.
	List(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Change, int, error)
	// TrailUpTo returns the changes of one entity up to a point in time,
	// oldest first, for point-in-time reconstruction.
	TrailUpTo(ctx context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error)
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu      sync.RWMutex
	changes []Change
	seq     int
}

// NewMemoryRepository creates an empty in-memory history repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{}
}

// Record appends a change row.
func (r *MemoryRepository) Record(_ context.Context, change *Change) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	change.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	if change.CreatedAt.IsZero() {
		change.CreatedAt = time.Now().UTC()
	}
	r.changes = append(r.changes, *change)
	return nil
}

// List returns the changes of one entity, newest first.
func (r *MemoryRepository) List(_ context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Change, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Change
	for _, c := range r.changes {
		if c.OrganizationID == orgID && c.EntityType == entityType && c.EntityID == entityID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

// TrailUpTo returns changes oldest-first for replay.
func (r *MemoryRepository) TrailUpTo(_ context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Change
	for _, c := range r.changes {
		if c.OrganizationID == orgID && c.EntityType == entityType && c.EntityID == entityID &&
			!c.CreatedAt.After(at) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
