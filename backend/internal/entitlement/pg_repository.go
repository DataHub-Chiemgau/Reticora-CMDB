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
			SELECT organization_id::text, feature_key, plan, enabled, limits, valid_until, source, origin, updated_at
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

// OrganizationPlan reads organization.plan and organization.plan_changed_at
// in the tenant context of the organization (WP-880).
func (r *PGRepository) OrganizationPlan(ctx context.Context, orgID string) (Plan, *time.Time, error) {
	var plan string
	var changed *time.Time
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT plan, plan_changed_at FROM organization WHERE id = current_setting('app.org_id')::uuid`).
			Scan(&plan, &changed)
	})
	if err != nil {
		return "", nil, fmt.Errorf("read organization plan: %w", err)
	}
	if changed != nil {
		utc := changed.UTC()
		changed = &utc
	}
	return normalizePlan(Plan(plan)), changed, nil
}

// LockOrganizationPlan reads organization.plan and locks the row until the
// end of the context transaction (WP-882), so concurrent plan changes of the
// organization run one after the other.
func (r *PGRepository) LockOrganizationPlan(ctx context.Context, orgID string) (Plan, error) {
	var plan string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT plan FROM organization WHERE id = current_setting('app.org_id')::uuid FOR UPDATE`).Scan(&plan)
	})
	if err != nil {
		return "", fmt.Errorf("lock organization plan: %w", err)
	}
	return normalizePlan(Plan(plan)), nil
}

// SetOrganizationPlan stores organization.plan with plan_changed_at = now()
// (WP-882) and returns the time of the change.
func (r *PGRepository) SetOrganizationPlan(ctx context.Context, orgID string, plan Plan) (time.Time, error) {
	var at time.Time
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE organization SET plan = $1, plan_changed_at = now()
			WHERE id = current_setting('app.org_id')::uuid RETURNING plan_changed_at`, string(normalizePlan(plan))).Scan(&at)
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("set organization plan: %w", err)
	}
	return at.UTC(), nil
}

// Upsert stores an entitlement, replacing an existing row for the same
// (organization, feature) pair.
func (r *PGRepository) Upsert(ctx context.Context, in *Entitlement) (Entitlement, error) {
	ent := *in
	var stored Entitlement
	if ent.Limits == nil {
		ent.Limits = map[string]int64{}
	}
	if ent.Source == "" {
		ent.Source = "manual"
	}
	if ent.Origin == "" {
		ent.Origin = DefaultOrigin(ent.FeatureKey, ent.Plan)
	}

	// The origin of an existing row stays (WP-880): a grant on a row of the
	// plan does not make it a manual row; a new row takes the caller's.
	err := database.WithRequestTenant(ctx, r.pool, ent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source, origin)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (organization_id, feature_key) DO UPDATE
			SET plan = EXCLUDED.plan,
			    enabled = EXCLUDED.enabled,
			    limits = EXCLUDED.limits,
			    valid_until = EXCLUDED.valid_until,
			    source = EXCLUDED.source,
			    updated_at = now()
			RETURNING organization_id::text, feature_key, plan, enabled, limits, valid_until, source, origin, updated_at
		`, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limits, ent.ValidUntil, ent.Source, ent.Origin)

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
		for i := range ents {
			ent := ents[i]
			if ent.Limits == nil {
				ent.Limits = map[string]int64{}
			}
			if ent.Source == "" {
				ent.Source = "manual"
			}
			if ent.Origin == "" {
				ent.Origin = DefaultOrigin(ent.FeatureKey, ent.Plan)
			}
			var feature string
			err := tx.QueryRow(ctx, `
				INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source, origin)
				VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (organization_id, feature_key) DO NOTHING
				RETURNING feature_key
			`, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limits, ent.ValidUntil, ent.Source, ent.Origin).Scan(&feature)
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
		&ent.Origin,
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
