package movement

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool   *pgxpool.Pool
	outbox *events.Outbox
}

// NewPGRepository creates a PostgreSQL-backed movement repository; its
// bookings write stock.movement to the outbox (WP-274).
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool, outbox: events.NewOutbox()}
}

const movementSelectColumns = `
	id::text, organization_id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''),
	item_kind, COALESCE(asset_id::text, ''), COALESCE(consumable_id::text, ''),
	COALESCE(quantity_item_id::text, ''), movement_type,
	COALESCE(from_location_id::text, ''), COALESCE(to_location_id::text, ''),
	quantity::float8, COALESCE(actor_id::text, ''), COALESCE(assignee_user_id::text, ''),
	COALESCE(assignee_team_id::text, ''), COALESCE(reason, ''),
	COALESCE(ticket_id::text, ''), COALESCE(order_id::text, ''),
	COALESCE(workflow_run_id::text, ''), COALESCE(document_id::text, ''),
	COALESCE(notes, ''), COALESCE(signature_hash, ''), occurred_at, created_at
`

// canonicalMovementType returns the canonical type of a legacy or canonical
// type name and whether it is one.
func canonicalMovementType(name string) (string, bool) {
	if mapped, ok := LegacyMovementTypes[name]; ok {
		return mapped, true
	}
	return name, MovementTypes[name]
}

// normalizeMovement checks the type and the object of m and keeps only
// its object, like migration 000242: without item_kind a movement with an
// asset is one of the asset, otherwise of the quantity item. An asset
// movement carries no quantity (callers before WP-274 sent 1), a quantity
// item movement a positive one. A legacy type name becomes the canonical
// one, which m carries afterwards.
func normalizeMovement(m *Movement) error {
	movementType, ok := canonicalMovementType(m.MovementType)
	if !ok {
		return &api.FieldError{Field: "movement_type", Message: "unknown movement_type", Status: http.StatusUnprocessableEntity}
	}
	m.MovementType = movementType
	if m.ItemKind == "" {
		m.ItemKind = "asset"
		if m.AssetID == "" {
			m.ItemKind = "quantity_item"
		}
	}
	switch m.ItemKind {
	case "asset":
		if m.AssetID == "" {
			return &api.FieldError{Field: "asset_id", Message: "asset_id is required for an asset movement", Status: http.StatusUnprocessableEntity}
		}
		m.QuantityItemID = ""
		m.Quantity = nil
	case "quantity_item":
		if m.QuantityItemID == "" {
			return &api.FieldError{Field: "quantity_item_id", Message: "quantity_item_id is required for a quantity item movement", Status: http.StatusUnprocessableEntity}
		}
		m.AssetID = ""
		if m.Quantity == nil || *m.Quantity <= 0 {
			return &api.FieldError{Field: "quantity", Message: "quantity must be greater than 0", Status: http.StatusUnprocessableEntity}
		}
	default:
		return &api.FieldError{Field: "item_kind", Message: "item_kind must be asset or quantity_item", Status: http.StatusUnprocessableEntity}
	}
	m.ConsumableID = ""
	return nil
}

// requireRow fails with missing when id (if set) names no row of table that
// is visible under the caller's scope. The foreign keys of stock_movement
// and quantity_item are checked without row level security: an object of
// another organization or outside the scope passed them, an unknown one
// failed with the constraint text, so the two could be told apart (TEN-04,
// API-03, F-047). The check runs before the write, in its transaction.
func requireRow(ctx context.Context, tx pgx.Tx, table, orgID, id string, missing error) error {
	if nilIfEmpty(id) == nil {
		return nil
	}
	if !api.IsUUID(id) {
		return missing
	}
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND organization_id = $2)", id, orgID).Scan(&visible); err != nil {
		return fmt.Errorf("check %s: %w", table, err)
	}
	if !visible {
		return missing
	}
	return nil
}

// locationNotFound is the 422 field error of a location that is no UUID or
// not visible under the caller's scope.
func locationNotFound(field string) error {
	return &api.FieldError{Field: field, Message: "location not found", Status: http.StatusUnprocessableEntity}
}

// locationOfOtherClient is the 422 field error of a location of another
// client than the moved object or one of the CIs of a moved asset: the
// mirror of asset.location_id would fail on ci_location_client.
func locationOfOtherClient(field string) error {
	return &api.FieldError{Field: field, Message: "location belongs to another client", Status: http.StatusUnprocessableEntity}
}

