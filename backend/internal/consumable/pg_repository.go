package consumable

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// consumableCols read a consumable; the deprecated field stock_level is
// on_hand_total, the sum of on_hand over all bins whatever the caller's
// scope (E-128 (a), WP-285).
const consumableCols = `id::text, organization_id::text, COALESCE(client_id::text,''), name, COALESCE(sku,''), COALESCE(barcode,''), COALESCE(supplier_id::text,''), category, unit, on_hand_total::float8, min_level, COALESCE(notes,''), created_at, updated_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed consumable repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanConsumable(s pgx.Row) (*Consumable, error) {
	c := &Consumable{}
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.SKU, &c.Barcode, &c.SupplierID, &c.Category, &c.Unit, &c.StockLevel, &c.MinLevel, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Consumable, int, error) {
	out := []Consumable{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.Category != "" {
			where = append(where, fmt.Sprintf("category = $%d", pos))
			args = append(args, filter.Category)
			pos++
		}
		if filter.ClientID != "" {
			where = append(where, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, filter.ClientID)
			pos++
		}
		if filter.LowStock {
			// The threshold rule of the minimum level (WP-285).
			where = append(where, "on_hand_total < min_level")
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("name ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM consumable WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+consumableCols+" FROM consumable WHERE "+clause+fmt.Sprintf(" ORDER BY name ASC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConsumable(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Consumable, error) {
	var c *Consumable
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanConsumable(tx.QueryRow(ctx, "SELECT "+consumableCols+" FROM consumable WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Create(ctx context.Context, c *Consumable) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if c.Category == "" {
			c.Category = "general"
		}
		if c.Unit == "" {
			c.Unit = "pcs"
		}
		return tx.QueryRow(ctx, `
			INSERT INTO consumable (organization_id, client_id, name, sku, barcode, supplier_id, category, unit, min_level, notes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,'')::uuid, $7, $8, $9, $10)
			RETURNING id::text, created_at, updated_at
		`, c.OrganizationID, c.ClientID, c.Name, c.SKU, c.Barcode, c.SupplierID, c.Category, c.Unit, c.MinLevel, c.Notes).
			Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

// Update changes the fields of req. A new min_level is compared with
// on_hand_total under the row lock of the consumable (lockConsumable, as
// in ApplyBooking): when it takes the consumable from "not below" to
// "below" its minimum level, stock.below_minimum is the last write of the
// transaction (WP-285). Lowering min_level and other fields write none.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateConsumableRequest) (*Consumable, error) {
	var c *Consumable
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var before *lockedConsumable
		if req.MinLevel != nil {
			var lockErr error
			if before, lockErr = lockConsumable(ctx, tx, orgID, id); lockErr != nil {
				return lockErr
			}
		}
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
		if req.Name != nil {
			set("name", *req.Name)
		}
		if req.SKU != nil {
			set("sku", *req.SKU)
		}
		if req.Barcode != nil {
			set("barcode", nilIfEmpty(*req.Barcode))
		}
		if req.SupplierID != nil {
			set("supplier_id", nilIfEmpty(*req.SupplierID))
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
			set("notes", *req.Notes)
		}
		var err error
		if len(sets) == 0 {
			c, err = scanConsumable(tx.QueryRow(ctx, "SELECT "+consumableCols+" FROM consumable WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			c, err = scanConsumable(tx.QueryRow(ctx, "UPDATE consumable SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 RETURNING "+consumableCols, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil || before == nil || before.below || !c.LowStock() {
			return err
		}
		return appendBelowMinimum(ctx, tx, orgID, &lockedConsumable{id: c.ID, client: c.ClientID, sku: c.SKU,
			name: c.Name, unit: c.Unit, total: c.StockLevel, minLevel: c.MinLevel})
	})
	return c, err
}

// Delete removes the consumable. Stock in a bin keeps it (ErrHasStock,
// 409); rows with nothing on hand or reserved go in the same transaction.
// Rows of bins the caller cannot see, and the ledger, keep it through their
// RESTRICT foreign keys.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if !api.IsUUID(id) {
			return ErrNotFound
		}
		var stocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_level WHERE consumable_id = $1 AND organization_id = $2
			AND (on_hand > 0 OR reserved > 0))`, id, orgID).Scan(&stocked); err != nil {
			return fmt.Errorf("check stock: %w", err)
		}
		if stocked {
			return ErrHasStock
		}
		if _, err := tx.Exec(ctx, `DELETE FROM stock_level WHERE consumable_id = $1 AND organization_id = $2
			AND on_hand = 0 AND reserved = 0`, id, orgID); err != nil {
			return fmt.Errorf("remove empty stock: %w", err)
		}
		tag, err := tx.Exec(ctx, "DELETE FROM consumable WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddMovement books the movement on the stock of a bin and appends it to
// the ledger in one transaction (transitional path until WP-756): in is a
// receipt into the bin m.LocationID, out a retrieval from it, both through
// ApplyBooking. Without a bin the consumable must have exactly one
// stock_level row (visible to the caller), else a 422 on location_id. The
// ledger row (item_kind consumable) names the bin; its trigger derives the
// client of the consumable and the site of the bin.
func (r *PGRepository) AddMovement(ctx context.Context, m *Movement) (*Consumable, error) {
	var c *Consumable
	err := database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if m.LocationID == "" {
			bin, binErr := onlyBin(ctx, tx, m.OrganizationID, m.ConsumableID)
			if binErr != nil {
				return binErr
			}
			m.LocationID = bin
		}
		b := bookingOf(m)
		if err := ApplyBooking(ctx, tx, &b); err != nil {
			return err
		}
		if insertErr := tx.QueryRow(ctx, `
			INSERT INTO stock_movement (organization_id, item_kind, consumable_id, movement_type, from_location_id, to_location_id,
				quantity, reason, notes, actor_id)
			VALUES ($1, 'consumable', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, NULLIF($8, ''), NULLIF($9,'')::uuid)
			RETURNING id::text, created_at
		`, m.OrganizationID, m.ConsumableID, b.MovementType, b.FromLocationID, b.ToLocationID, m.Quantity, m.Reason, m.Reference, m.ActorID).
			Scan(&m.ID, &m.CreatedAt); insertErr != nil {
			return insertErr
		}
		var err error
		c, err = scanConsumable(tx.QueryRow(ctx, "SELECT "+consumableCols+" FROM consumable WHERE organization_id = $1 AND id = $2", m.OrganizationID, m.ConsumableID))
		return err
	})
	return c, err
}

