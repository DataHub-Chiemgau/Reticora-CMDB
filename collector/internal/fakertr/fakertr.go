// Package fakertr is an RPKI-to-Router cache (RTR, RFC 8210 and RFC 6810)
// over TCP loopback for the rpki_rtr probe (WP-1020, PRT-17, PRT-21). It
// serves a VRP set with a session ID and serial, keeps the announcements
// and withdrawals between serials for Serial Queries, sends Serial Notify on
// an update and has knobs for the cases a client must handle: protocol
// version 0 or 1, Cache Reset, Error Report and a delay before answers.
// It is test infrastructure like collector/internal/fakesnmp; the release
// build does not use it.
package fakertr

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

// PDU types of RFC 8210 section 5.
const (
	TypeSerialNotify  = 0
	TypeSerialQuery   = 1
	TypeResetQuery    = 2
	TypeCacheResponse = 3
	TypeIPv4Prefix    = 4
	TypeIPv6Prefix    = 6
	TypeEndOfData     = 7
	TypeCacheReset    = 8
	TypeRouterKey     = 9
	TypeErrorReport   = 10
)

// Error codes of RFC 8210 section 12.
const (
	ErrCorruptData        = 0
	ErrInternal           = 1
	ErrNoDataAvailable    = 2
	ErrInvalidRequest     = 3
	ErrUnsupportedVersion = 4
	ErrUnsupportedPDU     = 5
)

// VRP is a validated ROA payload: prefix, maximum length and origin AS.
type VRP struct {
	Prefix netip.Prefix
	MaxLen uint8
	ASN    uint32
}

// Delta is the change from one serial to the next. Gen, when set, adds
// GenN announcements produced on the fly (large sets without holding them);
// they are served only incrementally, never in a full Reset Query answer.
type Delta struct {
	Announce []VRP
	Withdraw []VRP
	Gen      func(i int) VRP
	GenN     int
}

// Config is the initial state of the cache.
type Config struct {
	// Version is the protocol version the cache speaks (0 or 1). A cache
	// of version 0 answers a version 1 query with an Error Report
	// "Unsupported Protocol Version" in version 0 and closes.
	Version   uint8
	SessionID uint16
	Serial    uint32
	// Refresh, Retry and Expire go into End of Data (version 1); zero
	// takes 3600, 600 and 7200 s.
	Refresh, Retry, Expire uint32
	VRPs                   []VRP
	// RouterKeys adds one Router Key PDU per full answer (version 1),
	// which a client must skip.
	RouterKeys bool
}

// Query is a query the cache received.
type Query struct {
	Type    uint8
	Version uint8
	Session uint16
	Serial  uint32
}

// Server is the fake cache.
type Server struct {
	ln net.Listener

	mu         sync.Mutex
	cfg        Config
	current    map[VRP]bool
	history    map[uint32]Delta // delta from serial-1 to serial
	conns      map[net.Conn]*sync.Mutex
	accepted   int
	queries    []Query
	cacheReset bool
	failNext   *uint16
	delay      time.Duration
	wg         sync.WaitGroup
	closed     bool
}

// Listen starts a cache on 127.0.0.1 with a free port.
func Listen(cfg Config) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if cfg.Refresh == 0 {
		cfg.Refresh = 3600
	}
	if cfg.Retry == 0 {
		cfg.Retry = 600
	}
	if cfg.Expire == 0 {
		cfg.Expire = 7200
	}
	s := &Server{ln: ln, cfg: cfg, current: map[VRP]bool{}, history: map[uint32]Delta{}, conns: map[net.Conn]*sync.Mutex{}}
	for _, v := range cfg.VRPs {
		s.current[v] = true
	}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Addr returns the listen address.
func (s *Server) Addr() *net.TCPAddr { return s.ln.Addr().(*net.TCPAddr) }

// Close stops the cache and closes every connection.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

// Accepted returns the number of TCP connections accepted so far.
func (s *Server) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// Open returns the number of open connections.
func (s *Server) Open() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Queries returns the queries received so far.
func (s *Server) Queries() []Query {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Query(nil), s.queries...)
}

