package snmp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

func TestEncodeOID(t *testing.T) {
	cases := []struct {
		oid  string
		want []byte
	}{
		// sysObjectID: 1.3 -> 43, then 6,1,2,1,1,2,0
		{"1.3.6.1.2.1.1.2.0", []byte{0x06, 0x08, 43, 6, 1, 2, 1, 1, 2, 0}},
		// Multi-byte component: 161 -> 0x81 0x21
		{"1.3.6.1.161", []byte{0x06, 0x05, 43, 6, 1, 0x81, 0x21}},
		{"", []byte{0x06, 0x01, 0x00}},
		{"1", []byte{0x06, 0x01, 0x00}},
	}
	for _, c := range cases {
		if got := encodeOID(c.oid); !bytes.Equal(got, c.want) {
			t.Errorf("encodeOID(%q) = %x, want %x", c.oid, got, c.want)
		}
	}
}

func TestBuildSNMPv2cGet(t *testing.T) {
	pkt := buildSNMPv2cGet("public", oidSysDescr)
	if len(pkt) < 10 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	if pkt[0] != 0x30 {
		t.Errorf("first byte = %#x, want 0x30 (SEQUENCE)", pkt[0])
	}
	if int(pkt[1]) != len(pkt)-2 {
		t.Errorf("declared length %d does not match actual %d", pkt[1], len(pkt)-2)
	}
	if !bytes.Contains(pkt, []byte("public")) {
		t.Error("packet does not contain community string")
	}
	if !bytes.Contains(pkt, []byte{0xA0}) {
		t.Error("packet does not contain GetRequest-PDU tag 0xA0")
	}
	// OID bytes for 1.3.6.1.2.1.1.1.0
	oidBytes := []byte{43, 6, 1, 2, 1, 1, 1, 0}
	if !bytes.Contains(pkt, oidBytes) {
		t.Errorf("packet does not contain encoded OID %x", oidBytes)
	}
}

func TestIsPrintable(t *testing.T) {
	if !isPrintable("PowerEdge R750 (v2.0)") {
		t.Error("expected printable ASCII to be accepted")
	}
	if isPrintable("bad\x00value") {
		t.Error("expected NUL byte to be rejected")
	}
	if isPrintable("bad\x80value") {
		t.Error("expected high byte to be rejected")
	}
}

// startSNMPFake binds UDP 161 and answers every SNMP GET with a scripted
// OctetString value.
func startSNMPFake(t *testing.T, value string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: defaultPort})
	if err != nil {
		t.Skipf("cannot bind udp 127.0.0.1:161: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 5 || buf[0] != 0x30 {
				continue
			}
			resp := make([]byte, 0, n+64)
			resp = append(resp, buf[:n]...) // echo the request (contains "public")
			resp = append(resp, 0x04, byte(len(value)))
			resp = append(resp, value...)
			conn.WriteToUDP(resp, addr)
		}
	}()
}

