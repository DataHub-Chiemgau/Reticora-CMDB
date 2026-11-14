package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func listen(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln
}

func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func TestTCPOpenPort(t *testing.T) {
	ln := listen(t, "tcp4", "127.0.0.1:0")
	r := Run(context.Background(), ptr(validTCPSpec("127.0.0.1", portOf(t, ln))), nil)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("got %s/%s, want ok", r.Status, r.Code)
	}
	if r.RTTMS == nil || *r.RTTMS < 0 {
		t.Fatalf("rtt_ms missing: %+v", r)
	}
	// params.port is required (WP-970 AK1) and replaces a port in the
	// target.
	spec := validTCPSpec("127.0.0.1:1", portOf(t, ln))
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
		t.Fatalf("target with port: got %s/%s", r.Status, r.Code)
	}
}

// tcpSpec is a valid tcp spec with extra params.
func tcpSpec(target string, port int, extra map[string]any) probewire.ProbeSpec {
	p := map[string]any{"port": port}
	for k, v := range extra {
		p[k] = v
	}
	params, _ := json.Marshal(p)
	spec := validTCPSpec(target, port)
	spec.Params = params
	return spec
}

// peer is a TCP server for one connection at a time: it writes banner
// (if any), records what it receives until the client closes or hold
// ends, and can close without sending.
type peer struct {
	ln       net.Listener
	mu       sync.Mutex
	received []byte
	done     chan struct{}
}

func newPeer(t *testing.T, serve func(c net.Conn)) *peer {
	return newRecordingPeer(t, func(_ *peer, c net.Conn) { serve(c) })
}

// newRecordingPeer passes the peer itself to serve.
func newRecordingPeer(t *testing.T, serve func(p *peer, c net.Conn)) *peer {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen: %v", err)
	}
	p := &peer{ln: ln, done: make(chan struct{}, 16)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close(); p.done <- struct{}{} }()
				serve(p, c)
			}()
		}
	}()
	return p
}

// record reads everything the client sends until it closes.
func (p *peer) record(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(c)
	p.mu.Lock()
	p.received = append(p.received, b...)
	p.mu.Unlock()
}

func (p *peer) got() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.received...)
}

func (p *peer) port(t *testing.T) int { return portOf(t, p.ln) }

// TestTCPStates covers PRT-03 and WP-970 AK3: ok, refused (closed port)
// and timeout (unrouted target through a fake dialer; no answer to
// expect).
func TestTCPStates(t *testing.T) {
	open := listen(t, "tcp4", "127.0.0.1:0")
	if r := Run(context.Background(), ptr(validTCPSpec("127.0.0.1", portOf(t, open))), nil); r.Status != probewire.StatusOK || r.RTTMS == nil {
		t.Fatalf("open port: %s/%s", r.Status, r.Code)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := portOf(t, ln)
	_ = ln.Close()
	if r := Run(context.Background(), ptr(validTCPSpec("127.0.0.1", closed)), nil); r.Status != probewire.StatusFail || r.Code != probewire.ReasonRefused {
		t.Fatalf("closed port: %s/%s, want fail/refused", r.Status, r.Code)
	}
	// No connect until the timeout: fake dialer for a target that is not
	// routed.
	swapExecutor(t, TypeTCP, &TCPExecutor{Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Err: ctx.Err()}
	}})
	spec := validTCPSpec("192.0.2.1", 81)
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("no connect: %s/%s, want fail/timeout", r.Status, r.Code)
	}
	swapExecutor(t, TypeTCP, &TCPExecutor{})
	// Connected, but the peer stays silent: no answer to expect until the
	// timeout.
	silent := newPeer(t, func(c net.Conn) { time.Sleep(1500 * time.Millisecond) })
	spec = tcpSpec("127.0.0.1", silent.port(t), map[string]any{"expect": "^220"})
	start := time.Now()
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("silent peer: %s/%s, want fail/timeout", r.Status, r.Code)
	}
	if d := time.Since(start); d > time.Duration(spec.TimeoutMS)*time.Millisecond+500*time.Millisecond {
		t.Fatalf("silent peer took %s", d)
	}
	// A reset family: v6 against an IPv4-only listener is refused or
	// unreachable, never ok.
	spec = tcpSpec("127.0.0.1", portOf(t, open), map[string]any{"family": "v6"})
	if r := Run(context.Background(), &spec, nil); r.Status == probewire.StatusOK {
		t.Fatal("family v6 connected to an IPv4 address")
	}
}

