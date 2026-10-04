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

const consumableCols = `id::text, organization_id::text, COALESCE(client_id::text,''), name, COALESCE(sku,''), category, unit, stock_level, min_level, COALESCE(location,''), COALESCE(notes,''), created_at, updated_at`

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
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.SKU, &c.Category, &c.Unit, &c.StockLevel, &c.MinLevel, &c.Location, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

// stockMovementVisible restricts stock movements to visible consumables: the
// consumable policy filters the subquery, while stock_movement carries no
// client column yet (WP-025).
const stockMovementVisible = "EXISTS (SELECT 1 FROM consumable WHERE consumable.id = stock_movement.consumable_id)"

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
			where = append(where, "stock_level <= min_level")
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
			return fmt.Errorf("consumable not found")
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
			INSERT INTO consumable (organization_id, client_id, name, sku, category, unit, stock_level, min_level, location, notes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, NULLIF($4,''), $5, $6, $7, $8, $9, $10)
			RETURNING id::text, created_at, updated_at
		`, c.OrganizationID, c.ClientID, c.Name, c.SKU, c.Category, c.Unit, c.StockLevel, c.MinLevel, c.Location, c.Notes).
			Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateConsumableRequest) (*Consumable, error) {
	var c *Consumable
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
		if req.Name != nil {
			set("name", *req.Name)
		}
		if req.SKU != nil {
			set("sku", *req.SKU)
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
		if req.Location != nil {
			set("location", *req.Location)
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
			return fmt.Errorf("consumable not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM consumable WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("consumable not found")
		}
		return nil
	})
}

// AddMovement records the movement and adjusts the stock level in one
// transaction, rejecting out-movements that would drive stock negative.
func (r *PGRepository) AddMovement(ctx context.Context, m *Movement) (*Consumable, error) {
	var c *Consumable
	err := database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanConsumable(tx.QueryRow(ctx, "SELECT "+consumableCols+" FROM consumable WHERE organization_id = $1 AND id = $2 FOR UPDATE", m.OrganizationID, m.ConsumableID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("consumable not found")
		}
		if err != nil {
			return err
		}
		delta := m.Quantity
		if m.Direction == "out" {
			delta = -m.Quantity
		}
		if c.StockLevel+delta < 0 {
			return fmt.Errorf("insufficient stock")
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO stock_movement (organization_id, consumable_id, direction, quantity, reason, reference, actor_id)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,'')::uuid)
			RETURNING id::text, created_at
		`, m.OrganizationID, m.ConsumableID, m.Direction, m.Quantity, m.Reason, m.Reference, m.ActorID).
			Scan(&m.ID, &m.CreatedAt); err != nil {
			return err
		}
		c, err = scanConsumable(tx.QueryRow(ctx, "UPDATE consumable SET stock_level = stock_level + $3, updated_at = now() WHERE organization_id = $1 AND id = $2 RETURNING "+consumableCols, m.OrganizationID, m.ConsumableID, delta))
		return err
	})
	return c, err
}

func (r *PGRepository) ListMovements(ctx context.Context, orgID, consumableID string, page api.PaginationParams) ([]Movement, int, error) {
	out := []Movement{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if consumableID != "" {
			where += " AND consumable_id = $2"
			args = append(args, consumableID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_movement WHERE "+stockMovementVisible+" AND "+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, consumable_id::text, direction, quantity, COALESCE(reason,''), COALESCE(reference,''), COALESCE(actor_id::text,''), created_at FROM stock_movement WHERE `+stockMovementVisible+` AND `+where+fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m := Movement{}
			if err := rows.Scan(&m.ID, &m.OrganizationID, &m.ConsumableID, &m.Direction, &m.Quantity, &m.Reason, &m.Reference, &m.ActorID, &m.CreatedAt); err != nil {
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