// referenceNotFound is the 422 field error of a ticket, order, workflow
// run or document that is not visible under the caller's scope.
func referenceNotFound(field string) error {
	return &api.FieldError{Field: field, Message: "referenced object not found", Status: http.StatusUnprocessableEntity}
}

// lockObject locks the moved asset or quantity item (SELECT … FOR UPDATE,
// which applies the select and update policies) and returns its client and,
// for a quantity item, its stock level. An object that is unknown, of
// another organization or outside the caller's scope is ErrAssetNotFound
// or ErrItemNotFound.
func lockObject(ctx context.Context, tx pgx.Tx, m *Movement) (client string, level float64, err error) {
	if m.ItemKind == "asset" {
		if !api.IsUUID(m.AssetID) {
			return "", 0, ErrAssetNotFound
		}
		err = tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM asset
			WHERE id = $1 AND organization_id = $2 FOR UPDATE`, m.AssetID, m.OrganizationID).Scan(&client)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, ErrAssetNotFound
		}
	} else {
		if !api.IsUUID(m.QuantityItemID) {
			return "", 0, ErrItemNotFound
		}
		err = tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), stock_level::float8 FROM quantity_item
			WHERE id = $1 AND organization_id = $2 FOR UPDATE`, m.QuantityItemID, m.OrganizationID).Scan(&client, &level)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, ErrItemNotFound
		}
	}
	if err != nil {
		return "", 0, fmt.Errorf("lock moved object: %w", err)
	}
	return client, level, nil
}

// checkLocation fails with a 422 field error when id (if set) names no
// location visible under the caller's scope, or one of another client
// than the moved object or, for an asset, one of its CIs.
func checkLocation(ctx context.Context, tx pgx.Tx, m *Movement, field, id, client string) error {
	if id == "" {
		return nil
	}
	if !api.IsUUID(id) {
		return locationNotFound(field)
	}
	var locationClient string
	err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM location WHERE id = $1 AND organization_id = $2`,
		id, m.OrganizationID).Scan(&locationClient)
	if errors.Is(err, pgx.ErrNoRows) {
		return locationNotFound(field)
	}
	if err != nil {
		return fmt.Errorf("check location: %w", err)
	}
	if locationClient == "" {
		return nil
	}
	if client != "" && client != locationClient {
		return locationOfOtherClient(field)
	}
	if m.ItemKind == "asset" {
		var foreign bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND asset_id = $2
				AND client_id IS NOT NULL AND client_id <> $3::uuid)`,
			m.OrganizationID, m.AssetID, locationClient).Scan(&foreign); err != nil {
			return fmt.Errorf("check location client: %w", err)
		}
		if foreign {
			return locationOfOtherClient(field)
		}
	}
	return nil
}

// Record books a movement in a transaction of its own (WithRequestTenant),
// or in the transaction the context carries (database.ContextWithTx,
// WP-332), like RecordTx.
func (r *PGRepository) Record(ctx context.Context, m *Movement) error {
	if err := normalizeMovement(m); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return recordTx(ctx, tx, r.outbox, m)
	})
}

// RecordTx books a movement in tx, the transaction of the calling
// operation (STK-04, AST-07, WP-274): the moved asset or quantity item is
// locked (FOR UPDATE); type, object (not visible: ErrAssetNotFound or
// ErrItemNotFound), locations (visible and of the object's client, else a
// 422 field error) and the references ticket_id, order_id,
// workflow_run_id and document_id (visible, else 422) are checked; the row
// is inserted into stock_movement, whose triggers derive client and site
// and refuse any later change; an asset with to_location_id gets it as
// location_id (mirrored to its CIs), a quantity item its stock level
// (receipt and correction add, transfer keeps, the others take away; a
// shortfall is ErrInsufficientStock); last, stock.movement is appended to
// the outbox (K20). Any error must abort the calling operation, which
// rolls back movement and event together.
func RecordTx(ctx context.Context, tx pgx.Tx, m *Movement) error {
	if err := normalizeMovement(m); err != nil {
		return err
	}
	return recordTx(ctx, tx, events.NewOutbox(), m)
}

