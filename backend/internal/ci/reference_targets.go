package ci

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// TargetDisplayNames is the optional name source of a registered reference
// target (WithReferenceTarget): the display names of the present targets
// among ids for include=references, in the caller's scope; an id that is
// not returned is missing for the caller. The ids of the result are lower
// case.
type TargetDisplayNames interface {
	ReferenceDisplayNames(ctx context.Context, orgID, target string, ids []string) (map[string]string, error)
}

// WithReferenceTarget hangs the existence check of a reference target that
// a module of its own provides (contract, WP-737) on the service, over the
// registry interface of WP-254 (fieldmeta.ReferenceLookup), and makes the
// target available (fieldmeta.RegisterReferenceTarget). When lookup also
// implements TargetDisplayNames, include=references names its targets.
// Without the PostgreSQL lookup of WithReferences (memory mode) the values
// keep the form check only, as for every other target.
func (s *Service) WithReferenceTarget(target string, lookup fieldmeta.ReferenceLookup) *Service {
	if s.targets == nil {
		s.targets = map[string]fieldmeta.ReferenceLookup{}
	}
	s.targets[target] = lookup
	fieldmeta.RegisterReferenceTarget(target)
	return s
}

// referenceLookup is the existence check of the write path: the lookup of
// WithReferences with the registered targets; nil in memory mode.
func (s *Service) referenceLookup() fieldmeta.ReferenceLookup {
	if s.references == nil || len(s.targets) == 0 {
		return s.references
	}
	return &routedReferences{base: s.references, targets: s.targets}
}

// referenceNamer is the name source of include=references, nil when the
// lookup of WithReferences resolves no names.
func (s *Service) referenceNamer() ReferenceNamer {
	base, ok := s.references.(ReferenceNamer)
	if !ok {
		return nil
	}
	if len(s.targets) == 0 {
		return base
	}
	return &routedReferences{base: s.references, namer: base, targets: s.targets}
}

// routedReferences sends a registered target to its own lookup and every
// other target to the base lookup.
type routedReferences struct {
	base    fieldmeta.ReferenceLookup
	namer   ReferenceNamer
	targets map[string]fieldmeta.ReferenceLookup
}

// LookupReferences implements fieldmeta.ReferenceLookup.
func (r *routedReferences) LookupReferences(ctx context.Context, orgID, target string, ids []string) (map[string]string, error) {
	if lookup, ok := r.targets[target]; ok {
		return lookup.LookupReferences(ctx, orgID, target, ids)
	}
	return r.base.LookupReferences(ctx, orgID, target, ids)
}

// ReferenceNames implements ReferenceNamer.
func (r *routedReferences) ReferenceNames(ctx context.Context, orgID, target string, ids []string) (map[string]ReferenceName, error) {
	lookup, ok := r.targets[target]
	if !ok {
		return r.namer.ReferenceNames(ctx, orgID, target, ids)
	}
	names, ok := lookup.(TargetDisplayNames)
	if !ok {
		return nil, fmt.Errorf("reference target %s has no names", target)
	}
	found, err := names.ReferenceDisplayNames(ctx, orgID, target, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ReferenceName, len(found))
	for id, name := range found {
		out[id] = ReferenceName{DisplayName: name}
	}
	return out, nil
}

// MergedTargets implements ReferenceNamer; only CIs are merged.
func (r *routedReferences) MergedTargets(ctx context.Context, orgID string, ids []string) (map[string]string, error) {
	return r.namer.MergedTargets(ctx, orgID, ids)
}
