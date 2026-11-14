package ipam

import (
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// TestSubnetUsageTotals covers the host counts of IPM-03 (WP-903): a /24
// empty and full, /30, /31 (RFC 3021), /32, /127 and /128; network and
// broadcast addresses of IPv4 up to /30 do not count.
func TestSubnetUsageTotals(t *testing.T) {
	for _, tc := range []struct {
		cidr  string
		total int64
	}{
		{"10.0.0.0/24", 254}, {"10.0.0.0/30", 2}, {"10.0.0.0/31", 2}, {"10.0.0.1/32", 1},
		{"2001:db8::/127", 2}, {"2001:db8::1/128", 1},
	} {
		u, err := ComputeUsage(&Occupancy{CIDR: tc.cidr})
		if err != nil {
			t.Fatal(err)
		}
		if u.Total == nil || *u.Total != tc.total || u.Used != 0 || u.Free == nil || *u.Free != tc.total || u.Percent != 0 {
			t.Errorf("%s: %+v, want total %d free", tc.cidr, u, tc.total)
		}
	}
	// A full /24: every host occupied once (one twice, on another
	// interface), the network address does not count.
	var full []OccupiedAddress
	for i := 1; i <= 254; i++ {
		full = append(full, OccupiedAddress{Host: fmt.Sprintf("10.0.0.%d", i), Type: IPTypeStatic, State: IPStateActive})
	}
	full = append(full, OccupiedAddress{Host: "10.0.0.7", Type: IPTypeDiscovered, State: IPStateUnknown},
		OccupiedAddress{Host: "10.0.0.0", Type: IPTypeReserved, State: IPStateUnknown})
	u, err := ComputeUsage(&Occupancy{CIDR: "10.0.0.0/24", Addresses: full})
	if err != nil {
		t.Fatal(err)
	}
	if u.Used != 254 || *u.Free != 0 || u.Percent != 100 || u.UsedByType[IPTypeStatic] != 253 || u.UsedByType[IPTypeDiscovered] != 1 ||
		u.UsedByState[IPStateActive] != 253 || u.UsedByState[IPStateUnknown] != 1 {
		t.Errorf("full /24: %+v", u)
	}
	// A /64 counts beyond 2^53 as text.
	u, err = ComputeUsage(&Occupancy{CIDR: "2001:db8::/64", Addresses: []OccupiedAddress{{Host: "2001:db8::1", Type: IPTypeStatic, State: IPStateActive}}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Total != nil || u.TotalText != "18446744073709551616" || u.FreeText != "18446744073709551615" || u.Used != 1 {
		t.Errorf("/64: %+v", u)
	}
	// One decimal.
	u, _ = ComputeUsage(&Occupancy{CIDR: "10.0.0.0/30", Addresses: []OccupiedAddress{{Host: "10.0.0.1", Type: IPTypeStatic}}})
	if u.Percent != 50 {
		t.Errorf("percent of 1 in 2 = %v", u.Percent)
	}
	u, _ = ComputeUsage(&Occupancy{CIDR: "10.0.0.0/24", Addresses: []OccupiedAddress{{Host: "10.0.0.1", Type: IPTypeStatic}}})
	if u.Percent != 0.4 {
		t.Errorf("percent of 1 in 254 = %v, want 0.4", u.Percent)
	}
}

// TestPoolCoverage: a pool gives the share its children cover.
func TestPoolCoverage(t *testing.T) {
	u, err := ComputeUsage(&Occupancy{CIDR: "10.0.0.0/16", IsPool: true, Children: []string{"10.0.0.0/17", "10.0.128.0/18"}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Total == nil || *u.Total != 65536 || u.Covered == nil || *u.Covered != 49152 || u.Percent != 75 || !u.IsPool {
		t.Errorf("pool usage %+v, want 49152 of 65536 covered (75 %%)", u)
	}
	if _, err = ComputeFreeAddresses(&Occupancy{CIDR: "10.0.0.0/16", IsPool: true}, 5); err != errFreeAddressesOfPool {
		t.Errorf("free addresses of a pool: %v, want 409 is_pool", err)
	}
}

// TestFreeAddresses lists free hosts in order, skipping addresses, the
// gateway and child ranges; a /64 with 10,000 occupied addresses answers
// without walking the address space.
func TestFreeAddresses(t *testing.T) {
	o := Occupancy{CIDR: "10.0.0.0/24", Gateway: "10.0.0.1",
		Addresses: []OccupiedAddress{{Host: "10.0.0.2"}, {Host: "10.0.0.4/32"}}, Children: []string{"10.0.0.8/30"}}
	got, err := ComputeFreeAddresses(&o, 6)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"10.0.0.3", "10.0.0.5", "10.0.0.6", "10.0.0.7", "10.0.0.12", "10.0.0.13"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free = %v, want %v", got, want)
	}
	got, _ = ComputeFreeAddresses(&Occupancy{CIDR: "10.0.0.0/30", Addresses: []OccupiedAddress{{Host: "10.0.0.1"}}}, 20)
	if want := []string{"10.0.0.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free in a /30 = %v, want %v", got, want)
	}
	got, _ = ComputeFreeAddresses(&Occupancy{CIDR: "10.0.0.0/31"}, 20)
	if want := []string{"10.0.0.0", "10.0.0.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free in a /31 = %v, want %v", got, want)
	}

	// A /64 with gaps: 10,000 occupied from ::1, a gap at ::2711 (10001).
	var many []OccupiedAddress
	base := netip.MustParseAddr("2001:db8::")
	a := base
	for i := 0; i < 10001; i++ {
		a = a.Next()
		if i == 5000 {
			continue
		}
		many = append(many, OccupiedAddress{Host: a.String()})
	}
	start := time.Now()
	got, err = ComputeFreeAddresses(&Occupancy{CIDR: "2001:db8::/64", Addresses: many}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2001:db8::", "2001:db8::1389", "2001:db8::2712"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free in the /64 = %v, want %v", got, want)
	}
	u, _ := ComputeUsage(&Occupancy{CIDR: "2001:db8::/64", Addresses: many})
	if u.Used != 10000 {
		t.Errorf("used in the /64 = %d", u.Used)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the /64 took %v", d)
	}
}

// TestFreeSubnets lists aligned free prefixes that hit neither children
// nor addresses; a prefix length not longer than the subnet's is refused.
func TestFreeSubnets(t *testing.T) {
	o := Occupancy{CIDR: "10.0.0.0/24", Children: []string{"10.0.0.0/25"}}
	got, err := ComputeFreeSubnets(&o, 26, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"10.0.0.128/26", "10.0.0.192/26"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free /26 = %v, want %v", got, want)
	}
	o.Addresses = []OccupiedAddress{{Host: "10.0.0.130"}}
	got, _ = ComputeFreeSubnets(&o, 26, 20)
	if want := []string{"10.0.0.192/26"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free /26 with an address = %v, want %v", got, want)
	}
	got, _ = ComputeFreeSubnets(&o, 28, 3)
	if want := []string{"10.0.0.144/28", "10.0.0.160/28", "10.0.0.176/28"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free /28 = %v, want %v", got, want)
	}
	for _, n := range []int{24, 8, 33} {
		if _, perr := ComputeFreeSubnets(&o, n, 20); perr != errPrefixLength {
			t.Errorf("prefix_length %d: %v, want 422 prefix_length", n, perr)
		}
	}
	got, err = ComputeFreeSubnets(&Occupancy{CIDR: "2001:db8::/48", Children: []string{"2001:db8::/64"}}, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2001:db8:0:1::/64", "2001:db8:0:2::/64"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free /64 = %v, want %v", got, want)
	}
	if _, err := ComputeFreeSubnets(&Occupancy{CIDR: "2001:db8::/48"}, 129, 2); err != errPrefixLength {
		t.Errorf("prefix_length 129: %v", err)
	}
}
