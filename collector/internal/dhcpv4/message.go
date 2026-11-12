// Package dhcpv4 is a minimal DHCPv4 message codec after RFC 2131 and
// RFC 2132 for the dhcp probe (WP-1011, PRT-13, E-190, E-195): the BOOTP
// header, the magic cookie and the options the probe sends and reads. It
// needs no third-party library. The probe type (collector/probe, WP-1012)
// and the test server collector/internal/fakedhcp use the same codec; the
// package registers nothing and does not import collector/probe.
package dhcpv4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Ports of DHCPv4 (RFC 2131 4.1).
const (
	ServerPort = 67
	ClientPort = 68
)

// BOOTP operations.
const (
	OpRequest uint8 = 1 // BOOTREQUEST
	OpReply   uint8 = 2 // BOOTREPLY
)

// Hardware type Ethernet (htype 1, hlen 6).
const (
	HTypeEthernet uint8 = 1
	HLenEthernet  uint8 = 6
)

// FlagBroadcast is the broadcast bit of the flags field (RFC 2131 2).
const FlagBroadcast uint16 = 0x8000

// Size limits: the fixed header with the cookie is 240 bytes; an encoded
// message is at least 300 bytes (RFC 1542 2.1); an input above 1500 bytes
// (Ethernet MTU) is refused.
const (
	headerLen  = 236
	minDecode  = headerLen + 4
	MinMessage = 300
	MaxMessage = 1500
)

// magicCookie is 99.130.83.99 (RFC 2131 3).
var magicCookie = [4]byte{99, 130, 83, 99}

// Errors of Decode and Encode.
var (
	ErrTooLong         = errors.New("dhcpv4: message longer than 1500 bytes")
	ErrTooShort        = errors.New("dhcpv4: message shorter than 240 bytes")
	ErrNoMagicCookie   = errors.New("dhcpv4: magic cookie missing")
	ErrOptionOverrun   = errors.New("dhcpv4: option length beyond the end of the message")
	ErrNoMessageType   = errors.New("dhcpv4: option 53 (message type) missing")
	ErrInvalidOption   = errors.New("dhcpv4: invalid option")
	ErrRelayInfoEncode = errors.New("dhcpv4: option 82 (relay agent information) is never encoded")
)

// Message is a DHCPv4 message: the BOOTP header and the options in the
// order of the message (without pad and end).
type Message struct {
	Op     uint8
	HType  uint8
	HLen   uint8
	Hops   uint8
	XID    uint32
	Secs   uint16
	Flags  uint16
	CIAddr netip.Addr
	YIAddr netip.Addr
	SIAddr netip.Addr
	GIAddr netip.Addr
	CHAddr [16]byte
	SName  [64]byte
	File   [128]byte
	// Options holds every option in message order; unknown options stay
	// raw. Option 0 (pad) and 255 (end) are not kept.
	Options []Option
}

// Decode reads a DHCPv4 message (AK1, AK2). It refuses inputs above 1500
// or below 240 bytes, inputs whose encoding would exceed 1500 bytes (an
// option area filling the input without option 255), without the magic cookie, with an option running
// beyond the end of the message, with a malformed known option and
// without option 53.
func Decode(b []byte) (*Message, error) {
	switch {
	case len(b) > MaxMessage:
		return nil, ErrTooLong
	case len(b) < minDecode:
		return nil, ErrTooShort
	case [4]byte(b[headerLen:minDecode]) != magicCookie:
		return nil, ErrNoMagicCookie
	}
	m := &Message{
		Op: b[0], HType: b[1], HLen: b[2], Hops: b[3],
		XID:    binary.BigEndian.Uint32(b[4:8]),
		Secs:   binary.BigEndian.Uint16(b[8:10]),
		Flags:  binary.BigEndian.Uint16(b[10:12]),
		CIAddr: netip.AddrFrom4([4]byte(b[12:16])),
		YIAddr: netip.AddrFrom4([4]byte(b[16:20])),
		SIAddr: netip.AddrFrom4([4]byte(b[20:24])),
		GIAddr: netip.AddrFrom4([4]byte(b[24:28])),
	}
	copy(m.CHAddr[:], b[28:44])
	copy(m.SName[:], b[44:108])
	copy(m.File[:], b[108:236])
	opts, err := decodeOptions(b[minDecode:])
	if err != nil {
		return nil, err
	}
	m.Options = opts
	if _, ok := m.Option(OptMessageType); !ok {
		return nil, ErrNoMessageType
	}
	// The 1500-byte limit holds in both directions (AK3): an option area
	// that runs to the end of a 1500-byte input without option 255 would
	// encode to 1501 bytes with the end option Encode always writes. Pad
	// bytes are dropped, so only header, options and end count; option 82
	// is never encoded.
	enc := minDecode + 1
	for _, o := range opts {
		if o.Code != OptRelayAgentInfo {
			enc += 2 + len(o.Data)
		}
	}
	if enc > MaxMessage {
		return nil, ErrTooLong
	}
	return m, nil
}

