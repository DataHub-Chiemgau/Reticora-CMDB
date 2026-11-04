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
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const assetSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(ci_id::text, ''),
	inventory_no,
	name,
	category,
	status,
	COALESCE(manufacturer, ''),
	COALESCE(model, ''),
	purchase_date,
	COALESCE(purchase_price::text, ''),
	COALESCE(currency, ''),
	warranty_until,
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
	COALESCE(site_id::text, ''),
	version
`

// physicalAssetModelID is the system lifecycle model physical_asset
// (000055): the model of an asset whose type has no effective asset model.
const physicalAssetModelID = "00000000-0000-0000-0000-000000000101"

// constraintFields maps the unique indexes and references of asset onto the
// request field they protect (api.FieldDBError): a taken inventory number,
// barcode or RFID tag is 409 with the field (WP-271). The unique index of
// the inventory number kept its name idx_asset_tag_org (WP-738).
var constraintFields = map[string]string{
	"idx_asset_tag_org":        "inventory_no",
	"idx_asset_barcode_org":    "barcode",
	"idx_asset_rfid":           "rfid_tag",
	"asset_supplier_id_fkey":   "supplier_id",
	"asset_contract_id_fkey":   "contract_id",
	"asset_owner_user_id_fkey": "owner_user_id",
	"asset_owner_team_id_fkey": "owner_team_id",
	// A client that differs from the client of a linked CI (WP-272,
	// trigger ci_asset_client of migration 000235).
	"ci_asset_client": "client_id",
}

// primaryCIConstraintFields are constraintFields for a write of the primary
// CI (asset.ci_id): the trigger asset_sync_primary_ci links the CI, so a
// client conflict of that link is one of primary_ci_id.
var primaryCIConstraintFields = func() map[string]string {
	out := make(map[string]string, len(constraintFields))
	for k, v := range constraintFields {
		out[k] = v
	}
	out["ci_asset_client"] = fieldPrimaryCI
	return out
}()

// assetFieldDBError maps a constraint violation onto its request field by
// fields.
func assetFieldDBError(err error, fields map[string]string) error {
	mapped := api.FieldDBError(err, fields)
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
	// outbox writes asset.created, asset.changed and asset.deleted in the
	// write transaction (EVT-03, WP-740); nil writes none.
	outbox *events.Outbox
}

// WithEvents attaches the event outbox: every create, change and delete
// writes its asset.* event last in its transaction, after the trail (K20).
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// NewPGRepository creates a new PostgreSQL-backed asset repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// requireCI fails with a 422 field error on primary_ci_id (the API name of
// the column ci_id, E-122) when the CI to link is no
// UUID or not visible under the caller's scope. The foreign key on ci is
// checked without row level security: a CI of another organization or
// client was linked (201) and an unknown one refused with the constraint
// text, so the two could be told apart (TEN-04, API-03, F-047).
func requireCI(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	if nilIfEmpty(ciID) == nil {
		return nil
	}
	if err := api.RequireUUID(fieldPrimaryCI, ciID); err != nil {
		return err
	}
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1 AND organization_id = $2)", ciID, orgID).Scan(&visible); err != nil {
		return fmt.Errorf("check ci of asset: %w", err)
	}
	if !visible {
		return &api.FieldError{Field: fieldPrimaryCI, Message: "CI not found", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// fieldPrimaryCI is the API name of the primary CI (column asset.ci_id,
// E-122).
const fieldPrimaryCI = "primary_ci_id"

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

	if err := checkSort(filter); err != nil {
		return nil, 0, err
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"true"}
		args := make([]any, 0, 5+len(filter.Filters))
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
			whereParts = append(whereParts, fmt.Sprintf("(name ILIKE $%d OR inventory_no ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}
		// filter[field][op] (API-02): the field is the column of the
		// whitelist filterColumns.
		for _, f := range filter.Filters {
			kind, ok := filterColumns[f.Field]
			if !ok {
				return &api.FieldError{Field: "filter[" + f.Field + "]", Status: http.StatusUnprocessableEntity,
					Message: fmt.Sprintf("unknown filter field %q", f.Field)}
			}
			f.Kind = kind
			clause, filterArgs := f.SQL(f.Field, argPos)
			whereParts = append(whereParts, clause)
			args = append(args, filterArgs...)
			argPos += len(filterArgs)
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM asset WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count assets: %w", err)
		}

		sortField, sortDirection := NormalizeSort(filter)
		sortColumn := sortColumns[sortField]

		// Keyset pagination: when a cursor is supplied it replaces the OFFSET
		// so page boundaries stay stable while rows are inserted or removed.
		listWhere := whereClause
		listArgs := append([]any{}, args...)
		if page.Cursor != nil {
			if !page.Cursor.Matches(sortField, sortDirection) {
				return fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
			}
			listWhere += " AND " + api.KeysetClause(sortColumn.expr, sortColumn.cast, sortDirection, argPos)
			listArgs = append(listArgs, page.Cursor.Value, page.Cursor.ID)
			argPos += 2
		}

		var query string
		if page.Cursor != nil {
			listArgs = append(listArgs, page.Limit)
			query = fmt.Sprintf(
				"SELECT %s FROM asset WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				assetSelectColumns, listWhere, sortColumn.expr, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM asset WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				assetSelectColumns, listWhere, sortColumn.expr, strings.ToUpper(sortDirection),
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
// inventory conversion); the API has no location on create. The free text
// supplier is not written: the trigger asset_mirror_legacy_columns mirrors
// supplier.name of supplier_id (E-41, WP-739).
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
				organization_id, client_id, ci_id, inventory_no, name, category, status,
				purchase_date, purchase_price, currency, warranty_until, invoice_number,
				serial_number, location, notes, custom_fields, rfid_tag, barcode,
				manufacturer, model, cost_center, supplier_id, contract_id, owner_user_id, owner_team_id,
				location_id, asset_type_id, lifecycle_state
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9::numeric, $10, $11, $12,
				$13, $14, $15, $16, $17, $18,
				$19, $20, $21, $22, $23, $24, $25,
				$26, $27::uuid,
				(SELECT s.key FROM lifecycle_state s
				 WHERE s.definition_id = COALESCE(lifecycle_model_effective($27::uuid, 'asset'), $28::uuid) AND s.is_initial
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
			nilIfEmpty(a.PurchaseCost),
			nilIfEmpty(a.Currency),
			nilIfEmpty(a.WarrantyEnd),
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
			return fmt.Errorf("create asset: %w", assetFieldDBError(err, primaryCIConstraintFields))
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
		if recErr := recordChange(ctx, tx, a.OrganizationID, a.ID, "create", "", nil, after, created.Version); recErr != nil {
			return recErr
		}
		return r.emit(ctx, tx, events.AssetCreated, created, nil)
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
		if preErr := checkPrecondition(ctx, tx, orgID, current, req); preErr != nil {
			return preErr
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
		addNonNullStringField("inventory_no", req.AssetTag)
		addNonNullStringField("category", req.Category)
		addNonNullStringField("status", req.Status)
		addStringField("manufacturer", req.Manufacturer)
		addStringField("model", req.Model)
		addStringField("purchase_date", req.PurchaseDate)
		if req.PurchaseCost != nil {
			setClauses = append(setClauses, fmt.Sprintf("purchase_price = $%d::numeric", argPos))
			args = append(args, nilIfEmpty(*req.PurchaseCost))
			argPos++
		}
		// An empty currency is stored as NULL; the trigger
		// asset_default_currency sets the currency of the organization.
		addStringField("currency", req.Currency)
		addStringField("warranty_until", req.WarrantyEnd)
		addStringField("cost_center", req.CostCenter)
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

		setClauses = append(setClauses, "updated_at = NOW()")
		// Redundant with row-level security, kept as defense in depth.
		query := fmt.Sprintf("UPDATE asset SET %s WHERE id = $1 AND organization_id = $%d RETURNING %s", strings.Join(setClauses, ", "), argPos, assetSelectColumns)
		args = append(args, orgID)
		asset, err = scanAsset(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			fields := constraintFields
			if req.CIID != nil {
				fields = primaryCIConstraintFields
			}
			return fmt.Errorf("update asset: %w", assetFieldDBError(err, fields))
		}
		return r.recordUpdate(ctx, tx, orgID, current, asset)
	})

	return asset, err
}

// Delete deletes an asset. The asset table has no deleted_at column, so this is a hard delete.
// The history keeps the deletion with the asset as it was (WP-516). An
// asset with stock movements or a disposal record (both append-only) is
// ErrDeleteBlocked (409, E-121): its end is a lifecycle state.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := r.getLocked(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		var blocked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM asset_movement WHERE asset_id = $1)
			OR EXISTS (SELECT 1 FROM disposal_record WHERE asset_id = $1)`, id).Scan(&blocked); err != nil {
			return fmt.Errorf("check records of asset: %w", err)
		}
		if blocked {
			return ErrDeleteBlocked
		}
		before, err := changeForm(current)
		if err != nil {
			return err
		}
		cmdTag, err := tx.Exec(ctx, "DELETE FROM asset WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			if appendOnly(err) {
				// A record the caller's scope does not show.
				return ErrDeleteBlocked
			}
			return fmt.Errorf("delete asset: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if recErr := recordChange(ctx, tx, orgID, id, "delete", "", before, nil, current.Version); recErr != nil {
			return recErr
		}
		return r.emit(ctx, tx, events.AssetDeleted, current, nil)
	})
}

