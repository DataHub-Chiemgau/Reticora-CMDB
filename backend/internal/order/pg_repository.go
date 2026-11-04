package order

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// orderCols reads an order with the name of its supplier; the subquery
// also works in RETURNING.
const orderCols = `id::text, organization_id::text, COALESCE(client_id::text,''), order_number, title, status, COALESCE(requested_by::text,''), COALESCE(approved_by::text,''), COALESCE(supplier_id::text,''), COALESCE((SELECT s.name FROM supplier s WHERE s.id = internal_order.supplier_id), ''), COALESCE(target_location_id::text,''), COALESCE(total_cost,0), COALESCE(currency,''), COALESCE(notes,''), created_at, updated_at`

const itemCols = `id::text, organization_id::text, order_id::text, description, quantity, received_quantity, COALESCE(unit_price,0), COALESCE(consumable_id::text,''), COALESCE(asset_id::text,''), created_at`

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
	if err := s.Scan(&o.ID, &o.OrganizationID, &o.ClientID, &o.OrderNumber, &o.Title, &o.Status, &o.RequestedBy, &o.ApprovedBy,
		&o.SupplierID, &o.SupplierName, &o.TargetLocationID, &o.TotalCost, &o.Currency, &o.Notes, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	// The deprecated alias carries the name until WP-320.
	o.Supplier = o.SupplierName
	return o, nil
}

func scanItem(s pgx.Row) (*Item, error) {
	it := &Item{}
	if err := s.Scan(&it.ID, &it.OrganizationID, &it.OrderID, &it.Description, &it.Quantity, &it.ReceivedQuantity, &it.UnitPrice,
		&it.ConsumableID, &it.AssetID, &it.CreatedAt); err != nil {
		return nil, err
	}
	return it, nil
}

// itemVisible restricts order items to those whose order, consumable and
// asset are visible under the transaction's tenant scope, on top of the
// client predicate of internal_order_item_isolation (000240).
const itemVisible = `EXISTS (SELECT 1 FROM internal_order WHERE internal_order.id = internal_order_item.order_id)
	AND (internal_order_item.consumable_id IS NULL OR EXISTS (SELECT 1 FROM consumable WHERE consumable.id = internal_order_item.consumable_id))
	AND (internal_order_item.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = internal_order_item.asset_id))`

// lockOrder reads the order under a row lock. FOR UPDATE applies
// internal_order_scoped_update, so an order the principal may not change
// is not found.
func lockOrder(ctx context.Context, tx pgx.Tx, orgID, id string) (*Order, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	o, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderCols+" FROM internal_order WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock order: %w", err)
	}
	return o, nil
}

// clientFits reports whether an object of objectClient may serve an order
// of orderClient: org-wide objects always, client objects only for an
// order of the same client.
func clientFits(objectClient *string, orderClient string) bool {
	return objectClient == nil || (orderClient != "" && strings.EqualFold(*objectClient, orderClient))
}

// supplierByName resolves the deprecated alias: the supplier of the
// organization with exactly this name, visible under the scope.
func supplierByName(ctx context.Context, tx pgx.Tx, orgID, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM supplier WHERE organization_id = $1 AND name = $2`, orgID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fieldError("supplier", "no supplier of the organization has this name")
	}
	if err != nil {
		return "", fmt.Errorf("resolve supplier: %w", err)
	}
	return id, nil
}

// checkSupplier verifies that supplierID names a supplier visible under the
// scope that is org-wide or of the order's client (422 on field).
func checkSupplier(ctx context.Context, tx pgx.Tx, orgID, clientID, supplierID, field string) error {
	if !api.IsUUID(supplierID) {
		return fieldError(field, "the referenced supplier does not exist")
	}
	var client *string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM supplier WHERE organization_id = $1 AND id = $2`, orgID, supplierID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError(field, "the referenced supplier does not exist")
	}
	if err != nil {
		return fmt.Errorf("read supplier: %w", err)
	}
	if !clientFits(client, clientID) {
		return fieldError(field, "the supplier belongs to another client than the order")
	}
	return nil
}

// checkTargetBin verifies the target bin of an order (WP-847): binID names
// a location of kind bin visible under the scope and, for an order with a
// client, of that client (422 on target_location_id). Without a client of
// the order every bin in the scope fits, since every location has a client.
// An unknown id, a location outside the scope and one of another client
// get the same answer (msgTargetBin); only a usable location that is no bin
// is reported as such.
func checkTargetBin(ctx context.Context, tx pgx.Tx, orgID, orderClient, binID string) error {
	if !api.IsUUID(binID) {
		return fieldError("target_location_id", msgTargetBin)
	}
	var kind, client string
	err := tx.QueryRow(ctx, `SELECT kind::text, client_id::text FROM location WHERE organization_id = $1 AND id = $2`, orgID, binID).Scan(&kind, &client)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError("target_location_id", msgTargetBin)
	}
	if err != nil {
		return fmt.Errorf("read target location: %w", err)
	}
	if orderClient != "" && !strings.EqualFold(client, orderClient) {
		return fieldError("target_location_id", msgTargetBin)
	}
	if kind != "bin" {
		return fieldError("target_location_id", "the target location must be a location of kind bin")
	}
	return nil
}

