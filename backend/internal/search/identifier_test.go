package search

import "testing"

// TestParseIdentifiers covers the identifier conditions of a search text
// (SRC-02, WP-308): the MAC formats of macaddr, IPv4 and IPv6, the bounds
// of a CIDR prefix, the prefixes with their minimum lengths and texts
// without identifier.
func TestParseIdentifiers(t *testing.T) {
	for _, c := range []struct {
		text string
		want Identifiers
		ok   bool
	}{
		{"08:00:2B:01:02:03", Identifiers{MAC: "08002b010203"}, true},
		{"08-00-2b-01-02-03", Identifiers{MAC: "08002b010203"}, true},
		{"08002b:010203", Identifiers{MAC: "08002b010203"}, true},
		{"08002b-010203", Identifiers{MAC: "08002b010203"}, true},
		{"0800.2b01.0203", Identifiers{MAC: "08002b010203"}, true},
		{"0800-2b01-0203", Identifiers{MAC: "08002b010203"}, true},
		{"08002B010203", Identifiers{MAC: "08002b010203"}, true},
		{" 10.20.30.40 ", Identifiers{Addr: "10.20.30.40"}, true},
		{"2001:DB8::1", Identifiers{Addr: "2001:db8::1"}, true},
		{"10.20.30.0/24", Identifiers{Low: "10.20.30.0", High: "10.20.30.255"}, true},
		{"10.20.30.77/30", Identifiers{Low: "10.20.30.76", High: "10.20.30.79"}, true},
		{"10.0.0.1/32", Identifiers{Low: "10.0.0.1", High: "10.0.0.1"}, true},
		{"2001:db8::/120", Identifiers{Low: "2001:db8::", High: "2001:db8::ff"}, true},
		{"INV-0042", Identifiers{Prefix: "inv-0042", PrefixEnd: "inv-0043"}, true},
		{"10.20.30", Identifiers{Prefix: "10.20.30", PrefixEnd: "10.20.31", MACPrefix: "102030", MACPrefixEnd: "102031"}, true},
		{"08:00:2b", Identifiers{Prefix: "08:00:2b", PrefixEnd: "08:00:2c", MACPrefix: "08002b", MACPrefixEnd: "08002c"}, true},
		{"08:00:2", Identifiers{Prefix: "08:00:2", PrefixEnd: "08:00:3"}, true},
		{"abz", Identifiers{Prefix: "abz", PrefixEnd: "ab{"}, true},
		{"ab", Identifiers{}, false},
		{"", Identifiers{}, false},
		{"two words", Identifiers{}, false},
		{"tab\there", Identifiers{}, false},
		{"fe80::1%eth0", Identifiers{Prefix: "fe80::1%eth0", PrefixEnd: "fe80::1%eth1"}, true},
	} {
		got, ok := ParseIdentifiers(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseIdentifiers(%q) = %+v, %v; want %+v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"08:00:2b:01:02:03": "08002b010203", "0800.2B01.0203": "08002b010203", "08002b010203": "08002b010203",
	} {
		if got, ok := NormalizeMAC(in); !ok || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"08:00:2b:01:02", "zz:00:2b:01:02:03", "08:00-2b:01:02:03", "08002b0102030405"} {
		if got, ok := NormalizeMAC(in); ok {
			t.Errorf("NormalizeMAC(%q) = %q, want no MAC", in, got)
		}
	}
}

func TestPrefixEnd(t *testing.T) {
	for in, want := range map[string]string{"abc": "abd", "ab\U0010FFFF": "ac", "\U0010FFFF": "", "a퟿": "a", "é": "ê"} {
		if got := prefixEnd(in); got != want {
			t.Errorf("prefixEnd(%q) = %q, want %q", in, got, want)
		}
	}
}
