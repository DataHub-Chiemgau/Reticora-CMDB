package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ICMP multiplexer (PRT-02, NFR-11, WP-953): one ICMP socket per address
// family for the whole process instead of one per ping. Every echo request
// waits in a table of the family under its sequence number; one reader per
// socket hands each echo reply to the request it answers: by (peer, id,
// sequence) on a raw socket, by (peer, sequence) on an unprivileged one,
// whose identifier the kernel rewrites. The first bytes of the payload
// carry a token of the request, so a late reply never answers a later
// request with the same sequence number.
//
// Capability (the order of sweep.ICMPCapability): first the unprivileged
// datagram socket (udp4 / udp6; Linux allows it for the groups in
// net.ipv4.ping_group_range, which also covers ICMPv6), else the raw
// socket (ip4:icmp / ip6:ipv6-icmp, needs CAP_NET_RAW). Without either the
// family is unavailable and a ping of it fails with ErrICMPUnavailable.
//
// A socket error (e.g. a network change) closes the socket; the next ping
// opens it again. Waiting requests end at the latest with their timeout.

// Payload limits of an echo request: 1472 bytes over IPv4, 1452 over IPv6
// (an Ethernet MTU of 1500 less the IP and ICMP headers).
const (
	MaxICMPPayloadV4 = 1472
	MaxICMPPayloadV6 = 1452
)

// icmpTokenLen is the length of the request token at the start of the
// payload.
const icmpTokenLen = 8

// Errors of the multiplexer.
var (
	// ErrICMPUnavailable: the process may open no ICMP socket of the
	// family (needs net.ipv4.ping_group_range or CAP_NET_RAW).
	ErrICMPUnavailable = errors.New("probe: no ICMP socket available (needs net.ipv4.ping_group_range or CAP_NET_RAW)")
	// ErrICMPPayload: the payload exceeds the limit of the family.
	ErrICMPPayload = errors.New("probe: ICMP payload too large")
	// ErrICMPBusy: every sequence number of the family is in use.
	ErrICMPBusy = errors.New("probe: every ICMP sequence number is in use")
)

// PacketConn is an ICMP socket as the multiplexer uses it;
// *icmp.PacketConn satisfies it.
type PacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	Close() error
}

// ListenFunc opens the ICMP socket of a family (4 or 6) and reports
// whether it is unprivileged (a datagram socket).
type ListenFunc func(family int) (conn PacketConn, unprivileged bool, err error)

// ListenICMP opens the system socket of a family: the unprivileged one
// first, else the raw one.
func ListenICMP(family int) (PacketConn, bool, error) {
	networks := [2]string{"udp4", "ip4:icmp"}
	address := "0.0.0.0"
	if family == 6 {
		networks, address = [2]string{"udp6", "ip6:ipv6-icmp"}, "::"
	}
	if c, err := icmp.ListenPacket(networks[0], address); err == nil {
		return c, true, nil
	}
	if c, err := icmp.ListenPacket(networks[1], address); err == nil {
		return c, false, nil
	}
	return nil, false, ErrICMPUnavailable
}

// PacketResult is the outcome of one echo request: its round trip time,
// or Lost.
type PacketResult struct {
	RTT  time.Duration
	Lost bool
}

// Mux multiplexes the echo requests of the process over one socket per
// family.
type Mux struct {
	id     int
	listen ListenFunc
	v4, v6 *familyMux
	tokens atomic.Uint64
}

// NewMux returns a multiplexer that opens its sockets with listen (nil:
// ListenICMP) on the first ping of a family.
func NewMux(listen ListenFunc) *Mux {
	if listen == nil {
		listen = ListenICMP
	}
	m := &Mux{id: os.Getpid() & 0xffff, listen: listen}
	m.v4 = &familyMux{mux: m, family: 4, pending: map[uint16]*echoWaiter{}}
	m.v6 = &familyMux{mux: m, family: 6, pending: map[uint16]*echoWaiter{}}
	m.tokens.Store(uint64(time.Now().UnixNano()))
	return m
}

var (
	defaultMuxOnce sync.Once
	defaultMux     *Mux
)

// DefaultMux is the multiplexer of the process on the system sockets.
func DefaultMux() *Mux {
	defaultMuxOnce.Do(func() { defaultMux = NewMux(nil) })
	return defaultMux
}

// Available reports whether the family can open its socket (it stays open
// for the pings that follow).
func (m *Mux) Available(family int) bool {
	_, err := m.family(family).socket()
	return err == nil
}

// Close closes the sockets; pings that follow open them again.
func (m *Mux) Close() {
	m.v4.drop(nil)
	m.v6.drop(nil)
}

