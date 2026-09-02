package topology

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	var reachable map[string]struct{}
	switch direction {
	case "upstream":
		// Dependencies of the node: follow its outgoing edges source→target.
		reachable = walkDirected(id, graph.Edges, false)
	case "downstream":
		// Dependents of the node: follow incoming edges target→source.
		reachable = walkDirected(id, graph.Edges, true)
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	impacted := computeImpact(id, graph.Edges, "")
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

// walkDirected performs a directed BFS. Relationship edges point from the
// dependent to its dependency (source depends on target). reverse=true
// follows edges from target to source (the node's dependents); reverse=false
// follows source to target (the node's dependencies).
func walkDirected(rootID string, edges []Edge, reverse bool) map[string]struct{} {
	reachable := map[string]struct{}{}
	queue := []string{rootID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edges {
			var from, to string
			if reverse {
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
// direct dependent of the failed CI is a SPOF candidate when it has no
// remaining outgoing redundancy edge of the same dependency class to a
// surviving node.
func findSPOFs(failedID string, edges []Edge, affected []Node) []Node {
	outgoing := map[string]map[string]int{} // source -> relType -> count to non-failed targets
	for _, edge := range edges {
		if edge.TargetCIID == failedID {
			continue
		}
		if _, ok := outgoing[edge.SourceCIID]; !ok {
			outgoing[edge.SourceCIID] = map[string]int{}
		}
		outgoing[edge.SourceCIID][edge.RelType]++
	}
	var spofs []Node
	for _, node := range affected {
		if node.ID == failedID {
			continue
		}
		// Direct dependents of the failed node with no surviving outgoing
		// edge of the same type have no redundancy.
		for _, edge := range edges {
			if edge.TargetCIID != failedID || edge.SourceCIID != node.ID {
				continue
			}
			if outgoing[node.ID][edge.RelType] == 0 {
				spofs = append(spofs, node)
				break
			}
		}
	}
	return spofs
}