// onlyBin returns the bin of the only stock_level row of the consumable the
// caller sees; none or several are a 422 on location_id, an invisible
// consumable ErrNotFound.
func onlyBin(ctx context.Context, tx pgx.Tx, orgID, consumableID string) (string, error) {
	if _, err := lockConsumable(ctx, tx, orgID, consumableID); err != nil {
		return "", err
	}
	bins, err := tx.Query(ctx, `SELECT location_id::text FROM stock_level WHERE consumable_id = $1 AND organization_id = $2 LIMIT 2`,
		consumableID, orgID)
	if err != nil {
		return "", fmt.Errorf("find bin: %w", err)
	}
	ids, err := pgx.CollectRows(bins, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("find bin: %w", err)
	}
	if len(ids) != 1 {
		return "", ErrBinRequired
	}
	return ids[0], nil
}

func (r *PGRepository) ListMovements(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Movement, int, error) {
	out := []Movement{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The consumable movements of the ledger; the policy of
		// stock_movement applies the caller's client and site scope.
		where := "organization_id = $1 AND item_kind = 'consumable'"
		args := []any{orgID}
		if consumableID != "" {
			where += " AND consumable_id = $2"
			args = append(args, consumableID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_movement WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, consumable_id::text,
			CASE movement_type WHEN 'receipt' THEN 'in' ELSE 'out' END,
			COALESCE(CASE movement_type WHEN 'receipt' THEN to_location_id ELSE from_location_id END::text, ''),
			quantity, COALESCE(reason,''), COALESCE(notes,''),
			COALESCE(actor_id::text,''), created_at FROM stock_movement WHERE `+where+
			fmt.Sprintf(" ORDER BY occurred_at DESC, created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m := Movement{}
			if err := rows.Scan(&m.ID, &m.OrganizationID, &m.ConsumableID, &m.Direction, &m.LocationID, &m.Quantity, &m.Reason, &m.Reference, &m.ActorID, &m.CreatedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, total, err
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// ListStock returns the stock per bin of the consumable visible to the
// caller (ListStock in its own transaction).
func (r *PGRepository) ListStock(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Stock, int, error) {
	var out []Stock
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, total, err = ListStock(ctx, tx, orgID, consumableID, page)
		return err
	})
	return out, total, err
}
