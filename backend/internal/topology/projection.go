package topology

import (
	"context"
	"crypto/md5" //nolint:gosec // a stable edge id like the projected containment of the database, not a secret
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// Node kinds of the topology (IMP-11, WP-298): a CI, or a subnet or a VLAN
// projected from the IPAM tables. A node without kind is a CI.
const (
	NodeKindCI     = "ci"
	NodeKindSubnet = "subnet"
	NodeKindVLAN   = "vlan"
)

// The relationship types of the projected network edges (IMP-11); they are
// relationshiptype.ProjectedKeys.
const (
	RelInSubnet = "in_subnet"
	RelInVLAN   = "in_vlan"
)

// IncludeNetwork is the include= value that adds the projected subnets and
// VLANs to the topology and the impact (IMP-11).
const IncludeNetwork = "network"

// NetworkProjector reads the network of a set of CIs (IMP-11): the subnets
// and VLANs they hang off with the links, in one call
// (ipam.PGRepository.NetworkProjection).
type NetworkProjector interface {
	NetworkProjection(ctx context.Context, orgID string, ciIDs []string) (ipam.NetworkProjection, error)
}

// WithNetworkProjection lets include=network add the subnets and VLANs of
// the CIs to the topology and the impact (IMP-11, WP-298). Without it
// network is an unknown include value.
func (h *Handler) WithNetworkProjection(p NetworkProjector) *Handler {
	h.network = p
	return h
}

// SubnetNodeID and VLANNodeID are the node ids of a projected subnet and
// VLAN; they are no CI ids.
func SubnetNodeID(id string) string { return NodeKindSubnet + ":" + id }

// VLANNodeID is the node id of a projected VLAN.
func VLANNodeID(id string) string { return NodeKindVLAN + ":" + id }

// projectedEdgeID is the stable id of a projected edge: the md5 of its type
// and its ends as a UUID, like the projected containment of the database
// (relationship graphEdges).
func projectedEdgeID(relType, source, target string) string {
	sum := md5.Sum([]byte(relType + ":" + source + ":" + target)) //nolint:gosec // see the import
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// networkLink is a projected edge: from a CI or a subnet to a subnet or a
// VLAN, by node id.
type networkLink struct {
	from, to, relType string
}

// network is a projection resolved to nodes: the projected nodes by node
// id, the links of the CIs, and the VLAN of each subnet node that has one.
type network struct {
	nodes      map[string]Node
	ciLinks    []networkLink
	subnetVLAN map[string]string
}

// readNetwork reads the network of the CIs ciIDs with one call of the
// projector (NFR-02): only subnets and VLANs a CI hangs off and the VLANs
// of those subnets, each visible to the caller.
func (h *Handler) readNetwork(ctx context.Context, orgID string, ciIDs []string) (*network, error) {
	net := &network{nodes: map[string]Node{}, subnetVLAN: map[string]string{}}
	if len(ciIDs) == 0 {
		return net, nil
	}
	p, err := h.network.NetworkProjection(ctx, orgID, ciIDs)
	if err != nil {
		return nil, err
	}
	vlans := make(map[string]bool, len(p.VLANs))
	for i := range p.VLANs {
		v := &p.VLANs[i]
		vlans[v.ID] = true
		net.nodes[VLANNodeID(v.ID)] = Node{ID: VLANNodeID(v.ID), NodeKind: NodeKindVLAN,
			Name: strings.TrimSpace(fmt.Sprintf("VLAN %d %s", v.Number, v.Name)), ClientID: v.ClientID, SiteID: v.SiteID, VLANNumber: v.Number}
	}
	for i := range p.Subnets {
		s := &p.Subnets[i]
		name := s.Name
		if name == "" {
			name = s.CIDR
		}
		id := SubnetNodeID(s.ID)
		net.nodes[id] = Node{ID: id, NodeKind: NodeKindSubnet, Name: name, ClientID: s.ClientID, SiteID: s.SiteID,
			CIDR: s.CIDR, VRFID: s.VRFID}
		if s.VLANID != "" && vlans[s.VLANID] {
			net.subnetVLAN[id] = VLANNodeID(s.VLANID)
		}
	}
	for _, l := range p.CISubnets {
		if _, ok := net.nodes[SubnetNodeID(l.Target)]; ok {
			net.ciLinks = append(net.ciLinks, networkLink{from: l.CIID, to: SubnetNodeID(l.Target), relType: RelInSubnet})
		}
	}
	for _, l := range p.CIVLANs {
		if _, ok := net.nodes[VLANNodeID(l.Target)]; ok {
			net.ciLinks = append(net.ciLinks, networkLink{from: l.CIID, to: VLANNodeID(l.Target), relType: RelInVLAN})
		}
	}
	return net, nil
}

// projectedOrder orders projected nodes by kind, name and id.
func projectedOrder(a, b *Node) int {
	if c := strings.Compare(a.NodeKind, b.NodeKind); c != 0 {
		return c
	}
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// parseTopologyInclude reads include= of GET /topology: network or nothing;
// any other value, and network without a projector, is 422 on include.
func (h *Handler) parseTopologyInclude(raw string) (bool, error) {
	network := false
	for _, name := range strings.Split(raw, ",") {
		switch name = strings.TrimSpace(name); {
		case name == "":
		case name == IncludeNetwork && h.network != nil:
			network = true
		default:
			allowed := "none"
			if h.network != nil {
				allowed = IncludeNetwork
			}
			return false, &api.FieldError{Field: "include", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown include %q; allowed: %s", name, allowed)}
		}
	}
	return network, nil
}

// parseImpactIncludes reads include= of the impact: the blocks of the
// service chain (parseImpactInclude, IMP-08) and network (IMP-11) when a
// projector is wired; any other value is 422 on include.
func (h *Handler) parseImpactIncludes(raw string) (impactInclude, bool, error) {
	allowed := slices.Clone(impactIncludes)
	if h.network != nil {
		allowed = append(allowed, IncludeNetwork)
	}
	network := false
	rest := make([]string, 0, len(allowed))
	for _, name := range strings.Split(raw, ",") {
		switch name = strings.TrimSpace(name); {
		case name == "":
		case name == IncludeNetwork && h.network != nil:
			network = true
		case slices.Contains(impactIncludes, name):
			rest = append(rest, name)
		default:
			return impactInclude{}, false, &api.FieldError{Field: "include", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown include %q; allowed: %s", name, strings.Join(allowed, ", "))}
		}
	}
	in, err := parseImpactInclude(strings.Join(rest, ","))
	return in, network, err
}

// mayReadNetwork reports whether the caller may see the projected network:
// it shows IPAM data (CIDR, VRF, VLAN number), so include=network needs
// ipam:read besides topology:read (IMP-11).
func mayReadNetwork(ctx context.Context) bool {
	p, ok := identity.PrincipalFromContext(ctx)
	return ok && p.Has(identity.PermIPAMRead)
}

// writeNetworkForbidden answers 403 for include=network without ipam:read.
func writeNetworkForbidden(w http.ResponseWriter) {
	api.WriteError(w, http.StatusForbidden, "Forbidden", "include=network requires the permission "+string(identity.PermIPAMRead))
}

// projectTopology adds the subnets and VLANs of the CI nodes of g with
// their edges (IMP-11): in_subnet and in_vlan from a CI, in_vlan from a
// subnet to its VLAN, source projection. It runs after the graph is built,
// so the type filter has already narrowed the CIs and a projected node
// keeps an edge to a CI or a subnet of g; it does not widen the walk.
// Without a root (bounded) the projected nodes count towards
// MaxTopologyNodes (IMP-06, 422 beyond).
func (h *Handler) projectTopology(ctx context.Context, orgID string, g *Graph, bounded bool) error {
	ids := make([]string, len(g.Nodes))
	for i := range g.Nodes {
		ids[i] = g.Nodes[i].ID
	}
	net, err := h.readNetwork(ctx, orgID, ids)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	var edges []Edge
	addEdge := func(from, to, relType string) {
		edges = append(edges, Edge{ID: projectedEdgeID(relType, from, to), SourceCIID: from, TargetCIID: to,
			RelType: relType, Source: relationship.ProjectionSource})
	}
	for _, l := range net.ciLinks {
		used[l.to] = true
		addEdge(l.from, l.to, l.relType)
	}
	for _, subnet := range slices.Sorted(maps.Keys(net.subnetVLAN)) {
		if used[subnet] {
			vlan := net.subnetVLAN[subnet]
			used[vlan] = true
			addEdge(subnet, vlan, RelInVLAN)
		}
	}
	nodes := make([]Node, 0, len(used))
	for id := range used {
		nodes = append(nodes, net.nodes[id])
	}
	if bounded && len(g.Nodes)+len(nodes) > MaxTopologyNodes {
		return &api.FieldError{Field: "include", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("with include=network the topology holds %d nodes (%d CIs, %d subnets and VLANs), more than %d without a root; set root_ci_id, narrow with site_id, client_id or ci_type or leave out include",
				len(g.Nodes)+len(nodes), len(g.Nodes), len(nodes), MaxTopologyNodes)}
	}
	slices.SortFunc(nodes, func(a, b Node) int { return projectedOrder(&a, &b) })
	slices.SortFunc(edges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	g.Nodes = append(g.Nodes, nodes...)
	g.Edges = append(g.Edges, edges...)
	return nil
}

// projectedHit is how the impact reaches a projected node: the length of
// the path, the node it comes from (a CI or a subnet) and the type of the
// last step, with the path of that node.
type projectedHit struct {
	hops    int
	via     Via
	viaPath func() []PathStep
}

// better reports whether a path of hops over via beats hit: shorter, or as
// long and from the smaller id (IMP-11).
func (hit *projectedHit) better(hops int, via string) bool {
	return hit == nil || hops < hit.hops || (hops == hit.hops && via < hit.via.CIID)
}

// projectImpact returns the subnets and VLANs of the failed and impacted
// CIs as leaves of the impact (IMP-11, E-131): read with one call for all
// of them, each with the hops of its CI + 1 (a subnet's VLAN: the subnet's
// + 1, via the subnet), at most depth, the shortest path and, at equal
// length, the one from the smaller id. A projected node is heuristic, its
// steps are of the category network and heuristic; the failure does not
// travel on over it to other CIs. Ordered by kind, name and id.
func (h *Handler) projectImpact(ctx context.Context, orgID string, g *ciGraph, failed []int32, impacted []ciHit,
	order []int32, depth int, withPaths bool) ([]ImpactNode, error) {
	number := make(map[string]int32, len(failed)+len(order))
	ids := make([]string, 0, len(failed)+len(order))
	for _, n := range append(slices.Clone(failed), order...) {
		if _, dup := number[g.ids[n]]; !dup {
			number[g.ids[n]] = n
			ids = append(ids, g.ids[n])
		}
	}
	net, err := h.readNetwork(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	hits := map[string]*projectedHit{}
	reach := func(to string, hops int, via Via, viaPath func() []PathStep) {
		if hops > depth {
			return
		}
		if hit := hits[to]; hit.better(hops, via.CIID) {
			hits[to] = &projectedHit{hops: hops, via: via, viaPath: viaPath}
		}
	}
	for _, l := range net.ciLinks {
		n := number[l.from]
		ciPath := func() []PathStep { return g.path(impacted, n) }
		reach(l.to, impacted[n].hops+1, Via{CIID: l.from, RelType: l.relType}, ciPath)
	}
	for subnet, vlan := range net.subnetVLAN {
		if hit := hits[subnet]; hit != nil {
			subnetPath := func() []PathStep { return projectedPath(hit, subnet) }
			reach(vlan, hit.hops+1, Via{CIID: subnet, RelType: RelInVLAN}, subnetPath)
		}
	}
	out := make([]ImpactNode, 0, len(hits))
	for id, hit := range hits {
		entry := ImpactNode{Node: net.nodes[id], Heuristic: true, Hops: hit.hops, Via: hit.via}
		if withPaths {
			entry.Path = projectedPath(hit, id)
		}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b ImpactNode) int { return projectedOrder(&a.Node, &b.Node) })
	return out, nil
}

// projectedPath is the path to the projected node id reached by hit: the
// path of the node it comes from and the step to id, of the category
// network and heuristic.
func projectedPath(hit *projectedHit, id string) []PathStep {
	return append(slices.Clone(hit.viaPath()), PathStep{FromCIID: hit.via.CIID, ToCIID: id, RelType: hit.via.RelType,
		Category: "network", Heuristic: true})
}