func TestDiscoverFindsSNMPDevice(t *testing.T) {
	startSNMPFake(t, "sysObjectID")

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Probe(context.Background(), []string{"127.0.0.1"}, map[string]string{"community": "public"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "network.device" {
		t.Errorf("CIType = %q, want network.device", r.CIType)
	}
	if r.Attributes["snmpReachable"] != true {
		t.Errorf("snmpReachable = %v, want true", r.Attributes["snmpReachable"])
	}
	if r.Attributes["snmpVersion"] != "v2c" {
		t.Errorf("snmpVersion = %v, want v2c", r.Attributes["snmpVersion"])
	}
}

func TestDiscoverSkipsSilentTarget(t *testing.T) {
	p := &Plugin{Timeout: 150 * time.Millisecond, Concurrency: 2}
	results, err := p.Probe(context.Background(), []string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for silent target, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Probe(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", results)
	}
}

func TestDiscoverCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Plugin{Timeout: 100 * time.Millisecond}
	results, err := p.Probe(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

// TestCollectFromSNMPFake collects the SNMPv2c fake of the real-stack test
// (internal/fakesnmp, answers its community only): the record carries the
// values of the response varbinds, and the community, which every v2c
// response echoes in its header, appears in no field or attribute (finding
// during WP-384: a hand-made parser returned the first printable
// OctetString of the response, the community, for every OID).
func TestCollectFromSNMPFake(t *testing.T) {
	const community = "fake-community-4711"
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "fake-sw-01",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	// The profile of the fake's sysObjectID gives the model.
	reg := profiles.NewRegistry()
	if err = reg.Register(profiles.Profile{Name: "fake-agent", Model: "SNMP fake", SysObjectIDs: []string{"1.3.6.1.4.1.8072.3.2.10"}}); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{Timeout: time.Second, Port: agent.Addr().Port, Profiles: reg}
	rec, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{"community": community})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if rec.Classification.CIType != "network.device" || rec.System.Name != "fake-sw-01" || rec.System.Description != "Reticora SNMP fake" ||
		rec.Classification.SysObjectID != "1.3.6.1.4.1.8072.3.2.10" {
		t.Errorf("CI type %q, name %q, sysDescr %q, sysObjectID %q; want network.device, the fake's values",
			rec.Classification.CIType, rec.System.Name, rec.System.Description, rec.Classification.SysObjectID)
	}
	if rec.Classification.Profile != "fake-agent" || rec.Classification.Model != "SNMP fake" {
		t.Errorf("profile %q, model %q; want fake-agent, SNMP fake", rec.Classification.Profile, rec.Classification.Model)
	}
	// What the fake does not serve (noSuchObject, no ifTable) stays empty.
	if rec.Identity.SerialNumber != "" || rec.Classification.Manufacturer != "" || rec.System.FirmwareVersion != "" ||
		rec.Attributes["sysLocation"] != "" || rec.Attributes["sysContact"] != "" || len(rec.Interfaces) != 0 {
		t.Errorf("serial %q, manufacturer %q, firmware %q, sysLocation %v, sysContact %v, interfaces %v; want empty",
			rec.Identity.SerialNumber, rec.Classification.Manufacturer, rec.System.FirmwareVersion,
			rec.Attributes["sysLocation"], rec.Attributes["sysContact"], rec.Interfaces)
	}
	if rec.Attributes["snmpVersion"] != "v2c" || rec.Attributes["protocol"] != "snmp" {
		t.Errorf("attributes = %v", rec.Attributes)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), community) {
		t.Errorf("the community is part of the record: %s", raw)
	}
	// The values came from the fake, asked with the community.
	seen := agent.Requests()
	if len(seen) == 0 {
		t.Fatal("the fake received no request")
	}
	for _, r := range seen {
		if r.Version != "2c" || r.Community != community || !r.Answered {
			t.Errorf("request %+v: want an answered v2c request with the community", r)
		}
	}
}

func TestCollectNameFallsBackToTarget(t *testing.T) {
	// Fake that never answers => all GETs time out => empty sysName.
	p := &Plugin{Timeout: 100 * time.Millisecond}
	res, _, err := p.collect(context.Background(), "192.0.2.55", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Name != "192.0.2.55" {
		t.Errorf("Name = %q, want target fallback", res.Name)
	}
}

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Retries != defaultRetries {
		t.Errorf("Retries = %d, want %d", p.Retries, defaultRetries)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.Name() != "snmp" {
		t.Errorf("Name() = %q, want snmp", p.Name())
	}
	if !strings.HasPrefix(oidSysDescr, "1.3.6.1.2.1.1.1") {
		t.Errorf("unexpected sysDescr OID constant %q", oidSysDescr)
	}
}

// TestPollReadsDecodedVarbinds covers WP-512 (MON-02): Poll takes the
// values from the decoded varbinds of one GET, Counter32, Counter64,
// INTEGER and numeric strings alike. What the device does not answer
// (noSuchObject) or answers with text gives no sample; the former
// hand-made parser read the last INTEGER-looking bytes of the response
// instead, for an unanswered UPS-MIB OID 33 from the OID itself.
func TestPollReadsDecodedVarbinds(t *testing.T) {
	const community = "poll-community"
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "ups-01",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.10.1", Type: gosnmp.Counter32, Value: uint(123456)},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.6.1", Type: gosnmp.Counter64, Value: uint64(9_000_000_000)},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.33.1.2.1.0", Type: gosnmp.Integer, Value: 2},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99.1.0", Type: gosnmp.OctetString, Value: []byte("42.5")},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99.2.0", Type: gosnmp.OctetString, Value: []byte("n/a")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	p := &Plugin{Timeout: time.Second, Port: agent.Addr().Port}
	samples, err := p.Poll(context.Background(), "127.0.0.1", map[string]string{"community": community}, []PollMetric{
		{Name: "in32", OID: "1.3.6.1.2.1.2.2.1.10.1"},
		{Name: "in64", OID: "1.3.6.1.2.1.31.1.1.1.6.1"},
		{Name: "battery_status", OID: "1.3.6.1.2.1.33.1.2.1.0", Labels: map[string]string{"device": "ups"}},
		{Name: "battery_runtime", OID: "1.3.6.1.2.1.33.1.2.3.0"},
		{Name: "numeric_text", OID: "1.3.6.1.4.1.99.1.0"},
		{Name: "text", OID: "1.3.6.1.4.1.99.2.0"},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	got := map[string]float64{}
	for _, s := range samples {
		got[s.Name] = s.Value
		if s.Labels["target"] != "127.0.0.1" || s.Labels["oid"] == "" {
			t.Errorf("sample %s labels = %v", s.Name, s.Labels)
		}
	}
	want := map[string]float64{"in32": 123456, "in64": 9e9, "battery_status": 2, "numeric_text": 42.5}
	if len(got) != len(want) {
		t.Errorf("samples = %v, want %v", got, want)
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %v, want %v", name, got[name], v)
		}
	}
	if requests := agent.Requests(); len(requests) != 1 || requests[0].PDU != gosnmp.GetRequest.String() {
		t.Errorf("requests = %+v, want one GET", requests)
	}

	// A silent target is an error, no sample.
	silent := &Plugin{Timeout: 150 * time.Millisecond}
	if s, err := silent.Poll(context.Background(), "192.0.2.1", nil, []PollMetric{{Name: "x", OID: "1.3.6.1.2.1.1.3.0"}}); err == nil || len(s) != 0 {
		t.Errorf("Poll of a silent target = %v, %v; want an error", s, err)
	}
}

// oidLess orders dotted OIDs numerically.
func oidLess(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "."), "."), strings.Split(strings.TrimPrefix(b, "."), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var x, y int
		fmt.Sscan(pa[i], &x)
		fmt.Sscan(pb[i], &y)
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func v3TestMIB() []gosnmp.SnmpPDU {
	s := func(oid, v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
	}
	i := func(oid string, v int) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
	}
	g := func(oid string, v uint) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Gauge32, Value: v}
	}
	mib := []gosnmp.SnmpPDU{
		s(oidSysDescr, "Cisco IOS XE Software, Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P"),
		{Name: "." + oidSysObjectID, Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.2494"},
		{Name: "." + oidSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(360000)},
		s(oidSysContact, "noc@example.com"),
		s(oidSysName, "core-sw-1"),
		s(oidSysLocation, "DC1 R1"),
		s(oidIfDescr+".1", "GigabitEthernet1/0/1"), s(oidIfDescr+".2", "GigabitEthernet1/0/2"),
		i(oidIfMtu+".1", 1500), i(oidIfMtu+".2", 9000),
		g(oidIfSpeed+".1", 1_000_000_000), g(oidIfSpeed+".2", 1_000_000_000),
		{Name: "." + oidIfPhysAddr + ".1", Type: gosnmp.OctetString, Value: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x01}},
		{Name: "." + oidIfPhysAddr + ".2", Type: gosnmp.OctetString, Value: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x02}},
		i(oidIfAdminSt+".1", 1), i(oidIfAdminSt+".2", 2),
		i(oidIfOperSt+".1", 1), i(oidIfOperSt+".2", 2),
		s(oidEntPhysFirmwareRev+".1", "17.9.4"),
		s(oidEntPhysSerialNum+".1", "FOC1234X0YZ"),
		s(oidEntPhysMfgName+".1", "Cisco Systems, Inc."),
		s(oidEntPhysModelName+".1", "C9300-48P"),
	}
	sort.Slice(mib, func(a, b int) bool { return oidLess(mib[a].Name, mib[b].Name) })
	return mib
}

