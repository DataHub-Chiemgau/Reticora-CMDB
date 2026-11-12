package topology

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// Analyzer runs the graph analyses of the configuration graph without an
// HTTP request (WP-1025, E-203): impact of several failed CIs with
// redundancy (IMP-04, IMP-05), upstream dependencies and the assignment of
// failed CIs to their root (ALR-03). Workers (correlation WP-1028,
// maintenance WP-1033 and WP-1034) call it with the organization of their
// job; the HTTP handler delegates to it.
//
// It reads through ci.Repository and relationship.Repository in the scope
// of the context and writes nothing: a worker under database.OrgWideScope
// sees every CI of the organization, a client- or site-restricted scope only
// its CIs (row level security). management_ip stays empty without a field
// policy in the context (fail-closed, nodeFromItem).
type Analyzer struct {
	ciRepo    ci.Repository
	relRepo   relationship.Repository
	traverser relationship.Traverser
	// walker walks with CI numbers (WP-611); the memory repository has
	// none, its walks go through traverser or hop by hop.
	walker relationship.Walker
	// types supplies the impact semantics of the organization's
	// relationship types; without it the built-in catalogue applies.
	types relationshiptype.Repository
	// ciTypes resolves which CI types are services (business_service,
	// application and their subtypes, IMP-08); without it the service
	// impact chain has no services.
	ciTypes citype.Repository
}

// NewAnalyzer creates an analyzer on the CI and relationship repositories.
// When the relationship repository also implements relationship.Walker or
// relationship.Traverser, graph walks run as one walk of the repository.
func NewAnalyzer(ciRepo ci.Repository, relRepo relationship.Repository) *Analyzer {
	a := &Analyzer{ciRepo: ciRepo, relRepo: relRepo}
	if traverser, ok := relRepo.(relationship.Traverser); ok {
		a.traverser = traverser
	}
	if walker, ok := relRepo.(relationship.Walker); ok {
		a.walker = walker
	}
	return a
}

// WithRelationshipTypes makes the analyses follow the impact_direction of
// the organization's relationship types, custom types included (IMP-01).
func (a *Analyzer) WithRelationshipTypes(types relationshiptype.Repository) *Analyzer {
	a.types = types
	return a
}

// WithCITypes lets the analyses name the affected services (IMP-08).
func (a *Analyzer) WithCITypes(types citype.Repository) *Analyzer {
	a.ciTypes = types
	return a
}

// ErrNotFound reports a CI the caller does not see (unknown, deleted, of
// another organization, outside the scope or no UUID); it is ErrCINotFound
// and wraps api.ErrNotFound.
var ErrNotFound = ErrCINotFound

// NotFoundError names the first CI of an analysis the caller does not see.
type NotFoundError struct{ ID string }

func (e *NotFoundError) Error() string { return "ci not found: " + e.ID }

// Unwrap makes errors.Is(err, ErrNotFound) hold.
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// ImpactOptions narrow an impact analysis like the query of GET /impact:
// Depth (0: the maximum of 20 hops), Categories (empty: all), RelType
// (empty: all), Paths builds the path of each impacted CI, ServiceChain
// adds the affected services, clients and sites (IMP-08).
type ImpactOptions struct {
	Depth        int
	Categories   []string
	RelType      string
	Paths        bool
	ServiceChain bool
}

// Provider is a provider of a dependent over a redundant type.
type Provider struct {
	CIID    string `json:"ci_id"`
	RelType string `json:"rel_type"`
}

// ProtectedCI is a CI a redundant type keeps up although one of its
// providers failed, with the providers that have not failed (IMP-04). A
// provider outside the caller's scope keeps the CI up without being named.
type ProtectedCI struct {
	Node
	Providers []Provider `json:"providers"`
}

