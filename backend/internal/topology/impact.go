package topology

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// DependencyResult is the directed upstream/downstream view of a CI
// (spec §15). Upstream = what this CI depends on, downstream = what depends
// on it, both read from the impact_direction of each relationship type
// (IMP-01, F-099): vm hosted_on server puts the server upstream of the vm
// and the vm downstream of the server.
type DependencyResult struct {
	CIID      string `json:"ci_id"`
	Direction string `json:"direction"`
	Depth     int    `json:"depth"`
	Nodes     []Node `json:"nodes"`
	Count     int    `json:"count"`
}

// BlastRadius summarizes the blast radius of a CI failure (spec §15):
// affected CIs plus the resolved assets, business services, clients and sites
// reachable through the impact set. As in /impact a CI reached only over an
// undirected network edge (connected_to) is a heuristic hit (IMP-03,
// F-098): listed with its mark, counted in HeuristicCount, not in
// AffectedCount, and its client and site are not affected.
type BlastRadius struct {
	CIID             string          `json:"ci_id"`
	AffectedCIs      []BlastRadiusCI `json:"affected_cis"`
	AffectedCount    int             `json:"affected_count"`
	HeuristicCount   int             `json:"heuristic_count"`
	AffectedServices []Node          `json:"affected_services,omitempty"`
	ClientIDs        []string        `json:"client_ids,omitempty"`
	SiteIDs          []string        `json:"site_ids,omitempty"`
	// SinglePointsOfFailure lists the affected CIs that depend on the failed
	// CI directly without an alternative provider (IMP-04).
	SinglePointsOfFailure []Node `json:"single_points_of_failure,omitempty"`
	// ProtectedByRedundancy lists the CIs that depend on the failed CI over
	// a redundant type but keep a working provider (IMP-04).
	ProtectedByRedundancy []Node `json:"protected_by_redundancy,omitempty"`
}

// BlastRadiusCI is a CI of the blast radius; Heuristic marks a CI reached
// only over an undirected network edge, a second-class path (IMP-03).
type BlastRadiusCI struct {
	Node
	Heuristic bool `json:"heuristic"`
}

// GetDependencies handles GET /api/v1/cis/{id}/dependencies?direction=up|down
func (h *Handler) GetDependencies(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id := chi.URLParam(r, "id")
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "downstream"
	}
	if direction != "upstream" && direction != "downstream" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "direction must be upstream or downstream")
		return
	}
	depth, err := parseDepth(r.URL.Query().Get("depth"), maxDepth)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	root, err := h.ciRepo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}
	g, err := h.graphFrom(r.Context(), t.OrganizationID, root, depth)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	reached := dependenciesOf(g, g.stepRules(h.impactRules(r.Context(), t.OrganizationID), impactQuery{}),
		direction == "upstream", depth)
	nodes := make([]Node, 0)
	for n := range g.ids {
		if reached[n] {
			nodes = append(nodes, g.nodes[n])
		}
	}
	api.WriteJSON(w, http.StatusOK, DependencyResult{
		CIID: id, Direction: direction, Depth: depth, Nodes: nodes, Count: len(nodes),
	})
}

// GetBlastRadius handles GET /api/v1/cis/{id}/blast-radius
func (h *Handler) GetBlastRadius(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id := chi.URLParam(r, "id")
	root, err := h.ciRepo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}
	g, err := h.graphFrom(r.Context(), t.OrganizationID, root, maxDepth)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	rules := h.impactRules(r.Context(), t.OrganizationID)
	impacted := impactOf(g, []int32{0}, impactQuery{depth: maxDepth}, rules)
	result := BlastRadius{CIID: id, AffectedCIs: []BlastRadiusCI{}}
	services := map[string]struct{}{}
	clients := map[string]struct{}{}
	sites := map[string]struct{}{}
	// Certain hits first, then by name, as in /impact; a heuristic hit is
	// listed with its mark but neither counted as affected nor does it
	// affect its client or site (IMP-03).
	for _, n := range impactOrder(g, impacted) {
		node := g.nodes[n]
		heuristic := impacted[n].heuristic
		result.AffectedCIs = append(result.AffectedCIs, BlastRadiusCI{Node: node, Heuristic: heuristic})
		if heuristic {
			result.HeuristicCount++
			continue
		}
		result.AffectedCount++
		if node.ClientID != "" {
			clients[node.ClientID] = struct{}{}
		}
		if node.SiteID != "" {
			sites[node.SiteID] = struct{}{}
		}
	}
	for id := range clients {
		result.ClientIDs = append(result.ClientIDs, id)
	}
	for id := range sites {
		result.SiteIDs = append(result.SiteIDs, id)
	}
	// Business services in the affected set (logical type heuristic: the CI
	// type key resolves through ci.CITypeID which carries the type id; the
	// caller can filter by type on the client side).
	for i := range result.AffectedCIs {
		if _, ok := services[result.AffectedCIs[i].ID]; ok {
			result.AffectedServices = append(result.AffectedServices, result.AffectedCIs[i].Node)
		}
	}
	result.SinglePointsOfFailure, result.ProtectedByRedundancy = redundancyOf(g, 0, impacted, rules)
	api.WriteJSON(w, http.StatusOK, result)
}

