package snmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// captureLogs records the log output of the test (slog default logger).
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func pduString(oid, v string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
}

func pduBytesOf(oid string, v ...byte) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: v}
}

func pduInteger(oid string, v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
}

func auditNeighborMIB() []gosnmp.SnmpPDU {
	s, i := pduString, pduInteger
	mib := v3TestMIB()
	// GigabitEthernet1/0/2 (ifIndex 2) is an up access port with a host on it.
	for k := range mib {
		if mib[k].Name == "."+oidIfAdminSt+".2" || mib[k].Name == "."+oidIfOperSt+".2" {
			mib[k].Value = 1
		}
	}
	mib = append(mib,
		// LLDP-MIB lldpLocPortTable: local port 1 = GigabitEthernet1/0/1.
		i("1.0.8802.1.1.2.1.3.7.1.2.1", 5), // lldpLocPortIdSubtype interfaceName(5)
		s("1.0.8802.1.1.2.1.3.7.1.3.1", "GigabitEthernet1/0/1"),
		s("1.0.8802.1.1.2.1.3.7.1.4.1", "GigabitEthernet1/0/1"),
		// lldpRemTable entry timeMark=0, localPortNum=1, index=1.
		i("1.0.8802.1.1.2.1.4.1.1.4.0.1.1", 4), // lldpRemChassisIdSubtype macAddress(4)
		pduBytesOf("1.0.8802.1.1.2.1.4.1.1.5.0.1.1", 0x00, 0x1b, 0x54, 0xaa, 0xbb, 0xcc),
		i("1.0.8802.1.1.2.1.4.1.1.6.0.1.1", 5), // lldpRemPortIdSubtype interfaceName(5)
		s("1.0.8802.1.1.2.1.4.1.1.7.0.1.1", "TenGigabitEthernet1/1/1"),
		s("1.0.8802.1.1.2.1.4.1.1.8.0.1.1", "uplink core-sw-1"),
		s("1.0.8802.1.1.2.1.4.1.1.9.0.1.1", "dist-sw-2"),
		// BRIDGE-MIB: bridge port 2 = ifIndex 2; MAC 00:50:56:01:02:03 learned on port 2.
		i("1.3.6.1.2.1.17.1.4.1.2.2", 2),
		i("1.3.6.1.2.1.17.4.3.1.2.0.80.86.1.2.3", 2),
		i("1.3.6.1.2.1.17.4.3.1.3.0.80.86.1.2.3", 3), // dot1dTpFdbStatus learned(3)
		pduBytesOf("1.3.6.1.2.1.17.4.3.1.1.0.80.86.1.2.3", 0x00, 0x50, 0x56, 0x01, 0x02, 0x03),
	)
	sort.Slice(mib, func(a, b int) bool { return oidLess(mib[a].Name, mib[b].Name) })
	return mib
}

