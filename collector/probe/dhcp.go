package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/dhcpv4"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeDHCP is the register key of the DHCPv4 probe.
const TypeDHCP = "dhcp"

// Modes of the dhcp probe (WP-1012 AK1).
const (
	DHCPModeInform   = "inform"
	DHCPModeDiscover = "discover"
	DHCPModeRelay    = "relay"
)

// Value and label names of a dhcp result (WP-1012 AK5): the response time
// carries the offered address (yiaddr of an OFFER) and the server
// identifier (option 54) as labels in its key (probewire.ValueKey).
const (
	ValueDHCPResponseSeconds = "probe_dhcp_response_seconds"
	LabelDHCPOfferedIP       = "offered_ip"
	LabelDHCPServerID        = "server_id"
)

func init() { Register(TypeDHCP, &DHCPExecutor{}) }

// DHCPExecutor is the probe type dhcp (PRT-13, WP-1012, E-190): a DHCPv4
// check that consumes no lease. It never sends DHCPREQUEST or
// DHCPRELEASE:
//
//   - inform: DHCPINFORM by unicast to the server, ciaddr the collector's
//     address towards the server, source port 68; the answer is the ACK;
//   - discover: DHCPDISCOVER by broadcast on interface, source port 68,
//     the first OFFER counts (server, when set, is the expected server
//     identifier; offers of other servers are discarded);
//   - relay: DHCPDISCOVER by unicast to the server with giaddr the
//     collector's address, source and receive port 67, as a relay agent
//     for a remote segment.
//
// At most one transaction runs per port binding and interface; answers
// are matched by xid and chaddr, foreign answers are discarded. A missing
// port capability (WP-966) or a failing bind is error/unsupported
// (unknown, never down); no answer by the deadline is fail/timeout; an
// offered address outside expect_subnet is ok with the warn code
// subnet_mismatch.
type DHCPExecutor struct {
	// ServerPort, ClientPort and RelayPort replace 67, 68 and 67 in tests
	// (high ports); 0 keeps the standard port.
	ServerPort int
	ClientPort int
	RelayPort  int
	// Capabilities returns the capability report (nil: CurrentReport).
	Capabilities func() *probewire.ProbeCapabilities
}

type dhcpParams struct {
	Server       string `json:"server"`
	Mode         string `json:"mode"`
	Interface    string `json:"interface"`
	ClientID     string `json:"client_id"`
	MAC          string `json:"mac"`
	ExpectSubnet string `json:"expect_subnet"`
}

// Errors of a transaction.
var (
	errDHCPBind = errors.New("dhcp: port cannot be bound")
	errDHCPNak  = errors.New("dhcp: the server answered with DHCPNAK")
)

// dhcpBindings holds one slot per port binding and interface (AK3).
var dhcpBindings sync.Map // key → chan struct{} (capacity 1)

func dhcpSlot(port int, iface string) chan struct{} {
	ch, _ := dhcpBindings.LoadOrStore(fmt.Sprintf("%d/%s", port, iface), make(chan struct{}, 1))
	return ch.(chan struct{})
}

// dhcpPortHolders counts the sockets of this process that hold a DHCP port
// (port → *atomic.Int32). The capability detection binds the same ports; a
// port taken by a running dhcp check proves the capability instead of
// denying it.
var dhcpPortHolders sync.Map

func dhcpHolders(port int) *atomic.Int32 {
	v, _ := dhcpPortHolders.LoadOrStore(port, new(atomic.Int32))
	return v.(*atomic.Int32)
}

// dhcpPortHeld reports whether a dhcp check of this process holds the port.
func dhcpPortHeld(port int) bool { return dhcpHolders(port).Load() > 0 }

// dhcpBindRetries bounds the retries of a bind that collides with the
// short bind of the capability detection.
const dhcpBindRetries = 3

