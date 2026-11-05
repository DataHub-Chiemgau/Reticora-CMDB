package savedview

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
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

// SpecChecker checks a filter spec against the catalog of the organization
// (attribute definitions, tag catalog) without running it (*PGQueryEngine):
// saving a view checks its filter by the rules of the query (WP-309).
type SpecChecker interface {
	CheckSpec(ctx context.Context, orgID string, spec *FilterSpec) error
}

// CheckSpec validates the spec and resolves its attributes and tags in
// WithRequestTenant as Query does, without running it. The errors are those
// of Query: a 422 or 400 *api.FieldError naming the field.
func (e *PGQueryEngine) CheckSpec(ctx context.Context, orgID string, spec *FilterSpec) error {
	return database.WithRequestTenant(ctx, e.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cat, err := resolveCatalog(ctx, tx, spec)
		if err != nil {
			return err
		}
		_, _, err = spec.compile(spec.entityKind(), cat, nil, 1)
		return err
	})
}

// Query executes the filter spec in WithRequestTenant, i.e. under RLS with
// the principal's full scope (client, site, team). entity_kind selects the
// table: "ci" or "asset". Unknown kinds are rejected; deleted CIs are never
// returned. The attributes and tags of the spec are resolved in the
// transaction first (resolveCatalog, API-02, WP-309): an unknown attribute
// or tag is a 422, an attribute that is not searchable a 400.
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
		cat, err := resolveCatalog(ctx, tx, spec)
		if err != nil {
			return err
		}
		where, args, err := spec.compile(spec.entityKind(), cat, []any{orgID}, 2)
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

// attributeUse is a dynamic attribute of a spec and the field that names it
// in an error; related marks an attribute of the related CIs of a related
// node (WP-789).
type attributeUse struct {
	ref     attrRef
	field   string
	related bool
}

// keyUse is a tag key or relationship type key of a spec and the field
// that names it in an error.
type keyUse struct{ key, field string }

// specUses returns the dynamic attributes the spec compares (the flat
// attributes, attributes.<key> and asset.attributes.<key> of the AST, also
// in the filter of a related node), the tag keys it names (the flat tags,
// the values of has_any and has_all) and the relationship types of its
// related nodes, in the order of the spec.
func specUses(spec *FilterSpec) (attrs []attributeUse, tags, relTypes []keyUse) {
	entityKind := spec.entityKind()
	for _, k := range sortedKeys(spec.Attributes) {
		attrs = append(attrs, attributeUse{attrRef{entityKind, k}, "attributes." + k, false})
	}
	for i, tag := range spec.Tags {
		tags = append(tags, keyUse{tag, fmt.Sprintf("tags[%d]", i)})
	}
	if spec.Where != nil {
		spec.Where.walkPaths("", func(n *Expr, path string) {
			kind := entityKind
			if inRelated(path) {
				kind = "ci"
			}
			if ref, ok := attributeRef(kind, n.Field); ok {
				attrs = append(attrs, attributeUse{ref, "where" + path, inRelated(path)})
			}
			if values, ok := n.Value.([]any); ok && n.Field == TagsField {
				for i, v := range values {
					tags = append(tags, keyUse{fmt.Sprint(v), fmt.Sprintf("where%s.value[%d]", path, i)})
				}
			}
			if n.Related != nil {
				for i, key := range n.Related.RelTypes {
					relTypes = append(relTypes, keyUse{key, fmt.Sprintf("where%s.related.rel_types[%d]", path, i)})
				}
			}
		})
	}
	return attrs, tags, relTypes
}

// knownKeys checks that query (with the keys as $1) finds every key of
// uses; an unknown one is a 422 naming its field (what: tag, relationship
// type).
func knownKeys(ctx context.Context, tx pgx.Tx, query, what string, uses []keyUse) error {
	if len(uses) == 0 {
		return nil
	}
	keys := make([]string, len(uses))
	for i, u := range uses {
		keys[i] = u.key
	}
	rows, err := tx.Query(ctx, query, keys)
	if err != nil {
		return fmt.Errorf("%ss of the filter: %w", what, err)
	}
	known, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("%ss of the filter: %w", what, err)
	}
	for _, u := range uses {
		if !slices.Contains(known, u.key) {
			return &api.FieldError{Field: u.field, Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf("unknown %s %q", what, u.key)}
		}
	}
	return nil
}