// resolveSupplier combines supplier_id and the deprecated name alias: an
// empty alias is not set, a name resolves to an existing supplier only,
// and a name that contradicts supplier_id is refused. It returns the
// supplier id and the field to report on.
func resolveSupplier(ctx context.Context, tx pgx.Tx, orgID, supplierID, alias string) (id, field string, err error) {
	id, field = strings.TrimSpace(supplierID), "supplier_id"
	if alias == "" {
		return id, field, nil
	}
	named, err := supplierByName(ctx, tx, orgID, alias)
	if err != nil {
		return "", "", err
	}
	if id != "" && !strings.EqualFold(id, named) {
		return "", "", fieldError("supplier", "supplier and supplier_id name different suppliers")
	}
	return named, "supplier", nil
}

// checkItemsClient refuses a client of the order that does not fit the
// consumables of its positions (422 on client_id).
func checkItemsClient(ctx context.Context, tx pgx.Tx, orgID, orderID, clientID string) error {
	var misfit bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM internal_order_item i JOIN consumable c ON c.id = i.consumable_id AND c.organization_id = i.organization_id
		                WHERE i.organization_id = $1 AND i.order_id = $2 AND c.client_id IS NOT NULL
		                  AND c.client_id IS DISTINCT FROM NULLIF($3, '')::uuid)`, orgID, orderID, clientID).Scan(&misfit); err != nil {
		return fmt.Errorf("check consumables of the positions: %w", err)
	}
	if misfit {
		return fieldError("client_id", "a consumable of the positions belongs to another client")
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
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		o, err = scanOrder(tx.QueryRow(ctx, "SELECT "+orderCols+" FROM internal_order WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
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
			o.Status = StatusDraft
		}
		if strings.TrimSpace(o.Currency) == "" {
			if err := tx.QueryRow(ctx, `SELECT currency FROM organization WHERE id = $1`, o.OrganizationID).Scan(&o.Currency); err != nil {
				return fmt.Errorf("read organization currency: %w", err)
			}
		}
		supplierID, field, err := resolveSupplier(ctx, tx, o.OrganizationID, o.SupplierID, strings.TrimSpace(o.Supplier))
		if err != nil {
			return err
		}
		if supplierID != "" {
			if supErr := checkSupplier(ctx, tx, o.OrganizationID, o.ClientID, supplierID, field); supErr != nil {
				return supErr
			}
		}
		o.TargetLocationID = strings.TrimSpace(o.TargetLocationID)
		if o.TargetLocationID != "" {
			if binErr := checkTargetBin(ctx, tx, o.OrganizationID, o.ClientID, o.TargetLocationID); binErr != nil {
				return binErr
			}
		}
		if o.OrderNumber == "" {
			if err = tx.QueryRow(ctx, "SELECT 'ORD-' || LPAD((COUNT(*)+1)::text, 6, '0') FROM internal_order WHERE organization_id = $1", o.OrganizationID).Scan(&o.OrderNumber); err != nil {
				return fmt.Errorf("assign order number: %w", err)
			}
		}
		created, err := scanOrder(tx.QueryRow(ctx, `
			INSERT INTO internal_order (organization_id, client_id, order_number, title, status, requested_by, supplier_id, target_location_id, total_cost, currency, notes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, NULLIF($6,'')::uuid, NULLIF($7,'')::uuid, NULLIF($8,'')::uuid, $9, $10, $11)
			RETURNING `+orderCols,
			o.OrganizationID, o.ClientID, o.OrderNumber, o.Title, o.Status, o.RequestedBy, supplierID, o.TargetLocationID, o.TotalCost, o.Currency, o.Notes))
		if err != nil {
			return err
		}
		*o = *created
		return nil
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateOrderRequest) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockOrder(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if current.Status != StatusDraft {
			return ErrNotDraft
		}
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		set := func(col string, val any) {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		client := current.ClientID
		if req.ClientID != nil {
			client = strings.TrimSpace(*req.ClientID)
			set("client_id", nilIfEmpty(client))
		}
		clientChanged := !strings.EqualFold(client, current.ClientID)
		alias := supplierName(req.Supplier)
		supplierID, field := current.SupplierID, "supplier_id"
		if req.SupplierID != nil || alias != "" {
			explicit := ""
			if req.SupplierID != nil {
				explicit = *req.SupplierID
			}
			if supplierID, field, err = resolveSupplier(ctx, tx, orgID, explicit, alias); err != nil {
				return err
			}
			set("supplier_id", nilIfEmpty(supplierID))
		}
		if supplierID != "" && (clientChanged || req.SupplierID != nil || alias != "") {
			if supErr := checkSupplier(ctx, tx, orgID, client, supplierID, field); supErr != nil {
				return supErr
			}
		}
		target := current.TargetLocationID
		if req.TargetLocationID != nil {
			target = strings.TrimSpace(*req.TargetLocationID)
			set("target_location_id", nilIfEmpty(target))
		}
		if target != "" && (clientChanged || req.TargetLocationID != nil) {
			if binErr := checkTargetBin(ctx, tx, orgID, client, target); binErr != nil {
				return binErr
			}
		}
		if clientChanged {
			if itemsErr := checkItemsClient(ctx, tx, orgID, id, client); itemsErr != nil {
				return itemsErr
			}
		}
		if req.Title != nil {
			set("title", *req.Title)
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
		if len(sets) == 0 {
			o = current
		} else {
			sets = append(sets, "updated_at = now()")
			o, err = scanOrder(tx.QueryRow(ctx, "UPDATE internal_order SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 AND status = 'draft' RETURNING "+orderCols, args...))
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotDraft
			}
			if err != nil {
				return err
			}
		}
		o.Items, err = r.listItemsTx(ctx, tx, orgID, id)
		return err
	})
	return o, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockOrder(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if current.Status != StatusDraft {
			return ErrNotDraft
		}
		tag, err := tx.Exec(ctx, "DELETE FROM internal_order WHERE organization_id = $1 AND id = $2 AND status = 'draft'", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotDraft
		}
		return nil
	})
}

// SetStatus locks the order, checks the state machine and the four-eyes
// rule and writes the new state only from the state it read
// (UPDATE … AND status = <from>): of parallel transitions one succeeds,
// the others find the new state and fail with ErrInvalidTransition.
func (r *PGRepository) SetStatus(ctx context.Context, orgID, id, status, actorID string) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockOrder(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if !CanTransition(current.Status, status) {
			return ErrInvalidTransition
		}
		if status == StatusApproved {
			if eyesErr := checkFourEyes(current.RequestedBy, actorID); eyesErr != nil {
				return eyesErr
			}
		}
		approvedBy := any(nil)
		if status == StatusApproved || status == StatusRejected {
			approvedBy = nilIfEmpty(actorID)
		}
		o, err = scanOrder(tx.QueryRow(ctx, `UPDATE internal_order SET status = $3, approved_by = COALESCE($4::uuid, approved_by), updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND status = $5 RETURNING `+orderCols, orgID, id, status, approvedBy, current.Status))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidTransition
		}
		if err != nil {
			return err
		}
		o.Items, err = r.listItemsTx(ctx, tx, orgID, id)
		return err
	})
	return o, err
}

// AddItem adds a position to an order in draft. The consumable must be
// org-wide or of the order's client; consumable and asset must be visible
// under the scope (422 otherwise). It books no movement (WP-760).
func (r *PGRepository) AddItem(ctx context.Context, item *Item) (*Order, error) {
	if err := item.validate(); err != nil {
		return nil, err
	}
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockOrder(ctx, tx, item.OrganizationID, item.OrderID)
		if err != nil {
			return err
		}
		if current.Status != StatusDraft {
			return ErrNotDraft
		}
		if item.ConsumableID != "" {
			if refErr := checkReference(ctx, tx, "consumable", "consumable_id", item.OrganizationID, item.ConsumableID, &current.ClientID); refErr != nil {
				return refErr
			}
		}
		if item.AssetID != "" {
			if refErr := checkReference(ctx, tx, "asset", "asset_id", item.OrganizationID, item.AssetID, nil); refErr != nil {
				return refErr
			}
		}
		if insErr := tx.QueryRow(ctx, `
			INSERT INTO internal_order_item (organization_id, order_id, description, quantity, unit_price, consumable_id, asset_id)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6,'')::uuid, NULLIF($7,'')::uuid)
			RETURNING id::text, created_at
		`, item.OrganizationID, item.OrderID, item.Description, item.Quantity, item.UnitPrice, item.ConsumableID, item.AssetID).
			Scan(&item.ID, &item.CreatedAt); insErr != nil {
			return insErr
		}
		o = current
		o.Items, err = r.listItemsTx(ctx, tx, item.OrganizationID, item.OrderID)
		return err
	})
	return o, err
}

// checkReference verifies that the row id of table (consumable or asset)
// is visible under the scope; with orderClient it must also be org-wide or
// of that client of the order ("" for an org-wide order).
func checkReference(ctx context.Context, tx pgx.Tx, table, field, orgID, id string, orderClient *string) error {
	if !api.IsUUID(id) {
		return fieldError(field, "the referenced record does not exist")
	}
	var client *string
	err := tx.QueryRow(ctx, "SELECT client_id::text FROM "+table+" WHERE organization_id = $1 AND id = $2", orgID, id).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError(field, "the referenced record does not exist")
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	if orderClient != nil && !clientFits(client, *orderClient) {
		return fieldError(field, "the referenced record belongs to another client than the order")
	}
	return nil
}

func (r *PGRepository) ListItems(ctx context.Context, orgID, orderID string) ([]Item, error) {
	if !api.IsUUID(orderID) {
		return nil, ErrNotFound
	}
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
