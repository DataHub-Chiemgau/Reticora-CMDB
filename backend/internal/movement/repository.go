package movement

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrItemNotFound: the article of the alias inventory/items (or of a
	// movement named by quantity_item_id) is unknown, of another
	// organization or outside the caller's scope; all answer 404 alike
	// (TEN-04).
	ErrItemNotFound = errors.New("inventory item not found")
	// ErrAssetNotFound: the asset of a movement is unknown, of another
	// organization or outside the caller's scope (404, TEN-04).
	ErrAssetNotFound = errors.New("asset not found")
	// ErrConflict: the organization has an item with this SKU already (409).
	ErrConflict = errors.New("an item with this SKU already exists")
	// ErrInsufficientStock: the movement would drive the stock of a bin of
	// the consumable below zero (409, ProblemInsufficientStock); it is
	// consumable.ErrInsufficientStock.
	ErrInsufficientStock = consumable.ErrInsufficientStock
	// ErrHasMovements: the object is kept by movements of the ledger,
	// which are never deleted (409 with dependencies stock_movement).
	ErrHasMovements = errors.New("the object has stock movements; it cannot be deleted")
	// errNoConsumables: the in-memory repository has no consumable store
	// (WithConsumables) to keep the articles of the alias.
	errNoConsumables = errors.New("inventory items need the consumable store")
)

// IsLedgerReference reports whether err is the violation of a RESTRICT
// foreign key of stock_movement: a delete of an object the ledger keeps
// (WP-744).
func IsLedgerReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "stock_movement"
}

// Repository defines persistence for movements and the articles of the
// alias inventory/items, which live in consumable and stock_level since
// WP-757 (E-40).
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
	// ConvertItem books the retrieval of one unit and creates the asset in
	// its bin through assets (see Conversion).
	ConvertItem(ctx context.Context, conv *Conversion, assets AssetCreator) error
}

// fieldError is the 422 field error of field with message.
func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkNewItem checks an article created through the alias (WP-757):
// attributes have no target column, a negative stock_level is refused, a
// stock_level > 0 needs the bin location_id for its receipt, and a
// location without stock is not kept (stock lies per bin).
func checkNewItem(item *QuantityItem) error {
	switch {
	case len(item.Attributes) > 0:
		return fieldError("attributes", "attributes are not kept; consumable has no attributes")
	case item.StockLevel < 0:
		return fieldError("stock_level", "stock_level must not be negative")
	case item.StockLevel > 0 && item.LocationID == "":
		return fieldError("location_id", "a stock_level greater than 0 is booked into the bin location_id")
	case item.StockLevel == 0 && item.LocationID != "":
		return fieldError("location_id", "location_id is kept only with stock; book a receipt through POST /api/v1/stock-movements")
	}
	return nil
}

// checkItemUpdate refuses location_id and attributes on PATCH (422,
// WP-757): stock moves by a transfer through POST /api/v1/stock-movements,
// consumable keeps no attributes.
func checkItemUpdate(req *UpdateItemRequest) error {
	if req.LocationID != nil {
		return fieldError("location_id", "stock moves between bins by a transfer through POST /api/v1/stock-movements")
	}
	if req.Attributes != nil {
		return fieldError("attributes", "attributes are not kept; consumable has no attributes")
	}
	return nil
}

// receiptOf is the receipt that books the initial stock of item into its
// bin (CreateItem).
func receiptOf(item *QuantityItem) *Movement {
	quantity := item.StockLevel
	return &Movement{OrganizationID: item.OrganizationID, ItemKind: "consumable", ConsumableID: item.ID,
		MovementType: "receipt", ToLocationID: item.LocationID, Quantity: &quantity, ActorID: item.actorID}
}

// retrievalOf is the retrieval of one unit of the conversion from bin.
func retrievalOf(conv *Conversion, bin, assetID string) *Movement {
	one := 1.0
	return &Movement{OrganizationID: conv.OrganizationID, ItemKind: "consumable", ConsumableID: conv.ItemID,
		MovementType: "retrieval", FromLocationID: bin, Quantity: &one, ActorID: conv.ActorID,
		Reason: "converted to serialized asset " + assetID}
}

// asLocationField reports a field error of the bin of a booking on
// location_id, the field of the alias.
func asLocationField(err error) error {
	var field *api.FieldError
	if errors.As(err, &field) && (field.Field == "to_location_id" || field.Field == "from_location_id") {
		return &api.FieldError{Field: "location_id", Message: field.Message, Status: field.Status}
	}
	return err
}

// asItemError maps the errors of the consumable store to those of the
// alias: an invisible consumable is ErrItemNotFound.
func asItemError(err error) error {
	if errors.Is(err, consumable.ErrNotFound) {
		return ErrItemNotFound
	}
	return err
}

