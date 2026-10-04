package movement

import (
	"context"
	"fmt"
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

// movementVisible restricts movements to those whose asset or quantity item
// is visible under the transaction's tenant scope: their policies filter the
// subqueries, while asset_movement carries no client column yet (WP-025).
const movementVisible = `(asset_movement.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = asset_movement.asset_id))
	AND (asset_movement.quantity_item_id IS NULL OR EXISTS (SELECT 1 FROM quantity_item WHERE quantity_item.id = asset_movement.quantity_item_id))`

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return fmt.Errorf("not found")
	}
	return nil
}

// Record appends a movement. The append-only trigger on asset_movement
// rejects any later mutation; quantity items get their stock level adjusted
// inside the same transaction.
func (r *PGRepository) Record(ctx context.Context, m *Movement) error {
	if !MovementTypes[m.MovementType] {
		return fmt.Errorf("invalid movement_type %q", m.MovementType)
	}
	if m.ItemKind == "" {
		m.ItemKind = "asset"
	}
	if m.AssetID == "" && m.QuantityItemID == "" {
		return fmt.Errorf("asset_id or quantity_item_id is required")
	}
	return database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO asset_movement (
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
		if m.QuantityItemID != "" && m.Quantity != nil {
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
		if m.AssetID != "" && m.ToLocationID != "" {
			if _, err := tx.Exec(ctx,
				"UPDATE asset SET location_id = $2 WHERE id = $1 AND organization_id = $3",
				m.AssetID, m.ToLocationID, m.OrganizationID); err != nil {
				return fmt.Errorf("update asset location: %w", err)
			}
		}
		return requireVisible(ctx, tx, "asset_movement", movementVisible, m.ID)
	})
}

// ListMovements returns the movement ledger filtered by the given parameters.
func (r *PGRepository) ListMovements(ctx context.Context, orgID string, filter MovementFilter, page api.PaginationParams) ([]Movement, int, error) {
	var out []Movement
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
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
			args = append(args, filter.MovementType)
			pos++
		}
		if filter.LocationID != "" {
			where = append(where, fmt.Sprintf("(from_location_id = $%d OR to_location_id = $%d)", pos, pos))
			args = append(args, filter.LocationID)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asset_movement WHERE "+movementVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count movements: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM asset_movement WHERE "+movementVisible+" AND %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
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
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get quantity item: %w", err)
		}
		out = item
		return nil
	})
	return out, err
}

// CreateItem inserts a quantity item.
func (r *PGRepository) CreateItem(ctx context.Context, item *QuantityItem) error {
	return database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Attributes == nil {
			item.Attributes = map[string]any{}
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
			pos++
		}
		if len(sets) == 0 {
			item, err := scanItem(tx.QueryRow(ctx, fmt.Sprintf(
				"SELECT %s FROM quantity_item WHERE id = $1 AND organization_id = $2",
				itemSelectColumns), id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
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
				return fmt.Errorf("not found")
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
			return fmt.Errorf("not found")
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
