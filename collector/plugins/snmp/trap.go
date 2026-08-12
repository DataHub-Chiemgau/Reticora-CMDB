package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// TrapEvent is a normalized SNMP trap/notification ready for upload to the
// monitoring ingest. Traps are mapped onto metrics so they flow through the
// existing alerting rules (spec §9: Trap/Event-Verarbeitung).
type TrapEvent struct {
	SourceIP  string
	Community string
	// TrapOID is the snmpTrapOID.0 value when present, else the enterprise OID.
	TrapOID   string
	Variables map[string]string
	Received  time.Time
}

// TrapReceiver listens for SNMPv1/v2c traps on a UDP socket, parses them with
// a tolerant BER decoder and hands each event to the configured sink. Traps
// whose community does not match the configured one are dropped silently —
// accepting every datagram would let any network neighbor inject events.
type TrapReceiver struct {
	// Address to listen on, e.g. ":162". Port 162 requires root/CAP_NET_BIND_SERVICE.
	ListenAddr string
	// Community the trap must carry; empty accepts every community.
	Community string
	// Sink receives each parsed event. It must be safe for concurrent use.
	Sink func(ctx context.Context, ev TrapEvent)

	conn     atomic.Pointer[net.PacketConn]
	inFlight atomic.Int64
}

// maxTrapInFlight bounds the concurrently processed datagrams so a trap storm
// cannot exhaust goroutines.
const maxTrapInFlight = 64

// Run starts the receive loop and blocks until the context is cancelled.
// Returns nil on clean shutdown.
func (r *TrapReceiver) Run(ctx context.Context) error {
	if r.Sink == nil {
		return fmt.Errorf("snmp trap receiver requires a sink")
	}
	addr := r.ListenAddr
	if addr == "" {
		addr = ":162"
	}
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("snmp trap listen %s: %w", addr, err)
	}
	r.conn.Store(&conn)
	slog.Info("snmp trap receiver listening", "addr", conn.LocalAddr().String())

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("snmp trap read: %w", err)
		}
		if r.inFlight.Load() >= maxTrapInFlight {
			slog.Warn("snmp trap dropped: too many in flight", "source", src.String())
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		r.inFlight.Add(1)
		go func() {
			defer r.inFlight.Add(-1)
			r.handle(ctx, pkt, src)
		}()
	}
}

// Close stops the receiver.
func (r *TrapReceiver) Close() error {
	if conn := r.conn.Load(); conn != nil {
		return (*conn).Close()
	}
	return nil
}

// LocalAddr returns the bound listen address, or nil before Run has bound the
// socket. Safe for concurrent use with Run.
func (r *TrapReceiver) LocalAddr() net.Addr {
	if conn := r.conn.Load(); conn != nil {
		return (*conn).LocalAddr()
	}
	return nil
}

func (r *TrapReceiver) handle(ctx context.Context, pkt []byte, src net.Addr) {
	srcIP := src.String()
	if addr, ok := src.(*net.UDPAddr); ok {
		srcIP = addr.IP.String()
	}
	ev, err := parseTrap(pkt)
	if err != nil {
		slog.Debug("snmp trap ignored (unparseable)", "source", srcIP, "error", err)
		return
	}
	ev.SourceIP = srcIP
	if r.Community != "" && ev.Community != r.Community {
		slog.Debug("snmp trap ignored (community mismatch)", "source", srcIP)
		return
	}
	r.Sink(ctx, *ev)
}

// parseTrap decodes an SNMPv1-Trap-PDU or SNMPv2-Trap-PDU into a TrapEvent.
// The decoder is deliberately tolerant: unknown or malformed varbinds are
// skipped instead of rejecting the whole trap.
func parseTrap(data []byte) (*TrapEvent, error) {
	if len(data) < 2 || data[0] != 0x30 {
		return nil, fmt.Errorf("not an ASN.1 sequence")
	}
	outer := &berParser{data: data}
	message, err := outer.readTLV() // step *into* the outer sequence
	if err != nil {
		return nil, err
	}
	p := &berParser{data: message}
	// version
	if _, err := p.readTLV(); err != nil {
		return nil, err
	}
	community, err := p.readTLV()
	if err != nil {
		return nil, err
	}
	ev := &TrapEvent{
		Community: string(community),
		Variables: map[string]string{},
		Received:  time.Now().UTC(),
	}
	pduTag, pduContent, err := p.readTLVWithTag()
	if err != nil {
		return nil, err
	}
	switch pduTag {
	case 0xA4: // SNMPv1 Trap-PDU
		return parseV1Trap(pduContent, ev)
	case 0xA7: // SNMPv2-Trap-PDU
		return parseV2Trap(pduContent, ev)
	default:
		return nil, fmt.Errorf("not a trap PDU (tag 0x%02x)", pduTag)
	}
}

