package movement

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for movements and quantity items.
type Repository interface {
	// Record appends a movement. There is intentionally no update or delete:
	// the ledger is append-only (spec §9).
	Record(ctx context.Context, m *Movement) error
	ListMovements(ctx context.Context, orgID string, filter MovementFilter, page api.PaginationParams) ([]Movement, int, error)

	ListItems(ctx context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error)
	GetItem(ctx context.Context, orgID, id string) (*QuantityItem, error)
	CreateItem(ctx context.Context, item *QuantityItem) error
	UpdateItem(ctx context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error)
	DeleteItem(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu        sync.RWMutex
	movements []Movement
	items     map[string]*QuantityItem
	seq       int
}

// NewMemoryRepository creates an empty in-memory movement repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: map[string]*QuantityItem{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// Record appends a movement and adjusts quantity item stock levels.
func (r *MemoryRepository) Record(_ context.Context, m *Movement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !MovementTypes[m.MovementType] {
		return fmt.Errorf("invalid movement_type %q", m.MovementType)
	}
	if m.ItemKind == "" {
		m.ItemKind = "asset"
	}
	if m.AssetID == "" && m.QuantityItemID == "" {
		return fmt.Errorf("asset_id or quantity_item_id is required")
	}
	m.ID = r.nextID()
	m.CreatedAt = time.Now().UTC()
	r.movements = append(r.movements, *m)
	// Quantity stock-level bookkeeping for quantity items.
	if m.QuantityItemID != "" && m.Quantity != nil {
		item, ok := r.items[m.QuantityItemID]
		if ok {
			switch m.MovementType {
			case "receipt", "return", "correction":
				item.StockLevel += *m.Quantity
			default:
				item.StockLevel -= *m.Quantity
			}
			item.UpdatedAt = time.Now().UTC()
		}
	}
	return nil
}

func (r *MemoryRepository) ListMovements(_ context.Context, orgID string, filter MovementFilter, page api.PaginationParams) ([]Movement, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Movement
	for _, m := range r.movements {
		if m.OrganizationID != orgID {
			continue
		}
		if filter.AssetID != "" && m.AssetID != filter.AssetID {
			continue
		}
		if filter.QuantityItemID != "" && m.QuantityItemID != filter.QuantityItemID {
			continue
		}
		if filter.MovementType != "" && m.MovementType != filter.MovementType {
			continue
		}
		if filter.LocationID != "" && m.FromLocationID != filter.LocationID && m.ToLocationID != filter.LocationID {
			continue
		}
		out = append(out, m)
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

func (r *MemoryRepository) ListItems(_ context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []QuantityItem
	for _, item := range r.items {
		if item.OrganizationID == orgID {
			out = append(out, *item)
		}
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

func (r *MemoryRepository) GetItem(_ context.Context, orgID, id string) (*QuantityItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	out := *item
	return &out, nil
}

func (r *MemoryRepository) CreateItem(_ context.Context, item *QuantityItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.items {
		if existing.OrganizationID == item.OrganizationID && item.SKU != "" && existing.SKU == item.SKU {
			return fmt.Errorf("an item with this SKU already exists")
		}
	}
	item.ID = r.nextID()
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Attributes == nil {
		item.Attributes = map[string]any{}
	}
	stored := *item
	r.items[item.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateItem(_ context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != nil {
		item.Name = *req.Name
	}
	if req.Category != nil {
		item.Category = *req.Category
	}
	if req.Unit != nil {
		item.Unit = *req.Unit
	}
	if req.MinLevel != nil {
		item.MinLevel = *req.MinLevel
	}
	if req.LocationID != nil {
		item.LocationID = *req.LocationID
	}
	if req.Notes != nil {
		item.Notes = *req.Notes
	}
	if req.Attributes != nil {
		item.Attributes = req.Attributes
	}
	item.UpdatedAt = time.Now().UTC()
	out := *item
	return &out, nil
}

func (r *MemoryRepository) DeleteItem(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}
