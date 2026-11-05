package movement

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	item_kind, COALESCE(asset_id::text, ''), COALESCE(consumable_id::text, ''), movement_type,
	COALESCE(from_location_id::text, ''), COALESCE(to_location_id::text, ''),
	quantity::float8, COALESCE(actor_id::text, ''), COALESCE(assignee_user_id::text, ''),
	COALESCE(assignee_team_id::text, ''), due_at, COALESCE(reason, ''),
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
// asset is one of the asset, otherwise of the consumable. quantity_item_id
// (item_kind quantity_item) is the deprecated alias of consumable_id since
// the merge of quantity_item into consumable with the same ids (E-40,
// WP-757). An asset movement carries no quantity (callers before WP-274
// sent 1), a consumable movement a positive one. A legacy type name
// becomes the canonical one, which m carries afterwards.
func normalizeMovement(m *Movement) error {
	movementType, ok := canonicalMovementType(m.MovementType)
	if !ok {
		return fieldError("movement_type", "unknown movement_type")
	}
	m.MovementType = movementType
	if m.ItemKind == "" {
		switch {
		case m.AssetID != "":
			m.ItemKind = "asset"
		case m.ConsumableID == "" && m.QuantityItemID != "":
			m.ItemKind = "quantity_item"
		default:
			m.ItemKind = "consumable"
		}
	}
	if m.ItemKind == "quantity_item" {
		if m.QuantityItemID == "" {
			return fieldError("quantity_item_id", "quantity_item_id is required for a quantity item movement")
		}
		m.ItemKind, m.ConsumableID, m.itemAlias = "consumable", m.QuantityItemID, true
	}
	switch m.ItemKind {
	case "asset":
		if m.AssetID == "" {
			return fieldError("asset_id", "asset_id is required for an asset movement")
		}
		m.QuantityItemID = ""
		m.ConsumableID = ""
		m.Quantity = nil
	case "consumable":
		if m.ConsumableID == "" {
			return fieldError("consumable_id", "consumable_id is required for a consumable movement")
		}
		m.AssetID, m.QuantityItemID = "", ""
		if m.Quantity == nil || *m.Quantity <= 0 {
			return fieldError("quantity", "quantity must be greater than 0")
		}
	default:
		return fieldError("item_kind", "item_kind must be asset or consumable (quantity_item is the alias of consumable)")
	}
	return nil
}

// requireRow fails with missing when id (if set) names no row of table that
// is visible under the caller's scope. The foreign keys of stock_movement
// are checked without row level security: an object of
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