// TestAuditSNMPCollectsNeighbors encodes DIS-05/TOP-01 (F-032, moved from
// neighbors_auditdefect_test.go in WP-416): the SNMP plugin reports the
// LLDP neighbour of a switch with its local port and the MAC the bridge
// learned on a port, over SNMPv3 authPriv. Control: a device without
// LLDP-MIB and BRIDGE-MIB has no neighbours and no error, its inventory
// stays complete.
func TestAuditSNMPCollectsNeighbors(t *testing.T) {
	creds := map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "auth-secret-1",
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
	cases := []struct {
		name     string
		mib      []gosnmp.SnmpPDU
		wantLLDP bool
		wantFDB  bool
		want     []plugins.DeviceNeighbor
	}{
		{name: "switch with LLDP neighbour and FDB entry", mib: auditNeighborMIB(), wantLLDP: true, wantFDB: true,
			want: []plugins.DeviceNeighbor{
				{Protocol: "lldp", LocalInterface: "GigabitEthernet1/0/1", RemoteMAC: "00:1b:54:aa:bb:cc", RemoteHostname: "dist-sw-2",
					RemoteInterface: "TenGigabitEthernet1/1/1"},
				{Protocol: "fdb", LocalInterface: "GigabitEthernet1/0/2", RemoteMAC: "00:50:56:01:02:03"},
			}},
		{name: "device without LLDP-MIB and BRIDGE-MIB (control)", mib: v3TestMIB()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := startV3Agent(t, "reticora", "auth-secret-1", "priv-secret-1", tc.mib)
			p := &Plugin{Timeout: time.Second, Port: agent.port()}
			rec, err := p.Collect(context.Background(), "127.0.0.1", creds)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if v := rec.Validate(); len(v) > 0 {
				t.Errorf("record breaks the DeviceRecord contract: %v", v)
			}
			var lldp, fdb bool
			for _, n := range rec.Neighbors {
				switch n.Protocol {
				case "lldp":
					lldp = lldp || (n.LocalInterface == "GigabitEthernet1/0/1" && (n.RemoteHostname == "dist-sw-2" || n.RemoteMAC == "00:1b:54:aa:bb:cc"))
				case "fdb":
					fdb = fdb || n.RemoteMAC == "00:50:56:01:02:03"
				}
			}
			if lldp != tc.wantLLDP {
				t.Errorf("LLDP neighbour dist-sw-2 on GigabitEthernet1/0/1 found=%v, want %v (neighbors=%+v)", lldp, tc.wantLLDP, rec.Neighbors)
			}
			if fdb != tc.wantFDB {
				t.Errorf("FDB neighbour 00:50:56:01:02:03 found=%v, want %v (neighbors=%+v)", fdb, tc.wantFDB, rec.Neighbors)
			}
			if !tc.wantLLDP && !tc.wantFDB && len(rec.Neighbors) != 0 {
				t.Errorf("device without neighbour MIBs reported neighbors %+v", rec.Neighbors)
			}
			if !reflect.DeepEqual(rec.Neighbors, tc.want) {
				t.Errorf("neighbors = %+v, want %+v", rec.Neighbors, tc.want)
			}
			if rec.System.Name != "core-sw-1" || len(rec.Interfaces) != 2 || rec.Identity.SerialNumber != "FOC1234X0YZ" {
				t.Errorf("inventory incomplete: name %q, serial %q, interfaces %+v", rec.System.Name, rec.Identity.SerialNumber, rec.Interfaces)
			}
		})
	}
}

