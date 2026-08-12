package assignment

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for assignments.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Assignment, error)
	Create(ctx context.Context, a *Assignment) error
	Update(ctx context.Context, orgID, id string, a *Assignment) error
	Delete(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu          sync.RWMutex
	assignments map[string]*Assignment
	nextID      int
}

// NewMemoryRepository creates a new in-memory assignment repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{assignments: make(map[string]*Assignment)}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Assignment
	for _, a := range r.assignments {
		if a.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && a.Status != filter.Status {
			continue
		}
		if filter.AssignedTo != "" && a.AssignedTo != filter.AssignedTo {
			continue
		}
		if filter.AssignedBy != "" && a.AssignedBy != filter.AssignedBy {
			continue
		}
		if filter.AssetID != "" && a.AssetID != filter.AssetID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(a.Notes), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *a)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(result[i], column), sortValue(result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Assignment, 0, len(result))
		for _, item := range result {
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Assignment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.assignments[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("assignment not found")
	}
	return a, nil
}

func (r *MemoryRepository) Create(_ context.Context, a *Assignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	a.ID = fmt.Sprintf("assign-%d", r.nextID)
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	a.AssignedAt = now
	r.assignments[a.ID] = a
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, updated *Assignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.assignments[id]
	if !ok || existing.OrganizationID != orgID {
		return fmt.Errorf("assignment not found")
	}
	updated.ID = id
	updated.OrganizationID = orgID
	updated.CreatedAt = existing.CreatedAt
	updated.UpdatedAt = time.Now().UTC()
	r.assignments[id] = updated
	return nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.assignments[id]
	if !ok || a.OrganizationID != orgID {
		return fmt.Errorf("assignment not found")
	}
	delete(r.assignments, id)
	return nil
}
