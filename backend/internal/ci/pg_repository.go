package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ciSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
	COALESCE(room_id::text, ''),
	COALESCE(location_id::text, ''),
	ci_type_id::text,
	name,
	status,
	COALESCE(manufacturer, ''),
	COALESCE(model, ''),
	COALESCE(serial_number, ''),
	COALESCE(hardware_uuid::text, ''),
	COALESCE(host(management_ip), ''),
	COALESCE(primary_mac::text, ''),
	COALESCE(hostname, ''),
	COALESCE(fqdn, ''),
	COALESCE(os_name, ''),
	COALESCE(os_version, ''),
	COALESCE(firmware_version, ''),
	COALESCE(sys_object_id, ''),
	attributes,
	COALESCE(discovery_source, ''),
	first_seen_at,
	last_seen_at,
	is_manual,
	created_at,
	updated_at,
	deleted_at,
	version,
	COALESCE(asset_id::text, ''),
	COALESCE(parent_ci_id::text, ''),
	COALESCE(lifecycle_state, ''),
	health,
	health_updated_at,
	schema_version,
	COALESCE(merged_into_id::text, '')
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool     *pgxpool.Pool
	recorder audit.TxRecorder
	outbox   *events.Outbox
}

// WithEvents makes every CI mutation write its domain event into the
// outbox of its transaction (EVT-01, EVT-02).
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// recordEvent writes the domain event of a CI mutation in tx.
func (r *PGRepository) recordEvent(ctx context.Context, tx pgx.Tx, eventType string, item *Item, changedFields []string) error {
	if r.outbox == nil {
		return nil
	}
	var data any = item
	if changedFields != nil {
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("encode event data: %w", err)
		}
		m := map[string]any{}
		if err = json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("encode event data: %w", err)
		}
		m["changed_fields"] = changedFields
		data = m
	}
	_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: item.OrganizationID, Type: eventType, EntityID: item.ID, Data: data})
	return err
}

// NewPGRepository creates a new PostgreSQL-backed CI repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return NewPGRepositoryWithAudit(pool, nil)
}

// NewPGRepositoryWithAudit creates a new PostgreSQL-backed CI repository with audit recording.
func NewPGRepositoryWithAudit(pool *pgxpool.Pool, recorder audit.TxRecorder) *PGRepository {
	return &PGRepository{pool: pool, recorder: recorder}
}

// List returns paginated CIs filtered by the given parameters.
// attributeFilterKind checks that an attribute may be filtered (API-02):
// it must be defined (else 422) and searchable (else 400 with a hint), and
// its data type gives the filter kind.
func attributeFilterKind(ctx context.Context, tx pgx.Tx, f api.Filter, key string) (string, error) {
	var dataType string
	var searchable bool
	err := tx.QueryRow(ctx, `SELECT data_type, bool_or(searchable) FROM attribute_definition
		WHERE key = $1 GROUP BY data_type ORDER BY bool_or(searchable) DESC LIMIT 1`, key).Scan(&dataType, &searchable)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &api.FieldError{Field: "filter[" + f.Field + "]", Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf("unknown attribute %q", key)}
	}
	if err != nil {
		return "", fmt.Errorf("attribute definition %s: %w", key, err)
	}
	if !searchable {
		return "", &api.FieldError{Field: "filter[" + f.Field + "]",
			Message: fmt.Sprintf("attribute %q is not searchable; mark it searchable in its attribute definition to filter on it", key)}
	}
	return fieldmeta.FilterKind(dataType), nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	items := make([]Item, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"deleted_at IS NULL"}
		args := make([]any, 0, 6)
		argPos := 1

		if filter.Status != "" {
			whereParts = append(whereParts, fmt.Sprintf("status = $%d", argPos))
			args = append(args, filter.Status)
			argPos++
		}
		if filter.TypeID != "" {
			whereParts = append(whereParts, fmt.Sprintf("ci_type_id = $%d", argPos))
			args = append(args, filter.TypeID)
			argPos++
		}
		if filter.ClientID != "" {
			whereParts = append(whereParts, fmt.Sprintf("client_id = $%d", argPos))
			args = append(args, filter.ClientID)
			argPos++
		}
		if filter.SiteID != "" {
			whereParts = append(whereParts, fmt.Sprintf("site_id = $%d", argPos))
			args = append(args, filter.SiteID)
			argPos++
		}
		if filter.RoomID != "" {
			whereParts = append(whereParts, fmt.Sprintf("room_id = $%d", argPos))
			args = append(args, filter.RoomID)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("name ILIKE $%d", argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}
		for _, f := range filter.Filters {
			expr := filterColumns[f.Field].expr
			if key, ok := f.Attribute(); ok {
				kind, err := attributeFilterKind(ctx, tx, f, key)
				if err != nil {
					return err
				}
				f.Kind = kind
				if cerr := f.Check(); cerr != nil {
					return cerr
				}
				expr = fmt.Sprintf("attributes->>$%d::text", argPos)
				args = append(args, key)
				argPos++
			}
			if expr == "" {
				return &api.FieldError{Field: "filter[" + f.Field + "]", Status: http.StatusUnprocessableEntity, Message: "unknown filter field"}
			}
			clause, fargs := f.SQL(expr, argPos)
			whereParts = append(whereParts, clause)
			args = append(args, fargs...)
			argPos += len(fargs)
		}

		whereClause := strings.Join(whereParts, " AND ")
		countQuery := "SELECT COUNT(*) FROM ci WHERE " + whereClause
		if err := tx.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
			return fmt.Errorf("count cis: %w", err)
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

		var listQuery string
		if page.Cursor != nil {
			listArgs = append(listArgs, page.Limit)
			listQuery = fmt.Sprintf(
				"SELECT %s FROM ci WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				ciSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			listQuery = fmt.Sprintf(
				"SELECT %s FROM ci WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				ciSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}

		rows, err := tx.Query(ctx, listQuery, listArgs...)
		if err != nil {
			return fmt.Errorf("list cis: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanCI(rows)
			if err != nil {
				return fmt.Errorf("scan ci: %w", err)
			}
			items = append(items, *item)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate cis: %w", err)
		}
		rows.Close()
		ptrs := make([]*Item, len(items))
		for i := range items {
			ptrs[i] = &items[i]
		}
		if err := overlayOverrides(ctx, tx, ptrs); err != nil {
			return err
		}
		return loadAssetFacts(ctx, tx, ptrs)
	})

	return items, total, err
}

// GetByID retrieves a single CI by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL", ciSelectColumns)
		var err error
		item, err = scanCI(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci by id: %w", err)
		}
		if err := overlayOverrides(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		return loadAssetFacts(ctx, tx, []*Item{item})
	})
	if err != nil {
		return nil, err
	}

	return item, nil
}

