package dhcpv4

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

// Option codes the codec knows (RFC 2132, RFC 3046). Every other code is
// kept raw.
const (
	OptPad            uint8 = 0
	OptSubnetMask     uint8 = 1
	OptRouter         uint8 = 3
	OptDNS            uint8 = 6
	OptHostName       uint8 = 12
	OptRequestedIP    uint8 = 50
	OptLeaseTime      uint8 = 51
	OptMessageType    uint8 = 53
	OptServerID       uint8 = 54
	OptParamRequest   uint8 = 55
	OptClientID       uint8 = 61
	OptRelayAgentInfo uint8 = 82
	OptEnd            uint8 = 255
)

// MessageType is the value of option 53 (RFC 2132 9.6).
type MessageType uint8

// Message types.
const (
	Discover MessageType = 1
	Offer    MessageType = 2
	Request  MessageType = 3
	Decline  MessageType = 4
	Ack      MessageType = 5
	Nak      MessageType = 6
	Release  MessageType = 7
	Inform   MessageType = 8
)

var messageTypeNames = map[MessageType]string{
	Discover: "DISCOVER", Offer: "OFFER", Request: "REQUEST", Decline: "DECLINE",
	Ack: "ACK", Nak: "NAK", Release: "RELEASE", Inform: "INFORM",
}

func (t MessageType) String() string {
	if s, ok := messageTypeNames[t]; ok {
		return s
	}
	return fmt.Sprintf("TYPE(%d)", uint8(t))
}

// Option is one option: its code and raw data.
type Option struct {
	Code uint8
	Data []byte
}

// check validates the length of the known options; unknown options are
// not checked.
func (o Option) check() error {
	n := len(o.Data)
	ok := true
	switch o.Code {
	case OptSubnetMask, OptRequestedIP, OptLeaseTime, OptServerID:
		ok = n == 4
	case OptRouter, OptDNS:
		ok = n >= 4 && n%4 == 0
	case OptHostName, OptParamRequest:
		ok = n >= 1
	case OptMessageType:
		ok = n == 1
	case OptClientID:
		ok = n >= 2
	case OptRelayAgentInfo:
		_, err := parseSubOptions(o.Data)
		ok = err == nil
	}
	if !ok {
		return fmt.Errorf("%w: code %d with %d bytes", ErrInvalidOption, o.Code, n)
	}
	return nil
}

// AddrOption is an option holding one IPv4 address (1, 50, 54).
func AddrOption(code uint8, a netip.Addr) Option {
	v4, _ := addr4(a)
	return Option{Code: code, Data: v4[:]}
}

// AddrsOption is an option holding IPv4 addresses (3, 6).
func AddrsOption(code uint8, addrs ...netip.Addr) Option {
	data := make([]byte, 0, 4*len(addrs))
	for _, a := range addrs {
		v4, _ := addr4(a)
		data = append(data, v4[:]...)
	}
	return Option{Code: code, Data: data}
}

// LeaseTimeOption is option 51.
func LeaseTimeOption(d time.Duration) Option {
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, uint32(d/time.Second)) //nolint:gosec // lease times fit 32 bits
	return Option{Code: OptLeaseTime, Data: data}
}

// ParamRequestOption is option 55 with the requested codes.
func ParamRequestOption(codes ...uint8) Option {
	return Option{Code: OptParamRequest, Data: append([]byte(nil), codes...)}
}

// ClientIDOption is option 61: hardware type and address.
func ClientIDOption(htype uint8, hw []byte) Option {
	return Option{Code: OptClientID, Data: append([]byte{htype}, hw...)}
}

// Addr returns option code as one IPv4 address (1, 50, 54).
func (m *Message) Addr(code uint8) (netip.Addr, bool) {
	o, ok := m.Option(code)
	if !ok || len(o.Data) != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte(o.Data)), true
}

// Addrs returns option code as a list of IPv4 addresses (3, 6).
func (m *Message) Addrs(code uint8) []netip.Addr {
	o, ok := m.Option(code)
	if !ok || len(o.Data)%4 != 0 {
		return nil
	}
	out := make([]netip.Addr, 0, len(o.Data)/4)
	for i := 0; i+4 <= len(o.Data); i += 4 {
		out = append(out, netip.AddrFrom4([4]byte(o.Data[i:i+4])))
	}
	return out
}

// SubnetMask returns option 1.
func (m *Message) SubnetMask() (netip.Addr, bool) { return m.Addr(OptSubnetMask) }

// Routers returns option 3.
func (m *Message) Routers() []netip.Addr { return m.Addrs(OptRouter) }

// DNSServers returns option 6.
func (m *Message) DNSServers() []netip.Addr { return m.Addrs(OptDNS) }

// HostName returns option 12.
func (m *Message) HostName() string {
	o, _ := m.Option(OptHostName)
	return string(o.Data)
}

// RequestedIP returns option 50.
func (m *Message) RequestedIP() (netip.Addr, bool) { return m.Addr(OptRequestedIP) }

// LeaseTime returns option 51.
func (m *Message) LeaseTime() (time.Duration, bool) {
	o, ok := m.Option(OptLeaseTime)
	if !ok || len(o.Data) != 4 {
		return 0, false
	}
	return time.Duration(binary.BigEndian.Uint32(o.Data)) * time.Second, true
}

// ServerID returns option 54.
func (m *Message) ServerID() (netip.Addr, bool) { return m.Addr(OptServerID) }

// ParamRequestList returns option 55.
func (m *Message) ParamRequestList() []uint8 {
	o, _ := m.Option(OptParamRequest)
	return o.Data
}

// ClientID returns option 61 (hardware type and identifier).
func (m *Message) ClientID() []byte {
	o, _ := m.Option(OptClientID)
	return o.Data
}

// SubOption is a sub-option of option 82 (RFC 3046), kept raw.
type SubOption struct {
	Code uint8
	Data []byte
}

// RelayAgentInfo returns the sub-options of option 82; option 82 is only
// decoded, never encoded.
func (m *Message) RelayAgentInfo() ([]SubOption, bool) {
	o, ok := m.Option(OptRelayAgentInfo)
	if !ok {
		return nil, false
	}
	subs, err := parseSubOptions(o.Data)
	return subs, err == nil
}

func parseSubOptions(b []byte) ([]SubOption, error) {
	var out []SubOption
	for i := 0; i < len(b); {
		if i+1 >= len(b) || i+2+int(b[i+1]) > len(b) {
			return nil, ErrOptionOverrun
		}
		n := int(b[i+1])
		out = append(out, SubOption{Code: b[i], Data: append([]byte(nil), b[i+2:i+2+n]...)})
		i += 2 + n
	}
	return out, nil
}
