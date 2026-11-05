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
	COALESCE(consumable_id::text, ''),
	quantity::float8,
	COALESCE(location_id::text, ''),
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
	COALESCE(ci_id::text, ''),
	scanned_by::text,
	scan_method,
	scan_result,
	COALESCE(legacy_scan_result, ''),
	COALESCE(code, ''),
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

// stocktakeWritable admits a stocktake for UPDATE and DELETE, its
// transitions and its completion (F-365, WP-848): a stocktake without area
// (location_id NULL, client_id and site_id NULL) is a row of the whole
// organization (migration 000012), so only an org-wide principal changes,
// starts, cancels, completes or deletes it (E-09); a principal restricted in
// the client or site scope gets ErrNotFound for such a stocktake. It takes
// no scans (no snapshot, WP-287). A stocktake with area carries the
// client and site of its node; the restrictive policies
// stocktake_scoped_update and stocktake_scoped_delete (000260) admit the
// principals whose scope covers both.
const stocktakeWritable = "(location_id IS NOT NULL OR " + database.OrgWideScopeSQL + ")"

// itemSort is the sort of the keyset cursor of the snapshot items.
const itemSort = "id"

// scanSort and differenceSort are the sorts of the keyset cursors of the
// scans (newest first) and of the difference list (by object key).
const (
	scanSort       = "scanned_at"
	differenceSort = "object"
)

// objectsCTE derives one row per object of the stocktakes $1 (uuid[]) from
// the snapshot and the scans the caller sees (WP-287): an asset of the
// snapshot or scanned (its last scan counts), a consumable per bin of the
// snapshot or scanned (the scanned quantities add up). Items of unexpected
// objects written by the completion are no part of the snapshot. An object
// outside the snapshot keeps the result of its last scan (unexpected, or
// the found and missing of an earlier release). key orders the difference
// list.
const objectsCTE = `
WITH snap AS (
    SELECT i.stocktake_id, i.item_kind, i.asset_id, i.consumable_id, i.location_id, i.expected_quantity
      FROM stocktake_item i
     WHERE i.stocktake_id = ANY ($1::uuid[]) AND i.classification IS DISTINCT FROM 'unexpected'
), last_asset_scan AS (
    SELECT DISTINCT ON (s.stocktake_id, s.asset_id) s.stocktake_id, s.asset_id, s.location_id, s.scan_result
      FROM stock_scan s
     WHERE s.stocktake_id = ANY ($1::uuid[]) AND s.asset_id IS NOT NULL
     ORDER BY s.stocktake_id, s.asset_id, s.scanned_at DESC, s.id DESC
), consumable_count AS (
    SELECT s.stocktake_id, s.consumable_id, s.location_id, sum(s.quantity) AS counted
      FROM stock_scan s
     WHERE s.stocktake_id = ANY ($1::uuid[]) AND s.consumable_id IS NOT NULL
     GROUP BY s.stocktake_id, s.consumable_id, s.location_id
), objects AS (
    SELECT COALESCE(n.stocktake_id, c.stocktake_id) AS stocktake_id, 'asset'::text AS item_kind,
           COALESCE(n.asset_id, c.asset_id) AS object_id,
           CASE WHEN n.asset_id IS NULL THEN c.scan_result
                WHEN c.asset_id IS NULL THEN 'missing' ELSE 'found' END AS classification,
           n.location_id AS expected_location_id, c.location_id AS found_location_id,
           COALESCE(n.expected_quantity, 0) AS expected_quantity,
           (CASE WHEN c.asset_id IS NULL OR c.scan_result = 'missing' THEN 0 ELSE 1 END)::numeric AS counted_quantity
      FROM (SELECT * FROM snap WHERE item_kind = 'asset') n
      FULL JOIN last_asset_scan c ON c.stocktake_id = n.stocktake_id AND c.asset_id = n.asset_id
    UNION ALL
    SELECT COALESCE(n.stocktake_id, c.stocktake_id), 'consumable'::text,
           COALESCE(n.consumable_id, c.consumable_id),
           CASE WHEN n.consumable_id IS NULL THEN 'unexpected'
                WHEN c.consumable_id IS NULL THEN 'missing' ELSE 'found' END,
           n.location_id, c.location_id,
           COALESCE(n.expected_quantity, 0), COALESCE(c.counted, 0)
      FROM (SELECT * FROM snap WHERE item_kind = 'consumable') n
      FULL JOIN consumable_count c
        ON c.stocktake_id = n.stocktake_id AND c.consumable_id = n.consumable_id AND c.location_id = n.location_id
), keyed AS (
    SELECT o.*, (o.item_kind || ':' || o.object_id::text
                 || CASE WHEN o.item_kind = 'consumable'
                         THEN ':' || COALESCE(o.expected_location_id, o.found_location_id)::text ELSE '' END) COLLATE "C" AS key
      FROM objects o
)
`