// GetByIDs implements BatchReader: the visible, not deleted CIs among ids
// in one query.
func (r *PGRepository) GetByIDs(ctx context.Context, orgID string, ids []string) ([]Item, error) {
	items := []Item{}
	if len(ids) == 0 {
		return items, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL",
			ciSelectColumns), orgID, ids)
		if err != nil {
			return fmt.Errorf("get cis by id: %w", err)
		}
		var ptrs []*Item
		for rows.Next() {
			item, serr := scanCI(rows)
			if serr != nil {
				rows.Close()
				return serr
			}
			ptrs = append(ptrs, item)
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return rerr
		}
		if oerr := overlayOverrides(ctx, tx, ptrs); oerr != nil {
			return oerr
		}
		if aerr := loadAssetFacts(ctx, tx, ptrs); aerr != nil {
			return aerr
		}
		for _, item := range ptrs {
			items = append(items, *item)
		}
		return nil
	})
	return items, err
}

// Create inserts a new CI.
func (r *PGRepository) Create(ctx context.Context, item *Item) error {
	err := database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Attributes == nil {
			item.Attributes = make(map[string]any)
		}

		query := `
			INSERT INTO ci (
				organization_id,
				client_id,
				location_id,
				ci_type_id,
				name,
				status,
				manufacturer,
				model,
				serial_number,
				hardware_uuid,
				management_ip,
				primary_mac,
				hostname,
				fqdn,
				os_name,
				os_version,
				firmware_version,
				sys_object_id,
				attributes,
				discovery_source,
				first_seen_at,
				last_seen_at,
				is_manual,
				asset_id,
				parent_ci_id
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20,
				$21, $22, $23, $24, $25
			)
			RETURNING id::text, COALESCE(site_id::text, ''), COALESCE(room_id::text, ''),
				COALESCE(location_id::text, ''), health, version, schema_version, created_at, updated_at
		`

		var createdAt time.Time
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			item.OrganizationID,
			nilIfEmpty(item.ClientID),
			nilIfEmpty(item.LocationID),
			item.CITypeID,
			item.Name,
			item.Status,
			nilIfEmpty(item.Manufacturer),
			nilIfEmpty(item.Model),
			nilIfEmpty(item.SerialNumber),
			nilIfEmpty(item.HardwareUUID),
			nilIfEmpty(item.ManagementIP),
			nilIfEmpty(item.PrimaryMAC),
			nilIfEmpty(item.Hostname),
			nilIfEmpty(item.FQDN),
			nilIfEmpty(item.OSName),
			nilIfEmpty(item.OSVersion),
			nilIfEmpty(item.FirmwareVersion),
			nilIfEmpty(item.SysObjectID),
			storedAttributes(item.Attributes, nil),
			nilIfEmpty(item.DiscoverySource),
			item.FirstSeenAt,
			item.LastSeenAt,
			item.IsManual,
			nilIfEmpty(item.AssetID),
			nilIfEmpty(item.ParentCIID),
		).Scan(&item.ID, &item.SiteID, &item.RoomID, &item.LocationID, &item.Health, &item.Version, &item.SchemaVersion, &createdAt, &updatedAt); err != nil {
			return locationError(fmt.Errorf("create ci: %w", err))
		}

		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		if err := r.recordCIChange(ctx, tx, item.OrganizationID, item.ID, item.Version, "create", "", nil, item); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.created", item, map[string]interface{}{"after": item}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CICreated, item, nil)
	})
	return r.uniqueField(ctx, item.OrganizationID, err)
}

