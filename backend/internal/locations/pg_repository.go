package locations

import (
	"context"
	"errors"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, client_id::text, site_id::text,
	COALESCE(parent_id::text, ''), kind, name, path::text, created_at, updated_at`

// PGRepository reads and writes the location tree under RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed location tree repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// Create inserts a node. Sites, buildings, rooms and racks are inserted into
// their specialist table, whose trigger creates the location row.
func (r *PGRepository) Create(ctx context.Context, orgID string, req CreateRequest) (*Location, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out *Location
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		var err error
		switch req.Kind {
		case KindSite:
			err = tx.QueryRow(ctx,
				`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
				orgID, req.ClientID, req.Name).Scan(&id)
		case KindBuilding, KindRoom, KindRack:
			err = tx.QueryRow(ctx, fmt.Sprintf(
				`INSERT INTO %s (organization_id, %s, name) VALUES ($1, $2, $3) RETURNING id::text`,
				req.Kind, specialistParentColumns[req.Kind]),
				orgID, req.ParentID, req.Name).Scan(&id)
		default:
			err = tx.QueryRow(ctx,
				`INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				orgID, req.Kind, req.ParentID, req.Name).Scan(&id)
		}
		if err != nil {
			return mapError(fmt.Errorf("create %s: %w", req.Kind, err))
		}
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

// Get returns one node.
func (r *PGRepository) Get(ctx context.Context, orgID, id string) (*Location, error) {
	var out *Location
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

// Subtree returns the node and all visible nodes below it, parents first.
func (r *PGRepository) Subtree(ctx context.Context, orgID, id string) ([]Location, error) {
	var out []Location
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+selectColumns+` FROM location
			WHERE path <@ (SELECT path FROM location WHERE id = $1)
			ORDER BY nlevel(path), name, id`, id)
		if err != nil {
			return fmt.Errorf("location subtree: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			loc, scanErr := scan(rows)
			if scanErr != nil {
				return fmt.Errorf("scan location: %w", scanErr)
			}
			out = append(out, *loc)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("location subtree: %w", err)
		}
		if len(out) == 0 {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}

// Move re-parents a node; the database re-derives path, site and client of
// the node and its subtree. Specialist kinds move through their table.
func (r *PGRepository) Move(ctx context.Context, orgID, id, parentID string) (*Location, error) {
	if parentID == "" {
		return nil, ErrInvalidParent
	}
	var out *Location
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		var tag pgconn.CommandTag
		switch {
		case cur.Kind == KindSite:
			return ErrInvalidParent
		case cur.Kind.HasSpecialistTable():
			tag, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE id = $1`,
				cur.Kind, specialistParentColumns[cur.Kind]), id, parentID)
		default:
			tag, err = tx.Exec(ctx, `UPDATE location SET parent_id = $2 WHERE id = $1`, id, parentID)
		}
		if err != nil {
			return mapError(fmt.Errorf("move location: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

func get(ctx context.Context, tx pgx.Tx, id string) (*Location, error) {
	loc, err := scan(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM location WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get location: %w", err)
	}
	return loc, nil
}

func scan(row pgx.Row) (*Location, error) {
	var l Location
	if err := row.Scan(&l.ID, &l.OrganizationID, &l.ClientID, &l.SiteID, &l.ParentID,
		&l.Kind, &l.Name, &l.Path, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

// mapError translates the errors of the location triggers and policies into
// the repository errors. A parent outside the caller's scope is reported as
// an invalid parent, like one that does not exist.
func mapError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.ConstraintName == "location_no_cycle":
		return fmt.Errorf("%w: %s", ErrCycle, pgErr.Message)
	case pgErr.ConstraintName == "location_parent_matrix",
		pgErr.ConstraintName == "location_root_is_site",
		pgErr.Code == "23503", // parent missing or invisible
		pgErr.Code == "42501": // row-level security
		return fmt.Errorf("%w: %s", ErrInvalidParent, pgErr.Message)
	case pgErr.Code == "22P02": // malformed uuid
		return fmt.Errorf("%w: %s", ErrInvalidInput, pgErr.Message)
	}
	return err
}
