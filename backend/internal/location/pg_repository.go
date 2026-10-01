package location

import (
	"context"
	"errors"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// assetVisible restricts location entries to assets visible under the
// transaction's tenant scope: the asset policy filters the subquery by client
// scope, while asset_location carries no client column yet (WP-025).
const assetVisible = "EXISTS (SELECT 1 FROM asset WHERE asset.id = asset_location.asset_id)"

func (r *PGRepository) Record(ctx context.Context, e *Entry) error {
	return database.WithRequestTenant(ctx, r.pool, e.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if e.Source == "" {
			e.Source = "scan"
		}
		if e.RecordedAt.IsZero() {
			e.RecordedAt = time.Now().UTC()
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO asset_location (organization_id, asset_id, lat, lon, accuracy_m, source, recorded_at)
			SELECT $1, $2::uuid, $3, $4, $5, $6, $7
			WHERE EXISTS (SELECT 1 FROM asset WHERE asset.id = $2::uuid)
			RETURNING id::text, created_at
		`, e.OrganizationID, e.AssetID, e.Lat, e.Lon, e.AccuracyM, e.Source, e.RecordedAt).
			Scan(&e.ID, &e.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAssetNotFound
		}
		return err
	})
}

func (r *PGRepository) History(ctx context.Context, orgID, assetID string, page api.PaginationParams) ([]Entry, int, error) {
	out := []Entry{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asset_location WHERE organization_id = $1 AND asset_id = $2 AND "+assetVisible, orgID, assetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+entryCols+" FROM asset_location WHERE organization_id = $1 AND asset_id = $2 AND "+assetVisible+" ORDER BY recorded_at DESC LIMIT $3 OFFSET $4", orgID, assetID, page.Limit, page.Offset)
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
