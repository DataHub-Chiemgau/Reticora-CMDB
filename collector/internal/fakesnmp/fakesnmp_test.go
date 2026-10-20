package fakesnmp

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
)

// WP-384: the SNMP fake of the real-stack test answers only its community;
// the test relies on that and on the record of every community received.

const (
	testCommunity = "fake-community-1"
	testSysName   = "fake-snmp-01"
	testSysDescr  = "Reticora SNMP fake"
	testSysOID    = ".1.3.6.1.4.1.8072.3.2.10"
)

func startAgent(t *testing.T) *Agent {
	t.Helper()
	a, err := Listen("127.0.0.1:0", Config{Community: testCommunity, SysDescr: testSysDescr, SysObjectID: testSysOID, SysName: testSysName})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve() }()
	t.Cleanup(func() {
		_ = a.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return a
}

func client(t *testing.T, a *Agent, community string) *gosnmp.GoSNMP {
	t.Helper()
	g := &gosnmp.GoSNMP{Target: "127.0.0.1", Port: uint16(a.Addr().Port), Transport: "udp", Community: community,
		Version: gosnmp.Version2c, Timeout: 300 * time.Millisecond, Retries: 0, MaxOids: gosnmp.MaxOids, Context: context.Background()}
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Conn.Close() })
	return g
}

func TestAgentAnswersItsCommunity(t *testing.T) {
	a := startAgent(t)
	g := client(t, a, testCommunity)

	pkt, err := g.Get([]string{OIDSysDescr, OIDSysObjectID, OIDSysName, ".1.3.6.1.2.1.1.6.0"})
	if err != nil {
		t.Fatalf("GET with the right community: %v", err)
	}
	want := []struct {
		typ   gosnmp.Asn1BER
		value string
	}{{gosnmp.OctetString, testSysDescr}, {gosnmp.ObjectIdentifier, testSysOID}, {gosnmp.OctetString, testSysName}, {gosnmp.NoSuchObject, ""}}
	if len(pkt.Variables) != len(want) {
		t.Fatalf("GET returned %d variables, want %d", len(pkt.Variables), len(want))
	}
	for i, v := range pkt.Variables {
		got := ""
		switch val := v.Value.(type) {
		case []byte:
			got = string(val)
		case string:
			got = val
		}
		if v.Type != want[i].typ || got != want[i].value {
			t.Errorf("%s = %v %q, want %v %q", v.Name, v.Type, got, want[i].typ, want[i].value)
		}
	}

	// GETNEXT and GETBULK walk exactly the three OIDs.
	var walked []string
	if err = g.BulkWalk(".1.3.6.1.2.1.1", func(v gosnmp.SnmpPDU) error {
		walked = append(walked, v.Name)
		return nil
	}); err != nil {
		t.Fatalf("BulkWalk: %v", err)
	}
	if want := []string{OIDSysDescr, OIDSysObjectID, OIDSysName}; !equal(walked, want) {
		t.Errorf("BulkWalk = %v, want %v", walked, want)
	}
	next, err := g.GetNext([]string{OIDSysName})
	if err != nil || len(next.Variables) != 1 || next.Variables[0].Type != gosnmp.EndOfMibView {
		t.Errorf("GETNEXT after sysName = %+v, %v; want endOfMibView", next, err)
	}

	for _, r := range a.Requests() {
		if r.Version != "2c" || r.Community != testCommunity || !r.Answered {
			t.Errorf("request %+v: want an answered v2c request with the agent's community", r)
		}
	}
}

func TestAgentIgnoresAnotherCommunity(t *testing.T) {
	a := startAgent(t)
	for _, community := range []string{"public", "fake-community-2", ""} {
		if _, err := client(t, a, community).Get([]string{OIDSysName}); err == nil {
			t.Errorf("community %q: the agent answered", community)
		}
	}
	// Every request is recorded with its community, none was answered.
	got := a.Requests()
	if len(got) != 3 {
		t.Fatalf("recorded %d requests, want 3: %+v", len(got), got)
	}
	for i, community := range []string{"public", "fake-community-2", ""} {
		if got[i].Version != "2c" || got[i].Community != community || got[i].Answered || got[i].PDU != gosnmp.GetRequest.String() {
			t.Errorf("request %d = %+v, want an unanswered GET with community %q", i, got[i], community)
		}
	}
}

func TestAgentIgnoresOtherMessages(t *testing.T) {
	a := startAgent(t)
	conn, err := net.DialUDP("udp", nil, a.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Garbage, an SNMPv1 GET with the right community and an SNMPv3
	// engine discovery: no answer.
	v1, err := (&gosnmp.SnmpPacket{Version: gosnmp.Version1, Community: testCommunity, PDUType: gosnmp.GetRequest, RequestID: 7,
		Variables: []gosnmp.SnmpPDU{{Name: OIDSysName, Type: gosnmp.Null}}, Logger: gosnmp.NewLogger(nil)}).MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	v3, err := (&gosnmp.SnmpPacket{Version: gosnmp.Version3, MsgFlags: gosnmp.Reportable, SecurityModel: gosnmp.UserSecurityModel,
		SecurityParameters: &gosnmp.UsmSecurityParameters{Logger: gosnmp.NewLogger(nil)}, PDUType: gosnmp.GetRequest, MsgID: 1, RequestID: 8,
		MsgMaxSize: 65507, Logger: gosnmp.NewLogger(nil)}).MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range [][]byte{[]byte("not snmp"), v1, v3} {
		if _, err = conn.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, rerr := conn.Read(make([]byte, 1500)); rerr == nil {
			t.Errorf("answer of %d bytes to %q", n, msg)
		}
	}
	want := []Request{{Version: ""}, {Version: "1", Community: testCommunity}, {Version: "3"}}
	got := a.Requests()
	if len(got) != len(want) {
		t.Fatalf("recorded %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Version != want[i].Version || got[i].Community != want[i].Community || got[i].Answered {
			t.Errorf("request %d = %+v, want version %q community %q unanswered", i, got[i], want[i].Version, want[i].Community)
		}
	}
}

// TestCollectorProbeSeesTheAgentWithItsCommunityOnly drives the agent with
// the collector's SNMP plugin, which builds its v2c requests itself: the
// probe finds the agent with the right community only.
func TestCollectorProbeSeesTheAgentWithItsCommunityOnly(t *testing.T) {
	a := startAgent(t)
	p := &snmp.Plugin{Port: a.Addr().Port, Timeout: 300 * time.Millisecond, Concurrency: 1}
	for _, tc := range []struct {
		creds map[string]string
		hits  int
	}{
		{map[string]string{"community": testCommunity}, 1},
		{map[string]string{"community": "fake-community-2"}, 0},
		{nil, 0}, // the plugin's default community "public"
	} {
		hits, err := p.Probe(context.Background(), []string{"127.0.0.1"}, tc.creds)
		if err != nil {
			t.Fatalf("Probe(%v): %v", tc.creds, err)
		}
		if len(hits) != tc.hits {
			t.Errorf("Probe(%v) = %d hits, want %d", tc.creds, len(hits), tc.hits)
		}
	}
	var communities []string
	for _, r := range a.Requests() {
		communities = append(communities, r.Community+"/"+strconv.FormatBool(r.Answered))
	}
	if want := []string{testCommunity + "/true", "fake-community-2/false", "public/false"}; !equal(communities, want) {
		t.Errorf("requests = %v, want %v", communities, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
