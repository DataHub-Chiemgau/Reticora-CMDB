package movement

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed movement repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

const movementSelectColumns = `
	id::text, organization_id::text, item_kind, COALESCE(asset_id::text, ''),
	COALESCE(quantity_item_id::text, ''), movement_type,
	COALESCE(from_location_id::text, ''), COALESCE(to_location_id::text, ''),
	quantity, COALESCE(actor_id::text, ''), COALESCE(reason, ''),
	COALESCE(ticket_id::text, ''), COALESCE(order_id::text, ''),
	COALESCE(workflow_run_id::text, ''), COALESCE(document_id::text, ''),
	COALESCE(notes, ''), created_at
`

// legacyMovementTypes maps the type names of asset_movement that STK-04
// does not know to the canonical types of stock_movement, like migration
// 000242 does for the existing rows (WP-744).
var legacyMovementTypes = map[string]string{
	"warehouse_transfer":  "transfer",
	"bin_transfer":        "transfer",
	"reservation_release": "release",
	"repair_transfer":     "repair",
}

// canonicalMovementTypes are the movement types of stock_movement
// (stock_movement_movement_type_check, STK-04).
var canonicalMovementTypes = map[string]bool{
	"receipt": true, "transfer": true, "assignment": true, "return": true,
	"deployment": true, "retrieval": true, "reservation": true, "release": true,
	"repair": true, "disposal": true, "correction": true,
}

// canonicalMovementType returns the canonical type of a legacy or canonical
// type name and whether it is one.
func canonicalMovementType(name string) (string, bool) {
	if mapped, ok := legacyMovementTypes[name]; ok {
		return mapped, true
	}
	return name, canonicalMovementTypes[name]
}

