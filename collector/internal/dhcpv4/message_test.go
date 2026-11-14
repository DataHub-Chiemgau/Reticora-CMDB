package dhcpv4

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"
)

var testMAC = []byte{0x02, 0x00, 0x5e, 0x10, 0x20, 0x30}

func mustEncode(t testing.TB, m *Message) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

func sampleMessages() map[string]*Message {
	discover := NewRequest(Discover, 0x11223344, testMAC)
	discover.Flags = FlagBroadcast
	discover.SetOption(ParamRequestOption(OptSubnetMask, OptRouter, OptDNS, OptLeaseTime))
	discover.SetOption(ClientIDOption(HTypeEthernet, testMAC))

	offer := NewReply(discover, Offer)
	offer.YIAddr = netip.MustParseAddr("192.0.2.50")
	offer.SIAddr = netip.MustParseAddr("192.0.2.1")
	offer.SetOption(AddrOption(OptServerID, netip.MustParseAddr("192.0.2.1")))
	offer.SetOption(AddrOption(OptSubnetMask, netip.MustParseAddr("255.255.255.0")))
	offer.SetOption(AddrsOption(OptRouter, netip.MustParseAddr("192.0.2.1")))
	offer.SetOption(AddrsOption(OptDNS, netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("198.51.100.53")))
	offer.SetOption(LeaseTimeOption(time.Hour))

	inform := NewRequest(Inform, 0xdeadbeef, testMAC)
	inform.CIAddr = netip.MustParseAddr("192.0.2.77")
	inform.GIAddr = netip.MustParseAddr("198.51.100.1")
	inform.Hops = 1
	inform.SetOption(Option{Code: OptHostName, Data: []byte("probe-collector")})
	inform.SetOption(ParamRequestOption(OptDNS))

	ack := NewReply(inform, Ack)
	ack.SetOption(AddrOption(OptServerID, netip.MustParseAddr("192.0.2.1")))
	ack.SetOption(AddrsOption(OptDNS, netip.MustParseAddr("192.0.2.53")))
	return map[string]*Message{"DISCOVER": discover, "OFFER": offer, "INFORM": inform, "ACK": ack}
}

// TestRoundTrip covers WP-1011 AK1: DISCOVER, OFFER, INFORM and ACK decode
// to what was encoded.
func TestRoundTrip(t *testing.T) {
	for name, m := range sampleMessages() {
		got, err := Decode(mustEncode(t, m))
		if err != nil {
			t.Fatalf("%s: Decode: %v", name, err)
		}
		if got.MessageType().String() != name {
			t.Errorf("%s: message type %s", name, got.MessageType())
		}
		// Addresses decode as 4-byte addresses; compare after one more
		// round trip and field by field for the set ones.
		again, err := Decode(mustEncode(t, got))
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Errorf("%s: second round trip differs: %v\n%+v\n%+v", name, err, got, again)
		}
		if got.XID != m.XID || got.Op != m.Op || got.Flags != m.Flags || got.Hops != m.Hops ||
			!bytes.Equal(got.HardwareAddr(), m.HardwareAddr()) || !reflect.DeepEqual(got.Options, m.Options) {
			t.Errorf("%s: decoded %+v, encoded %+v", name, got, m)
		}
		for _, pair := range [][2]netip.Addr{{got.CIAddr, m.CIAddr}, {got.YIAddr, m.YIAddr}, {got.SIAddr, m.SIAddr}, {got.GIAddr, m.GIAddr}} {
			want := pair[1]
			if !want.IsValid() {
				want = netip.IPv4Unspecified()
			}
			if pair[0] != want {
				t.Errorf("%s: address %s, want %s", name, pair[0], want)
			}
		}
	}
}

// withRawOptions returns an encoded message of type t whose option area is
// raw (cookie included by the caller's options after the header).
func withRawOptions(t *testing.T, raw ...byte) []byte {
	t.Helper()
	b := mustEncode(t, NewRequest(Discover, 1, testMAC))
	out := append([]byte(nil), b[:minDecode]...)
	out = append(out, raw...)
	for len(out) < MinMessage {
		out = append(out, OptPad)
	}
	return out
}