// dependenciesOf walks from the root of g (number 0) along the impact
// directions of steps (IMP-01, F-099), at most depth hops: downstream the
// way a failure travels (the CIs that depend on the root), upstream against
// it (the CIs the root depends on). Only directed types are dependencies:
// an undirected one (connected_to, a heuristic, IMP-03) and one without
// impact direction are not followed. g holds only visible, not deleted CIs
// (IMP-07). It returns, by CI number, the CIs reached, the root excluded.
func dependenciesOf(g *ciGraph, steps []stepRule, upstream bool, depth int) []bool {
	g.indexIncidence()
	reached := make([]bool, len(g.ids))
	hops := make([]int, len(g.ids))
	reached[0] = true
	queue := []int32{0}
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		if hops[current] >= depth {
			continue
		}
		for _, i := range g.incidentTo(current) {
			e := &g.edges[i]
			// The dependent and the provider of the edge by its impact
			// direction.
			var dependent, provider int32
			switch steps[e.relType].way {
			case stepToSource:
				dependent, provider = e.source, e.target
			case stepToTarget:
				dependent, provider = e.target, e.source
			case stepNone, stepBoth:
				continue
			}
			from, to := provider, dependent
			if upstream {
				from, to = dependent, provider
			}
			if from != current || reached[to] || !g.known[to] {
				continue
			}
			reached[to] = true
			hops[to] = hops[current] + 1
			queue = append(queue, to)
		}
	}
	reached[0] = false
	return reached
}

// ImpactNode is an impacted CI with its shortest path from the failed CI
// (IMP-02); Heuristic marks a CI reached only over an undirected network
// edge (connected_to), a second-class path (IMP-03). Via is the last step of
// the path, so following Via from CI to CI rebuilds the path without
// transferring it (NFR-02); Relay is set on a certain CI that the shortest
// path over all edges reaches over another step: a heuristic CI's path
// continues over the Relay of the certain CIs on it (ciGraph.path).
type ImpactNode struct {
	Node
	Heuristic bool       `json:"heuristic"`
	Hops      int        `json:"hops"`
	Via       Via        `json:"via"`
	Relay     *Via       `json:"relay,omitempty"`
	Path      []PathStep `json:"path"`
}

// Via is a step of an impact path by its origin: the CI the failure comes
// from and the relationship type it passes. Category and heuristic flag of
// the step follow from the type's impact rule.
type Via struct {
	CIID    string `json:"ci_id"`
	RelType string `json:"rel_type"`
}

// WithRelationshipTypes makes the impact follow the impact_direction of the
// organization's relationship types, custom types included (IMP-01).
func (h *Handler) WithRelationshipTypes(types relationshiptype.Repository) *Handler {
	h.types = types
	return h
}

// maxRelationshipTypes bounds the types read for one impact analysis.
const maxRelationshipTypes = 500

// impactRule is the impact semantics of a relationship type.
type impactRule struct {
	direction string
	category  string
	// redundant: a dependent fails only when all its providers of the type
	// failed (IMP-04).
	redundant bool
}

