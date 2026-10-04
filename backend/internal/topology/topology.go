// Package topology provides read-only topology graph queries for Reticora CMDB.
// It composes the existing CI and relationship repositories to return a
// {nodes, edges} view of the configuration graph for a tenant.
package topology

import (
	"context"
	"net/http"
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

const (
	maxFetch     = 10000
	defaultDepth = 2
	maxDepth     = 10
)

// Node is a CI vertex in the topology graph.
type Node struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CIType       string `json:"ci_type"`
	Status       string `json:"status"`
	ClientID     string `json:"client_id,omitempty"`
	SiteID       string `json:"site_id,omitempty"`
	ManagementIP string `json:"management_ip,omitempty"`
}

// Edge is a relationship between two CIs in the topology graph.
type Edge struct {
	ID         string `json:"id"`
	SourceCIID string `json:"source_ci_id"`
	TargetCIID string `json:"target_ci_id"`
	RelType    string `json:"rel_type"`
	Source     string `json:"source,omitempty"`
}

// Graph is the topology response payload.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Handler exposes topology query endpoints.
type Handler struct {
	ciRepo    ci.Repository
	relRepo   relationship.Repository
	traverser relationship.Traverser
}

// NewHandler creates a topology handler backed by the CI and relationship
// repositories. When the relationship repository also implements
// relationship.Traverser, graph walks run as a single recursive query
// instead of iterative hop-by-hop lookups.
func NewHandler(ciRepo ci.Repository, relRepo relationship.Repository) *Handler {
	h := &Handler{ciRepo: ciRepo, relRepo: relRepo}
	if traverser, ok := relRepo.(relationship.Traverser); ok {
		h.traverser = traverser
	}
	return h
}

// RegisterRoutes registers topology routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/topology", h.GetTopology)
	r.Get("/api/v1/topology/cis/{id}/neighbors", h.GetNeighbors)
	r.Get("/api/v1/topology/cis/{id}/impact", h.GetImpact)
	// Directed dependency/impact analysis (spec §15).
	r.Get("/api/v1/cis/{id}/dependencies", h.GetDependencies)
	r.Get("/api/v1/cis/{id}/blast-radius", h.GetBlastRadius)
}

func nodeFromItem(item ci.Item) Node {
	return Node{
		ID:           item.ID,
		Name:         item.Name,
		CIType:       item.CITypeID,
		Status:       item.Status,
		ClientID:     item.ClientID,
		SiteID:       item.SiteID,
		ManagementIP: item.ManagementIP,
	}
}

// GetTopology handles GET /api/v1/topology
func (h *Handler) GetTopology(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	q := r.URL.Query()
	ciType := q.Get("ci_type")
	rootCIID := q.Get("root_ci_id")
	depth := parseDepth(q.Get("depth"))

	var graph Graph
	var err error
	if rootCIID != "" {
		graph, err = h.buildFromRoot(r.Context(), t.OrganizationID, rootCIID, depth, ciType)
	} else {
		filter := ci.FilterParams{
			ClientID: q.Get("client_id"),
			SiteID:   q.Get("site_id"),
			TypeID:   ciType,
		}
		graph, err = h.buildFull(r.Context(), t.OrganizationID, filter)
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, graph)
}

// GetNeighbors handles GET /api/v1/topology/cis/{id}/neighbors
func (h *Handler) GetNeighbors(w http.ResponseWriter, r *http.Request) {
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

	graph, err := h.buildFromRoot(r.Context(), t.OrganizationID, id, 1, "")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, graph)
}

