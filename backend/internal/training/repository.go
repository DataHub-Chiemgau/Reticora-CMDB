package training

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores training courses and their assignments.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Course, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Course, error)
	Create(ctx context.Context, c *Course) error
	Update(ctx context.Context, orgID, id string, req UpdateCourseRequest) (*Course, error)
	Delete(ctx context.Context, orgID, id string) error
	Assign(ctx context.Context, a *Assignment) (*Assignment, error)
	// Complete marks an assignment completed with an optional proof document.
	Complete(ctx context.Context, orgID, assignmentID, proofObjectKey string) (*Assignment, error)
	ListAssignments(ctx context.Context, orgID, trainingID string) ([]Assignment, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu          sync.RWMutex
	courses     map[string]*Course
	assignments map[string]*Assignment
	seq         int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{courses: map[string]*Course{}, assignments: map[string]*Assignment{}}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Course, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Course
	for _, c := range r.courses {
		if c.OrganizationID != orgID {
			continue
		}
		if filter.Category != "" && c.Category != filter.Category {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(c.Title), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Course, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.courses[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("training not found")
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, c *Course) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.ID = r.nextID("trn")
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	r.courses[c.ID] = c
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateCourseRequest) (*Course, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.courses[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("training not found")
	}
	if req.Title != nil {
		c.Title = *req.Title
	}
	if req.Description != nil {
		c.Description = *req.Description
	}
	if req.Category != nil {
		c.Category = *req.Category
	}
	if req.ValidityMonths != nil {
		c.ValidityMonths = req.ValidityMonths
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.courses[id]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("training not found")
	}
	delete(r.courses, id)
	for aid, a := range r.assignments {
		if a.TrainingID == id {
			delete(r.assignments, aid)
		}
	}
	return nil
}

func (r *MemoryRepository) Assign(_ context.Context, a *Assignment) (*Assignment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.courses[a.TrainingID]; !ok {
		return nil, fmt.Errorf("training not found")
	}
	for _, existing := range r.assignments {
		if existing.TrainingID == a.TrainingID && existing.UserID == a.UserID {
			return nil, fmt.Errorf("user already assigned")
		}
	}
	a.ID = r.nextID("ta")
	a.AssignedAt = time.Now().UTC()
	a.CreatedAt = a.AssignedAt
	if a.Status == "" {
		a.Status = "assigned"
	}
	r.assignments[a.ID] = a
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) Complete(_ context.Context, orgID, assignmentID, proofObjectKey string) (*Assignment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.assignments[assignmentID]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("training assignment not found")
	}
	now := time.Now().UTC()
	a.CompletedAt = &now
	a.ProofObjectKey = proofObjectKey
	a.Status = "completed"
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) ListAssignments(_ context.Context, orgID, trainingID string) ([]Assignment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Assignment
	for _, a := range r.assignments {
		if a.OrganizationID == orgID && a.TrainingID == trainingID {
			out = append(out, *a)
		}
	}
	return out, nil
}
