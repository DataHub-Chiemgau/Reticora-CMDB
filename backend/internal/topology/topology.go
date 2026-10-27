// Package topology provides read-only topology graph queries for Reticora CMDB.
// It composes the existing CI and relationship repositories to return a
// {nodes, edges} view of the configuration graph for a tenant.
package topology

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

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

// DefaultDepth is the depth of a walk from a root without a depth.
const DefaultDepth = defaultDepth

// ErrCINotFound reports a root_ci_id the caller does not see: an unknown or
// deleted CI, a CI of another organization or outside the client or site
// scope. GET /topology answers 404 "ci not found" for each alike, like
// GetNeighbors (F-161); it wraps api.ErrNotFound, so WriteRepoError answers
// 404 as well.
var ErrCINotFound = fmt.Errorf("ci %w", api.ErrNotFound)

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
	// walker walks with CI numbers (WP-611); the memory repository has
	// none, its walks go through traverser or hop by hop.
	walker relationship.Walker
	// types supplies the impact semantics of the organization's
	// relationship types; without it the built-in catalogue applies.
	types relationshiptype.Repository
}

// NewHandler creates a topology handler backed by the CI and relationship
// repositories. When the relationship repository also implements
// relationship.Walker or relationship.Traverser, graph walks run as one
// walk of the repository instead of iterative hop-by-hop lookups.
func NewHandler(ciRepo ci.Repository, relRepo relationship.Repository) *Handler {
	h := &Handler{ciRepo: ciRepo, relRepo: relRepo}
	if traverser, ok := relRepo.(relationship.Traverser); ok {
		h.traverser = traverser
	}
	if walker, ok := relRepo.(relationship.Walker); ok {
		h.walker = walker
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
	r.Get("/api/v1/impact/{ci_id}", h.GetCIImpact)
	// Directed dependency/impact analysis (spec §15).
	r.Get("/api/v1/cis/{id}/dependencies", h.GetDependencies)
	r.Get("/api/v1/cis/{id}/blast-radius", h.GetBlastRadius)
}

func nodeFromCI(n *ci.Node) Node {
	return Node{
		ID:           n.ID,
		Name:         n.Name,
		CIType:       n.CITypeID,
		Status:       n.Status,
		ClientID:     n.ClientID,
		SiteID:       n.SiteID,
		ManagementIP: n.ManagementIP,
	}
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
	depth, err := parseDepth(q.Get("depth"), defaultDepth)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}

	graph, err := h.Graph(r.Context(), t.OrganizationID, rootCIID, depth, &ci.FilterParams{
		ClientID: q.Get("client_id"),
		SiteID:   q.Get("site_id"),
		TypeID:   ciType,
	})
	if err != nil {
		if errors.Is(err, ErrCINotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
			return
		}
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
	root, err := h.ciRepo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}

	g, err := h.graphFrom(r.Context(), t.OrganizationID, root, 1)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g.topology(""))
}

// GetImpact handles GET /api/v1/topology/cis/{id}/impact
//
// It answers the failure-simulation question (spec §4): which CIs lose
// connectivity/power/hosting when this CI fails. The failure travels along
// the impact_direction of each relationship type (IMP-01); connected_to is
// undirected and only a network heuristic, so CIs reached only through it
// are marked heuristic (IMP-03). The rel_type facet (powered_by,
// connected_to, hosted_on, …) narrows the simulation to one dependency
// class. The deprecated path answers in the full form until its sunset
// (API-01); fields= narrows the answer on the catalog path only.
func (h *Handler) GetImpact(w http.ResponseWriter, r *http.Request) {
	h.writeSingleImpact(w, r, chi.URLParam(r, "id"), false)
}

// GetCIImpact handles GET /api/v1/impact/{ci_id}, the catalog path of
// GetImpact (API-05): fields= returns a compact answer (API-02).
func (h *Handler) GetCIImpact(w http.ResponseWriter, r *http.Request) {
	h.writeSingleImpact(w, r, chi.URLParam(r, "ci_id"), true)
}