func recordTx(ctx context.Context, tx pgx.Tx, outbox *events.Outbox, m *Movement) error {
	client, level, err := lockObject(ctx, tx, m)
	if err != nil {
		return err
	}
	if fromErr := checkLocation(ctx, tx, m, "from_location_id", m.FromLocationID, client); fromErr != nil {
		return fromErr
	}
	if toErr := checkLocation(ctx, tx, m, "to_location_id", m.ToLocationID, client); toErr != nil {
		return toErr
	}
	for _, ref := range []struct{ table, field, id string }{
		{"ticket", "ticket_id", m.TicketID},
		{"internal_order", "order_id", m.OrderID},
		{"workflow_run", "workflow_run_id", m.WorkflowRunID},
		{"document", "document_id", m.DocumentID},
	} {
		if refErr := requireRow(ctx, tx, ref.table, m.OrganizationID, ref.id, referenceNotFound(ref.field)); refErr != nil {
			return refErr
		}
	}
	var delta float64
	if m.ItemKind == "quantity_item" {
		delta = stockDelta(m.MovementType, *m.Quantity)
		if level+delta < 0 {
			return ErrInsufficientStock
		}
	}
	var clientID, siteID *string
	err = tx.QueryRow(ctx, `
		INSERT INTO stock_movement (
			organization_id, item_kind, asset_id, quantity_item_id,
			movement_type, from_location_id, to_location_id, quantity,
			actor_id, assignee_user_id, assignee_team_id, reason, ticket_id,
			order_id, workflow_run_id, document_id, notes
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id::text, client_id::text, site_id::text, occurred_at, created_at`,
		m.OrganizationID, m.ItemKind, nilIfEmpty(m.AssetID), nilIfEmpty(m.QuantityItemID),
		m.MovementType, nilIfEmpty(m.FromLocationID), nilIfEmpty(m.ToLocationID),
		m.Quantity, nilIfEmpty(m.ActorID), nilIfEmpty(m.AssigneeUserID), nilIfEmpty(m.AssigneeTeamID),
		nilIfEmpty(m.Reason), nilIfEmpty(m.TicketID), nilIfEmpty(m.OrderID),
		nilIfEmpty(m.WorkflowRunID), nilIfEmpty(m.DocumentID), nilIfEmpty(m.Notes),
	).Scan(&m.ID, &clientID, &siteID, &m.OccurredAt, &m.CreatedAt)
	if err != nil {
		return fmt.Errorf("record movement: %w", err)
	}
	m.ClientID, m.SiteID = deref(clientID), deref(siteID)
	m.OccurredAt, m.CreatedAt = m.OccurredAt.UTC(), m.CreatedAt.UTC()
	entity := m.AssetID
	switch {
	case m.ItemKind == "quantity_item":
		entity = m.QuantityItemID
		if delta != 0 {
			if _, err = tx.Exec(ctx,
				"UPDATE quantity_item SET stock_level = stock_level + $2 WHERE id = $1 AND organization_id = $3",
				m.QuantityItemID, delta, m.OrganizationID); err != nil {
				return fmt.Errorf("adjust stock level: %w", err)
			}
		}
	case m.ToLocationID != "":
		// The mirror trigger sets the location of every CI of the asset
		// (asset_propagate_location); a CI outside the scope fails it with
		// asset_location_ci_scope, which the wrapped error keeps.
		tag, updErr := tx.Exec(ctx,
			"UPDATE asset SET location_id = $2, updated_at = now() WHERE id = $1 AND organization_id = $3",
			m.AssetID, m.ToLocationID, m.OrganizationID)
		if updErr != nil {
			return fmt.Errorf("update asset location: %w", updErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrAssetNotFound
		}
	}
	if _, err = outbox.Append(ctx, tx, &events.Event{OrgID: m.OrganizationID, Type: events.StockMovement, EntityID: entity,
		Data: events.StockMovementData{ID: m.ID, ItemKind: m.ItemKind, AssetID: m.AssetID, QuantityItemID: m.QuantityItemID,
			MovementType: m.MovementType, Quantity: m.Quantity, ClientID: m.ClientID, SiteID: m.SiteID}}); err != nil {
		return fmt.Errorf("record movement event: %w", err)
	}
	return nil
}

// The movement lists sort newest first on (occurred_at, id); the keyset
// cursor pins this order.
const (
	movementSort = "occurred_at"
	movementDir  = "desc"
)

// ListMovements returns the movements of the ledger of every object kind
// filtered by the given parameters, newest first on (occurred_at, id),
// with at most one row more than page.Limit so the caller knows whether
// more follow (MovementPage), and the number of matching rows. A legacy
// type name filters by its canonical type. The policy of stock_movement
// applies the caller's client and site scope.
func (r *PGRepository) ListMovements(ctx context.Context, orgID string, filter *MovementFilter, page api.PaginationParams) ([]Movement, int, error) {
	var out []Movement
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		add := func(clause string, value any) {
			args = append(args, value)
			where = append(where, strings.ReplaceAll(clause, "?", fmt.Sprintf("$%d", len(args))))
		}
		if filter.AssetID != "" {
			add("asset_id = ?", filter.AssetID)
		}
		if filter.ConsumableID != "" {
			add("consumable_id = ?", filter.ConsumableID)
		}
		if filter.QuantityItemID != "" {
			add("quantity_item_id = ?", filter.QuantityItemID)
		}
		if filter.MovementType != "" {
			movementType, _ := canonicalMovementType(filter.MovementType)
			add("movement_type = ?", movementType)
		}
		if filter.LocationID != "" {
			add("(from_location_id = ? OR to_location_id = ?)", filter.LocationID)
		}
		if filter.OccurredFrom != nil {
			add("occurred_at >= ?", *filter.OccurredFrom)
		}
		if filter.OccurredTo != nil {
			add("occurred_at <= ?", *filter.OccurredTo)
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_movement WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count movements: %w", err)
		}
		if page.Cursor != nil {
			at, id, posErr := movementPosition(*page.Cursor)
			if posErr != nil {
				return posErr
			}
			clause += " AND " + api.KeysetClause(movementSort, "timestamptz", movementDir, len(args)+1)
			args = append(args, at, id)
		}
		tail := fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args)+1)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", len(args)+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+movementSelectColumns+" FROM stock_movement WHERE "+clause+tail, args...)
		if err != nil {
			return fmt.Errorf("list movements: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			m, scanErr := scanMovement(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *m)
		}
		return rows.Err()
	})
	return out, total, err
}

