package assignment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// assignmentSelectColumns are the columns of the projection
// assignment_projection (000257) in the order of scanAssignment.
const assignmentSelectColumns = `
	id::text,
	organization_id::text,
	asset_id::text,
	COALESCE(assigned_to::text, ''),
	COALESCE(assigned_team_id::text, ''),
	COALESCE(assigned_by::text, ''),
	assignment_type,
	status,
	assigned_at,
	due_at,
	due_date,
	returned_at,
	COALESCE(return_condition, ''),
	COALESCE(notes, ''),
	created_at,
	updated_at
`

// PGRepository reads the assignments from the projection
// assignment_projection over the ledger stock_movement (AST-02, AST-07,
// WP-747): one row per assignment movement, its return the first return
// movement of the same asset and holder after it. The view runs with
// security_invoker, so the row level security of stock_movement (client
// and site scope of the booking) applies to the caller. The assignments
// are written by the assignment service (Service) only.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed assignment repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns paginated assignments filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
	var assignments []Assignment
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"true"}
		args := make([]any, 0, 5)
		argPos := 1

		if filter.Status != "" {
			whereParts = append(whereParts, fmt.Sprintf("status = $%d", argPos))
			args = append(args, filter.Status)
			argPos++
		}
		if filter.AssignedTo != "" {
			// A value that is no UUID matches no row (uuid column).
			if !api.IsUUID(filter.AssignedTo) {
				whereParts = append(whereParts, "false")
			} else {
				whereParts = append(whereParts, fmt.Sprintf("assigned_to = $%d", argPos))
				args = append(args, filter.AssignedTo)
				argPos++
			}
		}
		if filter.AssignedBy != "" {
			// A value that is no UUID matches no row (uuid column).
			if !api.IsUUID(filter.AssignedBy) {
				whereParts = append(whereParts, "false")
			} else {
				whereParts = append(whereParts, fmt.Sprintf("assigned_by = $%d", argPos))
				args = append(args, filter.AssignedBy)
				argPos++
			}
		}
		if filter.AssetID != "" {
			// A value that is no UUID matches no row (uuid column).
			if !api.IsUUID(filter.AssetID) {
				whereParts = append(whereParts, "false")
			} else {
				whereParts = append(whereParts, fmt.Sprintf("asset_id = $%d", argPos))
				args = append(args, filter.AssetID)
				argPos++
			}
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("notes ILIKE $%d", argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM assignment_projection WHERE "+whereClause, args...).Scan(&total); err != nil {
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
				"SELECT %s FROM assignment_projection WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				assignmentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM assignment_projection WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
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
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Assignment, error) {
	var assignment *Assignment

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if !api.IsUUID(id) {
			return fmt.Errorf("not found")
		}
		query := fmt.Sprintf("SELECT %s FROM assignment_projection WHERE id = $1", assignmentSelectColumns)
		var err error
		assignment, err = scanAssignment(tx.QueryRow(ctx, query, id))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
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

type assignmentScanner interface {
	Scan(dest ...any) error
}

func scanAssignment(scanner assignmentScanner) (*Assignment, error) {
	item := &Assignment{}
	var dueDate, returnedAt sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.AssetID,
		&item.AssignedTo,
		&item.AssignedTeamID,
		&item.AssignedBy,
		&item.AssignmentType,
		&item.Status,
		&item.AssignedAt,
		&item.DueAt,
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
		item.DueDate = dueDate.Time.Format(time.DateOnly)
	}
	if returnedAt.Valid {
		item.ReturnedAt = returnedAt.Time.UTC().Format(time.RFC3339)
	}
	if item.DueAt != nil {
		due := item.DueAt.UTC()
		item.DueAt = &due
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
