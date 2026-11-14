package collectorcmd

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The trap receiver configured by the server per collector group (DIS-06,
// WP-977).

// v2cTrap encodes a minimal SNMPv2c trap with the community.
func v2cTrap(community string) []byte {
	tlv := func(tag byte, body []byte) []byte {
		return append([]byte{tag, byte(len(body))}, body...)
	}
	oid := []byte{0x2b, 6, 1, 6, 3, 1, 1, 4, 1, 0} // 1.3.6.1.6.3.1.1.4.1.0 snmpTrapOID.0
	val := []byte{0x2b, 6, 1, 6, 3, 1, 1, 5, 3}    // 1.3.6.1.6.3.1.1.5.3 linkDown
	vb := tlv(0x30, append(tlv(0x06, oid), tlv(0x06, val)...))
	pdu := tlv(0xa7, append(append(append(tlv(0x02, []byte{1}), tlv(0x02, []byte{0})...), tlv(0x02, []byte{0})...),
		tlv(0x30, vb)...))
	msg := append(append(tlv(0x02, []byte{1}), tlv(0x04, []byte(community))...), pdu...)
	return tlv(0x30, msg)
}

// trapSink collects the traps handed on.
type trapSink struct {
	mu  sync.Mutex
	got []snmp.TrapEvent
}

func (s *trapSink) sink(_ context.Context, ev snmp.TrapEvent) { //nolint:gocritic // signature of the sink
	s.mu.Lock()
	s.got = append(s.got, ev)
	s.mu.Unlock()
}

func (s *trapSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// freePort returns a UDP port of the loopback that is free now.
func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

// newTrapManager returns a manager with a fixed community table.
func newTrapManager(t *testing.T, communities map[string]string) (*trapManager, *trapSink, *bytes.Buffer) {
	t.Helper()
	s := &trapSink{}
	var logs bytes.Buffer
	m := &trapManager{sink: s.sink, logger: slog.New(slog.NewTextHandler(&logs, nil)),
		communities: func(ids []string) []string {
			var out []string
			for _, id := range ids {
				if c, ok := communities[id]; ok {
					out = append(out, c)
				}
			}
			return out
		}}
	t.Cleanup(m.stop)
	return m, s, &logs
}

// boundPort waits until the receiver is bound and returns its port.
func boundPort(t *testing.T, m *trapManager) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a := m.localAddr(); a != nil {
			return a.(*net.UDPAddr).Port
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("trap receiver not bound")
	return 0
}

// unbound waits until no receiver is bound any more.
func unbound(t *testing.T, m *trapManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.localAddr() == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("trap receiver still bound")
}

// sendTrap sends a trap to the loopback port and waits until want traps
// reached the sink.
func sendTrap(t *testing.T, port int, community string) {
	t.Helper()
	c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(v2cTrap(community)); err != nil {
		t.Fatal(err)
	}
}

func trapWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTrapReceiverFollowsAssignmentSet covers AK4: the receiver starts with
// the first set, binds again on a changed port and stops with enabled
// false, without a restart.
func TestTrapReceiverFollowsAssignmentSet(t *testing.T) {
	m, s, _ := newTrapManager(t, map[string]string{"cred-a": "alpha"})
	m.start(context.Background(), &collectorConfig{}, nil)
	if m.localAddr() != nil {
		t.Fatal("receiver bound without configuration")
	}
	p1 := freePort(t)
	m.apply(&probewire.TrapConfig{Enabled: true, Port: p1, CredentialIDs: []string{"cred-a"}})
	if got := boundPort(t, m); got != p1 {
		t.Fatalf("bound to %d, want %d", got, p1)
	}
	sendTrap(t, p1, "alpha")
	trapWaitFor(t, "the trap on the first port", func() bool { return s.count() == 1 })

	p2 := freePort(t)
	m.apply(&probewire.TrapConfig{Enabled: true, Port: p2, CredentialIDs: []string{"cred-a"}})
	if got := boundPort(t, m); got != p2 {
		t.Fatalf("after the port change bound to %d, want %d", got, p2)
	}
	// The first port is free again.
	c, err := net.ListenPacket("udp", ":"+strconv.Itoa(p1))
	if err != nil {
		t.Fatalf("old port %d still bound: %v", p1, err)
	}
	_ = c.Close()
	sendTrap(t, p2, "alpha")
	trapWaitFor(t, "the trap on the second port", func() bool { return s.count() == 2 })

	m.apply(&probewire.TrapConfig{Enabled: false, Port: p2})
	unbound(t, m)
	c, err = net.ListenPacket("udp", ":"+strconv.Itoa(p2))
	if err != nil {
		t.Fatalf("port %d still bound after enabled false: %v", p2, err)
	}
	_ = c.Close()
}

// TestTrapUnknownCommunityDroppedAndCounted covers AK4 and AK3: a trap with
// a community of no configured credential is dropped and counted under
// community; without a snmp_v2c credential every trap is dropped; an empty
// community accepts nothing in this mode.
func TestTrapUnknownCommunityDroppedAndCounted(t *testing.T) {
	m, s, _ := newTrapManager(t, map[string]string{"cred-a": "alpha"})
	m.start(context.Background(), &collectorConfig{}, nil)
	port := freePort(t)
	m.apply(&probewire.TrapConfig{Enabled: true, Port: port, CredentialIDs: []string{"cred-a"}})
	boundPort(t, m)
	sendTrap(t, port, "alpha")
	sendTrap(t, port, "beta")
	sendTrap(t, port, "")
	trapWaitFor(t, "three traps", func() bool { r, _ := m.stats(); return r == 3 })
	received, dropped := m.stats()
	if s.count() != 1 || received != 3 || dropped[probewire.TrapDropCommunity] != 2 {
		t.Fatalf("handed on %d, received %d, dropped %v; want 1, 3 and community 2", s.count(), received, dropped)
	}
	// No snmp_v2c credential at all: every v1/v2c trap is dropped.
	m.apply(&probewire.TrapConfig{Enabled: true, Port: port})
	sendTrap(t, port, "alpha")
	trapWaitFor(t, "the fourth trap", func() bool { r, _ := m.stats(); return r == 4 })
	if _, dropped = m.stats(); s.count() != 1 || dropped[probewire.TrapDropCommunity] != 3 {
		t.Fatalf("without credential: handed on %d, dropped %v", s.count(), dropped)
	}
}

// TestTrapEnvFallbackWithoutServerConfig covers AK5: without a set with
// trap_config RETICORA_SNMP_TRAP_LISTEN starts the receiver with the local
// community, as before; a set without trap_config keeps it.
func TestTrapEnvFallbackWithoutServerConfig(t *testing.T) {
	m, s, _ := newTrapManager(t, nil)
	port := freePort(t)
	cfg := &collectorConfig{TrapListenAddr: "127.0.0.1:" + strconv.Itoa(port),
		Credentials: map[string]string{"snmp.community": "local"}}
	m.start(context.Background(), cfg, nil)
	if got := boundPort(t, m); got != port {
		t.Fatalf("fallback bound to %d, want %d", got, port)
	}
	m.apply(nil) // a set of a server without trap_config
	if got := boundPort(t, m); got != port {
		t.Fatalf("after a set without trap_config bound to %d, want %d", got, port)
	}
	sendTrap(t, port, "local")
	sendTrap(t, port, "other") // the local community filters as before
	trapWaitFor(t, "two traps", func() bool { r, _ := m.stats(); return r >= 1 })
	time.Sleep(100 * time.Millisecond)
	if s.count() != 1 {
		t.Fatalf("handed on %d traps, want the one with the local community", s.count())
	}
}

// TestServerConfigReplacesEnvReceiver covers AK5: the first set with
// trap_config replaces the receiver of the environment (one bind per port)
// and logs it; enabled false stops the environment receiver as well.
func TestServerConfigReplacesEnvReceiver(t *testing.T) {
	m, s, logs := newTrapManager(t, map[string]string{"cred-a": "alpha"})
	port := freePort(t)
	m.start(context.Background(), &collectorConfig{TrapListenAddr: ":" + strconv.Itoa(port)}, nil)
	boundPort(t, m)
	// The same port from the server: the environment receiver gives it up.
	m.apply(&probewire.TrapConfig{Enabled: true, Port: port, CredentialIDs: []string{"cred-a"}})
	if got := boundPort(t, m); got != port {
		t.Fatalf("bound to %d, want %d", got, port)
	}
	if !strings.Contains(logs.String(), "server configuration replaces RETICORA_SNMP_TRAP_LISTEN") {
		t.Fatalf("log lacks the replacement notice:\n%s", logs.String())
	}
	sendTrap(t, port, "beta") // the environment would have accepted any community
	sendTrap(t, port, "alpha")
	trapWaitFor(t, "two traps", func() bool { r, _ := m.stats(); return r == 2 })
	if s.count() != 1 {
		t.Fatalf("handed on %d, want 1 (server communities only)", s.count())
	}
	m.apply(&probewire.TrapConfig{Enabled: false})
	unbound(t, m)
}

// TestServerConfigDisablingEnvReceiverWarns covers AK5 for an upgrade: a
// first set whose group switches the receiver off stops the environment
// receiver and says so with a warning (the operator relied on it).
func TestServerConfigDisablingEnvReceiverWarns(t *testing.T) {
	m, _, logs := newTrapManager(t, nil)
	port := freePort(t)
	m.start(context.Background(), &collectorConfig{TrapListenAddr: ":" + strconv.Itoa(port)}, nil)
	boundPort(t, m)
	m.apply(&probewire.TrapConfig{Enabled: false, Port: probewire.DefaultTrapPort})
	unbound(t, m)
	if !strings.Contains(logs.String(), "the collector group disables the trap receiver") {
		t.Fatalf("log lacks the warning:\n%s", logs.String())
	}
}

// v3Trap builds an SNMPv3 linkDown trap (SHA-256/AES-128) of user with
// gosnmp as the sender.
func v3Trap(t *testing.T, user, authPass, privPass string) []byte {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	defer func() { _ = ln.Close() }()
	g := &gosnmp.GoSNMP{
		Target: "127.0.0.1", Port: uint16(ln.LocalAddr().(*net.UDPAddr).Port), Transport: "udp", //nolint:gosec // a port
		Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, Timeout: time.Second,
		SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: user, AuthoritativeEngineID: "\x80\x00\x1f\x88\x80collectorcmd",
			AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 100,
			AuthenticationProtocol: gosnmp.SHA256, AuthenticationPassphrase: authPass,
			PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: privPass},
	}
	if cerr := g.Connect(); cerr != nil {
		t.Fatal(cerr)
	}
	defer func() { _ = g.Conn.Close() }()
	if _, serr := g.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.6.3.1.1.5.3"},
	}}); serr != nil {
		t.Fatal(serr)
	}
	buf := make([]byte, 4096)
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buf[:n]...)
}

