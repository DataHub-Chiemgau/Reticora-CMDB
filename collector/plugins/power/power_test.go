package power

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestEncodeOID(t *testing.T) {
	cases := []struct {
		oid  string
		want []byte
	}{
		{"1.3.6.1.2.1.33.1.1.2.0", []byte{0x06, 0x0A, 43, 6, 1, 2, 1, 33, 1, 1, 2, 0}},
		// Enterprise OID with multi-byte component 318 -> 0x82 0x3E
		{"1.3.6.1.4.1.318", []byte{0x06, 0x06, 43, 6, 1, 4, 1, 0x82, 0x3E}},
		{"", []byte{0x06, 0x01, 0x00}},
	}
	for _, c := range cases {
		if got := encodeOID(c.oid); !bytes.Equal(got, c.want) {
			t.Errorf("encodeOID(%q) = %x, want %x", c.oid, got, c.want)
		}
	}
}

func TestBuildSNMPGet(t *testing.T) {
	pkt := buildSNMPGet("public", oidUPSIdentModel)
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
}

func TestExtractStringValue(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"octet string", []byte{0x30, 0x10, 0x04, 0x03, 'A', 'P', 'C'}, "APC"},
		{"empty", nil, ""},
		{"non printable", []byte{0x04, 0x02, 0x00, 0x01}, ""},
		{"truncated", []byte{0x04, 0x09, 'x'}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractStringValue(c.data); got != c.want {
				t.Errorf("extractStringValue() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClassifyPowerDevice(t *testing.T) {
	cases := []struct {
		mfg, model, want string
	}{
		{"APC", "Smart-UPS 1500", "ups"},
		{"APC", "Rack PDU AP8941", "pdu"},
		{"Eaton", "ATS 16A", "ats"},
		{"", "automatic transfer switch", "ats"},
		{"Generic", "Unknown", "ups"},
		{"", "", "ups"},
	}
	for _, c := range cases {
		if got := classifyPowerDevice(c.mfg, c.model); got != c.want {
			t.Errorf("classifyPowerDevice(%q, %q) = %q, want %q", c.mfg, c.model, got, c.want)
		}
	}
}

// scriptedConn returns queued responses for each Read call.
type scriptedConn struct {
	net.Conn
	responses [][]byte
	writes    int
}

func (c *scriptedConn) Write(b []byte) (int, error) {
	c.writes++
	return len(b), nil
}

func (c *scriptedConn) Read(b []byte) (int, error) {
	if len(c.responses) == 0 {
		return 0, fmt.Errorf("no more scripted responses")
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	n := copy(b, resp)
	return n, nil
}

func (c *scriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedConn) SetWriteDeadline(time.Time) error { return nil }

func snmpResponseWithValue(value string) []byte {
	resp := make([]byte, 0, 32)
	resp = append(resp, 0x30, 0x20, 0x02, 0x01, 0x01)
	resp = append(resp, make([]byte, 10)...) // ensure n >= 10 threshold
	resp = append(resp, 0x04, byte(len(value)))
	resp = append(resp, value...)
	return resp
}

func TestSNMPGetStringReadsValue(t *testing.T) {
	conn := &scriptedConn{responses: [][]byte{snmpResponseWithValue("Smart-UPS 1500")}}
	got := snmpGetString(conn, "public", oidUPSIdentModel, time.Second)
	if got != "Smart-UPS 1500" {
		t.Errorf("snmpGetString = %q, want %q", got, "Smart-UPS 1500")
	}
	if conn.writes != 1 {
		t.Errorf("expected exactly 1 packet written, got %d", conn.writes)
	}
}

func TestSNMPGetStringEmptyOnTimeout(t *testing.T) {
	conn := &scriptedConn{} // no responses -> Read errors
	if got := snmpGetString(conn, "public", oidUPSIdentModel, time.Second); got != "" {
		t.Errorf("snmpGetString = %q, want empty on read error", got)
	}
}

func TestIsPowerDevice(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: snmpPort})
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
			resp := snmpResponseWithValue("Smart-UPS 1500")
			conn.WriteToUDP(resp, addr)
		}
	}()

	if !isPowerDevice("127.0.0.1", "public", time.Second) {
		t.Error("isPowerDevice = false, want true for UPS-MIB responding target")
	}
}

func TestIsPowerDeviceSilent(t *testing.T) {
	if isPowerDevice("192.0.2.1", "public", 150*time.Millisecond) {
		t.Error("isPowerDevice = true, want false for silent target")
	}
}

func TestCollectFromSNMPFake(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: snmpPort})
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
			resp := snmpResponseWithValue("Smart-UPS 1500")
			conn.WriteToUDP(resp, addr)
		}
	}()

	p := &Plugin{Timeout: time.Second}
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.CIType != "power-device" {
		t.Errorf("CIType = %q, want power-device", res.CIType)
	}
	if res.Model != "Smart-UPS 1500" {
		t.Errorf("Model = %q, want Smart-UPS 1500", res.Model)
	}
	if res.Name != "Smart-UPS 1500" {
		t.Errorf("Name = %q, want model name", res.Name)
	}
	if res.Attributes["deviceType"] != "ups" {
		t.Errorf("deviceType = %v, want ups", res.Attributes["deviceType"])
	}
	if len(res.Metrics) != 4 {
		t.Errorf("expected 4 UPS metric placeholders, got %d", len(res.Metrics))
	}
	for _, m := range res.Metrics {
		if m.Labels["target"] != "127.0.0.1" {
			t.Errorf("metric %q missing target label", m.Name)
		}
	}
}

func TestDiscoverFindsPowerDevice(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: snmpPort})
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
			conn.WriteToUDP(snmpResponseWithValue("Smart-UPS 1500"), addr)
		}
	}()

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].CIType != "power-device" {
		t.Errorf("CIType = %q, want power-device", results[0].CIType)
	}
	if results[0].Attributes["upsCapable"] != true {
		t.Errorf("upsCapable = %v, want true", results[0].Attributes["upsCapable"])
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

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.Name() != "power" {
		t.Errorf("Name() = %q, want power", p.Name())
	}
}
