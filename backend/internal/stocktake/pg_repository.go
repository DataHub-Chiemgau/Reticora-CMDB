package stocktake

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

const stocktakeSelectColumns = `
	id::text,
	organization_id::text,
	title,
	COALESCE(description, ''),
	status,
	scope,
	COALESCE(started_by::text, ''),
	started_at,
	completed_at,
	due_date,
	COALESCE(total_expected, 0),
	COALESCE(total_scanned, 0),
	COALESCE(total_missing, 0),
	COALESCE(total_surplus, 0),
	created_at,
	updated_at
`

const stockScanSelectColumns = `
	id::text,
	organization_id::text,
	stocktake_id::text,
	COALESCE(asset_id::text, ''),
	COALESCE(ci_id::text, ''),
	scanned_by::text,
	scan_method,
	scan_result,
	COALESCE(location_found, ''),
	COALESCE(notes, ''),
	scanned_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed stocktake repository.
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

// List returns paginated stocktakes filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Stocktake, int, error) {
	var stocktakes []Stocktake
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
		if filter.Scope != "" {
			whereParts = append(whereParts, fmt.Sprintf("scope = $%d", argPos))
			args = append(args, filter.Scope)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("title ILIKE $%d", argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stocktake WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count stocktakes: %w", err)
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
				"SELECT %s FROM stocktake WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				stocktakeSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM stocktake WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				stocktakeSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list stocktakes: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanStocktake(rows)
			if err != nil {
				return fmt.Errorf("scan stocktake: %w", err)
			}
			stocktakes = append(stocktakes, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate stocktakes: %w", err)
		}
		return nil
	})

	return stocktakes, total, err
}

// GetByID retrieves a single stocktake by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Stocktake, error) {
	var stocktake *Stocktake

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM stocktake WHERE id = $1", stocktakeSelectColumns)
		var err error
		stocktake, err = scanStocktake(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get stocktake by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stocktake, nil
}

// Create inserts a new stocktake.
func (r *PGRepository) Create(ctx context.Context, s *Stocktake) error {
	return r.withTenant(ctx, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO stocktake (
				organization_id, title, description, status, scope, started_by, started_at,
				completed_at, due_date, total_expected, total_scanned, total_missing, total_surplus
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13
			)
			RETURNING ` + stocktakeSelectColumns
		created, err := scanStocktake(tx.QueryRow(ctx, query,
			s.OrganizationID,
			s.Title,
			nilIfEmpty(s.Description),
			s.Status,
			s.Scope,
			nilIfEmpty(s.StartedBy),
			nilIfEmpty(s.StartedAt),
			nilIfEmpty(s.CompletedAt),
			nilIfEmpty(s.DueDate),
			s.TotalExpected,
			s.TotalScanned,
			s.TotalMissing,
			s.TotalSurplus,
		))
		if err != nil {
			return fmt.Errorf("create stocktake: %w", err)
		}
		*s = *created
		return nil
	})
}

// Update modifies an existing stocktake.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error) {
	var stocktake *Stocktake

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 7)
		args := []any{id}
		argPos := 2

		if req.Title != nil {
			setClauses = append(setClauses, fmt.Sprintf("title = $%d", argPos))
			args = append(args, *req.Title)
			argPos++
		}
		if req.Description != nil {
			setClauses = append(setClauses, fmt.Sprintf("description = $%d", argPos))
			args = append(args, nilIfEmpty(*req.Description))
			argPos++
		}
		if req.Status != nil {
			setClauses = append(setClauses, fmt.Sprintf("status = $%d", argPos))
			args = append(args, *req.Status)
			argPos++
			if *req.Status == "in_progress" {
				setClauses = append(setClauses, "started_at = COALESCE(started_at, NOW())")
			}
			if *req.Status == "completed" {
				setClauses = append(setClauses, "completed_at = NOW()")
			}
		}
		if req.DueDate != nil {
			setClauses = append(setClauses, fmt.Sprintf("due_date = $%d", argPos))
			args = append(args, nilIfEmpty(*req.DueDate))
			argPos++
		}
		if req.TotalExpected != nil {
			setClauses = append(setClauses, fmt.Sprintf("total_expected = $%d", argPos))
			args = append(args, *req.TotalExpected)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM stocktake WHERE id = $1", stocktakeSelectColumns)
			var err error
			stocktake, err = scanStocktake(tx.QueryRow(ctx, query, id))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return fmt.Errorf("get stocktake for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf("UPDATE stocktake SET %s WHERE id = $1 RETURNING %s", strings.Join(setClauses, ", "), stocktakeSelectColumns)
		var err error
		stocktake, err = scanStocktake(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update stocktake: %w", err)
		}
		return nil
	})

	return stocktake, err
}

