package sweep

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyByPort(t *testing.T) {
	cases := []struct {
		port int
		want string
	}{
		{22, "server"},
		{80, "network.endpoint"},
		{443, "network.endpoint"},
		{8080, "network.endpoint"},
		{8443, "network.endpoint"},
		{161, "network.device"},
		{3389, "windows-server"},
		{5985, "windows-server"},
		{623, "bmc"},
		{25, "network.endpoint"},
		{0, "network.endpoint"},
	}
	for _, c := range cases {
		if got := classifyByPort(c.port); got != c.want {
			t.Errorf("classifyByPort(%d) = %q, want %q", c.port, got, c.want)
		}
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
	if fmt.Sprint(p.TCPPorts) != "[22 80 443 623 5985 8006]" {
		t.Errorf("TCPPorts = %v, want exactly the DIS-01 ports 22/80/443/623/5985/8006", p.TCPPorts)
	}
	if p.Concurrency != MaxConcurrency || MaxConcurrency != 256 {
		t.Errorf("Concurrency = %d, MaxConcurrency = %d; want 256", p.Concurrency, MaxConcurrency)
	}
	if p.Pinger == nil || p.Neighbors == nil {
		t.Error("New must enable ICMP and ARP")
	}
	if p.Name() != "sweep" {
		t.Errorf("Name() = %q, want sweep", p.Name())
	}
}

func TestDiscoverFindsReachableTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	p := &Plugin{Timeout: time.Second, Concurrency: 4, TCPPorts: []int{port}}
	results, err := p.Probe(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Attributes["reachable"] != true {
		t.Errorf("reachable = %v, want true", r.Attributes["reachable"])
	}
	if r.Attributes["openPort"] != port {
		t.Errorf("openPort = %v, want %d", r.Attributes["openPort"], port)
	}
	if r.Name == "" {
		t.Error("expected Name to fall back to target when reverse DNS fails")
	}
	if r.CIType == "" {
		t.Error("expected CIType to be classified from open port")
	}
}

func TestDiscoverSkipsUnreachableTarget(t *testing.T) {
	// Grab a free port and close it so dials are refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	p := &Plugin{Timeout: 300 * time.Millisecond, Concurrency: 2, TCPPorts: []int{port}}
	results, err := p.Probe(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for closed port, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Probe(context.Background(), nil, nil)
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
	p := &Plugin{Timeout: 100 * time.Millisecond, TCPPorts: []int{1}}
	results, err := p.Probe(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCollectReportsOpenPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	// closed port that will refuse connections
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := closedLn.Addr().(*net.TCPAddr).Port
	closedLn.Close()

	p := &Plugin{Timeout: 300 * time.Millisecond, TCPPorts: []int{closedPort, port}}
	res, err := p.collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	openPorts, ok := res.Attributes["openPorts"].([]int)
	if !ok {
		t.Fatalf("openPorts attribute has unexpected type %T", res.Attributes["openPorts"])
	}
	if len(openPorts) != 1 || openPorts[0] != port {
		t.Errorf("openPorts = %v, want [%d]", openPorts, port)
	}
	if res.Attributes["reachable"] != true {
		t.Errorf("reachable = %v, want true", res.Attributes["reachable"])
	}
	if res.Attributes["collectedBy"] != "sweep" {
		t.Errorf("collectedBy = %v, want sweep", res.Attributes["collectedBy"])
	}
}

func TestCollectNoOpenPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	p := &Plugin{Timeout: 300 * time.Millisecond, TCPPorts: []int{port}}
	res, err := p.collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Attributes["reachable"] != false {
		t.Errorf("reachable = %v, want false", res.Attributes["reachable"])
	}
	if res.CIType != "network.endpoint" {
		t.Errorf("CIType = %q, want network.endpoint fallback", res.CIType)
	}
}

// fakePinger answers for the listed hosts and records the parallel probes.
type fakePinger struct {
	alive    map[string]bool
	err      error
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakePinger) Ping(_ context.Context, ip string, _ time.Duration) (time.Duration, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	if f.err != nil {
		return 0, f.err
	}
	if f.alive[ip] {
		return 3 * time.Millisecond, nil
	}
	return 0, errors.New("timeout")
}

// closedPort returns a TCP port on 127.0.0.1 that refuses connections.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestDiscoverCapsParallelProbesAt256(t *testing.T) {
	targets := make([]string, 600)
	for i := range targets {
		targets[i] = fmt.Sprintf("127.0.%d.%d", i/250, i%250+1)
	}
	pinger := &fakePinger{alive: map[string]bool{}}
	p := &Plugin{Timeout: 50 * time.Millisecond, Concurrency: 10000, TCPPorts: []int{closedPort(t)}, Pinger: pinger}
	if _, err := p.Probe(context.Background(), targets, nil); err != nil {
		t.Fatal(err)
	}
	if peak := pinger.peak.Load(); peak > MaxConcurrency || peak < 2 {
		t.Fatalf("peak parallel probes = %d, want 2..%d", peak, MaxConcurrency)
	}
	if got := (&Plugin{Concurrency: 1000}).effectiveConcurrency(); got != 256 {
		t.Errorf("effective concurrency = %d, want 256", got)
	}
	if got := (&Plugin{Concurrency: 16}).effectiveConcurrency(); got != 16 {
		t.Errorf("effective concurrency = %d, want 16", got)
	}
}

func TestDiscoverFindsHostsByICMPAndARP(t *testing.T) {
	pinger := &fakePinger{alive: map[string]bool{"127.0.0.1": true}}
	neighbors := func() (map[string]string, error) {
		// 127.0.0.2 answers only ARP; 127.0.0.9 is not a target.
		return map[string]string{"127.0.0.1": "aa:bb:cc:00:00:01", "127.0.0.2": "aa:bb:cc:00:00:02", "127.0.0.9": "aa:bb:cc:00:00:09"}, nil
	}
	p := &Plugin{Timeout: 100 * time.Millisecond, TCPPorts: []int{closedPort(t)}, Pinger: pinger, Neighbors: neighbors}
	results, err := p.Probe(context.Background(), []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byIP := map[string]string{}
	for _, r := range results {
		byIP[r.IP] = fmt.Sprint(r.Attributes["discoveryMethod"], " ", r.MAC)
	}
	want := map[string]string{"127.0.0.1": "icmp aa:bb:cc:00:00:01", "127.0.0.2": "arp aa:bb:cc:00:00:02"}
	if fmt.Sprint(byIP) != fmt.Sprint(want) {
		t.Fatalf("results = %v, want %v", byIP, want)
	}
}

func TestDiscoverWithoutICMPCapabilityFallsBackToTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if c, aerr := ln.Accept(); aerr == nil {
			c.Close()
		}
	}()
	p := &Plugin{Timeout: time.Second, TCPPorts: []int{ln.Addr().(*net.TCPAddr).Port}, Pinger: &fakePinger{err: ErrNoICMP}}
	results, err := p.Probe(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil || len(results) != 1 || results[0].Attributes["discoveryMethod"] != "tcp" {
		t.Fatalf("results = %+v, %v", results, err)
	}
	wg.Wait()
}

func TestParseARPTable(t *testing.T) {
	table := `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         AA:BB:CC:DD:EE:FF     *        eth0
192.168.1.7      0x1         0x0         00:00:00:00:00:00     *        eth0
192.168.1.9      0x1         0x6         aa:bb:cc:dd:ee:09     *        eth0
`
	got, err := parseARPTable(strings.NewReader(table))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"192.168.1.1": "aa:bb:cc:dd:ee:ff", "192.168.1.9": "aa:bb:cc:dd:ee:09"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseARPTable = %v, want %v", got, want)
	}
}

