package sweep

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// ICMP echo (DIS-01). Capability: the collector first opens an
// unprivileged ICMP datagram socket (Linux, allowed for the group in
// net.ipv4.ping_group_range – Docker sets the full range in containers
// with their own network namespace; with host networking the host's value
// applies); otherwise a raw socket, which needs CAP_NET_RAW (systemd unit
// of install-vm.sh: AmbientCapabilities=CAP_NET_RAW). The image sets no
// file capability and runs as nobody, so there only the unprivileged
// socket works, or NET_RAW as root in the container (collector/Dockerfile).
// Without either, ICMP is off and the sweep relies on TCP and ARP.

// Pinger sends one ICMP echo request and waits for the reply.
type Pinger interface {
	Ping(ctx context.Context, ip string, timeout time.Duration) (time.Duration, error)
}

// ErrNoICMP reports that the process may open no ICMP socket.
var ErrNoICMP = errors.New("sweep: no ICMP socket available (needs net.ipv4.ping_group_range or CAP_NET_RAW)")

// ICMPPinger pings over the system's ICMP sockets.
type ICMPPinger struct {
	seq atomic.Uint32
}

// ICMPCapability reports which ICMP socket the process can open:
// "unprivileged", "raw" or "" (none).
func ICMPCapability() string {
	if c, err := icmp.ListenPacket("udp4", "0.0.0.0"); err == nil {
		c.Close()
		return "unprivileged"
	}
	if c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0"); err == nil {
		c.Close()
		return "raw"
	}
	return ""
}

func listenICMP() (conn *icmp.PacketConn, unprivileged bool, err error) {
	if conn, err = icmp.ListenPacket("udp4", "0.0.0.0"); err == nil {
		return conn, true, nil
	}
	if conn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0"); err == nil {
		return conn, false, nil
	}
	return nil, false, ErrNoICMP
}

// Ping implements Pinger for IPv4 targets.
func (p *ICMPPinger) Ping(ctx context.Context, ip string, timeout time.Duration) (time.Duration, error) {
	dst := net.ParseIP(ip).To4()
	if dst == nil {
		return 0, fmt.Errorf("sweep: ICMP needs an IPv4 address, got %q", ip)
	}
	conn, unprivileged, err := listenICMP()
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	id := os.Getpid() & 0xffff
	seq := int(p.seq.Add(1) & 0xffff)
	raw, err := echoRequest(id, seq)
	if err != nil {
		return 0, err
	}
	var addr net.Addr = &net.IPAddr{IP: dst}
	if unprivileged {
		addr = &net.UDPAddr{IP: dst}
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if derr := conn.SetDeadline(deadline); derr != nil {
		return 0, derr
	}
	start := time.Now()
	if _, err = conn.WriteTo(raw, addr); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, peer, rerr := conn.ReadFrom(buf)
		if rerr != nil {
			return 0, rerr
		}
		if !peerIs(peer, dst) {
			continue
		}
		// The kernel rewrites the identifier of unprivileged sockets and
		// delivers only their own replies.
		if isEchoReply(buf[:n], id, seq, !unprivileged) {
			return time.Since(start), nil
		}
	}
}

func echoRequest(id, seq int) ([]byte, error) {
	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("reticora-sweep")}}
	return msg.Marshal(nil)
}

// isEchoReply reports whether raw is the echo reply for seq (and id when
// checkID is set).
func isEchoReply(raw []byte, id, seq int, checkID bool) bool {
	msg, err := icmp.ParseMessage(1, raw) // protocol 1 = ICMPv4
	if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
		return false
	}
	echo, ok := msg.Body.(*icmp.Echo)
	return ok && echo.Seq == seq && (!checkID || echo.ID == id)
}

func peerIs(peer net.Addr, ip net.IP) bool {
	switch a := peer.(type) {
	case *net.IPAddr:
		return a.IP.Equal(ip)
	case *net.UDPAddr:
		return a.IP.Equal(ip)
	}
	return false
}