// Serial returns the current serial.
func (s *Server) Serial() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Serial
}

// Update applies d as the next serial and sends Serial Notify to every
// connection; it returns the new serial.
func (s *Server) Update(d Delta) uint32 {
	s.mu.Lock()
	s.cfg.Serial++
	serial := s.cfg.Serial
	s.history[serial] = d
	for _, v := range d.Withdraw {
		delete(s.current, v)
	}
	for _, v := range d.Announce {
		s.current[v] = true
	}
	type target struct {
		c  net.Conn
		mu *sync.Mutex
	}
	conns := make([]target, 0, len(s.conns))
	for c, mu := range s.conns {
		conns = append(conns, target{c, mu})
	}
	version, session := s.cfg.Version, s.cfg.SessionID
	s.mu.Unlock()
	for _, t := range conns {
		t.mu.Lock()
		_, _ = t.c.Write(header(version, TypeSerialNotify, session, 12, serial))
		t.mu.Unlock()
	}
	return serial
}

// SetSessionID changes the session ID (a cache restart): the history is
// dropped, the next Serial Query is answered with Cache Reset.
func (s *Server) SetSessionID(id uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.SessionID = id
	s.history = map[uint32]Delta{}
}

// CacheResetNext answers the next Serial Query with Cache Reset.
func (s *Server) CacheResetNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheReset = true
}

// FailNext answers the next query with an Error Report of code.
func (s *Server) FailNext(code uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = &code
}

// SetDelay delays every answer by d.
func (s *Server) SetDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.accepted++
		wmu := &sync.Mutex{}
		s.conns[c] = wmu
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c, wmu)
	}
}

func (s *Server) serve(c net.Conn, wmu *sync.Mutex) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close()
	}()
	r := bufio.NewReader(c)
	for {
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		length := binary.BigEndian.Uint32(hdr[4:8])
		if length < 8 || length > 1<<16 {
			return
		}
		body := make([]byte, length-8)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		q := Query{Version: hdr[0], Type: hdr[1], Session: binary.BigEndian.Uint16(hdr[2:4])}
		if q.Type == TypeSerialQuery && len(body) >= 4 {
			q.Serial = binary.BigEndian.Uint32(body[:4])
		}
		s.mu.Lock()
		s.queries = append(s.queries, q)
		delay := s.delay
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		wmu.Lock()
		ok := s.answer(c, q, append(hdr, body...))
		wmu.Unlock()
		if !ok {
			return
		}
	}
}

// answer writes the answer to q; false closes the connection.
func (s *Server) answer(c net.Conn, q Query, raw []byte) bool {
	w := bufio.NewWriterSize(c, 64<<10)
	s.mu.Lock()
	version, session := s.cfg.Version, s.cfg.SessionID
	if q.Version > version {
		s.mu.Unlock()
		_, _ = c.Write(errorReport(version, ErrUnsupportedVersion, raw))
		return false
	}
	if s.failNext != nil {
		code := *s.failNext
		s.failNext = nil
		s.mu.Unlock()
		_, _ = c.Write(errorReport(version, code, raw))
		return false
	}
	switch q.Type {
	case TypeResetQuery:
		full := make([]VRP, 0, len(s.current))
		for v := range s.current {
			full = append(full, v)
		}
		cfg := s.cfg
		s.mu.Unlock()
		_, _ = w.Write(header(version, TypeCacheResponse, session, 8, 0))
		for _, v := range full {
			_, _ = w.Write(prefixPDU(version, v, true))
		}
		if cfg.RouterKeys && version >= 1 {
			_, _ = w.Write(routerKey(version))
		}
		_, _ = w.Write(endOfData(version, session, &cfg))
		return w.Flush() == nil
	case TypeSerialQuery:
		if s.cacheReset || q.Session != session {
			s.cacheReset = false
			s.mu.Unlock()
			_, _ = c.Write(header(version, TypeCacheReset, 0, 8, 0))
			return true
		}
		var deltas []Delta
		for serial := q.Serial + 1; serial != s.cfg.Serial+1; serial++ {
			d, ok := s.history[serial]
			if !ok {
				s.mu.Unlock()
				_, _ = c.Write(header(version, TypeCacheReset, 0, 8, 0))
				return true
			}
			deltas = append(deltas, d)
		}
		cfg := s.cfg
		s.mu.Unlock()
		_, _ = w.Write(header(version, TypeCacheResponse, session, 8, 0))
		for _, d := range deltas {
			for _, v := range d.Withdraw {
				_, _ = w.Write(prefixPDU(version, v, false))
			}
			for _, v := range d.Announce {
				_, _ = w.Write(prefixPDU(version, v, true))
			}
			for i := 0; i < d.GenN; i++ {
				_, _ = w.Write(prefixPDU(version, d.Gen(i), true))
			}
		}
		_, _ = w.Write(endOfData(version, session, &cfg))
		return w.Flush() == nil
	}
	s.mu.Unlock()
	_, _ = c.Write(errorReport(version, ErrUnsupportedPDU, raw))
	return false
}

