package fakesnmp

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
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

// WP-542 (DIS-08): the SNMPv3 mode of the fake used by
// frontend/e2e-real/discovery-errors.spec.ts answers like an agent after
// RFC 3414 3.2 (startRFC3414Agent of plugins/snmp/v3_agent_test.go): a
// wrong key gets the report usmStatsWrongDigests, the right keys the
// response.

const (
	v3User = "audit-v3"
	v3Auth = "auth-secret-542"
	v3Priv = "priv-secret-542"
)

func startV3Agent(t *testing.T) *Agent {
	t.Helper()
	a, err := ListenV3("127.0.0.1:0", &V3{User: v3User, AuthProtocol: gosnmp.SHA256, AuthPassphrase: v3Auth, PrivProtocol: gosnmp.AES, PrivPassphrase: v3Priv},
		Config{SysDescr: testSysDescr, SysObjectID: testSysOID, SysName: testSysName})
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

// v3Keys are the USM parameters of user with the passphrases auth and priv
// (SHA-256, AES) for the fake's engine.
func v3Keys(t *testing.T, user, auth, priv string) *gosnmp.UsmSecurityParameters {
	t.Helper()
	sp := &gosnmp.UsmSecurityParameters{UserName: user, AuthenticationProtocol: gosnmp.SHA256, AuthenticationPassphrase: auth,
		PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: priv, AuthoritativeEngineID: engineID,
		AuthoritativeEngineBoots: engineBoots, AuthoritativeEngineTime: engineTime, Logger: gosnmp.NewLogger(nil)}
	if err := sp.InitSecurityKeys(); err != nil {
		t.Fatal(err)
	}
	return sp
}

// v3Get is an authPriv GET of sysName with msgID as a client with the keys
// sp sends it after the engine discovery.
func v3Get(t *testing.T, sp *gosnmp.UsmSecurityParameters, msgID uint32) []byte {
	t.Helper()
	pkt := &gosnmp.SnmpPacket{Version: gosnmp.Version3, MsgFlags: gosnmp.AuthPriv | gosnmp.Reportable,
		SecurityModel: gosnmp.UserSecurityModel, SecurityParameters: sp, MsgID: msgID, RequestID: msgID, MsgMaxSize: 65507,
		PDUType: gosnmp.GetRequest, ContextEngineID: engineID, Logger: gosnmp.NewLogger(nil),
		Variables: []gosnmp.SnmpPDU{{Name: OIDSysName, Type: gosnmp.Null}}}
	if err := sp.InitPacket(pkt); err != nil {
		t.Fatal(err)
	}
	out, err := pkt.MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// exchange sends msg to the agent and returns its answer, nil for none.
func exchange(t *testing.T, a *Agent, msg []byte) []byte {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, a.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// decodeReport decodes an unauthenticated SNMPv3 report.
func decodeReport(t *testing.T, raw []byte) *gosnmp.SnmpPacket {
	t.Helper()
	if raw == nil {
		t.Fatal("no answer, want a report")
	}
	pkt, err := (&gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.NoAuthNoPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "test", Logger: gosnmp.NewLogger(nil)}, Logger: gosnmp.NewLogger(nil)}).SnmpDecodePacket(raw)
	if err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	return pkt
}

func TestAgentV3ReportsWrongKeysAndAnswersTheUser(t *testing.T) {
	a := startV3Agent(t)
	report := func(raw []byte, msgID uint32, counter string) {
		t.Helper()
		pkt := decodeReport(t, raw)
		usm, _ := pkt.SecurityParameters.(*gosnmp.UsmSecurityParameters)
		if pkt.PDUType != gosnmp.Report || pkt.MsgID != msgID || pkt.MsgFlags&gosnmp.AuthNoPriv != 0 ||
			len(pkt.Variables) != 1 || pkt.Variables[0].Name != counter || usm == nil || usm.AuthoritativeEngineID != engineID ||
			usm.AuthoritativeEngineBoots != engineBoots || usm.AuthoritativeEngineTime != engineTime {
			t.Errorf("answer = %v msgID %d flags %v vars %+v usm %+v; want the unauthenticated report %s for msgID %d with the engine",
				pkt.PDUType, pkt.MsgID, pkt.MsgFlags, pkt.Variables, usm, counter, msgID)
		}
	}

	// Engine discovery: no user, no authentication, reportable.
	discovery, err := (&gosnmp.SnmpPacket{Version: gosnmp.Version3, MsgFlags: gosnmp.Reportable, SecurityModel: gosnmp.UserSecurityModel,
		SecurityParameters: &gosnmp.UsmSecurityParameters{Logger: gosnmp.NewLogger(nil)}, PDUType: gosnmp.GetRequest, MsgID: 11, RequestID: 11,
		MsgMaxSize: 65507, Logger: gosnmp.NewLogger(nil)}).MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	report(exchange(t, a, discovery), 11, OIDUsmStatsUnknownEngineIDs)

	// The user's name with a wrong authentication key, with both keys
	// wrong, and an unknown user: reports, no response.
	report(exchange(t, a, v3Get(t, v3Keys(t, v3User, "wrong-auth-pw", v3Priv), 12)), 12, OIDUsmStatsWrongDigests)
	report(exchange(t, a, v3Get(t, v3Keys(t, v3User, "wrong-auth-pw", "wrong-priv-pw"), 13)), 13, OIDUsmStatsWrongDigests)
	report(exchange(t, a, v3Get(t, v3Keys(t, "someone-else", v3Auth, v3Priv), 14)), 14, OIDUsmStatsUnknownUserNames)

	// The user's keys: the authPriv response with sysName.
	right := v3Keys(t, v3User, v3Auth, v3Priv)
	raw := exchange(t, a, v3Get(t, right, 15))
	if raw == nil {
		t.Fatal("the right keys got no answer")
	}
	resp, err := (&gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv,
		SecurityParameters: right.Copy(), Logger: gosnmp.NewLogger(nil)}).SnmpDecodePacket(raw)
	if err != nil {
		t.Fatalf("decode the response with the user's keys: %v", err)
	}
	if resp.PDUType != gosnmp.GetResponse || resp.MsgID != 15 || resp.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv || len(resp.Variables) != 1 ||
		resp.Variables[0].Name != OIDSysName || string(resp.Variables[0].Value.([]byte)) != testSysName {
		t.Errorf("response = %v msgID %d flags %v vars %+v; want the authPriv response with sysName %q", resp.PDUType, resp.MsgID, resp.MsgFlags, resp.Variables, testSysName)
	}

	// No community opens the agent in the SNMPv3 mode.
	for _, community := range []string{"public", ""} {
		v2c, merr := (&gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: community, PDUType: gosnmp.GetRequest, RequestID: 16,
			Variables: []gosnmp.SnmpPDU{{Name: OIDSysName, Type: gosnmp.Null}}, Logger: gosnmp.NewLogger(nil)}).MarshalMsg()
		if merr != nil {
			t.Fatal(merr)
		}
		if out := exchange(t, a, v2c); out != nil {
			t.Errorf("v2c GET with community %q answered with %d bytes", community, len(out))
		}
	}

	// The record: what each message carried and how it was answered.
	type seen struct{ version, user, pdu, report string }
	var got []seen
	for _, r := range a.Requests() {
		if !r.Answered && r.Version == "3" {
			t.Errorf("SNMPv3 request %+v unanswered", r)
		}
		got = append(got, seen{r.Version, r.User, r.PDU, r.Report})
	}
	want := []seen{
		{"3", "", "", "usmStatsUnknownEngineIDs"},
		{"3", v3User, "", "usmStatsWrongDigests"},
		{"3", v3User, "", "usmStatsWrongDigests"},
		{"3", "someone-else", "", "usmStatsUnknownUserNames"},
		{"3", v3User, gosnmp.GetRequest.String(), ""},
		{"2c", "", gosnmp.GetRequest.String(), ""},
		{"2c", "", gosnmp.GetRequest.String(), ""},
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests = %+v, want %+v", got, want)
	}
}

