package fakedhcp

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/dhcpv4"
)

var (
	testMAC      = []byte{0x02, 0x00, 0x5e, 0x10, 0x20, 0x30}
	testServerID = netip.MustParseAddr("192.0.2.1")
	testOffer    = netip.MustParseAddr("192.0.2.50")
	testDNS      = netip.MustParseAddr("192.0.2.53")
)

func startServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if !cfg.ServerID.IsValid() {
		cfg.ServerID = testServerID
	}
	if !cfg.OfferAddr.IsValid() {
		cfg.OfferAddr = testOffer
	}
	cfg.DNS = append(cfg.DNS, testDNS)
	s, err := Listen("127.0.0.1:0", &cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return s
}

// clientConn is the UDP socket of a test client (or relay) on a high port.
func clientConn(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func send(t *testing.T, c *net.UDPConn, s *Server, m *dhcpv4.Message) {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteToUDP(b, s.Addr()); err != nil {
		t.Fatal(err)
	}
}

// receive reads one answer; nil after the timeout.
func receive(t *testing.T, c *net.UDPConn, timeout time.Duration) *dhcpv4.Message {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil
	}
	m, err := dhcpv4.Decode(buf[:n])
	if err != nil {
		t.Fatalf("answer does not decode: %v", err)
	}
	return m
}

// TestFakeDHCPOffersOnDiscover covers WP-1011 AK4: DISCOVER → OFFER with
// the xid, yiaddr and server id; the configured delay holds the answer back.
func TestFakeDHCPOffersOnDiscover(t *testing.T) {
	s := startServer(t, Config{Delay: 50 * time.Millisecond})
	c := clientConn(t)
	start := time.Now()
	send(t, c, s, dhcpv4.NewRequest(dhcpv4.Discover, 0x0a0b0c0d, testMAC))
	offer := receive(t, c, 2*time.Second)
	if offer == nil {
		t.Fatal("no OFFER")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Errorf("the answer came before the delay")
	}
	if offer.MessageType() != dhcpv4.Offer || offer.Op != dhcpv4.OpReply || offer.XID != 0x0a0b0c0d || offer.YIAddr != testOffer {
		t.Fatalf("answer %+v", offer)
	}
	if id, ok := offer.ServerID(); !ok || id != testServerID {
		t.Errorf("server id %v", id)
	}
	if d := offer.DNSServers(); len(d) != 1 || d[0] != testDNS {
		t.Errorf("dns %v", d)
	}
	if _, ok := offer.LeaseTime(); !ok {
		t.Error("OFFER without lease time")
	}
}

// TestFakeDHCPAcksInform: INFORM → ACK without lease and yiaddr; a silent
// server does not answer.
func TestFakeDHCPAcksInform(t *testing.T) {
	s := startServer(t, Config{})
	c := clientConn(t)
	inform := dhcpv4.NewRequest(dhcpv4.Inform, 42, testMAC)
	inform.CIAddr = netip.MustParseAddr("127.0.0.1")
	send(t, c, s, inform)
	ack := receive(t, c, 2*time.Second)
	if ack == nil || ack.MessageType() != dhcpv4.Ack || ack.XID != 42 {
		t.Fatalf("answer %+v", ack)
	}
	if _, ok := ack.LeaseTime(); ok || !ack.YIAddr.IsUnspecified() {
		t.Errorf("ACK to INFORM carries a lease: %+v", ack)
	}
	if ack.CIAddr != inform.CIAddr {
		t.Errorf("ciaddr %s, want %s", ack.CIAddr, inform.CIAddr)
	}

	s.SetSilent(true)
	send(t, c, s, inform)
	if m := receive(t, c, 200*time.Millisecond); m != nil {
		t.Fatalf("silent server answered %v", m.MessageType())
	}
	s.SetSilent(false)
	send(t, c, s, inform)
	if m := receive(t, c, 2*time.Second); m == nil {
		t.Fatal("no answer after SetSilent(false)")
	}
}

// TestFakeDHCPAnswersRelayToGiaddr: a request with giaddr is answered to
// giaddr and the relay port, not to the sender.
func TestFakeDHCPAnswersRelayToGiaddr(t *testing.T) {
	relay := clientConn(t)
	s := startServer(t, Config{RelayPort: relay.LocalAddr().(*net.UDPAddr).Port})
	sender := clientConn(t)
	discover := dhcpv4.NewRequest(dhcpv4.Discover, 7, testMAC)
	discover.GIAddr = netip.MustParseAddr("127.0.0.1")
	discover.Hops = 1
	send(t, sender, s, discover)
	offer := receive(t, relay, 2*time.Second)
	if offer == nil || offer.MessageType() != dhcpv4.Offer || offer.GIAddr != discover.GIAddr {
		t.Fatalf("relay got %+v", offer)
	}
	if m := receive(t, sender, 200*time.Millisecond); m != nil {
		t.Fatal("the relayed request was answered to the sender")
	}
}

// TestFakeDHCPRecordsRequests: every message is recorded with what was
// answered; REQUEST and an undecodable packet stay unanswered (no lease).
func TestFakeDHCPRecordsRequests(t *testing.T) {
	s := startServer(t, Config{})
	c := clientConn(t)
	send(t, c, s, dhcpv4.NewRequest(dhcpv4.Discover, 1, testMAC))
	_ = receive(t, c, 2*time.Second)
	send(t, c, s, dhcpv4.NewRequest(dhcpv4.Request, 2, testMAC))
	if _, err := c.WriteToUDP([]byte("no dhcp"), s.Addr()); err != nil {
		t.Fatal(err)
	}
	if m := receive(t, c, 200*time.Millisecond); m != nil {
		t.Fatalf("the server answered %v", m.MessageType())
	}
	deadline := time.Now().Add(2 * time.Second)
	var reqs []Request
	for time.Now().Before(deadline) {
		if reqs = s.Requests(); len(reqs) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests, want 3", len(reqs))
	}
	if reqs[0].Message.MessageType() != dhcpv4.Discover || reqs[0].Answered != dhcpv4.Offer {
		t.Errorf("first %+v", reqs[0])
	}
	if reqs[1].Message.MessageType() != dhcpv4.Request || reqs[1].Answered != 0 {
		t.Errorf("REQUEST %+v must be recorded and unanswered", reqs[1])
	}
	if reqs[2].Message != nil || reqs[2].Err == nil {
		t.Errorf("undecodable packet %+v", reqs[2])
	}
	for _, r := range reqs {
		if !r.From.IsValid() || r.At.IsZero() {
			t.Errorf("request without sender or time: %+v", r)
		}
	}
}