// qBridgeMIB is a switch over SNMPv2c with the VLAN-aware forwarding
// database (Q-BRIDGE-MIB), LLDP ports named by ifName and bridge ports
// numbered apart from the ifIndex.
func qBridgeMIB() []gosnmp.SnmpPDU {
	s, i, b := pduString, pduInteger, pduBytesOf
	const (
		locPort = "1.0.8802.1.1.2.1.3.7.1"
		rem     = "1.0.8802.1.1.2.1.4.1.1"
		manAddr = "1.0.8802.1.1.2.1.4.2.1.3"
		qFdb    = "1.3.6.1.2.1.17.7.1.2.2.1"
		dFdb    = "1.3.6.1.2.1.17.4.3.1"
	)
	mib := []gosnmp.SnmpPDU{
		s(oidIfDescr+".1", "GigabitEthernet1/0/1"), s(oidIfDescr+".2", "GigabitEthernet1/0/2"), s(oidIfDescr+".3", "GigabitEthernet1/0/3"),
		s(oidIfName+".1", "Gi1/0/1"), s(oidIfName+".2", "Gi1/0/2"), s(oidIfName+".3", "Gi1/0/3"),
		// Bridge ports 11-13 are ifIndex 1-3; LLDP numbers its ports like the bridge.
		i(oidDot1dBasePortIfIndex+".11", 1), i(oidDot1dBasePortIfIndex+".12", 2), i(oidDot1dBasePortIfIndex+".13", 3),
		// Port 11 names its interface by ifName; port 13 by MAC (mapped over the bridge port).
		i(locPort+".2.11", 5), s(locPort+".3.11", "Gi1/0/1"),
		i(locPort+".2.13", 3), b(locPort+".3.13", 0x00, 0x11, 0x22, 0x33, 0x44, 0x03),

		// Remote 0.11.1: dist-sw-2 with an IPv4 and an IPv6 management address.
		i(rem+".4.0.11.1", 4), b(rem+".5.0.11.1", 0x00, 0x1b, 0x54, 0xaa, 0xbb, 0xcc),
		i(rem+".6.0.11.1", 5), s(rem+".7.0.11.1", "Te1/1/1"), s(rem+".9.0.11.1", "dist-sw-2"),
		i(manAddr+".0.11.1.1.4.192.0.2.12", 2),
		i(manAddr+".0.11.1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.18", 2),
		// The same remote again under a later time mark (TimeFilter).
		i(rem+".4.700.11.1", 4), b(rem+".5.700.11.1", 0x00, 0x1b, 0x54, 0xaa, 0xbb, 0xcc),
		// Remote 0.12.3: neither MAC, address nor name: not reportable.
		i(rem+".4.0.12.3", 7), s(rem+".5.0.12.3", "chassis-7"), i(rem+".6.0.12.3", 7), s(rem+".7.0.12.3", "17"),
		// Remote 500.13.2: chassis ID is the IPv4 address 198.51.100.7, port ID a MAC.
		i(rem+".4.500.13.2", 5), b(rem+".5.500.13.2", 1, 198, 51, 100, 7),
		i(rem+".6.500.13.2", 3), b(rem+".7.500.13.2", 0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xee),

		// dot1qTpFdbTable (FDB ID, MAC): learned in VLAN 10 and 20 on port 12.
		i(qFdb+".2.10.0.80.86.1.2.3", 12), i(qFdb+".3.10.0.80.86.1.2.3", 3),
		i(qFdb+".2.20.0.80.86.1.2.3", 12), i(qFdb+".3.20.0.80.86.1.2.3", 3),
		// The bridge's own MAC, an invalid entry, an entry without port and a multicast MAC.
		i(qFdb+".2.10.0.17.34.51.68.1", 11), i(qFdb+".3.10.0.17.34.51.68.1", 4),
		i(qFdb+".2.10.0.80.86.7.8.9", 12), i(qFdb+".3.10.0.80.86.7.8.9", 2),
		i(qFdb+".2.10.0.80.86.10.11.12", 0), i(qFdb+".3.10.0.80.86.10.11.12", 3),
		i(qFdb+".2.10.1.0.94.0.0.1", 12), i(qFdb+".3.10.1.0.94.0.0.1", 5),
		// A bridge port without interface: the MAC without local port.
		i(qFdb+".2.20.0.80.86.4.5.6", 99), i(qFdb+".3.20.0.80.86.4.5.6", 3),
		// dot1dTpFdbTable is not read when the Q-BRIDGE table answers.
		i(dFdb+".2.0.80.86.255.255.255", 12), i(dFdb+".3.0.80.86.255.255.255", 3),
	}
	sort.Slice(mib, func(a, b int) bool { return oidLess(mib[a].Name, mib[b].Name) })
	return mib
}

// TestSNMPNeighborsQBridge covers WP-416 (DIS-05, TOP-01) over SNMPv2c:
// FDB entries come from dot1qTpFdbTable (a MAC in two VLANs on one port is
// one neighbour; the bridge's own, invalid, portless and multicast entries
// are none) mapped over dot1dBasePortIfIndex to the interface name of the
// record; LLDP ports named by ifName or mapped over the bridge port number
// name the same interfaces; the management address gives the remote IP.
func TestSNMPNeighborsQBridge(t *testing.T) {
	const community = "neighbor-community"
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "access-sw-1",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, qBridgeMIB()...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	logs := captureLogs(t)

	p := &Plugin{Timeout: time.Second, Port: agent.Addr().Port}
	rec, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{"community": community})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the DeviceRecord contract: %v", v)
	}
	want := []plugins.DeviceNeighbor{
		{Protocol: "lldp", LocalInterface: "GigabitEthernet1/0/1", RemoteMAC: "00:1b:54:aa:bb:cc", RemoteIP: "192.0.2.12",
			RemoteHostname: "dist-sw-2", RemoteInterface: "Te1/1/1"},
		{Protocol: "lldp", LocalInterface: "GigabitEthernet1/0/3", RemoteIP: "198.51.100.7", RemoteInterface: "00:aa:bb:cc:dd:ee"},
		{Protocol: "fdb", LocalInterface: "GigabitEthernet1/0/2", RemoteMAC: "00:50:56:01:02:03"},
		{Protocol: "fdb", RemoteMAC: "00:50:56:04:05:06"},
	}
	if !reflect.DeepEqual(rec.Neighbors, want) {
		t.Errorf("neighbors =\n%+v\nwant\n%+v", rec.Neighbors, want)
	}
	if rec.System.Name != "access-sw-1" || len(rec.Interfaces) != 3 {
		t.Errorf("inventory: name %q, interfaces %+v", rec.System.Name, rec.Interfaces)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("unexpected warning:\n%s", logs)
	}
}

