package savedview

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// CompileSQL renders the WHERE clause (without the keyword) and its args for
// the given entity kind ("ci" or "asset"). args are appended to the returned
// slice so the caller controls the parameter offset via startAt.
//
// Field names are fixed allow-listed identifiers — never user input — so the
// generated SQL is injection-safe; only values are parameterized. The spec
// is validated first (Validate), so no value fails a cast in SQL.
func (f FilterSpec) CompileSQL(entityKind string, args []any, startAt int) (string, []any, error) {
	if err := f.Validate(entityKind); err != nil {
		return "", nil, err
	}
	var parts []string
	add := func(clause string, vals ...any) {
		parts = append(parts, clause)
		args = append(args, vals...)
	}
	pos := startAt
	next := func() string {
		p := fmt.Sprintf("$%d", pos)
		pos++
		return p
	}

	table := "ci"
	// Dynamic attributes live in ci.attributes; assets carry custom_fields.
	jsonbCol := "ci.attributes"
	if entityKind == "asset" {
		table = "asset"
		jsonbCol = "asset.custom_fields"
	}

	if f.Search != "" {
		p := next()
		add(fmt.Sprintf("%s.name ILIKE %s", table, p), "%"+f.Search+"%")
	}
	if f.ClientID != "" {
		p := next()
		add(fmt.Sprintf("%s.client_id = %s", table, p), f.ClientID)
	}
	if f.CIType != "" {
		// Match by type key/name through ci_type.
		if table == "ci" {
			p := next()
			add(fmt.Sprintf(`%s.ci_type_id IN (SELECT id FROM ci_type WHERE key = %s OR name = %s)`, table, p, p), f.CIType)
		} else {
			p := next()
			add(fmt.Sprintf(`(%s.asset_type_id IN (SELECT id FROM ci_type WHERE key = %s OR name = %s)
				OR %s.ci_id IN (SELECT id FROM ci WHERE ci_type_id IN (SELECT id FROM ci_type WHERE key = %s OR name = %s)))`,
				table, p, p, table, p, p), f.CIType)
		}
	}
	if f.AssetCategory != "" && table == "asset" {
		p := next()
		add(fmt.Sprintf("%s.category = %s", table, p), f.AssetCategory)
	}
	if f.Lifecycle != "" {
		p := next()
		add(fmt.Sprintf("%s.lifecycle_state = %s", table, p), f.Lifecycle)
	}
	if f.Status != "" {
		p := next()
		add(fmt.Sprintf("%s.status = %s", table, p), f.Status)
	}
	if f.DiscoverySource != "" && table == "ci" {
		p := next()
		add(fmt.Sprintf("%s.discovery_source = %s", table, p), f.DiscoverySource)
	}
	if f.LocationID != "" {
		p := next()
		add(fmt.Sprintf("%s.location_id = %s", table, p), f.LocationID)
	}
	if f.LocationSubtree != "" {
		// Subtree of the canonical location tree by its ltree path.
		p := next()
		add(fmt.Sprintf(`%s.location_id IN (
			SELECT l.id FROM location l
			WHERE l.path <@ (SELECT r.path FROM location r WHERE r.id::text = %s))`, table, p), f.LocationSubtree)
	}
	if f.LocationSearch != "" {
		// Name-based subtree: seed the recursion with every location whose
		// name matches, then walk down. Only the value is parameterized.
		p := next()
		add(fmt.Sprintf(`%s.location_id IN (
			SELECT l.id FROM location l
			WHERE l.path <@ ANY (ARRAY(SELECT r.path FROM location r WHERE r.name ILIKE %s)))`, table, p), "%"+f.LocationSearch+"%")
	}
	if f.WarrantyWithinDays > 0 && table == "asset" {
		p := next()
		add(fmt.Sprintf("%s.warranty_end IS NOT NULL AND %s.warranty_end <= CURRENT_DATE + (%s || ' days')::interval", table, table, p), fmt.Sprintf("%d", f.WarrantyWithinDays))
	}
	if f.HasOwner != nil && table == "asset" {
		if *f.HasOwner {
			add(`EXISTS (SELECT 1 FROM assignment a WHERE a.asset_id = asset.id AND a.status = 'active')`)
		} else {
			add(`NOT EXISTS (SELECT 1 FROM assignment a WHERE a.asset_id = asset.id AND a.status = 'active')`)
		}
	}
	if f.Reserved != nil {
		if table == "asset" {
			if *f.Reserved {
				add(`EXISTS (SELECT 1 FROM reservation rv WHERE rv.asset_id = asset.id AND rv.state = 'active')`)
			} else {
				add(`NOT EXISTS (SELECT 1 FROM reservation rv WHERE rv.asset_id = asset.id AND rv.state = 'active')`)
			}
		}
	}
	if f.AvailableOnly && table == "asset" {
		add(`NOT EXISTS (SELECT 1 FROM reservation rv WHERE rv.asset_id = asset.id AND rv.state = 'active')`)
		add(`(asset.lifecycle_state IS NULL OR asset.lifecycle_state IN ('in_stock', 'received', 'ordered'))
			AND asset.status IN ('in_stock')`)
	}
	if f.HasRelationship != "" && table == "ci" {
		p := next()
		add(fmt.Sprintf(`EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = %s AND (r.source_ci_id = ci.id OR r.target_ci_id = ci.id))`, p), f.HasRelationship)
	}
	if f.LacksRelationship != "" && table == "ci" {
		p := next()
		add(fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = %s AND (r.source_ci_id = ci.id OR r.target_ci_id = ci.id))`, p), f.LacksRelationship)
	}
	if f.UpstreamOf != "" && table == "ci" {
		// CIs upstream of X: sources of edges pointing (transitively) into X,
		// at most MaxRelationshipDepth hops away (SRC-04). The walk runs
		// under RLS and only passes through visible, not deleted CIs.
		p := next()
		add(fmt.Sprintf(`ci.id IN (
			WITH RECURSIVE up (id, depth) AS (
				SELECT r.source_ci_id, 1 FROM ci_relationship r
				JOIN ci n ON n.id = r.source_ci_id AND n.deleted_at IS NULL
				WHERE r.target_ci_id = %s::uuid
				UNION
				SELECT r.source_ci_id, up.depth + 1 FROM ci_relationship r
				JOIN up ON r.target_ci_id = up.id
				JOIN ci n ON n.id = r.source_ci_id AND n.deleted_at IS NULL
				WHERE up.depth < %d
			) SELECT id FROM up)`, p, MaxRelationshipDepth), f.UpstreamOf)
	}
	if f.DownstreamOf != "" && table == "ci" {
		p := next()
		add(fmt.Sprintf(`ci.id IN (
			WITH RECURSIVE down (id, depth) AS (
				SELECT r.target_ci_id, 1 FROM ci_relationship r
				JOIN ci n ON n.id = r.target_ci_id AND n.deleted_at IS NULL
				WHERE r.source_ci_id = %s::uuid
				UNION
				SELECT r.target_ci_id, down.depth + 1 FROM ci_relationship r
				JOIN down ON r.source_ci_id = down.id
				JOIN ci n ON n.id = r.target_ci_id AND n.deleted_at IS NULL
				WHERE down.depth < %d
			) SELECT id FROM down)`, p, MaxRelationshipDepth), f.DownstreamOf)
	}
	if f.ReconciliationConflict && table == "ci" {
		add(`EXISTS (SELECT 1 FROM ci_field_value fv WHERE fv.ci_id = ci.id
			AND fv.override_value IS NOT NULL AND fv.discovered_value IS NOT NULL
			AND fv.override_value IS DISTINCT FROM fv.discovered_value)`)
	}
	if len(f.Tags) > 0 {
		// Tags live in the dynamic JSONB attribute bag (array of strings).
		for _, tag := range f.Tags {
			p := next()
			add(fmt.Sprintf(`%s->'tags' ? %s`, jsonbCol, p), tag)
		}
	}
	for k, v := range f.Attributes {
		// Dynamic attribute predicate: attributes/custom_fields ->> key = value.
		pk, pv := next(), next()
		add(fmt.Sprintf(`%s->>%s = %s`, jsonbCol, pk, pv), k, fmt.Sprintf("%v", v))
	}

	if f.Where != nil {
		clause, astArgs, _, err := f.Where.CompileSQL(entityKind, table, nil, pos)
		if err != nil {
			return "", nil, err
		}
		add(clause, astArgs...)
	}
	if len(parts) == 0 {
		return "true", args, nil
	}
	return strings.Join(parts, " AND "), args, nil
}

// Validate checks the values of the spec that SQL would cast before any
// query runs (SRC-03, API-03): the ids of client_id, location_id,
// upstream_of and downstream_of are UUIDs, the AST in where is valid for
// the entity kind, and so are the sort keys and columns of the run
// (validatePresentation). A violation is a 422 *api.FieldError naming the
// field, for the AST where.<path> (e.g. where.and[1].value).
func (f *FilterSpec) Validate(entityKind string) error {
	if err := validatePresentation(entityKind, f.Sort, f.Columns); err != nil {
		return err
	}
	for _, id := range []struct{ field, value string }{
		{"client_id", f.ClientID}, {"location_id", f.LocationID},
		{"upstream_of", f.UpstreamOf}, {"downstream_of", f.DownstreamOf},
	} {
		if id.value != "" && !isUUID(id.value) {
			return &api.FieldError{Field: id.field, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
		}
	}
	if f.Where == nil {
		return nil
	}
	err := f.Where.Validate(entityKind)
	var astErr *ASTError
	if errors.As(err, &astErr) {
		return &api.FieldError{Field: "where" + astErr.Path, Message: astErr.Message, Status: http.StatusUnprocessableEntity}
	}
	return err
}

// MaxRelationshipDepth bounds upstream_of and downstream_of (SRC-04).
const MaxRelationshipDepth = 5

// QueryResult is one row of a filter execution.
type QueryResult struct {
	ID         string         `json:"id"`
	EntityKind string         `json:"entity_kind"`
	Name       string         `json:"name"`
	Summary    string         `json:"summary,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	// Columns holds the value of each requested column (FilterSpec.Columns)
	// by field name; absent when the run asked for no columns.
	Columns map[string]any `json:"columns,omitempty"`
}

// Filter AST (SRC-03, MET-45): Where combines comparisons with and, or and
// not. A node is either a group (exactly one of And, Or, Not) or a
// comparison of a whitelisted field with an operator and a value. Fields
// are the columns of the entity, its dynamic attributes (attributes.<key>)
// and, for CIs, the fields of the linked asset (asset.<field>). Only values
// reach the SQL as parameters; columns come from the whitelist.

// Expr is one node of the filter AST.
type Expr struct {
	And   []Expr `json:"and,omitempty"`
	Or    []Expr `json:"or,omitempty"`
	Not   *Expr  `json:"not,omitempty"`
	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	Value any    `json:"value,omitempty"`
}

// Operators of a comparison.
const (
	OpEq     = "eq"
	OpNe     = "ne"
	OpLt     = "lt"
	OpLte    = "lte"
	OpGt     = "gt"
	OpGte    = "gte"
	OpLike   = "like"
	OpIn     = "in"
	OpExists = "exists"
)

// Limits of one AST.
const (
	MaxExprDepth = 10
	MaxExprNodes = 100
)

// Field kinds decide the casts and the allowed operators.
const (
	kindText   = "text"
	kindNumber = "number"
	kindTime   = "time"
	kindUUID   = "uuid"
	kindBool   = "bool"
)

type astField struct{ expr, kind string }

// astFields are the whitelisted fields per entity kind; %s is the table.
var astFields = map[string]map[string]astField{
	"ci": {
		"name": {"%s.name", kindText}, "status": {"%s.status", kindText}, "hostname": {"%s.hostname", kindText},
		"fqdn": {"%s.fqdn", kindText}, "serial_number": {"%s.serial_number", kindText},
		"manufacturer": {"%s.manufacturer", kindText}, "model": {"%s.model", kindText},
		"os_name": {"%s.os_name", kindText}, "os_version": {"%s.os_version", kindText},
		"management_ip": {"host(%s.management_ip)", kindText}, "lifecycle_state": {"%s.lifecycle_state", kindText},
		"discovery_source": {"%s.discovery_source", kindText}, "health": {"%s.health", kindText},
		"ci_type":   {"(SELECT COALESCE(t.key, t.name) FROM ci_type t WHERE t.id = %s.ci_type_id)", kindText},
		"client_id": {"%s.client_id", kindUUID}, "site_id": {"%s.site_id", kindUUID}, "location_id": {"%s.location_id", kindUUID},
		"is_manual": {"%s.is_manual", kindBool}, "created_at": {"%s.created_at", kindTime},
		"updated_at": {"%s.updated_at", kindTime}, "last_seen_at": {"%s.last_seen_at", kindTime},
	},
	"asset": {
		"name": {"%s.name", kindText}, "asset_tag": {"%s.asset_tag", kindText}, "category": {"%s.category", kindText},
		"status": {"%s.status", kindText}, "serial_number": {"%s.serial_number", kindText},
		"supplier": {"%s.supplier", kindText}, "lifecycle_state": {"%s.lifecycle_state", kindText},
		"barcode": {"%s.barcode", kindText}, "rfid_tag": {"%s.rfid_tag", kindText},
		"purchase_cost": {"%s.purchase_cost", kindNumber}, "purchase_date": {"%s.purchase_date", kindTime},
		"warranty_end": {"%s.warranty_end", kindTime}, "client_id": {"%s.client_id", kindUUID},
		"location_id": {"%s.location_id", kindUUID}, "created_at": {"%s.created_at", kindTime},
	},
}

// attributeKey is the syntax of a dynamic attribute key.
func validAttributeKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// ASTError is an invalid filter AST; the API answers 422 with its path.
type ASTError struct {
	Path    string
	Message string
}

func (e *ASTError) Error() string { return "where" + e.Path + ": " + e.Message }

// Validate checks the AST against the field whitelist of the entity kind,
// the operators of each field kind and the size limits.
func (e *Expr) Validate(entityKind string) error {
	nodes := 0
	return e.validate(entityKind, "", 1, &nodes)
}

func (e *Expr) validate(entityKind, path string, depth int, nodes *int) error {
	*nodes++
	if depth > MaxExprDepth {
		return &ASTError{path, fmt.Sprintf("nested deeper than %d levels", MaxExprDepth)}
	}
	if *nodes > MaxExprNodes {
		return &ASTError{path, fmt.Sprintf("more than %d nodes", MaxExprNodes)}
	}
	parts := 0
	for _, set := range []bool{len(e.And) > 0, len(e.Or) > 0, e.Not != nil, e.Field != ""} {
		if set {
			parts++
		}
	}
	if parts != 1 {
		return &ASTError{path, "a node is exactly one of and, or, not or a comparison"}
	}
	for i := range e.And {
		if err := e.And[i].validate(entityKind, fmt.Sprintf("%s.and[%d]", path, i), depth+1, nodes); err != nil {
			return err
		}
	}
	for i := range e.Or {
		if err := e.Or[i].validate(entityKind, fmt.Sprintf("%s.or[%d]", path, i), depth+1, nodes); err != nil {
			return err
		}
	}
	if e.Not != nil {
		return e.Not.validate(entityKind, path+".not", depth+1, nodes)
	}
	if e.Field == "" {
		return nil
	}
	kind, err := e.fieldKind(entityKind)
	if err != nil {
		return &ASTError{path, err.Error()}
	}
	return e.validateComparison(kind, path)
}

// fieldKind resolves the comparison's field. Attributes have no declared
// type and compare by the value's type (for in by the first element's): a
// number as number, a boolean as bool, a string as text, or as time when
// it is a time compared with lt, lte, gt or gte.
func (e *Expr) fieldKind(entityKind string) (string, error) {
	if key, ok := strings.CutPrefix(e.Field, "attributes."); ok {
		if !validAttributeKey(key) {
			return "", fmt.Errorf("invalid attribute key %q", key)
		}
		v := e.Value
		if list, isList := v.([]any); isList && len(list) > 0 {
			v = list[0]
		}
		switch v := v.(type) {
		case float64:
			return kindNumber, nil
		case bool:
			return kindBool, nil
		case string:
			if ordering(e.Op) && validTime(v) {
				return kindTime, nil
			}
		}
		return kindText, nil
	}
	if sub, ok := strings.CutPrefix(e.Field, "asset."); ok && entityKind == "ci" {
		f, known := astFields["asset"][sub]
		if !known {
			return "", fmt.Errorf("unknown asset field %q", sub)
		}
		return f.kind, nil
	}
	f, known := astFields[entityKind][e.Field]
	if !known {
		return "", fmt.Errorf("unknown field %q", e.Field)
	}
	return f.kind, nil
}

// validateComparison checks the operator for the field kind and every value
// against it (each element of in), so that no value reaches a cast in SQL
// that could fail (F-179).
func (e *Expr) validateComparison(kind, path string) error {
	switch e.Op {
	case OpExists:
		if _, isBool := e.Value.(bool); e.Value != nil && !isBool {
			return &ASTError{path + ".value", "exists takes true, false or no value"}
		}
		return nil
	case OpEq, OpNe:
	case OpLt, OpLte, OpGt, OpGte:
		if kind == kindUUID || kind == kindBool {
			return &ASTError{path, "operator " + e.Op + " does not apply to " + e.Field}
		}
	case OpLike:
		if kind != kindText {
			return &ASTError{path, "like applies to text fields only"}
		}
	case OpIn:
		values, ok := e.Value.([]any)
		if !ok || len(values) == 0 {
			return &ASTError{path + ".value", "in needs a non-empty list"}
		}
		for i, v := range values {
			if msg := valueError(kind, v); msg != "" {
				return &ASTError{fmt.Sprintf("%s.value[%d]", path, i), msg}
			}
		}
		return nil
	default:
		return &ASTError{path, fmt.Sprintf("unknown operator %q", e.Op)}
	}
	if msg := valueError(kind, e.Value); msg != "" {
		return &ASTError{path + ".value", msg}
	}
	return nil
}

// valueError reports why v is no value of the field kind, or "" when it is
// one. Attributes take their kind from the value, so for them only the
// elements of in after the first can disagree.
func valueError(kind string, v any) string {
	switch v.(type) {
	case string, float64, bool:
	default:
		return "must be a string, number or boolean"
	}
	switch kind {
	case kindNumber:
		if _, ok := v.(float64); !ok {
			return "must be a number"
		}
		return ""
	case kindBool:
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}
		return ""
	}
	s, ok := v.(string)
	switch {
	case !ok:
		return "must be a string"
	case strings.ContainsRune(s, 0):
		return "must not contain NUL characters"
	case kind == kindUUID && !isUUID(s):
		return "must be a UUID"
	case kind == kindTime && !validTime(s):
		return "must be an RFC 3339 time or a date (YYYY-MM-DD)"
	}
	return ""
}

