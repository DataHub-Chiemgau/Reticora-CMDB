package snmp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// buildV2Trap constructs a minimal SNMPv2c Trap-PDU carrying one snmpTrapOID.0
// varbind plus one extra string varbind, mirroring what real agents send.
func buildV2Trap(community, trapOID string, extraOID, extraValue string) []byte {
	// snmpTrapOID.0 varbind: OID value
	trapOIDVarbind := asn1Sequence(append(encodeOID("1.3.6.1.6.3.1.1.4.1.0"), encodeOID(trapOID)...))
	extraVarbind := asn1Sequence(append(encodeOID(extraOID),
		append([]byte{0x04, byte(len(extraValue))}, []byte(extraValue)...)...))
	varbindList := asn1Sequence(append(trapOIDVarbind, extraVarbind...))

	requestID := []byte{0x02, 0x01, 0x01}
	errorStatus := []byte{0x02, 0x01, 0x00}
	errorIndex := []byte{0x02, 0x01, 0x00}
	pduContent := append(requestID, errorStatus...)
	pduContent = append(pduContent, errorIndex...)
	pduContent = append(pduContent, varbindList...)
	pdu := append([]byte{0xA7, byte(len(pduContent))}, pduContent...)

	version := []byte{0x02, 0x01, 0x01}
	communityBytes := append([]byte{0x04, byte(len(community))}, []byte(community)...)
	return asn1Sequence(append(append(version, communityBytes...), pdu...))
}