// TestSNMPv3AuthPrivRoundTrip covers WP-152 (DIS-04): probe and collect a
// device over SNMPv3 authPriv (SHA-256/AES) including the interface table;
// credentials the agent refuses are reported as auth_failed (WP-492).
func TestSNMPv3AuthPrivRoundTrip(t *testing.T) {
	agent := startV3Agent(t, "reticora", "auth-secret-1", "priv-secret-1", v3TestMIB())
	creds := map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "auth-secret-1",
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
	p := &Plugin{Timeout: time.Second, Port: agent.port()}

	hits, err := p.Probe(context.Background(), []string{"127.0.0.1"}, creds)
	if err != nil || len(hits) != 1 || hits[0].Attributes["snmpVersion"] != "v3" {
		t.Fatalf("v3 probe = %+v, %v", hits, err)
	}
	rec, err := p.Collect(context.Background(), "127.0.0.1", creds)
	if err != nil {
		t.Fatalf("v3 collect: %v", err)
	}
	if rec.System.Name != "core-sw-1" || rec.Identity.SerialNumber != "FOC1234X0YZ" || rec.Classification.Model != "C9300-48P" ||
		rec.Classification.SysObjectID != "1.3.6.1.4.1.9.1.2494" || rec.System.FirmwareVersion != "17.9.4" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Attributes["snmpVersion"] != "v3" || rec.System.UptimeSeconds != 3600 {
		t.Errorf("attributes = %v", rec.Attributes)
	}
	if len(rec.Interfaces) != 2 {
		t.Fatalf("interfaces = %+v", rec.Interfaces)
	}
	first := rec.Interfaces[0]
	if first.Name != "GigabitEthernet1/0/1" || first.MAC != "00:11:22:33:44:01" || first.SpeedMbps != 1000 || first.MTU != 1500 ||
		first.OperStatus != "up" || rec.Interfaces[1].AdminStatus != "down" {
		t.Errorf("interfaces = %+v", rec.Interfaces)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the contract: %v", v)
	}

	// Wrong credentials (WP-492, DIS-08): an agent that refuses them with a
	// usmStatsWrongDigests report (RFC 3414 3.2) is found, and its
	// collection fails with auth_failed and the target; an agent that leaves
	// the request unanswered stays a silent target without error.
	rfc := startRFC3414Agent(t, oidUsmStatsWrongDigests, 0)
	refused := &Plugin{Timeout: time.Second, Port: rfc.port}
	if hits, err = refused.Probe(context.Background(), []string{"127.0.0.1"}, v3Bad); err != nil || len(hits) != 1 {
		t.Errorf("refused credentials: probe found %d devices, %v; want the answering agent", len(hits), err)
	}
	if _, err = refused.Collect(context.Background(), "127.0.0.1", v3Bad); !errors.Is(err, plugins.ErrAuthFailed) ||
		!strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "usmStatsWrongDigests") {
		t.Errorf("refused credentials: collect err = %v, want auth_failed with target and report", err)
	}
	quick := &Plugin{Timeout: 200 * time.Millisecond, Port: agent.port()}
	if hits, err = quick.Probe(context.Background(), []string{"127.0.0.1"}, v3Bad); err != nil || len(hits) != 0 {
		t.Errorf("unanswered request found %d devices, %v", len(hits), err)
	}
}