func (h *Handler) writeSingleImpact(w http.ResponseWriter, r *http.Request, id string, sparse bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	h.writeImpact(w, r, t.OrganizationID, []string{id}, true, sparse)
}

// Graph returns the topology graph: from rootCIID up to depth hops (the CI
// type of filter narrows the nodes), or without a root the CIs of filter
// with their edges, at most MaxTopologyNodes (IMP-06, 422 beyond). A root
// that is no UUID is a 400 *api.FieldError, a root the caller does not see
// ErrCINotFound.
func (h *Handler) Graph(ctx context.Context, orgID, rootCIID string, depth int, filter *ci.FilterParams) (Graph, error) {
	if rootCIID != "" {
		if !api.IsUUID(rootCIID) {
			return Graph{}, &api.FieldError{Field: "root_ci_id", Message: "must be a UUID"}
		}
		return h.buildFromRoot(ctx, orgID, rootCIID, depth, filter.TypeID)
	}
	return h.buildFull(ctx, orgID, *filter)
}

// ParseDepth reads a depth parameter: the default 2 for "", an integer
// of at least 1 capped at 20; anything else is a 400 *api.FieldError.
func ParseDepth(raw string) (int, error) { return parseDepth(raw, defaultDepth) }

// buildFull lists all CIs matching the filter and every edge whose endpoints are
// both in the resulting node set, ordered by id. GET /topology without a root
// and the BFF query topology share it.
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
	ids := make([]string, 0, len(items))
	for _, item := range items {
		nodeSet[item.ID] = struct{}{}
		nodes = append(nodes, nodeFromItem(item))
		ids = append(ids, item.ID)
	}

	rels, err := h.edgesAmong(ctx, orgID, ids)
	if err != nil {
		return Graph{}, err
	}
	edges := make([]Edge, 0, len(rels))
	seen := make(map[string]struct{}, len(rels))
	for i := range rels {
		rel := &rels[i]
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
	slices.SortFunc(edges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })

	return Graph{Nodes: nodes, Edges: edges}, nil
}

