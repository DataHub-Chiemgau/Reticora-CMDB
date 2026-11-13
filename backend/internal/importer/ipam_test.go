package importer

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// WP-920 (IPM-09): the rows of the phpIPAM example files parse without a
// database. subnet with mask (prefix length and dotted netmask) and cidr
// give the same network, a contradiction is a row error; state and
// is_gateway map from the phpIPAM values, unknown values are row errors.

// readExample maps every row of a testdata file with the identity mapping
// (each column onto the target of its name).
func readExample(t *testing.T, name string) []*MappedRow {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rd, err := OpenCSV(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	cols := map[string]ColumnMapping{}
	for _, c := range rd.Header() {
		cols[c] = ColumnMapping{Target: c}
	}
	mp, issues := newMapper(&Mapping{Columns: cols}, rd.Header())
	if len(issues) > 0 {
		t.Fatalf("mapping issues: %v", issues)
	}
	var out []*MappedRow
	for {
		r, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		row := r
		mapped, issues := mp.apply(&row)
		if len(issues) > 0 {
			t.Fatalf("line %d: %v", r.Line, issues)
		}
		out = append(out, mapped)
	}
}

func mapped(values map[string]string) *MappedRow {
	m := &MappedRow{Values: map[string]Value{}}
	for k, v := range values {
		m.Values[k] = Value{Text: v}
	}
	return m
}

func TestIPAMExampleSubnetsParse(t *testing.T) {
	rows := readExample(t, "phpipam_subnets.csv")
	want := []struct {
		cidr, field string
	}{
		{"10.92.0.0/16", ""},
		{"10.92.1.0/24", ""}, // dotted netmask
		{"10.92.2.0/24", ""}, // subnet with mask and cidr agree
		{"10.92.3.0/24", ""}, // cidr alone
		{"10.92.4.0/24", ""}, // the unknown section is a lookup error later
		{"10.92.5.0/24", ""},
		{"", SubnetCIDRTarget}, // contradiction
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		r, issue := parseSubnetRow(rows[i])
		if w.field != "" {
			if issue == nil || issue.Field != w.field || issue.Code != CodeInvalidValue {
				t.Fatalf("row %d: issue %+v, want %s", i, issue, w.field)
			}
			continue
		}
		if issue != nil {
			t.Fatalf("row %d: %+v", i, issue)
		}
		if r.prefix.String() != w.cidr {
			t.Fatalf("row %d: %s, want %s", i, r.prefix, w.cidr)
		}
	}
	first, _ := parseSubnetRow(rows[0])
	if first.pool == nil || !*first.pool {
		t.Fatalf("is_pool of the campus: %v", first.pool)
	}
	office, _ := parseSubnetRow(rows[1])
	if office.vlan != 200 || office.gateway != "10.92.1.1" || office.nameserver != "intern" {
		t.Fatalf("office: %+v", office)
	}
}

func TestIPAMSubnetMaskAndCIDRAgree(t *testing.T) {
	for _, values := range []map[string]string{
		{"subnet": "192.0.2.0", "mask": "24"},
		{"subnet": "192.0.2.0", "mask": "255.255.255.0"},
		{"cidr": "192.0.2.0/24"},
		{"cidr": "192.0.2.77/24"}, // the host bits go
		{"subnet": "192.0.2.0/24"},
		{"subnet": "192.0.2.0", "mask": "24", "cidr": "192.0.2.0/24"},
		{"subnet": "192.0.2.0/24", "mask": "24"},
		{"subnet": "192.0.2.0/24", "mask": "255.255.255.0"},
	} {
		r, issue := parseSubnetRow(mapped(values))
		if issue != nil || r.prefix.String() != "192.0.2.0/24" {
			t.Fatalf("%v: %v %+v", values, r, issue)
		}
	}
	// IPv6 takes a prefix length as mask.
	if r, issue := parseSubnetRow(mapped(map[string]string{"subnet": "2001:db8::", "mask": "64"})); issue != nil || r.prefix.String() != "2001:db8::/64" {
		t.Fatalf("IPv6 with prefix length: %v %+v", r, issue)
	}
	for values, field := range map[*map[string]string]string{
		{"subnet": "192.0.2.0", "mask": "24", "cidr": "192.0.3.0/24"}: SubnetCIDRTarget,
		{"subnet": "192.0.2.0", "mask": "255.0.255.0"}:                SubnetMaskTarget,
		{"subnet": "192.0.2.0"}:                                       SubnetMaskTarget,
		{"subnet": "192.0.2.0", "mask": "33x"}:                        SubnetMaskTarget,
		{"subnet": "192.0.2.0", "mask": "33"}:                         SubnetMaskTarget,
		{"subnet": "2001:db8::", "mask": "255.255.255.0"}:             SubnetMaskTarget,
		{"subnet": "2001:db8::", "mask": "129"}:                       SubnetMaskTarget,
		{"subnet": "10.0.0.0/16", "mask": "24"}:                       SubnetMaskTarget,
		{"subnet": "10.0.0.0/16", "mask": "255.255.255.0"}:            SubnetMaskTarget,
		{"subnet": "no-ip", "mask": "24"}:                             SubnetAddressTarget,
		{"description": "no prefix"}:                                  SubnetCIDRTarget,
		{"cidr": "192.0.2.0/24", "gateway": "192.0.3.1"}:              SubnetGatewayTarget,
		{"cidr": "192.0.2.0/24", "vlan": "5000"}:                      SubnetVLANTarget,
		{"cidr": "192.0.2.0/24", "is_pool": "maybe"}:                  SubnetPoolTarget,
		{"cidr": "192.0.2.0/24", "nonsense": "x"}:                     "nonsense",
	} {
		_, issue := parseSubnetRow(mapped(*values))
		if issue == nil || issue.Field != field {
			t.Fatalf("%v: issue %+v, want field %s", *values, issue, field)
		}
	}
}

func TestIPAMExampleAddressesParse(t *testing.T) {
	rows := readExample(t, "phpipam_addresses.csv")
	if len(rows) != 7 {
		t.Fatalf("%d rows", len(rows))
	}
	gw, issue := parseAddressRow(rows[0])
	if issue != nil || gw.state != "active" || gw.isGateway == nil || !*gw.isGateway || gw.addr.String() != "10.92.1.1" {
		t.Fatalf("gateway row: %+v %+v", gw, issue)
	}
	srv, _ := parseAddressRow(rows[1])
	if srv.mac != "02:00:00:09:20:01" || srv.isGateway == nil || *srv.isGateway || *srv.hostname != "srv1.example.test" {
		t.Fatalf("server row: %+v", srv)
	}
	off, _ := parseAddressRow(rows[2])
	if off.state != "offline" || off.subnet != "10.92.1.0/24" {
		t.Fatalf("offline row: %+v", off)
	}
	dhcp, _ := parseAddressRow(rows[3])
	if dhcp.typ != "dhcp" || dhcp.state != "" {
		t.Fatalf("DHCP row: %+v", dhcp)
	}
	if _, issue := parseAddressRow(rows[6]); issue == nil || issue.Field != AddressStateTarget {
		t.Fatalf("unknown state: %+v", issue)
	}
}

func TestIPAMAddressStateAndGatewayMapping(t *testing.T) {
	for text, want := range map[string][2]string{
		"Used": {"active", ""}, "used": {"active", ""}, "2": {"active", ""},
		"Offline": {"offline", ""}, "1": {"offline", ""},
		"Reserved": {"", "reserved"}, "3": {"", "reserved"},
		"DHCP": {"", "dhcp"}, "4": {"", "dhcp"},
		"active": {"active", ""}, "unknown": {"unknown", ""}, "": {"", ""},
	} {
		state, typ, err := mapAddressState(text)
		if err != nil || state != want[0] || typ != want[1] {
			t.Fatalf("%q: %q %q %v", text, state, typ, err)
		}
	}
	if _, _, err := mapAddressState("5"); err == nil {
		t.Fatal("tag 5 accepted")
	}
	for text, want := range map[string]bool{"1": true, "0": false, "true": true, "no": false, "Ja": true, "nein": false} {
		got, err := parseFlag(text)
		if err != nil || got == nil || *got != want {
			t.Fatalf("%q: %v %v", text, got, err)
		}
	}
	if _, err := parseFlag("vielleicht"); err == nil {
		t.Fatal("unknown flag accepted")
	}
	for values, field := range map[*map[string]string]string{
		{"ip_addr": "10.0.0.1", "state": "Reserved", "type": "static"}: AddressStateTarget,
		{"ip_addr": "10.0.0.1", "type": "discovered"}:                  AddressTypeTarget,
		{"ip_addr": "10.0.0.1", "is_gateway": "x?"}:                    AddressIsGatewayTarget,
		{"ip_addr": "10.0.0.1", "mac": "not-a-mac"}:                    AddressMACTarget,
		{"ip_addr": "10.0.0.1", "subnet": "10.1.0.0/16"}:               AddressSubnetTarget,
		{"ip_addr": "no-ip"}:                                           AddressIPTarget,
		{"hostname": "only"}:                                           AddressIPTarget,
	} {
		if _, issue := parseAddressRow(mapped(*values)); issue == nil || issue.Field != field {
			t.Fatalf("%v: issue %+v, want %s", *values, issue, field)
		}
	}
	// A phpIPAM export may carry the prefix length; Reserved sets the type.
	r, issue := parseAddressRow(mapped(map[string]string{"ip_addr": "10.0.0.9/24", "state": "Reserved", "mac": "02-00-00-AA-BB-CC"}))
	if issue != nil || r.addr.String() != "10.0.0.9" || r.typ != "reserved" || r.mac != "02:00:00:aa:bb:cc" {
		t.Fatalf("%+v %+v", r, issue)
	}
}

func TestIPAMChooseAddressRow(t *testing.T) {
	rows := []AddressRow{{ID: "a", InterfaceID: "if-1"}, {ID: "b", InterfaceID: "if-2"}}
	if got, amb := chooseAddressRow(rows, "if-2"); amb || got == nil || got.ID != "b" {
		t.Fatalf("own interface: %v %v", got, amb)
	}
	// Another interface holds it: a new row (the duplicate check reports it).
	if got, amb := chooseAddressRow(rows, "if-3"); amb || got != nil {
		t.Fatalf("other interface: %v %v", got, amb)
	}
	if _, amb := chooseAddressRow(rows, ""); !amb {
		t.Fatal("two bound rows without mac are ambiguous")
	}
	unbound := []AddressRow{{ID: "a", InterfaceID: "if-1"}, {ID: "u"}}
	if got, _ := chooseAddressRow(unbound, "if-9"); got == nil || got.ID != "u" {
		t.Fatalf("unbound row gets bound: %v", got)
	}
	if got, _ := chooseAddressRow(nil, ""); got != nil {
		t.Fatal("no row")
	}
}

func TestIPAMTargetsAreValidMappingTargets(t *testing.T) {
	for _, target := range append(append([]string{}, subnetInputTargets...), AddressTargets...) {
		if !targetPattern.MatchString(target) || strings.ContainsAny(target, " .") {
			t.Fatalf("target %q", target)
		}
	}
}
