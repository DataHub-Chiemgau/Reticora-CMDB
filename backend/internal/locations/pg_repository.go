package locations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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
			field := "parent_id"
			if req.Kind == KindSite {
				field = "client_id"
			}
			return withField(field, mapError(fmt.Errorf("create %s: %w", req.Kind, err)))
		}
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		return recordAudit(ctx, tx, "location.created", out, map[string]any{"after": out})
	})
	return out, err
}

var _ Repository = (*PGRepository)(nil)

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
	return r.Update(ctx, orgID, id, UpdateRequest{ParentID: &parentID})
}

// Update renames and/or moves a node in one transaction. Specialist kinds
// are changed through their table, whose trigger keeps the location row in
// step.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Location, error) {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return nil, fieldError("name", ErrInvalidInput, "name must not be empty")
	}
	if req.ParentID != nil && *req.ParentID == "" {
		return nil, fieldError("parent_id", ErrInvalidParent, "a node can only be moved below another node")
	}
	var out *Location
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		table := "location"
		if cur.Kind.HasSpecialistTable() {
			table = string(cur.Kind)
		}
		if req.Name != nil && *req.Name != cur.Name {
			if err = exec(ctx, tx, fmt.Sprintf(`UPDATE %s SET name = $2 WHERE id = $1`, table), id, *req.Name); err != nil {
				return withField("name", mapError(fmt.Errorf("rename location: %w", err)))
			}
			if auditErr := recordAudit(ctx, tx, "location.renamed", cur, map[string]any{"name": map[string]any{"old": cur.Name, "new": *req.Name}}); auditErr != nil {
				return auditErr
			}
		}
		moved := req.ParentID != nil && *req.ParentID != cur.ParentID
		if moved {
			column := "parent_id"
			switch {
			case cur.Kind == KindSite:
				return fieldError("parent_id", ErrInvalidParent, "a site is a root and cannot be moved")
			case cur.Kind.HasSpecialistTable():
				column = specialistParentColumns[cur.Kind]
			}
			if err = exec(ctx, tx, fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE id = $1`, table, column), id, *req.ParentID); err != nil {
				return withField("parent_id", mapError(fmt.Errorf("move location: %w", err)))
			}
		}
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		if moved {
			// The database rewrote the paths of the subtree and recorded the
			// move in location_change (migration 000075).
			return recordAudit(ctx, tx, "location.moved", out, map[string]any{
				"parent_id": map[string]any{"old": cur.ParentID, "new": out.ParentID},
				"path":      map[string]any{"old": cur.Path, "new": out.Path},
			})
		}
		return nil
	})
	return out, err
}

// List returns the visible nodes, parents first.
func (r *PGRepository) List(ctx context.Context, orgID string, filter Filter) ([]Location, error) {
	where := []string{"TRUE"}
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if filter.RootOnly {
		where = append(where, "parent_id IS NULL")
	}
	if filter.ParentID != "" {
		add("parent_id::text = $%d", filter.ParentID)
	}
	if filter.Kind != "" {
		add("kind = $%d", string(filter.Kind))
	}
	if filter.Search != "" {
		add("name ILIKE $%d", "%"+filter.Search+"%")
	}
	out := []Location{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+selectColumns+` FROM location WHERE `+strings.Join(where, " AND ")+`
			ORDER BY nlevel(path), name, id`, args...)
		if err != nil {
			return fmt.Errorf("list locations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			loc, scanErr := scan(rows)
			if scanErr != nil {
				return fmt.Errorf("scan location: %w", scanErr)
			}
			out = append(out, *loc)
		}
		return rows.Err()
	})
	return out, err
}

// Delete removes a leaf node; a node with children is refused with
// ErrHasChildren and one still referenced elsewhere with ErrInUse. The
// specialist row goes with the location row (ON DELETE CASCADE).
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if depErr := CheckDeletable(ctx, tx, id, false); depErr != nil {
			return depErr
		}
		if _, err = tx.Exec(ctx, `DELETE FROM location WHERE id = $1`, id); err != nil {
			if mapped := DeleteError(err); mapped != err {
				return mapped
			}
			return fmt.Errorf("delete location: %w", err)
		}
		return recordAudit(ctx, tx, "location.deleted", cur, map[string]any{"before": cur})
	})
}

func exec(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func get(ctx context.Context, tx pgx.Tx, id string) (*Location, error) {
	loc, err := scan(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM location WHERE id = $1`, id))
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02") {
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
		pgErr.ConstraintName == "location_root_is_site":
		return fmt.Errorf("%w: %s", ErrInvalidParent, pgErr.Message)
	case pgErr.Code == "23503", // parent missing or invisible
		pgErr.Code == "42501": // row-level security
		// No detail: it would tell a foreign parent from a missing one.
		return fmt.Errorf("%w: not found", ErrInvalidParent)
	case pgErr.Code == "22P02": // malformed uuid
		return fmt.Errorf("%w: malformed id", ErrInvalidInput)
	}
	return err
}

// recordAudit writes the audit entry of a location change in the same
// transaction (DOD-01).
func recordAudit(ctx context.Context, tx pgx.Tx, action string, loc *Location, changes map[string]any) error {
	actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	actorType := "system"
	if actor != "" {
		actorType = "user"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: loc.OrganizationID,
		ActorID:        actor,
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "location",
		ResourceID:     loc.ID,
		Changes:        changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}
