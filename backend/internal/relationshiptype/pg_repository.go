package relationshiptype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, COALESCE(organization_id::text, ''), key, display_name, reverse_name,
	allowed_source_types, allowed_target_types, directed, cardinality,
	category, impact_direction, redundant, is_system,
	COALESCE(description, ''), created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed relationship type repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns global system types plus tenant-specific types.
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Type, int, error) {
	var out []Type
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM relationship_type").Scan(&total); err != nil {
			return fmt.Errorf("count relationship types: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM relationship_type ORDER BY key ASC LIMIT $1 OFFSET $2",
			selectColumns), page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list relationship types: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByKey resolves a key within the tenant scope (tenant shadows global).
func (r *PGRepository) GetByKey(ctx context.Context, orgID, key string) (*Type, error) {
	var out *Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM relationship_type WHERE key = $1 ORDER BY organization_id NULLS LAST LIMIT 1",
			selectColumns), key)
		if err != nil {
			return fmt.Errorf("get relationship type: %w", err)
		}
		defer rows.Close()
		if rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return err
			}
			out = t
		}
		return rows.Err()
	})
	if out == nil && err == nil {
		return nil, fmt.Errorf("not found")
	}
	return out, err
}

// Create inserts a tenant-specific relationship type.
func (r *PGRepository) Create(ctx context.Context, typ *Type) error {
	if typ.Cardinality == "" {
		typ.Cardinality = "many_to_many"
	}
	if err := typ.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return database.WithRequestTenant(ctx, r.pool, typ.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		source, _ := json.Marshal(nonNil(typ.AllowedSourceTypes))
		target, _ := json.Marshal(nonNil(typ.AllowedTargetTypes))
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO relationship_type (
				organization_id, key, display_name, reverse_name,
				allowed_source_types, allowed_target_types, directed, cardinality,
				category, impact_direction, redundant, is_system, description
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,false,$12)
			RETURNING %s`, selectColumns),
			typ.OrganizationID, typ.Key, typ.DisplayName, typ.ReverseName,
			string(source), string(target), typ.Directed, typ.Cardinality,
			typ.Category, typ.ImpactDirection, typ.Redundant, nilIfEmpty(typ.Description))
		scanned, err := scan(row)
		if err != nil {
			return fmt.Errorf("create relationship type: %w", err)
		}
		*typ = *scanned
		return nil
	})
}

// Update modifies a tenant-owned relationship type; the merged type must
// satisfy the REL-01 contract.
func (r *PGRepository) Update(ctx context.Context, orgID, key string, req UpsertRequest) (*Type, error) {
	var out *Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM relationship_type WHERE key = $1 AND organization_id IS NOT NULL FOR UPDATE", selectColumns), key))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read relationship type: %w", err)
		}
		req.apply(current)
		if verr := current.Validate(); verr != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, verr)
		}
		source, _ := json.Marshal(nonNil(current.AllowedSourceTypes))
		target, _ := json.Marshal(nonNil(current.AllowedTargetTypes))
		out, err = scan(tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE relationship_type SET display_name = $2, reverse_name = $3, allowed_source_types = $4,
				allowed_target_types = $5, directed = $6, cardinality = $7, category = $8,
				impact_direction = $9, redundant = $10, description = $11
			WHERE id = $1 RETURNING %s`, selectColumns),
			current.ID, current.DisplayName, current.ReverseName, string(source), string(target), current.Directed,
			current.Cardinality, current.Category, current.ImpactDirection, current.Redundant, nilIfEmpty(current.Description)))
		if err != nil {
			return fmt.Errorf("update relationship type: %w", err)
		}
		return nil
	})
	return out, err
}

// Delete removes a tenant-owned non-system relationship type.
func (r *PGRepository) Delete(ctx context.Context, orgID, key string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM relationship_type WHERE key = $1 AND organization_id IS NOT NULL AND NOT is_system", key)
		if err != nil {
			return fmt.Errorf("delete relationship type: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// Exists reports whether a key resolves within the tenant scope.
func (r *PGRepository) Exists(ctx context.Context, orgID, key string) (bool, error) {
	_, err := r.GetByKey(ctx, orgID, key)
	if err != nil {
		if err.Error() == "not found" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Type, error) {
	t := &Type{}
	var sourceRaw, targetRaw []byte
	if err := s.Scan(
		&t.ID, &t.OrganizationID, &t.Key, &t.DisplayName, &t.ReverseName,
		&sourceRaw, &targetRaw, &t.Directed, &t.Cardinality, &t.Category,
		&t.ImpactDirection, &t.Redundant, &t.IsSystem, &t.Description, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	t.AllowedSourceTypes = []string{}
	t.AllowedTargetTypes = []string{}
	if len(sourceRaw) > 0 {
		_ = json.Unmarshal(sourceRaw, &t.AllowedSourceTypes)
	}
	if len(targetRaw) > 0 {
		_ = json.Unmarshal(targetRaw, &t.AllowedTargetTypes)
	}
	return t, nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