func TestSNMPv3RequiresAuthPriv(t *testing.T) {
	for name, creds := range map[string]map[string]string{
		"no privacy":       {"username": "u", "auth_password": "a-secret"},
		"no auth":          {"username": "u", "priv_password": "p-secret"},
		"version only":     {"version": "3"},
		"md5 refused":      {"username": "u", "auth_protocol": "MD5", "auth_password": "a", "priv_password": "p"},
		"des refused":      {"username": "u", "auth_password": "a", "priv_protocol": "DES", "priv_password": "p"},
		"unknown protocol": {"username": "u", "auth_protocol": "sha3", "auth_password": "a", "priv_password": "p"},
	} {
		if _, _, err := v3FromCreds(creds); err == nil {
			t.Errorf("%s: accepted %v", name, creds)
		}
		p := &Plugin{Timeout: 50 * time.Millisecond}
		if _, err := p.Probe(context.Background(), []string{"192.0.2.1"}, creds); err == nil {
			t.Errorf("%s: probe ran below authPriv", name)
		}
	}
	cfg, ok, err := v3FromCreds(map[string]string{"user": "u", "authKey": "a", "privKey": "p", "auth_protocol": "sha-512", "priv_protocol": "AES256"})
	if err != nil || !ok || cfg.authProto != gosnmp.SHA512 || cfg.privProto != gosnmp.AES256 {
		t.Errorf("alternative keys: %+v %v %v", cfg, ok, err)
	}
	if _, ok, _ = v3FromCreds(map[string]string{"community": "public"}); ok {
		t.Error("a community configures SNMPv2c")
	}
}

