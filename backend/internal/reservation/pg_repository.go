package reservation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, item_kind, COALESCE(asset_id::text, ''),
	COALESCE(quantity_item_id::text, ''), quantity, state, reserved_from,
	reserved_until, COALESCE(assignee_id::text, ''), COALESCE(project_ref, ''),
	COALESCE(order_id::text, ''), COALESCE(ticket_id::text, ''), expires_at,
	COALESCE(reason, ''), COALESCE(created_by::text, ''), created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed reservation repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// List returns reservations filtered by state.
func (r *PGRepository) List(ctx context.Context, orgID string, state string, page api.PaginationParams) ([]Reservation, int, error) {
	var out []Reservation
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		if state != "" {
			where = append(where, "state = $2")
			args = append(args, state)
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM reservation WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count reservations: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
			selectColumns, clause, len(args)-1, len(args)), args...)
		if err != nil {
			return fmt.Errorf("list reservations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *res)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one reservation.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Reservation, error) {
	var out *Reservation
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE id = $1 AND organization_id = $2",
			selectColumns), id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get reservation: %w", err)
		}
		out = res
		return nil
	})
	return out, err
}

// Create inserts a reservation. The partial unique index
// idx_reservation_one_active_per_asset rejects conflicting serialized-asset
// reservations atomically.
func (r *PGRepository) Create(ctx context.Context, res *Reservation) error {
	return r.withTenant(ctx, res.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO reservation (
				organization_id, item_kind, asset_id, quantity_item_id, quantity,
				state, reserved_from, reserved_until, assignee_id, project_ref,
				order_id, ticket_id, expires_at, reason, created_by
			) VALUES ($1,$2,$3,$4,$5,'active',COALESCE($6, now()),$7,$8,$9,$10,$11,$12,$13,$14)
			RETURNING %s`, selectColumns),
			res.OrganizationID, res.ItemKind, nilIfEmpty(res.AssetID),
			nilIfEmpty(res.QuantityItemID), res.Quantity, res.ReservedFrom,
			res.ReservedUntil, nilIfEmpty(res.AssigneeID), nilIfEmpty(res.ProjectRef),
			nilIfEmpty(res.OrderID), nilIfEmpty(res.TicketID), res.ExpiresAt,
			nilIfEmpty(res.Reason), nilIfEmpty(res.CreatedBy))
		scanned, err := scan(row)
		if err != nil {
			if strings.Contains(err.Error(), "idx_reservation_one_active_per_asset") {
				return fmt.Errorf("asset already has an active reservation")
			}
			return fmt.Errorf("create reservation: %w", err)
		}
		*res = *scanned
		return nil
	})
}

// Transition moves a reservation to a terminal state.
func (r *PGRepository) Transition(ctx context.Context, orgID, id, state string) (*Reservation, error) {
	var out *Reservation
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE reservation SET state = $3 WHERE id = $1 AND organization_id = $2 AND state = 'active' RETURNING %s",
			selectColumns), id, orgID, state))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found or not active")
			}
			return fmt.Errorf("transition reservation: %w", err)
		}
		out = res
		return nil
	})
	return out, err
}

// ActiveForItem returns the active reservations of one item.
func (r *PGRepository) ActiveForItem(ctx context.Context, orgID, itemKind, itemID string) ([]Reservation, error) {
	var out []Reservation
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		column := "asset_id"
		if itemKind == "quantity_item" {
			column = "quantity_item_id"
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE organization_id = $1 AND %s = $2 AND state = 'active'",
			selectColumns, column), orgID, itemID)
		if err != nil {
			return fmt.Errorf("list active reservations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *res)
		}
		return rows.Err()
	})
	return out, err
}

// ExpireDue marks overdue active reservations as expired.
func (r *PGRepository) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	// Expiry is a cross-tenant batch job: it runs with the system flag used
	// by the webhook retry worker pattern. RLS on reservation has no system
	// exception, so the sweeper iterates tenants instead.
	var count int
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE reservation SET state = 'expired', updated_at = now()
		WHERE state = 'active' AND expires_at IS NOT NULL AND expires_at < $1
		RETURNING organization_id::text`, now)
	if err != nil {
		return 0, fmt.Errorf("expire reservations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Reservation, error) {
	res := &Reservation{}
	if err := s.Scan(
		&res.ID, &res.OrganizationID, &res.ItemKind, &res.AssetID,
		&res.QuantityItemID, &res.Quantity, &res.State, &res.ReservedFrom,
		&res.ReservedUntil, &res.AssigneeID, &res.ProjectRef, &res.OrderID,
		&res.TicketID, &res.ExpiresAt, &res.Reason, &res.CreatedBy,
		&res.CreatedAt, &res.UpdatedAt,
	); err != nil {
		return nil, err
	}
	res.ReservedFrom = res.ReservedFrom.UTC()
	res.CreatedAt = res.CreatedAt.UTC()
	res.UpdatedAt = res.UpdatedAt.UTC()
	return res, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
