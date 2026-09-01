package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ciSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
	COALESCE(room_id::text, ''),
	ci_type_id::text,
	name,
	status,
	COALESCE(manufacturer, ''),
	COALESCE(model, ''),
	COALESCE(serial_number, ''),
	COALESCE(hardware_uuid::text, ''),
	COALESCE(management_ip::text, ''),
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
	deleted_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool     *pgxpool.Pool
	recorder audit.TxRecorder
}

// NewPGRepository creates a new PostgreSQL-backed CI repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return NewPGRepositoryWithAudit(pool, nil)
}

// NewPGRepositoryWithAudit creates a new PostgreSQL-backed CI repository with audit recording.
func NewPGRepositoryWithAudit(pool *pgxpool.Pool, recorder audit.TxRecorder) *PGRepository {
	return &PGRepository{pool: pool, recorder: recorder}
}

// withTenant executes fn within a transaction that has app.org_id set for RLS.
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if scope := tenant.ClientScope(ctx); scope != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.client_scope', $1, true)", scope); err != nil {
			return fmt.Errorf("set client scope: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// List returns paginated CIs filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	items := make([]Item, 0)
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

		return nil
	})

	return items, total, err
}

// GetByID retrieves a single CI by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	var item *Item

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND deleted_at IS NULL", ciSelectColumns)
		var err error
		item, err = scanCI(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return item, nil
}

// Create inserts a new CI.
func (r *PGRepository) Create(ctx context.Context, item *Item) error {
	return r.withTenant(ctx, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Attributes == nil {
			item.Attributes = make(map[string]any)
		}

		query := `
			INSERT INTO ci (
				organization_id,
				client_id,
				site_id,
				room_id,
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
				is_manual
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20,
				$21, $22, $23, $24
			)
			RETURNING id::text, created_at, updated_at
		`

		var createdAt time.Time
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			item.OrganizationID,
			nilIfEmpty(item.ClientID),
			nilIfEmpty(item.SiteID),
			nilIfEmpty(item.RoomID),
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
			item.Attributes,
			nilIfEmpty(item.DiscoverySource),
			item.FirstSeenAt,
			item.LastSeenAt,
			item.IsManual,
		).Scan(&item.ID, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("create ci: %w", err)
		}

		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if err := r.recordCIChange(ctx, tx, item.OrganizationID, item.ID, "create", "", nil, item); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.created", item, map[string]interface{}{"after": item}); err != nil {
			return err
		}
		return nil
	})
}

// Update modifies an existing CI.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	var item *Item

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		beforeQuery := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND deleted_at IS NULL", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, beforeQuery, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci before update: %w", err)
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
		addStringField("site_id", req.SiteID)
		addStringField("room_id", req.RoomID)
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
			setClauses = append(setClauses, fmt.Sprintf("attributes = COALESCE(attributes, '{}'::jsonb) || $%d", argPos))
			args = append(args, req.Attributes)
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
		query := fmt.Sprintf(
			"UPDATE ci SET %s WHERE id = $1 AND deleted_at IS NULL RETURNING %s",
			strings.Join(setClauses, ", "),
			ciSelectColumns,
		)

		item, err = scanCI(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update ci: %w", err)
		}

		changes := diffCI(before, item)
		if len(changes) == 0 {
			return nil
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
			if err := r.recordCIChange(ctx, tx, orgID, id, fieldChangeType, change.Field, change.Old, change.New); err != nil {
				return err
			}
		}
		if err := r.recordAudit(ctx, tx, action, item, map[string]interface{}{"before": before, "after": item, "fields": changes}); err != nil {
			return err
		}
		return nil
	})

	return item, err
}

// Delete soft-deletes a CI.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND deleted_at IS NULL", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci for delete: %w", err)
		}

		cmdTag, err := tx.Exec(ctx, "UPDATE ci SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL", id)
		if err != nil {
			return fmt.Errorf("delete ci: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		if err := r.recordCIChange(ctx, tx, orgID, id, "delete", "", before, nil); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.deleted", before, map[string]interface{}{"before": before}); err != nil {
			return err
		}
		return nil
	})
}

// ListChanges returns paginated change history for a CI.
func (r *PGRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	changes := []Change{}
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	var createdAt time.Time
	var updatedAt time.Time

	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.SiteID,
		&item.RoomID,
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
	); err != nil {
		return nil, err
	}

	if item.Attributes == nil {
		item.Attributes = make(map[string]any)
	}
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

func (r *PGRepository) recordCIChange(ctx context.Context, tx pgx.Tx, orgID, ciID, changeType, fieldName string, oldValue, newValue any) error {
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
			new_value
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb)
	`, orgID, ciID, actorID, changeType, nilIfEmpty(fieldName), oldJSON, newJSON)
	if err != nil {
		return fmt.Errorf("record ci change: %w", err)
	}
	return nil
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

func diffCI(before, after *Item) []ciFieldChange {
	fields := []struct {
		name string
		old  any
		new  any
	}{
		{"client_id", before.ClientID, after.ClientID},
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
		{"last_seen_at", timePtrRFC3339(before.LastSeenAt), timePtrRFC3339(after.LastSeenAt)},
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
