package discovery

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// TestResolveIdentity covers WP-086 (REC-02, CH29, NFR-01): the identity
// rules in their order, the blocklists, the client binding, the VRF of the
// IP match and hostnames that never merge.
func TestResolveIdentity(t *testing.T) {
	ic := &IdentityContext{GenericDeviceTypeID: "generic", VirtualTypeIDs: map[string]bool{"vm": true}, DefaultVRF: "vrf-default"}
	inventory := []ci.Item{
		{ID: "srv", CITypeID: "server", SerialNumber: "SN-100", ClientID: "c1"},
		{ID: "gen", CITypeID: "generic", SerialNumber: "SN-200"},
		{ID: "placeholder", CITypeID: "server", SerialNumber: "To be filled by O.E.M."},
		{ID: "uuid", CITypeID: "server", HardwareUUID: "4c4c4544-0042-3510-8051-b4c04f4a4e33"},
		{ID: "clone", CITypeID: "server", HardwareUUID: "03000200-0400-0500-0006-000700080009"},
		{ID: "vm-1", CITypeID: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555", PrimaryMAC: "00:50:56:00:00:01"},
		{ID: "mac", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:10"},
		{ID: "mac-a", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:20"},
		{ID: "mac-b", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:21"},
		{ID: "vrrp", CITypeID: "router", PrimaryMAC: "00:00:5e:00:01:07"},
		{ID: "ip", CITypeID: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"},
		{ID: "ip-vrf", CITypeID: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1",
			Attributes: map[string]any{"raw_data": map[string]any{"vrf_id": "vrf-blue"}}},
		{ID: "host", CITypeID: "server", Hostname: "web-01", FQDN: "web-01.example.net"},
		{ID: "srv-c2", CITypeID: "server", SerialNumber: "SN-300", ClientID: "c2"},
	}

	for _, c := range []struct {
		name      string
		in        IngestItem
		action    ReconcileAction
		matched   string
		criterion string
		dupes     int
	}{
		{"serial, same type", IngestItem{CITypeName: "server", SerialNumber: "sn-100"}, ReconcileMatched, "srv", "serial_number", 0},
		{"serial, other type", IngestItem{CITypeName: "switch", SerialNumber: "SN-100"}, ReconcileCreated, "", "", 0},
		{"serial of a generic_device", IngestItem{CITypeName: "switch", SerialNumber: "SN-200"}, ReconcileMatched, "gen", "serial_number", 0},
		{"incoming generic_device", IngestItem{CITypeName: "generic", SerialNumber: "SN-100"}, ReconcileMatched, "srv", "serial_number", 0},
		{"placeholder serial is ignored", IngestItem{CITypeName: "server", SerialNumber: "to be filled by o.e.m."}, ReconcileCreated, "", "", 0},
		{"repeated-character serial is ignored", IngestItem{CITypeName: "server", SerialNumber: "00000000"}, ReconcileCreated, "", "", 0},
		{"hardware uuid", IngestItem{CITypeName: "server", HardwareUUID: "4C4C4544-0042-3510-8051-B4C04F4A4E33"}, ReconcileMatched, "uuid", "hardware_uuid", 0},
		{"clone uuid is ignored", IngestItem{CITypeName: "server", HardwareUUID: "03000200-0400-0500-0006-000700080009"}, ReconcileCreated, "", "", 0},
		{"null uuid is ignored", IngestItem{CITypeName: "server", Fingerprint: map[string]any{"hardware_uuid": "00000000-0000-0000-0000-000000000000"}}, ReconcileCreated, "", "", 0},
		{"virtual uuid alone does not match", IngestItem{CITypeName: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555"}, ReconcileCreated, "", "", 0},
		{"virtual uuid with mac matches", IngestItem{CITypeName: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555", PrimaryMAC: "00-50-56-00-00-01"}, ReconcileMatched, "vm-1", "hardware_uuid", 0},
		{"mac intersection", IngestItem{CITypeName: "switch", Fingerprint: map[string]any{"mac_addresses": []any{"02:00:00:00:00:01", "00:1B:21:00:00:10"}}}, ReconcileMatched, "mac", "mac_addresses", 0},
		{"several mac hits are ambiguous", IngestItem{CITypeName: "switch", Fingerprint: map[string]any{"mac_addresses": []any{"00:1b:21:00:00:20", "00:1b:21:00:00:21"}}}, ReconcileConflict, "", "mac_addresses", 0},
		{"vrrp mac is ignored", IngestItem{CITypeName: "router", PrimaryMAC: "00:00:5e:00:01:07"}, ReconcileCreated, "", "", 0},
		{"locally administered mac is ignored", IngestItem{CITypeName: "switch", PrimaryMAC: "02:1b:21:00:00:10"}, ReconcileCreated, "", "", 0},
		{"ip and sysObjectID in the default vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"}, ReconcileMatched, "ip", "management_ip_sys_object_id", 0},
		{"explicit default vrf equals none", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1", VRFID: "vrf-default"}, ReconcileMatched, "ip", "management_ip_sys_object_id", 0},
		{"ip in another vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1"}, ReconcileCreated, "", "", 0},
		{"ip in the same non-default vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1", VRFID: "vrf-blue"}, ReconcileMatched, "ip-vrf", "management_ip_sys_object_id", 0},
		{"ip without sysObjectID", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5"}, ReconcileCreated, "", "", 0},
		{"hostname never merges", IngestItem{CITypeName: "server", Hostname: "WEB-01"}, ReconcileCreated, "", "", 1},
		{"fqdn never merges", IngestItem{CITypeName: "server", FQDN: "web-01.example.net"}, ReconcileCreated, "", "", 1},
		{"client binding: own client", IngestItem{CITypeName: "server", SerialNumber: "SN-100", ClientID: "c1"}, ReconcileMatched, "srv", "serial_number", 0},
		{"client binding: other client", IngestItem{CITypeName: "server", SerialNumber: "SN-300", ClientID: "c1"}, ReconcileCreated, "", "", 0},
		{"org-wide record matches any client", IngestItem{CITypeName: "server", SerialNumber: "SN-300"}, ReconcileMatched, "srv-c2", "serial_number", 0},
		{"serial before mac", IngestItem{CITypeName: "server", SerialNumber: "SN-100", PrimaryMAC: "00:1b:21:00:00:10"}, ReconcileMatched, "srv", "serial_number", 0},
	} {
		in := c.in
		got := ResolveIdentity(inventory, &in, ic)
		if got.Action != c.action || got.MatchedCIID != c.matched || got.Criterion != c.criterion || len(got.ProbableDuplicates) != c.dupes {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// TestWithoutTakenIdentity: a new CI does not take identity values another
// CI holds (they are unique per organization).
func TestWithoutTakenIdentity(t *testing.T) {
	item := IngestItem{SerialNumber: "SN-300", HardwareUUID: "AA", PrimaryMAC: "00-1b-21-00-00-10", Hostname: "h"}
	withoutTakenIdentity(&item, []ci.Item{{SerialNumber: "sn-300"}, {HardwareUUID: "aa"}, {PrimaryMAC: "00:1b:21:00:00:10"}})
	if item.SerialNumber != "" || item.HardwareUUID != "" || item.PrimaryMAC != "" || item.Hostname != "h" {
		t.Errorf("item %+v", item)
	}
}

func TestCanonicalMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00:1B:21:0A:0B:0C": "00:1b:21:0a:0b:0c",
		"001b.210a.0b0c":    "00:1b:21:0a:0b:0c",
		"02:00:00:00:00:01": "", // locally administered
		"01:00:5e:00:00:01": "", // multicast
		"00:00:0c:07:ac:01": "", // HSRP
		"00:00:0c:9f:f0:01": "", // HSRPv2
		"00:00:5e:00:02:01": "", // VRRP IPv6
		"zz":                "",
	} {
		if got := canonicalMAC(in); got != want {
			t.Errorf("canonicalMAC(%q) = %q, want %q", in, got, want)
		}
	}
}