// deviates selects the objects of the difference list: missing, unexpected
// and found with another location (asset) or quantity (consumable) than
// the snapshot.
const deviates = `(k.classification IN ('missing', 'unexpected')
    OR (k.classification = 'found' AND k.expected_location_id IS NOT NULL
        AND ((k.item_kind = 'asset' AND k.found_location_id IS DISTINCT FROM k.expected_location_id)
             OR (k.item_kind = 'consumable' AND k.counted_quantity <> k.expected_quantity))))`

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
		rows.Close()
		live := make([]*Stocktake, len(stocktakes))
		for i := range stocktakes {
			live[i] = &stocktakes[i]
		}
		return liveTotals(ctx, tx, live)
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
		return liveTotals(ctx, tx, []*Stocktake{stocktake})
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
		if current.terminal() {
			return ErrAlreadyCompleted
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
			return liveTotals(ctx, tx, []*Stocktake{stocktake})
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
			return immutableError(fmt.Errorf("update stocktake: %w", err))
		}
		return liveTotals(ctx, tx, []*Stocktake{stocktake})
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
		if current.terminal() {
			return ErrAlreadyCompleted
		}
		stocktake, err = scanStocktake(tx.QueryRow(ctx,
			"UPDATE stocktake SET status = 'cancelled', updated_at = NOW() WHERE id = $1 AND "+stocktakeWritable+" RETURNING "+stocktakeSelectColumns, id))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return immutableError(fmt.Errorf("cancel stocktake: %w", err))
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

