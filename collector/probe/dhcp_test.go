package probe

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/dhcpv4"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakedhcp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

var (
	dhcpTestServerID = netip.MustParseAddr("192.0.2.67")
	dhcpTestOffer    = netip.MustParseAddr("10.20.30.40")
)

// freeUDPPort returns a UDP port that is free on every IPv4 address.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

// startDHCP starts fakedhcp on a high port of the loopback; relayPort is
// the port relayed answers go to on giaddr.
func startDHCP(t *testing.T, relayPort int) *fakedhcp.Server {
	t.Helper()
	s, err := fakedhcp.Listen("127.0.0.1:0", &fakedhcp.Config{
		ServerID: dhcpTestServerID, OfferAddr: dhcpTestOffer, RelayPort: relayPort,
		SubnetMask: netip.MustParseAddr("255.255.255.0"),
	})
	if err != nil {
		t.Skipf("fakedhcp: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dhcpTestExecutor is an executor whose ports are injected: the server
// port of the fake and free high ports for client and relay.
func dhcpTestExecutor(t *testing.T, serverPort, relayPort int) *DHCPExecutor {
	t.Helper()
	return &DHCPExecutor{ServerPort: serverPort, ClientPort: freeUDPPort(t), RelayPort: relayPort,
		Capabilities: func() *probewire.ProbeCapabilities {
			return &probewire.ProbeCapabilities{UDP67: true, UDP68: true}
		}}
}

func dhcpSpec(params map[string]any) probewire.ProbeSpec {
	raw, _ := json.Marshal(params)
	return probewire.ProbeSpec{
		ID: "dhcp-1", Type: TypeDHCP, Target: "127.0.0.1", IntervalMS: 300_000, TimeoutMS: 2_000,
		DownCount: 3, UpCount: 1, Params: raw, Epoch: 1,
	}
}

func runDHCP(t *testing.T, e *DHCPExecutor, spec probewire.ProbeSpec, timeout time.Duration) probewire.Result { //nolint:gocritic // test helper
	t.Helper()
	if errs := probewire.Validate(&spec); len(errs) > 0 {
		t.Fatalf("spec rejected: %v", errs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r := e.Execute(ctx, spec, nil)
	r.ProbeID = spec.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid wire result %+v: %v", r, err)
	}
	return r
}

// dhcpValue returns the response time and its labels.
func dhcpValue(t *testing.T, r *probewire.Result) (float64, map[string]string) {
	t.Helper()
	for k, v := range r.Values {
		name, labels, err := probewire.ParseValueKey(k)
		if err != nil {
			t.Fatal(err)
		}
		if name == ValueDHCPResponseSeconds {
			return v, labels
		}
	}
	t.Fatalf("no %s in %v", ValueDHCPResponseSeconds, r.Values)
	return 0, nil
}

func requestTypes(s *fakedhcp.Server) []dhcpv4.MessageType {
	var out []dhcpv4.MessageType
	for _, r := range s.Requests() {
		if r.Message != nil {
			out = append(out, r.Message.MessageType())
		}
	}
	return out
}

// TestDHCPInform covers AK1 (inform), AK5 and PRT-13: DHCPINFORM by
// unicast, ACK with server_id and the response time; ciaddr is the
// collector address towards the server.
func TestDHCPInform(t *testing.T) {
	s := startDHCP(t, 0)
	e := dhcpTestExecutor(t, s.Addr().Port, 0)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "inform"}), 2*time.Second)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("result = %+v, want ok", r)
	}
	v, labels := dhcpValue(t, &r)
	if v <= 0 || r.RTTMS == nil {
		t.Fatalf("response time %v, rtt %v", v, r.RTTMS)
	}
	if labels[LabelDHCPServerID] != dhcpTestServerID.String() {
		t.Fatalf("labels = %v, want server_id %s", labels, dhcpTestServerID)
	}
	if _, ok := labels[LabelDHCPOfferedIP]; ok {
		t.Fatalf("an ACK to INFORM offers no address: %v", labels)
	}
	reqs := s.Requests()
	if len(reqs) != 1 || reqs[0].Message.MessageType() != dhcpv4.Inform {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := reqs[0].Message.CIAddr; got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("ciaddr = %v", got)
	}
	if reqs[0].From.Port() != uint16(e.ClientPort) { //nolint:gosec // a port
		t.Fatalf("source port %d, want the client port %d", reqs[0].From.Port(), e.ClientPort)
	}
}

