package probe

import (
	"context"
	"net"
	"os"
	"strconv"
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
	// A target that already carries the port works without params.port.
	spec := validTCPSpec(ln.Addr().String(), 1)
	spec.Params = nil
	if r := Run(context.Background(), &spec, nil); r.Status != probewire.StatusOK {
		t.Fatalf("target with port: got %s/%s", r.Status, r.Code)
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