// Delete deletes a planned or in-progress stocktake. The stocktake table
// has no deleted_at column, so this is a hard delete that cascades to the
// scans and items of all clients; only an org-wide principal may
// (stocktakeWritable). A completed or cancelled stocktake is immutable
// (ErrAlreadyCompleted, WP-761).
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockStocktake(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.terminal() {
			return ErrAlreadyCompleted
		}
		cmdTag, err := tx.Exec(ctx, "DELETE FROM stocktake WHERE id = $1 AND "+stocktakeWritable, id)
		if err != nil {
			return immutableError(fmt.Errorf("delete stocktake: %w", err))
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// liveTotals sets total_scanned, total_missing and total_surplus (the
// unexpected objects) of the in-progress stocktakes with snapshot among
// items from the snapshot and the scans the caller sees (WP-287); the
// stored totals of the others stay.
func liveTotals(ctx context.Context, tx pgx.Tx, items []*Stocktake) error {
	ids := make([]string, 0, len(items))
	byID := make(map[string]*Stocktake, len(items))
	for _, st := range items {
		if st.Status == StatusInProgress && st.hasSnapshot() {
			ids = append(ids, st.ID)
			byID[st.ID] = st
			st.TotalScanned, st.TotalMissing, st.TotalSurplus = 0, 0, 0
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, objectsCTE+`
		SELECT k.stocktake_id::text, count(*) FILTER (WHERE k.counted_quantity > 0),
		       count(*) FILTER (WHERE k.classification = 'missing'),
		       count(*) FILTER (WHERE k.classification = 'unexpected')
		  FROM keyed k GROUP BY k.stocktake_id`, ids)
	if err != nil {
		return fmt.Errorf("stocktake totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var scanned, missing, surplus int
		if err = rows.Scan(&id, &scanned, &missing, &surplus); err != nil {
			return fmt.Errorf("scan stocktake totals: %w", err)
		}
		if st := byID[id]; st != nil {
			st.TotalScanned, st.TotalMissing, st.TotalSurplus = scanned, missing, surplus
		}
	}
	return rows.Err()
}

// AddScan records a scan of an in-progress stocktake with snapshot
// (WP-287). The stocktake is read with FOR SHARE, so complete and cancel
// (FOR UPDATE) wait for the scan; the scanned asset or consumable must be
// visible (ErrObjectNotFound), the place found a location of the area and,
// for a consumable, a bin (ErrPlaceFound). scan_result is found when the
// object is in the snapshot (a consumable with a snapshot row of that
// bin), else unexpected. client_id and site_id come from the stocktake
// (stock_scan_derive_scope); AddScan writes no stocktake row.
func (r *PGRepository) AddScan(ctx context.Context, scan *StockScan) error {
	if !api.IsUUID(scan.StocktakeID) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, scan.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		st, err := scanStocktake(tx.QueryRow(ctx,
			"SELECT "+stocktakeSelectColumns+" FROM stocktake WHERE id = $1 FOR SHARE", scan.StocktakeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock stocktake: %w", err)
		}
		if st.terminal() {
			return ErrAlreadyCompleted
		}
		if st.Status != StatusInProgress {
			return ErrInvalidTransition
		}
		if !st.hasSnapshot() {
			return ErrNoSnapshot
		}

		var visible bool
		if scan.AssetID != "" {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM asset WHERE id = $1)`, scan.AssetID).Scan(&visible)
		} else {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM consumable WHERE id = $1)`, scan.ConsumableID).Scan(&visible)
		}
		if err != nil {
			return fmt.Errorf("check scanned object: %w", err)
		}
		if !visible {
			return ErrObjectNotFound
		}

		var inArea bool
		var kind string
		err = tx.QueryRow(ctx, `
			SELECT l.kind, l.path <@ area.path FROM location l, location area
			 WHERE l.id = $1 AND area.id = $2`, scan.LocationID, st.LocationID).Scan(&kind, &inArea)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!inArea || (scan.ConsumableID != "" && kind != "bin"))) {
			return ErrPlaceFound
		}
		if err != nil {
			return fmt.Errorf("check place found: %w", err)
		}

		var inSnapshot bool
		if scan.AssetID != "" {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stocktake_item WHERE stocktake_id = $1 AND asset_id = $2
				AND classification IS DISTINCT FROM 'unexpected')`, st.ID, scan.AssetID).Scan(&inSnapshot)
		} else {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stocktake_item WHERE stocktake_id = $1 AND consumable_id = $2
				AND location_id = $3 AND classification IS DISTINCT FROM 'unexpected')`, st.ID, scan.ConsumableID, scan.LocationID).Scan(&inSnapshot)
		}
		if err != nil {
			return fmt.Errorf("check snapshot: %w", err)
		}
		scan.ScanResult = ScanUnexpected
		if inSnapshot {
			scan.ScanResult = ScanFound
		}

		created, err := scanStockScan(tx.QueryRow(ctx, `
			INSERT INTO stock_scan (
				organization_id, stocktake_id, asset_id, consumable_id, quantity, location_id,
				scanned_by, scan_method, scan_result, code, notes
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+stockScanSelectColumns,
			scan.OrganizationID, scan.StocktakeID, nilIfEmpty(scan.AssetID), nilIfEmpty(scan.ConsumableID), scan.Quantity,
			scan.LocationID, scan.ScannedBy, scan.ScanMethod, scan.ScanResult, nilIfEmpty(scan.Code), nilIfEmpty(scan.Notes)))
		if err != nil {
			return immutableError(fmt.Errorf("add stock scan: %w", err))
		}
		*scan = *created
		return nil
	})
}

// requireStocktake fails with ErrNotFound when the caller does not see the
// stocktake id.
func requireStocktake(ctx context.Context, tx pgx.Tx, id string) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stocktake WHERE id = $1)", id).Scan(&exists); err != nil {
		return fmt.Errorf("check stocktake: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// ListScans returns a page of the scans of a stocktake the caller sees,
// newest first: after the keyset cursor (scanned_at, id) when given, else
// from the offset. The policies of stock_scan apply the client and site of
// each scan (WP-287).
func (r *PGRepository) ListScans(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error) {
	scans := []StockScan{}
	var total int

	if !api.IsUUID(stocktakeID) {
		return nil, 0, ErrNotFound
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireStocktake(ctx, tx, stocktakeID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM stock_scan WHERE stocktake_id = $1", stocktakeID).Scan(&total); err != nil {
			return fmt.Errorf("count stock scans: %w", err)
		}
		query := "SELECT " + stockScanSelectColumns + " FROM stock_scan WHERE stocktake_id = $1"
		args := []any{stocktakeID}
		if page.Cursor != nil {
			if !page.Cursor.Matches(scanSort, "desc") || !api.IsUUID(page.Cursor.ID) {
				return fmt.Errorf("%w: not issued for the stocktake scans", api.ErrInvalidCursor)
			}
			if _, perr := time.Parse(time.RFC3339Nano, page.Cursor.Value); perr != nil {
				return fmt.Errorf("%w: not issued for the stocktake scans", api.ErrInvalidCursor)
			}
			query += " AND " + api.KeysetClause("scanned_at", "timestamptz", "desc", 2) + " ORDER BY scanned_at DESC, id DESC LIMIT $4"
			args = append(args, page.Cursor.Value, page.Cursor.ID, page.Limit)
		} else {
			query += " ORDER BY scanned_at DESC, id DESC LIMIT $2 OFFSET $3"
			args = append(args, page.Limit, page.Offset)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list stock scans: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			scan, serr := scanStockScan(rows)
			if serr != nil {
				return fmt.Errorf("scan stock scan: %w", serr)
			}
			scans = append(scans, *scan)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return scans, total, nil
}

// ScansNextCursor returns the keyset cursor after the last scan of a full
// page, or an empty string when the page was not full.
func ScansNextCursor(scans []StockScan, limit int) string {
	if len(scans) == 0 || len(scans) < limit {
		return ""
	}
	last := &scans[len(scans)-1]
	return api.EncodeCursor(api.Cursor{Sort: scanSort, Dir: "desc", Value: last.ScannedAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
}

// Differences returns a page of the difference list of a stocktake
// (WP-287), ordered by object: after the keyset cursor when given, else
// from the offset. An asset or consumable the caller sees is embedded.
func (r *PGRepository) Differences(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]Difference, int, error) {
	entries := []Difference{}
	var total int

	if !api.IsUUID(stocktakeID) {
		return nil, 0, ErrNotFound
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireStocktake(ctx, tx, stocktakeID); err != nil {
			return err
		}
		ids := []string{stocktakeID}
		if err := tx.QueryRow(ctx, objectsCTE+"SELECT count(*) FROM keyed k WHERE "+deviates, ids).Scan(&total); err != nil {
			return fmt.Errorf("count stocktake differences: %w", err)
		}
		query := objectsCTE + `
			SELECT k.key, k.item_kind, k.object_id::text, k.classification,
			       COALESCE(k.expected_location_id::text, ''), COALESCE(k.found_location_id::text, ''),
			       k.expected_quantity::float8, k.counted_quantity::float8,
			       a.id::text, COALESCE(a.client_id::text, ''), COALESCE(a.site_id::text, ''), COALESCE(a.asset_type_id::text, ''),
			       COALESCE(a.name, ''), COALESCE(a.inventory_no, ''), COALESCE(a.serial_number, ''),
			       COALESCE(a.lifecycle_state, ''), COALESCE(a.location_id::text, ''),
			       c.id::text, COALESCE(c.name, ''), COALESCE(c.sku, ''), COALESCE(c.unit, '')
			  FROM keyed k
			  LEFT JOIN asset a ON k.item_kind = 'asset' AND a.id = k.object_id
			  LEFT JOIN consumable c ON k.item_kind = 'consumable' AND c.id = k.object_id
			 WHERE ` + deviates
		args := []any{ids}
		if page.Cursor != nil {
			if !page.Cursor.Matches(differenceSort, "asc") || page.Cursor.Value == "" {
				return fmt.Errorf("%w: not issued for the stocktake differences", api.ErrInvalidCursor)
			}
			query += ` AND k.key > $2 COLLATE "C" ORDER BY k.key LIMIT $3`
			args = append(args, page.Cursor.Value, page.Limit)
		} else {
			query += " ORDER BY k.key LIMIT $2 OFFSET $3"
			args = append(args, page.Limit, page.Offset)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list stocktake differences: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d Difference
			var objectID string
			var assetID, consumableID sql.NullString
			var a DifferenceAsset
			var c Supply
			if err = rows.Scan(&d.key, &d.ItemKind, &objectID, &d.Classification, &d.ExpectedLocationID, &d.FoundLocationID,
				&d.ExpectedQuantity, &d.CountedQuantity,
				&assetID, &a.ClientID, &a.SiteID, &a.AssetTypeID, &a.Name, &a.InventoryNo, &a.SerialNumber, &a.LifecycleState, &a.LocationID,
				&consumableID, &c.Name, &c.SKU, &c.Unit); err != nil {
				return fmt.Errorf("scan stocktake difference: %w", err)
			}
			if d.ItemKind == ItemKindAsset {
				d.AssetID = objectID
			} else {
				d.ConsumableID = objectID
			}
			if assetID.Valid {
				a.ID = assetID.String
				d.Asset = &a
			}
			if consumableID.Valid {
				c.ID = consumableID.String
				d.Consumable = &c
			}
			entries = append(entries, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// DifferencesNextCursor returns the keyset cursor after the last entry of a
// full page, or an empty string when the page was not full.
func DifferencesNextCursor(entries []Difference, limit int) string {
	if len(entries) == 0 || len(entries) < limit {
		return ""
	}
	last := entries[len(entries)-1].key
	return api.EncodeCursor(api.Cursor{Sort: differenceSort, Dir: "asc", Value: last, ID: last})
}

// RunInTx runs fn in one transaction of the context's tenant scope, which
// the repository calls and the movement service join through fn's context
// (database.WithRequestTenantTx, WP-332).
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

// fixResult locks the stocktake id for its completion (FOR UPDATE, only
// principals that may write it: stocktakeWritable and the restrictive
// policies of 000260), checks completeRule, computes the totals and fixes
// the result (WP-287): every snapshot item gets counted_quantity and
// classification, every unexpected object an item with expected_quantity 0
// at the place found. It returns the stocktake with its totals and the
// corrections of the result (plannedCorrections, WP-761).
func (r *PGRepository) fixResult(ctx context.Context, orgID, id string) (*Stocktake, []plannedCorrection, error) {
	var current *Stocktake
	var planned []plannedCorrection
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		current, err = lockStocktake(ctx, tx, id)
		if err != nil {
			return err
		}
		if ruleErr := completeRule(current); ruleErr != nil {
			return ruleErr
		}
		if totalsErr := liveTotals(ctx, tx, []*Stocktake{current}); totalsErr != nil {
			return totalsErr
		}
		ids := []string{id}
		if _, err = tx.Exec(ctx, objectsCTE+`
			UPDATE stocktake_item i SET counted_quantity = k.counted_quantity, classification = k.classification
			  FROM keyed k
			 WHERE i.stocktake_id = k.stocktake_id AND k.expected_location_id IS NOT NULL AND i.item_kind = k.item_kind
			   AND ((k.item_kind = 'asset' AND i.asset_id = k.object_id)
			        OR (k.item_kind = 'consumable' AND i.consumable_id = k.object_id AND i.location_id = k.expected_location_id))`,
			ids); err != nil {
			return fmt.Errorf("fix the counted items: %w", err)
		}
		if _, err = tx.Exec(ctx, objectsCTE+`
			INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, asset_id, consumable_id, location_id,
			                            expected_quantity, counted_quantity, classification)
			SELECT $2, k.stocktake_id, k.item_kind,
			       CASE WHEN k.item_kind = 'asset' THEN k.object_id END,
			       CASE WHEN k.item_kind = 'consumable' THEN k.object_id END,
			       k.found_location_id, 0, k.counted_quantity, 'unexpected'
			  FROM keyed k
			 WHERE k.expected_location_id IS NULL AND k.classification = 'unexpected' AND k.found_location_id IS NOT NULL`,
			ids, orgID); err != nil {
			return fmt.Errorf("fix the unexpected items: %w", err)
		}
		planned, err = plannedCorrections(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return current, planned, nil
}

// plannedCorrections derives the corrections of the fixed result of the
// stocktake id (WP-761): a transfer per asset whose last scan shows another
// place than its current location_id (found elsewhere or unexpected; a
// missing asset gets none, and its status stays), and per consumable and
// bin of the result a correction by the counted quantity minus the book
// stock of the bin now (stock_level.on_hand, the snapshot plus the
// movements since the start), none for a difference of 0. Assets and
// consumables come in id order, the bins of a consumable in location
// order, so concurrent completions lock in one order.
func plannedCorrections(ctx context.Context, tx pgx.Tx, id string) ([]plannedCorrection, error) {
	var planned []plannedCorrection
	rows, err := tx.Query(ctx, `
		SELECT l.asset_id::text, COALESCE(a.location_id::text, ''), l.location_id::text
		  FROM (SELECT DISTINCT ON (s.asset_id) s.asset_id, s.location_id
		          FROM stock_scan s
		         WHERE s.stocktake_id = $1 AND s.asset_id IS NOT NULL AND s.location_id IS NOT NULL
		         ORDER BY s.asset_id, s.scanned_at DESC, s.id DESC) l
		  JOIN asset a ON a.id = l.asset_id
		 WHERE a.location_id IS DISTINCT FROM l.location_id
		 ORDER BY l.asset_id`, id)
	if err != nil {
		return nil, fmt.Errorf("derive the asset corrections: %w", err)
	}
	for rows.Next() {
		c := plannedCorrection{itemKind: ItemKindAsset}
		if err = rows.Scan(&c.assetID, &c.from, &c.to); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan asset correction: %w", err)
		}
		planned = append(planned, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset corrections: %w", err)
	}
	rows, err = tx.Query(ctx, `
		SELECT i.consumable_id::text, i.location_id::text,
		       (COALESCE(i.counted_quantity, 0) - COALESCE(sl.on_hand, 0))::float8
		  FROM stocktake_item i
		  LEFT JOIN stock_level sl ON sl.organization_id = i.organization_id
		       AND sl.consumable_id = i.consumable_id AND sl.location_id = i.location_id
		 WHERE i.stocktake_id = $1 AND i.item_kind = 'consumable'
		   AND COALESCE(i.counted_quantity, 0) <> COALESCE(sl.on_hand, 0)
		 ORDER BY i.consumable_id, i.location_id`, id)
	if err != nil {
		return nil, fmt.Errorf("derive the consumable corrections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bin string
		var diff float64
		c := plannedCorrection{itemKind: ItemKindConsumable}
		if err = rows.Scan(&c.consumableID, &bin, &diff); err != nil {
			return nil, fmt.Errorf("scan consumable correction: %w", err)
		}
		c.quantity, c.to = diff, bin
		if diff < 0 {
			c.quantity, c.to, c.from = -diff, "", bin
		}
		planned = append(planned, c)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate consumable corrections: %w", err)
	}
	return planned, nil
}

// closeCompleted sets the totals of current and the status completed in
// one UPDATE; fixResult locked the stocktake in the same transaction.
func (r *PGRepository) closeCompleted(ctx context.Context, orgID string, current *Stocktake) (*Stocktake, error) {
	var st *Stocktake
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		st, err = scanStocktake(tx.QueryRow(ctx, `
			UPDATE stocktake SET status = 'completed', completed_at = NOW(), updated_at = NOW(),
			       total_scanned = $2, total_missing = $3, total_surplus = $4
			 WHERE id = $1 AND `+stocktakeWritable+` RETURNING `+stocktakeSelectColumns,
			current.ID, current.TotalScanned, current.TotalMissing, current.TotalSurplus))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("complete stocktake: %w", err)
		}
		return nil
	})
	return st, err
}

// immutableError maps the refusal of reject_completed_stocktake_mutation
// (23514 on stocktake_immutable, 000267) to ErrAlreadyCompleted; other
// errors are returned unchanged.
func immutableError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "stocktake_immutable" {
		return ErrAlreadyCompleted
	}
	return err
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
		&item.ConsumableID,
		&item.Quantity,
		&item.LocationID,
		&item.ClientID,
		&item.SiteID,
		&item.CIID,
		&item.ScannedBy,
		&item.ScanMethod,
		&item.ScanResult,
		&item.LegacyScanResult,
		&item.Code,
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