func (m *Mux) family(f int) *familyMux {
	if f == 6 {
		return m.v6
	}
	return m.v4
}

// Ping sends count echo requests of size payload bytes to ip, spacing
// apart, and waits for the replies until timeout after the first send. It
// returns one PacketResult per request. It fails with ErrICMPUnavailable
// without a socket of the family, ErrICMPPayload above the payload limit.
func (m *Mux) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration, count, size int, spacing time.Duration) ([]PacketResult, error) {
	ip = ip.Unmap()
	fam := 4
	limit := MaxICMPPayloadV4
	if ip.Is6() {
		fam, limit = 6, MaxICMPPayloadV6
	}
	if size < 0 || size > limit {
		return nil, fmt.Errorf("%w: %d bytes, at most %d over IPv%d", ErrICMPPayload, size, limit, fam)
	}
	count = max(count, 1)
	f := m.family(fam)
	if _, err := f.socket(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	waiters := make([]*echoWaiter, 0, count)
	defer func() {
		for _, w := range waiters {
			f.release(w)
		}
	}()
	for i := range count {
		if i > 0 {
			t := time.NewTimer(spacing)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			waiters = append(waiters, nil)
			continue
		}
		w, err := f.send(ip, size, m.tokens.Add(1))
		if err != nil {
			if errors.Is(err, ErrICMPUnavailable) && i == 0 {
				return nil, err
			}
			waiters = append(waiters, nil) // a failed send is a lost packet
			continue
		}
		waiters = append(waiters, w)
	}
	out := make([]PacketResult, len(waiters))
	wait := time.NewTimer(time.Until(deadline))
	defer wait.Stop()
	for i, w := range waiters {
		if w == nil {
			out[i].Lost = true
			continue
		}
		select {
		case rtt := <-w.reply:
			out[i].RTT = rtt
		case <-wait.C:
			// Deadline: this and every later request without a reply are lost.
			for j := i; j < len(waiters); j++ {
				if waiters[j] == nil {
					out[j].Lost = true
					continue
				}
				select {
				case rtt := <-waiters[j].reply:
					out[j].RTT = rtt
				default:
					out[j].Lost = true
				}
			}
			return out, nil
		case <-ctx.Done():
			for j := i; j < len(waiters); j++ {
				out[j].Lost = true
			}
			return out, nil
		}
	}
	return out, nil
}

// echoWaiter is a request waiting for its reply.
type echoWaiter struct {
	seq   uint16
	peer  netip.Addr
	token [icmpTokenLen]byte
	sent  time.Time
	reply chan time.Duration
}

// familyMux is the socket of one family with its waiting requests.
type familyMux struct {
	mux    *Mux
	family int

	mu      sync.Mutex
	conn    PacketConn
	unpriv  bool
	nextSeq uint16
	pending map[uint16]*echoWaiter
}

// socket returns the open socket, opening it when needed.
func (f *familyMux) socket() (PacketConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.socketLocked()
}

func (f *familyMux) socketLocked() (PacketConn, error) {
	if f.conn != nil {
		return f.conn, nil
	}
	conn, unpriv, err := f.mux.listen(f.family)
	if err != nil {
		return nil, ErrICMPUnavailable
	}
	f.conn, f.unpriv = conn, unpriv
	go f.read(conn, unpriv)
	return conn, nil
}

// drop closes conn when it is still the socket of the family (nil: the
// current one); the next send opens a new one.
func (f *familyMux) drop(conn PacketConn) {
	f.mu.Lock()
	cur := f.conn
	if cur != nil && (conn == nil || conn == cur) {
		f.conn = nil
	}
	f.mu.Unlock()
	if cur != nil && (conn == nil || conn == cur) {
		_ = cur.Close()
	}
}

// send registers a request under a free sequence number and writes it.
func (f *familyMux) send(ip netip.Addr, size int, token uint64) (*echoWaiter, error) {
	f.mu.Lock()
	conn, err := f.socketLocked()
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	seq, ok := f.freeSeqLocked()
	if !ok {
		f.mu.Unlock()
		return nil, ErrICMPBusy
	}
	// The payload carries the whole token, also when the check asks for
	// fewer bytes (payload_bytes 0 to 7): a late reply of an earlier
	// request with the same sequence (after the 16-bit wrap-around) never
	// answers this one (AK5).
	size = max(size, icmpTokenLen)
	w := &echoWaiter{seq: seq, peer: ip.WithZone(""), reply: make(chan time.Duration, 1)}
	binary.BigEndian.PutUint64(w.token[:], token)
	raw, err := f.echoRequest(seq, w.token, size)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	// sent is set before the request becomes visible to the reader, under
	// the lock deliver reads it with.
	w.sent = time.Now()
	f.pending[seq] = w
	unpriv := f.unpriv
	f.mu.Unlock()

	// The zone of a link-local target (fe80::1%eth0) names the interface
	// to send on.
	var dst net.Addr = &net.IPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
	if unpriv {
		dst = &net.UDPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
	}
	if _, err := conn.WriteTo(raw, dst); err != nil {
		f.release(w)
		return nil, err
	}
	return w, nil
}

// freeSeqLocked returns the next sequence number no waiting request holds
// (after a wrap-around the numbers still in use are skipped).
func (f *familyMux) freeSeqLocked() (uint16, bool) {
	if len(f.pending) >= 1<<16 {
		return 0, false
	}
	for range 1 << 16 {
		f.nextSeq++
		if _, used := f.pending[f.nextSeq]; !used {
			return f.nextSeq, true
		}
	}
	return 0, false
}

// release removes a request from the table.
func (f *familyMux) release(w *echoWaiter) {
	if w == nil {
		return
	}
	f.mu.Lock()
	if f.pending[w.seq] == w {
		delete(f.pending, w.seq)
	}
	f.mu.Unlock()
}

// echoRequest encodes an echo request with the token at the start of a
// payload of size bytes.
func (f *familyMux) echoRequest(seq uint16, token [icmpTokenLen]byte, size int) ([]byte, error) {
	data := make([]byte, size)
	copy(data, token[:])
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if f.family == 6 {
		typ = ipv6.ICMPTypeEchoRequest
	}
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: f.mux.id, Seq: int(seq), Data: data}}
	return msg.Marshal(nil)
}