// TestDHCPRelaySetsGiaddr covers AK1 (relay): DHCPDISCOVER by unicast with
// giaddr the collector address, sent from and answered to the relay port;
// the offered address is the label offered_ip.
func TestDHCPRelaySetsGiaddr(t *testing.T) {
	relay := freeUDPPort(t)
	s := startDHCP(t, relay)
	e := dhcpTestExecutor(t, s.Addr().Port, relay)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "relay", "server": "127.0.0.1"}), 2*time.Second)
	if r.Status != probewire.StatusOK {
		t.Fatalf("result = %+v, want ok", r)
	}
	_, labels := dhcpValue(t, &r)
	if labels[LabelDHCPOfferedIP] != dhcpTestOffer.String() || labels[LabelDHCPServerID] != dhcpTestServerID.String() {
		t.Fatalf("labels = %v", labels)
	}
	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %+v", reqs)
	}
	m := reqs[0].Message
	if m.MessageType() != dhcpv4.Discover || m.GIAddr != netip.MustParseAddr("127.0.0.1") || m.Hops != 1 {
		t.Fatalf("relayed discover = %+v", m)
	}
	if reqs[0].From.Port() != uint16(relay) { //nolint:gosec // a port
		t.Fatalf("source port %d, want the relay port %d", reqs[0].From.Port(), relay)
	}
}

// TestDHCPDiscoverOnLoopbackSegment covers AK1 (discover): a broadcast on
// an interface. It needs a broadcast-capable test interface on Linux
// (RETICORA_TEST_DHCP_INTERFACE) and is skipped otherwise; the loopback
// has no broadcast.
func TestDHCPDiscoverOnLoopbackSegment(t *testing.T) {
	iface := os.Getenv("RETICORA_TEST_DHCP_INTERFACE")
	if iface == "" || runtime.GOOS != "linux" {
		t.Skip("no broadcast-capable test interface (RETICORA_TEST_DHCP_INTERFACE, Linux)")
	}
	s, err := fakedhcp.Listen("0.0.0.0:0", &fakedhcp.Config{ServerID: dhcpTestServerID, OfferAddr: dhcpTestOffer})
	if err != nil {
		t.Skipf("fakedhcp: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	e := dhcpTestExecutor(t, s.Addr().Port, 0)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "discover", "interface": iface}), 3*time.Second)
	if r.Status != probewire.StatusOK {
		t.Fatalf("result = %+v, want ok", r)
	}
	if _, labels := dhcpValue(t, &r); labels[LabelDHCPOfferedIP] != dhcpTestOffer.String() {
		t.Fatalf("labels = %v", labels)
	}
}

// TestDHCPNeverSendsRequest covers AK1 and PRT-13 (no lease): after every
// mode the server saw neither REQUEST nor RELEASE.
func TestDHCPNeverSendsRequest(t *testing.T) {
	relay := freeUDPPort(t)
	s := startDHCP(t, relay)
	e := dhcpTestExecutor(t, s.Addr().Port, relay)
	for _, mode := range []string{"inform", "relay"} {
		if r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": mode}), 2*time.Second); r.Status != probewire.StatusOK {
			t.Fatalf("%s: %+v", mode, r)
		}
	}
	types := requestTypes(s)
	if !slices.Equal(types, []dhcpv4.MessageType{dhcpv4.Inform, dhcpv4.Discover}) {
		t.Fatalf("request types = %v", types)
	}
	// Mode discover needs a broadcast interface and cannot reach the
	// fake; its request is checked as built: a DISCOVER asking for a
	// broadcast answer.
	tx := dhcpTransaction{mode: DHCPModeDiscover, xid: 7, mac: []byte{2, 0, 0, 0, 0, 1}}
	b, err := tx.message()
	if err != nil {
		t.Fatal(err)
	}
	m, err := dhcpv4.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageType() != dhcpv4.Discover || m.Flags&dhcpv4.FlagBroadcast == 0 {
		t.Fatalf("discover request: type %v, flags %#x", m.MessageType(), m.Flags)
	}
	types = append(types, m.MessageType())
	for _, typ := range types {
		if typ == dhcpv4.Request || typ == dhcpv4.Release {
			t.Fatalf("the probe sent %v", typ)
		}
	}
}

// TestDHCPTimeoutIsFail covers AK4: no answer by the deadline is
// fail/timeout.
func TestDHCPTimeoutIsFail(t *testing.T) {
	s := startDHCP(t, 0)
	s.SetSilent(true)
	e := dhcpTestExecutor(t, s.Addr().Port, 0)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "inform"}), 300*time.Millisecond)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("result = %+v, want fail/timeout", r)
	}
}