// TestOptionsDecode covers AK1: the options 1, 3, 6, 12, 50, 51, 53, 54,
// 55, 61 and 82 (sub-options raw); option 0 is skipped.
func TestOptionsDecode(t *testing.T) {
	raw := []byte{
		OptPad, OptPad,
		OptMessageType, 1, byte(Offer),
		OptSubnetMask, 4, 255, 255, 255, 0,
		OptRouter, 8, 192, 0, 2, 1, 192, 0, 2, 2,
		OptDNS, 4, 192, 0, 2, 53,
		OptHostName, 3, 'c', 'p', 'e',
		OptPad,
		OptRequestedIP, 4, 192, 0, 2, 50,
		OptLeaseTime, 4, 0, 0, 0x0e, 0x10,
		OptServerID, 4, 192, 0, 2, 1,
		OptParamRequest, 3, 1, 3, 6,
		OptClientID, 7, 1, 0x02, 0x00, 0x5e, 0x10, 0x20, 0x30,
		OptRelayAgentInfo, 9, 1, 3, 'e', 't', '0', 2, 2, 0xab, 0xcd,
		OptEnd,
	}
	m, err := Decode(withRawOptions(t, raw...))
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageType() != Offer {
		t.Errorf("message type %v", m.MessageType())
	}
	if a, ok := m.SubnetMask(); !ok || a.String() != "255.255.255.0" {
		t.Errorf("subnet mask %v %v", a, ok)
	}
	if r := m.Routers(); len(r) != 2 || r[1].String() != "192.0.2.2" {
		t.Errorf("routers %v", r)
	}
	if d := m.DNSServers(); len(d) != 1 || d[0].String() != "192.0.2.53" {
		t.Errorf("dns %v", d)
	}
	if m.HostName() != "cpe" {
		t.Errorf("host name %q", m.HostName())
	}
	if a, ok := m.RequestedIP(); !ok || a.String() != "192.0.2.50" {
		t.Errorf("requested ip %v", a)
	}
	if l, ok := m.LeaseTime(); !ok || l != time.Hour {
		t.Errorf("lease %v", l)
	}
	if a, ok := m.ServerID(); !ok || a.String() != "192.0.2.1" {
		t.Errorf("server id %v", a)
	}
	if p := m.ParamRequestList(); !slices.Equal(p, []uint8{1, 3, 6}) {
		t.Errorf("parameter request list %v", p)
	}
	if c := m.ClientID(); len(c) != 7 || c[0] != HTypeEthernet {
		t.Errorf("client id %v", c)
	}
	subs, ok := m.RelayAgentInfo()
	want := []SubOption{{Code: 1, Data: []byte("et0")}, {Code: 2, Data: []byte{0xab, 0xcd}}}
	if !ok || !reflect.DeepEqual(subs, want) {
		t.Errorf("relay agent info %v %v, want %v", subs, ok, want)
	}
	codes := make([]uint8, 0, len(m.Options))
	for _, o := range m.Options {
		codes = append(codes, o.Code)
	}
	if !slices.Equal(codes, []uint8{53, 1, 3, 6, 12, 50, 51, 54, 55, 61, 82}) {
		t.Errorf("option order %v (pad must be skipped)", codes)
	}
	// Option 82 is only decoded.
	if _, err := m.Encode(); !errors.Is(err, ErrRelayInfoEncode) {
		t.Errorf("Encode with option 82: %v, want ErrRelayInfoEncode", err)
	}
}

