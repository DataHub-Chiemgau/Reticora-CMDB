package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const (
	probeCommunity = "probe-ro"
	// sysObjectIDs of the fake agents: a generic Net-SNMP agent (no
	// monitor profile) and a Cisco IOS-XE router (profile cisco-ios-snmp).
	genericSysObjectID = ".1.3.6.1.4.1.8072.3.2.10"
	ciscoSysObjectID   = ".1.3.6.1.4.1.9.1.2494"
)

var probeV3User = &fakesnmp.V3{User: "probe", AuthProtocol: gosnmp.SHA256, AuthPassphrase: "auth-secret-1",
	PrivProtocol: gosnmp.AES, PrivPassphrase: "priv-secret-1"}

func probeV3Creds(auth string) map[string]string {
	return map[string]string{"username": "probe", "auth_protocol": "SHA256", "auth_password": auth,
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
}

// walkFile loads a walk fixture of testdata.
func walkFile(t *testing.T, name string) []gosnmp.SnmpPDU {
	t.Helper()
	pdus, err := fakesnmp.LoadWalkFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return pdus
}

// startSNMPAgent serves pdus on a fake agent with sysObjectID (SNMPv3
// with v3, SNMPv2c with probeCommunity otherwise) until the test ends.
func startSNMPAgent(t *testing.T, sysObjectID string, v3 *fakesnmp.V3, pdus ...gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	cfg := fakesnmp.Config{Community: probeCommunity, SysName: "pe-1", SysDescr: "Reticora probe fake", SysObjectID: sysObjectID}
	var (
		agent *fakesnmp.Agent
		err   error
	)
	if v3 != nil {
		cfg.Community = ""
		agent, err = fakesnmp.ListenV3("127.0.0.1:0", v3, cfg, pdus...)
	} else {
		agent, err = fakesnmp.Listen("127.0.0.1:0", cfg, pdus...)
	}
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

// snmpSpec is a spec of typ against the agent with params.
func snmpSpec(typ string, agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	t, _ := probewire.Lookup(typ)
	return probewire.ProbeSpec{ID: "p-" + typ, Type: typ, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: t.DefaultIntervalS * 1000, TimeoutMS: 2000, DownCount: 3, UpCount: 1, Epoch: 1,
		Params: json.RawMessage(params)}
}

var v2Creds = map[string]string{"community": probeCommunity}

func bfdState(peer string) string {
	return probewire.ValueKey(ValueBFDSessionState, map[string]string{LabelBFDPeer: peer})
}

func bfdDiag(peer, diag string) string {
	return probewire.ValueKey(ValueBFDDiag, map[string]string{LabelBFDPeer: peer, LabelBFDDiag: diag})
}

func intPDU(oid string, v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: v}
}

// TestBFDSessionUp covers AK1, AK4 and AK5 (PRT-08): every expected
// session up → ok, state and diagnostic per neighbor in the values.
func TestBFDSessionUp(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["192.0.2.1","192.0.2.2"]}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("result %+v, want ok", r)
	}
	for _, peer := range []string{"192.0.2.1", "192.0.2.2"} {
		if r.Values[bfdState(peer)] != 4 {
			t.Errorf("state of %s = %v, want up(4); values %v", peer, r.Values[bfdState(peer)], r.Values)
		}
		if v, ok := r.Values[bfdDiag(peer, "noDiagnostic")]; !ok || v != 0 {
			t.Errorf("diag of %s missing: %v", peer, r.Values)
		}
	}
	if _, ok := r.Values[bfdState("2001:db8::3")]; ok {
		t.Error("a session that is not expected is in the values")
	}
	if r.Values[ValueBFDSessions] != 2 || r.Values[ValueBFDSessionsDown] != 0 {
		t.Errorf("counts %v", r.Values)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestBFDSessionDownCarriesDiag covers AK4: a session down with the
// diagnostic controlDetectionTimeExpired → fail/session_down, the
// diagnostic name as label diag.
func TestBFDSessionDownCarriesDiag(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	agent.Set(intPDU(".1.3.6.1.2.1.222.1.2.1.11.2", 2), intPDU(".1.3.6.1.2.1.222.1.2.1.13.2", 1))
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["192.0.2.1","192.0.2.2"]}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown {
		t.Fatalf("result %+v, want fail/session_down", r)
	}
	if r.Values[bfdState("192.0.2.2")] != 2 {
		t.Errorf("state of 192.0.2.2 = %v, want down(2)", r.Values[bfdState("192.0.2.2")])
	}
	if v, ok := r.Values[bfdDiag("192.0.2.2", "controlDetectionTimeExpired")]; !ok || v != 1 {
		t.Errorf("diag controlDetectionTimeExpired missing: %v", r.Values)
	}
	if r.Values[ValueBFDSessionsDown] != 1 {
		t.Errorf("sessions down = %v, want 1", r.Values[ValueBFDSessionsDown])
	}
	name, labels, err := probewire.ParseValueKey(bfdDiag("192.0.2.2", "controlDetectionTimeExpired"))
	if err != nil || name != ValueBFDDiag || labels[LabelBFDDiag] != "controlDetectionTimeExpired" || labels[LabelBFDPeer] != "192.0.2.2" {
		t.Errorf("value key does not parse: %s %v %v", name, labels, err)
	}
}