// appendOnly reports whether err is the refusal of an append-only guard
// (asset_movement, disposal_record) a delete cascaded into.
func appendOnly(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "P0001" && strings.Contains(pgErr.Message, "append-only")
}

// checkPrecondition enforces If-Match on a PATCH (API-07, API-03; pattern
// of the CI, WP-061): a missing If-Match fails when the organization
// requires one (412); a version the asset never had fails (412); an older
// version fails with 409 only when a field of the request changed since:
// a trail row of a later version names it, or a later version has no
// trail row at all (a writer outside the asset package, counted by the
// trigger asset_version_bump), which counts as a change of every field of
// the request.
func checkPrecondition(ctx context.Context, tx pgx.Tx, orgID string, current *Asset, req *UpdateRequest) error {
	if req.IfMatch == nil {
		var required bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE((settings->>'require_if_match')::boolean, false) FROM organization WHERE id = $1`,
			orgID).Scan(&required); err != nil {
			return fmt.Errorf("read if-match setting: %w", err)
		}
		if required {
			return ErrPreconditionRequired
		}
		return nil
	}
	switch {
	case *req.IfMatch == current.Version, *req.IfMatch == api.IfMatchAny:
		return nil
	case *req.IfMatch > current.Version || *req.IfMatch < 1:
		return ErrPreconditionFailed
	}
	requested := req.requestedFields()
	if len(requested) == 0 {
		return nil
	}
	var traced int64
	var fields []string
	if err := tx.QueryRow(ctx, `SELECT COUNT(DISTINCT entity_version),
			COALESCE(array_agg(DISTINCT field_name ORDER BY field_name) FILTER (WHERE field_name = ANY($4)), '{}')
		FROM entity_change
		WHERE entity_type = 'asset' AND entity_id = $1 AND entity_version > $2 AND entity_version <= $3`,
		current.ID, *req.IfMatch, current.Version, requested).Scan(&traced, &fields); err != nil {
		return fmt.Errorf("read changes since if-match: %w", err)
	}
	if traced < current.Version-*req.IfMatch {
		fields = requested
	}
	if len(fields) > 0 {
		return &ConflictError{Fields: fields, Current: current.Version}
	}
	return nil
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

// changeForm returns the asset in the JSON form of the API for its
// history; the version is a column of the trail (entity_version), not a
// field of it.
func changeForm(item *Asset) (map[string]any, error) {
	generic, err := pii.Generic(item)
	if err != nil {
		return nil, fmt.Errorf("encode asset change: %w", err)
	}
	obj, _ := generic.(map[string]any)
	delete(obj, "version")
	delete(obj, "primary_ci")
	return obj, nil
}

// untracked are the fields of an asset that are no change of their own.
var untracked = map[string]bool{"updated_at": true, "version": true}

// recordUpdate writes the trail of an update from before to after (one
// row per changed field with the version after it) and, when a field
// changed, asset.changed with the changed fields, last in the
// transaction (K20).
func (r *PGRepository) recordUpdate(ctx context.Context, tx pgx.Tx, orgID string, before, after *Asset) error {
	beforeForm, err := changeForm(before)
	if err != nil {
		return err
	}
	afterForm, err := changeForm(after)
	if err != nil {
		return err
	}
	changed, err := recordFieldChanges(ctx, tx, orgID, after.ID, beforeForm, afterForm, after.Version)
	if err != nil || len(changed) == 0 {
		return err
	}
	return r.emit(ctx, tx, events.AssetChanged, after, changed)
}

// emit writes the asset event typ (EVT-03, WP-740) with the asset in the
// JSON form of the API, its site and, for asset.changed, the changed
// fields; nothing without an outbox.
func (r *PGRepository) emit(ctx context.Context, tx pgx.Tx, typ string, a *Asset, changed []string) error {
	if r.outbox == nil {
		return nil
	}
	data, err := eventData(a, changed)
	if err != nil {
		return err
	}
	if _, appendErr := r.outbox.Append(ctx, tx, &events.Event{OrgID: a.OrganizationID, Type: typ, EntityID: a.ID, Data: data}); appendErr != nil {
		return fmt.Errorf("asset event %s: %w", typ, appendErr)
	}
	return nil
}

// eventData is the data of an asset event: the frame events.AssetData
// (id, client_id, site_id) with the members of the asset DTO, its version
// and, for asset.changed, changed_fields (events.AssetChangedData).
func eventData(a *Asset, changed []string) (map[string]any, error) {
	generic, err := pii.Generic(a)
	if err != nil {
		return nil, fmt.Errorf("encode asset event: %w", err)
	}
	data, _ := generic.(map[string]any)
	delete(data, "primary_ci")
	if a.SiteID != "" {
		data["site_id"] = a.SiteID
	}
	if changed != nil {
		data["changed_fields"] = changed
	}
	return data, nil
}

// recordFieldChanges writes one update row per field that differs between
// before and after (JSON forms of the asset), in the order of the field
// names, with the version the write left; it returns the changed fields.
func recordFieldChanges(ctx context.Context, tx pgx.Tx, orgID, id string, before, after map[string]any, version int64) ([]string, error) {
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
	var changed []string
	for _, name := range names {
		if reflect.DeepEqual(before[name], after[name]) {
			continue
		}
		if err := recordChange(ctx, tx, orgID, id, "update", name, before[name], after[name], version); err != nil {
			return nil, err
		}
		changed = append(changed, name)
	}
	return changed, nil
}

// recordChange appends a change of the asset to the entity_change trail in
// the transaction of the write (WP-516), with the acting user; personal
// values are stored as vault surrogates (SEC-05), as in the CI history.
// version is the asset version the write left (entity_version, WP-740).
func recordChange(ctx context.Context, tx pgx.Tx, orgID, id, changeType, field string, oldValue, newValue any, version int64) error {
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
		INSERT INTO entity_change (organization_id, entity_type, entity_id, actor_id, change_type, field_name, old_value, new_value, entity_version)
		VALUES ($1, 'asset', $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8)`,
		orgID, id, actor, changeType, nilIfEmpty(field), values[0], values[1], version); err != nil {
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
		&item.Version,
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

// DATEVRow is an asset as the DATEV export of the asset accounting reads it
// (EXP-01, WP-310): the canonical asset columns with the names of its
// supplier and location (and the number of its contract when asked for).
// The purchase price is the numeric text of the column (no float
// rounding), the dates are YYYY-MM-DD; empty means no value.
type DATEVRow struct {
	ID             string
	ClientID       string
	SiteID         string
	AssetTypeID    string
	InventoryNo    string
	Name           string
	Manufacturer   string
	Model          string
	SerialNumber   string
	InvoiceNumber  string
	PurchaseDate   string
	PurchasePrice  string
	Currency       string
	Supplier       string
	CostCenter     string
	WarrantyUntil  string
	Location       string
	LifecycleState string
	Contract       string
	CreatedAt      time.Time
}

// DATEVQuery is one batch of the DATEV export: the filters of the job,
// whether the contract number is joined, and the keyset position after
// which the batch starts (empty AfterID: the first batch).
type DATEVQuery struct {
	ClientID       string
	LifecycleState string
	WithContract   bool
	AfterCreatedAt time.Time
	AfterID        string
	Limit          int
}

// datevSelect reads the columns of DATEVRow; %s is the contract column.
const datevSelect = `SELECT a.id::text, COALESCE(a.client_id::text, ''), COALESCE(a.site_id::text, ''),
	COALESCE(a.asset_type_id::text, ''), a.inventory_no, a.name, COALESCE(a.manufacturer, ''),
	COALESCE(a.model, ''), COALESCE(a.serial_number, ''), COALESCE(a.invoice_number, ''),
	COALESCE(to_char(a.purchase_date, 'YYYY-MM-DD'), ''), COALESCE(a.purchase_price::text, ''),
	COALESCE(a.currency, ''), COALESCE(s.name, ''), COALESCE(a.cost_center, ''),
	COALESCE(to_char(a.warranty_until, 'YYYY-MM-DD'), ''), COALESCE(l.name, ''),
	COALESCE(a.lifecycle_state, ''), %s, a.created_at
	FROM asset a
	LEFT JOIN supplier s ON s.id = a.supplier_id AND s.organization_id = a.organization_id
	LEFT JOIN location l ON l.id = a.location_id AND l.organization_id = a.organization_id`

// ListDATEV returns one batch of assets for the DATEV export in the order
// (created_at, id), read in database.WithRequestTenant with the scope of
// ctx (the scope snapshot of the job) and without count(*) (E-17).
// supplier and location are joined for their names under RLS (a row the
// scope does not see stays empty), contract only for WithContract. It is
// not part of Repository; only the export reads it (export.AssetSource).
func (r *PGRepository) ListDATEV(ctx context.Context, orgID string, q *DATEVQuery) ([]DATEVRow, error) {
	var out []DATEVRow
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"a.organization_id = $1"}
		args := []any{orgID}
		if q.ClientID != "" {
			args = append(args, q.ClientID)
			where = append(where, fmt.Sprintf("a.client_id = $%d", len(args)))
		}
		if q.LifecycleState != "" {
			args = append(args, q.LifecycleState)
			where = append(where, fmt.Sprintf("a.lifecycle_state = $%d", len(args)))
		}
		if q.AfterID != "" {
			args = append(args, q.AfterCreatedAt, q.AfterID)
			where = append(where, fmt.Sprintf("(a.created_at, a.id) > ($%d, $%d::uuid)", len(args)-1, len(args)))
		}
		contractCol, contractJoin := "''", ""
		if q.WithContract {
			contractCol = "COALESCE(c.number, '')"
			contractJoin = "\n\tLEFT JOIN contract c ON c.id = a.contract_id AND c.organization_id = a.organization_id"
		}
		args = append(args, q.Limit)
		query := fmt.Sprintf(datevSelect, contractCol) + contractJoin + "\n\tWHERE " + strings.Join(where, " AND ") +
			fmt.Sprintf("\n\tORDER BY a.created_at, a.id LIMIT $%d", len(args))
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list assets for datev: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var row DATEVRow
			if serr := rows.Scan(&row.ID, &row.ClientID, &row.SiteID, &row.AssetTypeID, &row.InventoryNo, &row.Name,
				&row.Manufacturer, &row.Model, &row.SerialNumber, &row.InvoiceNumber, &row.PurchaseDate, &row.PurchasePrice,
				&row.Currency, &row.Supplier, &row.CostCenter, &row.WarrantyUntil, &row.Location, &row.LifecycleState,
				&row.Contract, &row.CreatedAt); serr != nil {
				return fmt.Errorf("scan asset for datev: %w", serr)
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}
