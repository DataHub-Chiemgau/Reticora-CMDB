package composition

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, parent_asset_id::text,
	COALESCE(child_ci_id::text, ''), COALESCE(child_asset_id::text, ''),
	COALESCE(role, ''), COALESCE(position, ''),
	configuration_only, independently_serialized, independently_assignable,
	independently_locatable, independently_lifecycle_managed,
	created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed composition repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// compositionVisible restricts composition rows to those whose parent and
// child are visible under the transaction's tenant scope: the asset and ci
// policies filter the subqueries, while composition carries no client column
// yet (WP-025).
const compositionVisible = `(composition.parent_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = composition.parent_asset_id))
	AND (composition.child_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = composition.child_ci_id))
	AND (composition.child_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = composition.child_asset_id))`

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return fmt.Errorf("not found")
	}
	return nil
}

// List returns composition links, optionally filtered by parent asset.
func (r *PGRepository) List(ctx context.Context, orgID, parentAssetID string, page api.PaginationParams) ([]Composition, int, error) {
	var out []Composition
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if parentAssetID != "" {
			where += " AND parent_asset_id = $2"
			args = append(args, parentAssetID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM composition WHERE "+compositionVisible+" AND "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count compositions: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM composition WHERE "+compositionVisible+" AND %s ORDER BY created_at ASC LIMIT $%d OFFSET $%d",
			selectColumns, where, len(args)-1, len(args)), args...)
		if err != nil {
			return fmt.Errorf("list compositions: %w", err)
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

// GetByID returns one composition link.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Composition, error) {
	var out *Composition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM composition WHERE "+compositionVisible+" AND id = $1 AND organization_id = $2",
			selectColumns), id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get composition: %w", err)
		}
		out = c
		return nil
	})
	return out, err
}

// Create inserts a composition link.
func (r *PGRepository) Create(ctx context.Context, c *Composition) error {
	if (c.ChildCIID == "") == (c.ChildAssetID == "") {
		return fmt.Errorf("exactly one of child_ci_id or child_asset_id is required")
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO composition (
				organization_id, parent_asset_id, child_ci_id, child_asset_id,
				role, position, configuration_only, independently_serialized,
				independently_assignable, independently_locatable,
				independently_lifecycle_managed
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING %s`, selectColumns),
			c.OrganizationID, c.ParentAssetID, nilIfEmpty(c.ChildCIID),
			nilIfEmpty(c.ChildAssetID), nilIfEmpty(c.Role), nilIfEmpty(c.Position),
			c.ConfigurationOnly, c.IndependentlySerialized, c.IndependentlyAssignable,
			c.IndependentlyLocatable, c.IndependentlyLifecycleManaged)
		scanned, err := scan(row)
		if err != nil {
			if strings.Contains(err.Error(), "idx_composition_unique") {
				return fmt.Errorf("child already has a parent asset")
			}
			if msg, ok := cycleMessage(err); ok {
				return fmt.Errorf("%s", msg)
			}
			return fmt.Errorf("create composition: %w", err)
		}
		*c = *scanned
		return requireVisible(ctx, tx, "composition", compositionVisible, c.ID)
	})
}

// Update modifies the role/position/independence flags of a link.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Composition, error) {
	var out *Composition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.Role != nil {
			sets = append(sets, fmt.Sprintf("role = $%d", pos))
			args = append(args, nilIfEmpty(*req.Role))
			pos++
		}
		if req.Position != nil {
			sets = append(sets, fmt.Sprintf("position = $%d", pos))
			args = append(args, nilIfEmpty(*req.Position))
			pos++
		}
		if req.ConfigurationOnly != nil {
			sets = append(sets, fmt.Sprintf("configuration_only = $%d", pos))
			args = append(args, *req.ConfigurationOnly)
			pos++
		}
		if req.IndependentlySerialized != nil {
			sets = append(sets, fmt.Sprintf("independently_serialized = $%d", pos))
			args = append(args, *req.IndependentlySerialized)
			pos++
		}
		if req.IndependentlyAssignable != nil {
			sets = append(sets, fmt.Sprintf("independently_assignable = $%d", pos))
			args = append(args, *req.IndependentlyAssignable)
			pos++
		}
		if req.IndependentlyLocatable != nil {
			sets = append(sets, fmt.Sprintf("independently_locatable = $%d", pos))
			args = append(args, *req.IndependentlyLocatable)
			pos++
		}
		if req.IndependentlyLifecycleManaged != nil {
			sets = append(sets, fmt.Sprintf("independently_lifecycle_managed = $%d", pos))
			args = append(args, *req.IndependentlyLifecycleManaged)
			pos++
		}
		if len(sets) == 0 {
			c, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
				"SELECT %s FROM composition WHERE "+compositionVisible+" AND id = $1 AND organization_id = $2",
				selectColumns), id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return err
			}
			out = c
			return nil
		}
		c, err := scan(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE composition SET %s WHERE "+compositionVisible+" AND id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(sets, ", "), selectColumns), args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update composition: %w", err)
		}
		out = c
		return requireVisible(ctx, tx, "composition", compositionVisible, id)
	})
	return out, err
}

// Delete removes a composition link.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM composition WHERE "+compositionVisible+" AND id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete composition: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// ParentOf returns the composition a child belongs to, if any.
func (r *PGRepository) ParentOf(ctx context.Context, orgID, childCIID, childAssetID string) (*Composition, error) {
	var out *Composition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var query string
		var arg any
		switch {
		case childCIID != "":
			query = fmt.Sprintf("SELECT %s FROM composition WHERE "+compositionVisible+" AND child_ci_id = $1 AND organization_id = $2", selectColumns)
			arg = childCIID
		case childAssetID != "":
			query = fmt.Sprintf("SELECT %s FROM composition WHERE "+compositionVisible+" AND child_asset_id = $1 AND organization_id = $2", selectColumns)
			arg = childAssetID
		default:
			return nil
		}
		c, err := scan(tx.QueryRow(ctx, query, arg, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return fmt.Errorf("lookup parent composition: %w", err)
		}
		out = c
		return nil
	})
	return out, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Composition, error) {
	c := &Composition{}
	if err := s.Scan(
		&c.ID, &c.OrganizationID, &c.ParentAssetID, &c.ChildCIID, &c.ChildAssetID,
		&c.Role, &c.Position, &c.ConfigurationOnly, &c.IndependentlySerialized,
		&c.IndependentlyAssignable, &c.IndependentlyLocatable,
		&c.IndependentlyLifecycleManaged, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return c, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// cycleMessage extracts the guard message raised by the trg_composition_no_cycle
// trigger so the API can report a precise, non-leaking conflict instead of a
// generic internal error.
func cycleMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Message, "composition cycle") {
		return pgErr.Message, true
	}
	if strings.Contains(err.Error(), "composition cycle") {
		return "composition cycle: the requested link would make an asset its own ancestor", true
	}
	return "", false
}
