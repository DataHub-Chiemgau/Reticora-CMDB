// Package fakedhcp is a DHCPv4 test server for the dhcp probe (WP-1011,
// PRT-13, PRT-21): it answers DISCOVER with OFFER and INFORM with ACK, never
// hands out a lease (REQUEST and every other type stay unanswered) and
// records every message it receives, so a test can prove that the probe
// sent no REQUEST. It is test infrastructure like collector/internal/
// fakesnmp; the release build does not use it.
//
// The server listens on a configurable UDP address (tests use high ports).
// A direct request is answered to the address it came from; a relayed
// request (giaddr set) to giaddr and the relay port (RFC 2131 4.1, the
// server port 67 unless configured). Answer delay and silence can be set
// while the server runs.
package fakedhcp

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/dhcpv4"
)

// Config is what the server offers.
type Config struct {
	// ServerID is option 54 and siaddr of the answers.
	ServerID netip.Addr
	// OfferAddr is yiaddr of an OFFER.
	OfferAddr  netip.Addr
	SubnetMask netip.Addr
	Router     netip.Addr
	DNS        []netip.Addr
	Lease      time.Duration
	// RelayPort is the port relayed answers go to on giaddr (default 67).
	RelayPort int
	// Delay postpones every answer; Silent suppresses them (both can be
	// changed with SetDelay and SetSilent).
	Delay  time.Duration
	Silent bool
}

// Request is a received message as the server saw it.
type Request struct {
	At   time.Time
	From netip.AddrPort
	// Message is the decoded message; nil when it did not decode (Err).
	Message *dhcpv4.Message
	Err     error
	// Answered names the type of the answer sent (0: none).
	Answered dhcpv4.MessageType
}

// Server is a running fake DHCPv4 server.
type Server struct {
	conn *net.UDPConn

	mu     sync.Mutex
	cfg    Config
	seen   []Request
	closed bool
	// wg counts the delayed answers; Add happens under mu while not
	// closed, so Close can wait for them.
	wg sync.WaitGroup
}

// Listen binds the server to addr (host:port, UDP); Serve answers.
func Listen(addr string, config *Config) (*Server, error) {
	cfg := *config
	if !cfg.ServerID.Is4() {
		return nil, errors.New("fakedhcp: an IPv4 server id is required")
	}
	if cfg.RelayPort == 0 {
		cfg.RelayPort = dhcpv4.ServerPort
	}
	if cfg.Lease == 0 {
		cfg.Lease = time.Hour
	}
	udp, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", udp)
	if err != nil {
		return nil, err
	}
	return &Server{conn: conn, cfg: cfg}, nil
}

// Addr is the address the server listens on.
func (s *Server) Addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

// Close stops Serve and waits for delayed answers.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	err := s.conn.Close()
	s.wg.Wait()
	return err
}

// SetDelay sets the answer delay.
func (s *Server) SetDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Delay = d
}

// SetSilent switches the answers off (true) or on.
func (s *Server) SetSilent(silent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Silent = silent
}

// Requests returns the messages received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// Serve answers until Close.
func (s *Server) Serve() error {
	buf := make([]byte, 2048)
	for {
		n, peer, err := s.conn.ReadFromUDPAddrPort(buf)
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		req := Request{At: time.Now().UTC(), From: peer}
		req.Message, req.Err = dhcpv4.Decode(buf[:n])
		s.mu.Lock()
		cfg := s.cfg
		var reply *dhcpv4.Message
		if req.Message != nil && req.Message.Op == dhcpv4.OpRequest && !cfg.Silent {
			reply = answer(&cfg, req.Message)
		}
		var out []byte
		if reply != nil {
			if out, err = reply.Encode(); err == nil && !s.closed {
				req.Answered = reply.MessageType()
				s.wg.Add(1)
			} else {
				out = nil
			}
		}
		s.seen = append(s.seen, req)
		s.mu.Unlock()
		if out == nil {
			continue
		}
		dst := peer
		if gi := req.Message.GIAddr; gi.IsValid() && !gi.IsUnspecified() {
			dst = netip.AddrPortFrom(gi, uint16(cfg.RelayPort)) //nolint:gosec // a port
		}
		go func() {
			defer s.wg.Done()
			if cfg.Delay > 0 {
				time.Sleep(cfg.Delay)
			}
			_, _ = s.conn.WriteToUDPAddrPort(out, dst)
		}()
	}
}

// answer builds the OFFER to a DISCOVER and the ACK to an INFORM; every
// other message type gets no answer (no lease, no NAK).
func answer(cfg *Config, req *dhcpv4.Message) *dhcpv4.Message {
	var reply *dhcpv4.Message
	switch req.MessageType() {
	case dhcpv4.Discover:
		reply = dhcpv4.NewReply(req, dhcpv4.Offer)
		reply.CIAddr = netip.Addr{}
		reply.YIAddr = cfg.OfferAddr
		reply.SetOption(dhcpv4.LeaseTimeOption(cfg.Lease))
	case dhcpv4.Inform:
		// RFC 2131 4.3.5: no lease time and no yiaddr in the ACK to an
		// INFORM.
		reply = dhcpv4.NewReply(req, dhcpv4.Ack)
	default:
		return nil
	}
	reply.SIAddr = cfg.ServerID
	reply.SetOption(dhcpv4.AddrOption(dhcpv4.OptServerID, cfg.ServerID))
	if cfg.SubnetMask.IsValid() {
		reply.SetOption(dhcpv4.AddrOption(dhcpv4.OptSubnetMask, cfg.SubnetMask))
	}
	if cfg.Router.IsValid() {
		reply.SetOption(dhcpv4.AddrsOption(dhcpv4.OptRouter, cfg.Router))
	}
	if len(cfg.DNS) > 0 {
		reply.SetOption(dhcpv4.AddrsOption(dhcpv4.OptDNS, cfg.DNS...))
	}
	return reply
}
