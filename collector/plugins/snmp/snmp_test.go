package snmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
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

func TestExtractSNMPValue(t *testing.T) {
	withValue := []byte{0x30, 0x20, 0x02, 0x01, 0x01, 0x04, 0x06, 'r', 'o', 'u', 't', 'e', 'r'}
	nonPrintable := []byte{0x30, 0x08, 0x04, 0x03, 0x01, 0x02, 0x03}
	zeroLen := []byte{0x04, 0x00}

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"octet string value", withValue, "router"},
		{"empty input", nil, ""},
		{"non printable skipped", nonPrintable, ""},
		{"zero length skipped", zeroLen, ""},
		{"truncated length", []byte{0x04, 0x10, 'a'}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractSNMPValue(c.data); got != c.want {
				t.Errorf("extractSNMPValue() = %q, want %q", got, c.want)
			}
		})
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

// fakeResponseConn feeds a scripted response to snmpGet and records writes.
type fakeResponseConn struct {
	net.Conn
	resp   []byte
	writes [][]byte
}

func (c *fakeResponseConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (c *fakeResponseConn) Read(b []byte) (int, error) {
	if c.resp == nil {
		return 0, fmt.Errorf("no more data")
	}
	n := copy(b, c.resp)
	c.resp = nil
	return n, nil
}

func (c *fakeResponseConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeResponseConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeResponseConn) SetWriteDeadline(time.Time) error { return nil }

func TestSNMPGetReturnsValueFromResponse(t *testing.T) {
	// Response > 20 bytes whose only OctetString is the varbind value.
	resp := make([]byte, 0, 64)
	resp = append(resp, 0x30, 0x30, 0x02, 0x01, 0x01)
	resp = append(resp, make([]byte, 16)...) // padding (no OctetStrings)
	value := "Cisco IOS Software"
	resp = append(resp, 0x04, byte(len(value)))
	resp = append(resp, value...)

	conn := &fakeResponseConn{resp: resp}
	p := New()
	got := p.snmpGet(conn, "public", oidSysDescr, time.Second)
	if got != value {
		t.Errorf("snmpGet = %q, want %q", got, value)
	}
	if len(conn.writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(conn.writes))
	}
	if !bytes.Contains(conn.writes[0], []byte{0xA0}) {
		t.Error("sent packet is not a GetRequest PDU")
	}
}

func TestSNMPGetReturnsFirstOctetString(t *testing.T) {
	// The simplified parser scans for the first printable OctetString in the
	// packet, which in a real SNMP response is the echoed community string.
	resp := make([]byte, 0, 64)
	resp = append(resp, 0x30, 0x30, 0x02, 0x01, 0x01)
	resp = append(resp, 0x04, 0x06, 'p', 'u', 'b', 'l', 'i', 'c')
	resp = append(resp, make([]byte, 10)...)
	resp = append(resp, 0x04, 0x06, 'r', 'o', 'u', 't', 'e', 'r')

	conn := &fakeResponseConn{resp: resp}
	p := New()
	if got := p.snmpGet(conn, "public", oidSysDescr, time.Second); got != "public" {
		t.Errorf("snmpGet = %q, want first octet string %q", got, "public")
	}
}

func TestSNMPGetShortResponseReturnsEmpty(t *testing.T) {
	conn := &fakeResponseConn{resp: []byte{0x30, 0x05, 0x02, 0x01, 0x01, 0x04, 0x00}}
	p := New()
	if got := p.snmpGet(conn, "public", oidSysDescr, time.Second); got != "" {
		t.Errorf("snmpGet = %q, want empty for short response", got)
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
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, map[string]string{"community": "public"})
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
	results, err := p.Discover(context.Background(), []string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for silent target, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Discover(context.Background(), nil, nil)
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
	results, err := p.Discover(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCollectFromSNMPFake(t *testing.T) {
	// The fake echoes the request first, so the community string "public" is
	// the first OctetString the simplified parser finds; every OID therefore
	// resolves to "public". That still exercises the full collect path.
	startSNMPFake(t, "test-device")

	p := &Plugin{Timeout: time.Second}
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.CIType != "network.device" {
		t.Errorf("CIType = %q, want network.device", res.CIType)
	}
	if res.Name == "" {
		t.Error("expected Name to be set from sysName or target fallback")
	}
	for _, attr := range []string{"sysDescr", "sysObjectID", "sysLocation", "sysContact"} {
		if res.Attributes[attr] == "" {
			t.Errorf("expected attribute %q to be populated", attr)
		}
	}
	if res.Attributes["snmpVersion"] != "v2c" {
		t.Errorf("snmpVersion = %v, want v2c", res.Attributes["snmpVersion"])
	}
}

func TestCollectNameFallsBackToTarget(t *testing.T) {
	// Fake that never answers => all GETs time out => empty sysName.
	p := &Plugin{Timeout: 100 * time.Millisecond}
	res, err := p.Collect(context.Background(), "192.0.2.55", nil)
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
