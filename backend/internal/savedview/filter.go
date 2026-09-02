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
		// Recursive subtree over location_node.
		p := next()
		add(fmt.Sprintf(`%s.location_id IN (
			WITH RECURSIVE subtree AS (
				SELECT id FROM location_node WHERE id = %s::uuid
				UNION ALL
				SELECT n.id FROM location_node n JOIN subtree s ON n.parent_id = s.id
			) SELECT id FROM subtree)`, table, p), f.LocationSubtree)
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
		// CIs upstream of X: sources of edges pointing (transitively) into X.
		p := next()
		add(fmt.Sprintf(`ci.id IN (
			WITH RECURSIVE up AS (
				SELECT source_ci_id AS id FROM ci_relationship WHERE target_ci_id = %s::uuid
				UNION
				SELECT r.source_ci_id FROM ci_relationship r JOIN up ON r.target_ci_id = up.id
			) SELECT id FROM up)`, p), f.UpstreamOf)
	}
	if f.DownstreamOf != "" && table == "ci" {
		p := next()
		add(fmt.Sprintf(`ci.id IN (
			WITH RECURSIVE down AS (
				SELECT target_ci_id AS id FROM ci_relationship WHERE source_ci_id = %s::uuid
				UNION
				SELECT r.target_ci_id FROM ci_relationship r JOIN down ON r.source_ci_id = down.id
			) SELECT id FROM down)`, p), f.DownstreamOf)
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

	if len(parts) == 0 {
		return "true", args, nil
	}
	return strings.Join(parts, " AND "), args, nil
}

// QueryResult is one row of a filter execution.
type QueryResult struct {
	ID         string         `json:"id"`
	EntityKind string         `json:"entity_kind"`
	Name       string         `json:"name"`
	Summary    string         `json:"summary,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
