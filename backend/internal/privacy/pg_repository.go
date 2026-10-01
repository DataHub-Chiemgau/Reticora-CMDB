package privacy

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := database.WithRequestTenant(ctx, r.pool, policy.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