// TestCollectorSNMPv3SeesTheWrongKeyAndTheAnswer drives the SNMPv3 mode with
// the collector's SNMP plugin, as discovery-errors.spec.ts does: with a
// wrong key the probe keeps the answering agent and Collect fails with
// auth_failed, the target and usmStatsWrongDigests (DIS-08); with the
// right keys Collect reads the agent's sysName.
func TestCollectorSNMPv3SeesTheWrongKeyAndTheAnswer(t *testing.T) {
	a := startV3Agent(t)
	p := &snmp.Plugin{Port: a.Addr().Port, Timeout: 500 * time.Millisecond, Concurrency: 1}
	creds := func(auth, priv string) map[string]string {
		return map[string]string{"username": v3User, "auth_protocol": "SHA256", "auth_password": auth, "priv_protocol": "AES", "priv_password": priv}
	}
	ctx := context.Background()
	wrong := creds("wrong-auth-pw", "wrong-priv-pw")
	if hits, err := p.Probe(ctx, []string{"127.0.0.1"}, wrong); err != nil || len(hits) != 1 {
		t.Fatalf("Probe with a wrong key = %+v, %v; want the answering agent as hit", hits, err)
	}
	rec, err := p.Collect(ctx, "127.0.0.1", wrong)
	if !errors.Is(err, plugins.ErrAuthFailed) || plugins.ErrorCause(err) != "auth_failed" ||
		!strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "usmStatsWrongDigests") {
		t.Fatalf("Collect with a wrong key = %+v, %v; want auth_failed with the target and usmStatsWrongDigests", rec, err)
	}
	right := creds(v3Auth, v3Priv)
	if hits, perr := p.Probe(ctx, []string{"127.0.0.1"}, right); perr != nil || len(hits) != 1 {
		t.Fatalf("Probe with the right keys = %+v, %v", hits, perr)
	}
	rec, err = p.Collect(ctx, "127.0.0.1", right)
	if err != nil || rec == nil || rec.System.Name != testSysName || rec.Attributes["snmpVersion"] != "v3" {
		t.Fatalf("Collect with the right keys = %+v, %v; want sysName %q over SNMPv3", rec, err, testSysName)
	}
	var reports []string
	for _, r := range a.Requests() {
		if r.Report != "" {
			reports = append(reports, r.Report)
		}
	}
	if !slices.Contains(reports, "usmStatsWrongDigests") {
		t.Errorf("reports sent = %v, want usmStatsWrongDigests", reports)
	}
}