// Update modifies an existing CI.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// FOR UPDATE: the merge patch is computed from this state.
		beforeQuery := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, beforeQuery, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci before update: %w", err)
		}
		if req.Manual != nil {
			if preErr := checkPrecondition(ctx, tx, orgID, before, &req); preErr != nil {
				return preErr
			}
		}

		setClauses := make([]string, 0, 18)
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

		addStringField("name", req.Name)
		addStringField("status", req.Status)
		addStringField("client_id", req.ClientID)
		addStringField("asset_id", req.AssetID)
		addStringField("parent_ci_id", req.ParentCIID)
		addStringField("location_id", req.LocationID)
		addStringField("manufacturer", req.Manufacturer)
		addStringField("model", req.Model)
		addStringField("serial_number", req.SerialNumber)
		addStringField("hardware_uuid", req.HardwareUUID)
		addStringField("management_ip", req.ManagementIP)
		addStringField("primary_mac", req.PrimaryMAC)
		addStringField("hostname", req.Hostname)
		addStringField("fqdn", req.FQDN)
		addStringField("os_name", req.OSName)
		addStringField("os_version", req.OSVersion)
		addStringField("firmware_version", req.FirmwareVersion)
		addStringField("sys_object_id", req.SysObjectID)
		addStringField("discovery_source", req.DiscoverySource)

		if req.Attributes != nil {
			// RFC 7396: nested objects merge, null removes a member (CI-04).
			// Instance attributes are stored under _instance; the projected
			// namespaces of the row stay (CI-10).
			instance, namesErr := instanceFieldNames(ctx, tx, id)
			if namesErr != nil {
				return namesErr
			}
			setClauses = append(setClauses, fmt.Sprintf("attributes = $%d::jsonb || ci_attribute_namespaces(attributes)", argPos))
			args = append(args, storedAttributes(MergePatch(before.Attributes, req.Attributes), instance))
			argPos++
		}

		if req.LastSeenAt != nil {
			parsed, err := time.Parse(time.RFC3339, *req.LastSeenAt)
			if err != nil {
				return fmt.Errorf("parse last_seen_at: %w", err)
			}
			setClauses = append(setClauses, fmt.Sprintf("last_seen_at = $%d", argPos))
			args = append(args, parsed)
			argPos++
		}

		if len(setClauses) == 0 {
			item = before
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		if req.Manual != nil || req.Authoritative {
			// Rank >= 92 writes raise the version (API-07).
			setClauses = append(setClauses, "version = version + 1")
		}
		// The tenant predicate is redundant with row-level security but is kept
		// as defense in depth: a missing or wrong app.org_id must never allow a
		// cross-tenant write.
		query := fmt.Sprintf(
			"UPDATE ci SET %s WHERE id = $1 AND organization_id = $%d AND deleted_at IS NULL RETURNING %s",
			strings.Join(setClauses, ", "),
			argPos,
			ciSelectColumns,
		)
		args = append(args, orgID)

		item, err = scanCI(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return locationError(fmt.Errorf("update ci: %w", err))
		}

		if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		changes := diffCI(before, item)
		if len(changes) == 0 {
			return nil
		}
		if req.Manual != nil {
			if err := recordManualOverrides(ctx, tx, orgID, before, item, &req); err != nil {
				return err
			}
		}
		changeType := "update"
		action := "ci.updated"
		if len(changes) == 1 && changes[0].Field == "status" {
			changeType = "status_change"
			action = "ci.status_changed"
		}
		for _, change := range changes {
			fieldChangeType := changeType
			if change.Field == "status" {
				fieldChangeType = "status_change"
			}
			if err := r.recordCIChange(ctx, tx, orgID, id, item.Version, fieldChangeType, change.Field, change.Old, change.New); err != nil {
				return err
			}
		}
		if err := r.recordAudit(ctx, tx, action, item, map[string]interface{}{"before": before, "after": item, "fields": changes}); err != nil {
			return err
		}
		changedFields := make([]string, 0, len(changes))
		for _, change := range changes {
			changedFields = append(changedFields, change.Field)
		}
		if err := r.recordEvent(ctx, tx, events.CIChanged, item, changedFields); err != nil {
			return err
		}
		return nil
	})

	return item, r.uniqueField(ctx, orgID, err)
}

