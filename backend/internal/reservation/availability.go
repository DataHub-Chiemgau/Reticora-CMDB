package reservation

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAvailability implements AvailabilityProvider with SQL projections over
// assets, the stock of consumables per bin, reservations and lifecycle
// states (spec §10).
type PGAvailability struct {
	pool *pgxpool.Pool
}

// NewPGAvailability creates a PostgreSQL-backed availability provider.
func NewPGAvailability(pool *pgxpool.Pool) *PGAvailability {
	return &PGAvailability{pool: pool}
}

// Availability computes per-item availability buckets.
//
// Assets are counted as units (1 per serialized asset) bucketed by lifecycle:
// deployed/assigned, repair, unavailable (retired/disposed/lost), reserved
// (active reservation), and available (everything else). Consumables
// (item_kind consumable; the filter quantity_item is its alias) project
// their stock rows per bin: Σ on_hand, Σ reserved and Σ (on_hand −
// reserved), WP-284.
func (p *PGAvailability) Availability(ctx context.Context, orgID string, filter AvailabilityFilter) ([]Availability, error) {
	var out []Availability
	// Reservations count against availability whoever made them, so the
	// reservation subqueries are not narrowed to the caller's scope; the
	// projected assets and quantity items are.
	err := database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {

		if filter.ItemKind == "" || filter.ItemKind == "asset" {
			rows, err := tx.Query(ctx, `
				SELECT a.id::text,
					1.0 AS total,
					CASE
						WHEN r.id IS NOT NULL THEN 0
						WHEN a.lifecycle_state IN ('deployed', 'reserved', 'retired', 'disposed')
							OR a.status IN ('assigned', 'retired', 'disposed', 'lost', 'maintenance') THEN 0
						ELSE 1
					END AS available,
					CASE WHEN r.id IS NOT NULL THEN 1 ELSE 0 END AS reserved,
					CASE WHEN a.lifecycle_state = 'deployed' OR a.status = 'assigned' THEN 1 ELSE 0 END AS assigned,
					CASE WHEN a.lifecycle_state = 'repair' OR a.status = 'maintenance' THEN 1 ELSE 0 END AS repair,
					CASE WHEN a.lifecycle_state IN ('retired', 'disposed')
						OR a.status IN ('retired', 'disposed', 'lost') THEN 1 ELSE 0 END AS unavailable
				FROM asset a
				LEFT JOIN reservation r
					ON r.asset_id = a.id AND r.state = 'active'
				WHERE a.organization_id = $1
					AND ($2::uuid IS NULL OR a.id = $2::uuid)`,
				orgID, nullUUID(filter.ItemID))
			if err != nil {
				return fmt.Errorf("project asset availability: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var a Availability
				var id string
				if err := rows.Scan(&id, &a.Total, &a.Available, &a.Reserved, &a.Assigned, &a.Repair, &a.Unavailable); err != nil {
					return err
				}
				a.ItemKind = "asset"
				a.ItemID = id
				out = append(out, a)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}

		if filter.ItemKind == "" || filter.ItemKind == "consumable" || filter.ItemKind == "quantity_item" {
			// The stock per bin (WP-284): total = Σ on_hand, reserved =
			// Σ reserved, available = Σ (on_hand − reserved) over the stock
			// rows the caller sees; reserved never exceeds on_hand
			// (stock_level_reserved_check), so no clamping. A visible
			// consumable without stock row counts 0.
			rows, err := tx.Query(ctx, `
				WITH stock AS (
					SELECT consumable_id, SUM(on_hand) AS total, SUM(on_hand - reserved) AS available,
						SUM(reserved) AS reserved
					FROM stock_level
					WHERE organization_id = $1 AND ($2::uuid IS NULL OR consumable_id = $2::uuid)
					GROUP BY consumable_id
				), items AS (
					SELECT consumable_id AS id FROM stock
					UNION
					SELECT id FROM consumable WHERE organization_id = $1 AND ($2::uuid IS NULL OR id = $2::uuid)
				)
				SELECT items.id::text, COALESCE(s.total, 0)::float8, COALESCE(s.available, 0)::float8,
					COALESCE(s.reserved, 0)::float8
				FROM items LEFT JOIN stock s ON s.consumable_id = items.id
				ORDER BY items.id`,
				orgID, nullUUID(filter.ItemID))
			if err != nil {
				return fmt.Errorf("project quantity availability: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var a Availability
				var id string
				if err := rows.Scan(&id, &a.Total, &a.Available, &a.Reserved); err != nil {
					return err
				}
				a.ItemKind = "consumable"
				a.ItemID = id
				out = append(out, a)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// binSort pins the order of the availability per bin for the keyset
// cursor.
const (
	binSort = "location_id"
	binDir  = "asc"
)

// BinAvailability returns the availability of the consumable per bin
// (GET /api/v1/consumables/{id}/availability, E-38, WP-759): its stock
// rows the caller sees, with locationID only that bin, ordered by bin, at
// most page.Limit+1 of them (BinAvailabilityPage), and their number. A
// consumable that is unknown or outside the caller's scope is
// ErrItemNotFound; a malformed cursor api.ErrInvalidCursor.
func (p *PGAvailability) BinAvailability(ctx context.Context, orgID, consumableID, locationID string,
	page api.PaginationParams) ([]BinAvailability, int, error) {
	if !api.IsUUID(consumableID) {
		return nil, 0, ErrItemNotFound
	}
	var out []BinAvailability
	var total int
	err := database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM consumable WHERE id = $1 AND organization_id = $2)`,
			consumableID, orgID).Scan(&visible); err != nil {
			return fmt.Errorf("check consumable: %w", err)
		}
		if !visible {
			return ErrItemNotFound
		}
		where := "organization_id = $1 AND consumable_id = $2 AND ($3::uuid IS NULL OR location_id = $3::uuid)"
		args := []any{orgID, consumableID, nullUUID(locationID)}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_level WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count bins: %w", err)
		}
		if page.Cursor != nil {
			if !page.Cursor.Matches(binSort, binDir) || !api.IsUUID(page.Cursor.Value) || !api.IsUUID(page.Cursor.ID) {
				return fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
			}
			where += " AND " + api.KeysetClause(binSort, "uuid", binDir, len(args)+1)
			args = append(args, page.Cursor.Value, page.Cursor.ID)
		}
		tail := fmt.Sprintf(" ORDER BY location_id, id LIMIT $%d", len(args)+1)
		args = append(args, page.Limit+1)
		rows, err := tx.Query(ctx, `SELECT id::text, location_id::text, on_hand::float8, reserved::float8,
			(on_hand - reserved)::float8 FROM stock_level WHERE `+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list bins: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (BinAvailability, error) {
			var b BinAvailability
			return b, row.Scan(&b.stockID, &b.LocationID, &b.OnHand, &b.Reserved, &b.Available)
		})
		if err != nil {
			return fmt.Errorf("list bins: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// BinAvailabilityPage is the list response of the rows BinAvailability
// read (at most one more than the limit): when more follow, the page ends
// at the limit, has_more is set and next_cursor is the keyset cursor after
// its last row.
func BinAvailabilityPage(rows []BinAvailability, total int, page api.PaginationParams) api.ListResponse[BinAvailability] {
	list := api.ListResponse[BinAvailability]{Data: api.NonNilSlice(rows), Total: total, Limit: page.Limit}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := &list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: binSort, Dir: binDir, Value: last.LocationID, ID: last.stockID})
	}
	return list
}

func nullUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