// Execute implements Executor.
func (e *DHCPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p := dhcpParams{Mode: DHCPModeInform}
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	if p.Mode == "" {
		p.Mode = DHCPModeInform
	}
	var prefix netip.Prefix
	if p.ExpectSubnet != "" {
		var err error
		if prefix, err = netip.ParsePrefix(strings.TrimSpace(p.ExpectSubnet)); err != nil || !prefix.Addr().Is4() {
			return configResult("params.expect_subnet", "must be an IPv4 prefix")
		}
		prefix = prefix.Masked()
	}
	mac, err := dhcpMAC(p.MAC, spec.ID)
	if err != nil {
		return configResult("params.mac", "must be a MAC address of six octets")
	}

	ports := e.ports()
	localPort := ports.client
	switch p.Mode {
	case DHCPModeInform, DHCPModeDiscover:
	case DHCPModeRelay:
		localPort = ports.relay
	default:
		return configResult("params.mode", "unknown mode")
	}
	if p.Mode == DHCPModeDiscover && strings.TrimSpace(p.Interface) == "" {
		return configResult("params.interface", "is required in mode discover")
	}
	if !e.portCapable(p.Mode) {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}

	// The server: the parameter server, else the target of the probe.
	// In mode discover it is optional and names the expected server id.
	serverText := strings.TrimSpace(p.Server)
	if serverText == "" && p.Mode != DHCPModeDiscover {
		serverText = strings.TrimSpace(spec.Target)
	}
	var server netip.Addr
	if serverText != "" {
		server, err = dhcpResolve(ctx, serverText)
		if err != nil {
			if ctx.Err() != nil {
				return failResult(probewire.StatusFail, probewire.ReasonTimeout)
			}
			return configResult("params.server", "must be an IPv4 address or a resolvable name")
		}
	}

	iface := ""
	if p.Mode == DHCPModeDiscover {
		iface = strings.TrimSpace(p.Interface)
		if _, ierr := net.InterfaceByName(iface); ierr != nil {
			return configResult("params.interface", "no such interface")
		}
	}

	// One transaction per binding (AK3): wait for the slot until the
	// deadline.
	slot := dhcpSlot(localPort, iface)
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return failResult(probewire.StatusError, probewire.ReasonLimitExceeded)
	}
	defer func() { <-slot }()

	conn, err := dhcpListen(ctx, localPort, iface)
	// The capability detection binds the port for a moment; a collision
	// with it is retried instead of reported as unsupported.
	for i := 0; err != nil && errors.Is(err, syscall.EADDRINUSE) && i < dhcpBindRetries && ctx.Err() == nil; i++ {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
		}
		conn, err = dhcpListen(ctx, localPort, iface)
	}
	if err != nil {
		if ctx.Err() != nil {
			return failResult(probewire.StatusFail, probewire.ReasonTimeout)
		}
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	holders := dhcpHolders(localPort)
	holders.Add(1)
	defer func() { _ = conn.Close(); holders.Add(-1) }()

	xid, err := dhcpXID()
	if err != nil {
		return failResult(probewire.StatusError, probewire.ReasonConfig)
	}
	tx := dhcpTransaction{mode: p.Mode, xid: xid, mac: mac, clientID: p.ClientID, server: server}
	var dst netip.AddrPort
	if p.Mode == DHCPModeDiscover {
		dst = netip.AddrPortFrom(netip.AddrFrom4([4]byte{255, 255, 255, 255}), uint16(ports.server)) //nolint:gosec // a port
	} else {
		dst = netip.AddrPortFrom(server, uint16(ports.server)) //nolint:gosec // a port
		local, lerr := dhcpLocalAddr(dst)
		if lerr != nil {
			return failResult(probewire.StatusFail, dhcpReason(ctx, lerr))
		}
		tx.local = local
	}
	msg, err := tx.message()
	if err != nil {
		return failResult(probewire.StatusError, probewire.ReasonConfig)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	start := time.Now()
	if _, werr := conn.WriteTo(msg, net.UDPAddrFromAddrPort(dst)); werr != nil {
		return failResult(probewire.StatusFail, dhcpReason(ctx, werr))
	}
	reply, err := tx.await(conn)
	elapsed := time.Since(start)
	if err != nil {
		return failResult(probewire.StatusFail, dhcpReason(ctx, err))
	}
	return dhcpEvaluate(reply, prefix, elapsed)
}

// dhcpPorts are the effective ports of an executor.
type dhcpPorts struct{ server, client, relay int }

func (e *DHCPExecutor) ports() dhcpPorts {
	p := dhcpPorts{server: dhcpv4.ServerPort, client: dhcpv4.ClientPort, relay: dhcpv4.ServerPort}
	if e.ServerPort > 0 {
		p.server = e.ServerPort
	}
	if e.ClientPort > 0 {
		p.client = e.ClientPort
	}
	if e.RelayPort > 0 {
		p.relay = e.RelayPort
	}
	return p
}

// portCapable checks the port capability of the report (AK4): inform and
// discover bind port 68, relay port 67. Without a report (before the first
// detection) the bind decides.
func (e *DHCPExecutor) portCapable(mode string) bool {
	report := e.Capabilities
	if report == nil {
		report = CurrentReport
	}
	r := report()
	if r == nil {
		return true
	}
	if mode == DHCPModeRelay {
		return r.UDP67
	}
	return r.UDP68
}

// dhcpEvaluate turns the matching answer into the result (AK4, AK5).
func dhcpEvaluate(reply *dhcpv4.Message, prefix netip.Prefix, elapsed time.Duration) probewire.Result {
	labels := map[string]string{}
	offered := reply.YIAddr
	if offered.IsValid() && !offered.IsUnspecified() {
		labels[LabelDHCPOfferedIP] = offered.String()
	}
	if id, ok := reply.ServerID(); ok {
		labels[LabelDHCPServerID] = id.String()
	}
	rtt := float64(elapsed.Microseconds()) / 1000
	r := probewire.Result{
		Status: probewire.StatusOK,
		RTTMS:  &rtt,
		Values: map[string]float64{probewire.ValueKey(ValueDHCPResponseSeconds, labels): elapsed.Seconds()},
	}
	if prefix.IsValid() && labels[LabelDHCPOfferedIP] != "" && !prefix.Contains(offered.Unmap()) {
		r.Code = probewire.WarnSubnetMismatch
	}
	return r
}

// dhcpTransaction is one exchange: the request and the matching rules of
// its answer.
type dhcpTransaction struct {
	mode     string
	xid      uint32
	mac      []byte
	clientID string
	server   netip.Addr
	local    netip.Addr
}

// dhcpParamRequest asks for subnet mask, router, DNS servers and lease
// time (option 55).
var dhcpParamRequest = []uint8{dhcpv4.OptSubnetMask, dhcpv4.OptRouter, dhcpv4.OptDNS, dhcpv4.OptLeaseTime}

// message encodes the request of the mode. Only DISCOVER and INFORM are
// ever built (AK1).
func (tx *dhcpTransaction) message() ([]byte, error) {
	var m *dhcpv4.Message
	switch tx.mode {
	case DHCPModeInform:
		m = dhcpv4.NewRequest(dhcpv4.Inform, tx.xid, tx.mac)
		m.CIAddr = tx.local
	case DHCPModeDiscover:
		m = dhcpv4.NewRequest(dhcpv4.Discover, tx.xid, tx.mac)
		// Without an address the client asks for a broadcast answer
		// (RFC 2131 4.1).
		m.Flags = dhcpv4.FlagBroadcast
	case DHCPModeRelay:
		m = dhcpv4.NewRequest(dhcpv4.Discover, tx.xid, tx.mac)
		m.GIAddr = tx.local
		m.Hops = 1
	default:
		return nil, fmt.Errorf("dhcp: unknown mode %q", tx.mode)
	}
	m.SetOption(dhcpv4.ParamRequestOption(dhcpParamRequest...))
	if tx.clientID != "" {
		// Client identifier of type 0 (RFC 2132 9.14): a text, not a
		// hardware address.
		m.SetOption(dhcpv4.Option{Code: dhcpv4.OptClientID, Data: append([]byte{0}, tx.clientID...)})
	} else {
		m.SetOption(dhcpv4.ClientIDOption(dhcpv4.HTypeEthernet, tx.mac))
	}
	return m.Encode()
}

// matches reports whether m answers the transaction: a BOOTREPLY with the
// same xid and chaddr (AK3); in mode discover with a server set, its
// server identifier.
func (tx *dhcpTransaction) matches(m *dhcpv4.Message) bool {
	if m.Op != dhcpv4.OpReply || m.XID != tx.xid || !bytes.Equal(m.HardwareAddr(), tx.mac) {
		return false
	}
	if tx.mode == DHCPModeDiscover && tx.server.IsValid() {
		if id, ok := m.ServerID(); !ok || id != tx.server {
			return false
		}
	}
	return true
}

// await reads until the answer of the transaction arrives or the
// deadline: ACK for inform, OFFER otherwise; a NAK ends the transaction,
// every other message is discarded.
func (tx *dhcpTransaction) await(conn net.PacketConn) (*dhcpv4.Message, error) {
	want := dhcpv4.Offer
	if tx.mode == DHCPModeInform {
		want = dhcpv4.Ack
	}
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, err
		}
		m, err := dhcpv4.Decode(buf[:n])
		if err != nil || !tx.matches(m) {
			continue // foreign or malformed: discard and wait until the deadline
		}
		switch m.MessageType() {
		case want:
			return m, nil
		case dhcpv4.Nak:
			return nil, errDHCPNak
		}
	}
}