// TestSNMPNeighborsMissingMIB covers WP-416: a device that does not
// support the neighbour tables (no table, noSuchObject/noSuchInstance in
// the walk, LLDP without remote systems, a bridge without learned MACs)
// has no neighbours; the collection succeeds without error or warning and
// keeps its inventory.
func TestSNMPNeighborsMissingMIB(t *testing.T) {
	creds := map[string]string{"username": "reticora", "auth_password": "auth-secret-1", "priv_password": "priv-secret-1"}
	with := func(extra ...gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
		mib := append(v3TestMIB(), extra...)
		sort.Slice(mib, func(a, b int) bool { return oidLess(mib[a].Name, mib[b].Name) })
		return mib
	}
	cases := map[string][]gosnmp.SnmpPDU{
		"no LLDP-MIB and BRIDGE-MIB": v3TestMIB(),
		"tables answer noSuchObject and noSuchInstance": with(
			gosnmp.SnmpPDU{Name: "." + oidLldpRemChassisIDSubtype + ".0", Type: gosnmp.NoSuchObject},
			gosnmp.SnmpPDU{Name: "." + oidDot1qTpFdbPort + ".0", Type: gosnmp.NoSuchInstance},
			gosnmp.SnmpPDU{Name: "." + oidDot1dTpFdbPort + ".0", Type: gosnmp.NoSuchObject},
		),
		"LLDP without remotes, bridge without learned MACs": with(
			pduInteger(oidLldpLocPortIDSubtype+".1", 5), pduString(oidLldpLocPortID+".1", "GigabitEthernet1/0/1"),
			pduInteger(oidDot1dBasePortIfIndex+".1", 1), pduInteger(oidDot1dBasePortIfIndex+".2", 2),
		),
	}
	for name, mib := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			agent := startV3Agent(t, "reticora", "auth-secret-1", "priv-secret-1", mib)
			p := &Plugin{Timeout: time.Second, Port: agent.port()}
			rec, err := p.Collect(context.Background(), "127.0.0.1", creds)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(rec.Neighbors) != 0 {
				t.Errorf("neighbors = %+v, want none", rec.Neighbors)
			}
			if rec.System.Name != "core-sw-1" || len(rec.Interfaces) != 2 {
				t.Errorf("inventory: name %q, interfaces %+v", rec.System.Name, rec.Interfaces)
			}
			if v := rec.Validate(); len(v) > 0 {
				t.Errorf("record breaks the DeviceRecord contract: %v", v)
			}
			if strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("unexpected warning:\n%s", logs)
			}
		})
	}
}

// tableWalker walks an in-memory MIB like gosnmp BulkWalk: every row of
// the column in order until the callback fails. served counts the rows it
// handed out per column.
type tableWalker struct {
	columns map[string][]gosnmp.SnmpPDU
	served  map[string]int
}

func (w *tableWalker) add(column string, rows int, row func(i int) gosnmp.SnmpPDU) {
	for i := range rows {
		w.columns[column] = append(w.columns[column], row(i))
	}
}

func (w *tableWalker) walk(root string, fn gosnmp.WalkFunc) error {
	for _, v := range w.columns[strings.TrimPrefix(root, ".")] {
		w.served[root]++
		if err := fn(v); err != nil {
			return err
		}
	}
	return nil
}

