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
	COALESCE(manufacturer, ''),
	COALESCE(model, ''),
	purchase_date,
	COALESCE(purchase_cost, 0),
	COALESCE(currency, ''),
	warranty_end,
	COALESCE(cost_center, ''),
	COALESCE(supplier, ''),
	COALESCE(supplier_id::text, ''),
	COALESCE(contract_id::text, ''),
	COALESCE(owner_user_id::text, ''),
	COALESCE(owner_team_id::text, ''),
	COALESCE(invoice_number, ''),
	COALESCE(serial_number, ''),
	COALESCE(rfid_tag, ''),
	COALESCE(barcode, ''),
	COALESCE(location, ''),
	COALESCE(location_id::text, ''),
	COALESCE(lifecycle_state, ''),
	COALESCE(notes, ''),
	custom_fields,
	created_at,
	updated_at,
	COALESCE(asset_type_id::text, ''),
	COALESCE(site_id::text, '')
`

// physicalAssetModelID is the system lifecycle model physical_asset
// (000055): the model of an asset whose type has no effective asset model.
const physicalAssetModelID = "00000000-0000-0000-0000-000000000101"

// constraintFields maps the unique indexes and references of asset onto the
// request field they protect (api.FieldDBError): a taken inventory number,
// barcode or RFID tag is 409 with the field (WP-271).
var constraintFields = map[string]string{
	"idx_asset_tag_org":        "asset_tag",
	"idx_asset_barcode_org":    "barcode",
	"idx_asset_rfid":           "rfid_tag",
	"asset_supplier_id_fkey":   "supplier_id",
	"asset_contract_id_fkey":   "contract_id",
	"asset_owner_user_id_fkey": "owner_user_id",
	"asset_owner_team_id_fkey": "owner_team_id",
}

// assetDBError maps a constraint violation onto its request field.
func assetDBError(err error) error {
	mapped := api.FieldDBError(err, constraintFields)
	var fe *api.FieldError
	if errors.As(mapped, &fe) && fe.Status == 0 {
		// A reference that does not hold is 422 like the check before
		// the write (refNotUsable).
		fe.Status = http.StatusUnprocessableEntity
	}
	return mapped
}

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

// Messages of the reference checks: the same text whether the object does
// not exist, belongs to another organization, is outside the caller's scope
// or belongs to another client (TEN-04).
const (
	msgSupplierNotUsable = "supplier not found or not usable for this asset"
	msgContractNotUsable = "contract not found or not usable for this asset"
	msgUserNotUsable     = "user not found in this organization"
	msgTeamNotUsable     = "team not found in this organization"
)

// assetRefs are the references of a write; nil is not written.
type assetRefs struct {
	supplierID, contractID, ownerUserID, ownerTeamID *string
}

// refNotUsable is the 422 field error of a reference.
func refNotUsable(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkRefs verifies the references of a write inside the tenant
// transaction (RLS and the client scope apply): supplier and contract must
// be visible and org-wide or of the asset's client, owner user and team of
// the organization. The foreign keys are only the fallback, their checks
// bypass row level security. Each violation is 422 on the field without
// telling whether the object exists.
func checkRefs(ctx context.Context, tx pgx.Tx, orgID, clientID string, refs *assetRefs) error {
	clientOK := func(owner *string) bool { return owner == nil || *owner == clientID }
	for _, ref := range []struct {
		field, message, table string
		value                 *string
	}{
		{"supplier_id", msgSupplierNotUsable, "supplier", refs.supplierID},
		{"contract_id", msgContractNotUsable, "contract", refs.contractID},
	} {
		if ref.value == nil || nilIfEmpty(*ref.value) == nil {
			continue
		}
		if !api.IsUUID(*ref.value) {
			return refNotUsable(ref.field, ref.message)
		}
		var owner *string
		err := tx.QueryRow(ctx, "SELECT client_id::text FROM "+ref.table+" WHERE organization_id = $1 AND id = $2",
			orgID, *ref.value).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !clientOK(owner) {
			return refNotUsable(ref.field, ref.message)
		}
		if err != nil {
			return fmt.Errorf("check %s of asset: %w", ref.table, err)
		}
	}
	for _, ref := range []struct {
		field, message, table string
		value                 *string
	}{
		{"owner_user_id", msgUserNotUsable, "app_user", refs.ownerUserID},
		{"owner_team_id", msgTeamNotUsable, "team", refs.ownerTeamID},
	} {
		if ref.value == nil || nilIfEmpty(*ref.value) == nil {
			continue
		}
		if !api.IsUUID(*ref.value) {
			return refNotUsable(ref.field, ref.message)
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+ref.table+" WHERE organization_id = $1 AND id = $2)",
			orgID, *ref.value).Scan(&exists); err != nil {
			return fmt.Errorf("check %s of asset: %w", ref.table, err)
		}
		if !exists {
			return refNotUsable(ref.field, ref.message)
		}
	}
	return nil
}

// List returns paginated assets filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Asset, int, error) {
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

// Create inserts a new asset. A missing currency is the currency of the
// organization (trigger asset_default_currency); the asset starts in the
// initial state of its effective lifecycle model (physical_asset without
// one, E-120). LocationID is written for internal callers only (the
// inventory conversion); the API has no location on create.
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
		if err := checkRefs(ctx, tx, a.OrganizationID, a.ClientID, &assetRefs{
			supplierID: &a.SupplierID, contractID: &a.ContractID, ownerUserID: &a.OwnerUserID, ownerTeamID: &a.OwnerTeamID,
		}); err != nil {
			return err
		}

		query := `
			INSERT INTO asset (
				organization_id, client_id, ci_id, asset_tag, name, category, status,
				purchase_date, purchase_cost, currency, warranty_end, supplier, invoice_number,
				serial_number, location, notes, custom_fields, rfid_tag, barcode,
				manufacturer, model, cost_center, supplier_id, contract_id, owner_user_id, owner_team_id,
				location_id, asset_type_id, lifecycle_state
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18, $19,
				$20, $21, $22, $23, $24, $25, $26,
				$27, $28::uuid,
				(SELECT s.key FROM lifecycle_state s
				 WHERE s.definition_id = COALESCE(lifecycle_model_effective($28::uuid, 'asset'), $29::uuid) AND s.is_initial
				 ORDER BY s.sort_order, s.key LIMIT 1)
			)
			RETURNING id::text
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
			nilIfEmpty(a.Manufacturer),
			nilIfEmpty(a.Model),
			nilIfEmpty(a.CostCenter),
			nilIfEmpty(a.SupplierID),
			nilIfEmpty(a.ContractID),
			nilIfEmpty(a.OwnerUserID),
			nilIfEmpty(a.OwnerTeamID),
			nilIfEmpty(a.LocationID),
			nilIfEmpty(a.AssetTypeID),
			physicalAssetModelID,
		).Scan(&a.ID); err != nil {
			return fmt.Errorf("create asset: %w", assetDBError(err))
		}
		// The row as stored: currency, lifecycle state and site come from
		// the database.
		created, err := r.getLocked(ctx, tx, a.OrganizationID, a.ID, "")
		if err != nil {
			return err
		}
		*a = *created
		after, err := changeForm(created)
		if err != nil {
			return err
		}
		return recordChange(ctx, tx, a.OrganizationID, a.ID, "create", "", nil, after)
	})
}

