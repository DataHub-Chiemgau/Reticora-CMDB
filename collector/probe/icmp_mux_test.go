package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// fakeICMP is an ICMP socket that answers echo requests itself: after
// delay(peer, seq), unless drop says so, with the identifier rewritten on
// an unprivileged socket (as the Linux kernel does).
type fakeICMP struct {
	family  int
	unpriv  bool
	delay   func(peer netip.Addr, seq int) time.Duration
	drop    func(peer netip.Addr, seq int) bool
	readErr chan error // a value makes the next ReadFrom fail

	mu      sync.Mutex
	closed  bool
	replies chan fakePacket
	sent    atomic.Int64
	seqs    sync.Map // seq → struct{} of the requests written
}

type fakePacket struct {
	data []byte
	peer net.Addr
}

func newFakeICMP(family int, unpriv bool) *fakeICMP {
	return &fakeICMP{family: family, unpriv: unpriv, replies: make(chan fakePacket, 1<<16), readErr: make(chan error, 1)}
}

func (c *fakeICMP) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case err := <-c.readErr:
		return 0, nil, err
	case p, ok := <-c.replies:
		if !ok {
			return 0, nil, net.ErrClosed
		}
		return copy(b, p.data), p.peer, nil
	}
}

func (c *fakeICMP) WriteTo(b []byte, dst net.Addr) (int, error) {
	return c.writeTo(b, dst, dst)
}

// writeTo answers from the address from (the destination, or another
// spelling of it).
func (c *fakeICMP) writeTo(b []byte, dst, from net.Addr) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	proto := 1
	if c.family == 6 {
		proto = 58
	}
	msg, err := icmp.ParseMessage(proto, b)
	if err != nil {
		return 0, err
	}
	echo := msg.Body.(*icmp.Echo)
	c.sent.Add(1)
	c.seqs.Store(echo.Seq, struct{}{})
	peer := peerAddr(dst)
	if _, isUDP := dst.(*net.UDPAddr); isUDP != c.unpriv {
		return 0, fmt.Errorf("fake: address %T on an unprivileged=%v socket", dst, c.unpriv)
	}
	if c.drop != nil && c.drop(peer, echo.Seq) {
		return len(b), nil
	}
	id := echo.ID
	if c.unpriv {
		id = 0x4242 // the kernel's identifier of the socket
	}
	c.reply(peer, id, echo.Seq, echo.Data, from, c.delayOf(peer, echo.Seq))
	return len(b), nil
}

func (c *fakeICMP) delayOf(peer netip.Addr, seq int) time.Duration {
	if c.delay == nil {
		return 0
	}
	return c.delay(peer, seq)
}

// reply queues an echo reply after d.
func (c *fakeICMP) reply(_ netip.Addr, id, seq int, data []byte, from net.Addr, d time.Duration) {
	var typ icmp.Type = ipv4.ICMPTypeEchoReply
	if c.family == 6 {
		typ = ipv6.ICMPTypeEchoReply
	}
	raw, err := (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: append([]byte(nil), data...)}}).Marshal(nil)
	if err != nil {
		panic(err)
	}
	send := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.closed {
			c.replies <- fakePacket{data: raw, peer: from}
		}
	}
	if d <= 0 {
		send()
		return
	}
	time.AfterFunc(d, send)
}

func (c *fakeICMP) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.replies)
	}
	return nil
}

// fakeListen returns a ListenFunc that hands out the sockets of open in
// turn and counts the calls.
func fakeListen(calls *atomic.Int32, open func(family int) (*fakeICMP, error)) ListenFunc {
	return func(family int) (PacketConn, bool, error) {
		calls.Add(1)
		c, err := open(family)
		if err != nil {
			return nil, false, err
		}
		return c, c.unpriv, nil
	}
}