// TestTrapV3UsersFromCredentials covers WP-978 AK1, AK3 and AK4: the
// snmp_v3 credentials of trap_config are the USM users; a valid v3 trap is
// handed on with the user name, a wrong key and an unknown user are
// dropped and counted next to community.
func TestTrapV3UsersFromCredentials(t *testing.T) {
	m, s, _ := newTrapManager(t, map[string]string{"cred-a": "alpha"})
	m.v3Users = func(ids []string) []map[string]string {
		var out []map[string]string
		for _, id := range ids {
			if id == "cred-v3" {
				out = append(out, map[string]string{"username": "monitor", "auth_protocol": "sha256",
					"auth_password": "auth-secret-978", "priv_protocol": "aes", "priv_password": "priv-secret-978"})
			}
		}
		return out
	}
	m.start(context.Background(), &collectorConfig{}, nil)
	port := freePort(t)
	m.apply(&probewire.TrapConfig{Enabled: true, Port: port, CredentialIDs: []string{"cred-a", "cred-v3"}})
	boundPort(t, m)
	c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, pkt := range [][]byte{
		v3Trap(t, "monitor", "auth-secret-978", "priv-secret-978"),
		v3Trap(t, "monitor", "wrong-secret-978", "priv-secret-978"),
		v3Trap(t, "stranger", "auth-secret-978", "priv-secret-978"),
	} {
		if _, err := c.Write(pkt); err != nil {
			t.Fatal(err)
		}
	}
	trapWaitFor(t, "three v3 traps", func() bool { r, _ := m.stats(); return r == 3 })
	received, dropped := m.stats()
	if s.count() != 1 || received != 3 || dropped[snmp.TrapDropAuth] != 1 || dropped[snmp.TrapDropUnknownUser] != 1 {
		t.Fatalf("handed on %d, received %d, dropped %v", s.count(), received, dropped)
	}
	s.mu.Lock()
	ev := s.got[0]
	s.mu.Unlock()
	if ev.User != "monitor" || ev.TrapOID != "1.3.6.1.6.3.1.1.5.3" || ev.Community != "" {
		t.Fatalf("event = %+v", ev)
	}
}