// resolveCatalog validates the spec and resolves, under the caller's row
// level security, the definitions of the dynamic attributes it compares
// (attributeDefinition), the tag keys it names and the relationship types
// of its related nodes, system and organization types of relationship_type
// (WP-789): an unknown tag or type is a 422 naming the value. The
// attributes of the filter of a related node belong to the related CIs and
// resolve for every type.
func resolveCatalog(ctx context.Context, tx pgx.Tx, spec *FilterSpec) (*catalog, error) {
	entityKind := spec.entityKind()
	if err := spec.Validate(entityKind); err != nil {
		return nil, err
	}
	attrs, tags, relTypes := specUses(spec)
	cat := &catalog{attrs: map[attrRef]resolvedAttribute{}, relAttrs: map[attrRef]resolvedAttribute{}}
	if len(attrs) > 0 {
		// The types of a CI query that names ci_type (key or name, as the
		// filter compares it); an asset query and the linked asset count
		// every type.
		var ciTypes []string
		named := entityKind == "ci" && spec.CIType != ""
		if named {
			rows, err := tx.Query(ctx, `SELECT id::text FROM ci_type WHERE key = $1 OR name = $1`, spec.CIType)
			if err != nil {
				return nil, fmt.Errorf("types of the filter: %w", err)
			}
			if ciTypes, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return nil, fmt.Errorf("types of the filter: %w", err)
			}
		}
		for _, u := range attrs {
			into := cat.attrs
			if u.related {
				into = cat.relAttrs
			}
			if _, done := into[u.ref]; done {
				continue
			}
			a, err := attributeDefinition(ctx, tx, u.ref.key, u.field, ciTypes, named && u.ref.entity == "ci" && !u.related)
			if err != nil {
				return nil, err
			}
			into[u.ref] = a
		}
	}
	if err := knownKeys(ctx, tx, `SELECT key FROM tag WHERE key = ANY($1::text[])`, "tag", tags); err != nil {
		return nil, err
	}
	if err := knownKeys(ctx, tx, `SELECT DISTINCT key FROM relationship_type WHERE key = ANY($1::text[])`, "relationship type", relTypes); err != nil {
		return nil, err
	}
	return cat, nil
}

// attributeDefinition resolves an attribute the filter compares (API-02)
// by the contract of attributeFilterOf of the CI list (ci/pg_repository.go,
// WP-566, WP-708): the definitions of the named types (types, when named)
// with the inheritable ones of their ancestors and the global ones apply,
// else every definition of the key counts. Not defined for any of them is
// a 422, searchable for none a 400 with the hint, searchable with different
// filter kinds a 400 with the hint to name the type. The kind is
// fieldmeta.FilterKind of the data type; the hits are restricted to the
// types the attribute is searchable for unless a searchable global
// definition covers every type. A computed attribute (WP-258) is resolved
// like any other. field names the attribute in the error.
func attributeDefinition(ctx context.Context, tx pgx.Tx, key, field string, types []string, named bool) (resolvedAttribute, error) {
	const covers = `CASE WHEN d.ci_type_id IS NULL OR NOT d.searchable THEN '{}'::text[]
			WHEN d.inheritable THEN ARRAY(SELECT s.id::text FROM ci_type_subtree(d.ci_type_id) s)
			ELSE ARRAY[d.ci_type_id::text] END`
	query, args := `SELECT d.ci_type_id IS NULL, d.data_type, d.searchable, `+covers+`
		FROM attribute_definition d WHERE d.key = $1`, []any{key}
	if named {
		query += ` AND (d.ci_type_id IS NULL OR d.ci_type_id = ANY($2::uuid[])
			OR (d.inheritable AND d.ci_type_id IN (SELECT a.id FROM unnest($2::uuid[]) AS n(id), LATERAL ci_type_ancestors(n.id) a)))`
		args = append(args, types)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return resolvedAttribute{}, fmt.Errorf("attribute definition %s: %w", key, err)
	}
	var (
		untyped, searchable bool
		dataType            string
		subtree             []string
		defined, global     bool
		kinds, covered      []string
	)
	if _, err = pgx.ForEachRow(rows, []any{&untyped, &dataType, &searchable, &subtree}, func() error {
		defined = true
		if !searchable {
			return nil
		}
		if k := fieldmeta.FilterKind(dataType); !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
		global = global || untyped
		for _, t := range subtree {
			if !slices.Contains(covered, t) {
				covered = append(covered, t)
			}
		}
		return nil
	}); err != nil {
		return resolvedAttribute{}, fmt.Errorf("attribute definition %s: %w", key, err)
	}
	switch {
	case !defined && named:
		return resolvedAttribute{}, &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("unknown attribute %q for the given ci_type", key)}
	case !defined:
		return resolvedAttribute{}, &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf("unknown attribute %q", key)}
	case len(kinds) == 0:
		return resolvedAttribute{}, &api.FieldError{Field: field, Status: http.StatusBadRequest,
			Message: fmt.Sprintf("attribute %q is not searchable; mark it searchable in its attribute definition to filter on it", key)}
	case len(kinds) > 1:
		return resolvedAttribute{}, &api.FieldError{Field: field, Status: http.StatusBadRequest,
			Message: fmt.Sprintf("attribute %q has different data types in the types that make it searchable; give ci_type to filter on it", key)}
	}
	if global {
		return resolvedAttribute{kind: kinds[0]}, nil
	}
	slices.Sort(covered)
	return resolvedAttribute{kind: kinds[0], types: covered}, nil
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
