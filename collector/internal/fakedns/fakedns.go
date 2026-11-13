// Package fakedns is a DNS test server for the dns probe (WP-1010, PRT-12,
// PRT-21). It serves a zone map per name and type over UDP and TCP on the
// same loopback port and has knobs for the cases a probe must handle:
// response code, AD bit, truncated UDP answers (TC bit), a wrong message ID,
// a delay, an oversized answer and a TCP connection closed without a
// complete answer. It is test infrastructure like
// collector/internal/fakesnmp; the release build does not use it.
//
// Answers: a name without any record is NXDOMAIN, a name with records of
// other types NOERROR without answers; a set response code overrides both.
package fakedns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// TypeCAA is the record type CAA (RFC 8659), which dnsmessage has no
// constant for.
const TypeCAA = dnsmessage.Type(257)

// WrongID modes of SetWrongID.
const (
	// WrongIDOff answers with the ID of the query.
	WrongIDOff = iota
	// WrongIDFirst sends an answer with a wrong ID before the right one.
	WrongIDFirst
	// WrongIDOnly sends only answers with a wrong ID.
	WrongIDOnly
)

type key struct {
	name string
	typ  dnsmessage.Type
}

// CloseTCP modes of SetCloseTCP.
const (
	// CloseTCPOff answers TCP queries.
	CloseTCPOff = iota
	// CloseTCPNoAnswer closes the connection after reading the query,
	// without an answer.
	CloseTCPNoAnswer
	// CloseTCPHalfPrefix sends one byte of the 2-byte length prefix and
	// closes the connection.
	CloseTCPHalfPrefix
)

// Server is a running fake DNS server.
type Server struct {
	udp  *net.UDPConn
	tcp  net.Listener
	port int

	mu       sync.Mutex
	zone     map[key][]dnsmessage.Resource
	names    map[string]bool
	rcode    *dnsmessage.RCode
	ad       bool
	tc       bool
	wrongID  int
	delay    time.Duration
	oversize int
	closeTCP int
	queries  map[string]int

	wg   sync.WaitGroup
	done chan struct{}
}

// Start listens on 127.0.0.1 with the same free port for UDP and TCP.
func Start() (*Server, error) {
	var lastErr error
	for range 20 {
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return nil, err
		}
		port := udp.LocalAddr().(*net.UDPAddr).Port
		tcp, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			_ = udp.Close()
			lastErr = err
			continue
		}
		s := &Server{
			udp: udp, tcp: tcp, port: port,
			zone: map[key][]dnsmessage.Resource{}, names: map[string]bool{},
			queries: map[string]int{}, done: make(chan struct{}),
		}
		s.wg.Add(2)
		go s.serveUDP()
		go s.serveTCP()
		return s, nil
	}
	return nil, fmt.Errorf("fakedns: no common UDP/TCP port: %w", lastErr)
}

// Close stops the server.
func (s *Server) Close() {
	close(s.done)
	_ = s.udp.Close()
	_ = s.tcp.Close()
	s.wg.Wait()
}

// Host is the address the server listens on.
func (s *Server) Host() string { return "127.0.0.1" }

// Port is the port of UDP and TCP.
func (s *Server) Port() int { return s.port }

// Queries returns the number of queries received over "udp" or "tcp".
func (s *Server) Queries(transport string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[transport]
}

// SetRCode makes every answer carry rc; nil restores the zone behaviour.
func (s *Server) SetRCode(rc *dnsmessage.RCode) { s.mu.Lock(); s.rcode = rc; s.mu.Unlock() }

// SetAD sets the AD bit of the answers.
func (s *Server) SetAD(ad bool) { s.mu.Lock(); s.ad = ad; s.mu.Unlock() }

// SetTruncateUDP answers UDP queries with the TC bit and no records.
func (s *Server) SetTruncateUDP(tc bool) { s.mu.Lock(); s.tc = tc; s.mu.Unlock() }

// SetWrongID sets the wrong-ID mode (WrongIDOff, WrongIDFirst,
// WrongIDOnly).
func (s *Server) SetWrongID(mode int) { s.mu.Lock(); s.wrongID = mode; s.mu.Unlock() }

