package probe

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakedns"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func startDNS(t *testing.T) *fakedns.Server {
	t.Helper()
	s, err := fakedns.Start()
	if err != nil {
		t.Skipf("fakedns: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// dnsSpec is a valid dns spec against the fake server; the target is the
// server (WP-1010 AK2).
func dnsSpec(s *fakedns.Server, params map[string]any) probewire.ProbeSpec {
	p := map[string]any{"port": s.Port()}
	for k, v := range params {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	return probewire.ProbeSpec{
		ID: "dns-1", Type: TypeDNS, Target: s.Host(), IntervalMS: 60_000, TimeoutMS: 1_000,
		DownCount: 3, UpCount: 1, Params: raw, Epoch: 2,
	}
}

func runDNS(t *testing.T, spec probewire.ProbeSpec) probewire.Result { //nolint:gocritic // test helper
	t.Helper()
	r := Run(context.Background(), &spec, nil)
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid wire result %+v: %v", r, err)
	}
	return r
}

// TestDNSQueryTypes covers PRT-12 and AK2: all ten qtypes, table driven,
// CAA over Type(257); the answer content is matched by expect_contains.
func TestDNSQueryTypes(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.AddAAAA("www.example.net", "2001:db8::80")
	s.AddCNAME("alias.example.net", "www.example.net")
	s.AddMX("example.net", 10, "mail.example.net")
	s.AddNS("example.net", "ns1.example.net")
	s.AddSOA("example.net", "ns1.example.net", "hostmaster.example.net", 2026111201)
	s.AddTXT("example.net", "v=spf1 -all")
	s.AddPTR("80.2.0.192.in-addr.arpa", "www.example.net")
	s.AddSRV("_sip._tcp.example.net", 10, 60, 5060, "sip.example.net")
	s.AddCAA("example.net", 0, "issue", "letsencrypt.org")
	cases := []struct{ qtype, qname, contains string }{
		{"A", "www.example.net", "192.0.2.80"},
		{"AAAA", "www.example.net", "2001:db8::80"},
		{"CNAME", "alias.example.net", "www.example.net."},
		{"MX", "example.net", "10 mail.example.net."},
		{"NS", "example.net", "ns1.example.net."},
		{"SOA", "example.net", "hostmaster.example.net."},
		{"TXT", "example.net", "v=spf1"},
		{"PTR", "80.2.0.192.in-addr.arpa", "www.example.net."},
		{"SRV", "_sip._tcp.example.net", "5060 sip.example.net."},
		{"CAA", "example.net", "issue \"letsencrypt.org\""},
	}
	for _, c := range cases {
		t.Run(c.qtype, func(t *testing.T) {
			for _, transport := range []string{"udp", "tcp"} {
				r := runDNS(t, dnsSpec(s, map[string]any{"qname": c.qname, "qtype": c.qtype, "transport": transport, "expect_contains": []string{strings.ToUpper(c.contains)}}))
				if r.Status != probewire.StatusOK || r.Code != "" {
					t.Fatalf("%s: %s/%s labels %v", transport, r.Status, r.Code, r.Labels)
				}
				if r.Values[ValueDNSAnswerCount] != 1 || r.Values[ValueDNSRcode] != 0 {
					t.Fatalf("%s: values %v", transport, r.Values)
				}
				if _, ok := r.Values[ValueDNSResponseSeconds]; !ok {
					t.Fatalf("%s: response time missing", transport)
				}
			}
		})
	}
	if dnsQTypes["CAA"] != dnsmessage.Type(257) {
		t.Fatal("CAA is not queried as Type(257)")
	}
}

// TestDNSNXDOMAINAgainstExpectedRcode covers AK5: NXDOMAIN against the
// expected NOERROR is answer_mismatch, fail for severity down and ok with
// the warn code (→ degraded) for severity degraded; an expected NXDOMAIN
// is ok.
func TestDNSNXDOMAINAgainstExpectedRcode(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "gone.example.net"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnAnswerMismatch || r.Values[ValueDNSRcode] != 3 {
		t.Fatalf("severity down: %s/%s %v", r.Status, r.Code, r.Values)
	}
	spec := dnsSpec(s, map[string]any{"qname": "gone.example.net", "severity": "degraded"})
	r = runDNS(t, spec)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnAnswerMismatch {
		t.Fatalf("severity degraded: %s/%s", r.Status, r.Code)
	}
	// The state machine derives degraded from ok with the warn code.
	m := openMachine(t, t.TempDir())
	spec.DownCount = 1
	if _, ch, err := m.Observe(&spec, r); err != nil || ch == nil || ch.To != probewire.StateDegraded || ch.Reason != probewire.WarnAnswerMismatch {
		t.Fatalf("state change = %+v, %v", ch, err)
	}
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "gone.example.net", "expect_rcode": "NXDOMAIN"}))
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("expected NXDOMAIN: %s/%s", r.Status, r.Code)
	}
	servfail := dnsmessage.RCodeServerFailure
	s.SetRCode(&servfail)
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnAnswerMismatch || r.Values[ValueDNSRcode] != 2 {
		t.Fatalf("SERVFAIL: %s/%s %v", r.Status, r.Code, r.Values)
	}
}

