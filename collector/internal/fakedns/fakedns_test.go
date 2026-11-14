package fakedns

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func start(t *testing.T) *Server {
	t.Helper()
	s, err := Start()
	if err != nil {
		t.Skipf("fakedns: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func query(t *testing.T, id uint16, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func (s *Server) addr() string { return net.JoinHostPort(s.Host(), strconv.Itoa(s.Port())) }

// udpAsk sends a query and returns every answer until the read times out.
func udpAsk(t *testing.T, s *Server, msg []byte, wait time.Duration) [][]byte {
	t.Helper()
	c, err := net.Dial("udp", s.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	buf := make([]byte, 65535)
	for {
		_ = c.SetReadDeadline(time.Now().Add(wait))
		n, err := c.Read(buf)
		if err != nil {
			return out
		}
		out = append(out, append([]byte(nil), buf[:n]...))
	}
}

func tcpAsk(t *testing.T, s *Server, msg []byte) []byte {
	t.Helper()
	c, err := net.Dial("tcp", s.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(msg))), msg...)); err != nil {
		t.Fatal(err)
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func parse(t *testing.T, msg []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	rrs, err := p.AllAnswers()
	if err != nil {
		t.Fatal(err)
	}
	return h, rrs
}

// TestFakeDNSServesUDPAndTCP: the zone is served on one port over UDP and
// TCP, unknown names are NXDOMAIN, other types of a known name NOERROR
// without answers, CAA as an unknown type.
func TestFakeDNSServesUDPAndTCP(t *testing.T) {
	s := start(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.AddCAA("example.net", 0, "issue", "letsencrypt.org")
	for _, transport := range []string{"udp", "tcp"} {
		var answer []byte
		q := query(t, 0x1234, "WWW.example.net.", dnsmessage.TypeA)
		if transport == "udp" {
			answers := udpAsk(t, s, q, 200*time.Millisecond)
			if len(answers) != 1 {
				t.Fatalf("udp answers = %d", len(answers))
			}
			answer = answers[0]
		} else {
			answer = tcpAsk(t, s, q)
		}
		h, rrs := parse(t, answer)
		if h.ID != 0x1234 || !h.Response || h.RCode != dnsmessage.RCodeSuccess || len(rrs) != 1 {
			t.Fatalf("%s: header %+v, %d answers", transport, h, len(rrs))
		}
		if a := rrs[0].Body.(*dnsmessage.AResource).A; a != [4]byte{192, 0, 2, 80} {
			t.Fatalf("%s: A = %v", transport, a)
		}
		if s.Queries(transport) != 1 {
			t.Fatalf("%s queries = %d", transport, s.Queries(transport))
		}
	}
	h, rrs := parse(t, tcpAsk(t, s, query(t, 1, "nothing.example.net.", dnsmessage.TypeA)))
	if h.RCode != dnsmessage.RCodeNameError || len(rrs) != 0 {
		t.Fatalf("unknown name: %v, %d answers", h.RCode, len(rrs))
	}
	h, rrs = parse(t, tcpAsk(t, s, query(t, 2, "www.example.net.", dnsmessage.TypeMX)))
	if h.RCode != dnsmessage.RCodeSuccess || len(rrs) != 0 {
		t.Fatalf("other type: %v, %d answers", h.RCode, len(rrs))
	}
	_, rrs = parse(t, tcpAsk(t, s, query(t, 3, "example.net.", TypeCAA)))
	if len(rrs) != 1 || rrs[0].Header.Type != TypeCAA {
		t.Fatalf("CAA answers = %+v", rrs)
	}
	if data := rrs[0].Body.(*dnsmessage.UnknownResource).Data; string(data[2:7]) != "issue" || string(data[7:]) != "letsencrypt.org" {
		t.Fatalf("CAA data = %q", data)
	}
}

// TestFakeDNSKnobs: response code, AD bit, TC bit over UDP only, wrong ID,
// delay and oversized answers.
func TestFakeDNSKnobs(t *testing.T) {
	s := start(t)
	s.AddA("a.example.net", "192.0.2.1")
	q := query(t, 0x0101, "a.example.net.", dnsmessage.TypeA)

	servfail := dnsmessage.RCodeServerFailure
	s.SetRCode(&servfail)
	s.SetAD(true)
	h, _ := parse(t, tcpAsk(t, s, q))
	if h.RCode != servfail || !h.AuthenticData {
		t.Fatalf("rcode/AD: %+v", h)
	}
	s.SetRCode(nil)
	s.SetAD(false)

	s.SetTruncateUDP(true)
	h, rrs := parse(t, udpAsk(t, s, q, 200*time.Millisecond)[0])
	if !h.Truncated || len(rrs) != 0 {
		t.Fatalf("udp with TC: %+v, %d answers", h, len(rrs))
	}
	h, rrs = parse(t, tcpAsk(t, s, q))
	if h.Truncated || len(rrs) != 1 {
		t.Fatalf("tcp with TC knob: %+v, %d answers", h, len(rrs))
	}
	s.SetTruncateUDP(false)

	s.SetWrongID(WrongIDFirst)
	answers := udpAsk(t, s, q, 200*time.Millisecond)
	if len(answers) != 2 {
		t.Fatalf("wrong id first: %d answers", len(answers))
	}
	if h, _ := parse(t, answers[0]); h.ID == 0x0101 {
		t.Fatal("first answer carries the right ID")
	}
	if h, _ := parse(t, answers[1]); h.ID != 0x0101 {
		t.Fatal("second answer carries a wrong ID")
	}
	s.SetWrongID(WrongIDOnly)
	answers = udpAsk(t, s, q, 200*time.Millisecond)
	if len(answers) != 1 {
		t.Fatalf("wrong id only: %d answers", len(answers))
	}
	if h, _ := parse(t, answers[0]); h.ID == 0x0101 {
		t.Fatal("wrong-id-only answer carries the right ID")
	}
	s.SetWrongID(WrongIDOff)

	s.SetDelay(150 * time.Millisecond)
	begin := time.Now()
	_ = tcpAsk(t, s, q)
	if d := time.Since(begin); d < 150*time.Millisecond {
		t.Fatalf("delay: answered after %s", d)
	}
	s.SetDelay(0)

	s.SetOversize(5000)
	answers = udpAsk(t, s, q, 300*time.Millisecond)
	if len(answers) != 1 || len(answers[0]) <= 5000 {
		t.Fatalf("oversize udp: %d answers, %d bytes", len(answers), len(answers[0]))
	}
	if msg := tcpAsk(t, s, q); len(msg) <= 5000 {
		t.Fatalf("oversize tcp: %d bytes", len(msg))
	}
	s.SetOversize(0)

	for mode, want := range map[int]int{CloseTCPNoAnswer: 0, CloseTCPHalfPrefix: 1} {
		s.SetCloseTCP(mode)
		before := s.Queries("tcp")
		if got := tcpRaw(t, s, q); len(got) != want {
			t.Fatalf("close tcp mode %d: read %d bytes before EOF, want %d", mode, len(got), want)
		}
		if s.Queries("tcp") != before+1 {
			t.Fatalf("close tcp mode %d: query not counted", mode)
		}
	}
	s.SetCloseTCP(CloseTCPOff)
	if h, _ := parse(t, tcpAsk(t, s, q)); h.ID != 0x0101 {
		t.Fatal("tcp after close knob off: no answer")
	}
}

// tcpRaw sends a framed query and returns every byte read until the
// server closes the connection.
func tcpRaw(t *testing.T, s *Server, msg []byte) []byte {
	t.Helper()
	c, err := net.Dial("tcp", s.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(msg))), msg...)); err != nil { //nolint:gosec // test query
		t.Fatal(err)
	}
	out, err := io.ReadAll(c)
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("read: %v", err)
	}
	return out
}
