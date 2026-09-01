package order

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores internal orders and their positions.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Order, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Order, error)
	Create(ctx context.Context, o *Order) error
	Update(ctx context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error)
	Delete(ctx context.Context, orgID, id string) error
	// SetStatus transitions the order; approve/reject record the actor.
	SetStatus(ctx context.Context, orgID, id, status, actorID string) (*Order, error)
	AddItem(ctx context.Context, item *Item) (*Order, error)
	ListItems(ctx context.Context, orgID, orderID string) ([]Item, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu     sync.RWMutex
	orders map[string]*Order
	items  map[string][]*Item
	seq    int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{orders: map[string]*Order{}, items: map[string][]*Item{}}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Order, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Order
	for _, o := range r.orders {
		if o.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && o.Status != filter.Status {
			continue
		}
		if filter.ClientID != "" && o.ClientID != filter.ClientID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(o.Title), strings.ToLower(filter.Search)) {
			continue
		}
		cp := *o
		cp.Items = r.itemsList(o.ID)
		out = append(out, cp)
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

func (r *MemoryRepository) itemsList(orderID string) []Item {
	var out []Item
	for _, it := range r.items[orderID] {
		out = append(out, *it)
	}
	return out
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.orders[id]
	if !ok || o.OrganizationID != orgID {
		return nil, fmt.Errorf("order not found")
	}
	cp := *o
	cp.Items = r.itemsList(id)
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, o *Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o.ID = r.nextID("ord")
	if o.OrderNumber == "" {
		o.OrderNumber = fmt.Sprintf("ORD-%06d", r.seq)
	}
	if o.Status == "" {
		o.Status = "draft"
	}
	now := time.Now().UTC()
	o.CreatedAt = now
	o.UpdatedAt = now
	r.orders[o.ID] = o
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok || o.OrganizationID != orgID {
		return nil, fmt.Errorf("order not found")
	}
	if req.ClientID != nil {
		o.ClientID = *req.ClientID
	}
	if req.Title != nil {
		o.Title = *req.Title
	}
	if req.Supplier != nil {
		o.Supplier = *req.Supplier
	}
	if req.TotalCost != nil {
		o.TotalCost = *req.TotalCost
	}
	if req.Currency != nil {
		o.Currency = *req.Currency
	}
	if req.Notes != nil {
		o.Notes = *req.Notes
	}
	o.UpdatedAt = time.Now().UTC()
	cp := *o
	cp.Items = r.itemsList(id)
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok || o.OrganizationID != orgID {
		return fmt.Errorf("order not found")
	}
	delete(r.orders, id)
	delete(r.items, id)
	return nil
}

func (r *MemoryRepository) SetStatus(_ context.Context, orgID, id, status, actorID string) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok || o.OrganizationID != orgID {
		return nil, fmt.Errorf("order not found")
	}
	o.Status = status
	if status == "approved" || status == "rejected" {
		o.ApprovedBy = actorID
	}
	o.UpdatedAt = time.Now().UTC()
	cp := *o
	cp.Items = r.itemsList(id)
	return &cp, nil
}

func (r *MemoryRepository) AddItem(_ context.Context, item *Item) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[item.OrderID]
	if !ok || o.OrganizationID != item.OrganizationID {
		return nil, fmt.Errorf("order not found")
	}
	item.ID = r.nextID("itm")
	item.CreatedAt = time.Now().UTC()
	r.items[item.OrderID] = append(r.items[item.OrderID], item)
	cp := *o
	cp.Items = r.itemsList(item.OrderID)
	return &cp, nil
}

func (r *MemoryRepository) ListItems(_ context.Context, orgID, orderID string) ([]Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.orders[orderID]
	if !ok || o.OrganizationID != orgID {
		return nil, fmt.Errorf("order not found")
	}
	return r.itemsList(orderID), nil
}
