package fakertr

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

type rawPDU struct {
	version, typ uint8
	session      uint16
	body         []byte
}

func readRaw(t *testing.T, r *bufio.Reader) rawPDU {
	t.Helper()
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[4:8])-8)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatal(err)
	}
	return rawPDU{version: hdr[0], typ: hdr[1], session: binary.BigEndian.Uint16(hdr[2:4]), body: body}
}

// readAnswer reads Cache Response … End of Data and returns the
// announcements, withdrawals and the serial of End of Data.
func readAnswer(t *testing.T, r *bufio.Reader) (ann, wd int, serial uint32) {
	t.Helper()
	if p := readRaw(t, r); p.typ != TypeCacheResponse || p.session != 42 {
		t.Fatalf("first PDU = %+v, want Cache Response of session 42", p)
	}
	for {
		p := readRaw(t, r)
		switch p.typ {
		case TypeIPv4Prefix, TypeIPv6Prefix:
			if p.body[0]&1 == 1 {
				ann++
			} else {
				wd++
			}
		case TypeEndOfData:
			if len(p.body) != 16 {
				t.Fatalf("End of Data v1 body = %d bytes, want 16", len(p.body))
			}
			return ann, wd, binary.BigEndian.Uint32(p.body[:4])
		default:
			t.Fatalf("unexpected PDU %+v", p)
		}
	}
}

// TestFakeRTRServesSerials covers WP-1020 AK7: Reset Query gives the full
// set, an update sends Serial Notify, a Serial Query gives the delta, a
// version 1 query to a version 0 cache gives an Error Report.
func TestFakeRTRServesSerials(t *testing.T) {
	v4 := VRP{Prefix: netip.MustParsePrefix("192.0.2.0/24"), MaxLen: 24, ASN: 64500}
	v6 := VRP{Prefix: netip.MustParsePrefix("2001:db8::/32"), MaxLen: 48, ASN: 64501}
	s, err := Listen(Config{Version: 1, SessionID: 42, Serial: 5, VRPs: []VRP{v4, v6}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck // test cleanup
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck // test cleanup
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	_, _ = c.Write(header(1, TypeResetQuery, 0, 8, 0))
	if ann, wd, serial := readAnswer(t, r); ann != 2 || wd != 0 || serial != 5 {
		t.Fatalf("reset answer: %d announce, %d withdraw, serial %d", ann, wd, serial)
	}
	v4b := VRP{Prefix: netip.MustParsePrefix("198.51.100.0/24"), MaxLen: 24, ASN: 64502}
	if got := s.Update(Delta{Announce: []VRP{v4b}, Withdraw: []VRP{v4}}); got != 6 {
		t.Fatalf("serial after update = %d", got)
	}
	if p := readRaw(t, r); p.typ != TypeSerialNotify || binary.BigEndian.Uint32(p.body) != 6 {
		t.Fatalf("notify = %+v", p)
	}
	_, _ = c.Write(header(1, TypeSerialQuery, 42, 12, 5))
	if ann, wd, serial := readAnswer(t, r); ann != 1 || wd != 1 || serial != 6 {
		t.Fatalf("serial answer: %d announce, %d withdraw, serial %d", ann, wd, serial)
	}
	// SetDelay holds every answer back measurably.
	s.SetDelay(300 * time.Millisecond)
	start := time.Now()
	_, _ = c.Write(header(1, TypeSerialQuery, 42, 12, 6))
	if ann, wd, serial := readAnswer(t, r); ann != 0 || wd != 0 || serial != 6 {
		t.Fatalf("delayed answer: %d announce, %d withdraw, serial %d", ann, wd, serial)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Fatalf("answer after %v, want at least the delay of 300ms", took)
	}
	s.SetDelay(0)
	// An unknown serial is a Cache Reset.
	_, _ = c.Write(header(1, TypeSerialQuery, 42, 12, 1000))
	if p := readRaw(t, r); p.typ != TypeCacheReset {
		t.Fatalf("unknown serial: %+v, want Cache Reset", p)
	}
	if q := s.Queries(); len(q) != 4 || q[1].Type != TypeSerialQuery || q[1].Serial != 5 {
		t.Fatalf("queries = %+v", q)
	}

	v0, err := Listen(Config{Version: 0, SessionID: 42})
	if err != nil {
		t.Fatal(err)
	}
	defer v0.Close() //nolint:errcheck // test cleanup
	c0, err := net.Dial("tcp", v0.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c0.Close() //nolint:errcheck // test cleanup
	_ = c0.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c0.Write(header(1, TypeResetQuery, 0, 8, 0))
	if p := readRaw(t, bufio.NewReader(c0)); p.typ != TypeErrorReport || p.version != 0 || p.session != ErrUnsupportedVersion {
		t.Fatalf("v1 query to v0 cache: %+v", p)
	}
}