// impactRules maps each relationship type key onto its impact direction
// and category: the built-in catalogue (CH27, E-18), overlaid by the
// organization's types.
func (h *Handler) impactRules(ctx context.Context, orgID string) map[string]impactRule {
	rules := map[string]impactRule{}
	catalogue := relationshiptype.Catalogue()
	for i := range catalogue {
		rules[catalogue[i].Key] = impactRule{catalogue[i].ImpactDirection, catalogue[i].Category, catalogue[i].Redundant}
	}
	if h.types == nil {
		return rules
	}
	types, _, err := h.types.List(ctx, orgID, api.PaginationParams{Limit: maxRelationshipTypes})
	if err != nil {
		slog.WarnContext(ctx, "relationship types unavailable; impact uses the built-in catalogue", "error", err)
		return rules
	}
	for i := range types {
		rules[types[i].Key] = impactRule{types[i].ImpactDirection, types[i].Category, types[i].Redundant}
	}
	return rules
}

// ruleOf returns the rule of a type; a type without a rule counts as a
// logical source_depends_on_target, the reading of a directed dependency.
func ruleOf(rules map[string]impactRule, relType string) impactRule {
	if rule, ok := rules[relType]; ok {
		return rule
	}
	return impactRule{direction: relationshiptype.ImpactSourceDependsOnTarget, category: "logical"}
}

// stepRule is the impact rule of a relationship type of a graph with the
// facet of an analysis: whether its steps are followed.
type stepRule struct {
	impactRule
	way    stepWay
	follow bool
}

// stepWay is the impact direction of a type as a number.
type stepWay uint8

const (
	stepNone stepWay = iota
	// stepToSource: the source depends on the target.
	stepToSource
	// stepToTarget: the target depends on the source.
	stepToTarget
	// stepBoth: undirected, a heuristic step either way (IMP-03).
	stepBoth
)

// stepRules resolves the rule of each relationship type of g once for an
// analysis narrowed by q.
func (g *ciGraph) stepRules(rules map[string]impactRule, q impactQuery) []stepRule {
	steps := make([]stepRule, len(g.relTypes))
	for t, relType := range g.relTypes {
		rule := ruleOf(rules, relType)
		way := stepNone
		switch rule.direction {
		case relationshiptype.ImpactSourceDependsOnTarget:
			way = stepToSource
		case relationshiptype.ImpactTargetDependsOnSource:
			way = stepToTarget
		case relationshiptype.ImpactUndirected:
			way = stepBoth
		}
		follow := (q.relType == "" || relType == q.relType) && (len(q.categories) == 0 || q.categories[rule.category])
		steps[t] = stepRule{impactRule: rule, way: way, follow: follow}
	}
	return steps
}

// step returns the CI a failure of from reaches over e, if any. heuristic
// is true for an undirected edge.
func (e *ciEdge) step(from int32, rule *stepRule) (to int32, heuristic, ok bool) {
	switch rule.way {
	case stepToSource:
		if e.target == from {
			return e.source, false, true
		}
	case stepToTarget:
		if e.source == from {
			return e.target, false, true
		}
	case stepBoth:
		switch from {
		case e.source:
			return e.target, true, true
		case e.target:
			return e.source, true, true
		}
	case stepNone:
	}
	return -1, false, false
}

// PathStep is one hop of an impact path: the failure passes from FromCIID
// to ToCIID over a relationship of RelType (IMP-02).
type PathStep struct {
	FromCIID  string `json:"from_ci_id"`
	ToCIID    string `json:"to_ci_id"`
	RelType   string `json:"rel_type"`
	Category  string `json:"category"`
	Heuristic bool   `json:"heuristic,omitempty"`
}

// ciLink is the last step of the path to an impacted CI: the CI it comes
// from and the type it passes, both as numbers of the graph.
type ciLink struct {
	from      int32
	relType   int32
	category  string
	heuristic bool
}

// via is the step as the answer names it.
func (g *ciGraph) via(l *ciLink) Via { return Via{CIID: g.ids[l.from], RelType: g.relTypes[l.relType]} }

// ciHit is how the failure reaches one CI: impacted is set for a CI it
// reaches, with the length of the shortest path and its last step (via),
// and whether only an undirected network edge reaches the CI. A certain CI
// carries the step of its shortest directed path in via; relay holds the
// step of its shortest path over all edges when that differs, the step the
// paths of heuristic CIs behind it take.
type ciHit struct {
	impacted  bool
	heuristic bool
	hops      int
	via       ciLink
	relay     *ciLink
}

