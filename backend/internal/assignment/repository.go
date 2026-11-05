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

// Repository reads the assignments (GET /api/v1/assignments, the DSAR
// export). Since WP-747 they are a projection of the ledger; only the
// assignment service (Service) writes, over stock_movement.
type Repository interface {
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Assignment, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Assignment, error)
}

// MemoryRepository is an in-memory implementation of Repository; Create
// fills it in tests.
type MemoryRepository struct {
	mu          sync.RWMutex
	assignments map[string]*Assignment
	nextID      int
}

// NewMemoryRepository creates a new in-memory assignment repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{assignments: make(map[string]*Assignment)}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
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
		vi, vj := sortValue(&result[i], column), sortValue(&result[j], column)
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
		for i := range result {
			item := &result[i]
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, *item)
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

// Create adds an assignment (test data; the PostgreSQL repository has no
// writes).
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
