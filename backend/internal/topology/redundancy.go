package topology

// Redundancy postfilter of the impact (IMP-04): over a type with
// redundant = true (powered_by, member_of_cluster) a dependent fails only
// when every provider of that type failed; over any other type (hosted_on)
// one failed provider suffices.

// providerKey identifies the providers of one dependent over one type.
type providerKey struct{ dependent, relType string }

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

// incidence lists the edges of each CI in graph order, so a step only looks
// at the edges of the CI it leaves (NFR-02).
func incidence(edges []Edge) map[string][]int {
	incident := make(map[string][]int, len(edges))
	for i := range edges {
		incident[edges[i].SourceCIID] = append(incident[edges[i].SourceCIID], i)
		if edges[i].TargetCIID != edges[i].SourceCIID {
			incident[edges[i].TargetCIID] = append(incident[edges[i].TargetCIID], i)
		}
	}
	return incident
}

// redundantProviders lists the providers of each dependent per redundant
// type, over every edge of the graph: a provider outside the analysis
// keeps the dependent up.
func redundantProviders(edges []Edge, rules map[string]impactRule) map[providerKey][]string {
	providers := map[providerKey][]string{}
	for i := range edges {
		rule := ruleOf(rules, edges[i].RelType)
		if !rule.redundant {
			continue
		}
		for _, from := range []string{edges[i].SourceCIID, edges[i].TargetCIID} {
			if to, _, ok := impactStep(&edges[i], from, rule); ok {
				key := providerKey{to, edges[i].RelType}
				providers[key] = append(providers[key], from)
			}
		}
	}
	return providers
}

// closeDown is the downward closure of computeImpact; a variable, so a test
// counts the closures of one analysis.
var closeDown = downClosure

// downClosure returns which CIs are down when the failed CIs fail, and how:
// the failed CIs and, until nothing changes, every dependent whose failure a
// followed step implies (all providers down for a redundant type, one for
// the others). A worklist reaches the same fixpoint as repeated scans: a CI
// only goes down through an edge of a CI that just went down (NFR-02). The
// first pass takes the directed steps only (levelCertain) and keeps the
// undirected ones aside; the second retries those and continues from that
// fixpoint over all steps (levelHeuristic). Every step the first pass did
// not take is tried again in the second (a redundant one when its last
// provider goes down), so one closure yields both the closure without and
// the one with undirected steps.
func downClosure(failed map[string]bool, edges []Edge, incident map[string][]int, rules map[string]impactRule,
	follow func(*Edge, impactRule) bool) map[string]downLevel {
	providers := redundantProviders(edges, rules)
	level := make(map[string]downLevel, len(failed))
	queue := make([]string, 0, len(failed))
	for id := range failed {
		level[id] = levelCertain
		queue = append(queue, id)
	}
	allDown := func(ids []string) bool {
		for _, id := range ids {
			if level[id] == levelUp {
				return false
			}
		}
		return true
	}
	type step struct {
		edge int
		to   string
	}
	var undirected []step
	// take marks to as down unless a redundant type keeps a provider up.
	take := func(i int, to string, rule impactRule, mark downLevel) {
		if rule.redundant && !allDown(providers[providerKey{to, edges[i].RelType}]) {
			return
		}
		level[to] = mark
		queue = append(queue, to)
	}
	drain := func(mark downLevel) {
		for len(queue) > 0 {
			from := queue[0]
			queue = queue[1:]
			for _, i := range incident[from] {
				rule := ruleOf(rules, edges[i].RelType)
				to, heuristic, ok := impactStep(&edges[i], from, rule)
				if !ok || level[to] != levelUp || !follow(&edges[i], rule) {
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
			take(s.edge, s.to, ruleOf(rules, edges[s.edge].RelType), levelHeuristic)
		}
	}
	drain(levelHeuristic)
	return level
}

// redundancyFindings classifies the direct dependents of the failed CI:
// single points of failure have no alternative provider (any non-redundant
// dependency, or a redundant type whose providers all failed); protected
// ones keep a working provider of a redundant type.
func redundancyFindings(failedID string, graph Graph, impacted map[string]impactHit, rules map[string]impactRule) (spof, protected []Node) {
	byID := make(map[string]Node, len(graph.Nodes))
	for _, n := range graph.Nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{}
	for i := range graph.Edges {
		rule := ruleOf(rules, graph.Edges[i].RelType)
		to, heuristic, ok := impactStep(&graph.Edges[i], failedID, rule)
		if !ok || heuristic || seen[to] {
			continue
		}
		node, known := byID[to]
		if !known {
			continue
		}
		hit, down := impacted[to]
		switch {
		case down && !hit.heuristic:
			seen[to] = true
			spof = append(spof, node)
		case !down && rule.redundant:
			seen[to] = true
			protected = append(protected, node)
		}
	}
	return spof, protected
}
