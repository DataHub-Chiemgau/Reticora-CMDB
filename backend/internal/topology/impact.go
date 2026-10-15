package topology

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// DependencyResult is the directed upstream/downstream view of a CI
// (spec §15). Upstream = what this CI depends on (follow edges target→source
// out of the node); downstream = what depends on it (source→target into the
// node's dependents).
type DependencyResult struct {
	CIID      string `json:"ci_id"`
	Direction string `json:"direction"`
	Depth     int    `json:"depth"`
	Nodes     []Node `json:"nodes"`
	Count     int    `json:"count"`
}

// BlastRadius summarizes the blast radius of a CI failure (spec §15):
// affected CIs plus the resolved assets, business services, clients and sites
// reachable through the impact set.
type BlastRadius struct {
	CIID             string   `json:"ci_id"`
	AffectedCIs      []Node   `json:"affected_cis"`
	AffectedCount    int      `json:"affected_count"`
	AffectedServices []Node   `json:"affected_services,omitempty"`
	ClientIDs        []string `json:"client_ids,omitempty"`
	SiteIDs          []string `json:"site_ids,omitempty"`
	// SinglePointsOfFailure lists the affected CIs that depend on the failed
	// CI directly without an alternative provider (IMP-04).
	SinglePointsOfFailure []Node `json:"single_points_of_failure,omitempty"`
	// ProtectedByRedundancy lists the CIs that depend on the failed CI over
	// a redundant type but keep a working provider (IMP-04).
	ProtectedByRedundancy []Node `json:"protected_by_redundancy,omitempty"`
}

