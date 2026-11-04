package savedview

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryEngine executes a filter spec against the CI or asset table.
type QueryEngine interface {
	Query(ctx context.Context, orgID string, spec *FilterSpec, page api.PaginationParams) ([]QueryResult, int, error)
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
func (e *PGQueryEngine) Query(ctx context.Context, orgID string, spec *FilterSpec, page api.PaginationParams) ([]QueryResult, int, error) {
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

		// summary joins its columns as concat_ws(' ', ...) did: the
		// columns that are not NULL, separated by a space. The columns are
		// read one by one, so the field projection keeps only the readable
		// ones (WP-729); the type and scope of each row are read for it.
		parts, attrsCol, typeCol := summaryColumns["ci"], "attributes", "ci_type_id"
		if table == "asset" {
			parts, attrsCol, typeCol = summaryColumns["asset"], "custom_fields", "asset_type_id"
		}
		partSQL := make([]string, len(parts))
		for i, part := range parts {
			partSQL[i] = fmt.Sprintf("%s.%s::text", table, part)
		}
		columns, orderBy, args := presentationSQL(table, spec.Sort, spec.Columns, args)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			`SELECT id::text, name, ARRAY[%s], %s, %s, COALESCE(%s::text, ''), COALESCE(client_id::text, ''),
				COALESCE(site_id::text, '') FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			strings.Join(partSQL, ", "), attrsCol, columns, typeCol, table, baseWhere, orderBy, len(args)-1, len(args)), args...)
		if err != nil {
			return fmt.Errorf("run filter query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var qr QueryResult
			var attrs, cols map[string]any
			var values []*string
			if scanErr := rows.Scan(&qr.ID, &qr.Name, &values, &attrs, &cols, &qr.TypeID, &qr.ClientID, &qr.SiteID); scanErr != nil {
				return scanErr
			}
			for i, v := range values {
				if v != nil && i < len(parts) {
					qr.SummaryParts = append(qr.SummaryParts, SummaryPart{Field: parts[i], Value: *v})
				}
			}
			qr.EntityKind = spec.entityKind()
			qr.Summary = joinSummary(qr.SummaryParts, nil)
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

// summaryColumns are the columns the summary of a row joins, per entity
// kind and in this order.
var summaryColumns = map[string][]string{
	"ci":    {"hostname", "manufacturer", "model", "serial_number", "os_name"},
	"asset": {"inventory_no", "category", "status", "supplier", "serial_number", "location"},
}

// joinSummary joins the parts like concat_ws(' ', ...); keep, when set,
// leaves out the parts it refuses.
func joinSummary(parts []SummaryPart, keep func(field string) bool) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if keep == nil || keep(part.Field) {
			values = append(values, part.Value)
		}
	}
	return strings.Join(values, " ")
}

// TypeIDs returns the ids of the CI types whose key or name is keyOrName,
// as ci_type of the filter spec matches them, for the field permission
// check of the queried types (WP-729).
func (e *PGQueryEngine) TypeIDs(ctx context.Context, orgID, keyOrName string) ([]string, error) {
	var ids []string
	err := database.WithRequestTenant(ctx, e.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM ci_type WHERE key = $1 OR name = $1 ORDER BY id`, keyOrName)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return ids, err
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
func (f *FilterSpec) entityKind() string {
	if f.EntityKind == "" {
		return "ci"
	}
	return f.EntityKind
}

// objectKinds maps the entity kinds of the query to their field
// permission kinds.
var objectKinds = map[string]permission.ObjectKind{"ci": permission.KindCI, "asset": permission.KindAsset}

// fieldKey returns the field key of a field of the entity kind the field
// permissions cover: for a CI the projected columns and attributes of the
// CI API (WP-265), for an asset the columns of permission.CoreFields and
// its custom fields (attributes.<key>). checked is false for a structural
// field (id, name, the type, client, site and timestamps, for CIs also
// status, lifecycle, location and discovery source), which RLS and the
// scope cover; known is false for a CI field without mapping, which counts
// as unreadable (fail-closed).
func fieldKey(entityKind, path string) (key string, checked, known bool) {
	switch entityKind {
	case "ci":
		k, structural, ok := permission.FieldKeyOf(permission.KindCI, path)
		if !ok {
			return "", true, false
		}
		return k, !structural, true
	case "asset":
		if name, ok := strings.CutPrefix(path, "attributes."); ok && name != "" {
			return permission.NamespaceAttributes + "." + name, true, true
		}
		// A core column, also by its AST-01 name (inventory_no covered by
		// core.asset_tag until WP-739, WP-738).
		if k, structural, ok := permission.FieldKeyOf(permission.KindAsset, path); ok && !structural &&
			strings.HasPrefix(k, permission.NamespaceCore+".") {
			return k, true, true
		}
		return "", false, true
	}
	return "", true, false
}

// ProjectResults projects the rows of a query by the field policy (RBA-07,
// WP-729): the summary joins only the columns the principal may read on
// the row, the attributes (custom fields of an asset) keep only the
// readable keys, the columns only the readable fields. The type and scope
// of each row decide (permission.Level with object grants). Without a
// restriction of the entity kind the rows stay as they are (phase 1); a nil
// policy reads no field with field permissions (fail-closed).
func ProjectResults(policy *permission.FieldPolicy, entityKind string, rows []QueryResult) {
	kind, ok := objectKinds[entityKind]
	if !ok || !policy.Restricted(kind) {
		return
	}
	for i := range rows {
		row := &rows[i]
		ref := permission.ObjectRef{ID: row.ID, ClientID: row.ClientID, SiteID: row.SiteID}
		readable := rowReader(policy, entityKind, row, &ref)
		row.Summary = joinSummary(row.SummaryParts, readable)
		if entityKind == "ci" {
			row.Attributes = projectCIAttributes(policy, row.TypeID, &ref, row.Attributes)
		} else {
			row.Attributes = projectByKey(row.Attributes, func(k string) bool { return readable("attributes." + k) })
		}
		if row.Columns != nil {
			row.Columns = projectByKey(row.Columns, readable)
		}
	}
}

// rowReader returns whether the principal may read a field (a path of
// the entity kind) on the row.
func rowReader(policy *permission.FieldPolicy, entityKind string, row *QueryResult, ref *permission.ObjectRef) func(path string) bool {
	return func(path string) bool {
		if entityKind == "ci" {
			if name, ok := strings.CutPrefix(path, "attributes."); ok {
				return ciAttributeReadable(policy, row.TypeID, ref, name, nil)
			}
		}
		key, checked, known := fieldKey(entityKind, path)
		switch {
		case !known:
			return false
		case !checked:
			return true
		}
		return policy.Level(objectKinds[entityKind], row.TypeID, key, *ref).Readable()
	}
}

// ciAttributeReadable reports whether the principal may read the CI
// attribute name: as a type attribute and, when instance names it, also
// as an instance attribute (fail-closed for a name that may be either).
func ciAttributeReadable(policy *permission.FieldPolicy, typeID string, ref *permission.ObjectRef, name string, instance map[string]bool) bool {
	path := "attributes." + name
	if !permission.Readable(policy, permission.KindCI, typeID, *ref, path) {
		return false
	}
	return !instance[name] || permission.Readable(policy, permission.KindCI, typeID, *ref, path, permission.WithInstanceKeys([]string{name}))
}

// projectByKey keeps the entries whose key keep accepts.
func projectByKey(m map[string]any, keep func(string) bool) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if keep(k) {
			out[k] = v
		}
	}
	return out
}