// SetDelay delays every answer.
func (s *Server) SetDelay(d time.Duration) { s.mu.Lock(); s.delay = d; s.mu.Unlock() }

// SetOversize pads every answer with TXT records to more than n bytes; 0
// turns padding off.
func (s *Server) SetOversize(n int) { s.mu.Lock(); s.oversize = n; s.mu.Unlock() }

// SetCloseTCP sets the close mode of TCP connections (CloseTCPOff,
// CloseTCPNoAnswer, CloseTCPHalfPrefix); the query is still counted.
func (s *Server) SetCloseTCP(mode int) { s.mu.Lock(); s.closeTCP = mode; s.mu.Unlock() }

// Add adds a record; the name of its header is the owner name.
func (s *Server) Add(rr *dnsmessage.Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := canonical(rr.Header.Name.String())
	if rr.Header.Class == 0 {
		rr.Header.Class = dnsmessage.ClassINET
	}
	if rr.Header.TTL == 0 {
		rr.Header.TTL = 300
	}
	k := key{name, rr.Header.Type}
	s.zone[k] = append(s.zone[k], *rr)
	s.names[name] = true
}

func hdr(name string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(canonical(name)), Type: typ, Class: dnsmessage.ClassINET}
}

// AddA adds an A record.
func (s *Server) AddA(name, ip string) {
	a := netip.MustParseAddr(ip).As4()
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: a}})
}

// AddAAAA adds an AAAA record.
func (s *Server) AddAAAA(name, ip string) {
	a := netip.MustParseAddr(ip).As16()
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: a}})
}

// AddCNAME adds a CNAME record.
func (s *Server) AddCNAME(name, target string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeCNAME), Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(canonical(target))}})
}

// AddMX adds an MX record.
func (s *Server) AddMX(name string, pref uint16, host string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeMX), Body: &dnsmessage.MXResource{Pref: pref, MX: dnsmessage.MustNewName(canonical(host))}})
}

// AddNS adds an NS record.
func (s *Server) AddNS(name, ns string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeNS), Body: &dnsmessage.NSResource{NS: dnsmessage.MustNewName(canonical(ns))}})
}

// AddSOA adds an SOA record.
func (s *Server) AddSOA(name, ns, mbox string, serial uint32) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeSOA), Body: &dnsmessage.SOAResource{
		NS: dnsmessage.MustNewName(canonical(ns)), MBox: dnsmessage.MustNewName(canonical(mbox)),
		Serial: serial, Refresh: 3600, Retry: 600, Expire: 86400, MinTTL: 300,
	}})
}

// AddTXT adds a TXT record.
func (s *Server) AddTXT(name string, txt ...string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeTXT), Body: &dnsmessage.TXTResource{TXT: txt}})
}

// AddPTR adds a PTR record.
func (s *Server) AddPTR(name, ptr string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypePTR), Body: &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(canonical(ptr))}})
}

// AddSRV adds an SRV record.
func (s *Server) AddSRV(name string, prio, weight, port uint16, target string) {
	s.Add(&dnsmessage.Resource{Header: hdr(name, dnsmessage.TypeSRV), Body: &dnsmessage.SRVResource{
		Priority: prio, Weight: weight, Port: port, Target: dnsmessage.MustNewName(canonical(target)),
	}})
}

// AddCAA adds a CAA record (RFC 8659: flags, tag length, tag, value).
func (s *Server) AddCAA(name string, flags uint8, tag, value string) {
	data := append([]byte{flags, byte(len(tag))}, tag...)
	data = append(data, value...)
	s.Add(&dnsmessage.Resource{Header: hdr(name, TypeCAA), Body: &dnsmessage.UnknownResource{Type: TypeCAA, Data: data}})
}

func canonical(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

func (s *Server) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, from, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		query := append([]byte(nil), buf[:n]...)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for _, msg := range s.answers(query, "udp") {
				_, _ = s.udp.WriteToUDP(msg, from)
			}
		}()
	}
}

