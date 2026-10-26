package power

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// startV2cAgent answers SNMPv2c GET and GETBULK from mib.
func startV2cAgent(t *testing.T, mib map[string]gosnmp.SnmpPDU) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	oids := make([]string, 0, len(mib))
	for oid := range mib {
		oids = append(oids, oid)
	}
	less := func(a, b string) bool {
		pa, pb := strings.Split(a, "."), strings.Split(b, ".")
		for i := 0; i < len(pa) && i < len(pb); i++ {
			x, _ := strconv.Atoi(pa[i])
			y, _ := strconv.Atoi(pb[i])
			if x != y {
				return x < y
			}
		}
		return len(pa) < len(pb)
	}
	sort.Slice(oids, func(i, j int) bool { return less(oids[i], oids[j]) })
	go func() {
		buf := make([]byte, 65535)
		dec := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "public", Logger: gosnmp.NewLogger(nil)}
		for {
			n, peer, rerr := conn.ReadFromUDP(buf)
			if rerr != nil {
				return
			}
			req, derr := dec.SnmpDecodePacket(buf[:n])
			if derr != nil {
				continue
			}
			var vars []gosnmp.SnmpPDU
			switch req.PDUType {
			case gosnmp.GetRequest:
				for _, v := range req.Variables {
					name := strings.TrimPrefix(v.Name, ".")
					if pdu, ok := mib[name]; ok {
						vars = append(vars, pdu)
					} else {
						vars = append(vars, gosnmp.SnmpPDU{Name: v.Name, Type: gosnmp.NoSuchObject})
					}
				}
			case gosnmp.GetBulkRequest, gosnmp.GetNextRequest:
				start := strings.TrimPrefix(req.Variables[0].Name, ".")
				limit := max(int(req.MaxRepetitions), 1)
				for _, oid := range oids {
					if len(vars) == limit {
						break
					}
					if less(start, oid) {
						vars = append(vars, mib[oid])
					}
				}
				if len(vars) == 0 {
					vars = append(vars, gosnmp.SnmpPDU{Name: req.Variables[0].Name, Type: gosnmp.EndOfMibView})
				}
			default:
				continue
			}
			resp := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: req.Community, PDUType: gosnmp.GetResponse,
				RequestID: req.RequestID, Variables: vars}
			out, merr := resp.MarshalMsg()
			if merr == nil {
				_, _ = conn.WriteToUDP(out, peer)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// TestCollectReadsOutletMapping covers WP-161 (IMP-09): the PDU outlet
// names (APC rPDU2) come with the record, factory names are left out.
func TestCollectReadsOutletMapping(t *testing.T) {
	str := func(oid, v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
	}
	num := func(oid string, v int) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
	}
	names, states := "1.3.6.1.4.1.318.1.1.26.9.2.3.1.3", "1.3.6.1.4.1.318.1.1.26.9.2.3.1.5"
	mib := map[string]gosnmp.SnmpPDU{}
	for _, pdu := range []gosnmp.SnmpPDU{
		str(oidUPSIdentManufacturer, "APC"), str(oidUPSIdentModel, "AP8959 Rack PDU"), str(oidUPSIdentSerial, "ZA1234"),
		str(names+".1", "web01 PSU1"), str(names+".2", "Outlet 2"), str(names+".3", "db01 PSU2"),
		num(states+".1", 2), num(states+".2", 1), num(states+".3", 1),
	} {
		mib[strings.TrimPrefix(pdu.Name, ".")] = pdu
	}
	p := &Plugin{Timeout: time.Second, Port: startV2cAgent(t, mib)}
	rec, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{"community": "public"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Power == nil || rec.Power.DeviceType != "pdu" || len(rec.Power.Outlets) != 2 {
		t.Fatalf("power = %+v", rec.Power)
	}
	if o := rec.Power.Outlets; o[0].Name != "web01 PSU1" || o[0].State != "on" || o[1].Name != "db01 PSU2" || o[1].State != "off" {
		t.Errorf("outlets = %+v", o)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the contract: %v", v)
	}
	for name, want := range map[string]bool{"Outlet 12": true, "outlet_3": true, "Outlet": true, "Outlet Group 3": true,
		"web01": false, "Outlet web": false, "group-a": false} {
		if isDefaultOutletName(name) != want {
			t.Errorf("isDefaultOutletName(%q) != %v", name, want)
		}
	}
}

// loadWalk reads a fixture walk (snmpwalk -On: "OID = TYPE: value") from
// testdata as the MIB of the SNMP fake; STRING and INTEGER values.
func loadWalk(t *testing.T, name string) map[string]gosnmp.SnmpPDU {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	mib := map[string]gosnmp.SnmpPDU{}
	for n, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		oid, typed, ok := strings.Cut(line, " = ")
		kind, value, ok2 := strings.Cut(typed, ": ")
		if !ok || !ok2 {
			t.Fatalf("%s:%d: %q is no walk line", name, n+1, line)
		}
		oid = strings.TrimPrefix(oid, ".")
		switch kind {
		case "STRING":
			mib[oid] = gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(strings.Trim(value, `"`))}
		case "INTEGER":
			v, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("%s:%d: %v", name, n+1, err)
			}
			mib[oid] = gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
		default:
			t.Fatalf("%s:%d: type %s not supported", name, n+1, kind)
		}
	}
	return mib
}

// TestCollectReadsVendorOutletTables covers WP-161/WP-498 (IMP-09): the
// outlet names and states of Raritan PX (PDU2-MIB), APC sPDU (PowerNet-MIB)
// and the outlet groups of an APC UPS, read from fixture walks of these
// devices; factory and empty names are left out. A device without an
// outlet table has no outlets and no error.
func TestCollectReadsVendorOutletTables(t *testing.T) {
	type outlet struct{ name, state string }
	for _, tc := range []struct {
		walk       string
		deviceType string
		want       []outlet
	}{
		{"raritan-px.walk", "pdu", []outlet{{"esx01 PSU1", "on"}, {"esx02 PSU1", "off"}}},
		{"apc-spdu.walk", "pdu", []outlet{{"fw01-A", "on"}, {"nas01 #2", "off"}}},
		{"apc-ups.walk", "ups", []outlet{{"pdu-a in1", "on"}, {"pdu-b in1", "off"}}},
		{"ups-no-outlets.walk", "ups", nil},
	} {
		t.Run(tc.walk, func(t *testing.T) {
			p := &Plugin{Timeout: time.Second, Port: startV2cAgent(t, loadWalk(t, tc.walk))}
			rec, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{"community": "public"})
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if rec.Power == nil || rec.Power.DeviceType != tc.deviceType {
				t.Fatalf("power = %+v, want device type %s", rec.Power, tc.deviceType)
			}
			var got []outlet
			for _, o := range rec.Power.Outlets {
				got = append(got, outlet{o.Name, o.State})
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("outlets = %v, want %v", got, tc.want)
			}
			if v := rec.Validate(); len(v) > 0 {
				t.Errorf("record breaks the contract: %v", v)
			}
		})
	}
}
