package topology

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// Redundancy postfilter of the impact (IMP-04): over a type with
// redundant = true (powered_by, member_of_cluster) a dependent fails only
// when every provider of that type failed; over any other type (hosted_on)
// one failed provider suffices.

// dependentType identifies the providers of one dependent over one type,
// both as numbers of the graph.
type dependentType struct{ dependent, relType int32 }

// downLevel is how a failure of the failed CIs affects a CI.
type downLevel uint8

const (
	// levelUp: the CI keeps working.
	levelUp downLevel = iota
	// levelCertain: the CI goes down over directed steps alone.
	levelCertain
	// levelHeuristic: the CI goes down only when undirected network steps
	// (IMP-03) are followed as well.
	levelHeuristic
)

// outside is the number of a provider that is no CI of the graph: one
// beyond the depth of the walk (readFeeds). It never goes down.
const outside int32 = -1

// redundantProviders lists the providers of each dependent per redundant
// type, over every edge of the graph and every feed to a provider beyond
// it: a provider outside the analysis keeps the dependent up (F-160).
func redundantProviders(g *ciGraph, steps []stepRule) map[dependentType][]int32 {
	providers := map[dependentType][]int32{}
	for _, edges := range [2][]ciEdge{g.edges, g.feeds} {
		for i := range edges {
			e := &edges[i]
			rule := &steps[e.relType]
			if !rule.redundant {
				continue
			}
			for _, from := range [2]int32{e.source, e.target} {
				if to, _, ok := e.step(from, rule); ok && to != outside {
					key := dependentType{to, e.relType}
					providers[key] = append(providers[key], from)
				}
			}
		}
	}
	return providers
}