// TestDNSExpectContainsAndRequireAD covers AK5: a missing expected content
// or AD bit is answer_mismatch with the label missing.
func TestDNSExpectContainsAndRequireAD(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.AddA("www.example.net", "192.0.2.81")
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "expect_contains": []string{"192.0.2.81", "192.0.2.99", "198.51.100.1"}}))
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnAnswerMismatch {
		t.Fatalf("missing content: %s/%s", r.Status, r.Code)
	}
	if got := strings.Split(r.Labels[LabelDNSMissing], ","); !slices.Equal(got, []string{"192.0.2.99", "198.51.100.1"}) {
		t.Fatalf("missing label = %q", r.Labels[LabelDNSMissing])
	}
	if r.Values[ValueDNSAnswerCount] != 2 {
		t.Fatalf("answer count = %v", r.Values[ValueDNSAnswerCount])
	}
	// require_ad without AD bit.
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "require_ad": true, "severity": "degraded"}))
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnAnswerMismatch || r.Labels[LabelDNSMissing] != "AD" {
		t.Fatalf("require_ad without AD: %s/%s %v", r.Status, r.Code, r.Labels)
	}
	s.SetAD(true)
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "require_ad": true, "expect_contains": []string{"192.0.2.80"}}))
	if r.Status != probewire.StatusOK || r.Code != "" || len(r.Labels) != 0 {
		t.Fatalf("with AD: %s/%s %v", r.Status, r.Code, r.Labels)
	}
}

// TestDNSTruncatedFallsBackToTCP covers AK3: a UDP answer with the TC bit
// is repeated over TCP.
func TestDNSTruncatedFallsBackToTCP(t *testing.T) {
	s := startDNS(t)
	s.AddTXT("big.example.net", "full answer over tcp")
	s.SetTruncateUDP(true)
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "big.example.net", "qtype": "TXT", "expect_contains": []string{"full answer"}}))
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("fallback: %s/%s %v", r.Status, r.Code, r.Labels)
	}
	if s.Queries("udp") != 1 || s.Queries("tcp") != 1 {
		t.Fatalf("queries udp=%d tcp=%d, want 1/1", s.Queries("udp"), s.Queries("tcp"))
	}
}

// TestDNSWrongIDIsDiscarded covers AK3: an answer with a wrong ID is
// discarded; the right one afterwards is ok, only wrong IDs time out.
func TestDNSWrongIDIsDiscarded(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.SetWrongID(fakedns.WrongIDFirst)
	for _, transport := range []string{"udp", "tcp"} {
		r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "transport": transport, "expect_contains": []string{"192.0.2.80"}}))
		if r.Status != probewire.StatusOK || r.Code != "" {
			t.Fatalf("%s wrong then right: %s/%s", transport, r.Status, r.Code)
		}
	}
	s.SetWrongID(fakedns.WrongIDOnly)
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("only wrong IDs: %s/%s", r.Status, r.Code)
	}
}

