package assignment

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
	checkout_signature,
	return_signature,
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

// assignmentVisible restricts assignments to those whose asset and CI are
// visible under the transaction's tenant scope: the asset and ci policies
// filter the subqueries, while assignment carries no client column yet
// (WP-025).
const assignmentVisible = `(assignment.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = assignment.asset_id))
	AND (assignment.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = assignment.ci_id))`

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

// List returns paginated assignments filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Assignment, int, error) {
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
			whereParts = append(whereParts, fmt.Sprintf("assigned_to = $%d", argPos))
			args = append(args, filter.AssignedTo)
			argPos++
		}
		if filter.AssignedBy != "" {
			whereParts = append(whereParts, fmt.Sprintf("assigned_by = $%d", argPos))
			args = append(args, filter.AssignedBy)
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
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM assignment WHERE "+assignmentVisible+" AND "+whereClause, args...).Scan(&total); err != nil {
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
				"SELECT %s FROM assignment WHERE "+assignmentVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d",
				assignmentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM assignment WHERE "+assignmentVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
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
		items := make([]*Assignment, len(assignments))
		for i := range assignments {
			items[i] = &assignments[i]
		}
		return reveal(ctx, tx, orgID, items...)
	})

	return assignments, total, err
}

// GetByID retrieves a single assignment by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Assignment, error) {
	var assignment *Assignment

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM assignment WHERE "+assignmentVisible+" AND id = $1", assignmentSelectColumns)
		var err error
		assignment, err = scanAssignment(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get assignment by id: %w", err)
		}
		return reveal(ctx, tx, orgID, assignment)
	})
	if err != nil {
		return nil, err
	}
	return assignment, nil
}

// Create inserts a new assignment.
func (r *PGRepository) Create(ctx context.Context, a *Assignment) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		// The signer name goes into the vault; the table keeps its
		// surrogate (SEC-05).
		checkoutSig, err := sealSignature(ctx, tx, a.OrganizationID, a.CheckoutSignature)
		if err != nil {
			return err
		}
		query := `
			INSERT INTO assignment (
				organization_id, asset_id, ci_id, assigned_to, assigned_by, assignment_type,
				status, assigned_at, due_date, returned_at, return_condition, notes, checkout_signature
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, COALESCE($8::timestamptz, NOW()), $9, $10, $11, $12, $13::jsonb
			)
			RETURNING id::text, assigned_at, created_at, updated_at
		`
		if err = tx.QueryRow(ctx, query,
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
			checkoutSig,
		).Scan(&a.ID, &a.AssignedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return fmt.Errorf("create assignment: %w", err)
		}
		a.AssignedAt = a.AssignedAt.UTC()
		a.CreatedAt = a.CreatedAt.UTC()
		a.UpdatedAt = a.UpdatedAt.UTC()
		return requireVisible(ctx, tx, "assignment", assignmentVisible, a.ID)
	})
}

// Update replaces mutable fields on an existing assignment.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, a *Assignment) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		returnSig, replaced, err := sealReturnSignature(ctx, tx, orgID, id, a.ReturnSignature)
		if err != nil {
			return err
		}
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
				return_signature = COALESCE($13::jsonb, return_signature),
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
			returnSig,
		))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update assignment: %w", err)
		}
		if serr := shred(ctx, tx, orgID, replaced); serr != nil {
			return serr
		}
		if rerr := reveal(ctx, tx, orgID, updated); rerr != nil {
			return rerr
		}
		*a = *updated
		return requireVisible(ctx, tx, "assignment", assignmentVisible, id)
	})
}

// sealReturnSignature returns the stored form of the return signature an
// update writes: nil keeps the stored one, which is the case when sig is
// none or equals it (a transfer or a repeated return carries the read
// signature along). A new signature replaces the stored one, whose signer
// subject (replaced) the caller deletes.
func sealReturnSignature(ctx context.Context, tx pgx.Tx, orgID, id string, sig *Signature) (stored any, replaced string, err error) {
	if sig == nil {
		return nil, "", nil
	}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT return_signature FROM assignment WHERE "+assignmentVisible+" AND id = $1 FOR UPDATE", id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, "", fmt.Errorf("not found")
	}
	if err != nil {
		return nil, "", fmt.Errorf("read return signature: %w", err)
	}
	current := &Assignment{}
	current.ReturnSignature, current.returnSigner = decodeSignature(raw)
	if rerr := reveal(ctx, tx, orgID, current); rerr != nil {
		return nil, "", rerr
	}
	if current.ReturnSignature != nil && *current.ReturnSignature == *sig {
		return nil, "", nil
	}
	if stored, err = sealSignature(ctx, tx, orgID, sig); err != nil {
		return nil, "", err
	}
	return stored, current.returnSigner, nil
}

// Delete deletes an assignment. The assignment table has no deleted_at column, so this is a hard delete.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The signer names of the deleted assignment are deleted with it
		// (crypto-shredding).
		var checkoutSig, returnSig []byte
		err := tx.QueryRow(ctx, "DELETE FROM assignment WHERE "+assignmentVisible+" AND id = $1 RETURNING checkout_signature, return_signature", id).
			Scan(&checkoutSig, &returnSig)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("delete assignment: %w", err)
		}
		_, checkoutSigner := decodeSignature(checkoutSig)
		_, returnSigner := decodeSignature(returnSig)
		return shred(ctx, tx, orgID, checkoutSigner, returnSigner)
	})
}

type assignmentScanner interface {
	Scan(dest ...any) error
}

func scanAssignment(scanner assignmentScanner) (*Assignment, error) {
	item := &Assignment{}
	var dueDate sql.NullTime
	var returnedAt sql.NullTime
	var checkoutSig, returnSig []byte
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
		&checkoutSig,
		&returnSig,
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
	item.CheckoutSignature, item.checkoutSigner = decodeSignature(checkoutSig)
	item.ReturnSignature, item.returnSigner = decodeSignature(returnSig)
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
