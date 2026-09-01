package consumable

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores consumables and their stock movements.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Consumable, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Consumable, error)
	Create(ctx context.Context, c *Consumable) error
	Update(ctx context.Context, orgID, id string, req UpdateConsumableRequest) (*Consumable, error)
	Delete(ctx context.Context, orgID, id string) error
	// AddMovement records an in/out movement and adjusts the stock level
	// atomically. It fails when an out movement would drive stock negative.
	AddMovement(ctx context.Context, m *Movement) (*Consumable, error)
	ListMovements(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Movement, int, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu          sync.RWMutex
	consumables map[string]*Consumable
	movements   map[string][]*Movement
	seq         int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{consumables: map[string]*Consumable{}, movements: map[string][]*Movement{}}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Consumable, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Consumable
	for _, c := range r.consumables {
		if c.OrganizationID != orgID {
			continue
		}
		if filter.Category != "" && c.Category != filter.Category {
			continue
		}
		if filter.ClientID != "" && c.ClientID != filter.ClientID {
			continue
		}
		if filter.LowStock && !c.LowStock() {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *c)
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Consumable, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.consumables[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("consumable not found")
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, c *Consumable) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.ID = r.nextID("cons")
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	r.consumables[c.ID] = c
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateConsumableRequest) (*Consumable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.consumables[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("consumable not found")
	}
	if req.ClientID != nil {
		c.ClientID = *req.ClientID
	}
	if req.Name != nil {
		c.Name = *req.Name
	}
	if req.SKU != nil {
		c.SKU = *req.SKU
	}
	if req.Category != nil {
		c.Category = *req.Category
	}
	if req.Unit != nil {
		c.Unit = *req.Unit
	}
	if req.MinLevel != nil {
		c.MinLevel = *req.MinLevel
	}
	if req.Location != nil {
		c.Location = *req.Location
	}
	if req.Notes != nil {
		c.Notes = *req.Notes
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.consumables[id]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("consumable not found")
	}
	delete(r.consumables, id)
	delete(r.movements, id)
	return nil
}

func (r *MemoryRepository) AddMovement(_ context.Context, m *Movement) (*Consumable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.consumables[m.ConsumableID]
	if !ok || c.OrganizationID != m.OrganizationID {
		return nil, fmt.Errorf("consumable not found")
	}
	if m.Direction == "out" && c.StockLevel-m.Quantity < 0 {
		return nil, fmt.Errorf("insufficient stock")
	}
	m.ID = r.nextID("mov")
	m.CreatedAt = time.Now().UTC()
	if m.Direction == "in" {
		c.StockLevel += m.Quantity
	} else {
		c.StockLevel -= m.Quantity
	}
	c.UpdatedAt = time.Now().UTC()
	r.movements[c.ID] = append(r.movements[c.ID], m)
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) ListMovements(_ context.Context, orgID, consumableID string, page api.PaginationParams) ([]Movement, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Movement
	for _, m := range r.movements[consumableID] {
		if m.OrganizationID == orgID {
			out = append(out, *m)
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