// TestDNSResponseOverLimit covers AK3 and PRT-21 (payload cap): a UDP
// answer above 4096 bytes and a TCP answer above the TCP limit are
// discarded with fail/response_too_large. The 2-byte length prefix caps a
// TCP message at 65535 bytes, so the TCP case runs with a lowered limit.
func TestDNSResponseOverLimit(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.SetOversize(5000)
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonResponseTooLarge {
		t.Fatalf("udp 5000 bytes: %s/%s", r.Status, r.Code)
	}
	// Over TCP the same answer is within 64 KiB.
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "transport": "tcp"}))
	if r.Status != probewire.StatusOK {
		t.Fatalf("tcp 5000 bytes: %s/%s", r.Status, r.Code)
	}
	swapExecutor(t, TypeDNS, &DNSExecutor{MaxTCPResponse: 4096})
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "transport": "tcp"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonResponseTooLarge {
		t.Fatalf("tcp above the limit: %s/%s", r.Status, r.Code)
	}
	if DNSMaxUDPResponse != 4096 || DNSMaxTCPResponse != 65535 {
		t.Fatal("response limits changed")
	}
	typ, _ := probewire.Lookup(TypeDNS)
	if typ.MaxPayloadBytes != DNSMaxTCPResponse {
		t.Fatalf("max_payload_bytes = %d, want %d", typ.MaxPayloadBytes, DNSMaxTCPResponse)
	}
}

// TestDNSSOASerialValue covers AK4: the SOA serial is a value.
func TestDNSSOASerialValue(t *testing.T) {
	s := startDNS(t)
	s.AddSOA("example.net", "ns1.example.net", "hostmaster.example.net", 2026111207)
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "example.net", "qtype": "SOA"}))
	if r.Status != probewire.StatusOK || r.Values[ValueDNSSOASerial] != 2026111207 {
		t.Fatalf("soa: %s/%s %v", r.Status, r.Code, r.Values)
	}
	r = runDNS(t, dnsSpec(s, map[string]any{"qname": "example.net", "qtype": "A"}))
	if _, ok := r.Values[ValueDNSSOASerial]; ok {
		t.Fatal("serial reported for a non-SOA query")
	}
}

// TestDNSTimeoutIsFail covers AK5: no answer by the deadline is
// fail/timeout; a refused TCP connection is fail/refused.
func TestDNSTimeoutIsFail(t *testing.T) {
	s := startDNS(t)
	s.AddA("www.example.net", "192.0.2.80")
	s.SetDelay(2 * time.Second)
	start := time.Now()
	r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net"}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("delayed answer: %s/%s", r.Status, r.Code)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("timeout after %s", d)
	}
	// A closed TCP port refuses the connection.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	spec := dnsSpec(s, map[string]any{"qname": "www.example.net", "transport": "tcp", "port": port})
	if r := runDNS(t, spec); r.Status != probewire.StatusFail || r.Code != probewire.ReasonRefused {
		t.Fatalf("closed tcp port: %s/%s", r.Status, r.Code)
	}
	// A TCP connection accepted and then closed without an answer, or in
	// the middle of the length prefix, is refused as well (PRT-21).
	s.SetDelay(0)
	for _, mode := range []int{fakedns.CloseTCPNoAnswer, fakedns.CloseTCPHalfPrefix} {
		s.SetCloseTCP(mode)
		before := s.Queries("tcp")
		r := runDNS(t, dnsSpec(s, map[string]any{"qname": "www.example.net", "transport": "tcp"}))
		if r.Status != probewire.StatusFail || r.Code != probewire.ReasonRefused {
			t.Fatalf("tcp closed by the server (mode %d): %s/%s", mode, r.Status, r.Code)
		}
		if s.Queries("tcp") != before+1 {
			t.Fatalf("tcp closed by the server (mode %d): query did not reach the server", mode)
		}
	}
	s.SetCloseTCP(fakedns.CloseTCPOff)
}