// TestSNMPNeighborWalkLimit covers WP-416: every neighbour table is read
// to at most 10,000 entries. A table of exactly 10,000 entries is read
// whole without warning; one of 10,001 is cut after 10,000 entries with
// one warning naming the table, and the collection goes on without error.
func TestSNMPNeighborWalkLimit(t *testing.T) {
	if maxTableEntries != 10_000 {
		t.Fatalf("maxTableEntries = %d, want 10000", maxTableEntries)
	}
	logs := captureLogs(t)
	w := &tableWalker{columns: map[string][]gosnmp.SnmpPDU{}, served: map[string]int{}}
	const lldpRows, fdbRows = maxTableEntries, maxTableEntries + 1
	w.add(oidDot1dBasePortIfIndex, 1, func(int) gosnmp.SnmpPDU { return pduInteger(oidDot1dBasePortIfIndex+".1", 1) })
	// lldpRemTable: 10,000 remote systems on local port 1, by name only.
	w.add(oidLldpRemChassisIDSubtype, lldpRows, func(i int) gosnmp.SnmpPDU {
		return pduInteger(fmt.Sprintf("%s.0.1.%d", oidLldpRemChassisIDSubtype, i+1), 7)
	})
	w.add(oidLldpRemSysName, lldpRows, func(i int) gosnmp.SnmpPDU {
		return pduString(fmt.Sprintf("%s.0.1.%d", oidLldpRemSysName, i+1), fmt.Sprintf("host-%d", i+1))
	})
	// dot1dTpFdbTable: 10,001 MACs learned on bridge port 1.
	fdbMAC := func(i int) string { return fmt.Sprintf("0.80.86.%d.%d.%d", i>>16, (i>>8)&0xff, i&0xff) }
	w.add(oidDot1dTpFdbPort, fdbRows, func(i int) gosnmp.SnmpPDU { return pduInteger(oidDot1dTpFdbPort+"."+fdbMAC(i), 1) })
	w.add(oidDot1dTpFdbStatus, fdbRows, func(i int) gosnmp.SnmpPDU { return pduInteger(oidDot1dTpFdbStatus+"."+fdbMAC(i), 3) })

	n := &neighborWalk{walk: w.walk, target: "192.0.2.1"}
	got := n.neighbors([]plugins.NetworkInterface{{Name: "GigabitEthernet1/0/1", IfIndex: 1}})
	if n.err != nil {
		t.Fatalf("walk error: %v", n.err)
	}
	var lldp, fdb int
	for _, nb := range got {
		if nb.LocalInterface != "GigabitEthernet1/0/1" {
			t.Fatalf("neighbor %+v: want local interface GigabitEthernet1/0/1", nb)
		}
		switch nb.Protocol {
		case "lldp":
			lldp++
		case "fdb":
			fdb++
		}
	}
	if lldp != maxTableEntries || fdb != maxTableEntries {
		t.Fatalf("neighbors: %d lldp, %d fdb; want %d each", lldp, fdb, maxTableEntries)
	}
	if last := got[len(got)-1].RemoteMAC; last != "00:50:56:00:27:0f" {
		t.Errorf("last FDB neighbour %s, want 00:50:56:00:27:0f (entry 10,000)", last)
	}
	// The walk stops at the first entry past the limit.
	if served := w.served[oidDot1dTpFdbPort]; served != fdbRows {
		t.Errorf("FDB port column: %d rows walked, want %d", served, fdbRows)
	}
	warnings := strings.Count(logs.String(), "level=WARN")
	if warnings != 1 || !strings.Contains(logs.String(), `msg="snmp: neighbour table truncated" target=192.0.2.1 table=dot1dTpFdbTable limit=10000`) {
		t.Errorf("want one truncation warning for dot1dTpFdbTable, got %d:\n%s", warnings, logs)
	}

	// Rows with an unreadable index (sub-identifier beyond 32 bits) count
	// towards the limit as well: an agent cannot stream past it.
	logs.Reset()
	junk := &tableWalker{columns: map[string][]gosnmp.SnmpPDU{}, served: map[string]int{}}
	junk.add(oidLldpRemChassisIDSubtype, fdbRows, func(i int) gosnmp.SnmpPDU {
		return pduInteger(fmt.Sprintf("%s.%d.1.1", oidLldpRemChassisIDSubtype, uint64(1)<<32+uint64(i)), 4)
	})
	m := &neighborWalk{walk: junk.walk, target: "192.0.2.2"}
	if got := m.neighbors(nil); len(got) != 0 || m.err != nil {
		t.Errorf("unreadable rows: neighbors %+v, error %v; want none", got, m.err)
	}
	if served := junk.served[oidLldpRemChassisIDSubtype]; served != fdbRows {
		t.Errorf("unreadable rows: %d walked, want %d", served, fdbRows)
	}
	if !strings.Contains(logs.String(), `msg="snmp: neighbour table truncated" target=192.0.2.2 table=lldpRemTable limit=10000`) {
		t.Errorf("no truncation warning for unreadable rows:\n%s", logs)
	}
}