// TestBFDExpectedSessionMissing covers AK4: an expected neighbor that the
// table lacks → fail/session_down.
func TestBFDExpectedSessionMissing(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["192.0.2.1","192.0.2.99"]}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown || r.Values[ValueBFDSessionsDown] != 1 {
		t.Fatalf("result %+v, want fail/session_down with one session down", r)
	}
}

// TestBFDAllIgnoresAdminDown covers AK4: with all: true a session
// adminDown is no failure, a session down is.
func TestBFDAllIgnoresAdminDown(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	agent.Set(intPDU(".1.3.6.1.2.1.222.1.2.1.11.3", 1))
	spec := snmpSpec(TypeBFD, agent, `{"all":true}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusOK || r.Values[ValueBFDSessions] != 3 || r.Values[bfdState("2001:db8::3")] != 1 {
		t.Fatalf("all with one adminDown: %+v, want ok over 3 sessions", r)
	}
	agent.Set(intPDU(".1.3.6.1.2.1.222.1.2.1.11.1", 3))
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown {
		t.Fatalf("all with one init: %+v, want fail/session_down", r)
	}
}

// TestBFDTimeoutAndAuth covers AK4: an SNMP timeout is fail/timeout, an
// SNMPv3 report refusing the credential error/auth_failed (→ unknown).
func TestBFDTimeoutAndAuth(t *testing.T) {
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close() //nolint:errcheck // test socket
	spec := probewire.ProbeSpec{ID: "p-bfd", Type: TypeBFD, Target: silent.LocalAddr().String(), IntervalMS: 60_000,
		TimeoutMS: 1000, DownCount: 3, UpCount: 1, Params: json.RawMessage(`{"all":true}`)}
	start := time.Now()
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("silent agent: %+v, want fail/timeout", r)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("silent agent took %s, want about the timeout", took)
	}

	agent := startSNMPAgent(t, genericSysObjectID, probeV3User, walkFile(t, "bfd-std-mib.walk")...)
	spec = snmpSpec(TypeBFD, agent, `{"all":true}`)
	if r := Run(context.Background(), &spec, probeV3Creds("wrong-auth-pw")); r.Status != probewire.StatusError || r.Code != probewire.ReasonAuthFailed {
		t.Fatalf("wrong v3 key: %+v, want error/auth_failed", r)
	}
	if r := Run(context.Background(), &spec, probeV3Creds("auth-secret-1")); r.Status != probewire.StatusOK {
		t.Fatalf("v3 with the right key: %+v, want ok", r)
	}
}

// TestBFDIPv6Neighbor covers AK1: an IPv6 neighbor (InetAddressType
// ipv6) is matched and labeled in its canonical form.
func TestBFDIPv6Neighbor(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["2001:0db8:0000::3"]}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusOK || r.Values[bfdState("2001:db8::3")] != 4 {
		t.Fatalf("IPv6 neighbor: %+v", r)
	}
	agent.Set(intPDU(".1.3.6.1.2.1.222.1.2.1.11.3", 2))
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown {
		t.Fatalf("IPv6 neighbor down: %+v", r)
	}
}

// TestBFDCiscoProfile covers AK2: a Cisco device is read from
// CISCO-IETF-BFD-MIB through monitor.bfd of the profile cisco-ios-snmp,
// with the enum mapping of the profile.
func TestBFDCiscoProfile(t *testing.T) {
	agent := startSNMPAgent(t, ciscoSysObjectID, nil, walkFile(t, "cisco-ietf-bfd.walk")...)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["198.51.100.10"]}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusOK || r.Values[bfdState("198.51.100.10")] != 4 {
		t.Fatalf("cisco session up: %+v", r)
	}
	spec = snmpSpec(TypeBFD, agent, `{"all":true}`)
	r = Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown {
		t.Fatalf("cisco session down: %+v, want fail/session_down", r)
	}
	if _, ok := r.Values[bfdDiag("198.51.100.11", "controlDetectionTimeExpired")]; !ok {
		t.Errorf("cisco diag not mapped through the profile: %v", r.Values)
	}
	// A Cisco device that leads the standard MIB: the empty vendor table
	// falls back to the core.
	std := startSNMPAgent(t, ciscoSysObjectID, nil, walkFile(t, "bfd-std-mib.walk")...)
	spec = snmpSpec(TypeBFD, std, `{"sessions":["192.0.2.1"]}`)
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusOK {
		t.Fatalf("cisco with BFD-STD-MIB: %+v, want ok from the core", r)
	}
}

// TestBFDNoTableIsUnsupported covers AK2: neither core nor profile table
// → error/unsupported (unknown, never down).
func TestBFDNoTableIsUnsupported(t *testing.T) {
	agent := startSNMPAgent(t, ciscoSysObjectID, nil)
	spec := snmpSpec(TypeBFD, agent, `{"all":true}`)
	r := Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("no BFD table: %+v, want error/unsupported", r)
	}
	sm := newStateMachine(t)
	spec.DownCount = 1
	if _, _, err := sm.Observe(&spec, r); err != nil {
		t.Fatal(err)
	}
	if c, _ := sm.Counters(spec.ID); c.State != probewire.StateUnknown {
		t.Fatalf("state %s, want unknown", c.State)
	}
}

// TestBFDLimits covers AK3: sessions with 1001 entries, sessions together
// with all and neither of them are field errors of probewire.Validate;
// a table of 1001 sessions truncates the walk (probe_bfd_truncated = 1)
// and a session behind the cut is no failure.
func TestBFDLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeBFD)
	if !ok || !typ.Available() || typ.MaxRows != BFDMaxRows || typ.MinIntervalS != 30 || typ.DefaultIntervalS != 60 ||
		typ.MaxTimeoutS != 30 || len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("bfd entry = %+v", typ)
	}
	many := make([]string, 1001)
	for i := range many {
		many[i] = fmt.Sprintf(`"10.0.%d.%d"`, i/250, i%250+1)
	}
	for name, params := range map[string]string{
		"1001 sessions":     `{"sessions":[` + strings.Join(many, ",") + `]}`,
		"sessions and all":  `{"sessions":["192.0.2.1"],"all":true}`,
		"neither":           `{}`,
		"no params":         ``,
		"empty sessions":    `{"sessions":[]}`,
		"all false":         `{"all":false}`,
		"not an address":    `{"sessions":["router-1"]}`,
		"old expected list": `{"expected_sessions":["192.0.2.1"],"all":true}`,
	} {
		spec := probewire.ProbeSpec{ID: "p", Type: TypeBFD, Target: "192.0.2.9", IntervalMS: 60_000, TimeoutMS: 5_000,
			DownCount: 3, UpCount: 1, Params: json.RawMessage(params)}
		errs := probewire.Validate(&spec)
		want := "params.sessions"
		switch name {
		case "not an address":
			want = "params.sessions[0]"
		case "old expected list":
			want = "params.expected_sessions"
		}
		found := false
		for _, e := range errs {
			found = found || e.Field == want
		}
		if !found {
			t.Errorf("%s: errs %v, want field %s", name, errs, want)
		}
	}
	valid := probewire.ProbeSpec{ID: "p", Type: TypeBFD, Target: "192.0.2.9", IntervalMS: 30_000, TimeoutMS: 29_000,
		DownCount: 3, UpCount: 1, Params: json.RawMessage(`{"sessions":["192.0.2.1","2001:db8::1"]}`)}
	if errs := probewire.Validate(&valid); len(errs) > 0 {
		t.Errorf("valid spec rejected: %v", errs)
	}

	// 1001 rows: the walk stops at 1000.
	var pdus []gosnmp.SnmpPDU
	for i := 1; i <= 1001; i++ {
		pdus = append(pdus, intPDU(fmt.Sprintf(".1.3.6.1.2.1.222.1.2.1.11.%d", i), 4),
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.2.1.222.1.2.1.22.%d", i), Type: gosnmp.OctetString,
				Value: []byte{10, 1, byte(i / 256), byte(i % 256)}})
	}
	agent := startSNMPAgent(t, genericSysObjectID, nil, pdus...)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["10.1.0.1","10.1.3.233"]}`) // rows 1 and 1001
	spec.TimeoutMS = 10_000
	r := Run(context.Background(), &spec, v2Creds)
	if r.Values[ValueBFDTruncated] != 1 {
		t.Fatalf("1001 rows: %+v, want probe_bfd_truncated = 1", r.Values[ValueBFDTruncated])
	}
	if r.Status != probewire.StatusOK || r.Values[ValueBFDSessions] != 1 {
		t.Fatalf("1001 rows: %s/%s with %v sessions, want ok over the session read (the one behind the cut is no failure)",
			r.Status, r.Code, r.Values[ValueBFDSessions])
	}
	if r.Values[ValueBFDSessionsUnseen] != 1 {
		t.Fatalf("1001 rows: probe_bfd_sessions_unseen = %v, want 1", r.Values[ValueBFDSessionsUnseen])
	}
	// Every expected session behind the cut: nothing read, so no up.
	spec = snmpSpec(TypeBFD, agent, `{"sessions":["10.1.3.233"]}`)
	spec.TimeoutMS = 10_000
	r = Run(context.Background(), &spec, v2Creds)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonResponseTooLarge || r.Values[ValueBFDTruncated] != 1 {
		t.Fatalf("only sessions behind the cut: %s/%s %v, want error/response_too_large", r.Status, r.Code, r.Values)
	}
}