// edgesAmong reads the edges between the CIs of ids: in one statement when
// the repository reads them in batches (relationship.EdgeReader, NFR-02),
// else the edges of each CI (the memory repository); buildFull keeps those
// between two of ids.
func (h *Handler) edgesAmong(ctx context.Context, orgID string, ids []string) ([]relationship.Relationship, error) {
	if reader, ok := h.relRepo.(relationship.EdgeReader); ok {
		return reader.EdgesAmong(ctx, orgID, ids)
	}
	var rels []relationship.Relationship
	for _, id := range ids {
		list, err := h.listRelationships(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		rels = append(rels, list...)
	}
	return rels, nil
}

// buildFromRoot walks the configuration graph from rootCIID up to depth hops.
// When ciType is non-empty, non-root nodes not matching the type are dropped
// along with any edges that dangle as a result.
func (h *Handler) buildFromRoot(ctx context.Context, orgID, rootCIID string, depth int, ciType string) (Graph, error) {
	root, err := h.root(ctx, orgID, rootCIID)
	if err != nil {
		return Graph{}, err
	}
	g, err := h.graphFrom(ctx, orgID, root, depth)
	if err != nil {
		return Graph{}, err
	}
	return g.topology(ciType), nil
}

// graphFrom walks the configuration graph from root, a CI the caller has
// read, up to depth hops and resolves the nodes of the walk in one read.
// It prefers the repository's walk with CI numbers (relationship.Walker,
// WP-611) and falls back to its traversal or to a breadth-first expansion
// hop by hop (the memory profile). A CI that is not visible or was removed
// stays out; its dangling edges are dropped.
func (h *Handler) graphFrom(ctx context.Context, orgID string, root *ci.Item, depth int) (*ciGraph, error) {
	if h.walker != nil {
		return h.walkedGraph(ctx, orgID, root, depth)
	}
	var nodes map[string]Node
	var edges []Edge
	var err error
	if h.traverser != nil {
		nodes, edges, err = h.traversedGraph(ctx, orgID, root, depth)
	} else {
		nodes, edges, err = h.iterativeGraph(ctx, orgID, root, depth)
	}
	if err != nil {
		return nil, err
	}
	g, _ := graphOf(root.ID, nodes, edges)
	return g, nil
}

// walkedGraph is graphFrom with the repository's walk: the graph keeps its
// CI numbers, and the nodes are read in one query (NFR-02).
func (h *Handler) walkedGraph(ctx context.Context, orgID string, root *ci.Item, depth int) (*ciGraph, error) {
	walk, err := h.walker.WalkFrom(ctx, orgID, root.ID, depth, relationship.MaxTraversalNodes)
	if err != nil {
		return nil, err
	}
	g := &ciGraph{ids: walk.CIs, nodes: make([]Node, len(walk.CIs)), known: make([]bool, len(walk.CIs)),
		edges: make([]ciEdge, len(walk.Edges))}
	g.nodes[0], g.known[0] = nodeFromItem(*root), true
	types := map[string]int32{}
	for i := range walk.Edges {
		e := &walk.Edges[i]
		g.edges[i] = ciEdge{id: e.ID, source: e.SourceCI, target: e.TargetCI, relType: g.typeNumber(types, e.RelType), origin: e.Source}
	}
	if len(g.ids) > 1 {
		nodes, err := h.readNodes(ctx, orgID, g.ids[1:])
		if err != nil {
			return nil, err
		}
		number := make(map[string]int32, len(g.ids))
		for n, id := range g.ids {
			number[id] = int32(n)
		}
		for i := range nodes {
			if n, ok := number[nodes[i].ID]; ok && n != 0 {
				g.nodes[n], g.known[n] = nodes[i], true
			}
		}
	}
	kept := g.edges[:0]
	for _, e := range g.edges {
		if g.known[e.source] && g.known[e.target] {
			kept = append(kept, e)
		}
	}
	g.edges = kept
	return g, nil
}

// traversedGraph expands the subgraph with the repository's traversal,
// then resolves the discovered nodes.
func (h *Handler) traversedGraph(ctx context.Context, orgID string, root *ci.Item, depth int) (map[string]Node, []Edge, error) {
	rels, err := h.traverser.TraverseFrom(ctx, orgID, root.ID, depth, relationship.MaxTraversalNodes)
	if err != nil {
		return nil, nil, err
	}

	nodes := map[string]Node{root.ID: nodeFromItem(*root)}
	edges := make([]Edge, 0, len(rels))
	var ids []string
	for i := range rels {
		rel := &rels[i]
		edges = append(edges, edgeFromRel(rel))
		for _, neighborID := range []string{rel.SourceCIID, rel.TargetCIID} {
			if neighborID != "" && neighborID != root.ID {
				ids = append(ids, neighborID)
			}
		}
	}
	// One read for all nodes (NFR-02).
	if err := h.resolveNodes(ctx, orgID, ids, nodes); err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

// iterativeGraph performs a breadth-first traversal from root up to depth
// hops, querying relationships one frontier at a time.
func (h *Handler) iterativeGraph(ctx context.Context, orgID string, root *ci.Item, depth int) (map[string]Node, []Edge, error) {
	nodes := map[string]Node{root.ID: nodeFromItem(*root)}
	edgeSet := make(map[string]Edge)
	visited := map[string]bool{}
	frontier := []string{root.ID}

	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			if visited[id] {
				continue
			}
			visited[id] = true

			rels, err := h.listRelationships(ctx, orgID, id)
			if err != nil {
				return nil, nil, err
			}
			for i := range rels {
				rel := &rels[i]
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
		// (IMP-07); their edges dangle and are dropped.
		if err := h.resolveNodes(ctx, orgID, next, nodes); err != nil {
			return nil, nil, err
		}
		frontier = frontier[:0]
		for _, neighborID := range next {
			if _, ok := nodes[neighborID]; ok && !visited[neighborID] && len(nodes) <= relationship.MaxTraversalNodes {
				frontier = append(frontier, neighborID)
			}
		}
	}

	return nodes, slices.Collect(maps.Values(edgeSet)), nil
}

// root reads the root CI of a walk. A CI the repository does not find
// (unknown, deleted, or invisible under the caller's organization and
// scope) is ErrCINotFound; any other failure stays as it is.
func (h *Handler) root(ctx context.Context, orgID, rootCIID string) (*ci.Item, error) {
	root, err := h.ciRepo.GetByID(ctx, orgID, rootCIID)
	if err != nil && (err.Error() == "not found" || errors.Is(err, api.ErrNotFound)) {
		return nil, ErrCINotFound
	}
	return root, err
}

// resolveNodes adds the visible, not deleted CIs among ids to nodes
// (readNodes).
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
	list, err := h.readNodes(ctx, orgID, missing)
	if err != nil {
		return err
	}
	for i := range list {
		nodes[list[i].ID] = list[i]
	}
	return nil
}

// readNodes reads the visible, not deleted CIs among ids, in one query
// when the repository reads in batches: the node projection without
// attributes (NFR-02), else the full CIs.
func (h *Handler) readNodes(ctx context.Context, orgID string, ids []string) ([]Node, error) {
	switch reader := h.ciRepo.(type) {
	case ci.NodeReader:
		list, err := reader.ListNodes(ctx, orgID, ids)
		if err != nil {
			return nil, err
		}
		nodes := make([]Node, len(list))
		for i := range list {
			nodes[i] = nodeFromCI(&list[i])
		}
		return nodes, nil
	case ci.BatchReader:
		items, err := reader.GetByIDs(ctx, orgID, ids)
		if err != nil {
			return nil, err
		}
		nodes := make([]Node, len(items))
		for i := range items {
			nodes[i] = nodeFromItem(items[i])
		}
		return nodes, nil
	}
	var nodes []Node
	for _, id := range ids {
		if item, err := h.ciRepo.GetByID(ctx, orgID, id); err == nil {
			nodes = append(nodes, nodeFromItem(*item))
		}
	}
	return nodes, nil
}

// ciGraph is the graph of a walk with its CIs as numbers (NFR-02, WP-611):
// the topology, the impact analysis and the redundancy findings index
// slices by number instead of maps by id. The root is number 0.
type ciGraph struct {
	// ids holds the id of each CI by number, nodes its node where known
	// is set: the CI was read, visible and not deleted.
	ids   []string
	nodes []Node
	known []bool
	// edges are the edges between two known CIs, ordered by id, with
	// their type as a number of relTypes.
	edges    []ciEdge
	relTypes []string
	// first and incident list the edges of each CI in graph order: those
	// of CI n are incident[first[n]:first[n+1]] (indexIncidence).
	first, incident []int32
}

// ciEdge is an edge of a ciGraph; origin is the source of the
// relationship (Edge.Source).
type ciEdge struct {
	id             string
	source, target int32
	relType        int32
	origin         string
}

// typeNumber returns the number of a relationship type in g.
func (g *ciGraph) typeNumber(types map[string]int32, relType string) int32 {
	if n, ok := types[relType]; ok {
		return n
	}
	n := int32(len(g.relTypes))
	types[relType] = n
	g.relTypes = append(g.relTypes, relType)
	return n
}

// graphOf numbers the CIs of a graph given by ids: the root first, then
// the ends of the edges in the order of their ids, then the other nodes by
// id. It keeps the edges between two nodes, ordered by id, and returns the
// number of each id as well.
func graphOf(rootID string, nodes map[string]Node, edges []Edge) (g *ciGraph, number map[string]int32) {
	sorted := slices.Clone(edges)
	slices.SortStableFunc(sorted, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	g = &ciGraph{}
	number = map[string]int32{}
	add := func(id string) int32 {
		if n, ok := number[id]; ok {
			return n
		}
		n := int32(len(g.ids))
		number[id] = n
		node, known := nodes[id]
		g.ids, g.nodes, g.known = append(g.ids, id), append(g.nodes, node), append(g.known, known)
		return n
	}
	add(rootID)
	types := map[string]int32{}
	for i := range sorted {
		e := &sorted[i]
		_, source := nodes[e.SourceCIID]
		_, target := nodes[e.TargetCIID]
		if source && target {
			g.edges = append(g.edges, ciEdge{id: e.ID, source: add(e.SourceCIID), target: add(e.TargetCIID),
				relType: g.typeNumber(types, e.RelType), origin: e.Source})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		add(id)
	}
	return g, number
}

// indexIncidence lists the edges of each CI in graph order, so a step only
// looks at the edges of the CI it leaves (NFR-02).
func (g *ciGraph) indexIncidence() {
	if g.first != nil {
		return
	}
	n := len(g.ids)
	g.first = make([]int32, n+1)
	for i := range g.edges {
		g.first[g.edges[i].source+1]++
		if g.edges[i].target != g.edges[i].source {
			g.first[g.edges[i].target+1]++
		}
	}
	for v := 0; v < n; v++ {
		g.first[v+1] += g.first[v]
	}
	g.incident = make([]int32, g.first[n])
	next := slices.Clone(g.first[:n])
	for i := range g.edges {
		e := &g.edges[i]
		g.incident[next[e.source]] = int32(i)
		next[e.source]++
		if e.target != e.source {
			g.incident[next[e.target]] = int32(i)
			next[e.target]++
		}
	}
}

// incidentTo returns the edges of CI n in graph order (indexIncidence).
func (g *ciGraph) incidentTo(n int32) []int32 {
	return g.incident[g.first[n]:g.first[n+1]]
}

// topology is the graph as GET /topology answers it: the known CIs in the
// order of their numbers, of type ciType when it is set (the root always),
// and the edges between two of them, ordered by id.
func (g *ciGraph) topology(ciType string) Graph {
	included := make([]bool, len(g.ids))
	nodes := make([]Node, 0, len(g.ids))
	for n := range g.ids {
		if !g.known[n] || (ciType != "" && n != 0 && g.nodes[n].CIType != ciType) {
			continue
		}
		included[n] = true
		nodes = append(nodes, g.nodes[n])
	}
	edges := make([]Edge, 0, len(g.edges))
	for i := range g.edges {
		e := &g.edges[i]
		if included[e.source] && included[e.target] {
			edges = append(edges, Edge{ID: e.id, SourceCIID: g.ids[e.source], TargetCIID: g.ids[e.target],
				RelType: g.relTypes[e.relType], Source: e.origin})
		}
	}
	return Graph{Nodes: nodes, Edges: edges}
}

func (h *Handler) listRelationships(ctx context.Context, orgID, ciID string) ([]relationship.Relationship, error) {
	rels, _, err := h.relRepo.List(ctx, orgID, ciID, api.PaginationParams{Limit: maxFetch, Offset: 0})
	return rels, err
}

func edgeFromRel(rel *relationship.Relationship) Edge {
	return Edge{
		ID:         rel.ID,
		SourceCIID: rel.SourceCIID,
		TargetCIID: rel.TargetCIID,
		RelType:    rel.RelType,
		Source:     rel.Source,
	}
}

// parseDepth reads the depth parameter of a walk (IMP-02, F-255): fallback
// for "", an integer of at least 1, capped at maxDepth (20, documented in
// the OpenAPI). A value that is no integer or below 1 is a 400 field error
// on depth instead of a silent default.
func parseDepth(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-") {
		// A positive integer beyond int is capped like any other.
		return maxDepth, nil
	}
	if err != nil || n < 1 {
		return 0, &api.FieldError{Field: "depth", Message: fmt.Sprintf("must be an integer of at least 1 (at most %d are walked)", maxDepth)}
	}
	return min(n, maxDepth), nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
