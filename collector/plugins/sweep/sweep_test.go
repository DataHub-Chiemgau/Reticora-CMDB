package sweep

import (
	"context"
	"errors"
	"net"
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
	if len(p.TCPPorts) != len(commonPorts) {
		t.Errorf("TCPPorts = %v, want %v", p.TCPPorts, commonPorts)
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
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
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
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for closed port, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Discover(context.Background(), nil, nil)
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
	results, err := p.Discover(ctx, []string{"192.0.2.1"}, nil)
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
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
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
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
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
