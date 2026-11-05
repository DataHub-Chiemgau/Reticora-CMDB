package stocktake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const stocktakeSelectColumns = `
	id::text,
	organization_id::text,
	title,
	COALESCE(description, ''),
	status,
	scope,
	COALESCE(location_id::text, ''),
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
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

// scanVisible restricts stocktake scans to those whose asset or CI is visible
// under the transaction's tenant scope: their policies filter the
// subqueries, while stock_scan carries no client column yet (WP-025).
const scanVisible = `(stock_scan.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = stock_scan.asset_id))
	AND (stock_scan.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = stock_scan.ci_id))`

// stocktakeWritable admits a stocktake for UPDATE and DELETE, its
// transitions and its completion (F-365, WP-848): a stocktake without area
// (location_id NULL, client_id and site_id NULL) is a row of the whole
// organization (migration 000012), so only an org-wide principal changes,
// starts, cancels, completes or deletes it (E-09). A principal restricted in
// the client or site scope records scans of its own assets (AddScan) but
// gets ErrNotFound for such a stocktake. A stocktake with area carries the
// client and site of its node; the restrictive policies
// stocktake_scoped_update and stocktake_scoped_delete (000260) admit the
// principals whose scope covers both.
const stocktakeWritable = "(location_id IS NOT NULL OR " + database.OrgWideScopeSQL + ")"

// itemSort is the sort of the keyset cursor of the snapshot items.
const itemSort = "id"

// areaFields maps the constraint of the area onto its request field.
var areaFields = map[string]string{"stocktake_location_fkey": "location_id"}

// areaError maps the refused area of a write (an unknown node, one of
// another organization or outside the scope, 23503 of
// stocktake_derive_location) to a 422 *api.FieldError on location_id; other
// errors are returned unchanged.
func areaError(err error) error {
	var fe *api.FieldError
	if errors.As(api.FieldDBError(err, areaFields), &fe) {
		fe.Status = http.StatusUnprocessableEntity
		return fe
	}
	return err
}

// requireVisible fails with ErrNotFound when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// List returns paginated stocktakes filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Stocktake, int, error) {
	var stocktakes []Stocktake
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

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM stocktake WHERE id = $1", stocktakeSelectColumns)
		var err error
		stocktake, err = scanStocktake(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
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
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO stocktake (
				organization_id, title, description, status, scope, started_by, started_at,
				completed_at, due_date, total_expected, total_scanned, total_missing, total_surplus,
				location_id
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13,
				$14
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
			nilIfEmpty(s.LocationID),
		))
		if err != nil {
			if mapped := areaError(err); mapped != err {
				return mapped
			}
			return fmt.Errorf("create stocktake: %w", err)
		}
		*s = *created
		return nil
	})
}

// Update modifies title, description, due date and, while the stocktake
// is planned, its area (WP-848). Only principals that may write the
// stocktake (stocktakeWritable and the restrictive policies of 000260) get
// it; any other gets ErrNotFound.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error) {
	var stocktake *Stocktake

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockStocktake(ctx, tx, id)
		if err != nil {
			return err
		}
		setClauses := make([]string, 0, 5)
		args := []any{id}
		argPos := 2

		if req.LocationID != nil {
			if current.Status != StatusPlanned {
				return ErrInvalidTransition
			}
			setClauses = append(setClauses, fmt.Sprintf("location_id = $%d", argPos))
			args = append(args, nilIfEmpty(*req.LocationID))
			argPos++
		}
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
		if req.DueDate != nil {
			setClauses = append(setClauses, fmt.Sprintf("due_date = $%d", argPos))
			args = append(args, nilIfEmpty(*req.DueDate))
		}
		if len(setClauses) == 0 {
			stocktake = current
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf("UPDATE stocktake SET %s WHERE id = $1 AND %s RETURNING %s", strings.Join(setClauses, ", "), stocktakeWritable, stocktakeSelectColumns)
		stocktake, err = scanStocktake(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if mapped := areaError(err); mapped != err {
				return mapped
			}
			return fmt.Errorf("update stocktake: %w", err)
		}
		return nil
	})

	return stocktake, err
}

// lockStocktake reads the stocktake id for a write (stocktakeWritable) and
// locks its row; a stocktake the principal may not write is ErrNotFound.
func lockStocktake(ctx context.Context, tx pgx.Tx, id string) (*Stocktake, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	st, err := scanStocktake(tx.QueryRow(ctx,
		"SELECT "+stocktakeSelectColumns+" FROM stocktake WHERE id = $1 AND "+stocktakeWritable+" FOR UPDATE", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock stocktake: %w", err)
	}
	return st, nil
}

// Start moves a planned stocktake with area to in_progress and freezes the
// expected stock of the area in the same transaction (WP-848): one row per
// asset whose location_id lies in the subtree of the area (location.path
// <@ the path of the area, expected quantity 1) and one per stock_level
// row of a bin in the subtree (expected quantity on_hand). Assets with a
// free-text location only belong to no snapshot. The rows are those the
// starting principal sees (row level security of asset and stock_level);
// later movements do not change them. started_at, started_by and
// total_expected (the number of snapshot rows) are set as well.
func (r *PGRepository) Start(ctx context.Context, orgID, id, userID string) (*Stocktake, error) {
	var stocktake *Stocktake

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockStocktake(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusPlanned {
			return ErrInvalidTransition
		}
		if current.LocationID == "" {
			return ErrNoArea
		}
		var area string
		if err = tx.QueryRow(ctx, `SELECT path::text FROM location WHERE id = $1`, current.LocationID).Scan(&area); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("read stocktake area: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, asset_id, location_id, expected_quantity)
			SELECT a.organization_id, $1, 'asset', a.id, a.location_id, 1
			  FROM asset a
			  JOIN location l ON l.id = a.location_id AND l.organization_id = a.organization_id
			 WHERE l.path <@ $2::ltree`, id, area)
		if err != nil {
			return fmt.Errorf("freeze expected assets: %w", err)
		}
		expected := tag.RowsAffected()
		tag, err = tx.Exec(ctx, `
			INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, consumable_id, location_id, expected_quantity)
			SELECT s.organization_id, $1, 'consumable', s.consumable_id, s.location_id, s.on_hand
			  FROM stock_level s
			  JOIN location l ON l.id = s.location_id AND l.organization_id = s.organization_id
			 WHERE l.path <@ $2::ltree`, id, area)
		if err != nil {
			return fmt.Errorf("freeze expected consumables: %w", err)
		}
		expected += tag.RowsAffected()
		stocktake, err = scanStocktake(tx.QueryRow(ctx, `
			UPDATE stocktake SET status = 'in_progress', started_at = NOW(), started_by = $2,
			       total_expected = $3, updated_at = NOW()
			 WHERE id = $1 AND `+stocktakeWritable+` RETURNING `+stocktakeSelectColumns,
			id, nilIfEmpty(userID), expected))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("start stocktake: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stocktake, nil
}

