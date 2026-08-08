package ssh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClassifyFromBanner(t *testing.T) {
	cases := []struct {
		banner string
		want   string
	}{
		{"SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.6", "server"},
		{"SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u2", "server"},
		{"SSH-2.0-OpenSSH_7.4", "server"},
		{"SSH-2.0-Cisco-1.25", "network.device"},
		{"SSH-2.0-OpenSSH_6.6 Junos", "network.device"},
		{"SSH-2.0-juniper SSH server", "network.device"},
		{"SSH-2.0-ROSSSH MikroTik", "network.device"},
		{"SSH-2.0-FortiSSH FortiOS v7", "network.device"},
		{"SSH-2.0-OpenSSH FortiGate-VM64", "network.device"},
		{"SSH-2.0-Arista Networks", "network.device"},
		{"SSH-2.0-dropbear_2019.78", "server"},
		{"", "server"},
	}
	for _, c := range cases {
		if got := classifyFromBanner(c.banner); got != c.want {
			t.Errorf("classifyFromBanner(%q) = %q, want %q", c.banner, got, c.want)
		}
	}
}

func TestParseOSFromBanner(t *testing.T) {
	cases := []struct {
		banner string
		want   string
	}{
		{"SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.6", "linux/ubuntu"},
		{"SSH-2.0-OpenSSH_9.2p1 Debian-2", "linux/debian"},
		{"SSH-2.0-Cisco-1.25", "cisco/ios"},
		{"SSH-2.0-OpenSSH_6.6 Junos", "juniper/junos"},
		{"SSH-2.0-OpenSSH_7.4 FreeBSD-2020", "freebsd"},
		{"SSH-2.0-OpenSSH_7.4", "linux"},
		{"SSH-2.0-Mikrotik", "linux"},
		{"", "linux"},
	}
	for _, c := range cases {
		if got := parseOSFromBanner(c.banner); got != c.want {
			t.Errorf("parseOSFromBanner(%q) = %q, want %q", c.banner, got, c.want)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("New().Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Name() != "ssh" {
		t.Errorf("Name() = %q, want %q", p.Name(), "ssh")
	}
}

// startBannerListener starts a TCP listener on port 22 and sends the given
// banner (or nothing when sendBanner is false) to every accepted connection.
func startBannerListener(t *testing.T, banner string, sendBanner bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:22")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:22: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if sendBanner {
					fmt.Fprintf(c, "%s\r\n", banner)
				} else {
					time.Sleep(300 * time.Millisecond)
				}
			}(conn)
		}
	}()
}

func TestDiscoverReadsBannerAndClassifies(t *testing.T) {
	banner := "SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.6"
	startBannerListener(t, banner, true)

	p := &Plugin{Timeout: 2 * time.Second}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "server" {
		t.Errorf("CIType = %q, want server", r.CIType)
	}
	if r.Attributes["sshBanner"] != banner {
		t.Errorf("sshBanner = %v, want %q", r.Attributes["sshBanner"], banner)
	}
	if r.Attributes["transport"] != "ssh" {
		t.Errorf("transport = %v, want ssh", r.Attributes["transport"])
	}
	if r.Attributes["sshPort"] != defaultPort {
		t.Errorf("sshPort = %v, want %d", r.Attributes["sshPort"], defaultPort)
	}
}

func TestDiscoverSkipsEmptyBanner(t *testing.T) {
	startBannerListener(t, "", false)

	p := &Plugin{Timeout: 200 * time.Millisecond}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for empty banner, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := &Plugin{Timeout: time.Second}
	results, err := p.Discover(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", results)
	}
}

func TestDiscoverCancelledContextReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Plugin{Timeout: time.Second}
	results, err := p.Discover(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCollectUsesBannerForFingerprint(t *testing.T) {
	banner := "SSH-2.0-Cisco-1.25"
	startBannerListener(t, banner, true)

	p := &Plugin{Timeout: 2 * time.Second}
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.CIType != "network.device" {
		t.Errorf("CIType = %q, want network.device", res.CIType)
	}
	if res.Firmware != banner {
		t.Errorf("Firmware = %q, want banner %q", res.Firmware, banner)
	}
	if res.Attributes["osFamily"] != "cisco/ios" {
		t.Errorf("osFamily = %v, want cisco/ios", res.Attributes["osFamily"])
	}
	cmds, ok := res.Attributes["commandSet"].(map[string]string)
	if !ok || len(cmds) == 0 {
		t.Errorf("expected non-empty commandSet attribute, got %v", res.Attributes["commandSet"])
	}
	if _, ok := cmds["serial"]; !ok {
		t.Errorf("commandSet missing serial command: %v", cmds)
	}
}

func TestCollectUnreachableTarget(t *testing.T) {
	// Reserve a port and close it so connections are refused.
	ln, err := net.Listen("tcp", "127.0.0.1:22")
	if err == nil {
		ln.Close()
	}
	p := &Plugin{Timeout: 200 * time.Millisecond}
	_, err = p.Collect(context.Background(), "192.0.2.1", nil)
	if err == nil || !strings.Contains(err.Error(), "ssh: connect") {
		t.Errorf("expected connect error, got %v", err)
	}
}

func TestDiscoverReadsOnlyFirstBannerLine(t *testing.T) {
	// Server sends multiple lines; scanner must only read the first.
	ln, err := net.Listen("tcp", "127.0.0.1:22")
	if err != nil {
		t.Skipf("cannot bind port 22: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				w := bufio.NewWriter(c)
				fmt.Fprintf(w, "SSH-2.0-OpenSSH_9.6\r\nsecond-line\r\n")
				w.Flush()
				io.Copy(io.Discard, c) // keep open until client closes
			}(conn)
		}
	}()

	p := &Plugin{Timeout: 2 * time.Second}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if got := results[0].Attributes["sshBanner"]; got != "SSH-2.0-OpenSSH_9.6" {
		t.Errorf("sshBanner = %v, want first line only", got)
	}
}