// ImpactResult is the impact of several failed CIs (IMP-05).
type ImpactResult struct {
	// Failed are the failed CIs in the order of the request.
	Failed []Node
	// Impacted are the impacted CIs with heuristic, hops, via and, with
	// ImpactOptions.Paths, their path: certain ones first, then by name.
	Impacted []ImpactNode
	Summary  ImpactSummary
	// Services, ClientIDs and SiteIDs are the service chain (only with
	// ImpactOptions.ServiceChain).
	Services  []BlastRadiusCI
	ClientIDs []string
	SiteIDs   []string
	// Protected are the dependents a redundant type keeps up.
	Protected []ProtectedCI
	// Truncated reports a walk the node limit cut (NFR-01).
	Truncated bool
}

// analysis is one impact analysis: the failed CIs, the graph of one walk
// from them with the redundant feeds, and the impact on it.
type analysis struct {
	failed   []Node
	g        *ciGraph
	roots    []int32
	rules    map[string]impactRule
	query    impactQuery
	impacted []ciHit
	order    []int32
}

// analyze reads the failed CIs ids, walks the graph from them once, adds
// the providers of redundant types beyond the walk (readFeeds) and computes
// the impact narrowed by q (the steps of GET /impact, WP-840). A CI the
// caller does not see is a *NotFoundError.
func (a *Analyzer) analyze(ctx context.Context, orgID string, ids []string, q impactQuery) (*analysis, error) {
	items, missing, err := a.readFailed(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	if missing != "" {
		return nil, &NotFoundError{ID: missing}
	}
	// The failed CIs as roots, each CI once.
	an := &analysis{failed: make([]Node, len(items)), query: q}
	var distinct []ci.Item
	seen := map[string]bool{}
	for i := range items {
		an.failed[i] = nodeFromItem(ctx, &items[i])
		if !seen[items[i].ID] {
			seen[items[i].ID] = true
			distinct = append(distinct, items[i])
		}
	}
	if an.g, an.roots, err = a.graphFromRoots(ctx, orgID, distinct, q.depth); err != nil {
		return nil, err
	}
	// The providers of a redundant type beyond the depth keep their
	// dependents up (IMP-04, F-160).
	an.rules = a.impactRules(ctx, orgID)
	if feedErr := a.readFeeds(ctx, orgID, an.g, an.roots, q.depth, an.rules); feedErr != nil {
		return nil, feedErr
	}
	an.impacted = impactOf(an.g, an.roots, q, an.rules)
	an.order = impactOrder(an.g, an.impacted)
	return an, nil
}

// impactNodes returns the impacted CIs in answer order and the summary of
// GET /impact; withPaths builds the path of each.
func (an *analysis) impactNodes(withPaths bool) ([]ImpactNode, ImpactSummary) {
	g, impacted := an.g, an.impacted
	nodes := make([]ImpactNode, 0, len(an.order))
	summary := ImpactSummary{Failed: len(an.failed), ByCategory: map[string]int{}}
	for _, n := range an.order {
		hit := &impacted[n]
		entry := ImpactNode{Node: g.nodes[n], Heuristic: hit.heuristic, Hops: hit.hops, Via: g.via(&hit.via)}
		if hit.relay != nil {
			relay := g.via(hit.relay)
			entry.Relay = &relay
		}
		if withPaths {
			entry.Path = g.path(impacted, n)
		}
		nodes = append(nodes, entry)
		if hit.heuristic {
			summary.Heuristic++
		} else {
			summary.Certain++
		}
		summary.ByCategory[hit.via.category]++
	}
	summary.Impacted = len(nodes)
	return nodes, summary
}

// Impact analyses the joint failure of the CIs failed (WP-1025 AK2): one
// read of the failed CIs, one walk for all of them, the redundancy feeds
// and the impact with the facets of opts. A CI the caller does not see is a
// *NotFoundError naming the first such id; any other error of the
// repository is returned as is. An empty set (after dropping empty ids)
// yields an empty result.
func (a *Analyzer) Impact(ctx context.Context, orgID string, failed []string, opts ImpactOptions) (*ImpactResult, error) {
	q := impactQuery{relType: opts.RelType, depth: opts.Depth}
	if q.depth <= 0 || q.depth > maxDepth {
		q.depth = maxDepth
	}
	if len(opts.Categories) > 0 {
		q.categories = map[string]bool{}
		for _, c := range opts.Categories {
			q.categories[c] = true
		}
	}
	ids := dedupe(failed)
	if len(ids) == 0 {
		// Nothing failed, nothing is impacted (a worker with an empty set).
		return &ImpactResult{Failed: []Node{}, Impacted: []ImpactNode{},
			Summary: ImpactSummary{ByCategory: map[string]int{}}, Protected: []ProtectedCI{}}, nil
	}
	an, err := a.analyze(ctx, orgID, ids, q)
	if err != nil {
		return nil, err
	}
	out := &ImpactResult{Failed: an.failed, Truncated: an.g.truncated}
	out.Impacted, out.Summary = an.impactNodes(opts.Paths)
	steps := an.g.stepRules(an.rules, q)
	if opts.ServiceChain {
		isService, err := a.serviceTypes(ctx, orgID)
		if err != nil {
			return nil, err
		}
		chain := serviceChainOf(an.g, an.roots, an.impacted, an.order, steps, isService, false)
		out.ClientIDs, out.SiteIDs = chain.clients, chain.sites
		for _, n := range chain.services {
			out.Services = append(out.Services, BlastRadiusCI{Node: an.g.nodes[n], Heuristic: an.impacted[n].heuristic})
		}
	}
	out.Protected = protectedOf(an.g, an.roots, an.impacted, steps)
	return out, nil
}

// dedupe drops empty and repeated ids, keeping the first order.
func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// providerRef is a provider of a dependent: a CI of the graph, or one
// beyond the walk named by its id (a hidden one has no id).
type providerRef struct {
	n         int32
	outsideID string
}

// protectedOf returns the dependents a redundant type keeps up although a
// provider of the type is down (failed or impacted), each with the
// providers of that type that are up (WP-1025 AK2). Dependents are ordered
// by name and id, providers by CI id and type.
func protectedOf(g *ciGraph, failed []int32, impacted []ciHit, steps []stepRule) []ProtectedCI {
	down := make([]bool, len(g.ids))
	for n := range impacted {
		down[n] = impacted[n].impacted
	}
	for _, n := range failed {
		down[n] = true
	}
	providers := map[dependentType][]providerRef{}
	for _, edges := range [2][]ciEdge{g.edges, g.feeds} {
		for i := range edges {
			e := &edges[i]
			rule := &steps[e.relType]
			if !rule.redundant || !rule.follow {
				continue
			}
			for _, from := range [2]int32{e.source, e.target} {
				to, _, ok := e.step(from, rule)
				if !ok || to == outside {
					continue
				}
				ref := providerRef{n: from}
				if from == outside {
					ref.outsideID = e.outsideID
				}
				key := dependentType{to, e.relType}
				providers[key] = append(providers[key], ref)
			}
		}
	}
	byDependent := map[int32][]Provider{}
	var dependents []int32
	for key, refs := range providers {
		if down[key.dependent] || !g.known[key.dependent] {
			continue
		}
		anyDown := false
		var up []Provider
		for _, ref := range refs {
			switch {
			case ref.n != outside && down[ref.n]:
				anyDown = true
			case ref.n != outside:
				up = append(up, Provider{CIID: g.ids[ref.n], RelType: g.relTypes[key.relType]})
			case ref.outsideID != "":
				up = append(up, Provider{CIID: ref.outsideID, RelType: g.relTypes[key.relType]})
			}
		}
		if !anyDown {
			continue
		}
		if _, ok := byDependent[key.dependent]; !ok {
			dependents = append(dependents, key.dependent)
			byDependent[key.dependent] = []Provider{}
		}
		byDependent[key.dependent] = append(byDependent[key.dependent], up...)
	}
	slices.SortFunc(dependents, func(i, j int32) int {
		return cmp.Or(strings.Compare(g.nodes[i].Name, g.nodes[j].Name), strings.Compare(g.ids[i], g.ids[j]))
	})
	out := make([]ProtectedCI, 0, len(dependents))
	for _, n := range dependents {
		ps := byDependent[n]
		slices.SortFunc(ps, func(x, y Provider) int {
			return cmp.Or(strings.Compare(x.CIID, y.CIID), strings.Compare(x.RelType, y.RelType))
		})
		out = append(out, ProtectedCI{Node: g.nodes[n], Providers: slices.Compact(ps)})
	}
	return out
}

// dependencies walks from the CI id up to depth hops and returns the CIs
// it depends on (upstream) or that depend on it (downstream) in the order
// of the graph (GET /cis/{id}/dependencies). A CI the caller does not see
// is a *NotFoundError.
func (a *Analyzer) dependencies(ctx context.Context, orgID, id string, depth int, upstream bool) ([]Node, error) {
	root, err := a.ciRepo.GetByID(ctx, orgID, id)
	if err != nil {
		if ciMissing(id, err) {
			return nil, &NotFoundError{ID: id}
		}
		return nil, err
	}
	g, err := a.graphFrom(ctx, orgID, root, depth)
	if err != nil {
		return nil, err
	}
	reached := dependenciesOf(g, g.stepRules(a.impactRules(ctx, orgID), impactQuery{}), upstream, depth)
	nodes := make([]Node, 0)
	for n := range g.ids {
		if reached[n] {
			nodes = append(nodes, g.nodes[n])
		}
	}
	return nodes, nil
}

// Upstream returns the CIs ciID depends on, up to depth hops (0: 20), with
// the semantics of GET /cis/{id}/dependencies?direction=upstream (WP-1025
// AK3).
func (a *Analyzer) Upstream(ctx context.Context, orgID, ciID string, depth int) ([]Node, error) {
	if depth <= 0 || depth > maxDepth {
		depth = maxDepth
	}
	return a.dependencies(ctx, orgID, ciID, depth, true)
}

// Roots assigns every failed CI to its root (WP-1025 AK4, ALR-03): the
// failed CI from whose failure it follows over certain impact paths with
// redundancy (certainDown); a CI that follows from no other failed CI is
// its own root. Heuristic (undirected) paths found no assignment. When two
// failed CIs follow from each other (a directed cycle), the smaller CI id
// is the root of both. A CI that follows only from several failed CIs
// together (all providers of a redundant type failed) takes the smallest
// root among the failed CIs its failure comes from. The keys and values are
// the ids of the CIs as read; the result does not depend on the order of
// failed, and the call walks the graph once for the whole set.
func (a *Analyzer) Roots(ctx context.Context, orgID string, failed []string) (map[string]string, error) {
	ids := dedupe(failed)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	slices.SortFunc(ids, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
	an, err := a.analyzeRoots(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	return an.assign(), nil
}

// rootAnalysis is the state of Roots over the failed CIs, sorted by id:
// roots[i] is the graph number of failed CI i.
type rootAnalysis struct {
	g         *ciGraph
	ids       []string
	roots     []int32
	index     map[int32]int
	steps     []stepRule
	providers map[dependentType][]int32
	// reach[i][j]: failed CI j goes down when failed CI i alone fails.
	reach [][]bool
	root  []int // -1 while unknown
	busy  []bool
}

func (a *Analyzer) analyzeRoots(ctx context.Context, orgID string, ids []string) (*rootAnalysis, error) {
	items, missing, err := a.readFailed(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	if missing != "" {
		return nil, &NotFoundError{ID: missing}
	}
	var distinct []ci.Item
	seen := map[string]bool{}
	for i := range items {
		if !seen[items[i].ID] {
			seen[items[i].ID] = true
			distinct = append(distinct, items[i])
		}
	}
	slices.SortFunc(distinct, func(x, y ci.Item) int { return strings.Compare(x.ID, y.ID) })
	g, roots, err := a.graphFromRoots(ctx, orgID, distinct, maxDepth)
	if err != nil {
		return nil, err
	}
	rules := a.impactRules(ctx, orgID)
	if feedErr := a.readFeeds(ctx, orgID, g, roots, maxDepth, rules); feedErr != nil {
		return nil, feedErr
	}
	ra := &rootAnalysis{g: g, roots: roots, index: make(map[int32]int, len(roots)), steps: g.stepRules(rules, impactQuery{})}
	for i := range distinct {
		ra.ids = append(ra.ids, distinct[i].ID)
		ra.index[roots[i]] = i
	}
	ra.providers = redundantProviders(g, ra.steps)
	ra.reach = make([][]bool, len(roots))
	for i, r := range roots {
		level := certainDown(g, []int32{r}, ra.steps, ra.providers)
		ra.reach[i] = make([]bool, len(roots))
		for j, s := range roots {
			ra.reach[i][j] = level[s] == levelCertain
		}
	}
	return ra, nil
}

// assign resolves the root of every failed CI, in id order.
func (ra *rootAnalysis) assign() map[string]string {
	k := len(ra.roots)
	ra.root, ra.busy = make([]int, k), make([]bool, k)
	for i := range ra.root {
		ra.root[i] = -1
	}
	out := make(map[string]string, k)
	for i := range k {
		out[ra.ids[i]] = ra.ids[ra.rootOf(i)]
	}
	return out
}

// rootOf returns the root of failed CI f (an index): the smallest root of
// the failed CIs it follows from outside its own cycle, else of the failed
// CIs a joint failure comes from, else the smallest CI of its cycle.
func (ra *rootAnalysis) rootOf(f int) int {
	if ra.root[f] >= 0 {
		return ra.root[f]
	}
	ra.busy[f] = true
	defer func() { ra.busy[f] = false }()
	group := []int{f}
	var explainers []int
	for r := range ra.roots {
		if r == f || !ra.reach[r][f] {
			continue
		}
		if ra.reach[f][r] {
			group = append(group, r)
		} else {
			explainers = append(explainers, r)
		}
	}
	best := -1
	pick := func(c int) {
		if best < 0 || c < best {
			best = c
		}
	}
	for _, r := range explainers {
		if !ra.busy[r] {
			pick(ra.rootOf(r))
		}
	}
	if best < 0 {
		for _, r := range ra.jointSources(f, group) {
			if !ra.busy[r] {
				pick(ra.rootOf(r))
			}
		}
	}
	if best < 0 {
		best = slices.Min(group)
	}
	// The members of one cycle share the root.
	if len(explainers) == 0 {
		for _, r := range group {
			ra.root[r] = best
		}
	}
	ra.root[f] = best
	return best
}

// jointSources returns the failed CIs (outside group) a failure of f comes
// from when only the failed CIs together explain it: f goes down when every
// failed CI but those of group fails, and the failed CIs found walking
// back from f over the steps of that closure are its sources. Nil when f
// does not follow from the others.
func (ra *rootAnalysis) jointSources(f int, group []int) []int {
	g := ra.g
	target := ra.roots[f]
	// Quick check: a provider of f must go down at all.
	others := make([]int32, 0, len(ra.roots))
	for i, n := range ra.roots {
		if !slices.Contains(group, i) {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return nil
	}
	level := certainDown(g, others, ra.steps, ra.providers)
	if level[target] != levelCertain {
		return nil
	}
	// Walk back from f over directed steps whose provider is down in the
	// closure, up to the failed CIs.
	seen := make([]bool, len(g.ids))
	seen[target] = true
	queue := []int32{target}
	var sources []int
	for head := 0; head < len(queue); head++ {
		to := queue[head]
		for _, i := range g.incidentTo(to) {
			e := &g.edges[i]
			rule := &ra.steps[e.relType]
			from := e.source
			if from == to {
				from = e.target
			}
			if seen[from] || level[from] != levelCertain {
				continue
			}
			if next, heuristic, ok := e.step(from, rule); !ok || heuristic || next != to {
				continue
			}
			seen[from] = true
			if j, failed := ra.index[from]; failed && !slices.Contains(group, j) {
				sources = append(sources, j)
				continue
			}
			queue = append(queue, from)
		}
	}
	slices.Sort(sources)
	return sources
}
