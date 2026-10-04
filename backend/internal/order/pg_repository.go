package order

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const orderCols = `id::text, organization_id::text, COALESCE(client_id::text,''), order_number, title, status, COALESCE(requested_by::text,''), COALESCE(approved_by::text,''), COALESCE(supplier,''), COALESCE(total_cost,0), COALESCE(currency,''), COALESCE(notes,''), created_at, updated_at`

const itemCols = `id::text, organization_id::text, order_id::text, description, quantity, COALESCE(unit_price,0), COALESCE(consumable_id::text,''), COALESCE(asset_id::text,''), created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed order repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanOrder(s pgx.Row) (*Order, error) {
	o := &Order{}
	if err := s.Scan(&o.ID, &o.OrganizationID, &o.ClientID, &o.OrderNumber, &o.Title, &o.Status, &o.RequestedBy, &o.ApprovedBy, &o.Supplier, &o.TotalCost, &o.Currency, &o.Notes, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return o, nil
}

func scanItem(s pgx.Row) (*Item, error) {
	it := &Item{}
	if err := s.Scan(&it.ID, &it.OrganizationID, &it.OrderID, &it.Description, &it.Quantity, &it.UnitPrice, &it.ConsumableID, &it.AssetID, &it.CreatedAt); err != nil {
		return nil, err
	}
	return it, nil
}

// itemVisible restricts order items to those whose order, consumable and
// asset are visible under the transaction's tenant scope: their policies
// filter the subqueries, while internal_order_item carries no client column
// yet (WP-025).
const itemVisible = `EXISTS (SELECT 1 FROM internal_order WHERE internal_order.id = internal_order_item.order_id)
	AND (internal_order_item.consumable_id IS NULL OR EXISTS (SELECT 1 FROM consumable WHERE consumable.id = internal_order_item.consumable_id))
	AND (internal_order_item.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = internal_order_item.asset_id))`

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

func (r *PGRepository) listItemsTx(ctx context.Context, tx pgx.Tx, orgID, orderID string) ([]Item, error) {
	rows, err := tx.Query(ctx, "SELECT "+itemCols+" FROM internal_order_item WHERE "+itemVisible+" AND organization_id = $1 AND order_id = $2 ORDER BY created_at ASC", orgID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Order, int, error) {
	out := []Order{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.Status != "" {
			where = append(where, fmt.Sprintf("status = $%d", pos))
			args = append(args, filter.Status)
			pos++
		}
		if filter.ClientID != "" {
			where = append(where, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, filter.ClientID)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("title ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM internal_order WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+orderCols+" FROM internal_order WHERE "+clause+fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			out = append(out, *o)
			ids = append(ids, o.ID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, id := range ids {
			items, err := r.listItemsTx(ctx, tx, orgID, id)
			if err != nil {
				return err
			}
			out[i].Items = items
		}
		return nil
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		o, err = scanOrder(tx.QueryRow(ctx, "SELECT "+orderCols+" FROM internal_order WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("order not found")
		}
		if err != nil {
			return err
		}
		o.Items, err = r.listItemsTx(ctx, tx, orgID, id)
		return err
	})
	return o, err
}

func (r *PGRepository) Create(ctx context.Context, o *Order) error {
	return database.WithRequestTenant(ctx, r.pool, o.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if o.Status == "" {
			o.Status = "draft"
		}
		if o.OrderNumber == "" {
			if err := tx.QueryRow(ctx, "SELECT 'ORD-' || LPAD((COUNT(*)+1)::text, 6, '0') FROM internal_order WHERE organization_id = $1", o.OrganizationID).Scan(&o.OrderNumber); err != nil {
				return fmt.Errorf("assign order number: %w", err)
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO internal_order (organization_id, client_id, order_number, title, status, requested_by, supplier, total_cost, currency, notes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, NULLIF($6,'')::uuid, $7, $8, $9, $10)
			RETURNING id::text, created_at, updated_at
		`, o.OrganizationID, o.ClientID, o.OrderNumber, o.Title, o.Status, o.RequestedBy, o.Supplier, o.TotalCost, o.Currency, o.Notes).
			Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		set := func(col string, val any) {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		if req.ClientID != nil {
			set("client_id", nilIfEmpty(*req.ClientID))
		}
		if req.Title != nil {
			set("title", *req.Title)
		}
		if req.Supplier != nil {
			set("supplier", *req.Supplier)
		}
		if req.TotalCost != nil {
			set("total_cost", *req.TotalCost)
		}
		if req.Currency != nil {
			set("currency", *req.Currency)
		}
		if req.Notes != nil {
			set("notes", *req.Notes)
		}
		var err error
		if len(sets) == 0 {
			o, err = scanOrder(tx.QueryRow(ctx, "SELECT "+orderCols+" FROM internal_order WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			o, err = scanOrder(tx.QueryRow(ctx, "UPDATE internal_order SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 RETURNING "+orderCols, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("order not found")
		}
		if err != nil {
			return err
		}
		o.Items, err = r.listItemsTx(ctx, tx, orgID, id)
		return err
	})
	return o, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM internal_order WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("order not found")
		}
		return nil
	})
}

func (r *PGRepository) SetStatus(ctx context.Context, orgID, id, status, actorID string) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		approvedBy := any(nil)
		if status == "approved" || status == "rejected" {
			approvedBy = nilIfEmpty(actorID)
		}
		o, err = scanOrder(tx.QueryRow(ctx, "UPDATE internal_order SET status = $3, approved_by = COALESCE($4::uuid, approved_by), updated_at = now() WHERE organization_id = $1 AND id = $2 RETURNING "+orderCols, orgID, id, status, approvedBy))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("order not found")
		}
		if err != nil {
			return err
		}
		o.Items, err = r.listItemsTx(ctx, tx, orgID, id)
		return err
	})
	return o, err
}

func (r *PGRepository) AddItem(ctx context.Context, item *Item) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO internal_order_item (organization_id, order_id, description, quantity, unit_price, consumable_id, asset_id)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6,'')::uuid, NULLIF($7,'')::uuid)
			RETURNING id::text, created_at
		`, item.OrganizationID, item.OrderID, item.Description, item.Quantity, item.UnitPrice, item.ConsumableID, item.AssetID).
			Scan(&item.ID, &item.CreatedAt); err != nil {
			return err
		}
		if err := requireVisible(ctx, tx, "internal_order_item", itemVisible, item.ID); err != nil {
			return err
		}
		var err error
		o, err = scanOrder(tx.QueryRow(ctx, "SELECT "+orderCols+" FROM internal_order WHERE organization_id = $1 AND id = $2", item.OrganizationID, item.OrderID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("order not found")
		}
		if err != nil {
			return err
		}
		o.Items, err = r.listItemsTx(ctx, tx, item.OrganizationID, item.OrderID)
		return err
	})
	return o, err
}

func (r *PGRepository) ListItems(ctx context.Context, orgID, orderID string) ([]Item, error) {
	var out []Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = r.listItemsTx(ctx, tx, orgID, orderID)
		return err
	})
	return out, err
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