// aliasError maps err of the movement m: a consumable named by
// quantity_item_id that is not visible is ErrItemNotFound (WP-757).
func aliasError(m *Movement, err error) error {
	if m.itemAlias {
		return asItemError(err)
	}
	return err
}

// ConsumableStock is the consumable store of the No-DB profile
// (consumable.MemoryRepository): the articles of the alias and their stock
// per bin; Book applies a booking with the rules of
// consumable.ApplyBooking, OnlyBin names the only bin.
type ConsumableStock interface {
	consumable.Repository
	Book(ctx context.Context, b *consumable.Booking) error
	OnlyBin(ctx context.Context, orgID, consumableID string) (string, error)
}

// MemoryRepository is an in-memory implementation (tests, --no-db). It
// applies the types, rules and filters of the service, without lock,
// event and transaction; consumable movements book on the stock per bin
// of consumables, which also keeps the articles of the alias.
type MemoryRepository struct {
	mu          sync.RWMutex
	movements   []Movement
	consumables ConsumableStock
	seq         int
}

// WithConsumables books the consumable movements on stock, the consumable
// store of the same profile (WP-756), which keeps the articles of the
// alias as well (WP-757); without it a consumable movement is
// consumable.ErrNotFound.
func (r *MemoryRepository) WithConsumables(stock ConsumableStock) *MemoryRepository {
	r.consumables = stock
	return r
}

// NewMemoryRepository creates an empty in-memory movement repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// Record appends a movement and books the stock per bin of a consumable
// like RecordTx: an unknown consumable is consumable.ErrNotFound
// (ErrItemNotFound when named by quantity_item_id), a shortfall
// ErrInsufficientStock; a refused booking appends nothing.
func (r *MemoryRepository) Record(ctx context.Context, m *Movement) error {
	if err := normalizeMovement(m); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.ItemKind == "consumable" {
		if r.consumables == nil {
			return aliasError(m, consumable.ErrNotFound)
		}
		if m.onlyBin {
			bin, err := r.consumables.OnlyBin(ctx, m.OrganizationID, m.ConsumableID)
			if err != nil {
				return aliasError(m, err)
			}
			placeOnlyBin(m, bin)
		}
		if err := r.consumables.Book(ctx, bookingOf(m)); err != nil {
			return aliasError(m, err)
		}
	}
	m.ID = r.nextID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.OccurredAt = now
	r.movements = append(r.movements, *m)
	return nil
}

