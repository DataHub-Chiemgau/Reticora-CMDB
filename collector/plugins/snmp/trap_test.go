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
	ev, err := parseTrap(pkt)
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
		if _, err := parseTrap(pkt); err == nil {
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
		if rcv.conn != nil {
			addr = rcv.conn.LocalAddr().String()
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
	defer conn.Close()

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

// BenchmarkParseV2Trap keeps the hot path honest: trap bursts must be cheap.
func BenchmarkParseV2Trap(b *testing.B) {
	pkt := buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.2.2.1.1.2", "2")
	for i := 0; i < b.N; i++ {
		if _, err := parseTrap(pkt); err != nil {
			b.Fatal(err)
		}
	}
}

func ExampleTrapEvent() {
	pkt := buildV2Trap("public", "1.3.6.1.6.3.1.1.5.3", "1.3.6.1.2.1.2.2.1.1.2", "2")
	ev, _ := parseTrap(pkt)
	fmt.Println(ev.Community, ev.TrapOID, ev.Variables["1.3.6.1.2.1.2.2.1.1.2"])
	// Output: public 1.3.6.1.6.3.1.1.5.3 2
}
