package topology

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
)

// Service impact chain (IMP-08, WP-297): the services a failure reaches,
// their clients and sites, the blast radius as counts and the single points
// of failure of the chain, the CIs whose failure alone takes down a
// service.

// serviceTypeKeys are the CI types whose CIs are services; a type that
// inherits from one of them over parent_type_id (MET-06) is a service as
// well.
var serviceTypeKeys = []string{"business_service", "application"}

// maxCITypes bounds the CI types read for one analysis.
const maxCITypes = 1000

// maxSPOFCandidates bounds the CIs whose single failure one analysis
// simulates; beyond it spof_truncated is set. A variable, so a test lowers
// it.
var maxSPOFCandidates = 500

// AffectedService is a service the failure reaches (IMP-08); Heuristic
// marks one reached only over an undirected network edge (IMP-03).
type AffectedService struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CIType    string `json:"ci_type"`
	Heuristic bool   `json:"heuristic"`
}

// ServiceSPOF is a single point of failure of the service chain (IMP-08):
// a failed or certainly affected CI whose failure alone takes down the
// certainly affected services ServiceIDs over directed steps.
type ServiceSPOF struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	CIType     string   `json:"ci_type"`
	ServiceIDs []string `json:"service_ids"`
}

// BlastCounts sums up the blast radius (IMP-08): CIs counts the certainly
// affected CIs; each other count is set when its block was computed.
// Services and ServicesHeuristic split the services like affected_count and
// heuristic_count. With truncated the counts are lower bounds (E-17).
type BlastCounts struct {
	CIs               int  `json:"cis"`
	Services          *int `json:"services,omitempty"`
	ServicesHeuristic *int `json:"services_heuristic,omitempty"`
	Clients           *int `json:"clients,omitempty"`
	Sites             *int `json:"sites,omitempty"`
	SPOF              *int `json:"spof,omitempty"`
}

// impactIncludes are the blocks include= adds to /impact (IMP-08).
var impactIncludes = []string{"services", "clients", "sites", "spof"}

// impactInclude is the parsed include= of an impact analysis.
type impactInclude struct {
	services, clients, sites, spof bool
}

// asked reports whether a block was asked for.
func (in impactInclude) asked() bool { return in.services || in.clients || in.sites || in.spof }