// TestFallbackModeKeepsMetricPath covers WP-979 AK2: in the monitoring
// mode (a set with trap_config) an accepted trap becomes an event of the
// engine with a UUIDv7 and the kind link_down; in the fallback mode
// without server configuration the metric path (sink) stays.
func TestFallbackModeKeepsMetricPath(t *testing.T) {
	m, s, _ := newTrapManager(t, map[string]string{"cred-a": "alpha"})
	var mu sync.Mutex
	var events []probewire.Event
	m.setMonitor(func(ev probewire.Event) { mu.Lock(); events = append(events, ev); mu.Unlock() })
	port := freePort(t)
	m.start(context.Background(), &collectorConfig{TrapListenAddr: "127.0.0.1:" + strconv.Itoa(port)}, nil)
	boundPort(t, m)
	sendTrap(t, port, "")
	trapWaitFor(t, "the fallback trap on the metric path", func() bool { return s.count() == 1 })
	mu.Lock()
	if len(events) != 0 {
		t.Fatalf("fallback mode produced events %+v", events)
	}
	mu.Unlock()

	p2 := freePort(t)
	m.apply(&probewire.TrapConfig{Enabled: true, Port: p2, CredentialIDs: []string{"cred-a"}})
	boundPort(t, m)
	sendTrap(t, p2, "alpha")
	trapWaitFor(t, "the event of the monitoring mode", func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 1 })
	mu.Lock()
	defer mu.Unlock()
	ev := events[0]
	if s.count() != 1 || ev.Kind != probewire.EventLinkDown || ev.SourceIP != "127.0.0.1" || ev.Validate() != nil || ev.ID[14] != '7' {
		t.Fatalf("event %+v (valid: %v), metric path %d", ev, ev.Validate(), s.count())
	}
}