func addr(t testing.TB, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestMuxDemultiplexesConcurrentRequests covers AK1 (NFR-11): 2,000
// concurrent checks over one socket, every reply reaches its own request
// (the fake delays each target by its own amount; a reply handed to
// another request would come too early or to the wrong peer).
func TestMuxDemultiplexesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	conn := newFakeICMP(4, false)
	delayOf := func(peer netip.Addr) time.Duration {
		b := peer.As4()
		return time.Duration(int(b[3])%20) * time.Millisecond
	}
	conn.delay = func(peer netip.Addr, _ int) time.Duration { return delayOf(peer) }
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return conn, nil }))
	defer m.Close()

	const n = 2000
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peer := netip.AddrFrom4([4]byte{10, 53, byte(i / 250), byte(i%250 + 1)})
			res, err := m.Ping(context.Background(), peer, 5*time.Second, 1, 56, 0)
			switch {
			case err != nil:
				errs <- err
			case len(res) != 1 || res[0].Lost:
				errs <- fmt.Errorf("%s: %+v", peer, res)
			case res[0].RTT < delayOf(peer):
				errs <- fmt.Errorf("%s: rtt %v below the delay %v of its target: a reply of another request", peer, res[0].RTT, delayOf(peer))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d sockets opened for %d checks, want 1", calls.Load(), n)
	}
	if got := conn.sent.Load(); got != n {
		t.Errorf("%d requests written, want %d", got, n)
	}
}

// TestMuxSequenceWrapAround covers AK5: after the wrap-around of the
// 16-bit sequence a number still in use is skipped, and a reply with the
// sequence of a waiting request but another peer or token is not handed
// to it.
func TestMuxSequenceWrapAround(t *testing.T) {
	var calls atomic.Int32
	conn := newFakeICMP(4, false)
	waiting := addr(t, "192.0.2.1")
	conn.drop = func(peer netip.Addr, _ int) bool { return peer == waiting }
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return conn, nil }))
	defer m.Close()

	// A request to waiting holds sequence 1 and gets no reply.
	held := make(chan []PacketResult, 1)
	go func() {
		res, _ := m.Ping(context.Background(), waiting, 2*time.Second, 1, 56, 0)
		held <- res
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.v4.mu.Lock()
		_, ok := m.v4.pending[1]
		m.v4.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiting request did not take sequence 1")
		}
		time.Sleep(time.Millisecond)
	}
	// Late replies to sequence 1: from another peer, and from the right
	// peer with a wrong token. Neither answers the waiting request.
	conn.reply(waiting, m.id, 1, make([]byte, 56), &net.IPAddr{IP: net.ParseIP("192.0.2.99")}, 0)
	conn.reply(waiting, m.id, 1, []byte("wrong-token-wrong-token"), &net.IPAddr{IP: waiting.AsSlice()}, 0)

	// Wrap around: the next numbers are 65535, 0, then 2 (1 is in use).
	m.v4.mu.Lock()
	m.v4.nextSeq = 65534
	m.v4.mu.Unlock()
	other := addr(t, "192.0.2.2")
	for range 3 {
		res, err := m.Ping(context.Background(), other, time.Second, 1, 56, 0)
		if err != nil || res[0].Lost {
			t.Fatalf("ping after the wrap-around: %+v, %v", res, err)
		}
	}
	for _, seq := range []int{65535, 0, 2} {
		if _, ok := conn.seqs.Load(seq); !ok {
			t.Errorf("sequence %d was not used", seq)
		}
	}
	if res := <-held; len(res) != 1 || !res[0].Lost {
		t.Errorf("the waiting request got %+v, want lost (no reply of its own)", res)
	}
}

