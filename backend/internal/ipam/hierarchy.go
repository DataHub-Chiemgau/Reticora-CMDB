package ipam

import (
	"net/netip"
	"sort"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// errPoolWithAddresses refuses a subnet with addresses of its own as a pool
// (409 is_pool, IPM-02).
var errPoolWithAddresses = &api.ConflictError{Field: "is_pool", Message: "a subnet with addresses cannot become a pool"}

// overlapConflict refuses a subnet of a strict section that overlaps
// another subnet (409 cidr, IPM-02): the address space of one is used
// twice, by addresses of a parent that is no pool inside its child. A true
// nesting is no overlap. partner is named only when the caller sees it
// (empty otherwise).
func overlapConflict(partner string) error {
	msg := "the subnet overlaps addresses of another subnet of a strict section"
	if partner != "" {
		msg = "the subnet overlaps addresses of subnet " + partner + " of a strict section"
	}
	return &api.ConflictError{Field: "cidr", Message: msg}
}

// The subnet hierarchy (IPM-02, WP-901, E-210): a subnet's parent is the
// narrowest subnet of the same organization and VRF that contains it.
// PostgreSQL computes it in the write path (subnet_place, migration
// 000300); this file holds the same pure computation for the memory
// repository and the unit tests.

// Relation is how one prefix relates to another.
type Relation int

// The relations of two prefixes. Prefixes of the same family either nest
// or are disjoint: two aligned prefixes that share an address always
// contain one another, so "intersects without containing" does not occur
// (prefixes of different families are disjoint).
const (
	// RelationDisjoint: no common address.
	RelationDisjoint Relation = iota
	// RelationEqual: the same prefix.
	RelationEqual
	// RelationContains: a contains b and is larger.
	RelationContains
	// RelationWithin: a lies in b and is smaller.
	RelationWithin
)

// String names the relation.
func (r Relation) String() string {
	switch r {
	case RelationEqual:
		return "equal"
	case RelationContains:
		return "contains"
	case RelationWithin:
		return "within"
	}
	return "disjoint"
}

// Classify relates prefix a to prefix b (both masked first).
func Classify(a, b netip.Prefix) Relation {
	a, b = a.Masked(), b.Masked()
	if !a.IsValid() || !b.IsValid() || a.Addr().Is4() != b.Addr().Is4() || !a.Overlaps(b) {
		return RelationDisjoint
	}
	switch {
	case a.Bits() == b.Bits():
		return RelationEqual
	case a.Bits() < b.Bits():
		return RelationContains
	}
	return RelationWithin
}

// Narrowest returns the index of the narrowest prefix among candidates that
// strictly contains p, and false when none does. Equal prefixes are no
// container.
func Narrowest(p netip.Prefix, candidates []netip.Prefix) (int, bool) {
	best, bits := -1, -1
	for i, c := range candidates {
		if Classify(c, p) == RelationContains && c.Bits() > bits {
			best, bits = i, c.Bits()
		}
	}
	return best, best >= 0
}

// placeSubnets recomputes the parent of every subnet of the organization
// in its VRF (memory repository): the narrowest containing subnet, or none.
func placeSubnets(subnets []*Subnet) {
	byVRF := map[string][]*Subnet{}
	for _, s := range subnets {
		byVRF[s.VRFID] = append(byVRF[s.VRFID], s)
	}
	for _, group := range byVRF {
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		prefixes := make([]netip.Prefix, len(group))
		for i, s := range group {
			prefixes[i], _ = netip.ParsePrefix(s.CIDR)
		}
		for _, s := range group {
			p, err := netip.ParsePrefix(s.CIDR)
			if err != nil {
				s.ParentID = ""
				continue
			}
			if i, ok := Narrowest(p, prefixes); ok {
				s.ParentID = group[i].ID
			} else {
				s.ParentID = ""
			}
		}
	}
}