// lockAsset locks the moved asset (SELECT … FOR UPDATE, which applies the
// select and update policies) and returns its client. An asset that is
// unknown, of another organization or outside the caller's scope is
// ErrAssetNotFound.
func lockAsset(ctx context.Context, tx pgx.Tx, m *Movement) (string, error) {
	if !api.IsUUID(m.AssetID) {
		return "", ErrAssetNotFound
	}
	var client string
	err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM asset
		WHERE id = $1 AND organization_id = $2 FOR UPDATE`, m.AssetID, m.OrganizationID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAssetNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock moved object: %w", err)
	}
	return client, nil
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
	return aliasError(m, database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return recordTx(ctx, tx, r.outbox, m)
	}))
}

// RecordTx books a movement in tx, the transaction of the calling
// operation (STK-04, AST-07, WP-274): a moved asset is locked (FOR
// UPDATE) and its locations are checked (visible and of the asset's
// client, else a 422 field error); a consumable is locked, checked and
// booked on its stock per bin by consumable.ApplyBooking before the row is
// inserted (bookConsumable, WP-756); quantity_item_id names the consumable
// of the same id (WP-757, ErrItemNotFound when not visible). The
// references ticket_id, order_id, workflow_run_id and document_id are
// checked (visible, else 422); the row is inserted into stock_movement,
// whose triggers derive client and site and refuse any later change; an
// asset with to_location_id gets it as location_id (mirrored to its CIs);
// last, stock.movement is appended to the outbox (K20). Any error must
// abort the calling operation, which rolls back movement and event
// together.
func RecordTx(ctx context.Context, tx pgx.Tx, m *Movement) error {
	if err := normalizeMovement(m); err != nil {
		return err
	}
	return aliasError(m, recordTx(ctx, tx, events.NewOutbox(), m))
}

// lockAndCheck locks the moved asset and checks its locations (lockAsset,
// checkLocation); a consumable is locked, checked and booked by
// bookConsumable.
func lockAndCheck(ctx context.Context, tx pgx.Tx, m *Movement) error {
	if m.ItemKind == "consumable" {
		return bookConsumable(ctx, tx, m)
	}
	client, err := lockAsset(ctx, tx, m)
	if err != nil {
		return err
	}
	if fromErr := checkLocation(ctx, tx, m, "from_location_id", m.FromLocationID, client); fromErr != nil {
		return fromErr
	}
	return checkLocation(ctx, tx, m, "to_location_id", m.ToLocationID, client)
}

func recordTx(ctx context.Context, tx pgx.Tx, outbox *events.Outbox, m *Movement) error {
	if err := lockAndCheck(ctx, tx, m); err != nil {
		return err
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
	var clientID, siteID *string
	err := tx.QueryRow(ctx, `
		INSERT INTO stock_movement (
			organization_id, item_kind, asset_id, consumable_id,
			movement_type, from_location_id, to_location_id, quantity,
			actor_id, assignee_user_id, assignee_team_id, due_at, reason, ticket_id,
			order_id, workflow_run_id, document_id, notes
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING id::text, client_id::text, site_id::text, occurred_at, created_at`,
		m.OrganizationID, m.ItemKind, nilIfEmpty(m.AssetID), nilIfEmpty(m.ConsumableID),
		m.MovementType, nilIfEmpty(m.FromLocationID), nilIfEmpty(m.ToLocationID),
		m.Quantity, nilIfEmpty(m.ActorID), nilIfEmpty(m.AssigneeUserID), nilIfEmpty(m.AssigneeTeamID),
		m.DueAt, nilIfEmpty(m.Reason), nilIfEmpty(m.TicketID), nilIfEmpty(m.OrderID),
		nilIfEmpty(m.WorkflowRunID), nilIfEmpty(m.DocumentID), nilIfEmpty(m.Notes),
	).Scan(&m.ID, &clientID, &siteID, &m.OccurredAt, &m.CreatedAt)
	if err != nil {
		return fmt.Errorf("record movement: %w", err)
	}
	m.ClientID, m.SiteID = deref(clientID), deref(siteID)
	m.OccurredAt, m.CreatedAt = m.OccurredAt.UTC(), m.CreatedAt.UTC()
	entity := m.AssetID
	if m.ItemKind == "consumable" {
		entity = m.ConsumableID
	} else if m.ToLocationID != "" {
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
		Data: events.StockMovementData{ID: m.ID, ItemKind: m.ItemKind, AssetID: m.AssetID,
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
	// An id filter that is no UUID names no row (the deprecated consumable
	// path passes its path id unchecked, WP-756).
	for _, id := range []string{filter.AssetID, filter.ConsumableID, filter.QuantityItemID, filter.LocationID} {
		if id != "" && !api.IsUUID(id) {
			return nil, 0, nil
		}
	}
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
			// The deprecated alias of consumable_id (WP-757).
			add("consumable_id = ?", filter.QuantityItemID)
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

// itemSelect reads the articles of the alias inventory/items from
// consumable (E-40, WP-757): stock_level is the sum of on_hand over the
// bins the caller sees (stock_level_isolation), location_id the bin when
// there is exactly one such row.
const itemSelect = `SELECT c.id::text, c.organization_id::text, COALESCE(c.client_id::text, ''),
	COALESCE(c.sku, ''), c.name, c.category, c.unit,
	COALESCE(s.on_hand, 0)::float8, c.min_level::float8, COALESCE(s.bin, ''), COALESCE(c.notes, ''),
	c.created_at, c.updated_at
	FROM consumable c
	LEFT JOIN LATERAL (SELECT sum(l.on_hand) AS on_hand,
	                          CASE WHEN count(*) = 1 THEN min(l.location_id::text) END AS bin
	                     FROM stock_level l
	                    WHERE l.consumable_id = c.id AND l.organization_id = c.organization_id) s ON true`

// ListItems returns the articles of the alias, ordered by name.
func (r *PGRepository) ListItems(ctx context.Context, orgID string, page api.PaginationParams) ([]QuantityItem, int, error) {
	var out []QuantityItem
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM consumable WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return fmt.Errorf("count inventory items: %w", err)
		}
		rows, err := tx.Query(ctx, itemSelect+" WHERE c.organization_id = $1 ORDER BY c.name ASC, c.id LIMIT $2 OFFSET $3",
			orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list inventory items: %w", err)
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

// getItem reads the article id in tx; an invisible one is ErrItemNotFound.
func getItem(ctx context.Context, tx pgx.Tx, orgID, id string) (*QuantityItem, error) {
	if !api.IsUUID(id) {
		return nil, ErrItemNotFound
	}
	item, err := scanItem(tx.QueryRow(ctx, itemSelect+" WHERE c.id = $1 AND c.organization_id = $2", id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get inventory item: %w", err)
	}
	return item, nil
}

// GetItem returns one article of the alias.
func (r *PGRepository) GetItem(ctx context.Context, orgID, id string) (*QuantityItem, error) {
	var out *QuantityItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getItem(ctx, tx, orgID, id)
		return err
	})
	return out, err
}

// CreateItem inserts the article into consumable and books a stock_level
// > 0 as a receipt into the bin location_id through the movement service
// in the same transaction (checkNewItem, WP-757); a location that is no
// visible bin of the article's client is 422 on location_id. A client
// outside the scope is refused by the policy of consumable, an unknown one
// or one of another organization by its composite foreign key.
func (r *PGRepository) CreateItem(ctx context.Context, item *QuantityItem) error {
	if err := checkNewItem(item); err != nil {
		return err
	}
	if nilIfEmpty(item.ClientID) != nil {
		if err := api.RequireUUID("client_id", item.ClientID); err != nil {
			return err
		}
	}
	return database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Category == "" {
			item.Category = "general"
		}
		if item.Unit == "" {
			item.Unit = "pcs"
		}
		item.Attributes = map[string]any{}
		if err := tx.QueryRow(ctx, `
			INSERT INTO consumable (organization_id, client_id, sku, name, category, unit, min_level, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id::text, created_at, updated_at`,
			item.OrganizationID, nilIfEmpty(item.ClientID), nilIfEmpty(item.SKU), item.Name, item.Category, item.Unit,
			item.MinLevel, nilIfEmpty(item.Notes),
		).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return fmt.Errorf("create inventory item: %w", err)
		}
		item.CreatedAt, item.UpdatedAt = item.CreatedAt.UTC(), item.UpdatedAt.UTC()
		if item.StockLevel == 0 {
			return nil
		}
		return asLocationField(recordTx(ctx, tx, r.outbox, receiptOf(item)))
	})
}

// UpdateItem modifies the metadata of an article (checkItemUpdate): stock
// changes only through movements, so it stays auditable.
func (r *PGRepository) UpdateItem(ctx context.Context, orgID, id string, req UpdateItemRequest) (*QuantityItem, error) {
	if err := checkItemUpdate(&req); err != nil {
		return nil, err
	}
	var out *QuantityItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if !api.IsUUID(id) {
			return ErrItemNotFound
		}
		sets := []string{}
		args := []any{id, orgID}
		set := func(column string, value any) {
			args = append(args, value)
			sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
		}
		if req.Name != nil {
			set("name", *req.Name)
		}
		if req.Category != nil {
			set("category", *req.Category)
		}
		if req.Unit != nil {
			set("unit", *req.Unit)
		}
		if req.MinLevel != nil {
			set("min_level", *req.MinLevel)
		}
		if req.Notes != nil {
			set("notes", nilIfEmpty(*req.Notes))
		}
		if len(sets) > 0 {
			tag, err := tx.Exec(ctx, "UPDATE consumable SET "+strings.Join(sets, ", ")+", updated_at = now() WHERE id = $1 AND organization_id = $2",
				args...)
			if err != nil {
				return fmt.Errorf("update inventory item: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return ErrItemNotFound
			}
		}
		var err error
		out, err = getItem(ctx, tx, orgID, id)
		return err
	})
	return out, err
}

// DeleteItem removes the article like DELETE /api/v1/consumables/{id}
// (consumable.PGRepository.Delete): stock in a bin keeps it
// (consumable.ErrHasStock), movements of the ledger as well (RESTRICT,
// ErrHasMovements).
func (r *PGRepository) DeleteItem(ctx context.Context, orgID, id string) error {
	err := consumable.NewPGRepository(r.pool).Delete(ctx, orgID, id)
	var pgErr *pgconn.PgError
	switch {
	case IsLedgerReference(err):
		return ErrHasMovements
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "stock_level":
		// A row in a bin the caller cannot see (RESTRICT of stock_level).
		return consumable.ErrHasStock
	}
	return asItemError(err)
}

// ConvertItem converts one unit of the article into a serialized asset in
// one transaction (WP-757, E-38): the bin is conv.LocationID or the only
// bin of the article (consumable.OnlyBin, else 422 on location_id); the
// asset is created in that bin through assets, which joins the transaction
// (database.ContextWithTx), and the retrieval of one unit is booked
// through the movement service. Any error, a shortfall
// (ErrInsufficientStock) included, rolls back asset and movement.
func (r *PGRepository) ConvertItem(ctx context.Context, conv *Conversion, assets AssetCreator) error {
	return database.WithRequestTenant(ctx, r.pool, conv.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		item, err := getItem(ctx, tx, conv.OrganizationID, conv.ItemID)
		if err != nil {
			return err
		}
		bin := conv.LocationID
		if bin == "" {
			if bin, err = consumable.OnlyBin(ctx, tx, conv.OrganizationID, conv.ItemID); err != nil {
				return asItemError(err)
			}
		}
		assetID, err := assets.Create(database.ContextWithTx(ctx, tx), &AssetRef{OrganizationID: conv.OrganizationID,
			ClientID: item.ClientID, AssetTag: conv.AssetTag, Name: item.Name, Category: "hardware", LocationID: bin,
			SerialNumber: conv.SerialNumber})
		if err != nil {
			return err
		}
		m := retrievalOf(conv, bin, assetID)
		if err = recordTx(ctx, tx, r.outbox, m); err != nil {
			return asItemError(asLocationField(err))
		}
		conv.AssetID, conv.Movement = assetID, m
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
		&m.ConsumableID, &m.MovementType, &m.FromLocationID,
		&m.ToLocationID, &m.Quantity, &m.ActorID, &m.AssigneeUserID, &m.AssigneeTeamID,
		&m.DueAt, &m.Reason, &m.TicketID, &m.OrderID, &m.WorkflowRunID, &m.DocumentID,
		&m.Notes, &m.SignatureHash, &m.OccurredAt, &m.CreatedAt,
	); err != nil {
		return nil, err
	}
	m.OccurredAt, m.CreatedAt = m.OccurredAt.UTC(), m.CreatedAt.UTC()
	if m.DueAt != nil {
		due := m.DueAt.UTC()
		m.DueAt = &due
	}
	return m, nil
}

func scanItem(scanner movementScanner) (*QuantityItem, error) {
	item := &QuantityItem{Attributes: map[string]any{}}
	if err := scanner.Scan(
		&item.ID, &item.OrganizationID, &item.ClientID, &item.SKU, &item.Name,
		&item.Category, &item.Unit, &item.StockLevel, &item.MinLevel,
		&item.LocationID, &item.Notes, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
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