// ordering reports whether op compares by order.
func ordering(op string) bool {
	return op == OpLt || op == OpLte || op == OpGt || op == OpGte
}

// rfc3339Shape is the form of an RFC 3339 time or a date. time.Parse is
// more lenient (a comma before the fraction, offset minute 60), which
// timestamptz refuses.
var rfc3339Shape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:[0-5]\d))?$`)

// validTime reports whether s is an RFC 3339 time or a date that
// PostgreSQL's timestamptz accepts: year 0001 or later and a UTC offset
// below 16 hours.
func validTime(s string) bool {
	if !rfc3339Shape.MatchString(s) {
		return false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.DateOnly, s)
	}
	if err != nil {
		return false
	}
	_, offset := t.Zone()
	return t.Year() >= 1 && offset > -16*3600 && offset < 16*3600
}

// CompileSQL renders the AST as a SQL predicate on table; args are
// appended and numbered from startAt.
func (e *Expr) CompileSQL(entityKind, table string, args []any, startAt int) (sql string, out []any, next int, err error) {
	next = startAt
	param := func(v any) string {
		args = append(args, v)
		p := fmt.Sprintf("$%d", next)
		next++
		return p
	}
	var compile func(n *Expr) (string, error)
	compile = func(n *Expr) (string, error) {
		group := func(items []Expr, sep string) (string, error) {
			parts := make([]string, len(items))
			for i := range items {
				s, cerr := compile(&items[i])
				if cerr != nil {
					return "", cerr
				}
				parts[i] = s
			}
			return "(" + strings.Join(parts, sep) + ")", nil
		}
		switch {
		case len(n.And) > 0:
			return group(n.And, " AND ")
		case len(n.Or) > 0:
			return group(n.Or, " OR ")
		case n.Not != nil:
			s, cerr := compile(n.Not)
			return "(NOT COALESCE(" + s + ", false))", cerr
		}
		kind, ferr := n.fieldKind(entityKind)
		if ferr != nil {
			return "", ferr
		}
		col, jsonValue, wrap := n.column(entityKind, table, param)
		return wrap(comparison(col, jsonValue, kind, n, param)), nil
	}
	sql, err = compile(e)
	return sql, args, next, err
}

// column returns the SQL expression of the field, for a dynamic attribute
// also its JSON value (jsonValue), and for asset fields of a CI the
// wrapper that evaluates the comparison on the linked asset.
func (e *Expr) column(entityKind, table string, param func(any) string) (col, jsonValue string, wrap func(string) string) {
	wrap = func(s string) string { return s }
	if key, ok := strings.CutPrefix(e.Field, "attributes."); ok {
		jsonb := table + ".attributes"
		if entityKind == "asset" {
			jsonb = table + ".custom_fields"
		}
		p := param(key)
		return fmt.Sprintf("%s->>%s", jsonb, p), fmt.Sprintf("%s->%s", jsonb, p), wrap
	}
	if sub, ok := strings.CutPrefix(e.Field, "asset."); ok && entityKind == "ci" {
		wrap = func(s string) string {
			return fmt.Sprintf("EXISTS (SELECT 1 FROM asset sa WHERE sa.id = %s.asset_id AND %s)", table, s)
		}
		return fmt.Sprintf(astFields["asset"][sub].expr, "sa"), "", wrap
	}
	return fmt.Sprintf(astFields[entityKind][e.Field].expr, table), "", wrap
}

// attributeGuards are the type checks under which comparison casts a
// dynamic attribute (%[1]s its JSON value, %[2]s its text). None of them
// can fail, so the result does not depend on the order in which
// PostgreSQL evaluates the predicates (F-165).
var attributeGuards = map[string]string{
	kindNumber: `jsonb_typeof(%[1]s) = 'number'`,
	kindBool:   `jsonb_typeof(%[1]s) = 'boolean'`,
	kindTime:   `jsonb_typeof(%[1]s) = 'string' AND %[2]s ~ '^\d{4}-\d{2}-\d{2}' AND pg_input_is_valid(%[2]s, 'timestamptz')`,
}

// comparison renders one comparison with the casts of the field kind. A
// dynamic attribute is cast only under its type check: a value of another
// type compares as NULL like a missing key, so it matches neither eq, in
// nor an order comparison, and like a missing key it matches ne (IS
// DISTINCT FROM). The values themselves are validated (validateComparison).
func comparison(col, jsonValue, kind string, e *Expr, param func(any) string) string {
	cast := map[string]string{kindText: "", kindNumber: "::numeric", kindTime: "::timestamptz", kindUUID: "::uuid", kindBool: "::boolean"}[kind]
	lhs := col
	if kind != kindText {
		lhs = "(" + col + ")" + cast
		if jsonValue != "" {
			lhs = "CASE WHEN " + fmt.Sprintf(attributeGuards[kind], jsonValue, col) + " THEN " + lhs + " END"
		}
	}
	value := func(v any) string { return param(fmt.Sprint(v)) + cast }
	switch e.Op {
	case OpExists:
		if b, _ := e.Value.(bool); e.Value != nil && !b {
			return col + " IS NULL"
		}
		return col + " IS NOT NULL"
	case OpLike:
		return lhs + " ILIKE " + param(likePattern(fmt.Sprint(e.Value)))
	case OpIn:
		values := e.Value.([]any)
		texts := make([]string, len(values))
		for i, v := range values {
			texts[i] = fmt.Sprint(v)
		}
		arrayCast := "::text[]"
		if kind != kindText {
			arrayCast = cast + "[]"
		}
		return lhs + " = ANY(" + param(texts) + arrayCast + ")"
	}
	op := map[string]string{OpEq: "=", OpNe: "IS DISTINCT FROM", OpLt: "<", OpLte: "<=", OpGt: ">", OpGte: ">="}[e.Op]
	return lhs + " " + op + " " + value(e.Value)
}

// likePattern escapes the LIKE metacharacters; * is the wildcard, without
// one the value matches as a substring.
func likePattern(v string) string {
	v = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
	if strings.Contains(v, "*") {
		return strings.ReplaceAll(v, "*", "%")
	}
	return "%" + v + "%"
}
