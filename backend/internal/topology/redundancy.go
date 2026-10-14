package topology

// Redundancy postfilter of the impact (IMP-04): over a type with
// redundant = true (powered_by, member_of_cluster) a dependent fails only
// when every provider of that type failed; over any other type (hosted_on)
// one failed provider suffices.

// stepFilter decides whether an impact step over an edge is followed.
type stepFilter func(e *Edge, rule impactRule, heuristic bool) bool

// providerKey identifies the providers of one dependent over one type.
type providerKey struct{ dependent, relType string }

// downSet returns the CIs that are down when the failed CIs fail: the
// failed CIs and, until nothing changes, every dependent whose failure a
// followed step implies (all providers down for a redundant type, one for
// the others).
func downSet(failed map[string]bool, edges []Edge, rules map[string]impactRule, follow stepFilter) map[string]bool {
	// The providers of each dependent per redundant type, over every edge
	// of the graph: a provider outside the analysis keeps the dependent up.
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
	incident := make(map[string][]int, len(edges))
	for i := range edges {
		incident[edges[i].SourceCIID] = append(incident[edges[i].SourceCIID], i)
		if edges[i].TargetCIID != edges[i].SourceCIID {
			incident[edges[i].TargetCIID] = append(incident[edges[i].TargetCIID], i)
		}
	}
	// A worklist reaches the same fixpoint as repeated scans: a CI only
	// goes down through an edge of a CI that just went down (NFR-02).
	down := map[string]bool{}
	queue := make([]string, 0, len(failed))
	for id := range failed {
		down[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, i := range incident[from] {
			rule := ruleOf(rules, edges[i].RelType)
			to, heuristic, ok := impactStep(&edges[i], from, rule)
			if !ok || down[to] || !follow(&edges[i], rule, heuristic) {
				continue
			}
			if rule.redundant && !allDown(providers[providerKey{to, edges[i].RelType}], down) {
				continue
			}
			down[to] = true
			queue = append(queue, to)
		}
	}
	return down
}

func allDown(ids []string, down map[string]bool) bool {
	for _, id := range ids {
		if !down[id] {
			return false
		}
	}
	return true
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