// dhcpMAC parses the mac parameter or derives a locally administered
// unicast MAC address from the probe ID, stable across checks (AK2).
func dhcpMAC(text, probeID string) ([]byte, error) {
	if text = strings.TrimSpace(text); text != "" {
		hw, err := net.ParseMAC(text)
		if err != nil || len(hw) != 6 {
			return nil, errors.New("dhcp: invalid mac")
		}
		return hw, nil
	}
	sum := sha256.Sum256([]byte("reticora-dhcp-probe/" + probeID))
	mac := append([]byte(nil), sum[:6]...)
	mac[0] = mac[0]&^0x01 | 0x02 // unicast, locally administered
	return mac, nil
}

// dhcpXID returns a random transaction ID.
func dhcpXID() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

// dhcpResolve returns the IPv4 address of a literal or a name.
func dhcpResolve(ctx context.Context, text string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(text); err == nil {
		if a = a.Unmap(); a.Is4() {
			return a, nil
		}
		return netip.Addr{}, errors.New("dhcp: not an IPv4 address")
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", text)
	if err != nil || len(addrs) == 0 {
		return netip.Addr{}, errors.New("dhcp: name does not resolve")
	}
	return addrs[0].Unmap(), nil
}

// dhcpLocalAddr is the collector's address towards dst (ciaddr of
// inform, giaddr of relay): the source address the routing table picks; a
// connected UDP socket sends nothing.
func dhcpLocalAddr(dst netip.AddrPort) (netip.Addr, error) {
	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() { _ = c.Close() }()
	a := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if !a.Is4() {
		return netip.Addr{}, errors.New("dhcp: no IPv4 source address")
	}
	return a, nil
}

// dhcpListen binds the UDP port on every IPv4 address; with an interface
// the socket is bound to it and may send broadcasts (mode discover).
func dhcpListen(ctx context.Context, port int, iface string) (net.PacketConn, error) {
	lc := net.ListenConfig{}
	if iface != "" {
		lc.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) { serr = dhcpBindDevice(fd, iface) }); err != nil {
				return err
			}
			return serr
		}
	}
	conn, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDHCPBind, err)
	}
	return conn, nil
}

// dhcpReason maps an exchange error to a reason code.
func dhcpReason(ctx context.Context, err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, errDHCPNak), errors.Is(err, syscall.ECONNREFUSED):
		return probewire.ReasonRefused
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return probewire.ReasonTimeout
	case errors.As(err, &ne) && ne.Timeout():
		return probewire.ReasonTimeout
	}
	return probewire.ReasonUnreachable
}
