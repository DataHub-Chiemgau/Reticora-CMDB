package asset

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
				return fmt.Errorf("not found")
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
		return nil
	})
}

// Update modifies an existing asset.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Asset, error) {
	var asset *Asset

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
					return fmt.Errorf("not found")
				}
				return fmt.Errorf("get asset for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		// Redundant with row-level security, kept as defense in depth.
		query := fmt.Sprintf("UPDATE asset SET %s WHERE id = $1 AND organization_id = $%d RETURNING %s", strings.Join(setClauses, ", "), argPos, assetSelectColumns)
		args = append(args, orgID)
		var err error
		asset, err = scanAsset(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update asset: %w", err)
		}
		return nil
	})

	return asset, err
}

// Delete deletes an asset. The asset table has no deleted_at column, so this is a hard delete.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM asset WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
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