func TestListenRefusesV3BelowAuthPriv(t *testing.T) {
	for name, v3 := range map[string]*V3{
		"no user":         {AuthProtocol: gosnmp.SHA256, AuthPassphrase: "a", PrivProtocol: gosnmp.AES, PrivPassphrase: "p"},
		"no privacy":      {User: "u", AuthProtocol: gosnmp.SHA256, AuthPassphrase: "a"},
		"no auth":         {User: "u", PrivProtocol: gosnmp.AES, PrivPassphrase: "p"},
		"noauth protocol": {User: "u", AuthProtocol: gosnmp.NoAuth, AuthPassphrase: "a", PrivProtocol: gosnmp.AES, PrivPassphrase: "p"},
	} {
		if a, err := ListenV3("127.0.0.1:0", v3, Config{}); err == nil {
			_ = a.Close()
			t.Errorf("%s: ListenV3 accepted %+v", name, v3)
		}
	}
	if a, err := ListenV3("127.0.0.1:0", nil, Config{Community: testCommunity}); err == nil {
		_ = a.Close()
		t.Error("ListenV3 without SNMPv3 user succeeded")
	}
	if a, err := Listen("127.0.0.1:0", Config{}); err == nil {
		_ = a.Close()
		t.Error("Listen without community succeeded")
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