// GetDependencies handles GET /api/v1/cis/{id}/dependencies?direction=up|down
func (h *Handler) GetDependencies(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := h.ciRepo.GetByID(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "downstream"
	}
	depth := parseDepth(r.URL.Query().Get("depth"))
	if r.URL.Query().Get("depth") == "" {
		depth = maxDepth
	}
	graph, err := h.buildFromRoot(r.Context(), t.OrganizationID, id, depth, "")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	var reachable map[string]struct{}
	switch direction {
	case "upstream":
		reachable = walkDirected(id, graph.Edges, true)
	case "downstream":
		reachable = walkDirected(id, graph.Edges, false)
	default:
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "direction must be upstream or downstream")
		return
	}
	nodes := make([]Node, 0, len(reachable))
	for _, node := range graph.Nodes {
		if _, ok := reachable[node.ID]; ok {
			nodes = append(nodes, node)
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
	if _, err := h.ciRepo.GetByID(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}
	graph, err := h.buildFromRoot(r.Context(), t.OrganizationID, id, maxDepth, "")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	rules := h.impactRules(r.Context(), t.OrganizationID)
	impacted := computeImpact([]string{id}, graph.Edges, impactQuery{depth: maxDepth}, rules)
	result := BlastRadius{CIID: id}
	services := map[string]struct{}{}
	clients := map[string]struct{}{}
	sites := map[string]struct{}{}
	for _, node := range graph.Nodes {
		if _, ok := impacted[node.ID]; !ok {
			continue
		}
		result.AffectedCIs = append(result.AffectedCIs, node)
		if node.ClientID != "" {
			clients[node.ClientID] = struct{}{}
		}
		if node.SiteID != "" {
			sites[node.SiteID] = struct{}{}
		}
	}
	result.AffectedCount = len(result.AffectedCIs)
	for id := range clients {
		result.ClientIDs = append(result.ClientIDs, id)
	}
	for id := range sites {
		result.SiteIDs = append(result.SiteIDs, id)
	}
	// Business services in the affected set (logical type heuristic: the CI
	// type key resolves through ci.CITypeID which carries the type id; the
	// caller can filter by type on the client side).
	for _, node := range result.AffectedCIs {
		if _, ok := services[node.ID]; ok {
			result.AffectedServices = append(result.AffectedServices, node)
		}
	}
	result.SinglePointsOfFailure, result.ProtectedByRedundancy = redundancyFindings(id, graph, impacted, rules)
	api.WriteJSON(w, http.StatusOK, result)
}

// walkDirected performs a directed BFS. upstream=true follows edges from
// target to source (dependencies of the node); otherwise source to target
// (dependents of the node).
func walkDirected(rootID string, edges []Edge, upstream bool) map[string]struct{} {
	reachable := map[string]struct{}{}
	queue := []string{rootID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edges {
			var from, to string
			if upstream {
				from, to = edge.TargetCIID, edge.SourceCIID
			} else {
				from, to = edge.SourceCIID, edge.TargetCIID
			}
			if from != current || to == rootID {
				continue
			}
			if _, seen := reachable[to]; seen {
				continue
			}
			reachable[to] = struct{}{}
			queue = append(queue, to)
		}
	}
	return reachable
}

// ImpactNode is an impacted CI with its shortest path from the failed CI
// (IMP-02); Heuristic marks a CI reached only over an undirected network
// edge (connected_to), a second-class path (IMP-03). Via is the last step of
// the path, so following Via from CI to CI rebuilds the path without
// transferring it (NFR-02); Relay is set on a certain CI that the shortest
// path over all edges reaches over another step: a heuristic CI's path
// continues over the Relay of the certain CIs on it (impactPath).
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

// impactStep returns the CI a failure of from reaches over edge, if any.
// heuristic is true for an undirected edge.
func impactStep(edge *Edge, from string, rule impactRule) (to string, heuristic, ok bool) {
	switch rule.direction {
	case relationshiptype.ImpactSourceDependsOnTarget:
		if edge.TargetCIID == from {
			return edge.SourceCIID, false, true
		}
	case relationshiptype.ImpactTargetDependsOnSource:
		if edge.SourceCIID == from {
			return edge.TargetCIID, false, true
		}
	case relationshiptype.ImpactUndirected:
		switch from {
		case edge.SourceCIID:
			return edge.TargetCIID, true, true
		case edge.TargetCIID:
			return edge.SourceCIID, true, true
		}
	}
	return "", false, false
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

// impactLink is the last step of the path to an impacted CI.
type impactLink struct {
	from      string
	relType   string
	category  string
	heuristic bool
}

func (l impactLink) via() Via { return Via{CIID: l.from, RelType: l.relType} }

// impactHit is how the failure reaches one CI: the length of the shortest
// path and its last step (via), and whether only an undirected network edge
// reaches the CI. A certain CI carries the step of its shortest directed
// path in via; relay holds the step of its shortest path over all edges
// when that differs, the step the paths of heuristic CIs behind it take.
type impactHit struct {
	heuristic bool
	hops      int
	via       impactLink
	relay     *impactLink
}

// impactQuery narrows an impact analysis: one relationship type, a set of
// categories (empty: all) and the maximum number of hops.
type impactQuery struct {
	relType    string
	categories map[string]bool
	depth      int
}

// computeImpact returns the CIs a failure of the failed CIs reaches along
// the impact directions (IMP-01) with the shortest path to each (IMP-02); a
// CI reached only over an undirected network edge is heuristic (IMP-03) and
// then carries its shortest path over all edges. The redundancy postfilter
// (IMP-04) keeps a CI that still has a working provider of a redundant
// type. One downward closure serves both walks: breadth-first over the
// directed steps among the certain CIs, and over all steps among every CI
// that goes down; edges in a fixed order, so the paths are deterministic.
// Each walk keeps the last step per CI only: impactPath rebuilds the paths.
func computeImpact(failedIDs []string, edges []Edge, q impactQuery, rules map[string]impactRule) map[string]impactHit {
	failed := map[string]bool{}
	for _, id := range failedIDs {
		failed[id] = true
	}
	follow := func(e *Edge, rule impactRule) bool {
		if q.relType != "" && e.RelType != q.relType {
			return false
		}
		return len(q.categories) == 0 || q.categories[rule.category]
	}
	incident := incidence(edges)
	level := closeDown(failed, edges, incident, rules, follow)
	walk := func(withHeuristic bool) map[string]impactHit {
		hits := make(map[string]impactHit, len(level))
		queue := make([]string, 0, len(failedIDs))
		for _, id := range failedIDs {
			if _, ok := hits[id]; !ok {
				hits[id] = impactHit{}
				queue = append(queue, id)
			}
		}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			hops := hits[current].hops
			if q.depth > 0 && hops >= q.depth {
				continue
			}
			for _, i := range incident[current] {
				rule := ruleOf(rules, edges[i].RelType)
				if !follow(&edges[i], rule) {
					continue
				}
				to, heuristic, ok := impactStep(&edges[i], current, rule)
				if !ok || level[to] == levelUp || (!withHeuristic && (heuristic || level[to] != levelCertain)) {
					continue
				}
				if _, seen := hits[to]; seen {
					continue
				}
				hits[to] = impactHit{hops: hops + 1, via: impactLink{from: current, relType: edges[i].RelType,
					category: rule.category, heuristic: heuristic}}
				queue = append(queue, to)
			}
		}
		for id := range failed {
			delete(hits, id)
		}
		return hits
	}
	certain := walk(false)
	impacted := walk(true)
	for id, hit := range impacted {
		sure, ok := certain[id]
		if !ok {
			hit.heuristic = true
			impacted[id] = hit
			continue
		}
		if hit.via.from != sure.via.from || hit.via.relType != sure.via.relType {
			relay := hit.via
			sure.relay = &relay
		}
		impacted[id] = sure
	}
	return impacted
}

// impactPath rebuilds the shortest path to an impacted CI from the last
// steps (IMP-02): back along via to a failed CI, on the path of a heuristic
// CI over the relay of the certain CIs it passes.
func impactPath(hits map[string]impactHit, id string) []PathStep {
	hit := hits[id]
	path := make([]PathStep, hit.hops)
	to := id
	for i := hit.hops - 1; i >= 0; i-- {
		at := hits[to]
		link := at.via
		if hit.heuristic && at.relay != nil {
			link = *at.relay
		}
		path[i] = PathStep{FromCIID: link.from, ToCIID: to, RelType: link.relType, Category: link.category, Heuristic: link.heuristic}
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

// writeImpact analyses the failure of ids and writes the answer: the
// failed CIs, the impacted CIs with their paths and a summary; single adds
// the fields of the single-CI short form; sparse lets fields= narrow the
// impacted CIs (API-02), and a path is only built when it is returned.
func (h *Handler) writeImpact(w http.ResponseWriter, r *http.Request, orgID string, ids []string, single, sparse bool) {
	ctx := r.Context()
	q := r.URL.Query()
	depth := parseDepth(q.Get("depth"))
	if q.Get("depth") == "" {
		depth = maxDepth
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
	nodes := map[string]Node{}
	var edgeList []Edge
	edges := map[string]Edge{}
	for _, id := range ids {
		root, gerr := h.ciRepo.GetByID(ctx, orgID, id)
		if gerr != nil {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found: "+id)
			return
		}
		failed = append(failed, nodeFromItem(*root))
		graph, berr := h.buildFromRoot(ctx, orgID, id, depth, "")
		if berr != nil {
			api.WriteRepoError(w, berr)
			return
		}
		for _, n := range graph.Nodes {
			nodes[n.ID] = n
		}
		// The edges of one graph are distinct; several graphs share edges.
		if len(ids) == 1 {
			edgeList = graph.Edges
			continue
		}
		for _, e := range graph.Edges {
			edges[e.ID] = e
		}
	}
	for _, e := range edges {
		edgeList = append(edgeList, e)
	}
	slices.SortFunc(edgeList, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })

	relType := q.Get("rel_type")
	impacted := computeImpact(ids, edgeList, impactQuery{relType: relType, categories: categories, depth: depth},
		h.impactRules(ctx, orgID))
	impactedNodes := make([]ImpactNode, 0, len(impacted))
	summary := ImpactSummary{Failed: len(failed), ByCategory: map[string]int{}}
	for id, hit := range impacted {
		node, ok := nodes[id]
		if !ok {
			continue
		}
		entry := ImpactNode{Node: node, Heuristic: hit.heuristic, Hops: hit.hops, Via: hit.via.via()}
		if hit.relay != nil {
			relay := hit.relay.via()
			entry.Relay = &relay
		}
		if withPaths {
			entry.Path = impactPath(impacted, id)
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
	// Certain impacts first, then by name: a stable answer.
	sort.Slice(impactedNodes, func(i, j int) bool {
		a, b := &impactedNodes[i], &impactedNodes[j]
		if a.Heuristic != b.Heuristic {
			return !a.Heuristic
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
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