// normalizeObject fixes the item kind of m and keeps only its object, like
// migration 000242: without item_kind a movement with an asset is one of the
// asset, otherwise of the quantity item. An asset movement carries no
// quantity (the booking dialog sends 1), a quantity item movement a positive
// one.
func normalizeObject(m *Movement) error {
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

// Record appends a movement to the ledger stock_movement (WP-744). The
// append-only trigger rejects any later mutation, the trigger
// stock_movement_derive_scope sets client and site, and the policy refuses
// a row outside the caller's scope (E-09). Legacy type names are mapped to
// the canonical ones, which m carries afterwards. Quantity items get their
// stock level adjusted inside the same transaction. An asset or quantity
// item that is unknown, of another organization or outside the caller's
// scope fails with ErrAssetNotFound or ErrItemNotFound, a location likewise
// with a 422 field error, before anything is written.
func (r *PGRepository) Record(ctx context.Context, m *Movement) error {
	movementType, ok := canonicalMovementType(m.MovementType)
	if !ok {
		return fmt.Errorf("invalid movement_type %q", m.MovementType)
	}
	if m.AssetID == "" && m.QuantityItemID == "" {
		return fmt.Errorf("asset_id or quantity_item_id is required")
	}
	if err := normalizeObject(m); err != nil {
		return err
	}
	m.MovementType = movementType
	return database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		for _, ref := range []struct {
			table, id string
			missing   error
		}{
			{"asset", m.AssetID, ErrAssetNotFound},
			{"quantity_item", m.QuantityItemID, ErrItemNotFound},
			{"location", m.FromLocationID, locationNotFound("from_location_id")},
			{"location", m.ToLocationID, locationNotFound("to_location_id")},
		} {
			if err := requireRow(ctx, tx, ref.table, m.OrganizationID, ref.id, ref.missing); err != nil {
				return err
			}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO stock_movement (
				organization_id, item_kind, asset_id, quantity_item_id,
				movement_type, from_location_id, to_location_id, quantity,
				actor_id, reason, ticket_id, order_id, workflow_run_id,
				document_id, notes
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			RETURNING id::text, created_at`,
			m.OrganizationID, m.ItemKind, nilIfEmpty(m.AssetID), nilIfEmpty(m.QuantityItemID),
			m.MovementType, nilIfEmpty(m.FromLocationID), nilIfEmpty(m.ToLocationID),
			m.Quantity, nilIfEmpty(m.ActorID), nilIfEmpty(m.Reason), nilIfEmpty(m.TicketID),
			nilIfEmpty(m.OrderID), nilIfEmpty(m.WorkflowRunID), nilIfEmpty(m.DocumentID),
			nilIfEmpty(m.Notes),
		).Scan(&m.ID, &m.CreatedAt)
		if err != nil {
			return fmt.Errorf("record movement: %w", err)
		}
		// Adjust the quantity item stock level atomically.
		if m.ItemKind == "quantity_item" {
			delta := *m.Quantity
			switch m.MovementType {
			case "receipt", "return", "correction":
				// positive delta
			default:
				delta = -delta
			}
			if _, err := tx.Exec(ctx,
				"UPDATE quantity_item SET stock_level = stock_level + $2 WHERE id = $1 AND organization_id = $3",
				m.QuantityItemID, delta, m.OrganizationID); err != nil {
				return fmt.Errorf("adjust stock level: %w", err)
			}
		}
		// Track the current location of a moved asset.
		if m.ItemKind == "asset" && m.ToLocationID != "" {
			if _, err := tx.Exec(ctx,
				"UPDATE asset SET location_id = $2 WHERE id = $1 AND organization_id = $3",
				m.AssetID, m.ToLocationID, m.OrganizationID); err != nil {
				return fmt.Errorf("update asset location: %w", err)
			}
		}
		return nil
	})
}

// ListMovements returns the asset and quantity item movements of the ledger
// filtered by the given parameters (consumable movements follow with
// WP-274); a legacy type name filters by its canonical type. The policy of
// stock_movement applies the caller's client and site scope.
func (r *PGRepository) ListMovements(ctx context.Context, orgID string, filter MovementFilter, page api.PaginationParams) ([]Movement, int, error) {
	var out []Movement
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1", "item_kind IN ('asset', 'quantity_item')"}
		args := []any{orgID}
		pos := 2
		if filter.AssetID != "" {
			where = append(where, fmt.Sprintf("asset_id = $%d", pos))
			args = append(args, filter.AssetID)
			pos++
		}
		if filter.QuantityItemID != "" {
			where = append(where, fmt.Sprintf("quantity_item_id = $%d", pos))
			args = append(args, filter.QuantityItemID)
			pos++
		}
		if filter.MovementType != "" {
			where = append(where, fmt.Sprintf("movement_type = $%d", pos))
			movementType, _ := canonicalMovementType(filter.MovementType)
			args = append(args, movementType)
			pos++
		}
		if filter.LocationID != "" {
			where = append(where, fmt.Sprintf("(from_location_id = $%d OR to_location_id = $%d)", pos, pos))
			args = append(args, filter.LocationID)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_movement WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count movements: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM stock_movement WHERE %s ORDER BY occurred_at DESC, created_at DESC LIMIT $%d OFFSET $%d",
			movementSelectColumns, clause, pos, pos+1), args...)
		if err != nil {
			return fmt.Errorf("list movements: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMovement(rows)
			if err != nil {
				return err
			}
			out = append(out, *m)
		}
		return rows.Err()
	})
	return out, total, err
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

// DeleteItem removes a quantity item.
func (r *PGRepository) DeleteItem(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM quantity_item WHERE id = $1 AND organization_id = $2", id, orgID)
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
	var createdAt time.Time
	if err := scanner.Scan(
		&m.ID, &m.OrganizationID, &m.ItemKind, &m.AssetID, &m.QuantityItemID,
		&m.MovementType, &m.FromLocationID, &m.ToLocationID, &m.Quantity,
		&m.ActorID, &m.Reason, &m.TicketID, &m.OrderID, &m.WorkflowRunID,
		&m.DocumentID, &m.Notes, &createdAt,
	); err != nil {
		return nil, err
	}
	m.CreatedAt = createdAt.UTC()
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

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
