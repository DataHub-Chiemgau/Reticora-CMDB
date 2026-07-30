package assignment

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const assignmentSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(asset_id::text, ''),
	COALESCE(ci_id::text, ''),
	assigned_to::text,
	assigned_by::text,
	assignment_type,
	status,
	assigned_at,
	due_date,
	returned_at,
	COALESCE(return_condition, ''),
	COALESCE(notes, ''),
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed assignment repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// withTenant executes fn within a transaction that has the tenant setting set for RLS.
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set legacy tenant context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// List returns paginated assignments filtered by the given parameters.
func (r *PGRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
	ctx := context.Background()
	var assignments []Assignment
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"true"}
		args := make([]any, 0, 5)
		argPos := 1

		if filter.Status != "" {
			whereParts = append(whereParts, fmt.Sprintf("status = $%d", argPos))
			args = append(args, filter.Status)
			argPos++
		}
		if filter.AssignedTo != "" {
			whereParts = append(whereParts, fmt.Sprintf("assigned_to = $%d", argPos))
			args = append(args, filter.AssignedTo)
			argPos++
		}
		if filter.AssetID != "" {
			whereParts = append(whereParts, fmt.Sprintf("asset_id = $%d", argPos))
			args = append(args, filter.AssetID)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("notes ILIKE $%d", argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM assignment WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count assignments: %w", err)
		}

		sortColumn, sortDirection := NormalizeSort(filter)

		// Keyset pagination: when a cursor is supplied it replaces the OFFSET
		// so page boundaries stay stable while rows are inserted or removed.
		listWhere := whereClause
		listArgs := append([]any{}, args...)
		if page.Cursor != nil {
			if !page.Cursor.Matches(sortColumn, sortDirection) {
				return fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
			}
			listWhere += " AND " + api.KeysetClause(sortColumn, sortColumnCasts[sortColumn], sortDirection, argPos)
			listArgs = append(listArgs, page.Cursor.Value, page.Cursor.ID)
			argPos += 2
		}

		var query string
		if page.Cursor != nil {
			listArgs = append(listArgs, page.Limit)
			query = fmt.Sprintf(
				"SELECT %s FROM assignment WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				assignmentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM assignment WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				assignmentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list assignments: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanAssignment(rows)
			if err != nil {
				return fmt.Errorf("scan assignment: %w", err)
			}
			assignments = append(assignments, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate assignments: %w", err)
		}
		return nil
	})

	return assignments, total, err
}

// GetByID retrieves a single assignment by ID within the tenant scope.
func (r *PGRepository) GetByID(orgID, id string) (*Assignment, error) {
	ctx := context.Background()
	var assignment *Assignment

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM assignment WHERE id = $1", assignmentSelectColumns)
		var err error
		assignment, err = scanAssignment(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get assignment by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assignment, nil
}

// Create inserts a new assignment.
func (r *PGRepository) Create(a *Assignment) error {
	ctx := context.Background()
	return r.withTenant(ctx, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO assignment (
				organization_id, asset_id, ci_id, assigned_to, assigned_by, assignment_type,
				status, assigned_at, due_date, returned_at, return_condition, notes
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, COALESCE($8::timestamptz, NOW()), $9, $10, $11, $12
			)
			RETURNING id::text, assigned_at, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			a.OrganizationID,
			nilIfEmpty(a.AssetID),
			nilIfEmpty(a.CIID),
			a.AssignedTo,
			a.AssignedBy,
			a.AssignmentType,
			a.Status,
			nilIfZeroTime(a.AssignedAt),
			nilIfEmpty(a.DueDate),
			nilIfEmpty(a.ReturnedAt),
			nilIfEmpty(a.ReturnCondition),
			nilIfEmpty(a.Notes),
		).Scan(&a.ID, &a.AssignedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return fmt.Errorf("create assignment: %w", err)
		}
		a.AssignedAt = a.AssignedAt.UTC()
		a.CreatedAt = a.CreatedAt.UTC()
		a.UpdatedAt = a.UpdatedAt.UTC()
		return nil
	})
}

// Update replaces mutable fields on an existing assignment.
func (r *PGRepository) Update(orgID, id string, a *Assignment) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			UPDATE assignment SET
				asset_id = $2,
				ci_id = $3,
				assigned_to = $4,
				assigned_by = $5,
				assignment_type = $6,
				status = $7,
				assigned_at = $8,
				due_date = $9,
				returned_at = $10,
				return_condition = $11,
				notes = $12,
				updated_at = NOW()
			WHERE id = $1
			RETURNING ` + assignmentSelectColumns
		updated, err := scanAssignment(tx.QueryRow(ctx, query,
			id,
			nilIfEmpty(a.AssetID),
			nilIfEmpty(a.CIID),
			a.AssignedTo,
			a.AssignedBy,
			a.AssignmentType,
			a.Status,
			a.AssignedAt,
			nilIfEmpty(a.DueDate),
			nilIfEmpty(a.ReturnedAt),
			nilIfEmpty(a.ReturnCondition),
			nilIfEmpty(a.Notes),
		))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update assignment: %w", err)
		}
		*a = *updated
		return nil
	})
}

// Delete deletes an assignment. The assignment table has no deleted_at column, so this is a hard delete.
func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM assignment WHERE id = $1", id)
		if err != nil {
			return fmt.Errorf("delete assignment: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

type assignmentScanner interface {
	Scan(dest ...any) error
}

func scanAssignment(scanner assignmentScanner) (*Assignment, error) {
	item := &Assignment{}
	var dueDate sql.NullTime
	var returnedAt sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.AssetID,
		&item.CIID,
		&item.AssignedTo,
		&item.AssignedBy,
		&item.AssignmentType,
		&item.Status,
		&item.AssignedAt,
		&dueDate,
		&returnedAt,
		&item.ReturnCondition,
		&item.Notes,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if dueDate.Valid {
		item.DueDate = dueDate.Time.UTC().Format("2006-01-02")
	}
	if returnedAt.Valid {
		item.ReturnedAt = returnedAt.Time.UTC().Format(time.RFC3339)
	}
	item.AssignedAt = item.AssignedAt.UTC()
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nilIfZeroTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