// Delete deletes a stocktake. The stocktake table has no deleted_at column, so this is a hard delete.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM stocktake WHERE id = $1", id)
		if err != nil {
			return fmt.Errorf("delete stocktake: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// AddScan records a stocktake scan and updates stocktake counters.
func (r *PGRepository) AddScan(ctx context.Context, scan *StockScan) error {
	return r.withTenant(ctx, scan.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stocktake WHERE id = $1)", scan.StocktakeID).Scan(&exists); err != nil {
			return fmt.Errorf("check stocktake: %w", err)
		}
		if !exists {
			return fmt.Errorf("not found")
		}

		query := `
			INSERT INTO stock_scan (
				organization_id, stocktake_id, asset_id, ci_id, scanned_by,
				scan_method, scan_result, location_found, notes, scanned_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, COALESCE($10::timestamptz, NOW())
			)
			RETURNING ` + stockScanSelectColumns
		created, err := scanStockScan(tx.QueryRow(ctx, query,
			scan.OrganizationID,
			scan.StocktakeID,
			nilIfEmpty(scan.AssetID),
			nilIfEmpty(scan.CIID),
			scan.ScannedBy,
			scan.ScanMethod,
			scan.ScanResult,
			nilIfEmpty(scan.LocationFound),
			nilIfEmpty(scan.Notes),
			nilIfZeroTime(scan.ScannedAt),
		))
		if err != nil {
			return fmt.Errorf("add stock scan: %w", err)
		}
		*scan = *created

		setClauses := []string{"total_scanned = total_scanned + 1", "updated_at = NOW()"}
		switch scan.ScanResult {
		case "missing":
			setClauses = append(setClauses, "total_missing = total_missing + 1")
		case "surplus":
			setClauses = append(setClauses, "total_surplus = total_surplus + 1")
		}
		if _, err := tx.Exec(ctx, "UPDATE stocktake SET "+strings.Join(setClauses, ", ")+" WHERE id = $1", scan.StocktakeID); err != nil {
			return fmt.Errorf("update stocktake counters: %w", err)
		}
		return nil
	})
}

// ListScans returns paginated scans for a stocktake.
func (r *PGRepository) ListScans(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error) {
	var scans []StockScan
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_scan WHERE stocktake_id = $1", stocktakeID).Scan(&total); err != nil {
			return fmt.Errorf("count stock scans: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM stock_scan WHERE stocktake_id = $1 ORDER BY scanned_at DESC LIMIT $2 OFFSET $3", stockScanSelectColumns)
		rows, err := tx.Query(ctx, query, stocktakeID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list stock scans: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			scan, err := scanStockScan(rows)
			if err != nil {
				return fmt.Errorf("scan stock scan: %w", err)
			}
			scans = append(scans, *scan)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate stock scans: %w", err)
		}
		return nil
	})

	return scans, total, err
}

type stockScanner interface {
	Scan(dest ...any) error
}

func scanStocktake(scanner stockScanner) (*Stocktake, error) {
	item := &Stocktake{}
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var dueDate sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Title,
		&item.Description,
		&item.Status,
		&item.Scope,
		&item.StartedBy,
		&startedAt,
		&completedAt,
		&dueDate,
		&item.TotalExpected,
		&item.TotalScanned,
		&item.TotalMissing,
		&item.TotalSurplus,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if startedAt.Valid {
		item.StartedAt = startedAt.Time.UTC().Format(time.RFC3339)
	}
	if completedAt.Valid {
		item.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339)
	}
	if dueDate.Valid {
		item.DueDate = dueDate.Time.UTC().Format("2006-01-02")
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

func scanStockScan(scanner stockScanner) (*StockScan, error) {
	item := &StockScan{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.StocktakeID,
		&item.AssetID,
		&item.CIID,
		&item.ScannedBy,
		&item.ScanMethod,
		&item.ScanResult,
		&item.LocationFound,
		&item.Notes,
		&item.ScannedAt,
	); err != nil {
		return nil, err
	}
	item.ScannedAt = item.ScannedAt.UTC()
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