// Reserved namespaces of the stored CI attributes (CI-10, GLO-13).
const (
	nsInstance    = "_instance"
	nsOverrides   = "_overrides"
	nsObserved    = "_observed"
	nsProvenance  = "_provenance"
	attributePath = "attributes."
)

// projectCIAttributes projects the stored attributes of a CI: a business
// key as attributes.<key>, the instance attributes under _instance as
// _instance.<key>, the per-field namespaces _overrides, _observed and
// _provenance per field by its name (a typed column or an attribute,
// permission.CIFieldPath). A value of a namespace that is no object and
// any other key starting with _ are left out (fail-closed).
func projectCIAttributes(policy *permission.FieldPolicy, typeID string, ref *permission.ObjectRef, attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}
	instance := map[string]bool{}
	if nested, ok := attrs[nsInstance].(map[string]any); ok {
		for k := range nested {
			instance[k] = true
		}
	}
	field := func(name string) bool {
		path := permission.CIFieldPath(name)
		if attr, isAttr := strings.CutPrefix(path, attributePath); isAttr {
			return ciAttributeReadable(policy, typeID, ref, attr, instance)
		}
		return permission.Readable(policy, permission.KindCI, typeID, *ref, path)
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		switch k {
		case nsInstance:
			if nested, ok := v.(map[string]any); ok {
				out[k] = projectByKey(nested, func(name string) bool {
					return permission.Readable(policy, permission.KindCI, typeID, *ref, attributePath+name,
						permission.WithInstanceKeys([]string{name}))
				})
			}
		case nsOverrides, nsObserved, nsProvenance:
			if nested, ok := v.(map[string]any); ok {
				out[k] = projectByKey(nested, field)
			}
		default:
			if !strings.HasPrefix(k, "_") && permission.Readable(policy, permission.KindCI, typeID, *ref, attributePath+k) {
				out[k] = v
			}
		}
	}
	return out
}
