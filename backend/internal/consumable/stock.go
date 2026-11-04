package consumable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/jackc/pgx/v5"
)

// insufficientStockError is the shortfall of a booking: the available stock
// (on_hand − reserved) of the bin does not cover the quantity. It is the
// problem insufficient-stock of the movement service (409, WP-274) and
// matches movement.ErrInsufficientStock, so the service and its handler
// treat a shortfall per bin like one of a quantity item.
type insufficientStockError struct{}

func (insufficientStockError) Error() string {
	return "the stock of the bin does not cover the booking"
}

// ProblemType names the RFC 7807 type (api.TypedProblem).
func (insufficientStockError) ProblemType() string { return movement.ProblemInsufficientStock }

// Is matches movement.ErrInsufficientStock.
func (insufficientStockError) Is(target error) bool { return target == movement.ErrInsufficientStock }

// ErrInsufficientStock: the booking would take more than the available
// stock of a bin (STK-03); 409 with the type insufficient-stock.
var ErrInsufficientStock error = insufficientStockError{}

// ErrNotFound: the consumable is unknown, of another organization or not
// visible under the caller's scope.
var ErrNotFound = errors.New("consumable not found")

// IsInsufficientStock reports whether err is a shortfall of a bin.
func IsInsufficientStock(err error) bool {
	var typed insufficientStockError
	return errors.As(err, &typed)
}

// binDelta is the change of on_hand of one bin by a booking.
type binDelta struct {
	field      string // from_location_id or to_location_id
	locationID string
	delta      float64
}

// bookingDeltas checks b against the rules per type (STK-02) and returns
// the changes per bin, ordered by location id, the lock order of
// ApplyBooking. The types are the quantity bookings of the movement service
// (movement.QuantityMovementTypes).
func bookingDeltas(b *Booking) ([]binDelta, error) {
	if !movement.QuantityMovementTypes[b.MovementType] {
		return nil, fieldError("movement_type", "movement_type must be receipt, retrieval, correction or transfer")
	}
	if b.Quantity <= 0 {
		return nil, fieldError("quantity", "quantity must be greater than 0")
	}
	from, to := b.FromLocationID, b.ToLocationID
	var deltas []binDelta
	switch b.MovementType {
	case "receipt":
		if to == "" || from != "" {
			return nil, fieldError("to_location_id", "a receipt books into to_location_id only")
		}
	case "retrieval":
		if from == "" || to != "" {
			return nil, fieldError("from_location_id", "a retrieval books from from_location_id only")
		}
	case "correction":
		if (from == "") == (to == "") {
			return nil, fieldError("to_location_id", "a correction names exactly one of to_location_id and from_location_id")
		}
	case "transfer":
		if from == "" || to == "" {
			return nil, fieldError("to_location_id", "a transfer names from_location_id and to_location_id")
		}
		if from == to {
			return nil, fieldError("to_location_id", "a transfer needs two different bins")
		}
	}
	if from != "" {
		deltas = append(deltas, binDelta{field: "from_location_id", locationID: from, delta: -b.Quantity})
	}
	if to != "" {
		deltas = append(deltas, binDelta{field: "to_location_id", locationID: to, delta: b.Quantity})
	}
	slices.SortFunc(deltas, func(x, y binDelta) int {
		switch {
		case x.locationID < y.locationID:
			return -1
		case x.locationID > y.locationID:
			return 1
		}
		return 0
	})
	return deltas, nil
}