// Update modifies an existing asset: the row is locked (FOR UPDATE, which
// applies asset_scoped_update), req.Check runs on it, then the references
// are checked against the asset's client.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req *UpdateRequest) (*Asset, error) {
	var asset *Asset

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := r.getLocked(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		if req.Check != nil {
			if checkErr := req.Check(current); checkErr != nil {
				return checkErr
			}
		}
		if req.CIID != nil {
			if ciErr := requireCI(ctx, tx, orgID, *req.CIID); ciErr != nil {
				return ciErr
			}
		}
		if refErr := checkRefs(ctx, tx, orgID, current.ClientID, &assetRefs{
			supplierID: req.SupplierID, contractID: req.ContractID, ownerUserID: req.OwnerUserID, ownerTeamID: req.OwnerTeamID,
		}); refErr != nil {
			return refErr
		}
		setClauses := make([]string, 0, 24)
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
		addStringField("manufacturer", req.Manufacturer)
		addStringField("model", req.Model)
		addStringField("purchase_date", req.PurchaseDate)
		if req.PurchaseCost != nil {
			setClauses = append(setClauses, fmt.Sprintf("purchase_cost = $%d", argPos))
			args = append(args, *req.PurchaseCost)
			argPos++
		}
		// An empty currency is stored as NULL; the trigger
		// asset_default_currency sets the currency of the organization.
		addStringField("currency", req.Currency)
		addStringField("warranty_end", req.WarrantyEnd)
		addStringField("cost_center", req.CostCenter)
		addStringField("supplier", req.Supplier)
		addStringField("supplier_id", req.SupplierID)
		addStringField("contract_id", req.ContractID)
		addStringField("owner_user_id", req.OwnerUserID)
		addStringField("owner_team_id", req.OwnerTeamID)
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
			asset = current
			return nil
		}

		// The row before the update for the changed fields of the history
		// (WP-516).
		before, err := changeForm(current)
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
			return fmt.Errorf("update asset: %w", assetDBError(err))
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
		current, err := r.getLocked(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		before, err := changeForm(current)
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

// getLocked reads the asset id in the transaction; lock is the locking
// clause of the read (FOR UPDATE) or empty.
func (r *PGRepository) getLocked(ctx context.Context, tx pgx.Tx, orgID, id, lock string) (*Asset, error) {
	query := fmt.Sprintf("SELECT %s FROM asset WHERE id = $1 AND organization_id = $2 %s", assetSelectColumns, lock)
	item, err := scanAsset(tx.QueryRow(ctx, query, id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return item, nil
}

// changeForm returns the asset in the JSON form of the API for its history.
func changeForm(item *Asset) (map[string]any, error) {
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
		&item.Manufacturer,
		&item.Model,
		&purchaseDate,
		&item.PurchaseCost,
		&item.Currency,
		&warrantyEnd,
		&item.CostCenter,
		&item.Supplier,
		&item.SupplierID,
		&item.ContractID,
		&item.OwnerUserID,
		&item.OwnerTeamID,
		&item.InvoiceNumber,
		&item.SerialNumber,
		&item.RFIDTag,
		&item.Barcode,
		&item.Location,
		&item.LocationID,
		&item.LifecycleState,
		&item.Notes,
		&item.CustomFields,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.AssetTypeID,
		&item.SiteID,
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
