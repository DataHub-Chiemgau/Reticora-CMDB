package consumable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The package consumable does not import the movement service: the
// dependency runs from movement to consumable (WP-756). movement.Service
// books a consumable movement through ApplyBooking in its transaction and
// takes over BookingTypes, ProblemInsufficientStock and ErrInsufficientStock
// as its own.

// BookingTypes are the quantity bookings on the stock per bin, the types
// POST /api/v1/stock-movements takes for a consumable (E-38, STK-03).
var BookingTypes = map[string]bool{
	"receipt": true, "retrieval": true, "correction": true, "transfer": true,
}

// ProblemInsufficientStock: the booking would drive the stock below zero
// (409, RFC 7807 type of the movement service).
const ProblemInsufficientStock = "https://reticora.io/problems/insufficient-stock"

// insufficientStockError is the shortfall of a booking: the available stock
// (on_hand − reserved) of the bin does not cover the quantity. It is the
// problem insufficient-stock of the movement service (409, WP-274); the
// service uses ErrInsufficientStock as movement.ErrInsufficientStock, so a
// shortfall per bin and one of a quantity item are the same error.
type insufficientStockError struct{}

func (insufficientStockError) Error() string {
	return "the available stock does not cover the booking"
}

// ProblemType names the RFC 7807 type (api.TypedProblem).
func (insufficientStockError) ProblemType() string { return ProblemInsufficientStock }

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
// ApplyBooking. The types are BookingTypes.
func bookingDeltas(b *Booking) ([]binDelta, error) {
	if !BookingTypes[b.MovementType] {
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

// lockedConsumable is the consumable as lockConsumable read it under its
// row lock: the fields of the event stock.below_minimum and whether it is
// below its minimum level (on_hand_total < min_level, compared in numeric).
type lockedConsumable struct {
	id, client, sku, name, unit string
	total, minLevel             float64
	below                       bool
}

// lockConsumable locks the consumable (SELECT … FOR UPDATE, which applies
// its select and update policies, like lockObject of the movement service)
// and returns it. A consumable that is unknown, of another organization or
// outside the caller's scope is ErrNotFound; so is an article without
// client for a client-scoped principal (E-128 (b)). on_hand_total and the
// stock per bin change only under this lock, so the edge of the minimum
// level is seen exactly once, also by parallel bookings on different bins
// (WP-285).
func lockConsumable(ctx context.Context, tx pgx.Tx, orgID, id string) (*lockedConsumable, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	c := &lockedConsumable{id: id}
	err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(sku, ''), name, unit,
		on_hand_total::float8, min_level::float8, on_hand_total < min_level
		FROM consumable WHERE id = $1 AND organization_id = $2 FOR UPDATE`, id, orgID).
		Scan(&c.client, &c.sku, &c.name, &c.unit, &c.total, &c.minLevel, &c.below)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock consumable: %w", err)
	}
	return c, nil
}

// appendBelowMinimum writes stock.below_minimum about c in tx, as the last
// write of the booking or the change of min_level that took c from "not
// below" to "below" its minimum level (K20). A refused event fails the
// operation, which then rolls back with it.
func appendBelowMinimum(ctx context.Context, tx pgx.Tx, orgID string, c *lockedConsumable) error {
	if _, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.StockBelowMinimum, EntityID: c.id,
		Data: events.StockBelowMinimumData{ID: c.id, ClientID: c.client, SKU: c.sku, Name: c.name, Unit: c.unit,
			OnHandTotal: c.total, MinLevel: c.minLevel}}); err != nil {
		return fmt.Errorf("stock below minimum event: %w", err)
	}
	return nil
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
// (409); the CHECK of stock_level stays the last guard. Last, the balance
// goes into consumable.on_hand_total and the edge of the minimum level is
// checked (bookTotal, WP-285). ApplyBooking is the only writer of on_hand
// and on_hand_total; a caller that locks stock_level or reservation rows
// before the booking locks the consumable row first, one booking several
// consumables in ascending id.
func ApplyBooking(ctx context.Context, tx pgx.Tx, b *Booking) error {
	deltas, err := bookingDeltas(b)
	if err != nil {
		return err
	}
	item, err := lockConsumable(ctx, tx, b.OrganizationID, b.ConsumableID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(deltas))
	for _, d := range deltas {
		if binErr := checkBin(ctx, tx, b.OrganizationID, d.field, d.locationID, item.client); binErr != nil {
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
	return bookTotal(ctx, tx, b, deltas, item)
}

// bookTotal carries the balance of the booking (receipt +, retrieval −,
// correction ±, transfer 0) into on_hand_total of the locked consumable,
// so on_hand_total stays the sum of on_hand over all bins whatever the
// caller's scope (E-128 (a)). When the total thereby falls from "not
// below" to "below" the minimum level (on_hand_total < min_level),
// stock.below_minimum is the last write (WP-285); further takes below the
// threshold and transfers write none.
func bookTotal(ctx context.Context, tx pgx.Tx, b *Booking, deltas []binDelta, locked *lockedConsumable) error {
	var balance float64
	for _, d := range deltas {
		balance += d.delta
	}
	if balance == 0 {
		return nil
	}
	var below bool
	if err := tx.QueryRow(ctx, `UPDATE consumable SET on_hand_total = on_hand_total + $3
		WHERE id = $1 AND organization_id = $2
		RETURNING on_hand_total::float8, on_hand_total < min_level`,
		b.ConsumableID, b.OrganizationID, balance).Scan(&locked.total, &below); err != nil {
		return fmt.Errorf("book total stock: %w", err)
	}
	if locked.below || !below {
		return nil
	}
	return appendBelowMinimum(ctx, tx, b.OrganizationID, locked)
}

// ReservationTypes are the movement types of a reservation on the stock
// of a bin (WP-284): reservation raises reserved, release lowers it;
// on_hand stays. Only the reservation path books them (ApplyReservation).
var ReservationTypes = map[string]bool{"reservation": true, "release": true}

// StockKey names the stock row of a consumable in a bin.
type StockKey struct {
	ConsumableID, LocationID string
}

// stockReservedCheck is the check reserved <= on_hand of stock_level.
const stockReservedCheck = "stock_level_reserved_check"

// LockStock locks the stock rows keys in tx, ordered by (consumable_id,
// location_id), the lock order of the reservation paths (WP-284: the stock
// rows first, then the reservations). Rows outside the caller's scope are
// not locked; the number of locked rows is returned.
func LockStock(ctx context.Context, tx pgx.Tx, orgID string, keys []StockKey) (int, error) {
	sorted := slices.Clone(keys)
	slices.SortFunc(sorted, func(x, y StockKey) int {
		if c := strings.Compare(x.ConsumableID, y.ConsumableID); c != 0 {
			return c
		}
		return strings.Compare(x.LocationID, y.LocationID)
	})
	sorted = slices.Compact(sorted)
	locked := 0
	for _, k := range sorted {
		if !api.IsUUID(k.ConsumableID) || !api.IsUUID(k.LocationID) {
			continue
		}
		tag, err := tx.Exec(ctx, `SELECT 1 FROM stock_level WHERE consumable_id = $1 AND location_id = $2
			AND organization_id = $3 FOR UPDATE`, k.ConsumableID, k.LocationID, orgID)
		if err != nil {
			return locked, fmt.Errorf("lock stock: %w", err)
		}
		locked += int(tag.RowsAffected())
	}
	return locked, nil
}

// ReservationBin returns, in tx, the bin of the only stock_level row of the
// consumable the caller sees: the bin of a reservation without location_id
// (WP-284). Unlike OnlyBin it does not lock the consumable, so a
// client-scoped principal reserves an org-wide article in its own bin. No
// row of an invisible consumable is ErrNotFound, none or several rows of a
// visible one ErrBinRequired (422 on location_id).
func ReservationBin(ctx context.Context, tx pgx.Tx, orgID, consumableID string) (string, error) {
	if !api.IsUUID(consumableID) {
		return "", ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT location_id::text FROM stock_level WHERE consumable_id = $1 AND organization_id = $2
		ORDER BY location_id LIMIT 2`, consumableID, orgID)
	if err != nil {
		return "", fmt.Errorf("find bin: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("find bin: %w", err)
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) == 0 {
		visible, visErr := consumableVisible(ctx, tx, orgID, consumableID)
		if visErr != nil {
			return "", visErr
		}
		if !visible {
			return "", ErrNotFound
		}
	}
	return "", ErrBinRequired
}

// consumableVisible reports whether the consumable is visible under the
// caller's scope.
func consumableVisible(ctx context.Context, tx pgx.Tx, orgID, id string) (bool, error) {
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM consumable WHERE id = $1 AND organization_id = $2)`,
		id, orgID).Scan(&visible); err != nil {
		return false, fmt.Errorf("check consumable: %w", err)
	}
	return visible, nil
}

// reservationBin checks b against the rules of the reservation types and
// returns its bin and the change of reserved: a reservation names its bin
// in FromLocationID and raises reserved, a release names it in
// ToLocationID and lowers it.
func reservationBin(b *Booking) (bin string, delta float64, err error) {
	switch {
	case !ReservationTypes[b.MovementType]:
		return "", 0, fieldError("movement_type", "movement_type must be reservation or release")
	case b.Quantity <= 0:
		return "", 0, fieldError("quantity", "quantity must be greater than 0")
	case b.MovementType == "reservation" && (b.FromLocationID == "" || b.ToLocationID != ""):
		return "", 0, fieldError("from_location_id", "a reservation names its bin in from_location_id only")
	case b.MovementType == "release" && (b.ToLocationID == "" || b.FromLocationID != ""):
		return "", 0, fieldError("to_location_id", "a release names its bin in to_location_id only")
	case b.MovementType == "release":
		return b.ToLocationID, -b.Quantity, nil
	}
	return b.FromLocationID, b.Quantity, nil
}

// ApplyReservation books the reservation movement b on the stock row of
// its bin in tx, the transaction of the caller (WP-284, convention 14):
// reservation raises reserved, release lowers it (reservationBin); on_hand
// and on_hand_total stay. The row is locked (SELECT … FOR UPDATE under the
// caller's scope) before it changes; the consumable row is not locked, so
// ApplyBooking (consumable, then stock rows) and the reservation paths
// (stock rows, then reservations) never wait on each other in a cycle, and
// a client-scoped principal reserves an org-wide article in its own bin. A
// reservation beyond on_hand − reserved is ErrInsufficientStock (409), so
// is a violation of the check reserved <= on_hand and a bin without stock
// row of a visible consumable; a bin without stock row of an invisible
// consumable is ErrNotFound.
func ApplyReservation(ctx context.Context, tx pgx.Tx, b *Booking) error {
	bin, delta, err := reservationBin(b)
	if err != nil {
		return err
	}
	if !api.IsUUID(b.ConsumableID) {
		return ErrNotFound
	}
	if !api.IsUUID(bin) {
		return ErrInsufficientStock
	}
	var onHand, reserved float64
	err = tx.QueryRow(ctx, `SELECT on_hand::float8, reserved::float8 FROM stock_level
		WHERE consumable_id = $1 AND location_id = $2 AND organization_id = $3 FOR UPDATE`,
		b.ConsumableID, bin, b.OrganizationID).Scan(&onHand, &reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		if delta < 0 {
			return fmt.Errorf("release stock: no stock row of consumable %s in bin %s", b.ConsumableID, bin)
		}
		visible, visErr := consumableVisible(ctx, tx, b.OrganizationID, b.ConsumableID)
		if visErr != nil {
			return visErr
		}
		if !visible {
			return ErrNotFound
		}
		return ErrInsufficientStock
	}
	if err != nil {
		return fmt.Errorf("lock stock: %w", err)
	}
	if delta > 0 && onHand-reserved < delta {
		return ErrInsufficientStock
	}
	if _, err = tx.Exec(ctx, `UPDATE stock_level SET reserved = reserved + $4, updated_at = now()
		WHERE consumable_id = $1 AND location_id = $2 AND organization_id = $3`,
		b.ConsumableID, bin, b.OrganizationID, delta); err != nil {
		var pgErr *pgconn.PgError
		if delta > 0 && errors.As(err, &pgErr) && pgErr.ConstraintName == stockReservedCheck {
			return ErrInsufficientStock
		}
		return fmt.Errorf("book reservation: %w", err)
	}
	return nil
}

// OnlyBin returns, in tx, the bin of the only stock_level row of the
// consumable the caller sees, after locking the consumable: the bin of
// POST /api/v1/consumables/{id}/movements without location_id (WP-756).
// None or several rows are ErrBinRequired (422 on location_id), an
// invisible consumable ErrNotFound.
func OnlyBin(ctx context.Context, tx pgx.Tx, orgID, consumableID string) (string, error) {
	return onlyBin(ctx, tx, orgID, consumableID)
}

// OnlyBin is OnlyBin of the No-DB profile.
func (r *MemoryRepository) OnlyBin(_ context.Context, orgID, consumableID string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.consumables[consumableID]
	if !ok || c.OrganizationID != orgID {
		return "", ErrNotFound
	}
	if len(r.stock[consumableID]) != 1 {
		return "", ErrBinRequired
	}
	var only string
	for bin := range r.stock[consumableID] {
		only = bin
	}
	return only, nil
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