// impactQuery narrows an impact analysis: one relationship type, a set of
// categories (empty: all) and the maximum number of hops.
type impactQuery struct {
	relType    string
	categories map[string]bool
	depth      int
}

// impactOf returns, by CI number, how a failure of the failed CIs
// reaches each CI of g along the impact directions (IMP-01), with the
// shortest path to each (IMP-02); a CI reached only over an undirected
// network edge is heuristic (IMP-03) and then carries its shortest path
// over all edges. The redundancy postfilter (IMP-04) keeps a CI that still
// has a working provider of a redundant type. One downward closure serves
// both walks: breadth-first over the directed steps among the certain CIs,
// and over all steps among every CI that goes down; edges in a fixed order,
// so the paths are deterministic. Each walk keeps the last step per CI
// only: path rebuilds the paths. The analysis works on CI numbers (WP-611).
func impactOf(g *ciGraph, failed []int32, q impactQuery, rules map[string]impactRule) []ciHit {
	steps := g.stepRules(rules, q)
	g.indexIncidence()
	level := closeDown(g, failed, steps)
	walk := func(withHeuristic bool) []ciHit {
		hits := make([]ciHit, len(g.ids))
		queue := make([]int32, 0, len(failed))
		for _, n := range failed {
			if !hits[n].impacted {
				hits[n].impacted = true
				queue = append(queue, n)
			}
		}
		for head := 0; head < len(queue); head++ {
			current := queue[head]
			hops := hits[current].hops
			if q.depth > 0 && hops >= q.depth {
				continue
			}
			for _, i := range g.incidentTo(current) {
				e := &g.edges[i]
				rule := &steps[e.relType]
				if !rule.follow {
					continue
				}
				to, heuristic, ok := e.step(current, rule)
				if !ok || level[to] == levelUp || (!withHeuristic && (heuristic || level[to] != levelCertain)) {
					continue
				}
				if hits[to].impacted {
					continue
				}
				hits[to] = ciHit{impacted: true, hops: hops + 1, via: ciLink{from: current, relType: e.relType,
					category: rule.category, heuristic: heuristic}}
				queue = append(queue, to)
			}
		}
		for _, n := range failed {
			hits[n] = ciHit{}
		}
		return hits
	}
	certain := walk(false)
	impacted := walk(true)
	for n := range impacted {
		hit := &impacted[n]
		if !hit.impacted {
			continue
		}
		sure := certain[n]
		if !sure.impacted {
			hit.heuristic = true
			continue
		}
		if hit.via.from != sure.via.from || hit.via.relType != sure.via.relType {
			relay := hit.via
			sure.relay = &relay
		}
		*hit = sure
	}
	return impacted
}

// path rebuilds the shortest path to the impacted CI n from the last steps
// (IMP-02): back along via to a failed CI, on the path of a heuristic CI
// over the relay of the certain CIs it passes.
func (g *ciGraph) path(hits []ciHit, n int32) []PathStep {
	hit := &hits[n]
	path := make([]PathStep, hit.hops)
	to := n
	for i := hit.hops - 1; i >= 0; i-- {
		at := &hits[to]
		link := &at.via
		if hit.heuristic && at.relay != nil {
			link = at.relay
		}
		path[i] = PathStep{FromCIID: g.ids[link.from], ToCIID: g.ids[to], RelType: g.relTypes[link.relType],
			Category: link.category, Heuristic: link.heuristic}
		to = link.from
	}
	return path
}

// parseCategories reads categories=power,network (IMP-02); an unknown
// category is a field error (422).
func parseCategories(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		known := false
		for _, k := range relationshiptype.Categories {
			known = known || k == c
		}
		if !known {
			return nil, &api.FieldError{Field: "categories", Message: "unknown category " + c + "; known: " +
				strings.Join(relationshiptype.Categories, ", "), Status: http.StatusUnprocessableEntity}
		}
		out[c] = true
	}
	return out, nil
}

// MaxImpactCIs bounds the failed CIs of one analysis.
const MaxImpactCIs = 100

// ImpactSummary sums up an impact analysis (IMP-05).
type ImpactSummary struct {
	Failed     int            `json:"failed"`
	Impacted   int            `json:"impacted"`
	Certain    int            `json:"certain"`
	Heuristic  int            `json:"heuristic"`
	ByCategory map[string]int `json:"by_category"`
}

