package entitlement

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed entitlement repository.
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

// List returns all entitlement rows stored for the organization.
func (r *PGRepository) List(ctx context.Context, orgID string) ([]Entitlement, error) {
	items := make([]Entitlement, 0)

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, feature_key, plan, enabled, limit_value, expires_at, updated_at
			FROM entitlement
			ORDER BY feature_key
		`)
		if err != nil {
			return fmt.Errorf("list entitlements: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			ent, err := scanEntitlement(rows)
			if err != nil {
				return fmt.Errorf("scan entitlement: %w", err)
			}
			items = append(items, ent)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	return items, nil
}

// Upsert stores an entitlement, replacing an existing row for the same
// (organization, feature) pair.
func (r *PGRepository) Upsert(ctx context.Context, ent Entitlement) (Entitlement, error) {
	var stored Entitlement

	err := r.withTenant(ctx, ent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limit_value, expires_at)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5)
			ON CONFLICT (organization_id, feature_key) DO UPDATE
			SET plan = EXCLUDED.plan,
			    enabled = EXCLUDED.enabled,
			    limit_value = EXCLUDED.limit_value,
			    expires_at = EXCLUDED.expires_at,
			    updated_at = now()
			RETURNING organization_id::text, feature_key, plan, enabled, limit_value, expires_at, updated_at
		`, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limit, ent.ExpiresAt)

		var err error
		stored, err = scanEntitlement(row)
		if err != nil {
			return fmt.Errorf("upsert entitlement: %w", err)
		}
		return nil
	})

	return stored, err
}

type entitlementScanner interface {
	Scan(dest ...any) error
}

func scanEntitlement(scanner entitlementScanner) (Entitlement, error) {
	var (
		ent       Entitlement
		plan      string
		limit     *int64
		expiresAt *time.Time
		updatedAt time.Time
	)

	if err := scanner.Scan(
		&ent.OrganizationID,
		&ent.FeatureKey,
		&plan,
		&ent.Enabled,
		&limit,
		&expiresAt,
		&updatedAt,
	); err != nil {
		return Entitlement{}, err
	}

	ent.Plan = Plan(plan)
	ent.Limit = limit
	if expiresAt != nil {
		utc := expiresAt.UTC()
		ent.ExpiresAt = &utc
	}
	ent.UpdatedAt = updatedAt.UTC()

	return ent, nil
}