// startIfFake serves n interfaces (ifIndex 1..n, ifOperStatus up for odd
// indexes) with the 64-bit counters and ifName of the ifXTable, or with
// the 32-bit counters of the ifTable only (hc false).
func startIfFake(t *testing.T, community string, n int, hc bool) *fakesnmp.Agent {
	t.Helper()
	var mib []gosnmp.SnmpPDU
	for i := 1; i <= n; i++ {
		idx := fmt.Sprintf(".%d", i)
		mib = append(mib, gosnmp.SnmpPDU{Name: "." + oidIfOperSt + idx, Type: gosnmp.Integer, Value: 2 - i%2})
		if hc {
			mib = append(mib,
				gosnmp.SnmpPDU{Name: "." + oidIfName + idx, Type: gosnmp.OctetString, Value: []byte(fmt.Sprintf("Gi1/0/%d", i))},
				gosnmp.SnmpPDU{Name: "." + oidIfHCInOctets + idx, Type: gosnmp.Counter64, Value: uint64(i) * 5_000_000_000},
				gosnmp.SnmpPDU{Name: "." + oidIfHCOutOctets + idx, Type: gosnmp.Counter64, Value: uint64(i) * 7_000_000_000})
		} else {
			mib = append(mib,
				gosnmp.SnmpPDU{Name: "." + oidIfInOctets + idx, Type: gosnmp.Counter32, Value: uint(i * 1000)},
				gosnmp.SnmpPDU{Name: "." + oidIfOutOctets + idx, Type: gosnmp.Counter32, Value: uint(i * 2000)})
		}
	}
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "access-sw",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, mib...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	return agent
}

