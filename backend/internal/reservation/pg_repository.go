package reservation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// referenceFields are the request fields of the composite foreign keys of
// reservation on app_user, ticket and internal_order (000275, WP-827) by
// constraint.
var referenceFields = map[string]string{
	"reservation_assignee_id_fkey": "assignee_id",
	"reservation_ticket_id_fkey":   "ticket_id",
	"reservation_order_id_fkey":    "order_id",
}

const selectColumns = `
	id::text, organization_id::text, item_kind, COALESCE(asset_id::text, ''),
	COALESCE(consumable_id::text, ''), COALESCE(location_id::text, ''), COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''), quantity, state, reserved_from,
	reserved_until, COALESCE(assignee_id::text, ''), COALESCE(project_ref, ''),
	COALESCE(order_id::text, ''), COALESCE(ticket_id::text, ''), expires_at,
	COALESCE(reason, ''), COALESCE(created_by::text, ''), created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS. Every
// writing path locks in a fixed order (WP-277): first the asset (SELECT …
// FOR UPDATE in the caller's scope, under the update policy
// asset_scoped_update; several assets ordered by id) or the stock row of
// the bin of a quantity reservation (several ordered by consumable_id and
// location_id, WP-284), then the reservation (FOR UPDATE, state active).
// Quantity reservations always book their movements reservation and
// release, which change stock_level.reserved, so reserved stays the sum of
// the active quantities of each stock row. With the wiring of the With methods it books
// the movements reservation and release through the movement service,
// converts through the assignment service, writes the events
// reservation.* to the outbox and audits the expiry, all in the
// transaction of the change; without it, as in tests, it writes the state
// only.
type PGRepository struct {
	pool      *pgxpool.Pool
	recorder  audit.TxRecorder
	outbox    *events.Outbox
	movements assignment.MovementBooker
	assigner  Assigner
}

// Assigner assigns an asset in the transaction the context carries: the
// assignment service of WP-275 (assignment.Service), which joins it.
type Assigner interface {
	Assign(ctx context.Context, orgID string, in *assignment.AssignInput) (*assignment.Assignment, error)
}

// NewPGRepository creates a PostgreSQL-backed reservation repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// WithAudit sets the recorder of the expiry's audit entries
// (reservations.expire, source system).
func (r *PGRepository) WithAudit(recorder audit.TxRecorder) *PGRepository {
	r.recorder = recorder
	return r
}

// WithEvents writes the events reservation.* to outbox in the transaction
// of the change (EVT-03, convention 13).
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// WithMovements books the movements reservation and release of an asset
// through the movement service (WP-274).
func (r *PGRepository) WithMovements(movements assignment.MovementBooker) *PGRepository {
	r.movements = movements
	return r
}

// WithAssigner sets the assignment service of the conversion (WP-275).
func (r *PGRepository) WithAssigner(a Assigner) *PGRepository {
	r.assigner = a
	return r
}

// inTx runs fn in the transaction of the request (WithRequestTenantTx),
// joining one the context carries, with ctx carrying it: the assignment
// service joins it and BeforeCommit applies.
func (r *PGRepository) inTx(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, func(txCtx context.Context) error {
		return database.WithRequestTenant(txCtx, r.pool, orgID, func(_ context.Context, tx pgx.Tx) error {
			return fn(txCtx, tx)
		})
	})
}

// requireBin resolves and checks the bin of the quantity reservation res
// in tx (WP-284): without location_id the only bin with a stock row of the
// consumable the caller sees (consumable.ReservationBin; none or several
// are 422 on location_id, none of an invisible consumable
// ErrItemNotFound); a location that is not visible under the caller's
// scope is ErrBinNotFound (404), one of another kind errNotABin (422). The
// foreign key on stock_level is checked without row level security, so
// the check runs before the write, in its transaction (TEN-04, API-03,
// F-047).
func requireBin(ctx context.Context, tx pgx.Tx, res *Reservation) error {
	if !api.IsUUID(res.ConsumableID) {
		return ErrItemNotFound
	}
	if res.LocationID == "" {
		bin, err := consumable.ReservationBin(ctx, tx, res.OrganizationID, res.ConsumableID)
		if errors.Is(err, consumable.ErrNotFound) {
			return ErrItemNotFound
		}
		if err != nil {
			return err
		}
		res.LocationID = bin
		return nil
	}
	if !api.IsUUID(res.LocationID) {
		return ErrBinNotFound
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM location WHERE id = $1 AND organization_id = $2`,
		res.LocationID, res.OrganizationID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBinNotFound
	}
	if err != nil {
		return fmt.Errorf("check bin of reservation: %w", err)
	}
	if kind != "bin" {
		return errNotABin
	}
	return nil
}