// GetImpact handles GET /api/v1/topology/cis/{id}/impact
//
// It answers the failure-simulation question (spec §4): which CIs lose
// connectivity/power/hosting when this CI fails. The impact set is every CI
// reachable from the failed node by following edges in their directed
// (source→target) direction, because a relationship reads "source supports/
// powers/connects target". The rel_type facet (powered_by, connected_to,
// hosted_on, …) narrows the simulation to one dependency class.
func (h *Handler) GetImpact(w http.ResponseWriter, r *http.Request) {
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

	// Impact analysis walks deeper than the default 2-hop neighborhood view so
	// transitive dependencies surface; the caller can narrow with depth and
	// rel_type.
	depth := parseDepth(r.URL.Query().Get("depth"))
	if r.URL.Query().Get("depth") == "" {
		depth = maxDepth
	}
	relType := r.URL.Query().Get("rel_type")

	graph, err := h.buildFromRoot(r.Context(), t.OrganizationID, id, depth, "")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	impacted := computeImpact(id, graph.Edges, relType)
	impactedNodes := make([]Node, 0, len(impacted))
	for _, node := range graph.Nodes {
		if _, ok := impacted[node.ID]; ok {
			impactedNodes = append(impactedNodes, node)
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"failed_ci_id": id,
		"failed_ci":    nodeFromItem(*root),
		"rel_type":     relType,
		"depth":        depth,
		"impacted":     impactedNodes,
		"count":        len(impactedNodes),
	})
}

// computeImpact walks directed edges source→target from the failed CI and
// returns the set of dependent CI ids. An empty relType follows every edge;
// otherwise only edges of that relationship type are followed.
func computeImpact(failedID string, edges []Edge, relType string) map[string]struct{} {
	impacted := map[string]struct{}{}
	queue := []string{failedID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edges {
			if relType != "" && edge.RelType != relType {
				continue
			}
			// Only follow edges that originate from the current node.
			if edge.SourceCIID != current {
				continue
			}
			// Never follow a back-edge into the failed node itself.
			if edge.TargetCIID == failedID {
				continue
			}
			if _, seen := impacted[edge.TargetCIID]; seen {
				continue
			}
			impacted[edge.TargetCIID] = struct{}{}
			queue = append(queue, edge.TargetCIID)
		}
	}
	return impacted
}

// buildFull lists all CIs matching the filter and every edge whose endpoints are
// both in the resulting node set.
func (h *Handler) buildFull(ctx context.Context, orgID string, filter ci.FilterParams) (Graph, error) {
	items, _, err := h.ciRepo.List(ctx, orgID, filter, api.PaginationParams{Limit: maxFetch, Offset: 0})
	if err != nil {
		return Graph{}, err
	}

	nodeSet := make(map[string]struct{}, len(items))
	nodes := make([]Node, 0, len(items))
	for _, item := range items {
		nodeSet[item.ID] = struct{}{}
		nodes = append(nodes, nodeFromItem(item))
	}

	edges := make([]Edge, 0)
	seen := make(map[string]struct{})
	for id := range nodeSet {
		rels, err := h.listRelationships(ctx, orgID, id)
		if err != nil {
			return Graph{}, err
		}
		for _, rel := range rels {
			if _, ok := nodeSet[rel.SourceCIID]; !ok {
				continue
			}
			if _, ok := nodeSet[rel.TargetCIID]; !ok {
				continue
			}
			if _, dup := seen[rel.ID]; dup {
				continue
			}
			seen[rel.ID] = struct{}{}
			edges = append(edges, edgeFromRel(rel))
		}
	}

	return Graph{Nodes: nodes, Edges: edges}, nil
}

// buildFromRoot walks the configuration graph from rootCIID up to depth hops.
// It prefers the repository's recursive-CTE traversal (one query for the whole
// walk) and falls back to iterative breadth-first expansion when the
// repository does not implement relationship.Traverser. When ciType is
// non-empty, non-root nodes not matching the type are dropped along with any
// edges that dangle as a result.
func (h *Handler) buildFromRoot(ctx context.Context, orgID, rootCIID string, depth int, ciType string) (Graph, error) {
	if h.traverser != nil {
		return h.buildFromRootViaTraversal(ctx, orgID, rootCIID, depth, ciType)
	}
	return h.buildFromRootIterative(ctx, orgID, rootCIID, depth, ciType)
}

