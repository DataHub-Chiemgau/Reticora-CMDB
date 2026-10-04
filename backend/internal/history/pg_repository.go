package history

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, entity_type, entity_id::text,
	COALESCE(actor_id::text, ''), change_type, COALESCE(field_name, ''),
	old_value, new_value, COALESCE(comment, ''), created_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed history repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// visibleEntity hides the changes of a CI that is not visible under the
// transaction's tenant scope: the ci policy filters the subquery by client
// scope, while entity_change carries no client column yet (WP-025). Other
// entity types follow with their packages' WithTenant migration.
const visibleEntity = `(entity_type <> 'ci' OR EXISTS (SELECT 1 FROM ci WHERE ci.id = entity_change.entity_id))`

// Record appends a change row to the entity_change trail.
func (r *PGRepository) Record(ctx context.Context, change *Change) error {
	return database.WithRequestTenant(ctx, r.pool, change.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if change.EntityType == "ci" {
			var visible bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", change.EntityID).Scan(&visible); err != nil {
				return fmt.Errorf("check ci visibility: %w", err)
			}
			if !visible {
				return fmt.Errorf("not found")
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO entity_change (
				organization_id, entity_type, entity_id, actor_id,
				change_type, field_name, old_value, new_value, comment
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id::text, created_at`,
			change.OrganizationID, change.EntityType, change.EntityID,
			nilIfEmpty(change.ActorID), change.ChangeType, nilIfEmpty(change.FieldName),
			jsonOrNil(change.OldValue), jsonOrNil(change.NewValue),
			nilIfEmpty(change.Comment),
		).Scan(&change.ID, &change.CreatedAt)
	})
}

// List returns the changes of one entity, newest first.
func (r *PGRepository) List(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Change, int, error) {
	var out []Change
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND "+visibleEntity,
			orgID, entityType, entityID).Scan(&total); err != nil {
			return fmt.Errorf("count entity changes: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND %s ORDER BY created_at DESC LIMIT $4 OFFSET $5",
			selectColumns, visibleEntity), orgID, entityType, entityID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list entity changes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

// TrailUpTo returns changes oldest-first for point-in-time replay.
func (r *PGRepository) TrailUpTo(ctx context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error) {
	var out []Change
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND created_at <= $4 AND %s ORDER BY created_at ASC",
			selectColumns, visibleEntity), orgID, entityType, entityID, at)
		if err != nil {
			return fmt.Errorf("list entity trail: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Change, error) {
	c := &Change{}
	var oldRaw, newRaw []byte
	if err := s.Scan(
		&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID,
		&c.ChangeType, &c.FieldName, &oldRaw, &newRaw, &c.Comment, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	if len(oldRaw) > 0 {
		_ = json.Unmarshal(oldRaw, &c.OldValue)
	}
	if len(newRaw) > 0 {
		_ = json.Unmarshal(newRaw, &c.NewValue)
	}
	return c, nil
}

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(raw)
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