// instanceFieldNames lists the names of the instance attributes of a CI.
func instanceFieldNames(ctx context.Context, tx pgx.Tx, ciID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT name FROM ci_instance_field_definition WHERE ci_id = $1`, ciID)
	if err != nil {
		return nil, fmt.Errorf("instance attributes: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Delete soft-deletes a CI.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci for delete: %w", err)
		}

		// CI-05: the CI is decommissioned (its operative status; the
		// lifecycle state stays, LCY-01) and its asset link is released;
		// the change row keeps what a restore brings back.
		var assetPrimary bool
		if before.AssetID != "" {
			tag, uerr := tx.Exec(ctx, "UPDATE asset SET ci_id = NULL WHERE id = $1 AND ci_id = $2", before.AssetID, id)
			if uerr != nil {
				return fmt.Errorf("release asset link: %w", uerr)
			}
			assetPrimary = tag.RowsAffected() > 0
		}
		cmdTag, err := tx.Exec(ctx, `UPDATE ci SET deleted_at = NOW(), updated_at = NOW(), status = 'decommissioned', asset_id = NULL
			WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL`, id, orgID)
		if err != nil {
			return fmt.Errorf("delete ci: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		released := map[string]any{"status": StatusDecommissioned}
		if before.AssetID != "" {
			released["unlinked_asset_id"] = before.AssetID
			released["asset_primary"] = assetPrimary
		}
		if err := r.recordCIChange(ctx, tx, orgID, id, before.Version, "delete", "", before, released); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.deleted", before, map[string]interface{}{"before": before}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CIDeleted, before, nil)
	})
}

// Restore reactivates a soft-deleted CI (CI-13): an operator decided that a
// device seen again is the deleted one. It records the decision in the
// change history and the audit trail.
func (r *PGRepository) Restore(ctx context.Context, orgID, id string) (*Item, error) {
	return r.restore(ctx, orgID, id, 0, "reconciliation_decision", "resurrected_device")
}

// RestoreDeleted implements Restorer: POST /cis/{id}/restore within the
// retention (CI-05); an older deletion is ErrRetentionExpired.
func (r *PGRepository) RestoreDeleted(ctx context.Context, orgID, id string, retention time.Duration) (*Item, error) {
	return r.restore(ctx, orgID, id, retention, "update", "api")
}

// restore clears deleted_at of a deleted CI (retention 0: no limit) and
// records the change, the audit entry and the ci.restored event.
func (r *PGRepository) restore(ctx context.Context, orgID, id string, retention time.Duration, changeType, reason string) (*Item, error) {
	var restored *Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var deletedAt *time.Time
		var mergedInto string
		if err := tx.QueryRow(ctx, "SELECT deleted_at, COALESCE(merged_into_id::text, '') FROM ci WHERE id = $1 AND organization_id = $2",
			id, orgID).Scan(&deletedAt, &mergedInto); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci for restore: %w", err)
		}
		if deletedAt == nil {
			return fmt.Errorf("not found")
		}
		// A merged CI comes back only through the unmerge (CI-12).
		if mergedInto != "" {
			return ErrMergeConflict
		}
		if retention > 0 && time.Since(*deletedAt) > retention {
			return ErrRetentionExpired
		}
		// The status and the asset link of the deletion come back (CI-05).
		var status, assetID string
		var primary bool
		err := tx.QueryRow(ctx, `SELECT COALESCE(old_value->>'status', ''), COALESCE(new_value->>'unlinked_asset_id', ''),
				COALESCE((new_value->>'asset_primary')::boolean, false)
			FROM ci_change WHERE ci_id = $1 AND change_type = 'delete' ORDER BY created_at DESC LIMIT 1`, id).Scan(&status, &assetID, &primary)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read deletion: %w", err)
		}
		if status == "" || status == StatusDecommissioned {
			status = StatusActive
		}
		tag, err := tx.Exec(ctx, `UPDATE ci SET deleted_at = NULL, updated_at = NOW(), version = version + 1,
				status = CASE WHEN status = 'decommissioned' THEN $3 ELSE status END,
				asset_id = COALESCE(asset_id, (SELECT a.id FROM asset a WHERE a.id = NULLIF($4, '')::uuid))
			WHERE id = $1 AND organization_id = $2 AND deleted_at IS NOT NULL`, id, orgID, status, assetID)
		if err != nil {
			// A reactivated CI may hold a value of a unique attribute that
			// another CI took in the meantime (MET-15).
			return locationError(fmt.Errorf("restore ci: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		if primary && assetID != "" {
			if _, err = tx.Exec(ctx, "UPDATE asset SET ci_id = $1 WHERE id = $2 AND ci_id IS NULL", id, assetID); err != nil {
				return fmt.Errorf("restore asset link: %w", err)
			}
		}
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2", ciSelectColumns)
		if restored, err = scanCI(tx.QueryRow(ctx, query, id, orgID)); err != nil {
			return fmt.Errorf("get restored ci: %w", err)
		}
		if changeErr := r.recordCIChange(ctx, tx, orgID, id, restored.Version, changeType, "deleted_at", "deleted", nil); changeErr != nil {
			return changeErr
		}
		if err := r.recordAudit(ctx, tx, "ci.restored", restored, map[string]interface{}{"reason": reason}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CIRestored, restored, nil)
	})
	return restored, r.uniqueField(ctx, orgID, err)
}

// ListChanges returns paginated change history for a CI.
func (r *PGRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	changes := []Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM ci_change
			WHERE organization_id = $1 AND ci_id = $2
		`, orgID, ciID).Scan(&total); err != nil {
			return fmt.Errorf("count ci changes: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT
				id::text,
				organization_id::text,
				ci_id::text,
				COALESCE(actor_id::text, ''),
				change_type,
				COALESCE(field_name, ''),
				COALESCE(old_value, 'null'::jsonb),
				COALESCE(new_value, 'null'::jsonb),
				COALESCE(comment, ''),
				created_at
			FROM ci_change
			WHERE organization_id = $1 AND ci_id = $2
			ORDER BY created_at DESC, id DESC
			LIMIT $3 OFFSET $4
		`, orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list ci changes: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var change Change
			var oldValue []byte
			var newValue []byte
			if err := rows.Scan(
				&change.ID,
				&change.OrganizationID,
				&change.CIID,
				&change.ActorID,
				&change.ChangeType,
				&change.FieldName,
				&oldValue,
				&newValue,
				&change.Comment,
				&change.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan ci change: %w", err)
			}
			if err := json.Unmarshal(oldValue, &change.OldValue); err != nil {
				return fmt.Errorf("decode old ci change value: %w", err)
			}
			if err := json.Unmarshal(newValue, &change.NewValue); err != nil {
				return fmt.Errorf("decode new ci change value: %w", err)
			}
			changes = append(changes, change)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate ci changes: %w", err)
		}
		// The history shows the personal values the surrogates stand for;
		// deleted ones are null (SEC-05).
		values := make([]any, 0, 2*len(changes))
		for i := range changes {
			values = append(values, changes[i].OldValue, changes[i].NewValue)
		}
		revealed, err := pii.RevealTx(ctx, tx, orgID, values)
		if err != nil {
			return fmt.Errorf("resolve ci change values: %w", err)
		}
		for i, v := range revealed.([]any) {
			if i%2 == 0 {
				changes[i/2].OldValue = v
			} else {
				changes[i/2].NewValue = v
			}
		}
		return nil
	})
	return changes, total, err
}

type ciScanner interface {
	Scan(dest ...any) error
}

func scanCI(scanner ciScanner) (*Item, error) {
	item := &Item{}
	var firstSeen sql.NullTime
	var lastSeen sql.NullTime
	var deletedAt sql.NullTime
	var healthUpdated sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time

	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.SiteID,
		&item.RoomID,
		&item.LocationID,
		&item.CITypeID,
		&item.Name,
		&item.Status,
		&item.Manufacturer,
		&item.Model,
		&item.SerialNumber,
		&item.HardwareUUID,
		&item.ManagementIP,
		&item.PrimaryMAC,
		&item.Hostname,
		&item.FQDN,
		&item.OSName,
		&item.OSVersion,
		&item.FirmwareVersion,
		&item.SysObjectID,
		&item.Attributes,
		&item.DiscoverySource,
		&firstSeen,
		&lastSeen,
		&item.IsManual,
		&createdAt,
		&updatedAt,
		&deletedAt,
		&item.Version,
		&item.AssetID,
		&item.ParentCIID,
		&item.LifecycleState,
		&item.Health,
		&healthUpdated,
		&item.SchemaVersion,
		&item.MergedIntoID,
	); err != nil {
		return nil, err
	}

	item.Attributes = presentAttributes(item.Attributes)
	if firstSeen.Valid {
		t := firstSeen.Time.UTC()
		item.FirstSeenAt = &t
	}
	if lastSeen.Valid {
		t := lastSeen.Time.UTC()
		item.LastSeenAt = &t
	}
	if deletedAt.Valid {
		t := deletedAt.Time.UTC()
		item.DeletedAt = &t
	}
	if healthUpdated.Valid {
		t := healthUpdated.Time.UTC()
		item.HealthUpdatedAt = &t
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)

	return item, nil
}

type ciFieldChange struct {
	Field string `json:"field"`
	Old   any    `json:"old_value"`
	New   any    `json:"new_value"`
}

func (r *PGRepository) recordAudit(ctx context.Context, tx pgx.Tx, action string, item *Item, changes map[string]interface{}) error {
	if r.recorder == nil {
		return nil
	}
	t := tenant.FromContext(ctx)
	actorType := "system"
	if strings.TrimSpace(t.UserID) != "" {
		actorType = "user"
	}
	if _, err := r.recorder.Record(ctx, tx, audit.Entry{
		OrganizationID: item.OrganizationID,
		ActorID:        strings.TrimSpace(t.UserID),
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "ci",
		ResourceID:     item.ID,
		Changes:        changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

func (r *PGRepository) recordCIChange(ctx context.Context, tx pgx.Tx, orgID, ciID string, version int64, changeType, fieldName string, oldValue, newValue any) error {
	// Personal values are stored as vault surrogates (SEC-05).
	oldValue, err := sealChangeValue(ctx, tx, orgID, fieldName, oldValue)
	if err != nil {
		return err
	}
	if newValue, err = sealChangeValue(ctx, tx, orgID, fieldName, newValue); err != nil {
		return err
	}
	oldJSON, err := marshalJSONValue(oldValue)
	if err != nil {
		return fmt.Errorf("marshal old ci change value: %w", err)
	}
	newJSON, err := marshalJSONValue(newValue)
	if err != nil {
		return fmt.Errorf("marshal new ci change value: %w", err)
	}
	actorID := nilIfEmpty(tenant.FromContext(ctx).UserID)
	_, err = tx.Exec(ctx, `
		INSERT INTO ci_change (
			organization_id,
			ci_id,
			actor_id,
			change_type,
			field_name,
			old_value,
			new_value,
			ci_version
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8)
	`, orgID, ciID, actorID, changeType, nilIfEmpty(fieldName), oldJSON, newJSON, version)
	if err != nil {
		return fmt.Errorf("record ci change: %w", err)
	}
	return nil
}

// sealChangeValue replaces the personal data in a historized value.
func sealChangeValue(ctx context.Context, tx pgx.Tx, orgID, fieldName string, value any) (any, error) {
	sealed, _, err := pii.SealFieldTx(ctx, tx, orgID, fieldName, value)
	if err != nil {
		return nil, fmt.Errorf("seal ci change value: %w", err)
	}
	return sealed, nil
}

func marshalJSONValue(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// diffCI lists the changed fields that are historized. last_seen_at is not
// (AUD-03, REC-07): a sighting alone writes no ci_change.
func diffCI(before, after *Item) []ciFieldChange {
	fields := []struct {
		name string
		old  any
		new  any
	}{
		{"client_id", before.ClientID, after.ClientID},
		{"asset_id", before.AssetID, after.AssetID},
		{"parent_ci_id", before.ParentCIID, after.ParentCIID},
		{"location_id", before.LocationID, after.LocationID},
		{"site_id", before.SiteID, after.SiteID},
		{"room_id", before.RoomID, after.RoomID},
		{"ci_type_id", before.CITypeID, after.CITypeID},
		{"name", before.Name, after.Name},
		{"status", before.Status, after.Status},
		{"manufacturer", before.Manufacturer, after.Manufacturer},
		{"model", before.Model, after.Model},
		{"serial_number", before.SerialNumber, after.SerialNumber},
		{"hardware_uuid", before.HardwareUUID, after.HardwareUUID},
		{"management_ip", before.ManagementIP, after.ManagementIP},
		{"primary_mac", before.PrimaryMAC, after.PrimaryMAC},
		{"hostname", before.Hostname, after.Hostname},
		{"fqdn", before.FQDN, after.FQDN},
		{"os_name", before.OSName, after.OSName},
		{"os_version", before.OSVersion, after.OSVersion},
		{"firmware_version", before.FirmwareVersion, after.FirmwareVersion},
		{"sys_object_id", before.SysObjectID, after.SysObjectID},
		{"attributes", before.Attributes, after.Attributes},
		{"discovery_source", before.DiscoverySource, after.DiscoverySource},
		{"first_seen_at", timePtrRFC3339(before.FirstSeenAt), timePtrRFC3339(after.FirstSeenAt)},
		{"is_manual", before.IsManual, after.IsManual},
	}
	changes := make([]ciFieldChange, 0, len(fields))
	for _, field := range fields {
		if !reflect.DeepEqual(field.old, field.new) {
			changes = append(changes, ciFieldChange{Field: field.name, Old: field.old, New: field.new})
		}
	}
	return changes
}

func timePtrRFC3339(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// locationError turns a rejected location of a CI (unknown or invisible
// location, location of another client; trigger ci_derive_location of
// migration 000074) into a validation error on location_id.
func locationError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == "23505" && strings.HasPrefix(pgErr.ConstraintName, "idx_attr_") {
		return uniqueIndexError(pgErr)
	}
	switch pgErr.ConstraintName {
	case "ci_location_id_fkey":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: "location not found"}}}
	case "ci_location_client":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: "the location belongs to another client"}}}
	case "ci_parent_ci_fkey":
		return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: "parent CI not found"}}}
	case "ci_parent_not_self", "ci_parent_cycle":
		return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: "a CI cannot contain itself, directly or through its parents"}}}
	case "ci_lifecycle_requires_model":
		return &ValidationError{Violations: []Violation{{Field: "lifecycle_state",
			Detail: "only CI types with a lifecycle model have a lifecycle state, and it must be a state of that model"}}}
	case "ci_health_read_only":
		return &ValidationError{Violations: []Violation{{Field: "health", Detail: "the health is computed from monitoring"}}}
	case "ci_asset_id_fkey":
		return &ValidationError{Violations: []Violation{{Field: "asset_id", Detail: "asset not found"}}}
	case "ci_location_from_asset":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: locationOwnedByAsset}}}
	}
	return err
}

// locationOwnedByAsset is the violation of a location write on a CI linked
// to an asset (DB-05).
const locationOwnedByAsset = "the location of a CI linked to an asset is the asset's location"

// loadAssetFacts shows the canonical data of each linked asset on its CIs
// (DB-05). Assets the principal cannot see are left out.
func loadAssetFacts(ctx context.Context, tx pgx.Tx, items []*Item) error {
	byAsset := map[string][]*Item{}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.AssetID == "" {
			continue
		}
		if _, seen := byAsset[item.AssetID]; !seen {
			ids = append(ids, item.AssetID)
		}
		byAsset[item.AssetID] = append(byAsset[item.AssetID], item)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, asset_tag, name, COALESCE(serial_number, ''), COALESCE(location_id::text, ''),
		       COALESCE(lifecycle_state, ''), status, COALESCE(barcode, ''), COALESCE(rfid_tag, ''),
		       COALESCE(to_char(purchase_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(warranty_end, 'YYYY-MM-DD'), ''),
		       COALESCE(supplier, ''), COALESCE(invoice_number, '')
		FROM asset WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("load asset facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a AssetFacts
		if err := rows.Scan(&a.ID, &a.AssetTag, &a.Name, &a.SerialNumber, &a.LocationID, &a.LifecycleState, &a.Status,
			&a.Barcode, &a.RFIDTag, &a.PurchaseDate, &a.WarrantyEnd, &a.Supplier, &a.InvoiceNumber); err != nil {
			return fmt.Errorf("scan asset facts: %w", err)
		}
		for _, item := range byAsset[a.ID] {
			facts := a
			item.Asset = &facts
		}
	}
	return rows.Err()
}

// derivedFields are set by the database, not by the writer of a change.
// The asset link is structural, not an observed value.
var derivedFields = map[string]bool{"site_id": true, "room_id": true, "attributes": true, "asset_id": true, "parent_ci_id": true}

// recordManualOverrides gives every field a manual change touched a
// protected override, so discovery never overwrites it (OVR-01, CH9): the
// changed CI columns by name, the attributes per changed top-level member
// (a removed member is recorded with a null value).
func recordManualOverrides(ctx context.Context, tx pgx.Tx, orgID string, before, after *Item, req *UpdateRequest) error {
	reason := strings.TrimSpace(req.ChangeReason)
	if reason == "" {
		reason = DefaultChangeReason
	}
	for _, change := range diffCI(before, after) {
		if derivedFields[change.Field] || (change.Field == "location_id" && after.AssetID != "") {
			// The location of a linked CI is the asset's (DB-05).
			continue
		}
		if err := override.SetOverrideTx(ctx, tx, orgID, after.ID, change.Field, change.New, req.Manual.Author, reason, true); err != nil {
			return err
		}
	}
	for _, key := range changedKeys(before.Attributes, after.Attributes) {
		if err := override.SetOverrideTx(ctx, tx, orgID, after.ID, key, after.Attributes[key], req.Manual.Author, reason, true); err != nil {
			return err
		}
	}
	return nil
}

// overrideColumns are CI columns a manual override replaces in the effective
// view; structuralFields are CI columns that are not overridden there.
// Every other field name is an attribute.
var (
	overrideColumns = map[string]func(*Item, string){
		"name":             func(i *Item, v string) { i.Name = v },
		"status":           func(i *Item, v string) { i.Status = v },
		"manufacturer":     func(i *Item, v string) { i.Manufacturer = v },
		"model":            func(i *Item, v string) { i.Model = v },
		"serial_number":    func(i *Item, v string) { i.SerialNumber = v },
		"hardware_uuid":    func(i *Item, v string) { i.HardwareUUID = v },
		"management_ip":    func(i *Item, v string) { i.ManagementIP = v },
		"primary_mac":      func(i *Item, v string) { i.PrimaryMAC = v },
		"hostname":         func(i *Item, v string) { i.Hostname = v },
		"fqdn":             func(i *Item, v string) { i.FQDN = v },
		"os_name":          func(i *Item, v string) { i.OSName = v },
		"os_version":       func(i *Item, v string) { i.OSVersion = v },
		"firmware_version": func(i *Item, v string) { i.FirmwareVersion = v },
		"sys_object_id":    func(i *Item, v string) { i.SysObjectID = v },
	}
	structuralFields = map[string]bool{
		"id": true, "organization_id": true, "client_id": true, "location_id": true, "asset_id": true, "parent_ci_id": true, "site_id": true,
		"room_id": true, "ci_type_id": true, "attributes": true, "discovery_source": true, "is_manual": true,
		"first_seen_at": true, "last_seen_at": true, "created_at": true, "updated_at": true, "deleted_at": true,
	}
)

// overlayOverrides applies the manual overrides of the CIs, so every read
// returns the effective value: an override before the observed value
// (OVR-01, CI-10). The CI rows normally carry it already (manual writes
// and override changes update them); the overlay keeps reads consistent
// for rows written before.
func overlayOverrides(ctx context.Context, tx pgx.Tx, items []*Item) error {
	if len(items) == 0 {
		return nil
	}
	byID := make(map[string]*Item, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		byID[item.ID] = item
		ids = append(ids, item.ID)
	}
	rows, err := tx.Query(ctx, `SELECT ci_id::text, field_name, override_value FROM ci_field_value
		WHERE ci_id = ANY($1::uuid[]) AND override_at IS NOT NULL`, ids)
	if err != nil {
		return fmt.Errorf("read overrides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ciID, field string
		var value any
		if err = rows.Scan(&ciID, &field, &value); err != nil {
			return fmt.Errorf("scan override: %w", err)
		}
		applyOverride(byID[ciID], field, value)
	}
	return rows.Err()
}

func applyOverride(item *Item, field string, value any) {
	if item == nil || structuralFields[field] {
		return
	}
	if set, ok := overrideColumns[field]; ok {
		text := ""
		if value != nil {
			text = fmt.Sprint(value)
		}
		set(item, text)
		return
	}
	if value == nil {
		delete(item.Attributes, field)
		return
	}
	if item.Attributes == nil {
		item.Attributes = map[string]any{}
	}
	item.Attributes[field] = value
}

// checkPrecondition enforces If-Match on a manual change (API-07, API-03):
// a missing If-Match fails when the organization requires one (412); an
// older version fails only when a field of the request changed since by a
// write of rank >= 92 (409, field-granular); a version the CI never had
// fails (412). Attributes count as one field.
func checkPrecondition(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) error {
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
	case *req.IfMatch == before.Version, *req.IfMatch == api.IfMatchAny:
		return nil
	case *req.IfMatch > before.Version || *req.IfMatch < 1:
		return ErrPreconditionFailed
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT field_name FROM ci_change
		WHERE ci_id = $1 AND ci_version > $2 AND field_name = ANY($3) ORDER BY field_name`,
		before.ID, *req.IfMatch, requestedFields(req))
	if err != nil {
		return fmt.Errorf("read changes since if-match: %w", err)
	}
	fields, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read changes since if-match: %w", err)
	}
	if len(fields) > 0 {
		return &ConflictError{Fields: fields, Current: before.Version}
	}
	return nil
}

