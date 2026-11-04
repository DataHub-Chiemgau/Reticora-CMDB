package consumable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrHasStock: the consumable still has stock on hand or reserved in a bin
// and is not deleted (409, WP-283).
var ErrHasStock = errors.New("the consumable still has stock in a bin")

// ErrBinRequired: a movement without location_id on a consumable with none
// or several bins (422 on location_id, transitional path until WP-756).
var ErrBinRequired error = &api.FieldError{Field: "location_id",
	Message: "location_id is required unless the consumable has exactly one bin", Status: http.StatusUnprocessableEntity}

// isStockReference reports whether err is the violation of a RESTRICT
// foreign key of stock_level: the delete of a consumable with a row in a
// bin the caller cannot see.
func isStockReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "stock_level"
}

// bookingOf is the booking of a transitional movement: in is a receipt into
// the bin, out a retrieval from it.
func bookingOf(m *Movement) Booking {
	b := Booking{OrganizationID: m.OrganizationID, ConsumableID: m.ConsumableID, MovementType: "receipt",
		ToLocationID: m.LocationID, Quantity: m.Quantity}
	if m.Direction == "out" {
		b.MovementType, b.FromLocationID, b.ToLocationID = "retrieval", m.LocationID, ""
	}
	return b
}

// Repository stores consumables, their stock per bin and their stock
// movements.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Consumable, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Consumable, error)
	Create(ctx context.Context, c *Consumable) error
	Update(ctx context.Context, orgID, id string, req UpdateConsumableRequest) (*Consumable, error)
	Delete(ctx context.Context, orgID, id string) error
	// AddMovement records an in/out movement on a bin and adjusts its
	// stock atomically. It fails with ErrInsufficientStock when an out
	// movement takes more than the bin holds.
	AddMovement(ctx context.Context, m *Movement) (*Consumable, error)
	ListMovements(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Movement, int, error)
	// ListStock returns the stock rows per bin, ordered by bin, at most
	// page.Limit+1 of them (StockPage), and their number.
	ListStock(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Stock, int, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db). It
// keeps the stock per bin like stock_level, with the rules of ApplyBooking
// (Book), without the location checks.
type MemoryRepository struct {
	mu          sync.RWMutex
	consumables map[string]*Consumable
	movements   map[string][]*Movement
	stock       map[string]map[string]*Stock // consumable → bin → stock
	seq         int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{consumables: map[string]*Consumable{}, movements: map[string][]*Movement{},
		stock: map[string]map[string]*Stock{}}
}

// withTotal returns a copy of c with StockLevel, the sum of on_hand over
// its bins. The caller holds the lock.
func (r *MemoryRepository) withTotal(c *Consumable) Consumable {
	cp := *c
	cp.StockLevel = 0
	for _, s := range r.stock[c.ID] {
		cp.StockLevel += s.OnHand
	}
	return cp
}

// Book applies b to the stock per bin (No-DB profile) with the rules of
// ApplyBooking: a take beyond on_hand − reserved is ErrInsufficientStock and
// changes nothing. Every non-empty location id counts as a bin.
func (r *MemoryRepository) Book(_ context.Context, b *Booking) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.book(b)
}

func (r *MemoryRepository) book(b *Booking) error {
	deltas, err := bookingDeltas(b)
	if err != nil {
		return err
	}
	c, ok := r.consumables[b.ConsumableID]
	if !ok || c.OrganizationID != b.OrganizationID {
		return ErrNotFound
	}
	bins := r.stock[c.ID]
	for _, d := range deltas {
		if d.delta >= 0 {
			continue
		}
		s, has := bins[d.locationID]
		if !has || s.OnHand-s.Reserved < -d.delta {
			return ErrInsufficientStock
		}
	}
	if bins == nil {
		bins = map[string]*Stock{}
		r.stock[c.ID] = bins
	}
	now := time.Now().UTC()
	for _, d := range deltas {
		s, has := bins[d.locationID]
		if !has {
			s = &Stock{ID: r.nextID("stock"), OrganizationID: c.OrganizationID, ConsumableID: c.ID, LocationID: d.locationID}
			bins[d.locationID] = s
		}
		s.OnHand += d.delta
		s.UpdatedAt = now
	}
	return nil
}

// ListStock returns the stock rows of the consumable ordered by bin.
func (r *MemoryRepository) ListStock(_ context.Context, orgID, consumableID string, page api.PaginationParams) ([]Stock, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.consumables[consumableID]
	if !ok || c.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	all := make([]Stock, 0, len(r.stock[consumableID]))
	for _, s := range r.stock[consumableID] {
		all = append(all, *s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LocationID < all[j].LocationID })
	out := make([]Stock, 0, len(all))
	for i := range all {
		if page.Cursor != nil && !api.KeysetCompare(all[i].LocationID, all[i].ID, *page.Cursor, stockDir) {
			continue
		}
		if page.Limit > 0 && len(out) > page.Limit {
			break
		}
		out = append(out, all[i])
	}
	return out, len(all), nil
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
		withTotal := r.withTotal(c)
		if filter.LowStock && !withTotal.LowStock() {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, withTotal)
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
		return nil, ErrNotFound
	}
	cp := r.withTotal(c)
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, c *Consumable) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.ID = r.nextID("cons")
	c.StockLevel = 0
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
		return nil, ErrNotFound
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
	if req.Barcode != nil {
		c.Barcode = *req.Barcode
	}
	if req.SupplierID != nil {
		c.SupplierID = *req.SupplierID
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
	if req.Notes != nil {
		c.Notes = *req.Notes
	}
	c.UpdatedAt = time.Now().UTC()
	cp := r.withTotal(c)
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.consumables[id]
	if !ok || c.OrganizationID != orgID {
		return ErrNotFound
	}
	for _, s := range r.stock[id] {
		if s.OnHand > 0 || s.Reserved > 0 {
			return ErrHasStock
		}
	}
	delete(r.consumables, id)
	delete(r.movements, id)
	delete(r.stock, id)
	return nil
}

// AddMovement books in as a receipt into the bin m.LocationID and out as a
// retrieval from it (Book); without a bin the consumable must have exactly
// one.
func (r *MemoryRepository) AddMovement(_ context.Context, m *Movement) (*Consumable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.consumables[m.ConsumableID]
	if !ok || c.OrganizationID != m.OrganizationID {
		return nil, ErrNotFound
	}
	if m.LocationID == "" {
		if len(r.stock[c.ID]) != 1 {
			return nil, ErrBinRequired
		}
		for bin := range r.stock[c.ID] {
			m.LocationID = bin
		}
	}
	b := bookingOf(m)
	if err := r.book(&b); err != nil {
		return nil, err
	}
	m.ID = r.nextID("mov")
	m.CreatedAt = time.Now().UTC()
	c.UpdatedAt = m.CreatedAt
	r.movements[c.ID] = append(r.movements[c.ID], m)
	cp := r.withTotal(c)
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
