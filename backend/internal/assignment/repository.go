package assignment

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for assignments.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error)
	GetByID(orgID, id string) (*Assignment, error)
	Create(a *Assignment) error
	Update(orgID, id string, a *Assignment) error
	Delete(orgID, id string) error
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

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
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
		if filter.AssetID != "" && a.AssetID != filter.AssetID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(a.Notes), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *a)
	}

	total := len(result)
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Assignment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.assignments[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("assignment not found")
	}
	return a, nil
}

func (r *MemoryRepository) Create(a *Assignment) error {
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

func (r *MemoryRepository) Update(orgID, id string, updated *Assignment) error {
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

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.assignments[id]
	if !ok || a.OrganizationID != orgID {
		return fmt.Errorf("assignment not found")
	}
	delete(r.assignments, id)
	return nil
}
