package savedview

import (
	"fmt"
	"strings"
)

// CompileSQL renders the WHERE clause (without the keyword) and its args for
// the given entity kind ("ci" or "asset"). args are appended to the returned
// slice so the caller controls the parameter offset via startAt.
//
// Field names are fixed allow-listed identifiers — never user input — so the
// generated SQL is injection-safe; only values are parameterized.
func (f FilterSpec) CompileSQL(entityKind string, args []any, startAt int) (string, []any, error) {
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
		if err := f.Where.Validate(entityKind); err != nil {
			return "", nil, err
		}
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

// MaxRelationshipDepth bounds upstream_of and downstream_of (SRC-04).
const MaxRelationshipDepth = 5

// QueryResult is one row of a filter execution.
type QueryResult struct {
	ID         string         `json:"id"`
	EntityKind string         `json:"entity_kind"`
	Name       string         `json:"name"`
	Summary    string         `json:"summary,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
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

// fieldKind resolves the comparison's field; attributes compare by the
// value's type.
func (e *Expr) fieldKind(entityKind string) (string, error) {
	if key, ok := strings.CutPrefix(e.Field, "attributes."); ok {
		if !validAttributeKey(key) {
			return "", fmt.Errorf("invalid attribute key %q", key)
		}
		if _, isNumber := e.Value.(float64); isNumber {
			return kindNumber, nil
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

func (e *Expr) validateComparison(kind, path string) error {
	switch e.Op {
	case OpExists:
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
			return &ASTError{path, "in needs a non-empty list"}
		}
		return nil
	default:
		return &ASTError{path, fmt.Sprintf("unknown operator %q", e.Op)}
	}
	switch e.Value.(type) {
	case string, float64, bool:
		return nil
	}
	return &ASTError{path, "the value must be a string, number or boolean"}
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
		col, wrap := n.column(entityKind, table, param)
		return wrap(comparison(col, kind, n, param)), nil
	}
	sql, err = compile(e)
	return sql, args, next, err
}

// column returns the SQL expression of the field and, for asset fields of
// a CI, the wrapper that evaluates the comparison on the linked asset.
func (e *Expr) column(entityKind, table string, param func(any) string) (col string, wrap func(string) string) {
	wrap = func(s string) string { return s }
	if key, ok := strings.CutPrefix(e.Field, "attributes."); ok {
		jsonb := table + ".attributes"
		if entityKind == "asset" {
			jsonb = table + ".custom_fields"
		}
		return fmt.Sprintf("%s->>%s", jsonb, param(key)), wrap
	}
	if sub, ok := strings.CutPrefix(e.Field, "asset."); ok && entityKind == "ci" {
		wrap = func(s string) string {
			return fmt.Sprintf("EXISTS (SELECT 1 FROM asset sa WHERE sa.id = %s.asset_id AND %s)", table, s)
		}
		return fmt.Sprintf(astFields["asset"][sub].expr, "sa"), wrap
	}
	return fmt.Sprintf(astFields[entityKind][e.Field].expr, table), wrap
}

// comparison renders one comparison with the casts of the field kind.
func comparison(col, kind string, e *Expr, param func(any) string) string {
	cast := map[string]string{kindText: "", kindNumber: "::numeric", kindTime: "::timestamptz", kindUUID: "::uuid", kindBool: "::boolean"}[kind]
	lhs := col
	if kind != kindText {
		lhs = "(" + col + ")" + cast
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