// TestDHCPForeignXidIgnored covers AK3: answers with another xid or
// another chaddr are discarded, the matching answer counts.
func TestDHCPForeignXidIgnored(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		buf := make([]byte, 2048)
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		req, err := dhcpv4.Decode(buf[:n])
		if err != nil {
			return
		}
		send := func(m *dhcpv4.Message, server netip.Addr) {
			m.SetOption(dhcpv4.AddrOption(dhcpv4.OptServerID, server))
			if b, err := m.Encode(); err == nil {
				_, _ = conn.WriteTo(b, peer)
			}
		}
		foreignXID := dhcpv4.NewReply(req, dhcpv4.Ack)
		foreignXID.XID++
		send(foreignXID, netip.MustParseAddr("192.0.2.1"))
		foreignMAC := dhcpv4.NewReply(req, dhcpv4.Ack)
		foreignMAC.CHAddr[0] ^= 0xff
		send(foreignMAC, netip.MustParseAddr("192.0.2.2"))
		send(dhcpv4.NewReply(req, dhcpv4.Ack), dhcpTestServerID)
	}()
	e := dhcpTestExecutor(t, conn.LocalAddr().(*net.UDPAddr).Port, 0)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "inform"}), 2*time.Second)
	if r.Status != probewire.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	if _, labels := dhcpValue(t, &r); labels[LabelDHCPServerID] != dhcpTestServerID.String() {
		t.Fatalf("a foreign answer counted: %v", labels)
	}
}

// TestDHCPOneTransactionPerBinding covers AK3: two checks on the same
// port binding run one after the other.
func TestDHCPOneTransactionPerBinding(t *testing.T) {
	s := startDHCP(t, 0)
	const delay = 300 * time.Millisecond
	s.SetDelay(delay)
	e := dhcpTestExecutor(t, s.Addr().Port, 0)
	var wg sync.WaitGroup
	results := make([]probewire.Result, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			results[i] = e.Execute(ctx, dhcpSpec(map[string]any{"mode": "inform"}), nil)
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.Status != probewire.StatusOK {
			t.Fatalf("check %d: %+v", i, r)
		}
	}
	reqs := s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if gap := reqs[1].At.Sub(reqs[0].At); gap < delay-50*time.Millisecond {
		t.Fatalf("second transaction started %v after the first, want after its answer (%v)", gap, delay)
	}
}

// TestDHCPUnsupportedWithoutPortCapability covers AK4 and PRT-13: a
// missing port capability and a failing bind are error/unsupported, and
// nothing is sent.
func TestDHCPUnsupportedWithoutPortCapability(t *testing.T) {
	s := startDHCP(t, 0)
	e := dhcpTestExecutor(t, s.Addr().Port, freeUDPPort(t))
	e.Capabilities = func() *probewire.ProbeCapabilities { return &probewire.ProbeCapabilities{UDP67: false, UDP68: false} }
	for _, mode := range []string{"inform", "relay"} {
		r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": mode}), time.Second)
		if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
			t.Fatalf("%s without capability: %+v, want error/unsupported", mode, r)
		}
	}
	// The capability is reported, but the port is taken.
	e = dhcpTestExecutor(t, s.Addr().Port, 0)
	busy, err := net.ListenPacket("udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(e.ClientPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "inform"}), time.Second)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("bind failure: %+v, want error/unsupported", r)
	}
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("%d requests sent without capability", n)
	}
}