// requestedFields lists the ci_change field names a PATCH writes.
func requestedFields(req *UpdateRequest) []string {
	var out []string
	for name, set := range map[string]bool{
		"name": req.Name != nil, "status": req.Status != nil, "client_id": req.ClientID != nil,
		"location_id": req.LocationID != nil, "manufacturer": req.Manufacturer != nil, "model": req.Model != nil,
		"serial_number": req.SerialNumber != nil, "hardware_uuid": req.HardwareUUID != nil,
		"management_ip": req.ManagementIP != nil, "primary_mac": req.PrimaryMAC != nil, "hostname": req.Hostname != nil,
		"fqdn": req.FQDN != nil, "os_name": req.OSName != nil, "os_version": req.OSVersion != nil,
		"firmware_version": req.FirmwareVersion != nil, "sys_object_id": req.SysObjectID != nil,
		"attributes": req.Attributes != nil, "discovery_source": req.DiscoverySource != nil,
	} {
		if set {
			out = append(out, name)
		}
	}
	return out
}

// uniqueIndexError turns a violated unique attribute index (DB-04) into
// the conflict of the attribute: a concurrent write took the value after
// the service's check. PostgreSQL leaves out the key of a table with row
// level security, so uniqueField names the attribute by the index.
func uniqueIndexError(pgErr *pgconn.PgError) error {
	return &UniqueError{Fields: []string{"attributes"}, index: pgErr.ConstraintName}
}