// TestPollInterfacesWalksTableWithLimit covers WP-512 (MON-02, F-339): the
// standard poll walks ifTable/ifXTable for every interface, at most
// MaxPollInterfaces (the first by ifIndex) per device, with the ifIndex
// and ifName labels and the 64-bit counters; a device without ifXTable
// gives the 32-bit counters; one without ifTable no samples and no error.
func TestPollInterfacesWalksTableWithLimit(t *testing.T) {
	const community = "poll-community"
	creds := map[string]string{"community": community}
	type key struct {
		name string
		idx  int
	}
	collect := func(t *testing.T, agent *fakesnmp.Agent) map[key]plugins.MetricSample {
		t.Helper()
		p := &Plugin{Timeout: time.Second, Port: agent.Addr().Port}
		samples, err := p.PollInterfaces(context.Background(), "127.0.0.1", creds)
		if err != nil {
			t.Fatalf("PollInterfaces: %v", err)
		}
		out := map[key]plugins.MetricSample{}
		for _, s := range samples {
			var idx int
			if _, err := fmt.Sscan(s.Labels["ifIndex"], &idx); err != nil || s.Labels["target"] != "127.0.0.1" {
				t.Fatalf("sample %+v: want the labels target and ifIndex", s)
			}
			k := key{s.Name, idx}
			if _, dup := out[k]; dup {
				t.Fatalf("sample %+v twice", s)
			}
			out[k] = s
		}
		return out
	}

	t.Run("300 interfaces, limit 256", func(t *testing.T) {
		agent := startIfFake(t, community, 300, true)
		got := collect(t, agent)
		if len(got) != 3*MaxPollInterfaces {
			t.Fatalf("samples = %d, want %d (3 per interface, %d interfaces)", len(got), 3*MaxPollInterfaces, MaxPollInterfaces)
		}
		for _, idx := range []int{1, 2, MaxPollInterfaces} {
			in, out, st := got[key{MetricIfInOctets, idx}], got[key{MetricIfOutOctets, idx}], got[key{MetricIfOperState, idx}]
			if in.Value != float64(idx)*5e9 || out.Value != float64(idx)*7e9 || st.Value != float64(2-idx%2) {
				t.Errorf("ifIndex %d: in %v, out %v, status %v", idx, in.Value, out.Value, st.Value)
			}
			if name := fmt.Sprintf("Gi1/0/%d", idx); in.Labels["ifName"] != name || st.Labels["ifName"] != name {
				t.Errorf("ifIndex %d: ifName %q/%q, want %q", idx, in.Labels["ifName"], st.Labels["ifName"], name)
			}
		}
		if _, ok := got[key{MetricIfOperState, MaxPollInterfaces + 1}]; ok {
			t.Errorf("interface %d polled beyond the limit", MaxPollInterfaces+1)
		}
		// Bounded load: GETBULK requests only, at most 6 per column
		// (ceil(256/50)) for status, name and the two counters.
		requests := agent.Requests()
		if len(requests) > 4*6 {
			t.Errorf("requests = %d, want at most 24", len(requests))
		}
		for _, r := range requests {
			if r.PDU != gosnmp.GetBulkRequest.String() || r.Community != community || !r.Answered {
				t.Errorf("request %+v: want an answered GETBULK with the community", r)
			}
		}
	})

	t.Run("no ifXTable", func(t *testing.T) {
		got := collect(t, startIfFake(t, community, 2, false))
		if len(got) != 6 {
			t.Fatalf("samples = %v, want 6", got)
		}
		if got[key{MetricIfInOctets, 2}].Value != 2000 || got[key{MetricIfOutOctets, 2}].Value != 4000 {
			t.Errorf("32-bit counters of ifIndex 2 = %v, %v", got[key{MetricIfInOctets, 2}].Value, got[key{MetricIfOutOctets, 2}].Value)
		}
		if _, named := got[key{MetricIfInOctets, 1}].Labels["ifName"]; named {
			t.Error("ifName label without ifXTable")
		}
	})

	t.Run("no ifTable", func(t *testing.T) {
		if got := collect(t, startIfFake(t, community, 0, true)); len(got) != 0 {
			t.Errorf("samples = %v, want none", got)
		}
	})
}
