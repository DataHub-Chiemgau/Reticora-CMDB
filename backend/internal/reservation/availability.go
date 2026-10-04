package reservation

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAvailability implements AvailabilityProvider with SQL projections over
// assets, quantity items, reservations and lifecycle states (spec §10).
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
// (active reservation), and available (everything else). Quantity items
// project stock_level minus active reservation quantities.
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

		if filter.ItemKind == "" || filter.ItemKind == "quantity_item" {
			rows, err := tx.Query(ctx, `
				SELECT q.id::text,
					q.stock_level,
					GREATEST(q.stock_level - COALESCE(res.reserved_qty, 0), 0),
					COALESCE(res.reserved_qty, 0)
				FROM quantity_item q
				LEFT JOIN (
					SELECT quantity_item_id, SUM(quantity) AS reserved_qty
					FROM reservation
					WHERE organization_id = $1 AND state = 'active' AND quantity_item_id IS NOT NULL
					GROUP BY quantity_item_id
				) res ON res.quantity_item_id = q.id
				WHERE q.organization_id = $1
					AND ($2::uuid IS NULL OR q.id = $2::uuid)`,
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
				a.ItemKind = "quantity_item"
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

func nullUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
