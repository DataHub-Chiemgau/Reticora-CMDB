package ipam

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestNarrowestContainer covers the pure hierarchy of WP-901 (IPM-02): the
// narrowest strictly containing prefix for IPv4 and IPv6, /0 as the widest
// container, /32 and /128 as the narrowest members, and no container for an
// equal or disjoint prefix or one of the other family.
func TestNarrowestContainer(t *testing.T) {
	mp := netip.MustParsePrefix
	for _, tc := range []struct {
		name       string
		p          string
		candidates []string
		want       int // -1: none
	}{
		{"ipv4 narrowest of three", "10.1.1.0/24", []string{"10.0.0.0/8", "10.1.0.0/16", "10.2.0.0/16"}, 1},
		{"ipv4 order does not matter", "10.1.1.0/24", []string{"10.1.0.0/16", "0.0.0.0/0", "10.0.0.0/8"}, 0},
		{"ipv4 /0 contains everything", "192.168.1.0/24", []string{"0.0.0.0/0", "10.0.0.0/8"}, 0},
		{"ipv4 /32 in /31", "10.0.0.1/32", []string{"10.0.0.0/31", "10.0.0.0/30"}, 0},
		{"equal is no container", "10.0.0.0/8", []string{"10.0.0.0/8"}, -1},
		{"disjoint", "172.16.0.0/12", []string{"10.0.0.0/8", "192.168.0.0/16"}, -1},
		{"ipv6 narrowest", "2001:db8:1:2::/64", []string{"2001:db8::/32", "2001:db8:1::/48", "2001:db8:2::/48"}, 1},
		{"ipv6 /128 in /127", "2001:db8::1/128", []string{"2001:db8::/127", "::/0"}, 0},
		{"ipv6 /0 contains", "2001:db8::/32", []string{"::/0"}, 0},
		{"families do not mix", "10.0.0.0/8", []string{"::/0"}, -1},
	} {
		cands := make([]netip.Prefix, len(tc.candidates))
		for i, c := range tc.candidates {
			cands[i] = mp(c)
		}
		got, ok := Narrowest(mp(tc.p), cands)
		if (tc.want < 0 && ok) || (tc.want >= 0 && (!ok || got != tc.want)) {
			t.Errorf("%s: Narrowest(%s) = %d, %v; want %d", tc.name, tc.p, got, ok, tc.want)
		}
	}
}

// TestClassifyPrefixes pins the relations contains, within, equal and
// disjoint, including /0, /32 and /128.
func TestClassifyPrefixes(t *testing.T) {
	mp := netip.MustParsePrefix
	for _, tc := range []struct {
		a, b string
		want Relation
	}{
		{"10.0.0.0/8", "10.1.0.0/16", RelationContains},
		{"10.1.0.0/16", "10.0.0.0/8", RelationWithin},
		{"10.0.0.0/8", "10.0.0.0/8", RelationEqual},
		{"10.0.0.0/8", "11.0.0.0/8", RelationDisjoint},
		{"0.0.0.0/0", "10.0.0.1/32", RelationContains},
		{"10.0.0.1/32", "10.0.0.1/32", RelationEqual},
		{"10.0.0.1/32", "10.0.0.2/32", RelationDisjoint},
		{"::/0", "2001:db8::1/128", RelationContains},
		{"2001:db8::1/128", "2001:db8::/64", RelationWithin},
		{"2001:db8::/64", "2001:db9::/64", RelationDisjoint},
		{"10.0.0.0/8", "::/0", RelationDisjoint},
		// Unmasked input is masked first: 10.0.0.5/24 is 10.0.0.0/24.
		{"10.0.0.5/24", "10.0.0.128/25", RelationContains},
	} {
		if got := Classify(mp(tc.a), mp(tc.b)); got != tc.want {
			t.Errorf("Classify(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestMemoryOverlapRule pins the overlap rule of IPM-02 in the memory
// repository, as subnet_overlap_partners does in PostgreSQL: a true
// nesting is no overlap in any section; a child over an address of its
// parent (no pool) is 409 cidr in a strict section and a warning
// otherwise; a child of a pool always passes.
func TestMemoryOverlapRule(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	strict := &Section{OrganizationID: "org-1", Name: "strict", StrictMode: true}
	if err := repo.CreateSection(ctx, strict); err != nil {
		t.Fatal(err)
	}
	subnet := func(cidr, section string, pool bool) (*Subnet, error) {
		s := &Subnet{OrganizationID: "org-1", CIDR: cidr, SectionID: section, IsPool: pool}
		return s, repo.CreateSubnet(ctx, s)
	}
	for _, section := range []string{strict.ID, ""} {
		base := "10.1"
		if section == "" {
			base = "10.2"
		}
		host, err := subnet(base+".0.0/16", section, false)
		if err != nil {
			t.Fatal(err)
		}
		nested, err := subnet(base+".1.0/24", section, false)
		if err != nil || nested.ParentID != host.ID || len(nested.Warnings) != 0 {
			t.Fatalf("section %q: true nesting = %+v, %v; want under the host without warning", section, nested, err)
		}
		if err = repo.CreateIPAddress(ctx, &IPAddress{OrganizationID: "org-1", SubnetID: host.ID, Address: base + ".2.5"}); err != nil {
			t.Fatal(err)
		}
		over, err := subnet(base+".2.0/24", section, false)
		var conflict *api.ConflictError
		switch {
		case section != "" && (!errors.As(err, &conflict) || conflict.Field != "cidr"):
			t.Errorf("strict overlap = %v, want 409 cidr", err)
		case section == "" && (err != nil || len(over.Warnings) != 1 || over.Warnings[0].SubnetID != host.ID):
			t.Errorf("overlap without strict_mode = %+v, %v; want a warning naming %s", over, err, host.ID)
		}
	}
	pool, err := subnet("10.3.0.0/16", strict.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if child, err := subnet("10.3.1.0/24", strict.ID, false); err != nil || child.ParentID != pool.ID {
		t.Errorf("child of a strict pool = %+v, %v", child, err)
	}
}