// TestTCPBannerMatchAndMismatch covers AK2 and AK3: send/expect, a match
// is ok, an answer without a match is fail/expect_mismatch (not degraded,
// E-177).
func TestTCPBannerMatchAndMismatch(t *testing.T) {
	smtp := newPeer(t, func(c net.Conn) {
		_, _ = io.WriteString(c, "220 mail.example.net ESMTP\r\n")
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		if strings.HasPrefix(string(buf[:n]), "QUIT\r\n") {
			_, _ = io.WriteString(c, "221 bye\r\n")
		}
	})
	// expect only: match on the banner.
	spec := tcpSpec("127.0.0.1", smtp.port(t), map[string]any{"expect": `^220 .*ESMTP`})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK || r.Code != "" || r.RTTMS == nil {
		t.Fatalf("banner match: %s/%s", r.Status, r.Code)
	}
	// send with escapes and expect on the answer.
	spec = tcpSpec("127.0.0.1", smtp.port(t), map[string]any{"send": `QUIT\r\n`, "expect": `221 bye`})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
		t.Fatalf("send/expect: %s/%s", r.Status, r.Code)
	}
	// Mismatch: the peer answers and closes, the pattern is not found.
	spec = tcpSpec("127.0.0.1", smtp.port(t), map[string]any{"send": "QUIT\r\n", "expect": `^SSH-2\.0`})
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonExpectMismatch {
		t.Fatalf("mismatch: %s/%s, want fail/expect_mismatch", r.Status, r.Code)
	}
	if r.RTTMS == nil {
		t.Fatal("mismatch without connect time")
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	// Mismatch while the peer keeps the connection open: reported before
	// the deadline of Run, not as timeout.
	open := newPeer(t, func(c net.Conn) {
		_, _ = io.WriteString(c, "SSH-2.0-OpenSSH_9.6\r\n")
		time.Sleep(2 * time.Second)
	})
	spec = tcpSpec("127.0.0.1", open.port(t), map[string]any{"expect": `^220`})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusFail || r.Code != probewire.ReasonExpectMismatch {
		t.Fatalf("open mismatch: %s/%s, want fail/expect_mismatch", r.Status, r.Code)
	}
	// An invalid RE2 expression is rejected before execution.
	spec = tcpSpec("127.0.0.1", smtp.port(t), map[string]any{"expect": `(?<=x)`})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig || r.Labels[LabelField] != "params.expect" {
		t.Fatalf("invalid expect: %s/%s %v", r.Status, r.Code, r.Labels)
	}
}

// countingConn counts the bytes read through it.
type countingConn struct {
	net.Conn
	read *atomic.Int64
}

func (c countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

// TestTCPReadCap1KiB covers AK4: the peer sends 1 MiB, at most 1 KiB is
// read; a pattern behind the cap is a mismatch.
func TestTCPReadCap1KiB(t *testing.T) {
	big := newPeer(t, func(c net.Conn) {
		_, _ = c.Write(bytes.Repeat([]byte("x"), 1<<20))
		_, _ = io.WriteString(c, "END")
	})
	var read atomic.Int64
	swapExecutor(t, TypeTCP, &TCPExecutor{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return countingConn{Conn: c, read: &read}, nil
	}})
	spec := tcpSpec("127.0.0.1", big.port(t), map[string]any{"expect": "END"})
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonExpectMismatch {
		t.Fatalf("pattern behind the cap: %s/%s, want fail/expect_mismatch", r.Status, r.Code)
	}
	if n := read.Load(); n > TCPReadCap || n == 0 {
		t.Fatalf("read %d bytes, want 1..%d", n, TCPReadCap)
	}
	// A pattern within the first KiB matches.
	read.Store(0)
	spec = tcpSpec("127.0.0.1", big.port(t), map[string]any{"expect": "^x{1000}"})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
		t.Fatalf("pattern within the cap: %s/%s", r.Status, r.Code)
	}
	if n := read.Load(); n > TCPReadCap {
		t.Fatalf("read %d bytes, want ≤ %d", n, TCPReadCap)
	}
}

