package ipam

import (
	"math/big"
	"net/http"
	"net/netip"
	"sort"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Usage and free space of a subnet (IPM-03, WP-903). The repositories
// deliver the occupancy (Occupancy): the addresses of the subnet and the
// prefixes of its direct children, all of them whatever the caller's scope
// (ipam_subnet_occupancy, migration 000301). The computation here never
// enumerates the address space: the occupied hosts are sorted numerically
// and the gaps between them walked, so its cost follows the number of
// occupied addresses and the requested limit, also for an IPv6 /64.

// OccupiedAddress is an address of a subnet: its host and its type and
// state (IPM-04).
type OccupiedAddress struct {
	Host  string
	Type  string
	State string
}

// Occupancy is what usage and free space are computed from.
type Occupancy struct {
	CIDR    string
	Gateway string
	IsPool  bool
	// Addresses are the addresses of the subnet, Children the prefixes of
	// its direct children.
	Addresses []OccupiedAddress
	Children  []string
}

// Usage is the answer of GET /subnets/{id}/usage. Counts that reach 2^53
// (IPv6) are given as decimal text (…_text) instead of a number. A pool
// gives covered (the addresses its children hold) instead of host counts.
type Usage struct {
	SubnetID    string         `json:"subnet_id"`
	CIDR        string         `json:"cidr"`
	IsPool      bool           `json:"is_pool"`
	Total       *int64         `json:"total,omitempty"`
	TotalText   string         `json:"total_text,omitempty"`
	Used        int            `json:"used"`
	UsedByType  map[string]int `json:"used_by_type,omitempty"`
	UsedByState map[string]int `json:"used_by_state,omitempty"`
	Free        *int64         `json:"free,omitempty"`
	FreeText    string         `json:"free_text,omitempty"`
	Covered     *int64         `json:"covered,omitempty"`
	CoveredText string         `json:"covered_text,omitempty"`
	// Percent is the share in use (pool: covered), one decimal.
	Percent float64 `json:"percent"`
}

// FreeAddresses is the answer of GET /subnets/{id}/free-addresses.
type FreeAddresses struct {
	SubnetID  string   `json:"subnet_id"`
	Addresses []string `json:"addresses"`
}

// FreeSubnets is the answer of GET /subnets/{id}/free-subnets.
type FreeSubnets struct {
	SubnetID     string   `json:"subnet_id"`
	PrefixLength int      `json:"prefix_length"`
	Subnets      []string `json:"subnets"`
}

// Fixed answers of the space rules.
var (
	errPrefixLength = &api.FieldError{Field: "prefix_length",
		Message: "prefix_length must be longer than the prefix of the subnet and at most 32 (IPv4) or 128 (IPv6)",
		Status:  http.StatusUnprocessableEntity}
	errFreeAddressesOfPool = &api.ConflictError{Field: "is_pool", Message: "a pool has no host addresses of its own"}
	errInvalidSubnetCIDR   = &api.FieldError{Field: "cidr", Message: "the subnet has no valid prefix",
		Status: http.StatusUnprocessableEntity}
)

// maxExact is 2^53: counts from here on are given as text.
var maxExact = new(big.Int).Lsh(big.NewInt(1), 53)

// count sets a number or its text, by size.
func count(n *big.Int) (exact *int64, text string) {
	if n.Cmp(maxExact) < 0 {
		v := n.Int64()
		return &v, ""
	}
	return nil, n.String()
}

// addrInt is the address as an integer.
func addrInt(a netip.Addr) *big.Int {
	b := a.As16()
	if a.Is4() {
		v4 := a.As4()
		return new(big.Int).SetBytes(v4[:])
	}
	return new(big.Int).SetBytes(b[:])
}

// intAddr is the integer as an address of the family of like.
func intAddr(n *big.Int, like netip.Addr) netip.Addr {
	if like.Is4() {
		var b [4]byte
		n.FillBytes(b[:])
		return netip.AddrFrom4(b)
	}
	var b [16]byte
	n.FillBytes(b[:])
	return netip.AddrFrom16(b)
}

// span is an inclusive range of addresses as integers.
type span struct{ lo, hi *big.Int }

// prefixSpan is the range of all addresses of p.
func prefixSpan(p netip.Prefix) span {
	lo := addrInt(p.Masked().Addr())
	size := new(big.Int).Lsh(big.NewInt(1), uint(p.Addr().BitLen()-p.Bits()))
	hi := new(big.Int).Add(lo, size)
	return span{lo: lo, hi: hi.Sub(hi, big.NewInt(1))}
}

// hostSpan is the range of the usable host addresses of p: IPv4 up to /30
// without network and broadcast address, /31 both (RFC 3021), /32 the one;
// IPv6 every address.
func hostSpan(p netip.Prefix) span {
	s := prefixSpan(p)
	if p.Addr().Is4() && p.Bits() <= 30 {
		s.lo = new(big.Int).Add(s.lo, big.NewInt(1))
		s.hi = new(big.Int).Sub(s.hi, big.NewInt(1))
	}
	return s
}

func spanSize(s span) *big.Int {
	n := new(big.Int).Sub(s.hi, s.lo)
	return n.Add(n, big.NewInt(1))
}

// blocked merges the occupied ranges inside within: single hosts (and the
// gateway) and, with children, the ranges of the children; sorted.
func (o *Occupancy) blocked(p netip.Prefix, within span, children bool) []span {
	var spans []span
	add := func(s span) {
		if s.hi.Cmp(within.lo) < 0 || s.lo.Cmp(within.hi) > 0 {
			return
		}
		spans = append(spans, s)
	}
	for _, a := range o.Addresses {
		if addr, err := netip.ParseAddr(hostOf(a.Host)); err == nil && addr.Is4() == p.Addr().Is4() && p.Contains(addr) {
			v := addrInt(addr)
			add(span{lo: v, hi: v})
		}
	}
	if gw, err := netip.ParseAddr(hostOf(o.Gateway)); err == nil && gw.Is4() == p.Addr().Is4() && p.Contains(gw) {
		v := addrInt(gw)
		add(span{lo: v, hi: v})
	}
	if children {
		for _, c := range o.Children {
			if cp, err := netip.ParsePrefix(c); err == nil && cp.Addr().Is4() == p.Addr().Is4() {
				add(prefixSpan(cp))
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo.Cmp(spans[j].lo) < 0 })
	var merged []span
	for _, s := range spans {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			next := new(big.Int).Add(last.hi, big.NewInt(1))
			if s.lo.Cmp(next) <= 0 {
				if s.hi.Cmp(last.hi) > 0 {
					last.hi = s.hi
				}
				continue
			}
		}
		merged = append(merged, span{lo: s.lo, hi: s.hi})
	}
	return merged
}

// percent is part/whole in percent with one decimal.
func percent(part, whole *big.Int) float64 {
	if whole.Sign() == 0 {
		return 0
	}
	r := new(big.Rat).SetFrac(new(big.Int).Mul(part, big.NewInt(1000)), whole)
	f, _ := r.Float64()
	return float64(int64(f+0.5)) / 10
}

// ComputeUsage computes the usage of a subnet (IPM-03): total usable hosts,
// the distinct occupied hosts among them by type and state, free and
// percent; for a pool the addresses its children cover.
func ComputeUsage(o *Occupancy) (Usage, error) {
	p, err := netip.ParsePrefix(o.CIDR)
	if err != nil {
		return Usage{}, errInvalidSubnetCIDR
	}
	p = p.Masked()
	u := Usage{CIDR: p.String(), IsPool: o.IsPool}
	if o.IsPool {
		all := prefixSpan(p)
		size := spanSize(all)
		covered := new(big.Int)
		children := Occupancy{Children: o.Children}
		for _, s := range children.blocked(p, all, true) {
			covered.Add(covered, spanSize(s))
		}
		u.Total, u.TotalText = count(size)
		u.Covered, u.CoveredText = count(covered)
		u.Percent = percent(covered, size)
		return u, nil
	}
	hosts := hostSpan(p)
	total := spanSize(hosts)
	// One entry per occupied host inside the usable range; with several
	// rows for a host (other interfaces) the first by type and state.
	rows := append([]OccupiedAddress(nil), o.Addresses...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Type != rows[j].Type {
			return rows[i].Type < rows[j].Type
		}
		return rows[i].State < rows[j].State
	})
	seen := map[netip.Addr]bool{}
	u.UsedByType = map[string]int{IPTypeDiscovered: 0, IPTypeReserved: 0, IPTypeStatic: 0, IPTypeDHCP: 0}
	u.UsedByState = map[string]int{IPStateActive: 0, IPStateOffline: 0, IPStateUnknown: 0}
	for _, a := range rows {
		addr, err := netip.ParseAddr(hostOf(a.Host))
		if err != nil || addr.Is4() != p.Addr().Is4() || seen[addr] {
			continue
		}
		v := addrInt(addr)
		if v.Cmp(hosts.lo) < 0 || v.Cmp(hosts.hi) > 0 {
			continue
		}
		seen[addr] = true
		u.Used++
		u.UsedByType[a.Type]++
		u.UsedByState[a.State]++
	}
	used := big.NewInt(int64(u.Used))
	u.Total, u.TotalText = count(total)
	u.Free, u.FreeText = count(new(big.Int).Sub(total, used))
	u.Percent = percent(used, total)
	return u, nil
}