// movementPosition returns the (occurred_at, id) position of a keyset
// cursor of the movement lists; a cursor of another order or with a
// malformed position is api.ErrInvalidCursor.
// validation: fixed texts wrapping api.ErrInvalidCursor (API-03).
func movementPosition(c api.Cursor) (time.Time, string, error) {
	if !c.Matches(movementSort, movementDir) {
		return time.Time{}, "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	at, err := time.Parse(time.RFC3339Nano, c.Value)
	if err != nil || !api.IsUUID(c.ID) {
		return time.Time{}, "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return at, c.ID, nil
}

// MovementPage is the list response of the rows ListMovements read (at
// most one more than the limit): when more follow, the page ends at the
// limit, has_more is set and next_cursor is the keyset cursor after its
// last row.
func MovementPage(rows []Movement, total int, page api.PaginationParams) api.ListResponse[Movement] {
	list := api.ListResponse[Movement]{Data: api.NonNilSlice(rows), Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := &list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: movementSort, Dir: movementDir,
			Value: last.OccurredAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
	}
	return list
}

const itemSelectColumns = `
	id::text, organization_id::text, COALESCE(client_id::text, ''),
	COALESCE(sku, ''), name, category, unit, stock_level, min_level,
	COALESCE(location_id::text, ''), COALESCE(notes, ''), attributes,
	created_at, updated_at
`

// ListItems returns quantity items.
func (r *PGRepository) ListItems(ctx context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error) {
	var out []QuantityItem
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM quantity_item WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return fmt.Errorf("count quantity items: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM quantity_item WHERE organization_id = $1 ORDER BY name ASC LIMIT $2 OFFSET $3",
			itemSelectColumns), orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list quantity items: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanItem(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetItem returns one quantity item.
func (r *PGRepository) GetItem(ctx context.Context, orgID, id string) (*QuantityItem, error) {
	var out *QuantityItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		item, err := scanItem(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM quantity_item WHERE id = $1 AND organization_id = $2",
			itemSelectColumns), id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrItemNotFound
			}
			return fmt.Errorf("get quantity item: %w", err)
		}
		out = item
		return nil
	})
	return out, err
}

// CreateItem inserts a quantity item. A location that is unknown, of another
// organization or outside the caller's scope is a 422 field error; a client
// outside the scope is refused by the policy of quantity_item, an unknown
// one or one of another organization by its composite foreign key.
func (r *PGRepository) CreateItem(ctx context.Context, item *QuantityItem) error {
	if nilIfEmpty(item.ClientID) != nil {
		if err := api.RequireUUID("client_id", item.ClientID); err != nil {
			return err
		}
	}
	return database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Attributes == nil {
			item.Attributes = map[string]any{}
		}
		if err := requireRow(ctx, tx, "location", item.OrganizationID, item.LocationID, locationNotFound("location_id")); err != nil {
			return err
		}
		category := item.Category
		if category == "" {
			category = "general"
		}
		unit := item.Unit
		if unit == "" {
			unit = "pcs"
		}
		return tx.QueryRow(ctx, `
			INSERT INTO quantity_item (
				organization_id, client_id, sku, name, category, unit,
				stock_level, min_level, location_id, notes, attributes
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id::text, created_at, updated_at`,
			item.OrganizationID, nilIfEmpty(item.ClientID), nilIfEmpty(item.SKU),
			item.Name, category, unit, item.StockLevel, item.MinLevel,
			nilIfEmpty(item.LocationID), nilIfEmpty(item.Notes), item.Attributes,
		).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	})
}

