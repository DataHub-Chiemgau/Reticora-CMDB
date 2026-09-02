package savedview

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for saved views.
type Repository interface {
	List(ctx context.Context, orgID, ownerID string, page api.PaginationParams) ([]View, int, error)
	GetByID(ctx context.Context, orgID, id string) (*View, error)
	Create(ctx context.Context, view *View) error
	Update(ctx context.Context, orgID, id, ownerID string, req UpsertRequest) (*View, error)
	Delete(ctx context.Context, orgID, id, ownerID string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	views map[string]*View
	seq   int
}

// NewMemoryRepository creates an empty in-memory saved view repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{views: map[string]*View{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// List returns the caller's own views plus shared views of the organization.
func (r *MemoryRepository) List(_ context.Context, orgID, ownerID string, page api.PaginationParams) ([]View, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []View
	for _, v := range r.views {
		if v.OrganizationID != orgID {
			continue
		}
		if !v.Shared && v.OwnerID != ownerID {
			continue
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*View, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	out := *v
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, view *View) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.views {
		if existing.OrganizationID == view.OrganizationID &&
			existing.OwnerID == view.OwnerID && existing.Name == view.Name {
			return fmt.Errorf("a saved view with this name already exists")
		}
	}
	view.ID = r.nextID()
	now := time.Now().UTC()
	view.CreatedAt = now
	view.UpdatedAt = now
	stored := *view
	r.views[view.ID] = &stored
	return nil
}

// Update modifies a view. Shared views are editable by anyone in the
// organization; private views only by their owner.
func (r *MemoryRepository) Update(_ context.Context, orgID, id, ownerID string, req UpsertRequest) (*View, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if !v.Shared && v.OwnerID != ownerID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != "" {
		v.Name = req.Name
	}
	if req.EntityKind != "" {
		v.EntityKind = req.EntityKind
	}
	if req.FilterSpec != nil {
		v.FilterSpec = req.FilterSpec
	}
	v.Shared = req.Shared
	v.UpdatedAt = time.Now().UTC()
	out := *v
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id, ownerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID || (!v.Shared && v.OwnerID != ownerID) {
		return fmt.Errorf("not found")
	}
	delete(r.views, id)
	return nil
}