// TestMuxReopensAfterSocketError covers AK4: a socket error drops the
// socket, the waiting request ends with its timeout, and the next ping
// opens a new socket.
func TestMuxReopensAfterSocketError(t *testing.T) {
	var calls atomic.Int32
	first := newFakeICMP(4, false)
	first.drop = func(netip.Addr, int) bool { return true }
	second := newFakeICMP(4, false)
	sockets := []*fakeICMP{first, second}
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) {
		c := sockets[0]
		if len(sockets) > 1 {
			sockets = sockets[1:]
		}
		return c, nil
	}))
	defer m.Close()

	target := addr(t, "198.51.100.7")
	done := make(chan []PacketResult, 1)
	start := time.Now()
	go func() {
		res, _ := m.Ping(context.Background(), target, 300*time.Millisecond, 1, 56, 0)
		done <- res
	}()
	time.Sleep(50 * time.Millisecond)
	first.readErr <- errors.New("network is down")
	select {
	case res := <-done:
		if len(res) != 1 || !res[0].Lost {
			t.Errorf("request during the socket error: %+v, want lost", res)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Errorf("the request hung %v after the socket error", waited)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting request did not end with its timeout")
	}
	res, err := m.Ping(context.Background(), target, time.Second, 1, 56, 0)
	if err != nil || len(res) != 1 || res[0].Lost {
		t.Fatalf("ping after the reopen: %+v, %v", res, err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d sockets opened, want 2 (one reopen)", calls.Load())
	}
}

// TestMuxUnprivilegedMatchesBySequenceAndPeer covers AK1 for the datagram
// socket: the kernel rewrites the identifier, the reply is matched by
// sequence and peer (and token); a reply of another peer is not.
func TestMuxUnprivilegedMatchesBySequenceAndPeer(t *testing.T) {
	for _, family := range []int{4, 6} {
		var calls atomic.Int32
		conn := newFakeICMP(family, true)
		m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return conn, nil }))
		target := addr(t, map[int]string{4: "203.0.113.5", 6: "2001:db8::5"}[family])
		res, err := m.Ping(context.Background(), target, time.Second, 3, 56, 5*time.Millisecond)
		if err != nil {
			t.Fatalf("IPv%d: %v", family, err)
		}
		for i, r := range res {
			if r.Lost {
				t.Errorf("IPv%d packet %d lost on the unprivileged socket", family, i)
			}
		}
		// A reply of another peer with the sequence of a waiting request.
		conn.drop = func(netip.Addr, int) bool { return true }
		go func() {
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				m.family(family).mu.Lock()
				var w *echoWaiter
				for _, p := range m.family(family).pending {
					w = p
				}
				m.family(family).mu.Unlock()
				if w != nil {
					other := &net.UDPAddr{IP: net.ParseIP(map[int]string{4: "203.0.113.6", 6: "2001:db8::6"}[family])}
					conn.reply(w.peer, 0x4242, int(w.seq), w.token[:], other, 0)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		res, err = m.Ping(context.Background(), target, 200*time.Millisecond, 1, 56, 0)
		if err != nil || len(res) != 1 || !res[0].Lost {
			t.Errorf("IPv%d: a reply of another peer answered the request: %+v, %v", family, res, err)
		}
		m.Close()
	}
}

// TestMuxShortPayloadCarriesToken covers AK5 for payload_bytes 0 to 7:
// the request still carries the whole token, so a late reply of an
// earlier request with the same sequence and peer (after the 16-bit
// wrap-around) does not answer a waiting request.
func TestMuxShortPayloadCarriesToken(t *testing.T) {
	var calls atomic.Int32
	conn := newFakeICMP(4, false)
	target := addr(t, "192.0.2.10")
	conn.drop = func(netip.Addr, int) bool { return true }
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return conn, nil }))
	defer m.Close()
	held := make(chan []PacketResult, 1)
	go func() {
		res, _ := m.Ping(context.Background(), target, 300*time.Millisecond, 1, 0, 0)
		held <- res
	}()
	var w *echoWaiter
	deadline := time.Now().Add(2 * time.Second)
	for w == nil {
		m.v4.mu.Lock()
		for _, p := range m.v4.pending {
			w = p
		}
		m.v4.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the request did not register")
		}
		time.Sleep(time.Millisecond)
	}
	// The reply of an earlier request: same peer and sequence, no payload
	// or the payload of another token.
	conn.reply(target, m.id, int(w.seq), nil, &net.IPAddr{IP: target.AsSlice()}, 0)
	conn.reply(target, m.id, int(w.seq), []byte{1, 2, 3, 4, 5, 6, 7, 8}, &net.IPAddr{IP: target.AsSlice()}, 0)
	if res := <-held; len(res) != 1 || !res[0].Lost {
		t.Errorf("a late reply answered the request of payload 0: %+v, want lost", res)
	}
	// With replies, payload 0 works: the request carried the token.
	conn.drop = nil
	res, err := m.Ping(context.Background(), target, time.Second, 2, 0, 0)
	if err != nil || len(res) != 2 || res[0].Lost || res[1].Lost {
		t.Fatalf("payload 0 with replies: %+v, %v", res, err)
	}
	for _, r := range res {
		if r.RTT <= 0 || r.RTT > time.Second {
			t.Errorf("rtt %v, want the time since the send", r.RTT)
		}
	}
}

