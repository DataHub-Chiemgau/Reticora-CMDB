//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-158 (TOP-01; input of DIS-05 / SIM-01 topology).
//
// Finding: P2 "Shipped collector produces no LLDP/CDP/FDB neighbors (TOP-01
// input missing in production)": the SNMP plugin never walks lldpRemTable or
// the BRIDGE-MIB FDB, so DeviceRecord.Neighbors stays empty for real
// switches and the WP-158..160 topology logic only ever sees simulated data.
//
// The test serves a switch over the package's SNMPv3 test agent whose MIB
// carries one LLDP neighbour on GigabitEthernet1/0/1 and one learned FDB
// entry on bridge port 2 (ifIndex 2). The negative case (no LLDP/BRIDGE MIB)
// must stay without neighbours and without error.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditSNMPCollectsNeighbors' ./plugins/snmp/
package snmp

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func auditNeighborMIB() []gosnmp.SnmpPDU {
	s := func(oid, v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
	}
	i := func(oid string, v int) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
	}
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
		gosnmp.SnmpPDU{Name: ".1.0.8802.1.1.2.1.4.1.1.5.0.1.1", Type: gosnmp.OctetString, Value: []byte{0x00, 0x1b, 0x54, 0xaa, 0xbb, 0xcc}},
		i("1.0.8802.1.1.2.1.4.1.1.6.0.1.1", 5), // lldpRemPortIdSubtype interfaceName(5)
		s("1.0.8802.1.1.2.1.4.1.1.7.0.1.1", "TenGigabitEthernet1/1/1"),
		s("1.0.8802.1.1.2.1.4.1.1.8.0.1.1", "uplink core-sw-1"),
		s("1.0.8802.1.1.2.1.4.1.1.9.0.1.1", "dist-sw-2"),
		// BRIDGE-MIB: bridge port 2 = ifIndex 2; MAC 00:50:56:01:02:03 learned on port 2.
		i("1.3.6.1.2.1.17.1.4.1.2.2", 2),
		i("1.3.6.1.2.1.17.4.3.1.2.0.80.86.1.2.3", 2),
		i("1.3.6.1.2.1.17.4.3.1.3.0.80.86.1.2.3", 3), // dot1dTpFdbStatus learned(3)
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.17.4.3.1.1.0.80.86.1.2.3", Type: gosnmp.OctetString, Value: []byte{0x00, 0x50, 0x56, 0x01, 0x02, 0x03}},
	)
	sort.Slice(mib, func(a, b int) bool { return oidLess(mib[a].Name, mib[b].Name) })
	return mib
}

func TestAuditSNMPCollectsNeighbors(t *testing.T) {
	creds := map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "auth-secret-1",
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
	cases := []struct {
		name     string
		mib      []gosnmp.SnmpPDU
		wantLLDP bool
		wantFDB  bool
	}{
		{name: "switch with LLDP neighbour and FDB entry", mib: auditNeighborMIB(), wantLLDP: true, wantFDB: true},
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
		})
	}
}