// TestDNSLimits covers AK6 and PRT-21 (limits test): timeout 11 s, an
// unknown qtype and 11 entries in expect_contains are field errors of
// probewire.Validate; the check is rejected before execution.
func TestDNSLimits(t *testing.T) {
	s := startDNS(t)
	typ, ok := probewire.Lookup(TypeDNS)
	if !ok || !typ.Available() {
		t.Fatal("dns missing or not available")
	}
	if typ.MinIntervalS != 10 || typ.DefaultIntervalS != 60 || typ.DefaultTimeoutS != 5 || typ.MaxTimeoutS != 10 || typ.MaxPayloadBytes != 65535 {
		t.Fatalf("dns limits = %+v", typ)
	}
	for name, want := range map[string]any{"qtype": "A", "transport": "udp", "port": 53.0, "rd": true, "expect_rcode": "NOERROR", "require_ad": false, "severity": "down"} {
		if got, ok := typ.ParamDefault(name); !ok || got != want {
			t.Errorf("default of %s = %v, want %v", name, got, want)
		}
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "x"
	}
	cases := []struct {
		name  string
		spec  probewire.ProbeSpec
		field string
	}{
		{"timeout 11 s", func() probewire.ProbeSpec {
			sp := dnsSpec(s, map[string]any{"qname": "a.example.net"})
			sp.TimeoutMS = 11_000
			return sp
		}(), "timeout_s"},
		{"unknown qtype", dnsSpec(s, map[string]any{"qname": "a.example.net", "qtype": "ANY"}), "params.qtype"},
		{"11 expect_contains", dnsSpec(s, map[string]any{"qname": "a.example.net", "expect_contains": eleven}), "params.expect_contains"},
		{"qname missing", dnsSpec(s, nil), "params.qname"},
		{"qname empty label", dnsSpec(s, map[string]any{"qname": "a..example.net"}), "params.qname"},
		{"qname label of 64 bytes", dnsSpec(s, map[string]any{"qname": strings.Repeat("a", 64) + ".example.net"}), "params.qname"},
		{"qname blank", dnsSpec(s, map[string]any{"qname": " "}), "params.qname"},
		{"qname leading dot", dnsSpec(s, map[string]any{"qname": ".example.net"}), "params.qname"},
		{"unknown rcode", dnsSpec(s, map[string]any{"qname": "a.example.net", "expect_rcode": "BADVERS"}), "params.expect_rcode"},
		{"unknown severity", dnsSpec(s, map[string]any{"qname": "a.example.net", "severity": "warning"}), "params.severity"},
	}
	for _, c := range cases {
		errs := probewire.Validate(c.spec)
		if !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == c.field }) {
			t.Errorf("%s: errs = %v, want field %s", c.name, errs, c.field)
		}
		r := runDNS(t, c.spec)
		if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig || !strings.Contains(r.Labels[LabelField], c.field) {
			t.Errorf("%s: run = %s/%s %v", c.name, r.Status, r.Code, r.Labels)
		}
	}
	if s.Queries("udp")+s.Queries("tcp") != 0 {
		t.Fatal("a rejected spec reached the server")
	}
	for name, want := range map[string]bool{
		"example.net": true, "example.net.": true, ".": true, " www.example.net ": true,
		strings.Repeat("a", 63) + ".example.net": true, "_sip._tcp.example.net": true,
		"": false, "..": false, "a..b": false, strings.Repeat("a", 64): false,
	} {
		if got := probewire.ValidDNSName(name); got != want {
			t.Errorf("ValidDNSName(%q) = %v, want %v", name, got, want)
		}
	}
}
