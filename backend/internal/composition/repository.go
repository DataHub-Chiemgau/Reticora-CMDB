package composition

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for composition links.
type Repository interface {
	List(ctx context.Context, orgID, parentAssetID string, page api.PaginationParams) ([]Composition, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Composition, error)
	Create(ctx context.Context, c *Composition) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Composition, error)
	Delete(ctx context.Context, orgID, id string) error
	// ParentOf returns the composition a child belongs to, if any.
	ParentOf(ctx context.Context, orgID, childCIID, childAssetID string) (*Composition, error)
}

// ParentOfAsset reports whether an asset participates as a composition child.
// Shared helper used by the asset module to reject parent-owned fields.
func ParentOfAsset(ctx context.Context, repo Repository, orgID, assetID string) (bool, error) {
	c, err := repo.ParentOf(ctx, orgID, "", assetID)
	if err != nil {
		return false, err
	}
	return c != nil, nil
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Composition
	seq   int
}

// NewMemoryRepository creates an empty in-memory composition repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: map[string]*Composition{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID, parentAssetID string, page api.PaginationParams) ([]Composition, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Composition
	for _, c := range r.items {
		if c.OrganizationID != orgID {
			continue
		}
		if parentAssetID != "" && c.ParentAssetID != parentAssetID {
			continue
		}
		out = append(out, *c)
	}
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Composition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.items[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	out := *c
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, c *Composition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (c.ChildCIID == "") == (c.ChildAssetID == "") {
		return fmt.Errorf("exactly one of child_ci_id or child_asset_id is required")
	}
	// A child belongs to at most one parent.
	for _, existing := range r.items {
		if existing.OrganizationID != c.OrganizationID {
			continue
		}
		if c.ChildCIID != "" && existing.ChildCIID == c.ChildCIID {
			return fmt.Errorf("child CI already has a parent asset")
		}
		if c.ChildAssetID != "" && existing.ChildAssetID == c.ChildAssetID {
			return fmt.Errorf("child asset already has a parent asset")
		}
	}
	c.ID = r.nextID()
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	stored := *c
	r.items[c.ID] = &stored
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Composition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.items[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Role != nil {
		c.Role = *req.Role
	}
	if req.Position != nil {
		c.Position = *req.Position
	}
	if req.ConfigurationOnly != nil {
		c.ConfigurationOnly = *req.ConfigurationOnly
	}
	if req.IndependentlySerialized != nil {
		c.IndependentlySerialized = *req.IndependentlySerialized
	}
	if req.IndependentlyAssignable != nil {
		c.IndependentlyAssignable = *req.IndependentlyAssignable
	}
	if req.IndependentlyLocatable != nil {
		c.IndependentlyLocatable = *req.IndependentlyLocatable
	}
	if req.IndependentlyLifecycleManaged != nil {
		c.IndependentlyLifecycleManaged = *req.IndependentlyLifecycleManaged
	}
	c.UpdatedAt = time.Now().UTC()
	out := *c
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.items[id]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

func (r *MemoryRepository) ParentOf(_ context.Context, orgID, childCIID, childAssetID string) (*Composition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.items {
		if c.OrganizationID != orgID {
			continue
		}
		if childCIID != "" && c.ChildCIID == childCIID {
			out := *c
			return &out, nil
		}
		if childAssetID != "" && c.ChildAssetID == childAssetID {
			out := *c
			return &out, nil
		}
	}
	return nil, nil
}
