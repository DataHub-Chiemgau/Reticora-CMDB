package savedview

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
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

// Query executes the filter spec in WithRequestTenant, i.e. under RLS with
// the principal's full scope (client, site, team). entity_kind selects the
// table: "ci" or "asset". Unknown kinds are rejected; deleted CIs are never
// returned.
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
	err := database.WithRequestTenant(ctx, e.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args, err := spec.CompileSQL(spec.entityKind(), []any{orgID}, 2)
		if err != nil {
			return err
		}
		baseWhere := fmt.Sprintf("%s.organization_id = $1 AND %s", table, where)
		if table == "ci" {
			baseWhere = "ci.deleted_at IS NULL AND " + baseWhere
		}

		if countErr := tx.QueryRow(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, baseWhere), args...).Scan(&total); countErr != nil {
			return fmt.Errorf("count query results: %w", countErr)
		}

		summary := "concat_ws(' ', hostname, manufacturer, model, serial_number, os_name)"
		attrsCol := "attributes"
		if table == "asset" {
			summary = "concat_ws(' ', asset_tag, category, status, supplier, serial_number, location)"
			attrsCol = "custom_fields"
		}
		columns, orderBy, args := presentationSQL(table, spec.Sort, spec.Columns, args)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT id::text, name, COALESCE(%s, ''), %s, %s FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d",
			summary, attrsCol, columns, table, baseWhere, orderBy, len(args)-1, len(args)), args...)
		if err != nil {
			return fmt.Errorf("run filter query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var qr QueryResult
			var attrs, cols map[string]any
			if scanErr := rows.Scan(&qr.ID, &qr.Name, &qr.Summary, &attrs, &cols); scanErr != nil {
				return scanErr
			}
			qr.EntityKind = spec.entityKind()
			qr.Attributes = attrs
			qr.Columns = cols
			out = append(out, qr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// presentationSQL renders the sort keys and columns of a run (VIE-01,
// F-336) for table: columns is a jsonb_build_object of the requested
// fields (NULL without columns), orderBy the ORDER BY list, by name
// without sort keys and otherwise by the keys with NULLs last and the id
// as tie-breaker, so that pages do not overlap. The fields passed
// validatePresentation (CompileSQL validates the spec), so each one is a
// whitelisted column, the id or a dynamic attribute; field names and
// attribute keys reach the SQL as parameters appended to args.
func presentationSQL(table string, sortKeys []SortKey, fields []string, args []any) (columns, orderBy string, outArgs []any) {
	param := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	field := func(name string) string {
		if name == "id" {
			return table + ".id"
		}
		if key, ok := strings.CutPrefix(name, "attributes."); ok {
			jsonb := table + ".attributes"
			if table == "asset" {
				jsonb = table + ".custom_fields"
			}
			// The JSON value orders numbers as numbers, strings as text.
			return fmt.Sprintf("%s->%s::text", jsonb, param(key))
		}
		return fmt.Sprintf(astFields[table][name].expr, table)
	}
	columns = "NULL::jsonb"
	if len(fields) > 0 {
		pairs := make([]string, 0, len(fields))
		for _, name := range fields {
			pairs = append(pairs, param(name)+"::text, "+field(name))
		}
		columns = "jsonb_build_object(" + strings.Join(pairs, ", ") + ")"
	}
	if len(sortKeys) == 0 {
		return columns, "name ASC", args
	}
	keys := make([]string, 0, len(sortKeys)+1)
	for _, key := range sortKeys {
		dir := "ASC"
		if key.Dir == "desc" {
			dir = "DESC"
		}
		keys = append(keys, field(key.Field)+" "+dir+" NULLS LAST")
	}
	keys = append(keys, table+".id ASC")
	return columns, strings.Join(keys, ", "), args
}

// entityKind defaults the spec's entity kind to "ci".
func (f FilterSpec) entityKind() string {
	if f.EntityKind == "" {
		return "ci"
	}
	return f.EntityKind
}
