package ipam

import (
	"net/netip"
	"sort"
	"time"

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

// LongestPrefix returns the index of the narrowest prefix among candidates
// that contains addr (longest prefix match, IPM-14), and false when none
// does. The PostgreSQL repository computes it in ipam_longest_prefix_subnet
// (migration 000322).
func LongestPrefix(addr netip.Addr, candidates []netip.Prefix) (int, bool) {
	best, bits := -1, -1
	for i, c := range candidates {
		if c.IsValid() && c.Contains(addr) && c.Bits() > bits {
			best, bits = i, c.Bits()
		}
	}
	return best, best >= 0
}

// longestPrefixLocked is the subnet of the organization and VRF that is no
// pool, is not except and holds addr with the longest prefix, or "" (the
// memory repository's ipam_longest_prefix_subnet; the caller holds r.mu).
func (r *MemoryRepository) longestPrefixLocked(orgID, vrfID, address, except string) string {
	addr, err := netip.ParseAddr(hostOf(address))
	if err != nil {
		return ""
	}
	var ids []string
	var prefixes []netip.Prefix
	for _, s := range r.subnets {
		if s.OrganizationID != orgID || s.VRFID != vrfID || s.IsPool || s.ID == except {
			continue
		}
		p, err := netip.ParsePrefix(s.CIDR)
		if err != nil {
			continue
		}
		ids, prefixes = append(ids, s.ID), append(prefixes, p.Masked())
	}
	// Equal lengths cannot both contain the address in one VRF (unique
	// CIDR); the order of ids does not matter.
	if i, ok := LongestPrefix(addr, prefixes); ok {
		return ids[i]
	}
	return ""
}

// containerLocked is the narrowest subnet without pool of the
// organization and VRF of s whose prefix holds the prefix of s, or "": the
// longest prefix of the addresses inside s without s, also when a pool lies
// between (the caller holds r.mu).
func (r *MemoryRepository) containerLocked(s *Subnet) string {
	p, err := netip.ParsePrefix(s.CIDR)
	if err != nil {
		return ""
	}
	p = p.Masked()
	best, bestBits := "", -1
	for _, o := range r.subnets {
		if o.OrganizationID != s.OrganizationID || o.VRFID != s.VRFID || o.IsPool || o.ID == s.ID {
			continue
		}
		q, err := netip.ParsePrefix(o.CIDR)
		if err != nil {
			continue
		}
		q = q.Masked()
		if q.Bits() < p.Bits() && q.Contains(p.Addr()) && q.Bits() > bestBits {
			best, bestBits = o.ID, q.Bits()
		}
	}
	return best
}

// takeOverAddressesLocked gives the addresses of the VRF inside subnet s
// that belong to the narrowest subnet without pool around it or to no
// subnet their longest prefix (the takeover of subnet_place, IPM-14); a
// pool takes none, gateway marks stay.
func (r *MemoryRepository) takeOverAddressesLocked(s *Subnet) {
	if s.IsPool {
		return
	}
	p, err := netip.ParsePrefix(s.CIDR)
	if err != nil {
		return
	}
	container := r.containerLocked(s)
	for _, a := range r.addresses {
		if a.OrganizationID != s.OrganizationID || a.VRFID != s.VRFID || a.IsGateway ||
			(a.SubnetID != "" && a.SubnetID != container) {
			continue
		}
		addr, err := netip.ParseAddr(hostOf(a.Address))
		if err != nil || !p.Masked().Contains(addr) {
			continue
		}
		if id := r.longestPrefixLocked(s.OrganizationID, s.VRFID, a.Address, ""); id != "" && id != a.SubnetID {
			a.SubnetID, a.UpdatedAt = id, time.Now().UTC()
		}
	}
}
