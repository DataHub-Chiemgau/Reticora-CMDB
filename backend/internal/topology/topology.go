// Package topology provides read-only topology graph queries for Reticora CMDB.
// It composes the existing CI and relationship repositories to return a
// {nodes, edges} view of the configuration graph for a tenant.
package topology

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

const (
	maxFetch = 10000
	// MaxTopologyNodes bounds GET /topology without a root (IMP-06).
	MaxTopologyNodes = 2000
	defaultDepth     = 2
	maxDepth         = 20
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
	// types supplies the impact semantics of the organization's
	// relationship types; without it the built-in catalogue applies.
	types relationshiptype.Repository
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
	// impact is the catalog path (API-05); the topology path stays until
	// its sunset (API-01).
	// Several CIs failing at once (IMP-05): redundancy analysis and
	// maintenance windows.
	r.Get("/api/v1/impact", h.GetMultiImpact)
	r.Get("/api/v1/impact/{ci_id}", func(w http.ResponseWriter, r *http.Request) {
		chi.RouteContext(r.Context()).URLParams.Add("id", chi.URLParam(r, "ci_id"))
		h.GetImpact(w, r)
	})
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
		if !api.WriteFieldError(w, err) {
			api.WriteRepoError(w, err)
		}
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
// connectivity/power/hosting when this CI fails. The failure travels along
// the impact_direction of each relationship type (IMP-01); connected_to is
// undirected and only a network heuristic, so CIs reached only through it
// are marked heuristic (IMP-03). The rel_type facet (powered_by,
// connected_to, hosted_on, …) narrows the simulation to one dependency
// class.
func (h *Handler) GetImpact(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	h.writeImpact(w, r, t.OrganizationID, []string{chi.URLParam(r, "id")}, true)
}

// buildFull lists all CIs matching the filter and every edge whose endpoints are
// both in the resulting node set.
func (h *Handler) buildFull(ctx context.Context, orgID string, filter ci.FilterParams) (Graph, error) {
	items, total, err := h.ciRepo.List(ctx, orgID, filter, api.PaginationParams{Limit: MaxTopologyNodes, Offset: 0})
	if err != nil {
		return Graph{}, err
	}
	// Without a root the graph holds at most 2,000 CIs (IMP-06); the UI
	// then starts with a site or type filter.
	if total > MaxTopologyNodes {
		return Graph{}, &api.FieldError{Field: "root_ci_id", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("the topology holds %d CIs, more than %d without a root; set root_ci_id or narrow with site_id, client_id or ci_type (a CI type id)",
				total, MaxTopologyNodes)}
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
	var ids []string
	for _, rel := range rels {
		edgeSet[rel.ID] = edgeFromRel(rel)
		for _, neighborID := range []string{rel.SourceCIID, rel.TargetCIID} {
			if neighborID != "" && neighborID != rootCIID {
				ids = append(ids, neighborID)
			}
		}
	}
	// One read for all nodes (NFR-02). A CI that is not visible or was
	// removed stays out; its dangling edge is dropped by assembleGraph.
	if err := h.resolveNodes(ctx, orgID, ids, nodes); err != nil {
		return Graph{}, err
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
				if neighborID != "" && neighborID != id {
					next = append(next, neighborID)
				}
			}
		}
		// The neighbors of one hop in one read (NFR-02). Invisible or
		// deleted CIs stay out: the walk must not pass through them
		// (IMP-07); their edges dangle and are dropped by assembleGraph.
		if err := h.resolveNodes(ctx, orgID, next, nodes); err != nil {
			return Graph{}, err
		}
		frontier = frontier[:0]
		for _, neighborID := range next {
			if _, ok := nodes[neighborID]; ok && !visited[neighborID] && len(nodes) <= relationship.MaxTraversalNodes {
				frontier = append(frontier, neighborID)
			}
		}
	}

	return assembleGraph(nodes, edgeSet, rootCIID, ciType), nil
}

// resolveNodes adds the visible, not deleted CIs among ids to nodes, in one
// query when the repository reads in batches.
func (h *Handler) resolveNodes(ctx context.Context, orgID string, ids []string, nodes map[string]Node) error {
	missing := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := nodes[id]; !ok && !seen[id] {
			seen[id] = true
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if reader, ok := h.ciRepo.(ci.BatchReader); ok {
		items, err := reader.GetByIDs(ctx, orgID, missing)
		if err != nil {
			return err
		}
		for i := range items {
			nodes[items[i].ID] = nodeFromItem(items[i])
		}
		return nil
	}
	for _, id := range missing {
		if item, err := h.ciRepo.GetByID(ctx, orgID, id); err == nil {
			nodes[id] = nodeFromItem(*item)
		}
	}
	return nil
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

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
