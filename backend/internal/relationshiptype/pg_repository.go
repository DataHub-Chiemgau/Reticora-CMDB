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
	COALESCE(description, ''), metadata_schema, created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed relationship type repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// resolvedTypes selects one row per key of the types the organization sees
// (RLS: system rows and its own), the organization's row before the system
// row of the same key (REL-06). An organization row with a system key comes
// from before WP-294; Create no longer accepts one.
const resolvedTypes = "SELECT DISTINCT ON (key) %s FROM relationship_type ORDER BY key, organization_id NULLS LAST"

// List returns the types the organization sees, one per key.
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Type, int, error) {
	var out []Type
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(DISTINCT key) FROM relationship_type").Scan(&total); err != nil {
			return fmt.Errorf("count relationship types: %w", err)
		}
		var err error
		out, err = queryTypes(ctx, tx, fmt.Sprintf(resolvedTypes, selectColumns)+" LIMIT $1 OFFSET $2", page.Limit, page.Offset)
		return err
	})
	return out, total, err
}

// ListAll returns every type the organization sees, one per key, in one
// statement without a page bound (IMP-10).
func (r *PGRepository) ListAll(ctx context.Context, orgID string) ([]Type, error) {
	var out []Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = queryTypes(ctx, tx, fmt.Sprintf(resolvedTypes, selectColumns))
		return err
	})
	return out, err
}

func queryTypes(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]Type, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list relationship types: %w", err)
	}
	defer rows.Close()
	out := []Type{}
	for rows.Next() {
		t, serr := scan(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// GetByKey resolves a key within the tenant scope: the organization's row
// before the system row (REL-06, as List).
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
		return nil, ErrNotFound
	}
	return out, err
}

// Create inserts a tenant-specific relationship type; the key of a system
// type is taken (ErrConflict, REL-06), so no organization type shadows one.
func (r *PGRepository) Create(ctx context.Context, typ *Type) error {
	if typ.Cardinality == "" {
		typ.Cardinality = CardinalityManyToMany
	}
	if err := typ.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return database.WithRequestTenant(ctx, r.pool, typ.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var system bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM relationship_type WHERE key = $1 AND organization_id IS NULL)",
			typ.Key).Scan(&system); err != nil {
			return fmt.Errorf("check system relationship type: %w", err)
		}
		if system {
			return ErrConflict
		}
		if err := checkCITypeKeys(ctx, tx, ciTypeLists(typ.AllowedSourceTypes, typ.AllowedTargetTypes)); err != nil {
			return err
		}
		source, _ := json.Marshal(nonNil(typ.AllowedSourceTypes))
		target, _ := json.Marshal(nonNil(typ.AllowedTargetTypes))
		schema, err := marshalSchema(typ.MetadataSchema)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO relationship_type (
				organization_id, key, display_name, reverse_name,
				allowed_source_types, allowed_target_types, directed, cardinality,
				category, impact_direction, redundant, is_system, description, metadata_schema
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,false,$12,$13)
			RETURNING %s`, selectColumns),
			typ.OrganizationID, typ.Key, typ.DisplayName, typ.ReverseName,
			string(source), string(target), typ.Directed, typ.Cardinality,
			typ.Category, typ.ImpactDirection, typ.Redundant, nilIfEmpty(typ.Description), schema)
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
func (r *PGRepository) Update(ctx context.Context, orgID, key string, req *UpsertRequest) (*Type, error) {
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
		// Only the lists of the request: a stored list stays as it is.
		if cerr := checkCITypeKeys(ctx, tx, ciTypeLists(req.AllowedSourceTypes, req.AllowedTargetTypes)); cerr != nil {
			return cerr
		}
		source, _ := json.Marshal(nonNil(current.AllowedSourceTypes))
		target, _ := json.Marshal(nonNil(current.AllowedTargetTypes))
		schema, err := marshalSchema(current.MetadataSchema)
		if err != nil {
			return err
		}
		out, err = scan(tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE relationship_type SET display_name = $2, reverse_name = $3, allowed_source_types = $4,
				allowed_target_types = $5, directed = $6, cardinality = $7, category = $8,
				impact_direction = $9, redundant = $10, description = $11, metadata_schema = $12
			WHERE id = $1 RETURNING %s`, selectColumns),
			current.ID, current.DisplayName, current.ReverseName, string(source), string(target), current.Directed,
			current.Cardinality, current.Category, current.ImpactDirection, current.Redundant, nilIfEmpty(current.Description),
			schema))
		if err != nil {
			return fmt.Errorf("update relationship type: %w", err)
		}
		return nil
	})
	return out, err
}

