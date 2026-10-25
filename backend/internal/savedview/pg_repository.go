package savedview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, COALESCE(owner_id::text, ''),
	name, entity_kind, filter_spec, sort, columns, shared, shared_with_roles::text[], created_at, updated_at
`

// visibleTo is the predicate of the views a user may read (VIE-01): own,
// shared org-wide, or shared with a role of the user; %d is the parameter
// of the user id.
const visibleTo = `(shared OR owner_id = $%[1]d OR shared_with_roles && ARRAY(
	SELECT ra.role_id FROM role_assignment ra WHERE ra.user_id = $%[1]d
	UNION SELECT ucr.custom_role_id FROM user_custom_role ucr WHERE ucr.user_id = $%[1]d))`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed saved view repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns the caller's own views plus shared views of the organization.
func (r *PGRepository) List(ctx context.Context, orgID, ownerID string, page api.PaginationParams) ([]View, int, error) {
	var out []View
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1 AND " + fmt.Sprintf(visibleTo, 2)
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM saved_view WHERE "+where,
			orgID, nilIfEmpty(ownerID)).Scan(&total); err != nil {
			return fmt.Errorf("count saved views: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM saved_view WHERE %s ORDER BY name ASC LIMIT $3 OFFSET $4",
			selectColumns, where), orgID, nilIfEmpty(ownerID), page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list saved views: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one view the user may read; a private view of somebody
// else is not found (VIE-01).
func (r *PGRepository) GetByID(ctx context.Context, orgID, id, userID string) (*View, error) {
	var out *View
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM saved_view WHERE id = $1 AND organization_id = $2 AND "+fmt.Sprintf(visibleTo, 3),
			selectColumns), id, orgID, nilIfEmpty(userID)))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get saved view: %w", err)
		}
		out = v
		return nil
	})
	return out, err
}

// Create inserts a saved view.
func (r *PGRepository) Create(ctx context.Context, view *View) error {
	return database.WithRequestTenant(ctx, r.pool, view.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if view.FilterSpec == nil {
			view.FilterSpec = map[string]any{}
		}
		entityKind := view.EntityKind
		if entityKind == "" {
			entityKind = "ci"
		}
		normalizeView(view)
		sortJSON, err := json.Marshal(view.Sort)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO saved_view (organization_id, owner_id, name, entity_kind, filter_spec, sort, columns, shared, shared_with_roles)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::uuid[])
			RETURNING id::text, created_at, updated_at`,
			view.OrganizationID, nilIfEmpty(view.OwnerID), view.Name, entityKind,
			view.FilterSpec, sortJSON, view.Columns, view.Shared, view.SharedWithRoles,
		).Scan(&view.ID, &view.CreatedAt, &view.UpdatedAt)
	})
}

// Update modifies a view the caller owns (or any shared view).
func (r *PGRepository) Update(ctx context.Context, orgID, id, ownerID string, req UpsertRequest) (*View, error) {
	var out *View
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.Name != "" {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, req.Name)
			pos++
		}
		if req.EntityKind != "" {
			sets = append(sets, fmt.Sprintf("entity_kind = $%d", pos))
			args = append(args, req.EntityKind)
			pos++
		}
		if req.FilterSpec != nil {
			sets = append(sets, fmt.Sprintf("filter_spec = $%d", pos))
			args = append(args, req.FilterSpec)
			pos++
		}
		if req.Sort != nil {
			sortJSON, merr := json.Marshal(req.Sort)
			if merr != nil {
				return merr
			}
			sets = append(sets, fmt.Sprintf("sort = $%d::jsonb", pos))
			args = append(args, sortJSON)
			pos++
		}
		if req.Columns != nil {
			sets = append(sets, fmt.Sprintf("columns = $%d", pos))
			args = append(args, req.Columns)
			pos++
		}
		if req.SharedWithRoles != nil {
			sets = append(sets, fmt.Sprintf("shared_with_roles = $%d::uuid[]", pos))
			args = append(args, req.SharedWithRoles)
			pos++
		}
		sets = append(sets, fmt.Sprintf("shared = $%d", pos))
		args = append(args, req.Shared)
		pos++
		v, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE saved_view SET %s WHERE id = $1 AND organization_id = $2 AND (shared OR owner_id = $%d) RETURNING %s",
			strings.Join(sets, ", "), pos, selectColumns), append(args, nilIfEmpty(ownerID))...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("update saved view: %w", err)
		}
		out = v
		return nil
	})
	return out, err
}

// Delete removes a view the caller owns (or any shared view).
func (r *PGRepository) Delete(ctx context.Context, orgID, id, ownerID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM saved_view WHERE id = $1 AND organization_id = $2 AND (shared OR owner_id = $3)",
			id, orgID, nilIfEmpty(ownerID))
		if err != nil {
			return fmt.Errorf("delete saved view: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*View, error) {
	v := &View{}
	var spec, sortRaw []byte
	var createdAt, updatedAt time.Time
	if err := s.Scan(
		&v.ID, &v.OrganizationID, &v.OwnerID, &v.Name, &v.EntityKind,
		&spec, &sortRaw, &v.Columns, &v.Shared, &v.SharedWithRoles, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	if len(sortRaw) > 0 {
		_ = json.Unmarshal(sortRaw, &v.Sort)
	}
	normalizeView(v)
	v.FilterSpec = map[string]any{}
	if len(spec) > 0 {
		_ = json.Unmarshal(spec, &v.FilterSpec)
	}
	v.CreatedAt = createdAt.UTC()
	v.UpdatedAt = updatedAt.UTC()
	return v, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// normalizeView gives a view empty lists instead of nil (JSON arrays).
func normalizeView(v *View) {
	if v.Sort == nil {
		v.Sort = []SortKey{}
	}
	if v.Columns == nil {
		v.Columns = []string{}
	}
	if v.SharedWithRoles == nil {
		v.SharedWithRoles = []string{}
	}
}
