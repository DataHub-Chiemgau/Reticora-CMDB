package asset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const assetSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(ci_id::text, ''),
	asset_tag,
	name,
	category,
	status,
	purchase_date,
	COALESCE(purchase_cost, 0),
	COALESCE(currency, ''),
	warranty_end,
	COALESCE(supplier, ''),
	COALESCE(invoice_number, ''),
	COALESCE(serial_number, ''),
	COALESCE(rfid_tag, ''),
	COALESCE(barcode, ''),
	COALESCE(location, ''),
	COALESCE(location_id::text, ''),
	COALESCE(notes, ''),
	custom_fields,
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed asset repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// requireCI fails with a 422 field error on ci_id when the CI to link is no
// UUID or not visible under the caller's scope. The foreign key on ci is
// checked without row level security: a CI of another organization or
// client was linked (201) and an unknown one refused with the constraint
// text, so the two could be told apart (TEN-04, API-03, F-047).
func requireCI(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	if nilIfEmpty(ciID) == nil {
		return nil
	}
	if err := api.RequireUUID("ci_id", ciID); err != nil {
		return err
	}
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1 AND organization_id = $2)", ciID, orgID).Scan(&visible); err != nil {
		return fmt.Errorf("check ci of asset: %w", err)
	}
	if !visible {
		return &api.FieldError{Field: "ci_id", Message: "CI not found", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// List returns paginated assets filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Asset, int, error) {
	var assets []Asset
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
		if filter.Category != "" {
			whereParts = append(whereParts, fmt.Sprintf("category = $%d", argPos))
			args = append(args, filter.Category)
			argPos++
		}
		if filter.ClientID != "" {
			whereParts = append(whereParts, fmt.Sprintf("client_id = $%d", argPos))
			args = append(args, filter.ClientID)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("(name ILIKE $%d OR asset_tag ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asset WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count assets: %w", err)
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
				"SELECT %s FROM asset WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				assetSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM asset WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				assetSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list assets: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanAsset(rows)
			if err != nil {
				return fmt.Errorf("scan asset: %w", err)
			}
			assets = append(assets, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate assets: %w", err)
		}
		return nil
	})

	return assets, total, err
}

// GetByID retrieves a single asset by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Asset, error) {
	var asset *Asset

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM asset WHERE id = $1 AND organization_id = $2", assetSelectColumns)
		var err error
		asset, err = scanAsset(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get asset by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return asset, nil
}