// Cancel moves a planned or in-progress stocktake to cancelled (WP-848);
// the snapshot stays.
func (r *PGRepository) Cancel(ctx context.Context, orgID, id string) (*Stocktake, error) {
	var stocktake *Stocktake

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockStocktake(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusPlanned && current.Status != StatusInProgress {
			return ErrInvalidTransition
		}
		stocktake, err = scanStocktake(tx.QueryRow(ctx,
			"UPDATE stocktake SET status = 'cancelled', updated_at = NOW() WHERE id = $1 AND "+stocktakeWritable+" RETURNING "+stocktakeSelectColumns, id))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("cancel stocktake: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stocktake, nil
}

// ListItems returns a page of the snapshot of the stocktake id ordered by
// id: after the keyset cursor (sort id) when given, else from the offset.
// A stocktake the principal cannot see is ErrNotFound.
func (r *PGRepository) ListItems(ctx context.Context, orgID, id string, page api.PaginationParams) ([]Item, int, error) {
	items := []Item{}
	var total int

	if !api.IsUUID(id) {
		return nil, 0, ErrNotFound
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stocktake WHERE id = $1)", id).Scan(&exists); err != nil {
			return fmt.Errorf("check stocktake: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM stocktake_item WHERE stocktake_id = $1", id).Scan(&total); err != nil {
			return fmt.Errorf("count stocktake items: %w", err)
		}
		query := `SELECT id::text, stocktake_id::text, item_kind, COALESCE(asset_id::text, ''), COALESCE(consumable_id::text, ''),
		                 location_id::text, expected_quantity::float8, counted_quantity::float8, COALESCE(classification, ''), created_at
		            FROM stocktake_item WHERE stocktake_id = $1`
		args := []any{id}
		if page.Cursor != nil {
			if !page.Cursor.Matches(itemSort, "asc") || !api.IsUUID(page.Cursor.ID) {
				return fmt.Errorf("%w: not issued for the stocktake items", api.ErrInvalidCursor)
			}
			query += " AND id > $2::uuid ORDER BY id LIMIT $3"
			args = append(args, page.Cursor.ID, page.Limit)
		} else {
			query += " ORDER BY id LIMIT $2 OFFSET $3"
			args = append(args, page.Limit, page.Offset)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list stocktake items: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item Item
			if err = rows.Scan(&item.ID, &item.StocktakeID, &item.ItemKind, &item.AssetID, &item.ConsumableID,
				&item.LocationID, &item.ExpectedQuantity, &item.CountedQuantity, &item.Classification, &item.CreatedAt); err != nil {
				return fmt.Errorf("scan stocktake item: %w", err)
			}
			item.CreatedAt = item.CreatedAt.UTC()
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ItemsNextCursor returns the keyset cursor after the last item of a full
// page, or an empty string when the page was not full.
func ItemsNextCursor(items []Item, limit int) string {
	if len(items) == 0 || len(items) < limit {
		return ""
	}
	last := items[len(items)-1].ID
	return api.EncodeCursor(api.Cursor{Sort: itemSort, Dir: "asc", Value: last, ID: last})
}

// Delete deletes a stocktake. The stocktake table has no deleted_at column, so this is a hard delete
// that cascades to the scans of all clients; only an org-wide principal may (stocktakeWritable).
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM stocktake WHERE id = $1 AND "+stocktakeWritable, id)
		if err != nil {
			return fmt.Errorf("delete stocktake: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddScan records a stocktake scan and updates stocktake counters.
func (r *PGRepository) AddScan(ctx context.Context, scan *StockScan) error {
	return database.WithRequestTenant(ctx, r.pool, scan.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stocktake WHERE id = $1)", scan.StocktakeID).Scan(&exists); err != nil {
			return fmt.Errorf("check stocktake: %w", err)
		}
		if !exists {
			return ErrNotFound
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
		if err := requireVisible(ctx, tx, "stock_scan", scanVisible, created.ID); err != nil {
			return err
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

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_scan WHERE "+scanVisible+" AND stocktake_id = $1", stocktakeID).Scan(&total); err != nil {
			return fmt.Errorf("count stock scans: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM stock_scan WHERE "+scanVisible+" AND stocktake_id = $1 ORDER BY scanned_at DESC LIMIT $2 OFFSET $3", stockScanSelectColumns)
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

// Difference returns the deviating scans of a stocktake joined with the
// referenced asset (when present), oldest scan first. The asset carries
// its inventory number (inventory_no), lifecycle_state and location_id
// (E-120 (b), WP-738); status and the free text location stay as
// deprecated fields.
func (r *PGRepository) Difference(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]DifferenceEntry, int, error) {
	entries := []DifferenceEntry{}
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stocktake WHERE id = $1)", stocktakeID).Scan(&exists); err != nil {
			return fmt.Errorf("check stocktake: %w", err)
		}
		if !exists {
			return ErrNotFound
		}

		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM stock_scan
			 WHERE stocktake_id = $1 AND scan_result IN ('missing', 'surplus', 'damaged', 'wrong_location')
			   AND `+scanVisible,
			stocktakeID).Scan(&total); err != nil {
			return fmt.Errorf("count stocktake difference: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT s.id::text, s.organization_id::text, s.stocktake_id::text,
			       COALESCE(s.asset_id::text, ''), COALESCE(s.ci_id::text, ''),
			       s.scanned_by::text, s.scan_method, s.scan_result,
			       COALESCE(s.location_found, ''), COALESCE(s.notes, ''), s.scanned_at,
			       a.id::text, a.inventory_no, a.name, a.lifecycle_state, a.location_id::text, a.status, a.location
			FROM stock_scan s
			LEFT JOIN asset a ON a.id = s.asset_id
			WHERE s.stocktake_id = $1
			  AND s.scan_result IN ('missing', 'surplus', 'damaged', 'wrong_location')
			  AND EXISTS (SELECT 1 FROM stock_scan WHERE stock_scan.id = s.id AND `+scanVisible+`)
			ORDER BY s.scanned_at ASC
			LIMIT $2 OFFSET $3`, stocktakeID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list stocktake difference: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var entry DifferenceEntry
			var assetID, assetTag, assetName, assetState, assetLocationID, assetStatus, assetLocation sql.NullString
			if err := rows.Scan(
				&entry.Scan.ID, &entry.Scan.OrganizationID, &entry.Scan.StocktakeID,
				&entry.Scan.AssetID, &entry.Scan.CIID, &entry.Scan.ScannedBy,
				&entry.Scan.ScanMethod, &entry.Scan.ScanResult,
				&entry.Scan.LocationFound, &entry.Scan.Notes, &entry.Scan.ScannedAt,
				&assetID, &assetTag, &assetName, &assetState, &assetLocationID, &assetStatus, &assetLocation,
			); err != nil {
				return fmt.Errorf("scan difference row: %w", err)
			}
			if assetID.Valid {
				entry.Asset = &asset.Asset{
					ID:             assetID.String,
					AssetTag:       assetTag.String,
					Name:           assetName.String,
					LifecycleState: assetState.String,
					LocationID:     assetLocationID.String,
					Status:         assetStatus.String,
					Location:       assetLocation.String,
				}
			}
			entries = append(entries, entry)
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return entries, total, nil
}

// Complete finalizes a stocktake and applies the recorded inventory
// corrections atomically: the stocktake transition and all asset updates run
// in a single transaction, so a failure rolls everything back. Only an
// org-wide principal completes a stocktake (stocktakeWritable): a scoped one
// would close it for the differences of other clients too.
func (r *PGRepository) Complete(ctx context.Context, orgID, id string, applyCorrections bool) (*Completion, error) {
	completion := &Completion{Corrections: []Correction{}}

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, "SELECT status FROM stocktake WHERE id = $1 AND "+stocktakeWritable+" FOR UPDATE", id).Scan(&status)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock stocktake: %w", err)
		}
		if status == "completed" || status == "cancelled" {
			return ErrAlreadyCompleted
		}

		if applyCorrections {
			// The corrections report the status and free text location
			// they overwrite; WP-287 moves them onto movements.
			rows, queryErr := tx.Query(ctx, `
				SELECT s.id::text, s.scan_result, COALESCE(s.location_found, ''),
				       a.id::text, a.inventory_no, a.status, COALESCE(a.location, '')
				FROM stock_scan s
				JOIN asset a ON a.id = s.asset_id
				WHERE s.stocktake_id = $1
				  AND s.scan_result IN ('missing', 'surplus', 'damaged', 'wrong_location')
				ORDER BY s.scanned_at ASC`, id)
			if queryErr != nil {
				return fmt.Errorf("list deviating scans: %w", queryErr)
			}
			type correctionRow struct {
				scan                      StockScan
				assetID, tag, status, loc string
			}
			var pending []correctionRow
			for rows.Next() {
				var cr correctionRow
				if rowErr := rows.Scan(&cr.scan.ID, &cr.scan.ScanResult, &cr.scan.LocationFound, &cr.assetID, &cr.tag, &cr.status, &cr.loc); rowErr != nil {
					rows.Close()
					return fmt.Errorf("scan deviating row: %w", rowErr)
				}
				pending = append(pending, cr)
			}
			rows.Close()
			if iterErr := rows.Err(); iterErr != nil {
				return fmt.Errorf("iterate deviating scans: %w", iterErr)
			}

			for i := range pending {
				p := &pending[i]
				c := Correction{
					AssetID:          p.assetID,
					AssetTag:         p.tag,
					ScanResult:       p.scan.ScanResult,
					PreviousStatus:   p.status,
					PreviousLocation: p.loc,
				}
				var query string
				switch p.scan.ScanResult {
				case "missing":
					c.NewStatus = "lost"
					query = "UPDATE asset SET status = 'lost', updated_at = NOW() WHERE id = $1"
				case "surplus":
					c.NewStatus = "in_stock"
					query = "UPDATE asset SET status = 'in_stock', updated_at = NOW() WHERE id = $1"
				case "damaged":
					c.NewStatus = "maintenance"
					query = "UPDATE asset SET status = 'maintenance', updated_at = NOW() WHERE id = $1"
				case "wrong_location":
					if p.scan.LocationFound == "" {
						c.Detail = "scan did not record a found location"
						completion.Corrections = append(completion.Corrections, c)
						continue
					}
					c.NewLocation = p.scan.LocationFound
					query = "UPDATE asset SET location = $2, updated_at = NOW() WHERE id = $1"
				default:
					continue
				}
				var cmdTag pgconn.CommandTag
				var execErr error
				if c.NewLocation != "" {
					cmdTag, execErr = tx.Exec(ctx, query, p.assetID, p.scan.LocationFound)
				} else {
					cmdTag, execErr = tx.Exec(ctx, query, p.assetID)
				}
				if execErr != nil {
					return fmt.Errorf("apply correction for asset %s: %w", p.assetID, execErr)
				}
				c.Applied = cmdTag.RowsAffected() > 0
				completion.Corrections = append(completion.Corrections, c)
				if c.Applied {
					completion.CorrectionsApplied++
				}
			}
		}

		st, err := scanStocktake(tx.QueryRow(ctx,
			"UPDATE stocktake SET status = 'completed', completed_at = NOW(), updated_at = NOW() WHERE id = $1 AND "+stocktakeWritable+" RETURNING "+stocktakeSelectColumns, id))
		if err != nil {
			return fmt.Errorf("complete stocktake: %w", err)
		}
		completion.Stocktake = *st
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return completion, nil
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
		&item.LocationID,
		&item.ClientID,
		&item.SiteID,
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