// matches reports whether m passes filter.
func (filter *MovementFilter) matches(m *Movement) bool {
	movementType, _ := canonicalMovementType(filter.MovementType)
	switch {
	case filter.AssetID != "" && m.AssetID != filter.AssetID,
		filter.ConsumableID != "" && m.ConsumableID != filter.ConsumableID,
		filter.QuantityItemID != "" && m.ConsumableID != filter.QuantityItemID,
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

// itemOf is the answer form of the article c with the first of its total
// stock rows: the bin is named when it is the only one.
func itemOf(c *consumable.Consumable, stock []consumable.Stock, total int) QuantityItem {
	item := QuantityItem{ID: c.ID, OrganizationID: c.OrganizationID, ClientID: c.ClientID, SKU: c.SKU, Name: c.Name,
		Category: c.Category, Unit: c.Unit, StockLevel: c.StockLevel, MinLevel: c.MinLevel, Notes: c.Notes,
		Attributes: map[string]any{}, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if total == 1 && len(stock) > 0 {
		item.LocationID = stock[0].LocationID
	}
	return item
}

// item returns the article c in the answer form.
func (r *MemoryRepository) item(ctx context.Context, c *consumable.Consumable) (QuantityItem, error) {
	stock, total, err := r.consumables.ListStock(ctx, c.OrganizationID, c.ID, api.PaginationParams{Limit: 1})
	if err != nil {
		return QuantityItem{}, asItemError(err)
	}
	return itemOf(c, stock, total), nil
}

func (r *MemoryRepository) ListItems(ctx context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error) {
	if r.consumables == nil {
		return nil, 0, nil
	}
	list, total, err := r.consumables.List(ctx, orgID, consumable.FilterParams{}, page)
	if err != nil {
		return nil, 0, err
	}
	out := make([]QuantityItem, 0, len(list))
	for i := range list {
		item, itemErr := r.item(ctx, &list[i])
		if itemErr != nil {
			return nil, 0, itemErr
		}
		out = append(out, item)
	}
	return out, total, nil
}

func (r *MemoryRepository) GetItem(ctx context.Context, orgID, id string) (*QuantityItem, error) {
	if r.consumables == nil {
		return nil, ErrItemNotFound
	}
	c, err := r.consumables.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, asItemError(err)
	}
	item, err := r.item(ctx, c)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// CreateItem creates the article as a consumable and books a stock_level
// > 0 as a receipt into its bin; a refused receipt removes the article
// again (no transaction in the No-DB profile).
func (r *MemoryRepository) CreateItem(ctx context.Context, item *QuantityItem) error {
	if err := checkNewItem(item); err != nil {
		return err
	}
	if r.consumables == nil {
		return errNoConsumables
	}
	if item.SKU != "" {
		all, _, err := r.consumables.List(ctx, item.OrganizationID, consumable.FilterParams{}, api.PaginationParams{Limit: math.MaxInt32})
		if err != nil {
			return err
		}
		for i := range all {
			if all[i].SKU == item.SKU {
				return ErrConflict
			}
		}
	}
	c := &consumable.Consumable{OrganizationID: item.OrganizationID, ClientID: item.ClientID, SKU: item.SKU, Name: item.Name,
		Category: item.Category, Unit: item.Unit, MinLevel: item.MinLevel, Notes: item.Notes}
	if c.Category == "" {
		c.Category = "general"
	}
	if c.Unit == "" {
		c.Unit = "pcs"
	}
	if err := r.consumables.Create(ctx, c); err != nil {
		return err
	}
	item.ID, item.Category, item.Unit, item.CreatedAt, item.UpdatedAt = c.ID, c.Category, c.Unit, c.CreatedAt, c.UpdatedAt
	item.Attributes = map[string]any{}
	if item.StockLevel > 0 {
		if err := r.Record(ctx, receiptOf(item)); err != nil {
			_ = r.consumables.Delete(ctx, c.OrganizationID, c.ID)
			return asLocationField(err)
		}
	}
	return nil
}

func (r *MemoryRepository) UpdateItem(ctx context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error) {
	if err := checkItemUpdate(&req); err != nil {
		return nil, err
	}
	if r.consumables == nil {
		return nil, ErrItemNotFound
	}
	c, err := r.consumables.Update(ctx, orgID, id, consumable.UpdateConsumableRequest{Name: req.Name, Category: req.Category,
		Unit: req.Unit, MinLevel: req.MinLevel, Notes: req.Notes})
	if err != nil {
		return nil, asItemError(err)
	}
	item, err := r.item(ctx, c)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// DeleteItem deletes the article like DELETE /api/v1/consumables/{id}; one
// kept by movements is ErrHasMovements.
func (r *MemoryRepository) DeleteItem(ctx context.Context, orgID, id string) error {
	if r.consumables == nil {
		return ErrItemNotFound
	}
	if _, err := r.consumables.GetByID(ctx, orgID, id); err != nil {
		return asItemError(err)
	}
	r.mu.RLock()
	for i := range r.movements {
		if r.movements[i].OrganizationID == orgID && r.movements[i].ConsumableID == id {
			r.mu.RUnlock()
			return ErrHasMovements
		}
	}
	r.mu.RUnlock()
	return asItemError(r.consumables.Delete(ctx, orgID, id))
}

// ConvertItem checks the available stock of the bin, creates the asset and
// books the retrieval (no transaction in the No-DB profile).
func (r *MemoryRepository) ConvertItem(ctx context.Context, conv *Conversion, assets AssetCreator) error {
	item, err := r.GetItem(ctx, conv.OrganizationID, conv.ItemID)
	if err != nil {
		return err
	}
	bin := conv.LocationID
	if bin == "" {
		if bin, err = r.consumables.OnlyBin(ctx, conv.OrganizationID, conv.ItemID); err != nil {
			return asItemError(err)
		}
	}
	stock, _, err := r.consumables.ListStock(ctx, conv.OrganizationID, conv.ItemID, api.PaginationParams{Limit: math.MaxInt32 - 1})
	if err != nil {
		return asItemError(err)
	}
	available := false
	for i := range stock {
		available = available || (stock[i].LocationID == bin && stock[i].OnHand-stock[i].Reserved >= 1)
	}
	if !available {
		return ErrInsufficientStock
	}
	assetID, err := assets.Create(ctx, &AssetRef{OrganizationID: conv.OrganizationID, ClientID: item.ClientID, AssetTag: conv.AssetTag,
		Name: item.Name, Category: "hardware", LocationID: bin, SerialNumber: conv.SerialNumber})
	if err != nil {
		return err
	}
	m := retrievalOf(conv, bin, assetID)
	if err = r.Record(ctx, m); err != nil {
		return asLocationField(err)
	}
	conv.AssetID, conv.Movement = assetID, m
	return nil
}