// Create inserts a new asset.
func (r *PGRepository) Create(ctx context.Context, a *Asset) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if a.CustomFields == nil {
			a.CustomFields = make(map[string]any)
		}
		if nilIfEmpty(a.ClientID) != nil {
			if err := api.RequireUUID("client_id", a.ClientID); err != nil {
				return err
			}
		}
		if err := requireCI(ctx, tx, a.OrganizationID, a.CIID); err != nil {
			return err
		}

		query := `
			INSERT INTO asset (
				organization_id, client_id, ci_id, asset_tag, name, category, status,
				purchase_date, purchase_cost, currency, warranty_end, supplier, invoice_number,
				serial_number, location, notes, custom_fields, rfid_tag, barcode
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18, $19
			)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			a.OrganizationID,
			nilIfEmpty(a.ClientID),
			nilIfEmpty(a.CIID),
			a.AssetTag,
			a.Name,
			a.Category,
			a.Status,
			nilIfEmpty(a.PurchaseDate),
			a.PurchaseCost,
			nilIfEmpty(a.Currency),
			nilIfEmpty(a.WarrantyEnd),
			nilIfEmpty(a.Supplier),
			nilIfEmpty(a.InvoiceNumber),
			nilIfEmpty(a.SerialNumber),
			nilIfEmpty(a.Location),
			nilIfEmpty(a.Notes),
			a.CustomFields,
			nilIfEmpty(a.RFIDTag),
			nilIfEmpty(a.Barcode),
		).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return fmt.Errorf("create asset: %w", err)
		}
		a.CreatedAt = a.CreatedAt.UTC()
		a.UpdatedAt = a.UpdatedAt.UTC()
		created, err := r.getForChange(ctx, tx, a.OrganizationID, a.ID)
		if err != nil {
			return err
		}
		return recordChange(ctx, tx, a.OrganizationID, a.ID, "create", "", nil, created)
	})
}

// Update modifies an existing asset.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Asset, error) {
	var asset *Asset

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if req.CIID != nil {
			if err := requireCI(ctx, tx, orgID, *req.CIID); err != nil {
				return err
			}
		}
		setClauses := make([]string, 0, 14)
		args := []any{id}
		argPos := 2

		addStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, nilIfEmpty(*value))
			argPos++
		}
		addNonNullStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, *value)
			argPos++
		}

		addNonNullStringField("name", req.Name)
		addNonNullStringField("category", req.Category)
		addNonNullStringField("status", req.Status)
		addStringField("purchase_date", req.PurchaseDate)
		if req.PurchaseCost != nil {
			setClauses = append(setClauses, fmt.Sprintf("purchase_cost = $%d", argPos))
			args = append(args, *req.PurchaseCost)
			argPos++
		}
		addStringField("currency", req.Currency)
		addStringField("warranty_end", req.WarrantyEnd)
		addStringField("supplier", req.Supplier)
		addStringField("invoice_number", req.InvoiceNumber)
		addStringField("serial_number", req.SerialNumber)
		addStringField("rfid_tag", req.RFIDTag)
		addStringField("barcode", req.Barcode)
		addStringField("location", req.Location)
		addStringField("notes", req.Notes)
		addStringField("ci_id", req.CIID)
		if req.CustomFields != nil {
			setClauses = append(setClauses, fmt.Sprintf("custom_fields = COALESCE(custom_fields, '{}'::jsonb) || $%d", argPos))
			args = append(args, req.CustomFields)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM asset WHERE id = $1 AND organization_id = $2", assetSelectColumns)
			var err error
			asset, err = scanAsset(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return ErrNotFound
				}
				return fmt.Errorf("get asset for update: %w", err)
			}
			return nil
		}

		// The row before the update, locked, for the changed fields of the
		// history (WP-516).
		before, err := r.getForChange(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		setClauses = append(setClauses, "updated_at = NOW()")
		// Redundant with row-level security, kept as defense in depth.
		query := fmt.Sprintf("UPDATE asset SET %s WHERE id = $1 AND organization_id = $%d RETURNING %s", strings.Join(setClauses, ", "), argPos, assetSelectColumns)
		args = append(args, orgID)
		asset, err = scanAsset(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("update asset: %w", err)
		}
		after, err := pii.Generic(asset)
		if err != nil {
			return fmt.Errorf("encode asset change: %w", err)
		}
		return recordFieldChanges(ctx, tx, orgID, id, before, after)
	})

	return asset, err
}

// Delete deletes an asset. The asset table has no deleted_at column, so this is a hard delete.
// The history keeps the deletion with the asset as it was (WP-516).
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := r.getForChange(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		cmdTag, err := tx.Exec(ctx, "DELETE FROM asset WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return recordChange(ctx, tx, orgID, id, "delete", "", before, nil)
	})
}

// getForChange reads the asset id in the JSON form of the API for its
// history; lock is the locking clause of the read (FOR UPDATE), if any.
func (r *PGRepository) getForChange(ctx context.Context, tx pgx.Tx, orgID, id string, lock ...string) (map[string]any, error) {
	query := fmt.Sprintf("SELECT %s FROM asset WHERE id = $1 AND organization_id = $2 %s", assetSelectColumns, strings.Join(lock, " "))
	item, err := scanAsset(tx.QueryRow(ctx, query, id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset for history: %w", err)
	}
	generic, err := pii.Generic(item)
	if err != nil {
		return nil, fmt.Errorf("encode asset change: %w", err)
	}
	obj, _ := generic.(map[string]any)
	return obj, nil
}

// untracked are the fields of an asset that are no change of their own.
var untracked = map[string]bool{"updated_at": true}

// recordFieldChanges writes one update row per field that differs between
// before and after (JSON forms of the asset), in the order of the field
// names.
func recordFieldChanges(ctx context.Context, tx pgx.Tx, orgID, id string, before map[string]any, afterValue any) error {
	after, _ := afterValue.(map[string]any)
	names := make([]string, 0, len(after))
	seen := map[string]bool{}
	for _, m := range []map[string]any{before, after} {
		for k := range m {
			if !seen[k] && !untracked[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if reflect.DeepEqual(before[name], after[name]) {
			continue
		}
		if err := recordChange(ctx, tx, orgID, id, "update", name, before[name], after[name]); err != nil {
			return err
		}
	}
	return nil
}

// recordChange appends a change of the asset to the entity_change trail in
// the transaction of the write (WP-516), with the acting user; personal
// values are stored as vault surrogates (SEC-05), as in the CI history.
func recordChange(ctx context.Context, tx pgx.Tx, orgID, id, changeType, field string, oldValue, newValue any) error {
	values := make([]any, 2)
	for i, v := range []any{oldValue, newValue} {
		if v == nil {
			continue
		}
		sealed, _, err := pii.SealFieldTx(ctx, tx, orgID, field, v)
		if err != nil {
			return fmt.Errorf("seal asset change value: %w", err)
		}
		raw, err := json.Marshal(sealed)
		if err != nil {
			return fmt.Errorf("encode asset change value: %w", err)
		}
		values[i] = string(raw)
	}
	var actor any
	if user := tenant.FromContext(ctx).UserID; api.IsUUID(user) {
		actor = user
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_change (organization_id, entity_type, entity_id, actor_id, change_type, field_name, old_value, new_value)
		VALUES ($1, 'asset', $2, $3, $4, $5, $6::jsonb, $7::jsonb)`,
		orgID, id, actor, changeType, nilIfEmpty(field), values[0], values[1]); err != nil {
		return fmt.Errorf("record asset change: %w", err)
	}
	return nil
}

type assetScanner interface {
	Scan(dest ...any) error
}

func scanAsset(scanner assetScanner) (*Asset, error) {
	item := &Asset{}
	var purchaseDate sql.NullTime
	var warrantyEnd sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.CIID,
		&item.AssetTag,
		&item.Name,
		&item.Category,
		&item.Status,
		&purchaseDate,
		&item.PurchaseCost,
		&item.Currency,
		&warrantyEnd,
		&item.Supplier,
		&item.InvoiceNumber,
		&item.SerialNumber,
		&item.RFIDTag,
		&item.Barcode,
		&item.Location,
		&item.LocationID,
		&item.Notes,
		&item.CustomFields,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if item.CustomFields == nil {
		item.CustomFields = make(map[string]any)
	}
	if purchaseDate.Valid {
		item.PurchaseDate = purchaseDate.Time.UTC().Format("2006-01-02")
	}
	if warrantyEnd.Valid {
		item.WarrantyEnd = warrantyEnd.Time.UTC().Format("2006-01-02")
	}
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