// TestBFDZoneAndDuplicateSessions: an ipv6z neighbor (link-local with zone
// index, RFC 4001) matches an expected address with a zone; several rows of
// one neighbor (micro-BFD, one session per VRF) decide by the worst row,
// and the per-session values carry that row.
func TestBFDZoneAndDuplicateSessions(t *testing.T) {
	const col = ".1.3.6.1.2.1.222.1.2.1."
	ll := append(netip.MustParseAddr("fe80::1").AsSlice(), 0, 0, 0, 4)
	agent := startSNMPAgent(t, genericSysObjectID, nil,
		intPDU(col+"11.1", 4), intPDU(col+"11.2", 4), intPDU(col+"11.3", 2),
		intPDU(col+"13.1", 0), intPDU(col+"13.2", 0), intPDU(col+"13.3", 1),
		intPDU(col+"21.1", 4), intPDU(col+"21.2", 1), intPDU(col+"21.3", 1),
		gosnmp.SnmpPDU{Name: col + "22.1", Type: gosnmp.OctetString, Value: ll},
		gosnmp.SnmpPDU{Name: col + "22.2", Type: gosnmp.OctetString, Value: []byte{10, 2, 0, 1}},
		gosnmp.SnmpPDU{Name: col + "22.3", Type: gosnmp.OctetString, Value: []byte{10, 2, 0, 1}},
	)
	spec := snmpSpec(TypeBFD, agent, `{"sessions":["fe80::1%eth0"]}`)
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusOK || r.Values[bfdState("fe80::1")] != 4 {
		t.Fatalf("ipv6z neighbor: %s/%s %v, want ok with state up", r.Status, r.Code, r.Values)
	}
	for _, params := range []string{`{"sessions":["10.2.0.1"]}`, `{"all":true}`} {
		spec = snmpSpec(TypeBFD, agent, params)
		r := Run(context.Background(), &spec, v2Creds)
		if r.Status != probewire.StatusFail || r.Code != probewire.ReasonSessionDown {
			t.Fatalf("%s: %s/%s, want fail/session_down (one of two rows down)", params, r.Status, r.Code)
		}
		if r.Values[bfdState("10.2.0.1")] != 2 || r.Values[bfdDiag("10.2.0.1", "controlDetectionTimeExpired")] != 1 {
			t.Fatalf("%s: values %v, want the down row of 10.2.0.1", params, r.Values)
		}
	}
}
