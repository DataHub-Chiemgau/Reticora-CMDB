package privacy

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository persists retention policies in PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed privacy repository.
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
	if scope := tenant.ClientScope(ctx); scope != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.client_scope', $1, true)", scope); err != nil {
			return fmt.Errorf("set client scope: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const policySelectColumns = `
	id::text,
	organization_id::text,
	retention_days,
	mode,
	created_at,
	updated_at
`

func (r *PGRepository) GetPolicy(ctx context.Context, orgID string) (*RetentionPolicy, error) {
	var policy *RetentionPolicy
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"SELECT "+policySelectColumns+" FROM privacy_retention_policy WHERE organization_id = $1", orgID)
		var err error
		policy, err = scanPolicy(row)
		if err == pgx.ErrNoRows {
			return ErrNoPolicy
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func (r *PGRepository) UpsertPolicy(ctx context.Context, policy *RetentionPolicy) (*RetentionPolicy, error) {
	var stored *RetentionPolicy
	err := r.withTenant(ctx, policy.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO privacy_retention_policy (organization_id, retention_days, mode)
			VALUES ($1, $2, $3)
			ON CONFLICT (organization_id)
			DO UPDATE SET retention_days = EXCLUDED.retention_days, mode = EXCLUDED.mode
			RETURNING `+policySelectColumns,
			policy.OrganizationID, policy.RetentionDays, policy.Mode)
		var err error
		stored, err = scanPolicy(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

func scanPolicy(row pgx.Row) (*RetentionPolicy, error) {
	policy := &RetentionPolicy{}
	if err := row.Scan(
		&policy.ID,
		&policy.OrganizationID,
		&policy.RetentionDays,
		&policy.Mode,
		&policy.CreatedAt,
		&policy.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return policy, nil
}
