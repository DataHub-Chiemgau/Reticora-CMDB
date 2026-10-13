package topology

import (
	"context"
	"log/slog"
	"net/http"
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
	// SinglePointsOfFailure lists CIs in the affected set whose only
	// redundancy path is the failed node (articulation approximation).
	SinglePointsOfFailure []Node `json:"single_points_of_failure,omitempty"`
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
	impacted := computeImpact(id, graph.Edges, impactQuery{depth: maxDepth}, h.impactRules(r.Context(), t.OrganizationID))
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
	result.SinglePointsOfFailure = findSPOFs(id, graph.Edges, result.AffectedCIs)
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

// findSPOFs approximates single points of failure within the affected set: a
// node is a SPOF when removing the failed CI leaves it with no remaining
// incoming redundancy edge of the same dependency class.
func findSPOFs(failedID string, edges []Edge, affected []Node) []Node {
	incoming := map[string]map[string]int{} // target -> relType -> count from non-failed sources
	for _, edge := range edges {
		if edge.SourceCIID == failedID {
			continue
		}
		if _, ok := incoming[edge.TargetCIID]; !ok {
			incoming[edge.TargetCIID] = map[string]int{}
		}
		incoming[edge.TargetCIID][edge.RelType]++
	}
	var spofs []Node
	for _, node := range affected {
		if node.ID == failedID {
			continue
		}
		// Direct dependents of the failed node with no surviving incoming
		// edge of the same type have no redundancy.
		for _, edge := range edges {
			if edge.SourceCIID != failedID || edge.TargetCIID != node.ID {
				continue
			}
			if incoming[node.ID][edge.RelType] == 0 {
				spofs = append(spofs, node)
				break
			}
		}
	}
	return spofs
}

// ImpactNode is an impacted CI with its shortest path from the failed CI
// (IMP-02); Heuristic marks a CI reached only over an undirected network
// edge (connected_to), a second-class path (IMP-03).
type ImpactNode struct {
	Node
	Heuristic bool       `json:"heuristic"`
	Hops      int        `json:"hops"`
	Path      []PathStep `json:"path"`
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
}

// impactRules maps each relationship type key onto its impact direction
// and category: the built-in catalogue (CH27, E-18), overlaid by the
// organization's types.
func (h *Handler) impactRules(ctx context.Context, orgID string) map[string]impactRule {
	rules := map[string]impactRule{}
	catalogue := relationshiptype.Catalogue()
	for i := range catalogue {
		rules[catalogue[i].Key] = impactRule{catalogue[i].ImpactDirection, catalogue[i].Category}
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
		rules[types[i].Key] = impactRule{types[i].ImpactDirection, types[i].Category}
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

// impactHit is how the failure reaches one CI: the shortest path, and
// whether only an undirected network edge reaches it.
type impactHit struct {
	heuristic bool
	path      []PathStep
}

// impactQuery narrows an impact analysis: one relationship type, a set of
// categories (empty: all) and the maximum number of hops.
type impactQuery struct {
	relType    string
	categories map[string]bool
	depth      int
}

// computeImpact returns the CIs a failure of failedID reaches along the
// impact directions (IMP-01) with the shortest path to each (IMP-02); a CI
// reached only over an undirected network edge is heuristic (IMP-03) and
// then carries its shortest path over all edges. Breadth-first over edges
// in a fixed order, so the paths are deterministic.
func computeImpact(failedID string, edges []Edge, q impactQuery, rules map[string]impactRule) map[string]impactHit {
	walk := func(withHeuristic bool) map[string][]PathStep {
		paths := map[string][]PathStep{failedID: {}}
		queue := []string{failedID}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if q.depth > 0 && len(paths[current]) >= q.depth {
				continue
			}
			for i := range edges {
				if q.relType != "" && edges[i].RelType != q.relType {
					continue
				}
				rule := ruleOf(rules, edges[i].RelType)
				if len(q.categories) > 0 && !q.categories[rule.category] {
					continue
				}
				to, heuristic, ok := impactStep(&edges[i], current, rule)
				if !ok || (heuristic && !withHeuristic) {
					continue
				}
				if _, seen := paths[to]; seen {
					continue
				}
				prev := paths[current]
				path := make([]PathStep, len(prev), len(prev)+1)
				copy(path, prev)
				paths[to] = append(path, PathStep{FromCIID: current, ToCIID: to, RelType: edges[i].RelType,
					Category: rule.category, Heuristic: heuristic})
				queue = append(queue, to)
			}
		}
		delete(paths, failedID)
		return paths
	}
	certain := walk(false)
	impacted := map[string]impactHit{}
	for id, path := range walk(true) {
		if sure, ok := certain[id]; ok {
			impacted[id] = impactHit{path: sure}
			continue
		}
		impacted[id] = impactHit{heuristic: true, path: path}
	}
	return impacted
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
