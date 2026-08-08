package ipmi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBuildASFPing(t *testing.T) {
	msg := buildASFPing()
	if len(msg) != 12 {
		t.Fatalf("len(buildASFPing()) = %d, want 12", len(msg))
	}
	if msg[0] != rmcpVersion {
		t.Errorf("RMCP version byte = %#x, want %#x", msg[0], rmcpVersion)
	}
	if msg[2] != rmcpSeqNo {
		t.Errorf("sequence byte = %#x, want %#x", msg[2], rmcpSeqNo)
	}
	if msg[3] != rmcpClassASF {
		t.Errorf("class byte = %#x, want %#x (ASF)", msg[3], rmcpClassASF)
	}
	if got := binary.BigEndian.Uint32(msg[4:8]); got != 0x000011BE {
		t.Errorf("IANA enterprise = %#x, want 0x11BE", got)
	}
	if msg[8] != 0x80 {
		t.Errorf("message type = %#x, want 0x80 (Presence Ping)", msg[8])
	}
}

func TestBuildGetChannelAuthCap(t *testing.T) {
	msg := buildGetChannelAuthCap()
	if len(msg) != 22 {
		t.Fatalf("len(buildGetChannelAuthCap()) = %d, want 22", len(msg))
	}
	if msg[0] != rmcpVersion {
		t.Errorf("RMCP version byte = %#x, want %#x", msg[0], rmcpVersion)
	}
	if msg[3] != 0x07 {
		t.Errorf("class byte = %#x, want 0x07 (IPMI)", msg[3])
	}
	if msg[13] != 0x09 {
		t.Errorf("message length = %#x, want 0x09", msg[13])
	}
	if msg[19] != 0x38 {
		t.Errorf("command = %#x, want 0x38 (Get Channel Auth Capabilities)", msg[19])
	}
	if msg[21] != 0x04 {
		t.Errorf("privilege = %#x, want 0x04 (Administrator)", msg[21])
	}
}

func TestParseIPMIVersion(t *testing.T) {
	// 22-byte response without the 0x20 (IPMI 2.0) capability bit set.
	resp15 := make([]byte, 22)
	resp15[0] = rmcpVersion
	// 22-byte response with the IPMI 2.0 extended capability bit in the payload.
	resp20 := make([]byte, 22)
	copy(resp20, resp15)
	resp20[18] = 0x20

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"short buffer", []byte{0x06, 0x00}, "1.5"},
		{"empty", nil, "1.5"},
		{"v1.5 response", resp15, "1.5"},
		{"v2.0 bit set", resp20, "2.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseIPMIVersion(c.data); got != c.want {
				t.Errorf("parseIPMIVersion() = %q, want %q", got, c.want)
			}
		})
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
	if p.Name() != "ipmi" {
		t.Errorf("Name() = %q, want ipmi", p.Name())
	}
}

// startRMCPFake binds UDP port 623 and replies to RMCP packets with a
// synthetic ASF Presence Pong.
func startRMCPFake(t *testing.T) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: defaultPort})
	if err != nil {
		t.Skipf("cannot bind udp 127.0.0.1:623: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	pong := make([]byte, 32)
	pong[0] = rmcpVersion
	pong[3] = rmcpClassASF
	binary.BigEndian.PutUint32(pong[4:8], 0x000011BE)
	pong[8] = 0x40 // Presence Pong

	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 4 || buf[0] != rmcpVersion {
				continue
			}
			conn.WriteToUDP(pong, addr)
		}
	}()
}

func TestDiscoverFindsIPMICapableTarget(t *testing.T) {
	startRMCPFake(t)

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "bmc" {
		t.Errorf("CIType = %q, want bmc", r.CIType)
	}
	if r.Attributes["ipmiCapable"] != true {
		t.Errorf("ipmiCapable = %v, want true", r.Attributes["ipmiCapable"])
	}
	if r.Attributes["rmcpPort"] != defaultPort {
		t.Errorf("rmcpPort = %v, want %d", r.Attributes["rmcpPort"], defaultPort)
	}
}

func TestDiscoverSkipsNonRespondingTarget(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond, Concurrency: 2}
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

func TestCollectFromRMCPFake(t *testing.T) {
	startRMCPFake(t)

	p := &Plugin{Timeout: time.Second}
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.CIType != "bmc" {
		t.Errorf("CIType = %q, want bmc", res.CIType)
	}
	if res.Attributes["ipmiCapable"] != true {
		t.Errorf("ipmiCapable = %v, want true", res.Attributes["ipmiCapable"])
	}
	if v := res.Attributes["ipmiVersion"]; v != "1.5" && v != "2.0" {
		t.Errorf("ipmiVersion = %v, want parsed version", v)
	}
	cmds, ok := res.Attributes["commandSet"].(map[string]string)
	if !ok || len(cmds) == 0 {
		t.Errorf("expected non-empty commandSet, got %v", res.Attributes["commandSet"])
	}
}

func TestCollectUnreachableTarget(t *testing.T) {
	p := &Plugin{Timeout: 150 * time.Millisecond}
	_, err := p.Collect(context.Background(), "192.0.2.1", nil)
	if err == nil || !strings.Contains(err.Error(), "not reachable via RMCP") {
		t.Errorf("expected RMCP reachability error, got %v", err)
	}
}

func TestProbeRMCPRejectsMalformedResponse(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: defaultPort})
	if err != nil {
		t.Skipf("cannot bind udp 127.0.0.1:623: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// Respond with garbage that fails the RMCP header validation.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			conn.WriteToUDP(bytes.Repeat([]byte{0xFF}, n), addr)
		}
	}()

	p := &Plugin{Timeout: 300 * time.Millisecond}
	if p.probeRMCP("127.0.0.1", 300*time.Millisecond) {
		t.Error("probeRMCP should reject a malformed (non-RMCP) response")
	}
}