// header returns a PDU of 8 or 12 bytes (12: with the 32-bit field).
func header(version, typ uint8, session uint16, length, field uint32) []byte {
	b := make([]byte, length)
	b[0], b[1] = version, typ
	binary.BigEndian.PutUint16(b[2:4], session)
	binary.BigEndian.PutUint32(b[4:8], length)
	if length >= 12 {
		binary.BigEndian.PutUint32(b[8:12], field)
	}
	return b
}

func prefixPDU(version uint8, v VRP, announce bool) []byte {
	addr := v.Prefix.Addr()
	var flags uint8
	if announce {
		flags = 1
	}
	if addr.Is4() {
		b := header(version, TypeIPv4Prefix, 0, 20, 0)
		b[8], b[9], b[10] = flags, uint8(v.Prefix.Bits()), v.MaxLen //nolint:gosec // prefix length ≤ 32
		a := addr.As4()
		copy(b[12:16], a[:])
		binary.BigEndian.PutUint32(b[16:20], v.ASN)
		return b
	}
	b := header(version, TypeIPv6Prefix, 0, 32, 0)
	b[8], b[9], b[10] = flags, uint8(v.Prefix.Bits()), v.MaxLen //nolint:gosec // prefix length ≤ 128
	a := addr.As16()
	copy(b[12:28], a[:])
	binary.BigEndian.PutUint32(b[28:32], v.ASN)
	return b
}

func endOfData(version uint8, session uint16, cfg *Config) []byte {
	if version == 0 {
		return header(0, TypeEndOfData, session, 12, cfg.Serial)
	}
	b := header(version, TypeEndOfData, session, 24, cfg.Serial)
	binary.BigEndian.PutUint32(b[12:16], cfg.Refresh)
	binary.BigEndian.PutUint32(b[16:20], cfg.Retry)
	binary.BigEndian.PutUint32(b[20:24], cfg.Expire)
	return b
}

// routerKey returns a Router Key PDU (RFC 8210 section 5.10) with a dummy
// SKI and key.
func routerKey(version uint8) []byte {
	const length = 8 + 20 + 4 + 16
	b := header(version, TypeRouterKey, 0, length, 0)
	b[2] = 1 // flags: announce
	binary.BigEndian.PutUint32(b[28:32], 64500)
	return b
}

func errorReport(version uint8, code uint16, pdu []byte) []byte {
	text := []byte("fakertr")
	length := uint32(8 + 4 + len(pdu) + 4 + len(text)) //nolint:gosec // small
	b := header(version, TypeErrorReport, code, length, 0)
	binary.BigEndian.PutUint32(b[8:12], uint32(len(pdu))) //nolint:gosec // small
	copy(b[12:], pdu)
	off := 12 + len(pdu)
	binary.BigEndian.PutUint32(b[off:off+4], uint32(len(text))) //nolint:gosec // small
	copy(b[off+4:], text)
	return b
}
