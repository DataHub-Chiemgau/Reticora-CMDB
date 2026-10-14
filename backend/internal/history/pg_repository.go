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

// unifiedCIHistory is the history of a CI (CI-06): its ci_change rows
// (with the site at the time), the entity_change rows and the audit
// entries of the CI, one chronological trail.
const unifiedCIHistory = `
	SELECT id::text, organization_id::text, 'ci', ci_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, 'ci_change', COALESCE(site_id::text, '')
	FROM ci_change WHERE organization_id = $1 AND ci_id = $2
	UNION ALL
	SELECT id::text, organization_id::text, entity_type, entity_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, 'entity_change', ''
	FROM entity_change WHERE organization_id = $1 AND entity_type = 'ci' AND entity_id = $2
	UNION ALL
	SELECT id::text, organization_id::text, 'ci', resource_id::text, COALESCE(actor_id::text, ''), action,
	       '', NULL, changes, '', "timestamp", 'audit', ''
	FROM audit_log WHERE organization_id = $1 AND resource_type = 'ci' AND resource_id = $2`

// listCI returns the unified history of a CI the caller can see, newest
// first.
func (r *PGRepository) listCI(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	out := []Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", ciID).Scan(&visible); err != nil || !visible {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ("+unifiedCIHistory+") h", orgID, ciID).Scan(&total); err != nil {
			return fmt.Errorf("count CI history: %w", err)
		}
		rows, err := tx.Query(ctx, "SELECT * FROM ("+unifiedCIHistory+") h ORDER BY 11 DESC, 1 LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list CI history: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c := Change{}
			var oldRaw, newRaw []byte
			if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
				&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.Source, &c.SiteID); err != nil {
				return err
			}
			if len(oldRaw) > 0 {
				_ = json.Unmarshal(oldRaw, &c.OldValue)
			}
			if len(newRaw) > 0 {
				_ = json.Unmarshal(newRaw, &c.NewValue)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, total, err
}

// List returns the changes of one entity, newest first; for a CI the
// unified history of CI-06.
func (r *PGRepository) List(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Change, int, error) {
	if entityType == "ci" {
		return r.listCI(ctx, orgID, entityID, page)
	}
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