func (s *Server) serveTCP() {
	defer s.wg.Done()
	for {
		c, err := s.tcp.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			for {
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(l[:]))
				if _, err := io.ReadFull(c, query); err != nil {
					return
				}
				s.mu.Lock()
				closeMode := s.closeTCP
				if closeMode != CloseTCPOff {
					s.queries["tcp"]++
				}
				s.mu.Unlock()
				switch closeMode {
				case CloseTCPNoAnswer:
					return
				case CloseTCPHalfPrefix:
					_, _ = c.Write([]byte{0})
					return
				}
				for _, msg := range s.answers(query, "tcp") {
					out := binary.BigEndian.AppendUint16(nil, uint16(len(msg))) //nolint:gosec // answers stay below 64 KiB
					if _, err := c.Write(append(out, msg...)); err != nil {
						return
					}
				}
			}
		}()
	}
}

// answers builds the messages sent for one query, in order.
func (s *Server) answers(query []byte, transport string) [][]byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	s.mu.Lock()
	s.queries[transport]++
	delay, wrong := s.delay, s.wrongID
	rcode, ad, tc, oversize := s.rcode, s.ad, s.tc && transport == "udp", s.oversize
	name := canonical(q.Name.String())
	records := append([]dnsmessage.Resource(nil), s.zone[key{name, q.Type}]...)
	known := s.names[name]
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-s.done:
			return nil
		}
	}
	rc := dnsmessage.RCodeSuccess
	if !known {
		rc = dnsmessage.RCodeNameError
	}
	if rcode != nil {
		rc = *rcode
	}
	if tc {
		records = nil
	}
	build := func(id uint16) []byte {
		msg, err := buildMessage(id, &h, &q, rc, ad, tc, records, oversize)
		if err != nil {
			return nil
		}
		return msg
	}
	switch wrong {
	case WrongIDFirst:
		return [][]byte{build(h.ID ^ 0x5a5a), build(h.ID)}
	case WrongIDOnly:
		return [][]byte{build(h.ID ^ 0x5a5a)}
	}
	return [][]byte{build(h.ID)}
}

func buildMessage(id uint16, qh *dnsmessage.Header, q *dnsmessage.Question, rc dnsmessage.RCode, ad, tc bool, records []dnsmessage.Resource, oversize int) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: id, Response: true, Authoritative: true, RecursionDesired: qh.RecursionDesired,
		RecursionAvailable: true, AuthenticData: ad, Truncated: tc, RCode: rc,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(*q); err != nil {
		return nil, err
	}
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	// records is the per-query copy made in answers, so the owner name
	// may be rewritten in place.
	for i := range records {
		rr := &records[i]
		rr.Header.Name = q.Name // keep the spelling of the question
		if err := addResource(&b, rr); err != nil {
			return nil, err
		}
	}
	size := 0
	for i := 0; oversize > 0 && size <= oversize; i++ {
		pad := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 1}
		if err := b.TXTResource(pad, dnsmessage.TXTResource{TXT: []string{strings.Repeat("p", 250)}}); err != nil {
			return nil, err
		}
		size += 250 + 12
	}
	return b.Finish()
}

func addResource(b *dnsmessage.Builder, rr *dnsmessage.Resource) error {
	switch body := rr.Body.(type) {
	case *dnsmessage.AResource:
		return b.AResource(rr.Header, *body)
	case *dnsmessage.AAAAResource:
		return b.AAAAResource(rr.Header, *body)
	case *dnsmessage.CNAMEResource:
		return b.CNAMEResource(rr.Header, *body)
	case *dnsmessage.MXResource:
		return b.MXResource(rr.Header, *body)
	case *dnsmessage.NSResource:
		return b.NSResource(rr.Header, *body)
	case *dnsmessage.SOAResource:
		return b.SOAResource(rr.Header, *body)
	case *dnsmessage.TXTResource:
		return b.TXTResource(rr.Header, *body)
	case *dnsmessage.PTRResource:
		return b.PTRResource(rr.Header, *body)
	case *dnsmessage.SRVResource:
		return b.SRVResource(rr.Header, *body)
	case *dnsmessage.UnknownResource:
		return b.UnknownResource(rr.Header, *body)
	}
	return errors.New("fakedns: unsupported record body")
}