// parseImpactInclude reads include=services,clients,sites,spof (IMP-08); an
// unknown value is 422 on include.
func parseImpactInclude(raw string) (impactInclude, error) {
	var in impactInclude
	for _, name := range strings.Split(raw, ",") {
		switch name = strings.TrimSpace(name); name {
		case "":
		case "services":
			in.services = true
		case "clients":
			in.clients = true
		case "sites":
			in.sites = true
		case "spof":
			in.spof = true
		default:
			return impactInclude{}, &api.FieldError{Field: "include", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown include %q; allowed: %s", name, strings.Join(impactIncludes, ", "))}
		}
	}
	return in, nil
}

// serviceTypes returns the ids of the CI types of the organization that are
// services: business_service, application and every type inheriting from
// one of them (WP-248), deactivated types included. One read of the
// types per analysis; nil without a CI type repository.
func (h *Handler) serviceTypes(ctx context.Context, orgID string) (map[string]bool, error) {
	if h.ciTypes == nil {
		return nil, nil
	}
	types, _, err := h.ciTypes.List(ctx, orgID, citype.FilterParams{IncludeInactive: true},
		api.PaginationParams{Limit: maxCITypes})
	if err != nil {
		return nil, err
	}
	return serviceTypeIDs(types), nil
}

// serviceTypeIDs picks the service types among types by key along the
// type chain; a chain is followed at most len(types) steps, so a cycle
// ends.
func serviceTypeIDs(types []citype.Type) map[string]bool {
	byID := make(map[string]*citype.Type, len(types))
	for i := range types {
		byID[types[i].ID] = &types[i]
	}
	out := map[string]bool{}
	for i := range types {
		t := &types[i]
		for steps := 0; t != nil && steps <= len(types); steps++ {
			if slices.Contains(serviceTypeKeys, t.Key) {
				out[types[i].ID] = true
				break
			}
			t = byID[t.ParentTypeID]
		}
	}
	return out
}

// serviceChain is the service impact chain of one analysis.
type serviceChain struct {
	// services are the affected services in the order of impactOrder,
	// certain ones first.
	services []int32
	// certain counts the affected CIs, certainServices the services
	// among them.
	certain, certainServices int
	// clients and sites of the certainly affected CIs, sorted (WP-490).
	clients, sites []string
	spofs          []ServiceSPOF
	spofTruncated  bool
}

// serviceChainOf collects the service chain of the failure of failed from
// the impacted CIs (impactOf) in order (impactOrder); isService holds the
// service types (serviceTypes). The SPOF are computed only with withSPOF.
func serviceChainOf(g *ciGraph, failed []int32, impacted []ciHit, order []int32, steps []stepRule,
	isService map[string]bool, withSPOF bool) serviceChain {
	chain := serviceChain{services: []int32{}, clients: []string{}, sites: []string{}, spofs: []ServiceSPOF{}}
	clients := map[string]bool{}
	sites := map[string]bool{}
	var certainServices []int32
	for _, n := range order {
		node := &g.nodes[n]
		heuristic := impacted[n].heuristic
		if isService[node.CIType] {
			chain.services = append(chain.services, n)
			if !heuristic {
				certainServices = append(certainServices, n)
			}
		}
		if heuristic {
			continue
		}
		chain.certain++
		if node.ClientID != "" {
			clients[node.ClientID] = true
		}
		if node.SiteID != "" {
			sites[node.SiteID] = true
		}
	}
	chain.certainServices = len(certainServices)
	chain.clients = append(chain.clients, sortedKeys(clients)...)
	chain.sites = append(chain.sites, sortedKeys(sites)...)
	if withSPOF {
		chain.spofs, chain.spofTruncated = serviceSPOFs(g, failed, impacted, certainServices, steps)
	}
	return chain
}

// serviceSPOFs returns the single points of failure of the certainly
// affected services (IMP-08): the failed and certainly affected CIs whose
// failure alone takes down at least one of services other than
// themselves over directed steps. A service is a SPOF only for another
// service; a heuristic service has none.
//
// The candidates are the failed and certainly affected CIs from which a
// service is reached over a followed directed step: a run backwards from
// the services over incidentTo, which covers the via chains of path and
// every other provider of a service, such as a second application on the
// same database. The closest to the failure come first (failed, then by
// hops, name and id); beyond maxSPOFCandidates the rest is dropped and
// truncated is set. Each candidate fails alone, the other failed CIs stay
// up (IMP-05), in a closure over the directed steps with the steps of the
// analysis (categories, rel_type) and the redundant providers computed
// once (certainDown).
func serviceSPOFs(g *ciGraph, failed []int32, impacted []ciHit, services []int32, steps []stepRule) (spofs []ServiceSPOF, truncated bool) {
	spofs = []ServiceSPOF{}
	if len(services) == 0 {
		return spofs, false
	}
	g.indexIncidence()
	isFailed := make([]bool, len(g.ids))
	for _, n := range failed {
		isFailed[n] = true
	}
	down := func(n int32) bool { return isFailed[n] || (impacted[n].impacted && !impacted[n].heuristic) }
	candidate := make([]bool, len(g.ids))
	queue := make([]int32, 0, len(services))
	for _, n := range services {
		if !candidate[n] {
			candidate[n] = true
			queue = append(queue, n)
		}
	}
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		for _, i := range g.incidentTo(current) {
			e := &g.edges[i]
			rule := &steps[e.relType]
			provider := e.source
			if provider == current {
				provider = e.target
			}
			if !rule.follow || candidate[provider] || !g.known[provider] || !down(provider) {
				continue
			}
			if to, heuristic, ok := e.step(provider, rule); !ok || heuristic || to != current {
				continue
			}
			candidate[provider] = true
			queue = append(queue, provider)
		}
	}
	hops := func(n int32) int {
		if isFailed[n] {
			return 0
		}
		return impacted[n].hops
	}
	slices.SortStableFunc(queue, func(a, b int32) int {
		if c := hops(a) - hops(b); c != 0 {
			return c
		}
		if c := strings.Compare(g.nodes[a].Name, g.nodes[b].Name); c != 0 {
			return c
		}
		return strings.Compare(g.ids[a], g.ids[b])
	})
	if len(queue) > maxSPOFCandidates {
		queue, truncated = queue[:maxSPOFCandidates], true
	}
	providers := redundantProviders(g, steps)
	for _, c := range queue {
		level := certainDown(g, []int32{c}, steps, providers)
		var ids []string
		for _, s := range services {
			if s != c && level[s] == levelCertain {
				ids = append(ids, g.ids[s])
			}
		}
		if len(ids) > 0 {
			node := &g.nodes[c]
			spofs = append(spofs, ServiceSPOF{ID: node.ID, Name: node.Name, CIType: node.CIType, ServiceIDs: ids})
		}
	}
	return spofs, truncated
}

// counts returns the blast radius of the chain with the counts of the
// blocks in asked.
func (c *serviceChain) counts(asked impactInclude, heuristicServices int) BlastCounts {
	count := func(n int) *int { return &n }
	out := BlastCounts{CIs: c.certain}
	if asked.services {
		out.Services, out.ServicesHeuristic = count(c.certainServices), count(heuristicServices)
	}
	if asked.clients {
		out.Clients = count(len(c.clients))
	}
	if asked.sites {
		out.Sites = count(len(c.sites))
	}
	if asked.spof {
		out.SPOF = count(len(c.spofs))
	}
	return out
}

// addServiceChain adds the blocks of include= to the body of an impact
// answer (IMP-08): affected_services, client_ids, site_ids, service_spofs
// with spof_truncated, and blast_radius with the counts of the blocks
// asked for. The CI types are read only for services or spof.
func (h *Handler) addServiceChain(ctx context.Context, orgID string, body map[string]any, g *ciGraph, failed []int32,
	impacted []ciHit, order []int32, q impactQuery, rules map[string]impactRule, in impactInclude) error {
	var isService map[string]bool
	if in.services || in.spof {
		var err error
		if isService, err = h.serviceTypes(ctx, orgID); err != nil {
			return err
		}
	}
	chain := serviceChainOf(g, failed, impacted, order, g.stepRules(rules, q), isService, in.spof)
	if in.services {
		services := make([]AffectedService, len(chain.services))
		for i, n := range chain.services {
			node := &g.nodes[n]
			services[i] = AffectedService{ID: node.ID, Name: node.Name, CIType: node.CIType, Heuristic: impacted[n].heuristic}
		}
		body["affected_services"] = services
	}
	if in.clients {
		body["client_ids"] = chain.clients
	}
	if in.sites {
		body["site_ids"] = chain.sites
	}
	if in.spof {
		body["service_spofs"] = chain.spofs
		body["spof_truncated"] = chain.spofTruncated
	}
	body["blast_radius"] = chain.counts(in, len(chain.services)-chain.certainServices)
	return nil
}
