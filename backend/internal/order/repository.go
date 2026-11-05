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
//
// Errors: ErrNotFound for an order outside the caller's scope,
// ErrNotDraft for a change outside draft, ErrInvalidTransition for a
// transition the state machine does not allow from the current state,
// ErrFourEyes for an approval by the requester and *api.FieldError (422)
// for a supplier, target bin, consumable or asset that does not fit the
// order.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Order, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Order, error)
	// Create stores o; a non-empty Supplier is resolved to the supplier of
	// that name, a missing currency is the organization's. A target bin
	// (TargetLocationID) is a location of kind bin in the scope and, for an
	// order with a client, of that client (WP-847).
	Create(ctx context.Context, o *Order) error
	// Update changes an order in draft; a new target bin, or a new client
	// with a target bin, is checked like on Create.
	Update(ctx context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error)
	// Delete removes an order in draft.
	Delete(ctx context.Context, orgID, id string) error
	// SetStatus moves the order to status along the state machine
	// (CanTransition) under a lock of the row, so of parallel transitions
	// exactly one succeeds. An approval checks the four-eyes rule
	// (actorID differs from requested_by); approve and reject record the
	// actor in approved_by.
	SetStatus(ctx context.Context, orgID, id, status, actorID string) (*Order, error)
	// AddItem adds a position to an order in draft; it books nothing.
	AddItem(ctx context.Context, item *Item) (*Order, error)
	ListItems(ctx context.Context, orgID, orderID string) ([]Item, error)
	// RunInTx runs fn in one transaction of the context's tenant scope;
	// the repository calls fn makes with its context join it (WP-332,
	// database.WithRequestTenantTx). The in-memory repository calls fn
	// without a transaction.
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
	// Receive books the goods receipt rc through book (WP-760, see
	// PGRepository.Receive): ErrNotReceivable outside ordered and
	// partially_received, *api.FieldError (422) for a position, quantity,
	// bin or asset that does not fit.
	Receive(ctx context.Context, rc *Receipt, book Booker) (*Order, error)
}

// checkFourEyes refuses an approval by the requester or of an order
// without a known requester.
func checkFourEyes(requestedBy, actorID string) error {
	if requestedBy == "" || actorID == "" || strings.EqualFold(requestedBy, actorID) {
		return ErrFourEyes
	}
	return nil
}

// MemoryRepository is the in-memory implementation (tests, --no-db). It
// knows no supplier master data and no locations: a supplier alias name and
// a target bin are unknown (422).
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

// get returns the stored order of orgID; the caller holds the lock.
func (r *MemoryRepository) get(orgID, id string) (*Order, error) {
	o, ok := r.orders[id]
	if !ok || o.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return o, nil
}

func (r *MemoryRepository) copyOf(o *Order) *Order {
	cp := *o
	cp.Items = r.itemsList(o.ID)
	return &cp
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	return r.copyOf(o), nil
}

func (r *MemoryRepository) Create(_ context.Context, o *Order) error {
	if strings.TrimSpace(o.Supplier) != "" {
		return fieldError("supplier", "no supplier of the organization has this name")
	}
	if strings.TrimSpace(o.TargetLocationID) != "" {
		return fieldError("target_location_id", msgTargetBin)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	o.ID = r.nextID("ord")
	if o.OrderNumber == "" {
		o.OrderNumber = fmt.Sprintf("ORD-%06d", r.seq)
	}
	if o.Status == "" {
		o.Status = StatusDraft
	}
	o.Supplier, o.SupplierName = "", ""
	now := time.Now().UTC()
	o.CreatedAt = now
	o.UpdatedAt = now
	stored := *o
	r.orders[o.ID] = &stored
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	if o.Status != StatusDraft {
		return nil, ErrNotDraft
	}
	if supplierName(req.Supplier) != "" {
		return nil, fieldError("supplier", "no supplier of the organization has this name")
	}
	if req.TargetLocationID != nil && strings.TrimSpace(*req.TargetLocationID) != "" {
		return nil, fieldError("target_location_id", msgTargetBin)
	}
	if req.TargetLocationID != nil {
		o.TargetLocationID = ""
	}
	if req.ClientID != nil {
		o.ClientID = *req.ClientID
	}
	if req.Title != nil {
		o.Title = *req.Title
	}
	if req.SupplierID != nil {
		o.SupplierID = strings.TrimSpace(*req.SupplierID)
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
	return r.copyOf(o), nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.get(orgID, id)
	if err != nil {
		return err
	}
	if o.Status != StatusDraft {
		return ErrNotDraft
	}
	delete(r.orders, id)
	delete(r.items, id)
	return nil
}

func (r *MemoryRepository) SetStatus(_ context.Context, orgID, id, status, actorID string) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.get(orgID, id)
	if err != nil {
		return nil, err
	}
	if !CanTransition(o.Status, status) {
		return nil, ErrInvalidTransition
	}
	if status == StatusApproved {
		if err := checkFourEyes(o.RequestedBy, actorID); err != nil {
			return nil, err
		}
	}
	o.Status = status
	if status == StatusApproved || status == StatusRejected {
		o.ApprovedBy = actorID
	}
	o.UpdatedAt = time.Now().UTC()
	return r.copyOf(o), nil
}

func (r *MemoryRepository) AddItem(_ context.Context, item *Item) (*Order, error) {
	if err := item.validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.get(item.OrganizationID, item.OrderID)
	if err != nil {
		return nil, err
	}
	if o.Status != StatusDraft {
		return nil, ErrNotDraft
	}
	item.ID = r.nextID("itm")
	item.CreatedAt = time.Now().UTC()
	stored := *item
	r.items[item.OrderID] = append(r.items[item.OrderID], &stored)
	return r.copyOf(o), nil
}

// RunInTx calls fn without a transaction (--no-db).
func (r *MemoryRepository) RunInTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// Receive checks the state and the positions like the PostgreSQL
// repository; it knows no locations, so every bin is unknown (422) and
// nothing is booked.
func (r *MemoryRepository) Receive(_ context.Context, rc *Receipt, _ Booker) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.get(rc.OrganizationID, rc.OrderID)
	if err != nil {
		return nil, err
	}
	if o.Status != StatusOrdered && o.Status != StatusPartiallyReceived {
		return nil, ErrNotReceivable
	}
	if len(rc.Items) == 0 {
		return nil, fieldError("items", "at least one position is required")
	}
	for i := range rc.Items {
		found := false
		for _, it := range r.items[o.ID] {
			found = found || it.ID == rc.Items[i].ItemID
		}
		if !found {
			return nil, fieldError(itemField(i, "item_id"), msgReceiveItem)
		}
	}
	if rc.Items[0].LocationID == "" && o.TargetLocationID == "" {
		return nil, fieldError(itemField(0, "location_id"), msgReceiveNoBin)
	}
	return nil, fieldError(itemField(0, "location_id"), msgReceiveBin)
}

func (r *MemoryRepository) ListItems(_ context.Context, orgID, orderID string) ([]Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, err := r.get(orgID, orderID); err != nil {
		return nil, err
	}
	return r.itemsList(orderID), nil
}