func TestParseV2Trap(t *testing.T) {
	pkt := buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.2.2.1.1.2", "2")
	ev, err := parseTrap(pkt, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Community != "public" {
		t.Fatalf("community: got %q", ev.Community)
	}
	// linkDown trap OID should have been extracted from snmpTrapOID.0.
	if ev.TrapOID != "1.3.6.1.6.3.1.1.5.3" {
		t.Fatalf("trap OID: got %q", ev.TrapOID)
	}
	if ev.Variables["1.3.6.1.2.1.2.2.1.1.2"] != "2" {
		t.Fatalf("ifIndex varbind: got %q", ev.Variables["1.3.6.1.2.1.2.2.1.1.2"])
	}
	// snmpTrapOID.0 itself must not leak into the variable map.
	if _, ok := ev.Variables["1.3.6.1.6.3.1.1.4.1.0"]; ok {
		t.Fatal("snmpTrapOID.0 must be consumed, not listed as a variable")
	}
}

func TestParseTrapRejectsGarbage(t *testing.T) {
	for _, pkt := range [][]byte{
		{},
		{0x00},
		{0x30},
		[]byte("not an snmp packet at all"),
	} {
		if _, err := parseTrap(pkt, nil); err == nil {
			t.Fatalf("expected error for %v", pkt)
		}
	}
}

func TestTrapReceiverEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var received []TrapEvent

	rcv := &TrapReceiver{
		ListenAddr: "127.0.0.1:0",
		Community:  "public",
		Sink: func(_ context.Context, ev TrapEvent) {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, ev)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- rcv.Run(ctx) }()

	// Wait for the socket to be up.
	var addr string
	for i := 0; i < 50; i++ {
		if bound := rcv.LocalAddr(); bound != nil {
			addr = bound.String()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("receiver did not start")
	}

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write(buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.1.3.0", "12345")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wrong community must be dropped.
	if _, err := conn.Write(buildV2Trap("wrong", "1.3.6.1.6.3.1.1.5.1", "1.3.6.1.2.1.1.3.0", "9")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected exactly 1 accepted trap (community-filtered), got %d", len(received))
	}
	if received[0].TrapOID != "1.3.6.1.6.3.1.1.5.3" {
		t.Fatalf("unexpected trap OID: %q", received[0].TrapOID)
	}
	if received[0].SourceIP != "127.0.0.1" {
		t.Fatalf("unexpected source: %q", received[0].SourceIP)
	}
}

func TestTrapReceiverRequiresSink(t *testing.T) {
	rcv := &TrapReceiver{ListenAddr: "127.0.0.1:0"}
	if err := rcv.Run(context.Background()); err == nil {
		t.Fatal("expected an error when no sink is configured")
	}
}

func TestBerIntStringSignExtension(t *testing.T) {
	cases := []struct {
		value []byte
		want  string
	}{
		{[]byte{0xFF}, "-1"},
		{[]byte{0x80}, "-128"},
		{[]byte{0x7F}, "127"},
		{[]byte{0x00, 0x80}, "128"},
		{[]byte{0xFF, 0x7F}, "-129"},
		{[]byte{0x01, 0x00}, "256"},
	}
	for _, tc := range cases {
		if got := berIntString(tc.value); got != tc.want {
			t.Errorf("berIntString(%x) = %s, want %s", tc.value, got, tc.want)
		}
	}
}

// BenchmarkParseV2Trap keeps the hot path honest: trap bursts must be cheap.
func BenchmarkParseV2Trap(b *testing.B) {
	pkt := buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.2.2.1.1.2", "2")
	for i := 0; i < b.N; i++ {
		if _, err := parseTrap(pkt, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// TestParseTrapDispatchesByVersion covers WP-978 AK1: v1/v2c stay with the
// BER decoder (community), version 3 goes to parseV3Trap (user); a v3 trap
// at the receiver from an unknown user is dropped and counted, and a user
// added to the credentials applies without a restart.
func TestParseTrapDispatchesByVersion(t *testing.T) {
	ev, err := parseTrap(buildV2Trap("public", "1.3.6.1.6.3.1.1.5.4", "1.3.6.1.2.1.1.3.0", "1"), nil)
	if err != nil || ev.Community != "public" || ev.User != "" {
		t.Fatalf("v2c: %+v, %v", ev, err)
	}
	s := defaultSender()
	raw := buildV3Trap(t, &s)
	known := trapUserCreds("trapuser", "sha256", v3AuthPass, "aes", v3PrivPass)
	ev, err = parseTrap(raw, usmOf(known))
	if err != nil || ev.User != "trapuser" || ev.Community != "" {
		t.Fatalf("v3: %+v, %v", ev, err)
	}

	var mu sync.Mutex
	var got []TrapEvent
	drops := map[string]int{}
	users := []map[string]string{trapUserCreds("other", "sha256", v3AuthPass, "aes", v3PrivPass)}
	rcv := &TrapReceiver{ListenAddr: "127.0.0.1:0", Community: "public",
		Sink:   func(_ context.Context, ev TrapEvent) { mu.Lock(); got = append(got, ev); mu.Unlock() }, //nolint:gocritic // signature of Sink
		OnDrop: func(reason string) { mu.Lock(); drops[reason]++; mu.Unlock() },
		USM:    &TrapUSM{Users: func() []map[string]string { mu.Lock(); defer mu.Unlock(); return users }},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rcv.Run(ctx) }()
	var addr net.Addr
	for i := 0; i < 50 && addr == nil; i++ {
		addr = rcv.LocalAddr()
		time.Sleep(10 * time.Millisecond)
	}
	if addr == nil {
		t.Fatal("receiver did not start")
	}
	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	_, _ = conn.Write(raw) // user unknown to this receiver
	waitFor(func() bool { return drops[TrapDropUnknownUser] == 1 })
	mu.Lock()
	users = append(users, known)
	mu.Unlock()
	_, _ = conn.Write(raw)
	waitFor(func() bool { return len(got) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].User != "trapuser" || got[0].SourceIP != "127.0.0.1" || drops[TrapDropUnknownUser] != 1 {
		t.Fatalf("events %+v, drops %v", got, drops)
	}
}

func ExampleTrapEvent() {
	pkt := buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.2.2.1.1.2", "2")
	ev, _ := parseTrap(pkt, nil)
	fmt.Println(ev.Community, ev.TrapOID, ev.Variables["1.3.6.1.2.1.2.2.1.1.2"])
	// Output: public 1.3.6.1.6.3.1.1.5.3 2
}