// fieldError is the 422 field error of a booking.
func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// lockConsumable locks the consumable (SELECT … FOR UPDATE, which applies
// its select and update policies, like lockObject of the movement service)
// and returns its client. A consumable that is unknown, of another
// organization or outside the caller's scope is ErrNotFound; so is an
// article without client for a client-scoped principal (E-128 (b)).
func lockConsumable(ctx context.Context, tx pgx.Tx, orgID, id string) (string, error) {
	if !api.IsUUID(id) {
		return "", ErrNotFound
	}
	var client string
	err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM consumable
		WHERE id = $1 AND organization_id = $2 FOR UPDATE`, id, orgID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock consumable: %w", err)
	}
	return client, nil
}

// checkBin fails with a 422 field error when id names no location visible
// under the caller's scope, a location that is no bin or a bin of another
// client than the consumable. The derivation trigger of stock_level checks
// kind and client again (23514).
func checkBin(ctx context.Context, tx pgx.Tx, orgID, field, id, client string) error {
	if !api.IsUUID(id) {
		return fieldError(field, "location not found")
	}
	var kind, binClient string
	err := tx.QueryRow(ctx, `SELECT kind, client_id::text FROM location WHERE id = $1 AND organization_id = $2`,
		id, orgID).Scan(&kind, &binClient)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError(field, "location not found")
	}
	if err != nil {
		return fmt.Errorf("check bin: %w", err)
	}
	if kind != "bin" {
		return fieldError(field, "location is not a bin")
	}
	if client != "" && client != binClient {
		return fieldError(field, "location belongs to another client")
	}
	return nil
}

// ApplyBooking books b on the stock per bin in tx, the transaction of the
// caller (database.WithRequestTenant); it opens none of its own and writes
// no ledger row (the caller does, in the same transaction). The rules per
// type are those of bookingDeltas. The consumable is locked, the bins are
// checked (visible, kind bin, client of the consumable, else 422), a
// receipt target gets its row by upsert, and the affected rows are locked
// in the order of their location id (FOR UPDATE), so opposite transfers do
// not deadlock. A take beyond on_hand − reserved is ErrInsufficientStock
// (409); the CHECK of stock_level stays the last guard.
func ApplyBooking(ctx context.Context, tx pgx.Tx, b *Booking) error {
	deltas, err := bookingDeltas(b)
	if err != nil {
		return err
	}
	client, err := lockConsumable(ctx, tx, b.OrganizationID, b.ConsumableID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(deltas))
	for _, d := range deltas {
		if binErr := checkBin(ctx, tx, b.OrganizationID, d.field, d.locationID, client); binErr != nil {
			return binErr
		}
		ids = append(ids, d.locationID)
	}
	rows, err := tx.Query(ctx, `SELECT location_id::text, on_hand::float8, reserved::float8 FROM stock_level
		WHERE consumable_id = $1 AND organization_id = $2 AND location_id = ANY ($3::uuid[])
		ORDER BY location_id FOR UPDATE`, b.ConsumableID, b.OrganizationID, ids)
	if err != nil {
		return fmt.Errorf("lock stock: %w", err)
	}
	type level struct{ onHand, reserved float64 }
	locked := map[string]level{}
	for rows.Next() {
		var id string
		var l level
		if scanErr := rows.Scan(&id, &l.onHand, &l.reserved); scanErr != nil {
			rows.Close()
			return fmt.Errorf("lock stock: %w", scanErr)
		}
		locked[id] = l
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("lock stock: %w", err)
	}
	for _, d := range deltas {
		if d.delta >= 0 {
			continue
		}
		l, ok := locked[d.locationID]
		if !ok || l.onHand-l.reserved < -d.delta {
			return ErrInsufficientStock
		}
	}
	for _, d := range deltas {
		if d.delta < 0 {
			if _, err = tx.Exec(ctx, `UPDATE stock_level SET on_hand = on_hand + $4, updated_at = now()
				WHERE consumable_id = $1 AND location_id = $2 AND organization_id = $3`,
				b.ConsumableID, d.locationID, b.OrganizationID, d.delta); err != nil {
				return fmt.Errorf("book stock: %w", err)
			}
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (consumable_id, location_id) DO UPDATE
			SET on_hand = stock_level.on_hand + EXCLUDED.on_hand, updated_at = now()`,
			b.OrganizationID, b.ConsumableID, d.locationID, d.delta); err != nil {
			return fmt.Errorf("book stock: %w", err)
		}
	}
	return nil
}

// stockSort pins the order of the stock list for the keyset cursor.
const (
	stockSort = "location_id"
	stockDir  = "asc"
)

// ListStock returns the stock rows of the consumable visible under the
// caller's scope in tx, ordered by bin, at most page.Limit+1 of them
// (StockPage), and their number. The consumable itself must be visible,
// else ErrNotFound.
func ListStock(ctx context.Context, tx pgx.Tx, orgID, consumableID string, page api.PaginationParams) ([]Stock, int, error) {
	if !api.IsUUID(consumableID) {
		return nil, 0, ErrNotFound
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM consumable WHERE id = $1 AND organization_id = $2)`,
		consumableID, orgID).Scan(&visible); err != nil {
		return nil, 0, fmt.Errorf("check consumable: %w", err)
	}
	if !visible {
		return nil, 0, ErrNotFound
	}
	where := "organization_id = $1 AND consumable_id = $2"
	args := []any{orgID, consumableID}
	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_level WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count stock: %w", err)
	}
	if page.Cursor != nil {
		if !page.Cursor.Matches(stockSort, stockDir) || !api.IsUUID(page.Cursor.Value) || !api.IsUUID(page.Cursor.ID) {
			return nil, 0, fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
		}
		where += " AND " + api.KeysetClause(stockSort, "uuid", stockDir, len(args)+1)
		args = append(args, page.Cursor.Value, page.Cursor.ID)
	}
	tail := fmt.Sprintf(" ORDER BY location_id, id LIMIT $%d", len(args)+1)
	args = append(args, page.Limit+1)
	rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, consumable_id::text, location_id::text,
		client_id::text, site_id::text, on_hand::float8, reserved::float8, updated_at
		FROM stock_level WHERE `+where+tail, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list stock: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stock, error) {
		var s Stock
		scanErr := row.Scan(&s.ID, &s.OrganizationID, &s.ConsumableID, &s.LocationID, &s.ClientID, &s.SiteID,
			&s.OnHand, &s.Reserved, &s.UpdatedAt)
		s.UpdatedAt = s.UpdatedAt.UTC()
		return s, scanErr
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list stock: %w", err)
	}
	return out, total, nil
}

// StockPage is the list response of the rows ListStock read (at most one
// more than the limit): when more follow, the page ends at the limit,
// has_more is set and next_cursor is the keyset cursor after its last row.
func StockPage(rows []Stock, total int, page api.PaginationParams) api.ListResponse[Stock] {
	list := api.ListResponse[Stock]{Data: api.NonNilSlice(rows), Total: total, Limit: page.Limit}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := &list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: stockSort, Dir: stockDir, Value: last.LocationID, ID: last.ID})
	}
	return list
}
