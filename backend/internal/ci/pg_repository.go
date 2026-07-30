package ci

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
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed CI repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
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

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// List returns paginated CIs filtered by the given parameters.
func (r *PGRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	ctx := context.Background()
	var items []Item
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

		sortColumn := "created_at"
		switch filter.SortBy {
		case "name", "status", "created_at", "updated_at":
			sortColumn = filter.SortBy
		}
		sortDirection := "DESC"
		if strings.EqualFold(filter.SortDir, "asc") {
			sortDirection = "ASC"
		}

		listArgs := append(append([]any{}, args...), page.Limit, page.Offset)
		listQuery := fmt.Sprintf(
			"SELECT %s FROM ci WHERE %s ORDER BY %s %s LIMIT $%d OFFSET $%d",
			ciSelectColumns,
			whereClause,
			sortColumn,
			sortDirection,
			argPos,
			argPos+1,
		)

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
func (r *PGRepository) GetByID(orgID, id string) (*Item, error) {
	ctx := context.Background()
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
func (r *PGRepository) Create(item *Item) error {
	ctx := context.Background()
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
		return nil
	})
}

// Update modifies an existing CI.
func (r *PGRepository) Update(orgID, id string, req UpdateRequest) (*Item, error) {
	ctx := context.Background()
	var item *Item

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
			query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND deleted_at IS NULL", ciSelectColumns)
			var err error
			item, err = scanCI(tx.QueryRow(ctx, query, id))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return fmt.Errorf("get ci for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE ci SET %s WHERE id = $1 AND deleted_at IS NULL RETURNING %s",
			strings.Join(setClauses, ", "),
			ciSelectColumns,
		)

		var err error
		item, err = scanCI(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update ci: %w", err)
		}

		return nil
	})

	return item, err
}

// Delete soft-deletes a CI.
func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "UPDATE ci SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL", id)
		if err != nil {
			return fmt.Errorf("delete ci: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
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

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