// read is the reader of a socket: it hands every echo reply to its
// request until the socket fails; then the socket is dropped (the next
// send opens a new one).
func (f *familyMux) read(conn PacketConn, unpriv bool) {
	buf := make([]byte, 1<<16)
	proto, replyType := 1, icmp.Type(ipv4.ICMPTypeEchoReply)
	if f.family == 6 {
		proto, replyType = 58, ipv6.ICMPTypeEchoReply
	}
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			f.mu.Lock()
			current := f.conn == conn
			f.mu.Unlock()
			if current {
				slog.Warn("ICMP socket failed; it is opened again", "family", f.family, "error", err)
				f.drop(conn)
			}
			return
		}
		at := time.Now()
		msg, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil || msg.Type != replyType {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || (!unpriv && echo.ID != f.mux.id) {
			continue
		}
		f.deliver(peerAddr(peer), uint16(echo.Seq), echo.Data, at) //nolint:gosec // the sequence number is 16 bits on the wire
	}
}

// deliver hands a reply to the request with seq when the peer (without
// its zone) and the token match.
func (f *familyMux) deliver(peer netip.Addr, seq uint16, data []byte, at time.Time) {
	f.mu.Lock()
	w, ok := f.pending[seq]
	var sent time.Time
	if ok && w.peer == peer && tokenMatches(w, data) {
		delete(f.pending, seq)
		sent = w.sent
	} else {
		ok = false
	}
	f.mu.Unlock()
	if ok {
		w.reply <- at.Sub(sent)
	}
}

// tokenMatches compares the token at the start of the payload (every
// request carries all icmpTokenLen bytes of it).
func tokenMatches(w *echoWaiter, data []byte) bool {
	if len(data) < icmpTokenLen {
		return false
	}
	for i := range icmpTokenLen {
		if data[i] != w.token[i] {
			return false
		}
	}
	return true
}

// peerAddr returns the IP of a peer address without its zone (the zone of
// a link-local reply names the interface, by name or number, so requests
// and replies compare without it; the token keeps them apart).
func peerAddr(a net.Addr) netip.Addr {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	}
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap()
}

// SweepPinger adapts the multiplexer to sweep.Pinger (one echo request per
// call, WP-953 AK6).
type SweepPinger struct {
	Mux *Mux
	// Size is the payload of the sweep's echo requests.
	Size int
}

// Ping implements sweep.Pinger.
func (p SweepPinger) Ping(ctx context.Context, ip string, timeout time.Duration) (time.Duration, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return 0, fmt.Errorf("probe: ICMP needs an IP address, got %q", ip)
	}
	res, err := p.Mux.Ping(ctx, addr, timeout, 1, max(p.Size, icmpTokenLen), 0)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 || res[0].Lost {
		return 0, fmt.Errorf("probe: no echo reply from %s", ip)
	}
	return res[0].RTT, nil
}