// TestMuxLinkLocalZone: a link-local target with a zone is sent with the
// zone (the interface), and the reply, whose zone the kernel names its own
// way, reaches the request.
func TestMuxLinkLocalZone(t *testing.T) {
	for _, unpriv := range []bool{false, true} {
		var calls atomic.Int32
		conn := newFakeICMP(6, unpriv)
		var zones sync.Map
		m := NewMux(func(int) (PacketConn, bool, error) {
			calls.Add(1)
			return zoneConn{fakeICMP: conn, zones: &zones}, unpriv, nil
		})
		target := addr(t, "fe80::1%eth0")
		res, err := m.Ping(context.Background(), target, time.Second, 1, 56, 0)
		if err != nil || len(res) != 1 || res[0].Lost {
			t.Errorf("unprivileged %v: link-local with zone: %+v, %v", unpriv, res, err)
		}
		if z, _ := zones.Load("dst"); z != "eth0" {
			t.Errorf("unprivileged %v: sent with zone %q, want eth0", unpriv, z)
		}
		m.Close()
	}
}

// zoneConn records the zone of the destination and answers with the
// interface number as zone (as the kernel may).
type zoneConn struct {
	*fakeICMP
	zones *sync.Map
}

func (c zoneConn) WriteTo(b []byte, dst net.Addr) (int, error) {
	var reply net.Addr
	switch v := dst.(type) {
	case *net.IPAddr:
		c.zones.Store("dst", v.Zone)
		reply = &net.IPAddr{IP: v.IP, Zone: "2"}
	case *net.UDPAddr:
		c.zones.Store("dst", v.Zone)
		reply = &net.UDPAddr{IP: v.IP, Zone: "2"}
	}
	return c.writeTo(b, dst, reply)
}

// openFDs counts the open file descriptors of the process.
func openFDs(t testing.TB) int {
	t.Helper()
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("cannot count file descriptors: %v", err)
		}
		return len(entries)
	}
	// Elsewhere: the descriptors below 4096 that fstat accepts.
	n := 0
	var st syscall.Stat_t
	for fd := range 4096 {
		if syscall.Fstat(fd, &st) == nil {
			n++
		}
	}
	return n
}

// BenchmarkMuxLoopback covers PRT-19: 2,000 concurrent pings against the
// loopback over the system socket, without a file descriptor per ping
// (skipped without ICMP capability).
func BenchmarkMuxLoopback(b *testing.B) {
	m := NewMux(nil)
	defer m.Close()
	if !m.Available(4) {
		b.Skip("no ICMP capability (net.ipv4.ping_group_range or CAP_NET_RAW)")
	}
	loopback := netip.MustParseAddr("127.0.0.1")
	before := openFDs(b)
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		var lost atomic.Int64
		for range 2000 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := m.Ping(context.Background(), loopback, 5*time.Second, 1, 56, 0)
				if err != nil || res[0].Lost {
					lost.Add(1)
				}
			}()
		}
		// While the pings wait for their replies.
		during := openFDs(b)
		wg.Wait()
		if during > before+16 {
			b.Fatalf("%d file descriptors open during the pings (%d before): a socket per ping", during, before)
		}
		b.ReportMetric(float64(lost.Load()), "lost/op")
	}
}