// TestUnknownOptionsKeptRaw: unknown options keep data and order through a
// round trip.
func TestUnknownOptionsKeptRaw(t *testing.T) {
	m := NewRequest(Inform, 7, testMAC)
	m.Options = append(m.Options,
		Option{Code: 224, Data: []byte{1, 2, 3}},
		Option{Code: OptDNS, Data: []byte{192, 0, 2, 53}},
		Option{Code: 43, Data: []byte{}},
		Option{Code: 224, Data: []byte{9}},
	)
	got, err := Decode(mustEncode(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Options, m.Options) {
		t.Fatalf("options %v, want %v", got.Options, m.Options)
	}
}

// TestRejectsMalformed covers AK2.
func TestRejectsMalformed(t *testing.T) {
	valid := mustEncode(t, NewRequest(Discover, 1, testMAC))
	noCookie := slices.Clone(valid)
	noCookie[headerLen] = 0
	noType := withRawOptions(t, OptHostName, 1, 'x', OptEnd)
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"above 1500 bytes":              {append(slices.Clone(valid), make([]byte, MaxMessage)...), ErrTooLong},
		"below 240 bytes":               {valid[:239], ErrTooShort},
		"no magic cookie":               {noCookie, ErrNoMagicCookie},
		"option beyond end":             {append(slices.Clone(valid[:minDecode]), OptMessageType, 1, 1, OptHostName, 10, 'a'), ErrOptionOverrun},
		"length byte missing":           {append(slices.Clone(valid[:minDecode]), OptMessageType, 1, 1, OptHostName), ErrOptionOverrun},
		"no option 53":                  {noType, ErrNoMessageType},
		"bad option 53 length":          {withRawOptions(t, OptMessageType, 2, 1, 1, OptEnd), ErrInvalidOption},
		"bad server id":                 {withRawOptions(t, OptMessageType, 1, 1, OptServerID, 3, 1, 2, 3, OptEnd), ErrInvalidOption},
		"bad option 82":                 {withRawOptions(t, OptMessageType, 1, 1, OptRelayAgentInfo, 3, 1, 5, 0, OptEnd), ErrInvalidOption},
		"1500 bytes without option 255": {fullWithoutEnd(t), ErrTooLong},
	} {
		if _, err := Decode(tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// Option 82 and a message without option 53 do not encode.
	m := NewRequest(Discover, 1, testMAC)
	m.Options = append(m.Options, Option{Code: OptRelayAgentInfo, Data: []byte{1, 1, 'x'}})
	if _, err := m.Encode(); !errors.Is(err, ErrRelayInfoEncode) {
		t.Errorf("encode option 82: %v", err)
	}
	if _, err := (&Message{}).Encode(); !errors.Is(err, ErrNoMessageType) {
		t.Errorf("encode without option 53: %v", err)
	}
}

// TestMinimumLength300: an encoded message is at least 300 bytes (RFC 1542)
// and ends its options with 255.
func TestMinimumLength300(t *testing.T) {
	for name, m := range sampleMessages() {
		b := mustEncode(t, m)
		if len(b) < MinMessage {
			t.Errorf("%s: %d bytes, want at least %d", name, len(b), MinMessage)
		}
	}
	big := NewRequest(Inform, 1, testMAC)
	big.SetOption(Option{Code: 250, Data: bytes.Repeat([]byte{'x'}, 200)})
	b := mustEncode(t, big)
	if len(b) != minDecode+3+202+1 {
		t.Errorf("a message above 300 bytes is padded: %d bytes", len(b))
	}
}

// FuzzDecode covers AK3: no panic for any input; a decoded message (without
// option 82, which is never encoded) encodes and decodes to the same value.
func FuzzDecode(f *testing.F) {
	for _, m := range sampleMessages() {
		b, err := m.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add(make([]byte, 240))
	f.Add(fullWithoutEnd(f))
	f.Fuzz(func(t *testing.T, in []byte) {
		m, err := Decode(in)
		if err != nil {
			return
		}
		if _, relay := m.Option(OptRelayAgentInfo); relay {
			return
		}
		out, err := m.Encode()
		if err != nil {
			t.Fatalf("decoded message does not encode: %v", err)
		}
		again, err := Decode(out)
		if err != nil {
			t.Fatalf("re-encoded message does not decode: %v", err)
		}
		if !reflect.DeepEqual(m, again) {
			t.Fatalf("round trip differs:\n%+v\n%+v", m, again)
		}
	})
}

// fullWithoutEnd returns a 1500-byte message whose options fill the input
// up to its last byte without option 255: header and cookie (240), 53/1/1
// (3), four options 200 of 255 bytes (1028) and option 201 of 227 bytes
// (229). Encoding it would need 1501 bytes.
func fullWithoutEnd(tb testing.TB) []byte {
	tb.Helper()
	b := make([]byte, minDecode, MaxMessage)
	copy(b[headerLen:minDecode], magicCookie[:])
	b = append(b, OptMessageType, 1, byte(Discover))
	for range 4 {
		b = append(b, 200, 255)
		b = append(b, bytes.Repeat([]byte{'x'}, 255)...)
	}
	b = append(b, 201, 227)
	b = append(b, bytes.Repeat([]byte{'y'}, 227)...)
	if len(b) != MaxMessage {
		tb.Fatalf("fixture has %d bytes, want %d", len(b), MaxMessage)
	}
	return b
}