// TestTCPSendsNothingWithoutSend covers AK2: without send nothing is
// sent, with or without expect; with send exactly that string.
func TestTCPSendsNothingWithoutSend(t *testing.T) {
	for _, extra := range []map[string]any{nil, {"expect": "^220"}} {
		p := newRecordingPeer(t, func(p *peer, c net.Conn) {
			if extra != nil {
				_, _ = io.WriteString(c, "220 ready\r\n")
			}
			p.record(c)
		})
		spec := tcpSpec("127.0.0.1", p.port(t), extra)
		if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
			t.Fatalf("%v: %s/%s", extra, r.Status, r.Code)
		}
		<-p.done
		if got := p.got(); len(got) != 0 {
			t.Fatalf("%v: peer received %q, want nothing", extra, got)
		}
	}
	p := newRecordingPeer(t, func(p *peer, c net.Conn) { p.record(c) })
	spec := tcpSpec("127.0.0.1", p.port(t), map[string]any{"send": `PING\r\n`})
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
		t.Fatalf("send: %s/%s", r.Status, r.Code)
	}
	<-p.done
	if got := string(p.got()); got != "PING\r\n" {
		t.Fatalf("peer received %q, want PING\\r\\n", got)
	}
}

func TestTCPRefused(t *testing.T) {
	// Take a free port and close the listener again.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, ln)
	_ = ln.Close()
	r := Run(context.Background(), ptr(validTCPSpec("127.0.0.1", port)), nil)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonRefused {
		t.Fatalf("got %s/%s, want fail/refused", r.Status, r.Code)
	}
}

func TestTCPTimeout(t *testing.T) {
	// 192.0.2.1 (TEST-NET-1) is not routed; the dial hangs until the
	// deadline. Hosts without a default route answer unreachable at once.
	spec := validTCPSpec("192.0.2.1", 81)
	start := time.Now()
	r := Run(context.Background(), &spec, nil)
	if r.Code == probewire.ReasonUnreachable {
		t.Skip("no route to TEST-NET-1 on this host")
	}
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("got %s/%s, want fail/timeout", r.Status, r.Code)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	// Directly through the executor with a fake dialer that blocks until
	// the context ends.
	e := &TCPExecutor{Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Err: ctx.Err()}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if r := e.Execute(ctx, validTCPSpec("192.0.2.1", 81), nil); r.Code != probewire.ReasonTimeout {
		t.Fatalf("fake dialer: got %s/%s, want fail/timeout", r.Status, r.Code)
	}
}

func TestTCPUnreachable(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EHOSTUNREACH, syscall.ENETUNREACH} {
		e := &TCPExecutor{Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
		}}
		r := e.Execute(context.Background(), validTCPSpec("192.0.2.1", 80), nil)
		if r.Status != probewire.StatusFail || r.Code != probewire.ReasonUnreachable {
			t.Fatalf("%v: got %s/%s, want fail/unreachable", errno, r.Status, r.Code)
		}
	}
}

func TestTCPIPv6Loopback(t *testing.T) {
	ln := listen(t, "tcp6", "[::1]:0")
	for _, target := range []string{"::1", "[::1]"} {
		r := Run(context.Background(), ptr(validTCPSpec(target, portOf(t, ln))), nil)
		if r.Status != probewire.StatusOK {
			t.Fatalf("target %s: got %s/%s, want ok", target, r.Status, r.Code)
		}
	}
}

func TestTCPMissingPortIsConfig(t *testing.T) {
	spec := validTCPSpec("127.0.0.1", 1)
	spec.Params = nil
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
		t.Fatalf("got %s/%s, want error/config", r.Status, r.Code)
	}
	if r.Labels[LabelField] != "params.port" || r.Labels[LabelMessage] == "" {
		t.Fatalf("labels = %v, want field params.port with a message", r.Labels)
	}
}

// TestTCPCancelIsNeverFail: a cancellation by the caller (shutdown) yields
// error (→ unknown), never fail (→ down). With the context cancelled the
// dial returns at once, so both select branches of Run can be ready and Go
// picks one at random; the test repeats to hit both orders, every other run
// cancelling during the dial instead of before it.
func TestTCPCancelIsNeverFail(t *testing.T) {
	swapExecutor(t, TypeTCP, &TCPExecutor{Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Err: ctx.Err()}
	}})
	for i := range 1000 {
		ctx, cancel := context.WithCancel(context.Background())
		if i%2 == 0 {
			cancel() // already shut down when the check starts
		} else {
			go cancel() // the shutdown arrives during the dial
		}
		r := Run(ctx, ptr(validTCPSpec("192.0.2.1", 81)), nil)
		cancel()
		if r.Status != probewire.StatusError || r.Code != probewire.ReasonTimeout {
			t.Fatalf("run %d: got %s/%s, want error/timeout", i, r.Status, r.Code)
		}
	}
}
