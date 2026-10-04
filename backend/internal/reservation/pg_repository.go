package reservation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// reservationVisible restricts reservations to those whose asset or quantity
// item is visible under the transaction's tenant scope: the asset and
// quantity_item policies filter the subqueries, while reservation carries no
// client column yet (WP-025).
const reservationVisible = `(reservation.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = reservation.asset_id))
	AND (reservation.quantity_item_id IS NULL OR EXISTS (SELECT 1 FROM quantity_item WHERE quantity_item.id = reservation.quantity_item_id))`

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

// List returns reservations filtered by state.
func (r *PGRepository) List(ctx context.Context, orgID string, state string, page api.PaginationParams) ([]Reservation, int, error) {
	var out []Reservation
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		if state != "" {
			where = append(where, "state = $2")
			args = append(args, state)
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM reservation WHERE "+reservationVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count reservations: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE "+reservationVisible+" AND %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE "+reservationVisible+" AND id = $1 AND organization_id = $2",
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
	return database.WithRequestTenant(ctx, r.pool, res.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
		return requireVisible(ctx, tx, "reservation", reservationVisible, res.ID)
	})
}

// Transition moves a reservation to a terminal state.
func (r *PGRepository) Transition(ctx context.Context, orgID, id, state string) (*Reservation, error) {
	var out *Reservation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		res, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE reservation SET state = $3 WHERE "+reservationVisible+" AND id = $1 AND organization_id = $2 AND state = 'active' RETURNING %s",
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		column := "asset_id"
		if itemKind == "quantity_item" {
			column = "quantity_item_id"
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM reservation WHERE "+reservationVisible+" AND organization_id = $1 AND %s = $2 AND state = 'active'",
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

// ExpireDue marks overdue active reservations as expired. Expiry is a
// cross-tenant batch job: it iterates the organizations and expires each
// organization's reservations in its own tenant transaction (WP-022). A
// failing organization does not stop the others; the errors are joined.
func (r *PGRepository) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	orgIDs, err := database.OrganizationIDs(ctx, r.pool)
	if err != nil {
		return 0, err
	}
	count := 0
	var errs []error
	for _, orgID := range orgIDs {
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `
				UPDATE reservation SET state = 'expired', updated_at = now()
				WHERE state = 'active' AND expires_at IS NOT NULL AND expires_at < $1`, now)
			if err != nil {
				return fmt.Errorf("expire reservations of organization %s: %w", orgID, err)
			}
			count += int(tag.RowsAffected())
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return count, errors.Join(errs...)
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
