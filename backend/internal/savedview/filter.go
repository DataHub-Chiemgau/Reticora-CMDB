package savedview

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
)

// CompileSQL renders the WHERE clause (without the keyword) and its args for
// the given entity kind ("ci" or "asset"). args are appended to the returned
// slice so the caller controls the parameter offset via startAt.
//
// Field names are fixed allow-listed identifiers — never user input — so the
// generated SQL is injection-safe; only values are parameterized. The spec
// is validated first (Validate), so no value fails a cast in SQL.
//
// This is the static compilation without the catalog of the organization:
// dynamic attributes compare by the type of their value and tags are not
// checked against the tag catalog. PGQueryEngine compiles with the catalog
// it resolves in the transaction of the query (compile, WP-309).
func (f *FilterSpec) CompileSQL(entityKind string, args []any, startAt int) (where string, whereArgs []any, err error) {
	return f.compile(entityKind, nil, args, startAt)
}

// compile is CompileSQL with the catalog cat (nil: static).
func (f *FilterSpec) compile(entityKind string, cat *catalog, args []any, startAt int) (where string, whereArgs []any, err error) {
	if verr := f.validate(entityKind, cat); verr != nil {
		return "", nil, verr
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
	if f.CITypeSubtree != "" {
		// The type of the key or name and every subtype (ci_type_subtree,
		// WP-248); for an asset through its type or the linked CI. The
		// subtree is an array the query computes once (an init plan): as
		// IN (subquery) the planner, which estimates one visible CI under
		// ci_isolation, joined it in a nested loop and walked the type
		// tree again for every CI (finding of the load run of PR #147,
		// the preset servers_without_monitoring).
		p := next()
		subtree := fmt.Sprintf(`ARRAY(SELECT s.id FROM ci_type t CROSS JOIN LATERAL ci_type_subtree(t.id) s WHERE t.key = %s OR t.name = %s)`, p, p)
		if table == "ci" {
			add(fmt.Sprintf(`ci.ci_type_id = ANY (%s)`, subtree), f.CITypeSubtree)
		} else {
			add(fmt.Sprintf(`(asset.asset_type_id = ANY (%s)
				OR asset.ci_id IN (SELECT id FROM ci WHERE ci_type_id = ANY (%s)))`, subtree, subtree), f.CITypeSubtree)
		}
	}
	if f.OwnedByMe {
		add(ownedByMeSQL(table))
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
		add(fmt.Sprintf("%s.warranty_until IS NOT NULL AND %s.warranty_until <= CURRENT_DATE + (%s || ' days')::interval", table, table, p), fmt.Sprintf("%d", f.WarrantyWithinDays))
	}
	if f.HasOwner != nil && table == "asset" {
		// The holder cache of the asset (WP-745, kept by the assignment
		// service; WP-747): a user or a team holds it.
		if *f.HasOwner {
			add(`(asset.assigned_user_id IS NOT NULL OR asset.assigned_team_id IS NOT NULL)`)
		} else {
			add(`(asset.assigned_user_id IS NULL AND asset.assigned_team_id IS NULL)`)
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
		// The state is lifecycle_state (E-120 (b), WP-738); only an asset
		// without a state falls back to the deprecated status.
		add(`(asset.lifecycle_state IN ('in_stock', 'received', 'ordered')
			OR (asset.lifecycle_state IS NULL AND asset.status = 'in_stock'))`)
	}
	if f.HasRelationship != "" && table == "ci" {
		p := next()
		add(relationshipExists(f.HasRelationship, p), f.HasRelationship)
	}
	if f.LacksRelationship != "" && table == "ci" {
		p := next()
		add("NOT "+relationshipExists(f.LacksRelationship, p), f.LacksRelationship)
	}
	if f.LacksOutgoingRelationship != "" && table == "ci" {
		p := next()
		add("NOT "+directedRelationshipExists(f.LacksOutgoingRelationship, p, "source_ci_id"), f.LacksOutgoingRelationship)
	}
	if f.LacksIncomingRelationship != "" && table == "ci" {
		p := next()
		add("NOT "+directedRelationshipExists(f.LacksIncomingRelationship, p, "target_ci_id"), f.LacksIncomingRelationship)
	}
	if f.NoMetricWithinHours > 0 && table == "ci" {
		// The samples the caller may read (metric_sample_v, scoped like
		// the CI; idx_metric_sample_ci on ci_id, time). A scalar subquery
		// per CI, not NOT EXISTS: the security barrier of the view keeps
		// the join clause ms.ci_id = ci.id out of its scan, so the anti
		// join the planner made of NOT EXISTS read every sample of the
		// window once per CI. The subquery probes idx_metric_sample_ci
		// with the id of the CI; it is NULL exactly when no sample exists.
		p := next()
		add(fmt.Sprintf(`(SELECT 1 FROM metric_sample_v ms WHERE ms.ci_id = ci.id
			AND ms.time >= now() - make_interval(hours => %s::int) LIMIT 1) IS NULL`, p), f.NoMetricWithinHours)
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
	// Each tag is required (AND), the has_all of the AST (tagExists).
	for _, tag := range f.Tags {
		p := next()
		add(tagExists(table, "t.key = "+p), tag)
	}
	if cat == nil {
		for k, v := range f.Attributes {
			// Dynamic attribute predicate: attributes/custom_fields ->> key = value.
			pk, pv := next(), next()
			add(fmt.Sprintf(`%s->>%s = %s`, jsonbCol, pk, pv), k, fmt.Sprintf("%v", v))
		}
	} else {
		// With the catalog a flat attribute is the comparison eq of the
		// AST: typed by its definition, over the expression of the
		// attribute indexes (API-02, WP-309).
		for _, k := range sortedKeys(f.Attributes) {
			clause, attrArgs, nextPos, cerr := (&Expr{Field: "attributes." + k, Op: OpEq, Value: f.Attributes[k]}).compile(entityKind, table, cat, nil, pos)
			if cerr != nil {
				return "", nil, cerr
			}
			pos = nextPos
			add(clause, attrArgs...)
		}
	}

	if f.Where != nil {
		clause, astArgs, _, cerr := f.Where.compile(entityKind, table, cat, nil, pos)
		if cerr != nil {
			return "", nil, cerr
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
	return f.validate(entityKind, nil)
}

// validate is Validate with the catalog cat: with one, the values of the
// flat attributes and of the attribute comparisons are checked against the
// kind of their definition (WP-309).
func (f *FilterSpec) validate(entityKind string, cat *catalog) error {
	if err := validatePresentation(entityKind, f.Sort, f.Columns); err != nil {
		return err
	}
	for _, id := range []struct{ field, value string }{
		{"client_id", f.ClientID}, {"location_id", f.LocationID},
		{"upstream_of", f.UpstreamOf}, {"downstream_of", f.DownstreamOf},
	} {
		if id.value != "" && !api.IsUUID(id.value) {
			return &api.FieldError{Field: id.field, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
		}
	}
	if f.NoMetricWithinHours < 0 || f.NoMetricWithinHours > MaxMetricWindowHours {
		return &api.FieldError{Field: "no_metric_within_hours", Message: fmt.Sprintf("must be between 1 and %d", MaxMetricWindowHours), Status: http.StatusUnprocessableEntity}
	}
	for _, key := range []struct{ field, value string }{
		{"ci_type_subtree", f.CITypeSubtree},
		{"lacks_outgoing_relationship", f.LacksOutgoingRelationship},
		{"lacks_incoming_relationship", f.LacksIncomingRelationship},
	} {
		if strings.ContainsRune(key.value, 0) {
			return &api.FieldError{Field: key.field, Message: "must not contain NUL characters", Status: http.StatusUnprocessableEntity}
		}
	}
	for i, tag := range f.Tags {
		if tag == "" || strings.ContainsRune(tag, 0) {
			return &api.FieldError{Field: fmt.Sprintf("tags[%d]", i), Message: "must be a tag key", Status: http.StatusUnprocessableEntity}
		}
	}
	for _, k := range sortedKeys(f.Attributes) {
		if !validAttributeKey(k) {
			return &api.FieldError{Field: "attributes." + k, Message: "invalid attribute key", Status: http.StatusUnprocessableEntity}
		}
		if cat == nil {
			continue
		}
		if a, ok := cat.attrs[attrRef{entityKind, k}]; ok {
			if msg := valueError(a.kind, f.Attributes[k], true); msg != "" {
				return &api.FieldError{Field: "attributes." + k, Message: msg, Status: http.StatusUnprocessableEntity}
			}
		}
	}
	if f.Where == nil {
		return nil
	}
	nodes := 0
	err := f.Where.validate(entityKind, "", 1, &nodes, cat)
	var astErr *ASTError
	if errors.As(err, &astErr) {
		return &api.FieldError{Field: "where" + astErr.Path, Message: astErr.Message, Status: http.StatusUnprocessableEntity}
	}
	return err
}

// MaxRelationshipDepth bounds upstream_of and downstream_of (SRC-04).
const MaxRelationshipDepth = 5

// MaxMetricWindowHours bounds no_metric_within_hours (one year).
const MaxMetricWindowHours = 8760

// currentUser is the calling user of the request transaction: app.user_id,
// set from the principal by database.WithRequestTenant; NULL without one.
const currentUser = `NULLIF(current_setting('app.user_id', true), '')::uuid`

// ownedByMeSQL is the predicate of owned_by_me on table (VIE-02): the
// calling user is the owner user, or one of the user's teams
// (team_member) the owner team; for an asset also the current holder of
// the holder cache (asset.assigned_user_id, assigned_team_id; WP-745,
// WP-747). The contact role owner (ci_contact) does not count.
func ownedByMeSQL(table string) string {
	teams := `(SELECT tm.team_id FROM team_member tm WHERE tm.user_id = ` + currentUser + `)`
	clause := fmt.Sprintf(`%[1]s.owner_user_id = %[2]s OR %[1]s.owner_team_id IN %[3]s`, table, currentUser, teams)
	if table == "asset" {
		clause += fmt.Sprintf(` OR asset.assigned_user_id = %s OR asset.assigned_team_id IN %s`, currentUser, teams)
	}
	return "(" + clause + ")"
}

// directedRelationshipExists is the predicate of lacks_outgoing_relationship
// (end source_ci_id) and lacks_incoming_relationship (end target_ci_id),
// negated: the CI is that end of a relationship of type p, stored or, for a
// type of projectedRelTypes, projected (as relationshipExists reads them,
// with the visible CI or location at the other end).
func directedRelationshipExists(relType, p, end string) string {
	stored := fmt.Sprintf(`EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = %s AND r.%s = ci.id)`, p, end)
	if !projectedRelTypes[relType] {
		return stored
	}
	if end == "source_ci_id" {
		return fmt.Sprintf(`(%s
		OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = %s AND e.source_ci_id = ci.id
			AND (e.target_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci t WHERE t.id = e.target_ci_id))))`, stored, p)
	}
	return fmt.Sprintf(`(%s
		OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = %s AND e.target_ci_id = ci.id
			AND EXISTS (SELECT 1 FROM ci s WHERE s.id = e.source_ci_id)))`, stored, p)
}

// projectedRelTypes are the structural relationship types of the view
// ci_structure_edge (DB-05, CH27): contains from ci.parent_ci_id,
// mounted_in from rack_mount and located_in from ci.location_id. They are
// never stored in ci_relationship (F-244).
var projectedRelTypes = map[string]bool{"contains": true, "mounted_in": true, "located_in": true}

// relationshipExists is the predicate of has_relationship (and, negated,
// of lacks_relationship): the CI is an end of a relationship of type p, a
// stored one or, for a type of projectedRelTypes, a projected one (F-244).
// Both read under the caller's RLS: ci_relationship carries the scope of
// both ends, the view ci_structure_edge runs as the invoker and is
// joined with the visible CI of the other end, as the relationship API
// lists projected edges (internal/relationship); the target of mounted_in and
// located_in is a location. The projection is read with one arm per end
// of the edge, each an equality on the CI, as the relationship API lists
// the edges of a CI (WP-609).
func relationshipExists(relType, p string) string {
	stored := fmt.Sprintf(`EXISTS (SELECT 1 FROM ci_relationship r WHERE r.rel_type = %s AND (r.source_ci_id = ci.id OR r.target_ci_id = ci.id))`, p)
	if !projectedRelTypes[relType] {
		return stored
	}
	return fmt.Sprintf(`(%s
		OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = %s AND e.source_ci_id = ci.id
			AND (e.target_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci t WHERE t.id = e.target_ci_id)))
		OR EXISTS (SELECT 1 FROM ci_structure_edge e WHERE e.rel_type = %s AND e.target_ci_id = ci.id
			AND EXISTS (SELECT 1 FROM ci s WHERE s.id = e.source_ci_id)))`, stored, p, p)
}

// relatedLevel is the nesting level of a related node compiled on table:
// 1 on the queried table, one more within the filter of a related node
// (whose CIs are rn<level>).
func relatedLevel(table string) int {
	if n, ok := strings.CutPrefix(table, "rn"); ok {
		if level, err := strconv.Atoi(n); err == nil {
			return level + 1
		}
	}
	return 1
}

// relatedSQL renders a related node (SRC-04, WP-789) on table as a
// recursive CTE: its start set are the CIs that are not deleted and match
// the filter (sub, compiled on rn<level>); each step follows one
// relationship from a CI of the set to the CI at its other end, at most
// max_depth steps; the CIs reached in one step or more match. With out a
// step goes from the target of a relationship to its source, with in from
// the source to the target, with both either way. Like upstream_of and
// downstream_of the walk runs under the caller's RLS (ci_relationship
// carries the scope of both ends) and only passes through visible CIs that
// are not deleted. The steps are a LATERAL union with one arm per
// direction and source, each an equality on the CI of the set, so that
// every arm reads idx_ci_rel_org_target or idx_ci_rel_org_source. The
// structural types of projectedRelTypes are read from the view
// ci_structure_edge where both ends are CIs (contains; mounted_in and
// located_in end at a location), as relationshipExists reads them (F-244).
// The type keys are parameters, the depth a validated number.
func relatedSQL(table string, level int, r *RelatedExpr, sub string, param func(any) string) string {
	var stored, projected []string
	for _, key := range r.RelTypes {
		if projectedRelTypes[key] {
			projected = append(projected, key)
		} else {
			stored = append(stored, key)
		}
	}
	all := len(r.RelTypes) == 0
	if all {
		for key := range projectedRelTypes {
			projected = append(projected, key)
		}
	}
	sort.Strings(projected)
	walk, start, step, reached := fmt.Sprintf("rel%d", level), fmt.Sprintf("rn%d", level), fmt.Sprintf("rs%d", level), fmt.Sprintf("rc%d", level)
	// The ends of a step per direction: near is the end in the set, far
	// the end it reaches.
	type ends struct{ near, far string }
	var steps []ends
	if r.Direction != DirectionIn {
		steps = append(steps, ends{"target_ci_id", "source_ci_id"})
	}
	if r.Direction != DirectionOut {
		steps = append(steps, ends{"source_ci_id", "target_ci_id"})
	}
	var arms []string
	if all || len(stored) > 0 {
		cond := ""
		if !all {
			cond = " AND r.rel_type = ANY(" + param(stored) + "::text[])"
		}
		for _, s := range steps {
			arms = append(arms, fmt.Sprintf("SELECT r.%s AS far FROM ci_relationship r WHERE r.%s = %s.id%s", s.far, s.near, walk, cond))
		}
	}
	if len(projected) > 0 {
		p := param(projected)
		for _, s := range steps {
			arms = append(arms, fmt.Sprintf("SELECT e.%s AS far FROM ci_structure_edge e WHERE e.%s = %s.id AND e.rel_type = ANY(%s::text[]) AND e.target_ci_id IS NOT NULL",
				s.far, s.near, walk, p))
		}
	}
	return fmt.Sprintf(`%[1]s.id IN (WITH RECURSIVE %[2]s (id, depth) AS (
				SELECT %[3]s.id, 0 FROM ci %[3]s WHERE %[3]s.deleted_at IS NULL AND %[4]s
				UNION
				SELECT %[5]s.id, %[2]s.depth + 1 FROM %[2]s
				CROSS JOIN LATERAL (%[6]s) %[7]s
				JOIN ci %[5]s ON %[5]s.id = %[7]s.far AND %[5]s.deleted_at IS NULL
				WHERE %[2]s.depth < %[8]d
			) SELECT id FROM %[2]s WHERE depth > 0)`,
		table, walk, start, sub, reached, strings.Join(arms, " UNION ALL "), step, r.depth())
}

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
	// TypeID, ClientID and SiteID are the type (ci_type_id, asset_type_id)
	// and scope of the row, SummaryParts the columns summary joins: the
	// field projection needs them (RBA-07, WP-729). Not part of the API.
	TypeID       string        `json:"-"`
	ClientID     string        `json:"-"`
	SiteID       string        `json:"-"`
	SummaryParts []SummaryPart `json:"-"`
}

// SummaryPart is a column of the summary of a row with its value; a NULL
// column is no part.
type SummaryPart struct {
	Field string
	Value string
}

// FieldRef is a field a filter spec reads (RBA-07, WP-729): the entity kind
// whose field permissions cover it ("ci" or "asset"; asset.<field> of a CI
// query is "asset"), its path in that kind (a column or attributes.<key>)
// and the name the request gives it, which a refusal names. Related marks a
// field of the related CIs of a related node (WP-789): their types are not
// those of ci_type of the spec.
type FieldRef struct {
	EntityKind string
	Path       string
	Name       string
	Related    bool
}

// ReferencedFields returns every field the spec reads for the entity kind,
// in the order of the spec and without duplicates: the flat fields on
// columns (asset_category as category, warranty_within_days as
// warranty_until, lifecycle, status and the location filters as
// location_id, discovery_source, ci_type as ci_type_id, search as name),
// tags as attributes.tags, the keys of attributes as attributes.<key>,
// every comparison of the AST in where (asset.<field> and
// asset.attributes.<key> of a CI as the asset's field, the tag comparisons
// as attributes.tags; between and computed attributes are comparisons of
// their field; the fields of the filter of a related node as fields of the
// related CIs, Related), the sort keys and the columns. The caller decides
// which of them field permissions cover (WP-729, WP-309, WP-789).
func (f *FilterSpec) ReferencedFields(entityKind string) []FieldRef {
	var out []FieldRef
	seen := map[FieldRef]bool{}
	related := false // adding the fields of a related node
	add := func(kind, path, name string) {
		ref := FieldRef{EntityKind: kind, Path: path, Name: name, Related: related && kind == "ci"}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	// field adds a field of the AST, a sort key or a column.
	field := func(name string) {
		switch sub, isAsset := strings.CutPrefix(name, "asset."); {
		case isAsset && entityKind == "ci":
			// A column of the linked asset or, as asset.attributes.<key>,
			// one of its custom fields (WP-309).
			add("asset", sub, name)
		case name == TagsField:
			// The tag comparisons (has_any, has_all) read the tags like
			// the flat tags (WP-309).
			add(entityKind, "attributes.tags", name)
		case name == "ci_type":
			add(entityKind, "ci_type_id", name)
		default:
			add(entityKind, name, name)
		}
	}
	isAsset := entityKind == "asset"
	for _, flat := range []struct {
		set        bool
		path, name string
	}{
		{f.Search != "", "name", "search"},
		{f.ClientID != "", "client_id", "client_id"},
		{f.CIType != "", "ci_type_id", "ci_type"},
		{f.CITypeSubtree != "", "ci_type_id", "ci_type_subtree"},
		{f.OwnedByMe, "owner_user_id", "owned_by_me"},
		{f.OwnedByMe, "owner_team_id", "owned_by_me"},
		{f.AssetCategory != "" && isAsset, "category", "asset_category"},
		{f.Lifecycle != "", "lifecycle_state", "lifecycle"},
		{f.Status != "", "status", "status"},
		{f.DiscoverySource != "" && !isAsset, "discovery_source", "discovery_source"},
		{f.LocationID != "", "location_id", "location_id"},
		{f.LocationSubtree != "", "location_id", "location_subtree"},
		{f.LocationSearch != "", "location_id", "location_search"},
		{f.WarrantyWithinDays > 0 && isAsset, "warranty_until", "warranty_within_days"},
		{f.AvailableOnly && isAsset, "status", "available_only"},
		{f.AvailableOnly && isAsset, "lifecycle_state", "available_only"},
	} {
		if flat.set {
			add(entityKind, flat.path, flat.name)
		}
	}
	if len(f.Tags) > 0 {
		add(entityKind, "attributes.tags", "tags")
	}
	keys := make([]string, 0, len(f.Attributes))
	for k := range f.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(entityKind, "attributes."+k, "attributes."+k)
	}
	if f.Where != nil {
		// The fields of the filter of a related node are fields of the
		// related CIs (WP-789).
		f.Where.walkPaths("", func(e *Expr, path string) {
			if e.Field != "" {
				related = inRelated(path)
				field(e.Field)
				related = false
			}
		})
	}
	for _, key := range f.Sort {
		field(key.Field)
	}
	for _, col := range f.Columns {
		field(col)
	}
	return out
}

// Filter AST (SRC-03, MET-45): Where combines comparisons with and, or and
// not. A node is either a group (exactly one of And, Or, Not), a
// relationship node (Related, SRC-04, WP-789) or a comparison of a
// whitelisted field with an operator and a value. Fields are the columns of
// the entity, its dynamic attributes (attributes.<key>), its tags (tags
// with has_any and has_all) and, for CIs, the fields of the linked asset
// (asset.<field>) and its custom fields (asset.attributes.<key>). Only
// values reach the SQL as parameters; columns come from the whitelist,
// attribute keys pass validAttributeKey. api/rules.schema.json describes
// the AST as $defs/filter; api/rules.conformance.json holds the cases
// (filter_cases, invalid_filters) that Validate and parseFilter of
// frontend/src/rules/filter.ts both pass.

// Expr is one node of the filter AST.
type Expr struct {
	And     []Expr       `json:"and,omitempty"`
	Or      []Expr       `json:"or,omitempty"`
	Not     *Expr        `json:"not,omitempty"`
	Related *RelatedExpr `json:"related,omitempty"`
	Field   string       `json:"field,omitempty"`
	Op      string       `json:"op,omitempty"`
	Value   any          `json:"value,omitempty"`
}

// RelatedExpr is the relationship node of the filter AST (SRC-04, WP-789),
// for entity kind ci only: a CI matches when it reaches, over at most
// MaxDepth relationships of RelTypes (all types when empty) in Direction,
// a CI that matches Where. With out the CI is the source of the
// relationship (a VM runs_on a host), with in its target, with both
// either.
type RelatedExpr struct {
	Direction string   `json:"direction"`
	RelTypes  []string `json:"rel_types,omitempty"`
	// MaxDepth counts relationships, 1 to MaxRelationshipDepth; nil is 1.
	MaxDepth *int  `json:"max_depth,omitempty"`
	Where    *Expr `json:"where,omitempty"`
}

// Directions of a related node.
const (
	DirectionOut  = "out"
	DirectionIn   = "in"
	DirectionBoth = "both"
)

// depth returns MaxDepth, 1 when it is not set.
func (r *RelatedExpr) depth() int {
	if r.MaxDepth == nil {
		return 1
	}
	return *r.MaxDepth
}

// FilterOps are the operators of a comparison, the enum of op in
// $defs/filter_compare of api/rules.schema.json.
var FilterOps = []string{OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpLike, OpIn, OpExists, OpBetween, OpHasAny, OpHasAll}

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
	// OpBetween compares a number or a time inclusively with a list of
	// two values (WP-309).
	OpBetween = "between"
	// OpHasAny and OpHasAll compare the tags with a list of tag keys:
	// one of them or all of them (WP-309).
	OpHasAny = "has_any"
	OpHasAll = "has_all"
)

// TagsField is the field of the tag comparisons (has_any, has_all).
const TagsField = "tags"

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
	// kindTag is the field tags: has_any and has_all only.
	kindTag = "tag"
)

type astField struct{ expr, kind string }

// astFields are the whitelisted fields per entity kind; %s is the table.
// The asset fields asset_tag, purchase_cost and warranty_end are the
// deprecated aliases of inventory_no, purchase_price and warranty_until
// (AST-01, WP-738) and read the new columns. The owner of a CI
// (owner_user_id, owner_team_id; WP-752) is the basis of "my devices"
// (WP-307); has_owner of the spec is the holder of an asset (holder cache).
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
		"owner_user_id": {"%s.owner_user_id", kindUUID}, "owner_team_id": {"%s.owner_team_id", kindUUID},
		"is_manual": {"%s.is_manual", kindBool}, "created_at": {"%s.created_at", kindTime},
		"updated_at": {"%s.updated_at", kindTime}, "last_seen_at": {"%s.last_seen_at", kindTime},
	},
	"asset": {
		"name": {"%s.name", kindText}, "inventory_no": {"%s.inventory_no", kindText},
		"asset_tag": {"%s.inventory_no", kindText}, "category": {"%s.category", kindText},
		"status": {"%s.status", kindText}, "serial_number": {"%s.serial_number", kindText},
		"supplier": {"%s.supplier", kindText}, "lifecycle_state": {"%s.lifecycle_state", kindText},
		"barcode": {"%s.barcode", kindText}, "rfid_tag": {"%s.rfid_tag", kindText},
		"purchase_price": {"%s.purchase_price", kindNumber}, "purchase_cost": {"%s.purchase_price", kindNumber},
		"purchase_date": {"%s.purchase_date", kindTime}, "warranty_until": {"%s.warranty_until", kindTime},
		"warranty_end": {"%s.warranty_until", kindTime}, "client_id": {"%s.client_id", kindUUID},
		"location_id": {"%s.location_id", kindUUID}, "created_at": {"%s.created_at", kindTime},
		// AST-01 (WP-271, WP-309).
		"manufacturer": {"%s.manufacturer", kindText}, "model": {"%s.model", kindText},
		"cost_center": {"%s.cost_center", kindText}, "currency": {"%s.currency", kindText},
		"supplier_id": {"%s.supplier_id", kindUUID}, "contract_id": {"%s.contract_id", kindUUID},
		"owner_user_id": {"%s.owner_user_id", kindUUID}, "owner_team_id": {"%s.owner_team_id", kindUUID},
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

// walk calls visit for the node and every node below it.
func (e *Expr) walk(visit func(*Expr)) {
	e.walkPaths("", func(n *Expr, _ string) { visit(n) })
}

// walkPaths calls visit for the node and every node below it with the path
// of the node (as ASTError names it).
func (e *Expr) walkPaths(path string, visit func(n *Expr, path string)) {
	visit(e, path)
	for i := range e.And {
		e.And[i].walkPaths(fmt.Sprintf("%s.and[%d]", path, i), visit)
	}
	for i := range e.Or {
		e.Or[i].walkPaths(fmt.Sprintf("%s.or[%d]", path, i), visit)
	}
	if e.Not != nil {
		e.Not.walkPaths(path+".not", visit)
	}
	if e.Related != nil && e.Related.Where != nil {
		e.Related.Where.walkPaths(path+".related.where", visit)
	}
}

// inRelated reports whether the path of a node (walkPaths) lies in the
// filter of a related node, i.e. the node compares a related CI.
func inRelated(path string) bool {
	return strings.Contains(path, ".related.where")
}

// catalog is what PGQueryEngine resolves for a spec in the transaction of
// the query (API-02, WP-309): the definition of each dynamic attribute the
// spec compares, by the entity whose JSON holds it ("ci": ci.attributes,
// "asset": asset.custom_fields, also of the linked asset of a CI). A nil
// catalog is the static compilation: attributes compare by the type of
// their value. relAttrs are the attributes compared in the filter of a
// related node (WP-789): they belong to the related CIs, whose types are
// not those of ci_type of the spec, so they resolve for every type.
type catalog struct {
	attrs    map[attrRef]resolvedAttribute
	relAttrs map[attrRef]resolvedAttribute
}

// related returns the catalog of the filter of a related node.
func (c *catalog) related() *catalog {
	if c == nil {
		return nil
	}
	return &catalog{attrs: c.relAttrs, relAttrs: c.relAttrs}
}

// attrRef names a dynamic attribute: the entity whose JSON holds it and
// its key.
type attrRef struct{ entity, key string }

// resolvedAttribute is the searchable definition of an attribute: the
// filter kind of its data type (fieldmeta.FilterKind) and the types the
// filter is restricted to (nil: every type), as attributeFilterOf of the CI
// list returns them (WP-566).
type resolvedAttribute struct {
	kind  string
	types []string
}

// attribute returns the definition of the attribute, ok false without a
// catalog or for an attribute it does not hold.
func (c *catalog) attribute(entity, key string) (resolvedAttribute, bool) {
	if c == nil {
		return resolvedAttribute{}, false
	}
	a, ok := c.attrs[attrRef{entity, key}]
	return a, ok
}

// attributeRef returns the attribute a field names for the entity kind:
// attributes.<key> of the entity, asset.attributes.<key> of the linked
// asset of a CI; ok is false for any other field.
func attributeRef(entityKind, field string) (ref attrRef, ok bool) {
	if key, isAttr := strings.CutPrefix(field, "attributes."); isAttr {
		return attrRef{entityKind, key}, true
	}
	if key, isAttr := strings.CutPrefix(field, "asset.attributes."); isAttr && entityKind == "ci" {
		return attrRef{"asset", key}, true
	}
	return attrRef{}, false
}

// Validate checks the AST against the field whitelist of the entity kind,
// the operators of each field kind and the size limits.
func (e *Expr) Validate(entityKind string) error {
	nodes := 0
	return e.validate(entityKind, "", 1, &nodes, nil)
}

func (e *Expr) validate(entityKind, path string, depth int, nodes *int, cat *catalog) error {
	*nodes++
	if depth > MaxExprDepth {
		return &ASTError{path, fmt.Sprintf("nested deeper than %d levels", MaxExprDepth)}
	}
	if *nodes > MaxExprNodes {
		return &ASTError{path, fmt.Sprintf("more than %d nodes", MaxExprNodes)}
	}
	parts := 0
	for _, set := range []bool{len(e.And) > 0, len(e.Or) > 0, e.Not != nil, e.Related != nil, e.Field != ""} {
		if set {
			parts++
		}
	}
	if parts != 1 {
		return &ASTError{path, "a node is exactly one of and, or, not, related or a comparison"}
	}
	for i := range e.And {
		if err := e.And[i].validate(entityKind, fmt.Sprintf("%s.and[%d]", path, i), depth+1, nodes, cat); err != nil {
			return err
		}
	}
	for i := range e.Or {
		if err := e.Or[i].validate(entityKind, fmt.Sprintf("%s.or[%d]", path, i), depth+1, nodes, cat); err != nil {
			return err
		}
	}
	if e.Not != nil {
		return e.Not.validate(entityKind, path+".not", depth+1, nodes, cat)
	}
	if e.Related != nil {
		return e.Related.validate(entityKind, path+".related", depth, nodes, cat)
	}
	if e.Field == "" {
		return nil
	}
	kind, typed, err := e.fieldKind(entityKind, cat)
	if err != nil {
		return &ASTError{path, err.Error()}
	}
	return e.validateComparison(kind, typed, path)
}

// validate checks a related node at path (where.<…>.related) of depth: it
// applies to entity kind ci only, direction is out, in or both, max_depth
// lies in 1..MaxRelationshipDepth, each of rel_types is a key, and where is
// a valid filter on CIs one level deeper; its nodes count towards
// MaxExprDepth and MaxExprNodes. Whether the rel_types exist is checked
// against relationship_type in the transaction (resolveCatalog).
func (r *RelatedExpr) validate(entityKind, path string, depth int, nodes *int, cat *catalog) error {
	if entityKind != "ci" {
		return &ASTError{path, "related applies to entity_kind ci only"}
	}
	switch r.Direction {
	case DirectionOut, DirectionIn, DirectionBoth:
	default:
		return &ASTError{path + ".direction", "must be out, in or both"}
	}
	if d := r.depth(); d < 1 || d > MaxRelationshipDepth {
		return &ASTError{path + ".max_depth", fmt.Sprintf("must be between 1 and %d", MaxRelationshipDepth)}
	}
	for i, key := range r.RelTypes {
		if key == "" || strings.ContainsRune(key, 0) {
			return &ASTError{fmt.Sprintf("%s.rel_types[%d]", path, i), "must be a relationship type key"}
		}
	}
	if r.Where == nil {
		return &ASTError{path + ".where", "related needs a filter (where)"}
	}
	return r.Where.validate("ci", path+".where", depth+1, nodes, cat.related())
}

// fieldKind resolves the comparison's field. An attribute the catalog holds
// takes the kind of its definition (typed, WP-309). Without one, an
// attribute compares by the value's type (for in and between by the first
// element's): a number as number, a boolean as bool, a string as text, or
// as time when it is a time compared with lt, lte, gt, gte or between.
func (e *Expr) fieldKind(entityKind string, cat *catalog) (kind string, typed bool, err error) {
	if ref, ok := attributeRef(entityKind, e.Field); ok {
		if !validAttributeKey(ref.key) {
			return "", false, fmt.Errorf("invalid attribute key %q", ref.key)
		}
		if a, resolved := cat.attribute(ref.entity, ref.key); resolved {
			return a.kind, true, nil
		}
		v := e.Value
		if list, isList := v.([]any); isList && len(list) > 0 {
			v = list[0]
		}
		switch v := v.(type) {
		case float64:
			return kindNumber, false, nil
		case bool:
			return kindBool, false, nil
		case string:
			if (ordering(e.Op) || e.Op == OpBetween) && validTime(v) {
				return kindTime, false, nil
			}
		}
		return kindText, false, nil
	}
	if e.Field == TagsField {
		return kindTag, false, nil
	}
	if sub, ok := strings.CutPrefix(e.Field, "asset."); ok && entityKind == "ci" {
		f, known := astFields["asset"][sub]
		if !known {
			return "", false, fmt.Errorf("unknown asset field %q", sub)
		}
		return f.kind, false, nil
	}
	f, known := astFields[entityKind][e.Field]
	if !known {
		return "", false, fmt.Errorf("unknown field %q", e.Field)
	}
	return f.kind, false, nil
}

// validateComparison checks the operator for the field kind and every value
// against it (each element of in and between), so that no value reaches a
// cast in SQL that could fail (F-179). typed is an attribute of a resolved
// definition, whose number values may also be given as strings.
func (e *Expr) validateComparison(kind string, typed bool, path string) error {
	if kind == kindTag && e.Op != OpHasAny && e.Op != OpHasAll {
		return &ASTError{path, "tags take has_any or has_all"}
	}
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
		return validateList(kind, typed, path, e.Value, "in needs a non-empty list", func(n int) bool { return n > 0 })
	case OpBetween:
		if kind != kindNumber && kind != kindTime {
			return &ASTError{path, "between applies to number and time fields only"}
		}
		return validateList(kind, typed, path, e.Value, "between needs a list of exactly two values", func(n int) bool { return n == 2 })
	case OpHasAny, OpHasAll:
		if kind != kindTag {
			return &ASTError{path, e.Op + " applies to tags only"}
		}
		return validateList(kind, false, path, e.Value, e.Op+" needs a non-empty list of tag keys", func(n int) bool { return n > 0 })
	default:
		return &ASTError{path, fmt.Sprintf("unknown operator %q", e.Op)}
	}
	if msg := valueError(kind, e.Value, typed); msg != "" {
		return &ASTError{path + ".value", msg}
	}
	return nil
}

// validateList checks that value is a list whose length size accepts
// (otherwise msg) and each element a value of the kind.
func validateList(kind string, typed bool, path string, value any, msg string, size func(int) bool) error {
	values, ok := value.([]any)
	if !ok || !size(len(values)) {
		return &ASTError{path + ".value", msg}
	}
	for i, v := range values {
		if m := valueError(kind, v, typed); m != "" {
			return &ASTError{fmt.Sprintf("%s.value[%d]", path, i), m}
		}
	}
	return nil
}

// numberShape is a number as numeric parses it, for the number values of a
// typed attribute given as a string.
var numberShape = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// valueError reports why v is no value of the field kind, or "" when it is
// one. Untyped attributes take their kind from the value, so for them only
// the elements of in after the first can disagree; a typed attribute
// (typed) also takes a number as a string ("16").
func valueError(kind string, v any, typed bool) string {
	switch v.(type) {
	case string, float64, bool:
	default:
		return "must be a string, number or boolean"
	}
	switch kind {
	case kindNumber:
		if s, isString := v.(string); typed && isString && numberShape.MatchString(s) {
			return ""
		}
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
	case kind == kindTag && s == "":
		return "must be a tag key"
	case kind == kindUUID && !api.IsUUID(s):
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
// appended and numbered from startAt. It is the static compilation (see
// FilterSpec.CompileSQL).
func (e *Expr) CompileSQL(entityKind, table string, args []any, startAt int) (sql string, out []any, next int, err error) {
	return e.compile(entityKind, table, nil, args, startAt)
}

// compile is CompileSQL with the catalog cat (nil: static).
func (e *Expr) compile(entityKind, table string, cat *catalog, args []any, startAt int) (sql string, out []any, next int, err error) {
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
		case n.Related != nil:
			// The filter of the related CIs reads them under their own
			// alias, numbered by nesting (relatedLevel), with its own
			// catalog (WP-789).
			level := relatedLevel(table)
			sub, subArgs, subNext, cerr := n.Related.Where.compile("ci", fmt.Sprintf("rn%d", level), cat.related(), args, next)
			if cerr != nil {
				return "", cerr
			}
			args, next = subArgs, subNext
			return relatedSQL(table, level, n.Related, sub, param), nil
		}
		kind, _, ferr := n.fieldKind(entityKind, cat)
		if ferr != nil {
			return "", ferr
		}
		if kind == kindTag {
			return tagComparison(table, n, param), nil
		}
		col, jsonValue, wrap := n.column(entityKind, table, cat, param)
		exact := comparison(col, jsonValue, kind, n, param)
		if index := n.numberColumnCondition(entityKind, table, kind, cat, param); index != "" {
			// The index condition holds for every hit of the exact
			// comparison, so the conjunction equals it (also as NULL).
			exact = "(" + index + " AND " + exact + ")"
		}
		return wrap(exact), nil
	}
	sql, err = compile(e)
	return sql, args, next, err
}

// column returns the SQL expression of the field, for a dynamic attribute
// also its JSON value (jsonValue), and the wrapper of the comparison: for
// fields of the linked asset of a CI the evaluation on that asset, for an
// attribute whose definitions are restricted to types the restriction to
// them (WP-566).
//
// With the catalog a CI attribute is read with the expression of the
// attribute indexes, reticora_attr_text with the key as a literal
// (citype.TableKeyExpression; E-73 (Korrekturplan), WP-435, WP-607), as the
// attribute filter of the CI list compares it; the key passed
// validAttributeKey.
func (e *Expr) column(entityKind, table string, cat *catalog, param func(any) string) (col, jsonValue string, wrap func(string) string) {
	wrap = func(s string) string { return s }
	field := e.Field
	if sub, ok := strings.CutPrefix(field, "asset."); ok && entityKind == "ci" {
		outer := table
		wrap = func(s string) string {
			return fmt.Sprintf("EXISTS (SELECT 1 FROM asset sa WHERE sa.id = %s.asset_id AND %s)", outer, s)
		}
		if !strings.HasPrefix(sub, "attributes.") {
			return fmt.Sprintf(astFields["asset"][sub].expr, "sa"), "", wrap
		}
		// asset.attributes.<key>: the custom fields of the linked asset.
		field, table = sub, "sa"
	}
	ref, isAttr := attributeRef(entityKind, e.Field)
	if !isAttr {
		return fmt.Sprintf(astFields[entityKind][field].expr, table), "", wrap
	}
	jsonb, typeCol := table+".attributes", table+".ci_type_id"
	if ref.entity == "asset" {
		jsonb, typeCol = table+".custom_fields", table+".asset_type_id"
	}
	a, typed := cat.attribute(ref.entity, ref.key)
	if typed && a.types != nil {
		inner := wrap
		restrict := param(a.types)
		wrap = func(s string) string {
			return inner(fmt.Sprintf("(%s = ANY(%s::uuid[]) AND %s)", typeCol, restrict, s))
		}
	}
	if typed && ref.entity == "ci" {
		// The key is a literal here as in the index expression; it passed
		// validAttributeKey ([a-z0-9_]), so it needs no quoting. No
		// parameter is added that the SQL might not use. The attributes
		// are those of table: in a related node the CI of its level
		// (rn<level>), not the bare column, which only the scoping of the
		// subquery resolved to it.
		return citype.TableKeyExpression(table, ref.key), fmt.Sprintf("%s->'%s'", jsonb, ref.key), wrap
	}
	p := param(ref.key)
	return fmt.Sprintf("%s->>%s", jsonb, p), fmt.Sprintf("%s->%s", jsonb, p), wrap
}

// numberColumns are the CI attributes whose number value is also a stored
// column of ci with an index (finding of WP-824, migration 000274):
// attribute key → column. numeric comparisons are not leakproof, so under
// row level security no index serves the comparison of the attribute
// itself (E-133); the column is double precision, compared with the
// leakproof float8 operators.
var numberColumns = map[string]string{"warranty_days_left": "warranty_days_left_num"}

// float8Of maps a numeric expression to double precision exactly as the
// generated columns of numberColumns map the attribute (000274): clamped,
// so that it never raises, and monotone.
func float8Of(x string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s >= 1e308 THEN 'Infinity'::float8 WHEN %[1]s <= -1e308 THEN '-Infinity'::float8 `+
		`WHEN abs(%[1]s) < 1e-300 THEN 0::float8 ELSE %[1]s::float8 END`, x)
}

// numberColumnCondition returns the index condition on the column of
// numberColumns for a comparison of a CI attribute resolved as a number
// (catalog), "" for any other comparison. The mapping to double precision
// is monotone (float8Of), so the condition holds for every value the exact
// numeric comparison matches: eq and in compare the mapped values for
// equality, lt and lte (gt and gte) with lte (gte), between inclusive. ne
// (IS DISTINCT FROM, also without a value), exists and like get none.
func (e *Expr) numberColumnCondition(entityKind, table, kind string, cat *catalog, param func(any) string) string {
	if kind != kindNumber {
		return ""
	}
	ref, isAttr := attributeRef(entityKind, e.Field)
	if !isAttr || ref.entity != "ci" {
		return ""
	}
	column, indexed := numberColumns[ref.key]
	if _, typed := cat.attribute(ref.entity, ref.key); !typed || !indexed {
		return ""
	}
	col := table + "." + column
	value := func(v any) string { return float8Of(param(fmt.Sprint(v)) + "::numeric") }
	switch e.Op {
	case OpEq:
		return col + " = " + value(e.Value)
	case OpLt, OpLte:
		return col + " <= " + value(e.Value)
	case OpGt, OpGte:
		return col + " >= " + value(e.Value)
	case OpBetween:
		values := e.Value.([]any)
		low := value(values[0])
		return col + " BETWEEN " + low + " AND " + value(values[1])
	case OpIn:
		values := e.Value.([]any)
		texts := make([]string, len(values))
		for i, v := range values {
			texts[i] = fmt.Sprint(v)
		}
		return col + " = ANY(ARRAY(SELECT " + float8Of("u.v") + " FROM unnest(" + param(texts) + "::numeric[]) AS u(v)))"
	}
	return ""
}

// tagComparison renders a tag comparison (WP-309) with the building block
// of the flat tags (tagExists): has_any is one EXISTS over the keys,
// has_all one EXISTS per key, as filter[tags] with in and all of the CI
// list (WP-754).
func tagComparison(table string, e *Expr, param func(any) string) string {
	values := e.Value.([]any)
	keys := make([]string, len(values))
	for i, v := range values {
		keys[i] = fmt.Sprint(v)
	}
	if e.Op == OpHasAny {
		return tagExists(table, "t.key = ANY("+param(keys)+"::text[])")
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = tagExists(table, "t.key = "+param(k))
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// tagExists is the predicate that the CI or asset (table) carries a tag of
// the tag catalog whose key matches cond on t.key: ci_tag (WP-281) or
// asset_tag_link (WP-755).
func tagExists(table, cond string) string {
	if table == "asset" {
		return `EXISTS (SELECT 1 FROM asset_tag_link atl JOIN tag t ON t.id = atl.tag_id WHERE atl.asset_id = asset.id AND ` + cond + `)`
	}
	// table is ci or the alias of the CIs of a related node (WP-789).
	return `EXISTS (SELECT 1 FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ` + table + `.id AND ` + cond + `)`
}

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// attributeGuards are the type checks under which comparison casts a
// dynamic attribute (%[1]s its JSON value, %[2]s its text). None of them
// can fail, so the result does not depend on the order in which
// PostgreSQL evaluates the predicates (F-165). An attribute typed uuid by
// its definition is cast only when its text is a uuid.
var attributeGuards = map[string]string{
	kindNumber: `jsonb_typeof(%[1]s) = 'number'`,
	kindBool:   `jsonb_typeof(%[1]s) = 'boolean'`,
	kindTime:   `jsonb_typeof(%[1]s) = 'string' AND %[2]s ~ '^\d{4}-\d{2}-\d{2}' AND pg_input_is_valid(%[2]s, 'timestamptz')`,
	kindUUID:   `jsonb_typeof(%[1]s) = 'string' AND pg_input_is_valid(%[2]s, 'uuid')`,
}

// comparison renders one comparison with the casts of the field kind. A
// dynamic attribute is cast only under its type check: a value of another
// type compares as NULL like a missing key, so it matches neither eq, in,
// between nor an order comparison, and like a missing key it matches ne (IS
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
	case OpBetween:
		// Inclusive, with the cast of the field kind (number or time).
		values := e.Value.([]any)
		low := value(values[0])
		return lhs + " BETWEEN " + low + " AND " + value(values[1])
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