// uniqueField names the attribute of a violated attribute index: the
// index name carries the definition id (idx_attr_<32 hex>_u, E-04).
func (r *PGRepository) uniqueField(ctx context.Context, orgID string, err error) error {
	var ue *UniqueError
	if !errors.As(err, &ue) || ue.index == "" {
		return err
	}
	hexID := strings.TrimSuffix(strings.TrimPrefix(ue.index, "idx_attr_"), "_u")
	if len(hexID) != 32 {
		return err
	}
	defID := hexID[0:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
	var key string
	if lerr := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT key FROM attribute_definition WHERE id = $1`, defID).Scan(&key)
	}); lerr == nil {
		ue.Fields = []string{key}
	}
	return err
}

// UniqueConflicts implements UniqueChecker: the unique attributes of the
// CI type and the global ones whose value another active CI of the
// organization (scope org) or of the same client (scope client) holds.
// Soft-deleted CIs do not count (MET-15).
func (r *PGRepository) UniqueConflicts(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) ([]string, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return nil, fmt.Errorf("encode attributes: %w", err)
	}
	var keys []string
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT d.key FROM attribute_definition d
			WHERE d.is_unique AND (d.ci_type_id = $2::uuid OR d.ci_type_id IS NULL)
			  AND (d.organization_id = $1::uuid OR d.organization_id IS NULL)
			  AND $5::jsonb ->> d.key IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM ci c
				WHERE c.organization_id = $1::uuid AND c.deleted_at IS NULL
				  AND ($4::uuid IS NULL OR c.id <> $4::uuid)
				  AND (d.ci_type_id IS NULL OR c.ci_type_id = d.ci_type_id)
				  AND (d.unique_scope = 'org' OR c.client_id IS NOT DISTINCT FROM $3::uuid)
				  AND c.attributes ->> d.key = $5::jsonb ->> d.key)
			ORDER BY d.key`, orgID, ciTypeID, nilIfEmpty(clientID), nilIfEmpty(ciID), raw)
		if qerr != nil {
			return fmt.Errorf("check unique attributes: %w", qerr)
		}
		keys, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		return qerr
	})
	return keys, err
}