// buildFromRootViaTraversal expands the subgraph with a single recursive
// traversal query, then resolves the discovered nodes.
func (h *Handler) buildFromRootViaTraversal(ctx context.Context, orgID, rootCIID string, depth int, ciType string) (Graph, error) {
	root, err := h.ciRepo.GetByID(ctx, orgID, rootCIID)
	if err != nil {
		return Graph{}, err
	}

	rels, err := h.traverser.TraverseFrom(ctx, orgID, rootCIID, depth, relationship.MaxTraversalNodes)
	if err != nil {
		return Graph{}, err
	}

	nodes := map[string]Node{rootCIID: nodeFromItem(*root)}
	edgeSet := make(map[string]Edge, len(rels))
	for _, rel := range rels {
		edgeSet[rel.ID] = edgeFromRel(rel)
		for _, neighborID := range []string{rel.SourceCIID, rel.TargetCIID} {
			if neighborID == "" || neighborID == rootCIID {
				continue
			}
			if _, ok := nodes[neighborID]; ok {
				continue
			}
			item, err := h.ciRepo.GetByID(ctx, orgID, neighborID)
			if err != nil {
				// The CI is not visible to this tenant or was removed; the
				// dangling edge is dropped when the graph is assembled.
				continue
			}
			nodes[neighborID] = nodeFromItem(*item)
		}
	}

	return assembleGraph(nodes, edgeSet, rootCIID, ciType), nil
}

// buildFromRootIterative performs a breadth-first traversal from rootCIID up
// to depth hops, querying relationships one frontier at a time.
func (h *Handler) buildFromRootIterative(ctx context.Context, orgID, rootCIID string, depth int, ciType string) (Graph, error) {
	root, err := h.ciRepo.GetByID(ctx, orgID, rootCIID)
	if err != nil {
		return Graph{}, err
	}

	nodes := map[string]Node{rootCIID: nodeFromItem(*root)}
	edgeSet := make(map[string]Edge)
	visited := map[string]bool{}
	frontier := []string{rootCIID}

	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			if visited[id] {
				continue
			}
			visited[id] = true

			rels, err := h.listRelationships(ctx, orgID, id)
			if err != nil {
				return Graph{}, err
			}
			for _, rel := range rels {
				edgeSet[rel.ID] = edgeFromRel(rel)
				neighborID := rel.TargetCIID
				if neighborID == id {
					neighborID = rel.SourceCIID
				}
				if neighborID == "" || neighborID == id {
					continue
				}
				if _, ok := nodes[neighborID]; !ok {
					item, err := h.ciRepo.GetByID(ctx, orgID, neighborID)
					if err != nil {
						// Invisible or deleted: the walk must not pass
						// through it (IMP-07); its edge dangles and is
						// dropped by assembleGraph.
						continue
					}
					nodes[neighborID] = nodeFromItem(*item)
				}
				if !visited[neighborID] && len(nodes) <= relationship.MaxTraversalNodes {
					next = append(next, neighborID)
				}
			}
		}
		frontier = next
	}

	return assembleGraph(nodes, edgeSet, rootCIID, ciType), nil
}

func assembleGraph(nodes map[string]Node, edgeSet map[string]Edge, rootCIID, ciType string) Graph {
	included := make(map[string]struct{}, len(nodes))
	nodeList := make([]Node, 0, len(nodes))
	for id, node := range nodes {
		if ciType != "" && id != rootCIID && node.CIType != ciType {
			continue
		}
		included[id] = struct{}{}
		nodeList = append(nodeList, node)
	}

	edgeList := make([]Edge, 0, len(edgeSet))
	for _, edge := range edgeSet {
		if _, ok := included[edge.SourceCIID]; !ok {
			continue
		}
		if _, ok := included[edge.TargetCIID]; !ok {
			continue
		}
		edgeList = append(edgeList, edge)
	}
	return Graph{Nodes: nodeList, Edges: edgeList}
}

func (h *Handler) listRelationships(ctx context.Context, orgID, ciID string) ([]relationship.Relationship, error) {
	rels, _, err := h.relRepo.List(ctx, orgID, ciID, api.PaginationParams{Limit: maxFetch, Offset: 0})
	return rels, err
}

func edgeFromRel(rel relationship.Relationship) Edge {
	return Edge{
		ID:         rel.ID,
		SourceCIID: rel.SourceCIID,
		TargetCIID: rel.TargetCIID,
		RelType:    rel.RelType,
		Source:     rel.Source,
	}
}

func parseDepth(raw string) int {
	if raw == "" {
		return defaultDepth
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultDepth
	}
	if n > maxDepth {
		return maxDepth
	}
	return n
}
