package keymgmt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores key items and their issue/return assignments.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Item, error)
	Create(ctx context.Context, item *Item) error
	Update(ctx context.Context, orgID, id string, req UpdateItemRequest) (*Item, error)
	Delete(ctx context.Context, orgID, id string) error
	// Issue records a key handover and marks the item issued; it fails when
	// the item is not available.
	Issue(ctx context.Context, a *Assignment) (*Item, error)
	// Return marks the open assignment returned and the item available.
	Return(ctx context.Context, orgID, keyItemID string) (*Item, error)
	ListAssignments(ctx context.Context, orgID, keyItemID string) ([]Assignment, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu          sync.RWMutex
	items       map[string]*Item
	assignments map[string][]*Assignment
	openByItem  map[string]string
	seq         int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: map[string]*Item{}, assignments: map[string][]*Assignment{}, openByItem: map[string]string{}}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Item
	for _, it := range r.items {
		if it.OrganizationID != orgID {
			continue
		}
		if filter.KeyType != "" && it.KeyType != filter.KeyType {
			continue
		}
		if filter.Status != "" && it.Status != filter.Status {
			continue
		}
		if filter.ClientID != "" && it.ClientID != filter.ClientID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(it.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *it)
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	it, ok := r.items[id]
	if !ok || it.OrganizationID != orgID {
		return nil, fmt.Errorf("key item not found")
	}
	cp := *it
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, item *Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item.ID = r.nextID("key")
	if item.Status == "" {
		item.Status = "available"
	}
	if item.KeyType == "" {
		item.KeyType = "physical"
	}
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	r.items[item.ID] = item
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateItemRequest) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	if !ok || it.OrganizationID != orgID {
		return nil, fmt.Errorf("key item not found")
	}
	if req.ClientID != nil {
		it.ClientID = *req.ClientID
	}
	if req.Name != nil {
		it.Name = *req.Name
	}
	if req.KeyType != nil {
		it.KeyType = *req.KeyType
	}
	if req.Identifier != nil {
		it.Identifier = *req.Identifier
	}
	if req.Status != nil {
		it.Status = *req.Status
	}
	if req.Location != nil {
		it.Location = *req.Location
	}
	if req.Notes != nil {
		it.Notes = *req.Notes
	}
	it.UpdatedAt = time.Now().UTC()
	cp := *it
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	if !ok || it.OrganizationID != orgID {
		return fmt.Errorf("key item not found")
	}
	delete(r.items, id)
	delete(r.assignments, id)
	delete(r.openByItem, id)
	return nil
}

func (r *MemoryRepository) Issue(_ context.Context, a *Assignment) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[a.KeyItemID]
	if !ok || it.OrganizationID != a.OrganizationID {
		return nil, fmt.Errorf("key item not found")
	}
	if it.Status != "available" {
		return nil, fmt.Errorf("key item is not available")
	}
	a.ID = r.nextID("ka")
	a.IssuedAt = time.Now().UTC()
	a.CreatedAt = a.IssuedAt
	r.assignments[it.ID] = append(r.assignments[it.ID], a)
	r.openByItem[it.ID] = a.ID
	it.Status = "issued"
	it.UpdatedAt = time.Now().UTC()
	cp := *it
	return &cp, nil
}

func (r *MemoryRepository) Return(_ context.Context, orgID, keyItemID string) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[keyItemID]
	if !ok || it.OrganizationID != orgID {
		return nil, fmt.Errorf("key item not found")
	}
	openID, hasOpen := r.openByItem[keyItemID]
	if !hasOpen {
		return nil, fmt.Errorf("key item is not issued")
	}
	now := time.Now().UTC()
	for _, a := range r.assignments[keyItemID] {
		if a.ID == openID {
			a.ReturnedAt = &now
		}
	}
	delete(r.openByItem, keyItemID)
	it.Status = "available"
	it.UpdatedAt = now
	cp := *it
	return &cp, nil
}

func (r *MemoryRepository) ListAssignments(_ context.Context, orgID, keyItemID string) ([]Assignment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Assignment
	for _, a := range r.assignments[keyItemID] {
		if a.OrganizationID == orgID {
			out = append(out, *a)
		}
	}
	return out, nil
}