// ComputeFreeAddresses lists up to limit free host addresses of a subnet in
// ascending order (IPM-03): every usable host that is no address of the
// subnet, not its gateway and not inside one of its children. A pool has
// none (409 is_pool).
func ComputeFreeAddresses(o *Occupancy, limit int) ([]string, error) {
	if o.IsPool {
		return nil, errFreeAddressesOfPool
	}
	p, err := netip.ParsePrefix(o.CIDR)
	if err != nil {
		return nil, errInvalidSubnetCIDR
	}
	p = p.Masked()
	hosts := hostSpan(p)
	out := []string{}
	cursor := new(big.Int).Set(hosts.lo)
	emit := func(upTo *big.Int) {
		for cursor.Cmp(upTo) <= 0 && len(out) < limit {
			out = append(out, intAddr(cursor, p.Addr()).String())
			cursor.Add(cursor, big.NewInt(1))
		}
	}
	for _, b := range o.blocked(p, hosts, true) {
		if len(out) >= limit {
			break
		}
		if b.lo.Cmp(cursor) > 0 {
			emit(new(big.Int).Sub(b.lo, big.NewInt(1)))
		}
		if b.hi.Cmp(cursor) >= 0 {
			cursor = new(big.Int).Add(b.hi, big.NewInt(1))
		}
	}
	if len(out) < limit {
		emit(hosts.hi)
	}
	return out, nil
}