// ChangeType implements TypeChanger (CI-11): the CI keeps its row, so its
// relationships, mounts, asset link and history stay; required attributes
// of the target type the CI lacks are reported as findings.
func (r *PGRepository) ChangeType(ctx context.Context, orgID, ciID, typeID, mode string) (*TypeChangeResult, error) {
	result := &TypeChangeResult{Findings: []TypeFinding{}}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, query, ciID, orgID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci for type change: %w", err)
		}
		var toKey string
		var required []string
		err = tx.QueryRow(ctx, `SELECT COALESCE(key, name), COALESCE(required_fields, '{}') FROM ci_type
			WHERE id = $1 AND (organization_id = $2 OR organization_id IS NULL)`, typeID, orgID).Scan(&toKey, &required)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownType
			}
			return fmt.Errorf("get target type: %w", err)
		}
		var fromKey string
		if err = tx.QueryRow(ctx, "SELECT COALESCE(key, name) FROM ci_type WHERE id = $1", before.CITypeID).Scan(&fromKey); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get current type: %w", err)
		}
		if mode == TypeChangeDiscovery && !DiscoveryMayReclassify(fromKey, toKey) {
			return ErrReclassificationRefused
		}
		result.Findings = MissingRequired(required, before.Attributes)
		if before.CITypeID == typeID {
			result.CI = before
			return nil
		}
		if _, err = tx.Exec(ctx, `UPDATE ci SET ci_type_id = $3, version = version + 1, updated_at = NOW()
			WHERE id = $1 AND organization_id = $2`, ciID, orgID, typeID); err != nil {
			return fmt.Errorf("change ci type: %w", err)
		}
		after, err := scanCI(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2", ciSelectColumns), ciID, orgID))
		if err != nil {
			return fmt.Errorf("get changed ci: %w", err)
		}
		result.CI, result.Changed = after, true
		change := map[string]any{"ci_type_id": typeID, "type": toKey, "mode": mode, "findings": result.Findings}
		if changeErr := r.recordCIChange(ctx, tx, orgID, ciID, after.Version, "type_change", "ci_type_id",
			map[string]any{"ci_type_id": before.CITypeID, "type": fromKey}, change); changeErr != nil {
			return changeErr
		}
		if auditErr := r.recordAudit(ctx, tx, "ci.type_changed", after, map[string]interface{}{
			"from": fromKey, "to": toKey, "mode": mode, "findings": result.Findings,
		}); auditErr != nil {
			return auditErr
		}
		// EVT-02 has no own type event: ci.changed names ci_type_id.
		return r.recordEvent(ctx, tx, events.CIChanged, after, []string{"ci_type_id"})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