// readFeeds adds to g the edges of a redundant type of each dependent the
// walk reached over such a type whose edges it may not hold completely,
// whatever the depth (IMP-04, F-160): the walk from roots keeps every edge
// of a CI closer than depth hops, but not those of a CI at the depth, so
// the alternative provider of a dependent at the edge of the analysis may
// be missing; when the node limit cut the walk, any dependent may lack
// one. One statement for the analysis (relationship.IncidentEdgeReader,
// RLS of the caller), none when every such dependent lies within the
// depth; a provider that is no visible CI of g is outside and counts as
// up. Without such a reader g stays as it is.
func (h *Handler) readFeeds(ctx context.Context, orgID string, g *ciGraph, roots []int32, depth int, rules map[string]impactRule) error {
	reader, ok := h.relRepo.(relationship.IncidentEdgeReader)
	if !ok {
		return nil
	}
	steps := g.stepRules(rules, impactQuery{})
	var relTypes []string
	types := map[string]int32{}
	for t := range steps {
		if steps[t].redundant {
			relTypes = append(relTypes, g.relTypes[t])
			types[g.relTypes[t]] = int32(t)
		}
	}
	if len(relTypes) == 0 {
		return nil
	}
	hops := g.hopsFrom(roots)
	dependent := make([]bool, len(g.ids))
	var ids []string
	inGraph := map[string]bool{}
	for i := range g.edges {
		e := &g.edges[i]
		rule := &steps[e.relType]
		if !rule.redundant {
			continue
		}
		inGraph[e.id] = true
		for _, from := range [2]int32{e.source, e.target} {
			to, _, ok := e.step(from, rule)
			if !ok || dependent[to] || (!g.truncated && hops[to] >= 0 && hops[to] < depth) {
				continue
			}
			dependent[to] = true
			ids = append(ids, g.ids[to])
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rels, err := reader.EdgesOf(ctx, orgID, ids, relTypes)
	if err != nil {
		return err
	}
	number := make(map[string]int32, len(g.ids))
	for n, id := range g.ids {
		if g.known[n] {
			number[id] = int32(n)
		}
	}
	end := func(id string) int32 {
		if n, ok := number[id]; ok {
			return n
		}
		return outside
	}
	// An edge between two CIs of g joins its edges: when its provider goes
	// down, the closure retries the dependent over it. The walk dropped it
	// because both ends lie at the depth of the analysis, so no path within
	// the depth passes it. An edge to a provider outside g is a feed.
	added := false
	for i := range rels {
		rel := &rels[i]
		relType, ok := types[rel.RelType]
		if !ok || inGraph[rel.ID] {
			continue
		}
		e := ciEdge{id: rel.ID, source: end(rel.SourceCIID), target: end(rel.TargetCIID), relType: relType, origin: rel.Source}
		if e.source == outside || e.target == outside {
			g.feeds = append(g.feeds, e)
			continue
		}
		g.edges = append(g.edges, e)
		added = true
	}
	if added {
		g.first, g.incident = nil, nil
	}
	return nil
}

// hopsFrom returns, by CI number, the fewest hops from one of roots over
// the edges of g in either direction; -1 for a CI they do not reach.
func (g *ciGraph) hopsFrom(roots []int32) []int {
	g.indexIncidence()
	hops := make([]int, len(g.ids))
	for n := range hops {
		hops[n] = -1
	}
	queue := make([]int32, 0, len(roots))
	for _, n := range roots {
		if hops[n] < 0 {
			hops[n] = 0
			queue = append(queue, n)
		}
	}
	for head := 0; head < len(queue); head++ {
		v := queue[head]
		for _, i := range g.incidentTo(v) {
			next := g.edges[i].target
			if next == v {
				next = g.edges[i].source
			}
			if hops[next] < 0 {
				hops[next] = hops[v] + 1
				queue = append(queue, next)
			}
		}
	}
	return hops
}

// closeDown is the downward closure of impactOf; a variable, so a test
// counts the closures of one analysis.
var closeDown = downLevels

// downLevels returns, by CI number, which CIs are down when the failed CIs
// fail, and how: the failed CIs and, until nothing changes, every
// dependent whose failure a followed step implies (all providers down for
// a redundant type, one for the others). A worklist reaches the same
// fixpoint as repeated scans: a CI only goes down through an edge of a CI
// that just went down (NFR-02). The first pass takes the directed steps
// only (levelCertain) and keeps the undirected ones aside; the second
// retries those and continues from that fixpoint over all steps
// (levelHeuristic). Every step the first pass did not take is tried again
// in the second (a redundant one when its last provider goes down), so one
// closure yields both the closure without and the one with undirected
// steps.
func downLevels(g *ciGraph, failed []int32, steps []stepRule) []downLevel {
	g.indexIncidence()
	providers := redundantProviders(g, steps)
	level := make([]downLevel, len(g.ids))
	queue := make([]int32, 0, len(failed))
	for _, n := range failed {
		if level[n] == levelUp {
			level[n] = levelCertain
			queue = append(queue, n)
		}
	}
	allDown := func(ns []int32) bool {
		for _, n := range ns {
			if n == outside || level[n] == levelUp {
				return false
			}
		}
		return true
	}
	type step struct{ edge, to int32 }
	var undirected []step
	// take marks to as down unless a redundant type keeps a provider up.
	take := func(i, to int32, rule *stepRule, mark downLevel) {
		if rule.redundant && !allDown(providers[dependentType{to, g.edges[i].relType}]) {
			return
		}
		level[to] = mark
		queue = append(queue, to)
	}
	drain := func(mark downLevel) {
		for len(queue) > 0 {
			from := queue[0]
			queue = queue[1:]
			for _, i := range g.incidentTo(from) {
				e := &g.edges[i]
				rule := &steps[e.relType]
				to, heuristic, ok := e.step(from, rule)
				if !ok || level[to] != levelUp || !rule.follow {
					continue
				}
				if heuristic && mark == levelCertain {
					undirected = append(undirected, step{i, to})
					continue
				}
				take(i, to, rule, mark)
			}
		}
	}
	drain(levelCertain)
	for _, s := range undirected {
		if level[s.to] == levelUp {
			take(s.edge, s.to, &steps[g.edges[s.edge].relType], levelHeuristic)
		}
	}
	drain(levelHeuristic)
	return level
}

// redundancyOf classifies the direct dependents of the failed CI:
// single points of failure have no alternative provider (any non-redundant
// dependency, or a redundant type whose providers all failed); protected
// ones keep a working provider of a redundant type. They follow the edges
// of the failed CI in graph order.
func redundancyOf(g *ciGraph, failed int32, impacted []ciHit, rules map[string]impactRule) (spof, protected []Node) {
	steps := g.stepRules(rules, impactQuery{})
	g.indexIncidence()
	seen := map[int32]bool{}
	for _, i := range g.incidentTo(failed) {
		e := &g.edges[i]
		rule := &steps[e.relType]
		to, heuristic, ok := e.step(failed, rule)
		if !ok || heuristic || seen[to] || !g.known[to] {
			continue
		}
		hit := &impacted[to]
		switch {
		case hit.impacted && !hit.heuristic:
			seen[to] = true
			spof = append(spof, g.nodes[to])
		case !hit.impacted && rule.redundant:
			seen[to] = true
			protected = append(protected, g.nodes[to])
		}
	}
	return spof, protected
}
