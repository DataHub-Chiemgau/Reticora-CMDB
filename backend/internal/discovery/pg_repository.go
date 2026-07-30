package discovery

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const collectorSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	name,
	COALESCE(version, ''),
	status,
	last_heartbeat,
	config,
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed discovery repository.
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

func (r *PGRepository) ListCollectors(orgID string, page api.PaginationParams) ([]Collector, int, error) {
	ctx := context.Background()
	var items []Collector
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM collector WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return fmt.Errorf("count collectors: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM collector WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
			collectorSelectColumns,
		)
		rows, err := tx.Query(ctx, query, orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list collectors: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanCollector(rows)
			if err != nil {
				return fmt.Errorf("scan collector: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate collectors: %w", err)
		}
		return nil
	})

	return items, total, err
}

func (r *PGRepository) RegisterCollector(c *Collector) error {
	ctx := context.Background()
	return r.withTenant(ctx, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if c.Config == nil {
			c.Config = make(map[string]any)
		}
		status := c.Status
		if status == "" {
			status = "online"
		}
		now := time.Now().UTC()
		query := `
			INSERT INTO collector (
				organization_id,
				client_id,
				name,
				version,
				status,
				last_heartbeat,
				config
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, status, last_heartbeat, created_at, updated_at
		`
		var lastHeartbeat sql.NullTime
		var createdAt time.Time
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			c.OrganizationID,
			nilIfEmpty(c.ClientID),
			c.Name,
			nilIfEmpty(c.Version),
			status,
			now,
			c.Config,
		).Scan(&c.ID, &c.Status, &lastHeartbeat, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("register collector: %w", err)
		}
		if lastHeartbeat.Valid {
			c.LastHeartbeat = lastHeartbeat.Time.UTC().Format(time.RFC3339Nano)
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
}

func (r *PGRepository) Heartbeat(orgID, collectorID string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx,
			"UPDATE collector SET status = 'online', last_heartbeat = NOW(), updated_at = NOW() WHERE id = $1 AND organization_id = $2",
			collectorID,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("collector heartbeat: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

type collectorScanner interface {
	Scan(dest ...any) error
}

func scanCollector(scanner collectorScanner) (*Collector, error) {
	item := &Collector{}
	var lastHeartbeat sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.Name,
		&item.Version,
		&item.Status,
		&lastHeartbeat,
		&item.Config,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Config == nil {
		item.Config = make(map[string]any)
	}
	if lastHeartbeat.Valid {
		item.LastHeartbeat = lastHeartbeat.Time.UTC().Format(time.RFC3339Nano)
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