// GetMultiImpact handles GET /api/v1/impact?ci_ids=a,b&categories=…: the
// joint impact of several CIs failing at once (IMP-05).
func (h *Handler) GetMultiImpact(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("ci_ids"), ",") {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		api.WriteFieldError(w, &api.FieldError{Field: "ci_ids", Message: "is required (comma-separated CI ids)"})
		return
	case len(ids) > MaxImpactCIs:
		api.WriteFieldError(w, &api.FieldError{Field: "ci_ids", Message: fmt.Sprintf("at most %d CIs per analysis", MaxImpactCIs),
			Status: http.StatusUnprocessableEntity})
		return
	}
	h.writeImpact(w, r, t.OrganizationID, ids, false, true)
}

// impactFields are the fields of an impacted CI that fields= selects (the
// sparse fieldset of API-02); id is always returned.
var impactFields = []string{"id", "name", "ci_type", "status", "client_id", "site_id", "management_ip",
	"heuristic", "hops", "via", "relay", "path"}

// parseImpactFields reads fields=hops,via (API-02): the fields each impacted
// CI carries besides its id; nil without a field (every field). An unknown
// field is 422.
func parseImpactFields(raw string) (map[string]bool, error) {
	var selected map[string]bool
	for _, field := range strings.Split(raw, ",") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		if !slices.Contains(impactFields, field) {
			return nil, &api.FieldError{Field: "fields", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown field %q; allowed: %s", field, strings.Join(impactFields, ", "))}
		}
		if selected == nil {
			selected = map[string]bool{"id": true}
		}
		selected[field] = true
	}
	return selected, nil
}

// impactEntry is an impacted CI narrowed to the fields of fields=: a field
// that is not selected stays out of the answer.
type impactEntry struct {
	ID           string     `json:"id"`
	Name         *string    `json:"name,omitempty"`
	CIType       *string    `json:"ci_type,omitempty"`
	Status       *string    `json:"status,omitempty"`
	ClientID     string     `json:"client_id,omitempty"`
	SiteID       string     `json:"site_id,omitempty"`
	ManagementIP string     `json:"management_ip,omitempty"`
	Heuristic    *bool      `json:"heuristic,omitempty"`
	Hops         *int       `json:"hops,omitempty"`
	Via          *Via       `json:"via,omitempty"`
	Relay        *Via       `json:"relay,omitempty"`
	Path         []PathStep `json:"path,omitempty"`
}

// selectImpactFields narrows n to the selected fields, each with the value
// and the omission rule of ImpactNode.
func selectImpactFields(n *ImpactNode, fields map[string]bool) impactEntry {
	e := impactEntry{ID: n.ID}
	if fields["name"] {
		e.Name = &n.Name
	}
	if fields["ci_type"] {
		e.CIType = &n.CIType
	}
	if fields["status"] {
		e.Status = &n.Status
	}
	if fields["client_id"] {
		e.ClientID = n.ClientID
	}
	if fields["site_id"] {
		e.SiteID = n.SiteID
	}
	if fields["management_ip"] {
		e.ManagementIP = n.ManagementIP
	}
	if fields["heuristic"] {
		e.Heuristic = &n.Heuristic
	}
	if fields["hops"] {
		e.Hops = &n.Hops
	}
	if fields["via"] {
		e.Via = &n.Via
	}
	if fields["relay"] {
		e.Relay = n.Relay
	}
	if fields["path"] {
		e.Path = n.Path
	}
	return e
}

// mergeGraphs joins the graphs of several failed CIs (IMP-05): each CI and
// each edge once, the edges ordered by id. It returns the numbers of the
// failed CIs, the roots of the graphs.
func mergeGraphs(graphs []*ciGraph) (merged *ciGraph, roots []int32) {
	nodes := map[string]Node{}
	edges := map[string]Edge{}
	for _, g := range graphs {
		for n := range g.ids {
			if g.known[n] {
				nodes[g.ids[n]] = g.nodes[n]
			}
		}
		for i := range g.edges {
			e := &g.edges[i]
			edges[e.id] = Edge{ID: e.id, SourceCIID: g.ids[e.source], TargetCIID: g.ids[e.target],
				RelType: g.relTypes[e.relType], Source: e.origin}
		}
	}
	merged, number := graphOf(graphs[0].ids[0], nodes, slices.Collect(maps.Values(edges)))
	roots = make([]int32, len(graphs))
	for i, g := range graphs {
		roots[i] = number[g.ids[0]]
	}
	return merged, roots
}

