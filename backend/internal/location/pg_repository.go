package location

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const entryCols = `id::text, organization_id::text, asset_id::text, lat, lon, accuracy_m, source, recorded_at, created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed location repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
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

func (r *PGRepository) Record(ctx context.Context, e *Entry) error {
	return r.withTenant(ctx, e.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if e.Source == "" {
			e.Source = "scan"
		}
		if e.RecordedAt.IsZero() {
			e.RecordedAt = time.Now().UTC()
		}
		return tx.QueryRow(ctx, `
			INSERT INTO asset_location (organization_id, asset_id, lat, lon, accuracy_m, source, recorded_at)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7)
			RETURNING id::text, created_at
		`, e.OrganizationID, e.AssetID, e.Lat, e.Lon, e.AccuracyM, e.Source, e.RecordedAt).
			Scan(&e.ID, &e.CreatedAt)
	})
}

func (r *PGRepository) History(ctx context.Context, orgID, assetID string, page api.PaginationParams) ([]Entry, int, error) {
	out := []Entry{}
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asset_location WHERE organization_id = $1 AND asset_id = $2", orgID, assetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+entryCols+" FROM asset_location WHERE organization_id = $1 AND asset_id = $2 ORDER BY recorded_at DESC LIMIT $3 OFFSET $4", orgID, assetID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := Entry{}
			if err := rows.Scan(&e.ID, &e.OrganizationID, &e.AssetID, &e.Lat, &e.Lon, &e.AccuracyM, &e.Source, &e.RecordedAt, &e.CreatedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, total, err
}