// TestDHCPOfferOutsideExpectedSubnetIsWarn covers AK4: an offered address
// outside expect_subnet is ok with the warn code subnet_mismatch, inside
// it plain ok.
func TestDHCPOfferOutsideExpectedSubnetIsWarn(t *testing.T) {
	relay := freeUDPPort(t)
	s := startDHCP(t, relay)
	e := dhcpTestExecutor(t, s.Addr().Port, relay)
	r := runDHCP(t, e, dhcpSpec(map[string]any{"mode": "relay", "expect_subnet": "192.168.0.0/24"}), 2*time.Second)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnSubnetMismatch || !probewire.IsWarnCode(r.Code) {
		t.Fatalf("result = %+v, want ok/subnet_mismatch", r)
	}
	r = runDHCP(t, e, dhcpSpec(map[string]any{"mode": "relay", "expect_subnet": "10.20.30.0/24"}), 2*time.Second)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("result = %+v, want ok", r)
	}
}

// TestDHCPLimits covers AK2 and PRT-21 (limits test): field errors of
// probewire.Validate for an interval below 60 s, a timeout above 10 s,
// discover without interface, an unknown mode and malformed values.
func TestDHCPLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeDHCP)
	if !ok || !typ.Available() || typ.MinIntervalS != 60 || typ.MaxRatePerCollector != 1 || typ.MaxTimeoutS != 10 ||
		!slices.Contains(typ.Capabilities, probewire.NeedPrivilegedUDPPorts) {
		t.Fatalf("register entry = %+v", typ)
	}
	cases := []struct {
		name  string
		edit  func(*probewire.ProbeSpec)
		field string
	}{
		{"interval 59 s", func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		{"timeout 11 s", func(s *probewire.ProbeSpec) { s.TimeoutMS = 11_000 }, "timeout_s"},
		{"discover without interface", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"mode":"discover"}`) }, "params.interface"},
		{"unknown mode", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"mode":"request"}`) }, "params.mode"},
		{"server IPv6", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"server":"2001:db8::1"}`) }, "params.server"},
		{"bad mac", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"mac":"00:11:22"}`) }, "params.mac"},
		{"bad subnet", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"expect_subnet":"10.0.0.0"}`) }, "params.expect_subnet"},
		{"giaddr is gone", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"giaddr":"10.0.0.1"}`) }, "params.giaddr"},
	}
	for _, c := range cases {
		spec := dhcpSpec(map[string]any{})
		c.edit(&spec)
		errs := probewire.Validate(&spec)
		if !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == c.field }) {
			t.Errorf("%s: errs = %v, want field %s", c.name, errs, c.field)
		}
	}
	ok1 := dhcpSpec(map[string]any{"mode": "discover", "interface": "eth0", "server": "192.0.2.67",
		"mac": "02:00:5e:10:00:01", "client_id": "reticora", "expect_subnet": "10.20.30.0/24"})
	if errs := probewire.Validate(&ok1); len(errs) > 0 {
		t.Fatalf("valid spec rejected: %v", errs)
	}
}

// TestDHCPDefaultMACStable covers AK2: the default MAC is locally
// administered, unicast and stable per probe ID.
func TestDHCPDefaultMACStable(t *testing.T) {
	a, _ := dhcpMAC("", "probe-a")
	b, _ := dhcpMAC("", "probe-a")
	c, _ := dhcpMAC("", "probe-b")
	if !slices.Equal(a, b) || slices.Equal(a, c) || len(a) != 6 {
		t.Fatalf("macs %x %x %x", a, b, c)
	}
	if a[0]&0x02 == 0 || a[0]&0x01 != 0 {
		t.Fatalf("mac %x is not locally administered unicast", a)
	}
}
