package entitlement

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// List returns all entitlement rows stored for the organization.
func (r *PGRepository) List(ctx context.Context, orgID string) ([]Entitlement, error) {
	items := make([]Entitlement, 0)

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, feature_key, plan, enabled, limits, valid_until, source, updated_at
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
	if ent.Limits == nil {
		ent.Limits = map[string]int64{}
	}
	if ent.Source == "" {
		ent.Source = "manual"
	}

	err := database.WithRequestTenant(ctx, r.pool, ent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (organization_id, feature_key) DO UPDATE
			SET plan = EXCLUDED.plan,
			    enabled = EXCLUDED.enabled,
			    limits = EXCLUDED.limits,
			    valid_until = EXCLUDED.valid_until,
			    source = EXCLUDED.source,
			    updated_at = now()
			RETURNING organization_id::text, feature_key, plan, enabled, limits, valid_until, source, updated_at
		`, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limits, ent.ValidUntil, ent.Source)

		var err error
		stored, err = scanEntitlement(row)
		if err != nil {
			return fmt.Errorf("upsert entitlement: %w", err)
		}
		return nil
	})

	return stored, err
}

// InsertMissing stores, in one transaction of the organization's tenant
// scope, the rows whose feature has no row yet and returns their feature
// keys. An existing row is never changed (ON CONFLICT DO NOTHING), so a
// concurrent grant or deactivation wins.
func (r *PGRepository) InsertMissing(ctx context.Context, orgID string, ents []Entitlement) ([]string, error) {
	var added []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		for _, ent := range ents {
			if ent.Limits == nil {
				ent.Limits = map[string]int64{}
			}
			if ent.Source == "" {
				ent.Source = "manual"
			}
			var feature string
			err := tx.QueryRow(ctx, `
				INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
				VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6)
				ON CONFLICT (organization_id, feature_key) DO NOTHING
				RETURNING feature_key
			`, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limits, ent.ValidUntil, ent.Source).Scan(&feature)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("insert entitlement %s: %w", ent.FeatureKey, err)
			}
			added = append(added, feature)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

type entitlementScanner interface {
	Scan(dest ...any) error
}

func scanEntitlement(scanner entitlementScanner) (Entitlement, error) {
	var (
		ent        Entitlement
		plan       string
		validUntil *time.Time
		updatedAt  time.Time
	)

	if err := scanner.Scan(
		&ent.OrganizationID,
		&ent.FeatureKey,
		&plan,
		&ent.Enabled,
		&ent.Limits,
		&validUntil,
		&ent.Source,
		&updatedAt,
	); err != nil {
		return Entitlement{}, err
	}

	ent.Plan = Plan(plan)
	if ent.Limits == nil {
		ent.Limits = map[string]int64{}
	}
	if validUntil != nil {
		utc := validUntil.UTC()
		ent.ValidUntil = &utc
	}
	ent.UpdatedAt = updatedAt.UTC()

	return ent, nil
}