// Delete removes a tenant-owned non-system relationship type. A type a
// relationship of the organization (ci_relationship.rel_type, index
// idx_ci_rel_type) or a suppression (relationship_suppression.
// relationship_type_id, 000178) uses stays: ErrInUse (409, REL-06). A
// missing, foreign or system type is ErrNotFound (404).
func (r *PGRepository) Delete(ctx context.Context, orgID, key string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM relationship_type
			WHERE key = $1 AND organization_id IS NOT NULL AND NOT is_system FOR UPDATE`, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read relationship type: %w", err)
		}
		var used bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_relationship WHERE rel_type = $1 AND organization_id = $3::uuid)
			OR EXISTS (SELECT 1 FROM relationship_suppression WHERE relationship_type_id = $2::uuid)`, key, id, orgID).Scan(&used); err != nil {
			return fmt.Errorf("check use of relationship type: %w", err)
		}
		if used {
			return ErrInUse
		}
		if _, err = tx.Exec(ctx, "DELETE FROM relationship_type WHERE id = $1", id); err != nil {
			return fmt.Errorf("delete relationship type: %w", err)
		}
		return nil
	})
}

// Exists reports whether a key resolves within the tenant scope.
func (r *PGRepository) Exists(ctx context.Context, orgID, key string) (bool, error) {
	_, err := r.GetByKey(ctx, orgID, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// checkCITypeKeys refuses a key of the lists that names no CI type the
// organization sees (an unknown key, a type of another organization) with
// a field error, 400 on the list (REL-01, E-95, F-313). ci_type is read
// under RLS: system types and the organization's own.
func checkCITypeKeys(ctx context.Context, tx pgx.Tx, lists []ciTypeList) error {
	for _, list := range lists {
		if len(list.keys) == 0 {
			continue
		}
		var unknown []string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(k.key ORDER BY k.pos), '{}') FROM unnest($1::text[]) WITH ORDINALITY AS k(key, pos)
			WHERE NOT EXISTS (SELECT 1 FROM ci_type WHERE ci_type.key = k.key)`, list.keys).Scan(&unknown); err != nil {
			return fmt.Errorf("check CI types of %s: %w", list.field, err)
		}
		if len(unknown) > 0 {
			return &api.FieldError{Field: list.field, Message: "unknown CI type: " + strings.Join(unknown, ", ")}
		}
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Type, error) {
	t := &Type{}
	var sourceRaw, targetRaw, schemaRaw []byte
	if err := s.Scan(
		&t.ID, &t.OrganizationID, &t.Key, &t.DisplayName, &t.ReverseName,
		&sourceRaw, &targetRaw, &t.Directed, &t.Cardinality, &t.Category,
		&t.ImpactDirection, &t.Redundant, &t.IsSystem, &t.Description, &schemaRaw, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	schema, err := decodeSchema(schemaRaw)
	if err != nil {
		return nil, err
	}
	t.MetadataSchema = schema
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

// marshalSchema renders the schema as the jsonb array of metadata_schema.
func marshalSchema(schema []AttributeDefinition) (string, error) {
	if schema == nil {
		schema = []AttributeDefinition{}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("encode metadata_schema: %w", err)
	}
	return string(raw), nil
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