// parseV1Trap reads enterprise-OID, agent-addr, generic/specific trap and the
// varbind list from an SNMPv1 Trap-PDU body.
func parseV1Trap(content []byte, ev *TrapEvent) (*TrapEvent, error) {
	p := &berParser{data: content}
	enterprise, err := p.readTLV()
	if err != nil {
		return nil, err
	}
	ev.TrapOID = decodeOIDValue(enterprise)
	if _, err := p.readTLV(); err != nil { // agent address
		return nil, err
	}
	generic, err := p.readTLV() // generic trap type
	if err != nil {
		return nil, err
	}
	specific, err := p.readTLV() // specific trap type
	if err != nil {
		return nil, err
	}
	if _, err := p.readTLV(); err != nil { // timestamp
		return nil, err
	}
	ev.Variables["generic_trap"] = berIntString(generic)
	ev.Variables["specific_trap"] = berIntString(specific)
	readVarbinds(p, ev)
	return ev, nil
}

// parseV2Trap reads request-id, error fields and the varbind list from an
// SNMPv2-Trap-PDU body; the snmpTrapOID.0 varbind becomes TrapOID.
func parseV2Trap(content []byte, ev *TrapEvent) (*TrapEvent, error) {
	p := &berParser{data: content}
	for i := 0; i < 3; i++ { // request-id, error-status, error-index
		if _, err := p.readTLV(); err != nil {
			return nil, err
		}
	}
	readVarbinds(p, ev)
	if oid, ok := ev.Variables["1.3.6.1.6.3.1.1.4.1.0"]; ok { // snmpTrapOID.0
		ev.TrapOID = oid
		delete(ev.Variables, "1.3.6.1.6.3.1.1.4.1.0")
	}
	return ev, nil
}

// readVarbinds walks the varbind list at the parser's current position and
// stores every decodable binding.
func readVarbinds(p *berParser, ev *TrapEvent) {
	listContent, err := p.readTLV() // varbind list sequence
	if err != nil {
		return
	}
	lp := &berParser{data: listContent}
	for lp.remaining() > 0 {
		vb, err := lp.readTLV() // single varbind sequence
		if err != nil {
			return
		}
		vp := &berParser{data: vb}
		oidBytes, err := vp.readTLV()
		if err != nil {
			return
		}
		valTag, valBytes, err := vp.readTLVWithTag()
		if err != nil {
			return
		}
		name := decodeOIDValue(oidBytes)
		ev.Variables[name] = berValueString(valTag, valBytes)
	}
}

// berValueString renders a varbind value as string; OIDs are decoded, byte
// strings are printed when printable and hex-encoded otherwise, integers are
// rendered as decimals.
func berValueString(tag byte, value []byte) string {
	switch tag {
	case 0x06:
		return decodeOIDValue(value)
	case 0x02, 0x41, 0x42, 0x43, 0x46, 0x47: // INTEGER and application unsigned types
		return berIntString(value)
	default:
		if isPrintable(string(value)) && len(value) > 0 {
			return string(value)
		}
		return fmt.Sprintf("0x%x", value)
	}
}

func berIntString(value []byte) string {
	// BER INTEGER is signed two's complement: sign-extend the most
	// significant byte so negative values decode correctly.
	var n int64
	if len(value) > 0 && value[0]&0x80 != 0 {
		n = -1
	}
	for _, b := range value {
		n = n<<8 | int64(b)
	}
	return fmt.Sprintf("%d", n)
}

// decodeOIDValue renders a BER-encoded OID value as a dotted string.
func decodeOIDValue(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d.%d", value[0]/40, value[0]%40)
	rest := value[1:]
	cur := 0
	for _, c := range rest {
		cur = cur<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			fmt.Fprintf(&b, ".%d", cur)
			cur = 0
		}
	}
	return b.String()
}

// berParser is a minimal cursor over BER-encoded data (short- and long-form
// lengths, no indefinite form — SNMP never uses it).
type berParser struct {
	data []byte
	pos  int
}

func (p *berParser) remaining() int { return len(p.data) - p.pos }

// readTLV reads the next TLV and returns its content bytes.
func (p *berParser) readTLV() ([]byte, error) {
	_, content, err := p.readTLVWithTag()
	return content, err
}

// readTLVWithTag reads the next TLV and returns tag and content bytes.
func (p *berParser) readTLVWithTag() (byte, []byte, error) {
	if p.remaining() < 2 {
		return 0, nil, fmt.Errorf("truncated TLV")
	}
	tag := p.data[p.pos]
	length, err := p.readLength()
	if err != nil {
		return 0, nil, err
	}
	if p.remaining() < length {
		return 0, nil, fmt.Errorf("truncated value")
	}
	content := p.data[p.pos : p.pos+length]
	p.pos += length
	return tag, content, nil
}

func (p *berParser) readLength() (int, error) {
	p.pos++ // move past tag
	if p.remaining() < 1 {
		return 0, fmt.Errorf("truncated length")
	}
	first := p.data[p.pos]
	p.pos++
	if first&0x80 == 0 {
		return int(first), nil
	}
	numBytes := int(first & 0x7f)
	if numBytes > 4 || p.remaining() < numBytes {
		return 0, fmt.Errorf("unsupported length form")
	}
	length := 0
	for i := 0; i < numBytes; i++ {
		length = length<<8 | int(p.data[p.pos])
		p.pos++
	}
	return length, nil
}
