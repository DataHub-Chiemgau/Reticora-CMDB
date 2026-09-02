package savedview

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryEngine executes a filter spec against the CI or asset table.
type QueryEngine interface {
	Query(ctx context.Context, orgID string, spec FilterSpec, page api.PaginationParams) ([]QueryResult, int, error)
}

// PGQueryEngine compiles the filter DSL to SQL and runs it tenant-scoped.
type PGQueryEngine struct {
	pool *pgxpool.Pool
}

// NewPGQueryEngine creates a PostgreSQL-backed query engine.
func NewPGQueryEngine(pool *pgxpool.Pool) *PGQueryEngine {
	return &PGQueryEngine{pool: pool}
}

// Query executes the filter spec. entity_kind selects the table: "ci" or
// "asset". Unknown kinds are rejected.
func (e *PGQueryEngine) Query(ctx context.Context, orgID string, spec FilterSpec, page api.PaginationParams) ([]QueryResult, int, error) {
	table := "ci"
	switch spec.entityKind() {
	case "ci":
		table = "ci"
	case "asset":
		table = "asset"
	default:
		return nil, 0, fmt.Errorf("unsupported entity_kind %q", spec.entityKind())
	}

	var out []QueryResult
	var total int
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return nil, 0, fmt.Errorf("set tenant context: %w", err)
	}

	where, args, err := spec.CompileSQL(spec.entityKind(), []any{orgID}, 2)
	if err != nil {
		return nil, 0, err
	}
	baseWhere := fmt.Sprintf("%s.organization_id = $1 AND %s", table, where)

	if err := tx.QueryRow(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, baseWhere), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count query results: %w", err)
	}

	summary := "concat_ws(' ', hostname, manufacturer, model, serial_number, os_name)"
	attrsCol := "attributes"
	if table == "asset" {
		summary = "concat_ws(' ', asset_tag, category, status, supplier, serial_number, location)"
		attrsCol = "custom_fields"
	}
	args = append(args, page.Limit, page.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(
		"SELECT id::text, name, COALESCE(%s, ''), %s FROM %s WHERE %s ORDER BY name ASC LIMIT $%d OFFSET $%d",
		summary, attrsCol, table, baseWhere, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("run filter query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var qr QueryResult
		var attrs map[string]any
		if err := rows.Scan(&qr.ID, &qr.Name, &qr.Summary, &attrs); err != nil {
			return nil, 0, err
		}
		qr.EntityKind = spec.entityKind()
		qr.Attributes = attrs
		out = append(out, qr)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, tx.Commit(ctx)
}

// entityKind defaults the spec's entity kind to "ci".
func (f FilterSpec) entityKind() string {
	if f.EntityKind == "" {
		return "ci"
	}
	return f.EntityKind
}
