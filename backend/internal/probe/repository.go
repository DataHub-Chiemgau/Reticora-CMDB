package probe

import (
	"context"
	"errors"
)

// ErrNotFound is returned for a probe that does not exist or is not
// visible to the principal (RLS); the two are not told apart.
var ErrNotFound = errors.New("probe not found")

// ListFilter narrows List; empty fields do not filter. Limit 0 means no
// limit.
type ListFilter struct {
	CIID             string
	Type             string
	CollectorGroupID string
	ClientID         string
	Enabled          *bool
	Limit            int
	Offset           int
}

// Resolved is a probe with its effective values. Epoch is the epoch of the
// probe's assignment (ResolveForCollector); 0 without one.
type Resolved struct {
	Probe     Probe         `json:"probe"`
	Effective EffectiveSpec `json:"effective"`
	Epoch     int64         `json:"epoch,omitempty"`
}

// Repository reads probe definitions and the override levels in the
// principal's tenant scope (the scope in the context,
// database.ContextWithTenantScope); every read runs in one tenant
// transaction under RLS.
type Repository interface {
	// Get returns the probe (ErrNotFound when missing or invisible).
	Get(ctx context.Context, id string) (*Probe, error)
	// List returns the probes matching f (nil: all), ordered by id.
	List(ctx context.Context, f *ListFilter) ([]Probe, error)
	// Layers returns the override levels of the probe for its type.
	Layers(ctx context.Context, p *Probe) (Layers, error)
	// ResolveForCollector resolves every probe assigned to the collector
	// (probe_assignment) with a fixed number of statements, independent of
	// the number of probes (WP-937 AK5).
	ResolveForCollector(ctx context.Context, collectorID string) ([]Resolved, error)
	// ResolveForGroup resolves every probe of the collector group the same
	// way.
	ResolveForGroup(ctx context.Context, groupID string) ([]Resolved, error)
}

// layerIndex holds the override sets of a batch by level and target, so
// that the layers of every probe are assembled without further reads.
type layerIndex struct {
	// org, client, group and ci profiles by probe type and target id.
	org    map[string]Overrides
	client map[[2]string]Overrides
	group  map[[2]string]Overrides
	ci     map[[2]string]Overrides
	// groups are the monitoring groups of each CI.
	groups map[string][]memberGroup
	// planMin is min_probe_interval_s of the organization (WP-940 AK4).
	planMin int64
}

// memberGroup is a monitoring group of a CI.
type memberGroup struct {
	id, name string
	priority int
}

func newLayerIndex() *layerIndex {
	return &layerIndex{
		org:    map[string]Overrides{},
		client: map[[2]string]Overrides{},
		group:  map[[2]string]Overrides{},
		ci:     map[[2]string]Overrides{},
		groups: map[string][]memberGroup{},
	}
}

// add files a profile.
func (x *layerIndex) add(p *MonitoringProfile) {
	o := ParseOverrides(p.Params)
	switch p.Level {
	case LevelOrg:
		x.org[p.ProbeType] = o
	case LevelClient:
		x.client[[2]string{p.ProbeType, p.ClientID}] = o
	case LevelGroup:
		x.group[[2]string{p.ProbeType, p.GroupID}] = o
	case LevelCI:
		x.ci[[2]string{p.ProbeType, p.CIID}] = o
	}
}

// layers assembles the levels of probe p.
func (x *layerIndex) layers(p *Probe) Layers {
	l := Layers{Org: x.org[p.Type], PlanMinIntervalS: x.planMin}
	if p.ClientID != "" {
		l.Client = x.client[[2]string{p.Type, p.ClientID}]
	}
	if p.CIID != "" {
		l.CI = x.ci[[2]string{p.Type, p.CIID}]
		for _, g := range x.groups[p.CIID] {
			if o, ok := x.group[[2]string{p.Type, g.id}]; ok {
				l.Groups = append(l.Groups, GroupOverrides{GroupID: g.id, Name: g.name, Priority: g.priority, Overrides: o})
			}
		}
	}
	return l
}

// resolveAll resolves the probes over the index.
func (x *layerIndex) resolveAll(probes []Probe, epochs map[string]int64) []Resolved {
	out := make([]Resolved, 0, len(probes))
	for i := range probes {
		p := &probes[i]
		out = append(out, Resolved{Probe: *p, Effective: Resolve(p, x.layers(p)), Epoch: epochs[p.ID]})
	}
	return out
}