// impactOrder returns the numbers of the impacted, known CIs: certain
// impacts first, then by name and id, a stable answer.
func impactOrder(g *ciGraph, impacted []ciHit) []int32 {
	order := make([]int32, 0, len(impacted))
	for n := range impacted {
		if impacted[n].impacted && g.known[n] {
			order = append(order, int32(n))
		}
	}
	slices.SortFunc(order, func(i, j int32) int {
		a, b := &impacted[i], &impacted[j]
		if a.heuristic != b.heuristic {
			if !a.heuristic {
				return -1
			}
			return 1
		}
		if c := strings.Compare(g.nodes[i].Name, g.nodes[j].Name); c != 0 {
			return c
		}
		return strings.Compare(g.nodes[i].ID, g.nodes[j].ID)
	})
	return order
}

// writeImpact analyses the failure of ids and writes the answer: the
// failed CIs, the impacted CIs with their paths and a summary; single adds
// the fields of the single-CI short form; sparse lets fields= narrow the
// impacted CIs (API-02), and a path is only built when it is returned.
// Each failed CI is read once, its graph walked once.
func (h *Handler) writeImpact(w http.ResponseWriter, r *http.Request, orgID string, ids []string, single, sparse bool) {
	ctx := r.Context()
	q := r.URL.Query()
	depth, err := parseDepth(q.Get("depth"), maxDepth)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	categories, err := parseCategories(q.Get("categories"))
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	var fields map[string]bool
	if sparse {
		if fields, err = parseImpactFields(q.Get("fields")); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	withPaths := fields == nil || fields["path"]
	failed := make([]Node, 0, len(ids))
	graphs := make([]*ciGraph, 0, len(ids))
	for _, id := range ids {
		root, gerr := h.ciRepo.GetByID(ctx, orgID, id)
		if gerr != nil {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found: "+id)
			return
		}
		failed = append(failed, nodeFromItem(*root))
		g, berr := h.graphFrom(ctx, orgID, root, depth)
		if berr != nil {
			api.WriteRepoError(w, berr)
			return
		}
		graphs = append(graphs, g)
	}
	// The graph of one failed CI is analysed as it is; several graphs
	// share CIs and edges.
	g, roots := graphs[0], []int32{0}
	if len(graphs) > 1 {
		g, roots = mergeGraphs(graphs)
	}

	relType := q.Get("rel_type")
	impacted := impactOf(g, roots, impactQuery{relType: relType, categories: categories, depth: depth},
		h.impactRules(ctx, orgID))
	order := impactOrder(g, impacted)
	impactedNodes := make([]ImpactNode, 0, len(order))
	summary := ImpactSummary{Failed: len(failed), ByCategory: map[string]int{}}
	for _, n := range order {
		hit := &impacted[n]
		entry := ImpactNode{Node: g.nodes[n], Heuristic: hit.heuristic, Hops: hit.hops, Via: g.via(&hit.via)}
		if hit.relay != nil {
			relay := g.via(hit.relay)
			entry.Relay = &relay
		}
		if withPaths {
			entry.Path = g.path(impacted, n)
		}
		impactedNodes = append(impactedNodes, entry)
		if hit.heuristic {
			summary.Heuristic++
		} else {
			summary.Certain++
		}
		summary.ByCategory[hit.via.category]++
	}
	summary.Impacted = len(impactedNodes)
	body := map[string]any{
		"failed_cis": failed,
		"rel_type":   relType,
		"categories": sortedKeys(categories),
		"depth":      depth,
		"impacted":   impactedNodes,
		"count":      len(impactedNodes),
		"summary":    summary,
	}
	if fields != nil {
		entries := make([]impactEntry, len(impactedNodes))
		for i := range impactedNodes {
			entries[i] = selectImpactFields(&impactedNodes[i], fields)
		}
		body["impacted"] = entries
	}
	if single {
		body["failed_ci_id"] = ids[0]
		body["failed_ci"] = failed[0]
	}
	api.WriteJSON(w, http.StatusOK, body)
}