// lockedAsset is a locked asset: whether it has a holder
// (assigned_user_id, assigned_team_id; WP-745).
type lockedAsset struct {
	assigned bool
}

// lockAsset locks the asset in the transaction's scope; the update
// policies apply (asset_scoped_update, 000079), so an asset a scoped
// principal may only read is not lockable: ErrAssetNotFound.
func lockAsset(ctx context.Context, tx pgx.Tx, orgID, assetID string) (*lockedAsset, error) {
	if !api.IsUUID(assetID) {
		return nil, ErrAssetNotFound
	}
	a := &lockedAsset{}
	err := tx.QueryRow(ctx, `SELECT assigned_user_id IS NOT NULL OR assigned_team_id IS NOT NULL
		FROM asset WHERE id = $1 AND organization_id = $2 FOR UPDATE`, assetID, orgID).Scan(&a.assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock asset: %w", err)
	}
	return a, nil
}

// lockAssets locks the assets ids ordered by id (the order of WP-277).
func lockAssets(ctx context.Context, tx pgx.Tx, orgID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM asset WHERE id = ANY($1::uuid[]) AND organization_id = $2 ORDER BY id FOR UPDATE`, ids, orgID)
	if err != nil {
		return fmt.Errorf("lock assets: %w", err)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("lock assets: %w", err)
	}
	return nil
}

// actor is the acting user of the request: the tenant user, else the
// principal's subject; the expiry has none.
func actor(ctx context.Context) string {
	if id := tenant.FromContext(ctx).UserID; id != "" {
		return id
	}
	if p, ok := identity.PrincipalFromContext(ctx); ok && api.IsUUID(p.Subject) {
		return p.Subject
	}
	return ""
}

// book books the movement typ (reservation, release) of res in tx. A
// quantity reservation always books, through the movement service or,
// without its wiring, through movement.RecordTx (convention 14): the
// reservation names its bin as from_location_id, the release as
// to_location_id, and consumable.ApplyReservation changes reserved of the
// stock row (WP-284); an invisible consumable is ErrItemNotFound. An asset
// reservation books only with the wiring of WithMovements.
func (r *PGRepository) book(ctx context.Context, tx pgx.Tx, res *Reservation, typ string) error {
	if res.ItemKind == "consumable" {
		q := res.Quantity
		m := &movement.Movement{OrganizationID: res.OrganizationID, ItemKind: "consumable", ConsumableID: res.ConsumableID,
			MovementType: typ, Quantity: &q, ActorID: actor(ctx), Reason: res.Reason}
		if typ == "reservation" {
			m.FromLocationID = res.LocationID
		} else {
			m.ToLocationID = res.LocationID
		}
		var err error
		if r.movements != nil {
			err = r.movements.RecordTx(ctx, tx, m)
		} else {
			err = movement.RecordTx(ctx, tx, m)
		}
		if errors.Is(err, consumable.ErrNotFound) {
			return ErrItemNotFound
		}
		return err
	}
	if r.movements == nil || res.ItemKind != "asset" || res.AssetID == "" {
		return nil
	}
	m := &movement.Movement{OrganizationID: res.OrganizationID, ItemKind: "asset", AssetID: res.AssetID,
		MovementType: typ, ActorID: actor(ctx), Reason: res.Reason}
	if typ == "reservation" {
		// The user the asset is reserved for; a user of another
		// organization fails the foreign key (422).
		m.AssigneeUserID = res.AssigneeID
	}
	return r.movements.RecordTx(ctx, tx, m)
}

// emit registers the event typ of res last before the commit (K20); the
// payload carries the client and site of the reservation's columns
// (000258, WP-750), for a quantity reservation those of the bin, with
// consumable_id, location_id and quantity (WP-284).
func (r *PGRepository) emit(ctx context.Context, tx pgx.Tx, res *Reservation, typ string) error {
	if r.outbox == nil {
		return nil
	}
	data := assignment.ReservationEventData{ID: res.ID, ItemKind: res.ItemKind, AssetID: res.AssetID,
		ConsumableID: res.ConsumableID, LocationID: res.LocationID, State: res.State,
		ReservedUntil: res.ReservedUntil, ExpiresAt: res.ExpiresAt, ClientID: res.ClientID, SiteID: res.SiteID}
	if res.ItemKind == "consumable" {
		q := res.Quantity
		data.Quantity = &q
	}
	e := &events.Event{OrgID: res.OrganizationID, Type: typ, EntityID: res.ID, Data: data}
	return database.BeforeCommit(ctx, func() error {
		if _, err := r.outbox.Append(ctx, tx, e); err != nil {
			return fmt.Errorf("append %s: %w", typ, err)
		}
		return nil
	})
}

// List returns reservations filtered by state.
func (r *PGRepository) List(ctx context.Context, orgID, state string, page api.PaginationParams) ([]Reservation, int, error) {
	var out []Reservation
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		if state != "" {
			where = append(where, "state = $2")
			args = append(args, state)
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM reservation WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count reservations: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d",
			selectColumns, clause, len(args)-1, len(args)), args...)
		if err != nil {
			return fmt.Errorf("list reservations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *res)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one reservation.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Reservation, error) {
	var out *Reservation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE id = $1 AND organization_id = $2",
			selectColumns), id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get reservation: %w", err)
		}
		out = res
		return nil
	})
	return out, err
}

// Create inserts a reservation. An asset is locked first; an active
// reservation of it is ErrConflict, which the partial unique index
// idx_reservation_one_active_per_asset also guarantees, an asset with a
// holder assignment.ErrAlreadyAssigned (409 asset-assigned). Without expires_at
// the reservation expires at reserved_until. A consumable is reserved in
// its bin (requireBin): the stock row is locked and reserved raised by the
// movement reservation before the insert, never beyond on_hand − reserved
// (consumable.ErrInsufficientStock, WP-284). The movement reservation and
// reservation.created are written in the same transaction.
func (r *PGRepository) Create(ctx context.Context, res *Reservation) error {
	if res.ExpiresAt == nil && res.ReservedUntil != nil {
		until := *res.ReservedUntil
		res.ExpiresAt = &until
	}
	return r.inTx(ctx, res.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if nilIfEmpty(res.AssetID) != nil {
			locked, err := lockAsset(ctx, tx, res.OrganizationID, res.AssetID)
			if err != nil {
				return err
			}
			var reserved bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reservation
				WHERE asset_id = $1 AND organization_id = $2 AND state = 'active' FOR UPDATE)`,
				res.AssetID, res.OrganizationID).Scan(&reserved); err != nil {
				return fmt.Errorf("check reservation of asset: %w", err)
			}
			if reserved {
				return ErrConflict
			}
			// An assigned asset is not available: of a reservation and a
			// concurrent assignment exactly one wins.
			if locked.assigned {
				return assignment.ErrAlreadyAssigned
			}
		}
		if res.ItemKind == "consumable" {
			// The stock row of the bin is locked and its reserved raised
			// first (the order of WP-277: object, then reservation); a
			// shortfall is consumable.ErrInsufficientStock (WP-284).
			if err := requireBin(ctx, tx, res); err != nil {
				return err
			}
			if err := r.book(ctx, tx, res, "reservation"); err != nil {
				return err
			}
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO reservation (
				organization_id, item_kind, asset_id, consumable_id, location_id, quantity,
				state, reserved_from, reserved_until, assignee_id, project_ref,
				order_id, ticket_id, expires_at, reason, created_by
			) VALUES ($1,$2,$3,$4,$5,$6,'active',COALESCE($7, now()),$8,$9,$10,$11,$12,$13,$14,$15)
			RETURNING %s`, selectColumns),
			res.OrganizationID, res.ItemKind, nilIfEmpty(res.AssetID),
			nilIfEmpty(res.ConsumableID), nilIfEmpty(res.LocationID), res.Quantity, nilIfZero(res.ReservedFrom),
			res.ReservedUntil, nilIfEmpty(res.AssigneeID), nilIfEmpty(res.ProjectRef),
			nilIfEmpty(res.OrderID), nilIfEmpty(res.TicketID), res.ExpiresAt,
			nilIfEmpty(res.Reason), nilIfEmpty(res.CreatedBy))
		scanned, err := scan(row)
		if err != nil {
			var pgErr *pgconn.PgError
			switch {
			case errors.As(err, &pgErr) && pgErr.ConstraintName == "idx_reservation_one_active_per_asset":
				return ErrConflict
			case errors.As(err, &pgErr) && pgErr.Code == "23503" && referenceFields[pgErr.ConstraintName] != "":
				// A holder, ticket or order that is unknown or of another
				// organization (composite foreign keys of 000275, WP-827):
				// 422 on the field, the same for both (no existence oracle).
				field := referenceFields[pgErr.ConstraintName]
				return &api.FieldError{Field: field, Message: field + " not found", Status: http.StatusUnprocessableEntity}
			case errors.As(err, &pgErr) && pgErr.Code == "42501":
				// WITH CHECK of reservation_isolation: the derived client
				// or site lies outside the caller's write scope, e.g. an
				// org-wide item reserved by a scoped principal (E-09).
				if res.AssetID != "" {
					return ErrAssetNotFound
				}
				return ErrItemNotFound
			}
			return fmt.Errorf("create reservation: %w", err)
		}
		*res = *scanned
		if res.ItemKind != "consumable" {
			if bookErr := r.book(ctx, tx, res, "reservation"); bookErr != nil {
				return bookErr
			}
		}
		return r.emit(ctx, tx, res, events.ReservationCreated)
	})
}

// lockReservation locks the reservation id and, before it, its asset or
// the stock row of its bin (the fixed order of WP-277 and WP-284). A
// reservation that is unknown, of another organization, reserves an item
// outside the caller's scope or an asset the caller may not lock is
// ErrNotFound, a visible one that is no longer active ErrNotActive. The
// asset of a quantity reservation is nil.
func lockReservation(ctx context.Context, tx pgx.Tx, orgID, id string) (*Reservation, *lockedAsset, error) {
	if !api.IsUUID(id) {
		return nil, nil, ErrNotFound
	}
	var assetID string
	var stock consumable.StockKey
	err := tx.QueryRow(ctx, `SELECT COALESCE(asset_id::text, ''), COALESCE(consumable_id::text, ''), COALESCE(location_id::text, '')
		FROM reservation WHERE id = $1 AND organization_id = $2`,
		id, orgID).Scan(&assetID, &stock.ConsumableID, &stock.LocationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read reservation: %w", err)
	}
	if stock.ConsumableID != "" {
		// The stock row of the bin before the reservation (WP-284); one
		// outside the caller's scope is not lockable.
		n, lockErr := consumable.LockStock(ctx, tx, orgID, []consumable.StockKey{stock})
		if lockErr != nil {
			return nil, nil, lockErr
		}
		if n == 0 {
			return nil, nil, ErrNotFound
		}
	}
	var locked *lockedAsset
	if assetID != "" {
		if locked, err = lockAsset(ctx, tx, orgID, assetID); err != nil {
			if errors.Is(err, ErrAssetNotFound) {
				return nil, nil, ErrNotFound
			}
			return nil, nil, err
		}
	}
	res, err := scan(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM reservation WHERE id = $1 AND organization_id = $2 FOR UPDATE",
		selectColumns), id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock reservation: %w", err)
	}
	if res.State != "active" {
		return nil, nil, ErrNotActive
	}
	return res, locked, nil
}

// setState sets the state of the locked, active reservation res.
func setState(ctx context.Context, tx pgx.Tx, res *Reservation, state string) error {
	updated, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
		"UPDATE reservation SET state = $3, updated_at = now() WHERE id = $1 AND organization_id = $2 AND state = 'active' RETURNING %s",
		selectColumns), res.ID, res.OrganizationID, state))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotActive
	}
	if err != nil {
		return fmt.Errorf("transition reservation: %w", err)
	}
	*res = *updated
	return nil
}

// stateEffects are the event and the movement of the terminal states
// Transition sets.
var stateEffects = map[string]struct{ event, movement string }{
	"released": {events.ReservationReleased, "release"},
	"expired":  {events.ReservationExpired, "release"},
}

// Transition moves an active reservation to released or expired after
// locking its asset and itself (lockReservation), and writes the movement
// release and reservation.released or reservation.expired in the same
// transaction. Consuming goes through Convert.
func (r *PGRepository) Transition(ctx context.Context, orgID, id, state string) (*Reservation, error) {
	effects, ok := stateEffects[state]
	if !ok {
		return nil, fmt.Errorf("invalid target state %q", state)
	}
	var out *Reservation
	err := r.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, _, err := lockReservation(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if stateErr := setState(ctx, tx, res, state); stateErr != nil {
			return stateErr
		}
		if bookErr := r.book(ctx, tx, res, effects.movement); bookErr != nil {
			return bookErr
		}
		out = res
		return r.emit(ctx, tx, res, effects.event)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Convert consumes the active asset reservation id and assigns its asset
// in the same transaction through the assignment service (WP-275), which
// books the movement assignment, sets the holder cache and writes
// asset.assigned; reservation.converted follows. The holder is in.Holder,
// else the user of the reservation; a project reservation without holder
// is a 422. When the assignment fails, everything rolls back and the
// reservation stays active.
//
// A quantity reservation is taken out of its bin instead (convertQuantity,
// WP-759).
func (r *PGRepository) Convert(ctx context.Context, orgID, id string, in *ConvertInput) (*ConvertResult, error) {
	var out *ConvertResult
	err := r.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		consumableID, err := reservedConsumable(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if consumableID != "" {
			out, err = r.convertQuantity(ctx, tx, orgID, id, consumableID, in)
			return err
		}
		if r.assigner == nil {
			return ErrConvertUnavailable
		}
		res, locked, err := lockReservation(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if res.ItemKind != "asset" || locked == nil {
			return errNotAssetReservation
		}
		holder := in.Holder
		if holder.UserID == "" && holder.TeamID == "" {
			if res.AssigneeID == "" {
				return errProjectNeedsHolder
			}
			holder.UserID = res.AssigneeID
		}
		// Consumed first: the assignment service then finds no active
		// reservation of the asset.
		if stateErr := setState(ctx, tx, res, "consumed"); stateErr != nil {
			return stateErr
		}
		a, err := r.assigner.Assign(ctx, orgID, &assignment.AssignInput{AssetID: res.AssetID, Holder: holder, DueAt: in.DueAt, Notes: in.Notes})
		if err != nil {
			return err
		}
		out = &ConvertResult{Reservation: res, Assignment: a}
		return r.emit(ctx, tx, res, events.ReservationConverted)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reservedConsumable returns the consumable of the reservation id the
// caller sees, empty for an asset reservation; an unknown or invisible
// reservation is ErrNotFound.
func reservedConsumable(ctx context.Context, tx pgx.Tx, orgID, id string) (string, error) {
	if !api.IsUUID(id) {
		return "", ErrNotFound
	}
	var consumableID string
	err := tx.QueryRow(ctx, `SELECT COALESCE(consumable_id::text, '') FROM reservation WHERE id = $1 AND organization_id = $2`,
		id, orgID).Scan(&consumableID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read reservation: %w", err)
	}
	return consumableID, nil
}

// reservedTaker books the retrieval of a converted quantity reservation in
// the transaction of the caller: movement.Service (WP-759).
type reservedTaker interface {
	RecordReservedRetrievalTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error
}

// errHolderOnQuantity: a quantity reservation becomes a retrieval, not an
// assignment; it names no holder and no due date (422, WP-759).
var errHolderOnQuantity = &api.FieldError{Field: "assignee_id",
	Message: "a quantity reservation is converted into a retrieval; assignee_id, team_id and due_at are not allowed",
	Status:  http.StatusUnprocessableEntity}

// convertQuantity consumes the active quantity reservation id and takes
// its whole quantity out of its bin in the same transaction (WP-759): the
// consumable is locked first (consumable.LockConsumable, the first lock of
// every booking), then the stock row of the bin and the reservation
// (lockReservation, the order of WP-284). The reservation becomes
// consumed, the movement service books one retrieval from the bin
// (movement.RecordReservedRetrievalTx: on_hand and reserved fall together,
// without a check against on_hand − reserved), and reservation.converted
// follows in the outbox. Any error rolls everything back; the reservation
// stays active.
func (r *PGRepository) convertQuantity(ctx context.Context, tx pgx.Tx, orgID, id, consumableID string,
	in *ConvertInput) (*ConvertResult, error) {
	if in.Holder.UserID != "" || in.Holder.TeamID != "" || in.DueAt != nil {
		return nil, errHolderOnQuantity
	}
	if err := consumable.LockConsumable(ctx, tx, orgID, consumableID); err != nil {
		if errors.Is(err, consumable.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	res, _, err := lockReservation(ctx, tx, orgID, id)
	if err != nil {
		return nil, err
	}
	if stateErr := setState(ctx, tx, res, "consumed"); stateErr != nil {
		return nil, stateErr
	}
	q := res.Quantity
	m := &movement.Movement{OrganizationID: orgID, ItemKind: "consumable", ConsumableID: res.ConsumableID,
		MovementType: "retrieval", FromLocationID: res.LocationID, Quantity: &q, ActorID: actor(ctx),
		Reason: res.Reason, Notes: in.Notes}
	if taker, ok := r.movements.(reservedTaker); ok {
		err = taker.RecordReservedRetrievalTx(ctx, tx, m)
	} else {
		err = movement.RecordReservedRetrievalTx(ctx, tx, m)
	}
	if errors.Is(err, consumable.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if emitErr := r.emit(ctx, tx, res, events.ReservationConverted); emitErr != nil {
		return nil, emitErr
	}
	return &ConvertResult{Reservation: res, Movement: m}, nil
}

// ActiveForItem returns the active reservations of one item.
func (r *PGRepository) ActiveForItem(ctx context.Context, orgID, itemKind, itemID string) ([]Reservation, error) {
	var out []Reservation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		column := "asset_id"
		if itemKind == "consumable" || itemKind == "quantity_item" {
			column = "consumable_id"
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE organization_id = $1 AND %s = $2 AND state = 'active'",
			selectColumns, column), orgID, itemID)
		if err != nil {
			return fmt.Errorf("list active reservations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *res)
		}
		return rows.Err()
	})
	return out, err
}

// expireBatchSize is the number of reservations one expiry transaction
// takes.
const expireBatchSize = 100

// ExpireDue expires the active reservations whose expires_at lies before
// now. Expiry is a cross-tenant batch job: per organization and batch it
// opens one transaction of the organization-wide scope (WithTenant,
// OrgWideScope, K20), reads the due reservations, locks their assets
// ordered by id and sets each reservation, if still active, to expired,
// with the movement release, the audit entry reservations.expire (source
// system) and reservation.expired in the same transaction. A failing
// organization does not stop the others; the errors are joined.
func (r *PGRepository) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	orgIDs, err := database.OrganizationIDs(ctx, r.pool)
	if err != nil {
		return 0, err
	}
	count := 0
	var errs []error
	for _, orgID := range orgIDs {
		for {
			n, more, batchErr := r.expireBatch(ctx, orgID, now)
			count += n
			if batchErr != nil {
				errs = append(errs, fmt.Errorf("expire reservations of organization %s: %w", orgID, batchErr))
				break
			}
			if !more {
				break
			}
		}
	}
	return count, errors.Join(errs...)
}

// dueReservation is a reservation the expiry takes.
type dueReservation struct {
	id, asset string
	stock     consumable.StockKey
}

// expireBatch expires up to expireBatchSize due reservations of orgID in
// one transaction (see ExpireDue); more reports a full batch.
func (r *PGRepository) expireBatch(ctx context.Context, orgID string, now time.Time) (count int, more bool, err error) {
	scope := database.OrgWideScope(orgID, "")
	err = database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		txCtx := database.ContextWithTx(ctx, tx)
		rows, queryErr := tx.Query(txCtx, `SELECT id::text, COALESCE(asset_id::text, ''), COALESCE(consumable_id::text, ''),
			COALESCE(location_id::text, '') FROM reservation
			WHERE organization_id = $1 AND state = 'active' AND expires_at IS NOT NULL AND expires_at < $2
			ORDER BY id LIMIT $3`, orgID, now, expireBatchSize)
		if queryErr != nil {
			return fmt.Errorf("read due reservations: %w", queryErr)
		}
		dues, collectErr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueReservation, error) {
			var d dueReservation
			return d, row.Scan(&d.id, &d.asset, &d.stock.ConsumableID, &d.stock.LocationID)
		})
		if collectErr != nil {
			return fmt.Errorf("read due reservations: %w", collectErr)
		}
		more = len(dues) == expireBatchSize
		var assetIDs []string
		var stock []consumable.StockKey
		for _, d := range dues {
			if d.asset != "" {
				assetIDs = append(assetIDs, d.asset)
			}
			if d.stock.ConsumableID != "" {
				stock = append(stock, d.stock)
			}
		}
		if lockErr := lockAssets(txCtx, tx, orgID, assetIDs); lockErr != nil {
			return lockErr
		}
		// The stock rows of the quantity reservations, ordered by
		// (consumable_id, location_id), before the reservations (WP-284).
		if _, lockErr := consumable.LockStock(txCtx, tx, orgID, stock); lockErr != nil {
			return lockErr
		}
		for _, d := range dues {
			done, expireErr := r.expireOne(txCtx, tx, orgID, d.id)
			if expireErr != nil {
				return expireErr
			}
			if done {
				count++
			}
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return count, more, nil
}

// expireOne sets the due reservation id to expired if it is still active
// (its asset is locked) and writes movement, audit entry and event; done
// is false for a reservation released or converted meanwhile.
func (r *PGRepository) expireOne(ctx context.Context, tx pgx.Tx, orgID, id string) (done bool, err error) {
	res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
		"UPDATE reservation SET state = 'expired', updated_at = now() WHERE id = $1 AND organization_id = $2 AND state = 'active' RETURNING %s",
		selectColumns), id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("expire reservation: %w", err)
	}
	if bookErr := r.book(ctx, tx, res, "release"); bookErr != nil {
		return false, bookErr
	}
	if auditErr := r.auditExpiry(ctx, tx, res); auditErr != nil {
		return false, auditErr
	}
	if emitErr := r.emit(ctx, tx, res, events.ReservationExpired); emitErr != nil {
		return false, emitErr
	}
	return true, nil
}

// auditExpiry registers the audit entry reservations.expire of res before
// the commit, ahead of its event (K20).
func (r *PGRepository) auditExpiry(ctx context.Context, tx pgx.Tx, res *Reservation) error {
	if r.recorder == nil {
		return nil
	}
	changes := map[string]any{"state": map[string]any{"old": "active", "new": res.State}, "asset_id": res.AssetID}
	if res.ItemKind == "consumable" {
		changes["consumable_id"], changes["location_id"], changes["quantity"] = res.ConsumableID, res.LocationID, res.Quantity
	}
	entry := audit.Entry{OrganizationID: res.OrganizationID, ActorType: "system", Action: "reservations.expire",
		ResourceType: "reservations", ResourceID: res.ID, Source: audit.SourceSystem, Changes: changes}
	return database.BeforeCommit(ctx, func() error {
		if _, err := r.recorder.Record(ctx, tx, entry); err != nil {
			return fmt.Errorf("audit reservation expiry: %w", err)
		}
		return nil
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Reservation, error) {
	res := &Reservation{}
	if err := s.Scan(
		&res.ID, &res.OrganizationID, &res.ItemKind, &res.AssetID,
		&res.ConsumableID, &res.LocationID, &res.ClientID, &res.SiteID, &res.Quantity, &res.State, &res.ReservedFrom,
		&res.ReservedUntil, &res.AssigneeID, &res.ProjectRef, &res.OrderID,
		&res.TicketID, &res.ExpiresAt, &res.Reason, &res.CreatedBy,
		&res.CreatedAt, &res.UpdatedAt,
	); err != nil {
		return nil, err
	}
	res.ReservedFrom = res.ReservedFrom.UTC()
	res.CreatedAt = res.CreatedAt.UTC()
	res.UpdatedAt = res.UpdatedAt.UTC()
	return res, nil
}

// nilIfZero is nil for the zero time: reserved_from then defaults to
// now().
func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