// UpdateItem modifies a quantity item (metadata only; stock levels change via
// movements so the level stays auditable).
func (r *PGRepository) UpdateItem(ctx context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error) {
	var out *QuantityItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if req.LocationID != nil {
			if err := requireRow(ctx, tx, "location", orgID, *req.LocationID, locationNotFound("location_id")); err != nil {
				return err
			}
		}
		sets := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Category != nil {
			sets = append(sets, fmt.Sprintf("category = $%d", pos))
			args = append(args, *req.Category)
			pos++
		}
		if req.Unit != nil {
			sets = append(sets, fmt.Sprintf("unit = $%d", pos))
			args = append(args, *req.Unit)
			pos++
		}
		if req.MinLevel != nil {
			sets = append(sets, fmt.Sprintf("min_level = $%d", pos))
			args = append(args, *req.MinLevel)
			pos++
		}
		if req.LocationID != nil {
			sets = append(sets, fmt.Sprintf("location_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.LocationID))
			pos++
		}
		if req.Notes != nil {
			sets = append(sets, fmt.Sprintf("notes = $%d", pos))
			args = append(args, nilIfEmpty(*req.Notes))
			pos++
		}
		if req.Attributes != nil {
			sets = append(sets, fmt.Sprintf("attributes = $%d", pos))
			args = append(args, req.Attributes)
		}
		if len(sets) == 0 {
			item, err := scanItem(tx.QueryRow(ctx, fmt.Sprintf(
				"SELECT %s FROM quantity_item WHERE id = $1 AND organization_id = $2",
				itemSelectColumns), id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return ErrItemNotFound
				}
				return err
			}
			out = item
			return nil
		}
		item, err := scanItem(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE quantity_item SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(sets, ", "), itemSelectColumns), args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrItemNotFound
			}
			return fmt.Errorf("update quantity item: %w", err)
		}
		out = item
		return nil
	})
	return out, err
}

// DeleteItem removes a quantity item. One kept by movements of the ledger
// (RESTRICT, WP-744) is ErrHasMovements.
func (r *PGRepository) DeleteItem(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM quantity_item WHERE id = $1 AND organization_id = $2", id, orgID)
		if IsLedgerReference(err) {
			return ErrHasMovements
		}
		if err != nil {
			return fmt.Errorf("delete quantity item: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrItemNotFound
		}
		return nil
	})
}

type movementScanner interface {
	Scan(dest ...any) error
}

func scanMovement(scanner movementScanner) (*Movement, error) {
	m := &Movement{}
	if err := scanner.Scan(
		&m.ID, &m.OrganizationID, &m.ClientID, &m.SiteID, &m.ItemKind, &m.AssetID,
		&m.ConsumableID, &m.QuantityItemID, &m.MovementType, &m.FromLocationID,
		&m.ToLocationID, &m.Quantity, &m.ActorID, &m.AssigneeUserID, &m.AssigneeTeamID,
		&m.Reason, &m.TicketID, &m.OrderID, &m.WorkflowRunID, &m.DocumentID,
		&m.Notes, &m.SignatureHash, &m.OccurredAt, &m.CreatedAt,
	); err != nil {
		return nil, err
	}
	m.OccurredAt, m.CreatedAt = m.OccurredAt.UTC(), m.CreatedAt.UTC()
	return m, nil
}

func scanItem(scanner movementScanner) (*QuantityItem, error) {
	item := &QuantityItem{}
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(
		&item.ID, &item.OrganizationID, &item.ClientID, &item.SKU, &item.Name,
		&item.Category, &item.Unit, &item.StockLevel, &item.MinLevel,
		&item.LocationID, &item.Notes, &item.Attributes, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Attributes == nil {
		item.Attributes = map[string]any{}
	}
	item.CreatedAt = createdAt.UTC()
	item.UpdatedAt = updatedAt.UTC()
	return item, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
