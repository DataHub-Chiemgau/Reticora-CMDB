package topology

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

// redundantProviders lists the providers of each dependent per redundant
// type, over every edge of the graph: a provider outside the analysis
// keeps the dependent up.
func redundantProviders(g *ciGraph, steps []stepRule) map[dependentType][]int32 {
	providers := map[dependentType][]int32{}
	for i := range g.edges {
		e := &g.edges[i]
		rule := &steps[e.relType]
		if !rule.redundant {
			continue
		}
		for _, from := range [2]int32{e.source, e.target} {
			if to, _, ok := e.step(from, rule); ok {
				key := dependentType{to, e.relType}
				providers[key] = append(providers[key], from)
			}
		}
	}
	return providers
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
			if level[n] == levelUp {
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