// TestTrapToEventAlwaysValid covers WP-979 AK2: a trap with a zoned
// IPv6 source, an invalid trap OID and a varbind the server cannot store
// still gives a valid event (the zone and the bad parts left out), so it
// never costs the result batch at the edge.
func TestTrapToEventAlwaysValid(t *testing.T) {
	ev := trapToEvent(&snmp.TrapEvent{SourceIP: "fe80::1%eth0", TrapOID: "1.3.6.1.6.3.1.1.5.3\x00",
		Variables: map[string]string{"1.3.6.1.2.1.2.2.1.1.7": "7", "1.3.6.1.2.1.2.2.1.2.7": "a\x00b"}, Received: time.Now()})
	if err := ev.Validate(); err != nil {
		t.Fatalf("event %+v: %v", ev, err)
	}
	if ev.SourceIP != "fe80::1" || ev.TrapOID != "" || !ev.Truncated || len(ev.Varbinds) != 1 {
		t.Fatalf("event = %+v", ev)
	}
	v4 := trapToEvent(&snmp.TrapEvent{SourceIP: "::ffff:192.0.2.1", TrapOID: "1.3.6.1.6.3.1.1.5.4", Received: time.Now()})
	if v4.SourceIP != "192.0.2.1" || v4.Kind != probewire.EventLinkUp {
		t.Fatalf("event = %+v", v4)
	}
}
