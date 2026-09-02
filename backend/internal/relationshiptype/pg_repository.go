package relationshiptype

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, COALESCE(organization_id::text, ''), key, forward_label, reverse_label,
	source_ci_types, target_ci_types, direction, cardinality,
	COALESCE(category, ''), impact_participation, is_system,
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

// List returns global system types plus tenant-specific types.
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Type, int, error) {
	var out []Type
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	return r.withTenant(ctx, typ.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		direction := typ.Direction
		if direction == "" {
			direction = "directed"
		}
		cardinality := typ.Cardinality
		if cardinality == "" {
			cardinality = "many_to_many"
		}
		source, _ := json.Marshal(typ.SourceCITypes)
		target, _ := json.Marshal(typ.TargetCITypes)
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO relationship_type (
				organization_id, key, forward_label, reverse_label,
				source_ci_types, target_ci_types, direction, cardinality,
				category, impact_participation, is_system, description
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,false,$11)
			RETURNING %s`, selectColumns),
			typ.OrganizationID, typ.Key, typ.ForwardLabel, typ.ReverseLabel,
			string(source), string(target), direction, cardinality,
			nilIfEmpty(typ.Category), typ.ImpactParticipation, nilIfEmpty(typ.Description))
		scanned, err := scan(row)
		if err != nil {
			return fmt.Errorf("create relationship type: %w", err)
		}
		*typ = *scanned
		return nil
	})
}

// Update modifies a tenant-owned relationship type.
func (r *PGRepository) Update(ctx context.Context, orgID, key string, req UpsertRequest) (*Type, error) {
	var out *Type
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{key}
		pos := 2
		add := func(col string, val any) {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		if req.ForwardLabel != "" {
			add("forward_label", req.ForwardLabel)
		}
		if req.ReverseLabel != "" {
			add("reverse_label", req.ReverseLabel)
		}
		if req.SourceCITypes != nil {
			raw, _ := json.Marshal(req.SourceCITypes)
			add("source_ci_types", string(raw))
		}
		if req.TargetCITypes != nil {
			raw, _ := json.Marshal(req.TargetCITypes)
			add("target_ci_types", string(raw))
		}
		if req.Direction != "" {
			add("direction", req.Direction)
		}
		if req.Cardinality != "" {
			add("cardinality", req.Cardinality)
		}
		if req.Category != "" {
			add("category", req.Category)
		}
		if req.ImpactParticipation != nil {
			add("impact_participation", *req.ImpactParticipation)
		}
		if req.Description != "" {
			add("description", req.Description)
		}
		if len(sets) == 0 {
			t, err := r.GetByKey(ctx, orgID, key)
			if err != nil {
				return err
			}
			out = t
			return nil
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"UPDATE relationship_type SET %s WHERE key = $1 AND organization_id IS NOT NULL RETURNING %s",
			strings.Join(sets, ", "), selectColumns), args...)
		if err != nil {
			return fmt.Errorf("update relationship type: %w", err)
		}
		defer rows.Close()
		if rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return err
			}
			out = t
			return rows.Err()
		}
		return fmt.Errorf("not found")
	})
	return out, err
}

// Delete removes a tenant-owned non-system relationship type.
func (r *PGRepository) Delete(ctx context.Context, orgID, key string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		&t.ID, &t.OrganizationID, &t.Key, &t.ForwardLabel, &t.ReverseLabel,
		&sourceRaw, &targetRaw, &t.Direction, &t.Cardinality, &t.Category,
		&t.ImpactParticipation, &t.IsSystem, &t.Description, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	t.SourceCITypes = []string{}
	t.TargetCITypes = []string{}
	if len(sourceRaw) > 0 {
		_ = json.Unmarshal(sourceRaw, &t.SourceCITypes)
	}
	if len(targetRaw) > 0 {
		_ = json.Unmarshal(targetRaw, &t.TargetCITypes)
	}
	return t, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