// decodeOptions reads the options up to option 255 or the end of b.
func decodeOptions(b []byte) ([]Option, error) {
	var out []Option
	for i := 0; i < len(b); {
		code := b[i]
		switch code {
		case OptPad:
			i++
			continue
		case OptEnd:
			return out, nil
		}
		if i+1 >= len(b) {
			return nil, ErrOptionOverrun
		}
		n := int(b[i+1])
		if i+2+n > len(b) {
			return nil, ErrOptionOverrun
		}
		data := make([]byte, n)
		copy(data, b[i+2:i+2+n])
		opt := Option{Code: code, Data: data}
		if err := opt.check(); err != nil {
			return nil, err
		}
		out = append(out, opt)
		i += 2 + n
	}
	return out, nil
}

// Encode writes the message (AK1): header, cookie, options, end, padded to
// at least 300 bytes. Option 82 cannot be encoded (only relays add it);
// option 53 is required.
func (m *Message) Encode() ([]byte, error) {
	if _, ok := m.Option(OptMessageType); !ok {
		return nil, ErrNoMessageType
	}
	out := make([]byte, minDecode, MinMessage)
	out[0], out[1], out[2], out[3] = m.Op, m.HType, m.HLen, m.Hops
	binary.BigEndian.PutUint32(out[4:8], m.XID)
	binary.BigEndian.PutUint16(out[8:10], m.Secs)
	binary.BigEndian.PutUint16(out[10:12], m.Flags)
	for i, a := range []netip.Addr{m.CIAddr, m.YIAddr, m.SIAddr, m.GIAddr} {
		v4, err := addr4(a)
		if err != nil {
			return nil, err
		}
		copy(out[12+4*i:16+4*i], v4[:])
	}
	copy(out[28:44], m.CHAddr[:])
	copy(out[44:108], m.SName[:])
	copy(out[108:236], m.File[:])
	copy(out[headerLen:minDecode], magicCookie[:])
	for _, o := range m.Options {
		if o.Code == OptRelayAgentInfo {
			return nil, ErrRelayInfoEncode
		}
		if o.Code == OptPad || o.Code == OptEnd || len(o.Data) > 255 {
			return nil, fmt.Errorf("%w: code %d with %d bytes", ErrInvalidOption, o.Code, len(o.Data))
		}
		if err := o.check(); err != nil {
			return nil, err
		}
		out = append(out, o.Code, byte(len(o.Data)))
		out = append(out, o.Data...)
	}
	out = append(out, OptEnd)
	for len(out) < MinMessage {
		out = append(out, OptPad)
	}
	if len(out) > MaxMessage {
		return nil, ErrTooLong
	}
	return out, nil
}

// addr4 returns the four bytes of an IPv4 address; the zero Addr is
// 0.0.0.0.
func addr4(a netip.Addr) ([4]byte, error) {
	if !a.IsValid() {
		return [4]byte{}, nil
	}
	a = a.Unmap()
	if !a.Is4() {
		return [4]byte{}, fmt.Errorf("dhcpv4: %s is no IPv4 address", a)
	}
	return a.As4(), nil
}

// MessageType returns option 53 (0 when missing).
func (m *Message) MessageType() MessageType {
	o, ok := m.Option(OptMessageType)
	if !ok || len(o.Data) != 1 {
		return 0
	}
	return MessageType(o.Data[0])
}

// HardwareAddr returns the client hardware address (chaddr cut to hlen).
func (m *Message) HardwareAddr() []byte {
	n := min(int(m.HLen), len(m.CHAddr))
	return m.CHAddr[:n]
}

// Option returns the first option with the code.
func (m *Message) Option(code uint8) (Option, bool) {
	for _, o := range m.Options {
		if o.Code == code {
			return o, true
		}
	}
	return Option{}, false
}

// SetOption replaces the first option with the code or appends it.
func (m *Message) SetOption(o Option) {
	for i := range m.Options {
		if m.Options[i].Code == o.Code {
			m.Options[i] = o
			return
		}
	}
	m.Options = append(m.Options, o)
}

// NewRequest returns a BOOTREQUEST of the message type with xid and the
// Ethernet hardware address mac (at most 16 bytes; hlen is its length).
func NewRequest(t MessageType, xid uint32, mac []byte) *Message {
	m := &Message{Op: OpRequest, HType: HTypeEthernet, HLen: uint8(min(len(mac), 16)), XID: xid} //nolint:gosec // at most 16
	copy(m.CHAddr[:], mac)
	m.Options = []Option{{Code: OptMessageType, Data: []byte{byte(t)}}}
	return m
}

// NewReply returns the BOOTREPLY of the message type to req: same xid,
// flags, giaddr and chaddr (RFC 2131 4.3.1, table 3).
func NewReply(req *Message, t MessageType) *Message {
	return &Message{
		Op: OpReply, HType: req.HType, HLen: req.HLen, XID: req.XID, Flags: req.Flags,
		CIAddr: req.CIAddr, GIAddr: req.GIAddr, CHAddr: req.CHAddr,
		Options: []Option{{Code: OptMessageType, Data: []byte{byte(t)}}},
	}
}