// TestSNMPNeighborWalkFailure covers WP-416: a walk that fails (timeout)
// ends the neighbour reading with what was read before, with a warning;
// the later tables are not walked.
func TestSNMPNeighborWalkFailure(t *testing.T) {
	logs := captureLogs(t)
	w := &tableWalker{columns: map[string][]gosnmp.SnmpPDU{}, served: map[string]int{}}
	w.add(oidLldpRemChassisIDSubtype, 1, func(int) gosnmp.SnmpPDU { return pduInteger(oidLldpRemChassisIDSubtype+".0.1.1", 7) })
	w.add(oidLldpRemSysName, 1, func(int) gosnmp.SnmpPDU { return pduString(oidLldpRemSysName+".0.1.1", "dist-sw-2") })
	timeout := errors.New("request timeout (after 0 retries)")
	n := &neighborWalk{target: "192.0.2.1", walk: func(root string, fn gosnmp.WalkFunc) error {
		if root == oidDot1qTpFdbPort {
			return timeout
		}
		return w.walk(root, fn)
	}}
	got := n.neighbors(nil)
	want := []plugins.DeviceNeighbor{{Protocol: "lldp", RemoteHostname: "dist-sw-2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("neighbors = %+v, want %+v", got, want)
	}
	if w.served[oidDot1dTpFdbPort] != 0 || w.served[oidDot1dBasePortIfIndex] != 0 {
		t.Errorf("tables walked after the failure: %v", w.served)
	}
	if !strings.Contains(logs.String(), `msg="snmp: neighbour table not read" target=192.0.2.1 table=dot1qTpFdbTable`) {
		t.Errorf("no warning for the failed walk:\n%s", logs)
	}
}

// TestNeighborIndexValues covers the decoding of MACs and addresses in
// table indexes and LLDP IDs.
func TestNeighborIndexValues(t *testing.T) {
	for _, tc := range []struct {
		family int
		idx    []int
		want   string
	}{
		{1, []int{4, 192, 0, 2, 1}, "192.0.2.1"},
		{1, []int{192, 0, 2, 1}, "192.0.2.1"},
		{1, []int{3, 1, 2, 3}, "3.1.2.3"},
		{2, []int{16, 32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, "2001:db8::1"},
		{1, []int{4, 192, 0, 2, 1, 7}, ""},
		{1, []int{4, 192, 0, 2, 256}, ""},
		{3, []int{4, 192, 0, 2, 1}, ""},
	} {
		if got := indexAddress(tc.family, tc.idx); got != tc.want {
			t.Errorf("indexAddress(%d, %v) = %q, want %q", tc.family, tc.idx, got, tc.want)
		}
	}
	for idx, want := range map[string]string{
		"0.80.86.1.2.3":   "00:50:56:01:02:03",
		"1.0.94.0.0.1":    "", // multicast
		"0.0.0.0.0.0":     "",
		"0.80.86.1.2":     "",
		"0.80.86.1.2.300": "",
	} {
		parsed, ok := oidIndex("1.2."+idx, "1.2")
		if !ok {
			t.Fatalf("oidIndex(%s)", idx)
		}
		if got := indexMAC(parsed); got != want {
			t.Errorf("indexMAC(%s) = %q, want %q", idx, got, want)
		}
	}
	for in, want := range map[string]string{"\x00\x1b\x54\xaa\xbb\xcc": "00:1b:54:aa:bb:cc", "001b.54aa.bbcc": "00:1b:54:aa:bb:cc",
		"00-1B-54-AA-BB-CC": "00:1b:54:aa:bb:cc", "core-sw-1": "", "": ""} {
		if got := octetMAC([]byte(in)); got != want {
			t.Errorf("octetMAC(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := oidIndex(".1.3.6.1.2.1.17.4.3.1.20.1", oidDot1dTpFdbPort); ok {
		t.Error("oidIndex matched a neighbouring column")
	}
}