func TestEchoRequestAndReply(t *testing.T) {
	req, err := echoRequest(0x1234, 7)
	if err != nil {
		t.Fatal(err)
	}
	if req[0] != 8 || req[1] != 0 {
		t.Fatalf("echo request type/code = %d/%d, want 8/0", req[0], req[1])
	}
	reply := append([]byte{}, req...)
	reply[0] = 0 // echo reply; checksum is not verified on parse
	if !isEchoReply(reply, 0x1234, 7, true) {
		t.Error("matching reply not recognised")
	}
	if isEchoReply(reply, 0x1234, 8, true) {
		t.Error("reply with another sequence accepted")
	}
	if isEchoReply(reply, 0x9999, 7, true) || !isEchoReply(reply, 0x9999, 7, false) {
		t.Error("identifier check wrong")
	}
	if isEchoReply(req, 0x1234, 7, true) {
		t.Error("an echo request is not a reply")
	}
}

// TestICMPPingerUsesTheAvailableSocket pings loopback when the process may
// open an ICMP socket and otherwise reports the missing capability.
func TestICMPPingerUsesTheAvailableSocket(t *testing.T) {
	p := &ICMPPinger{}
	_, err := p.Ping(context.Background(), "127.0.0.1", 2*time.Second)
	if ICMPCapability() == "" {
		if !errors.Is(err, ErrNoICMP) {
			t.Fatalf("without capability Ping = %v, want ErrNoICMP", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Ping 127.0.0.1 with %s socket: %v", ICMPCapability(), err)
	}
	if _, err = p.Ping(context.Background(), "::1", time.Second); err == nil {
		t.Error("IPv6 targets are not pinged over ICMPv4")
	}
}
