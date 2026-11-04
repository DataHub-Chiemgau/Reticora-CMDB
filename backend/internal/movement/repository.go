package movement

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrItemNotFound: the quantity item is unknown, of another organization
	// or outside the caller's scope; all answer 404 alike (TEN-04).
	ErrItemNotFound = errors.New("inventory item not found")
	// ErrAssetNotFound: the asset of a movement is unknown, of another
	// organization or outside the caller's scope (404, TEN-04).
	ErrAssetNotFound = errors.New("asset not found")
	// ErrConflict: the organization has an item with this SKU already (409).
	ErrConflict = errors.New("an item with this SKU already exists")
	// ErrInsufficientStock: the movement would drive the stock level of
	// the quantity item below zero (409, ProblemInsufficientStock).
	ErrInsufficientStock = errors.New("the stock level does not cover the movement")
	// ErrHasMovements: the object is kept by movements of the ledger,
	// which are never deleted (409 with dependencies stock_movement).
	ErrHasMovements = errors.New("the object has stock movements; it cannot be deleted")
)

// IsLedgerReference reports whether err is the violation of a RESTRICT
// foreign key of stock_movement: a delete of an object the ledger keeps
// (WP-744).
func IsLedgerReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "stock_movement"
}

// Repository defines persistence for movements and quantity items.
type Repository interface {
	// Record appends a movement. There is intentionally no update or delete:
	// the ledger is append-only (spec §9).
	Record(ctx context.Context, m *Movement) error
	// ListMovements returns the matching movements newest first on
	// (occurred_at, id), at most page.Limit+1 of them (MovementPage), and
	// their number.
	ListMovements(ctx context.Context, orgID string, filter *MovementFilter, page api.PaginationParams) ([]Movement, int, error)

	ListItems(ctx context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error)
	GetItem(ctx context.Context, orgID, id string) (*QuantityItem, error)
	CreateItem(ctx context.Context, item *QuantityItem) error
	UpdateItem(ctx context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error)
	DeleteItem(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db). It
// applies the types, rules and filters of the service, without lock and
// event.
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

// Record appends a movement and adjusts quantity item stock levels like
// RecordTx: an unknown quantity item is ErrItemNotFound, a shortfall
// ErrInsufficientStock.
func (r *MemoryRepository) Record(_ context.Context, m *Movement) error {
	if err := normalizeMovement(m); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var item *QuantityItem
	if m.ItemKind == "quantity_item" {
		var ok bool
		if item, ok = r.items[m.QuantityItemID]; !ok || item.OrganizationID != m.OrganizationID {
			return ErrItemNotFound
		}
		if item.StockLevel+stockDelta(m.MovementType, *m.Quantity) < 0 {
			return ErrInsufficientStock
		}
		m.ClientID = item.ClientID
	}
	m.ID = r.nextID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.OccurredAt = now
	r.movements = append(r.movements, *m)
	if item != nil {
		item.StockLevel += stockDelta(m.MovementType, *m.Quantity)
		item.UpdatedAt = now
	}
	return nil
}

// matches reports whether m passes filter.
func (filter *MovementFilter) matches(m *Movement) bool {
	movementType, _ := canonicalMovementType(filter.MovementType)
	switch {
	case filter.AssetID != "" && m.AssetID != filter.AssetID,
		filter.ConsumableID != "" && m.ConsumableID != filter.ConsumableID,
		filter.QuantityItemID != "" && m.QuantityItemID != filter.QuantityItemID,
		filter.MovementType != "" && m.MovementType != movementType,
		filter.LocationID != "" && m.FromLocationID != filter.LocationID && m.ToLocationID != filter.LocationID,
		filter.OccurredFrom != nil && m.OccurredAt.Before(*filter.OccurredFrom),
		filter.OccurredTo != nil && m.OccurredAt.After(*filter.OccurredTo):
		return false
	}
	return true
}

func (r *MemoryRepository) ListMovements(_ context.Context, orgID string, filter *MovementFilter, page api.PaginationParams) ([]Movement, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Movement
	for i := range r.movements {
		m := &r.movements[i]
		if m.OrganizationID == orgID && filter.matches(m) {
			out = append(out, *m)
		}
	}
	slices.SortStableFunc(out, func(a, b Movement) int {
		if c := b.OccurredAt.Compare(a.OccurredAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	total := len(out)
	start := min(page.Offset, total)
	if page.Cursor != nil {
		at, id, err := movementPosition(*page.Cursor)
		if err != nil {
			return nil, 0, err
		}
		start = total
		for i := range out {
			if out[i].OccurredAt.Before(at) || (out[i].OccurredAt.Equal(at) && out[i].ID < id) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, total)
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
		return nil, ErrItemNotFound
	}
	out := *item
	return &out, nil
}

func (r *MemoryRepository) CreateItem(_ context.Context, item *QuantityItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.items {
		if existing.OrganizationID == item.OrganizationID && item.SKU != "" && existing.SKU == item.SKU {
			return ErrConflict
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
		return nil, ErrItemNotFound
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
		return ErrItemNotFound
	}
	for i := range r.movements {
		if r.movements[i].QuantityItemID == id {
			return ErrHasMovements
		}
	}
	delete(r.items, id)
	return nil
}