// ComputeFreeSubnets lists up to limit aligned free prefixes of length
// prefixLen inside the subnet (IPM-03) that intersect neither a child nor
// an address of the subnet. prefixLen must be longer than the subnet's
// prefix and at most 32 or 128 (422 prefix_length).
func ComputeFreeSubnets(o *Occupancy, prefixLen, limit int) ([]string, error) {
	p, err := netip.ParsePrefix(o.CIDR)
	if err != nil {
		return nil, errInvalidSubnetCIDR
	}
	p = p.Masked()
	if prefixLen <= p.Bits() || prefixLen > p.Addr().BitLen() {
		return nil, errPrefixLength
	}
	all := prefixSpan(p)
	size := new(big.Int).Lsh(big.NewInt(1), uint(p.Addr().BitLen()-prefixLen))
	last := new(big.Int).Sub(size, big.NewInt(1))
	out := []string{}
	cursor := new(big.Int).Set(all.lo)
	// emit lists the aligned blocks from cursor (aligned) up to gapEnd.
	emit := func(gapEnd *big.Int) {
		for len(out) < limit {
			end := new(big.Int).Add(cursor, last)
			if end.Cmp(gapEnd) > 0 {
				return
			}
			out = append(out, netip.PrefixFrom(intAddr(cursor, p.Addr()), prefixLen).String())
			cursor.Add(cursor, size)
		}
	}
	alignUp := func(v *big.Int) *big.Int {
		r := new(big.Int).Mod(new(big.Int).Sub(v, all.lo), size)
		if r.Sign() == 0 {
			return new(big.Int).Set(v)
		}
		return new(big.Int).Add(v, new(big.Int).Sub(size, r))
	}
	for _, b := range o.blocked(p, all, true) {
		if len(out) >= limit {
			break
		}
		if b.lo.Cmp(cursor) > 0 {
			emit(new(big.Int).Sub(b.lo, big.NewInt(1)))
		}
		if b.hi.Cmp(cursor) >= 0 {
			cursor = alignUp(new(big.Int).Add(b.hi, big.NewInt(1)))
		}
	}
	if len(out) < limit {
		emit(all.hi)
	}
	return out, nil
}
