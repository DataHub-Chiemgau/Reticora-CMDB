package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeDNS is the register key of the DNS query probe.
const TypeDNS = "dns"

// Response limits of the dns probe (PRT-12): 4096 bytes over UDP (the
// EDNS0 buffer size it announces) and 64 KiB over TCP, where the 2-byte
// length prefix caps a message at 65535 bytes (max_payload_bytes).
const (
	DNSMaxUDPResponse = 4096
	DNSMaxTCPResponse = 65535
)

// Value names of a dns result (WP-1010 AK4) and the label that names the
// missing expected contents.
const (
	ValueDNSResponseSeconds = "probe_dns_response_seconds"
	ValueDNSRcode           = "probe_dns_rcode"
	ValueDNSAnswerCount     = "probe_dns_answer_count"
	ValueDNSSOASerial       = "probe_dns_soa_serial"
	LabelDNSMissing         = "missing"
)

// dnsTypeCAA is CAA (RFC 8659); dnsmessage has no constant for it.
const dnsTypeCAA = dnsmessage.Type(257)

var dnsQTypes = map[string]dnsmessage.Type{
	"A": dnsmessage.TypeA, "AAAA": dnsmessage.TypeAAAA, "CNAME": dnsmessage.TypeCNAME,
	"MX": dnsmessage.TypeMX, "NS": dnsmessage.TypeNS, "SOA": dnsmessage.TypeSOA,
	"TXT": dnsmessage.TypeTXT, "PTR": dnsmessage.TypePTR, "SRV": dnsmessage.TypeSRV,
	"CAA": dnsTypeCAA,
}

var dnsRcodes = map[string]dnsmessage.RCode{
	"NOERROR": 0, "FORMERR": 1, "SERVFAIL": 2, "NXDOMAIN": 3, "NOTIMP": 4, "REFUSED": 5,
	"YXDOMAIN": 6, "YXRRSET": 7, "NXRRSET": 8, "NOTAUTH": 9, "NOTZONE": 10,
}

func init() { Register(TypeDNS, &DNSExecutor{}) }

// DNSExecutor is the probe type dns (PRT-12, WP-1010): one query to the
// server named by the probe's target, over UDP with EDNS0 (4096 bytes) and
// a fallback to TCP when the answer is truncated, or over TCP only (2-byte
// length prefix). The query ID comes from crypto/rand; an answer whose ID
// or question does not match is discarded until the deadline. An answer
// above the response limit is discarded with fail/response_too_large.
//
// Outcomes: no answer by the deadline is fail/timeout, a refused or
// dropped connection fail/refused. A different RCODE, a missing expected
// content or a missing AD bit with require_ad is answer_mismatch: ok with
// that warn code (→ degraded, derived by the state machine) for severity
// degraded, fail for severity down. Values: response time, RCODE, answer
// count and for SOA the serial; missing contents are named in the label
// missing.
type DNSExecutor struct {
	// MaxTCPResponse lowers the TCP response limit in tests; 0 uses
	// DNSMaxTCPResponse.
	MaxTCPResponse int
}

type dnsParams struct {
	QName          string   `json:"qname"`
	QType          string   `json:"qtype"`
	Transport      string   `json:"transport"`
	Port           int      `json:"port"`
	RD             *bool    `json:"rd"`
	ExpectRcode    string   `json:"expect_rcode"`
	ExpectContains []string `json:"expect_contains"`
	RequireAD      bool     `json:"require_ad"`
	Severity       string   `json:"severity"`
}

// errDNSTooLarge marks an answer above the response limit.
var errDNSTooLarge = errors.New("dns answer above the response limit")

// Execute implements Executor.
func (e *DNSExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p := dnsParams{QType: "A", Transport: "udp", Port: 53, ExpectRcode: "NOERROR", Severity: "down"}
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	qtype, ok := dnsQTypes[p.QType]
	if !ok {
		return configResult("params.qtype", "unknown query type")
	}
	wantRcode, ok := dnsRcodes[p.ExpectRcode]
	if !ok {
		return configResult("params.expect_rcode", "unknown response code")
	}
	// An empty label or one above 63 bytes passes NewName and fails only
	// when the query is packed; it is a configuration error, not an
	// unreachable target.
	qname, err := dnsmessage.NewName(dnsCanonical(p.QName))
	if err != nil || !probewire.ValidDNSName(p.QName) {
		return configResult("params.qname", "invalid name")
	}
	server, ok := dnsServer(spec.Target, p.Port)
	if !ok {
		return configResult("target", "the target must be the address of the DNS server")
	}
	rd := p.RD == nil || *p.RD
	q := dnsmessage.Question{Name: qname, Type: qtype, Class: dnsmessage.ClassINET}
	if _, _, qerr := dnsQuery(&q, rd, p.RequireAD); qerr != nil {
		return configResult("params.qname", "invalid name")
	}
	maxTCP := e.MaxTCPResponse
	if maxTCP <= 0 {
		maxTCP = DNSMaxTCPResponse
	}

	start := time.Now()
	var answer []byte
	if p.Transport == "tcp" {
		answer, err = dnsExchangeTCP(ctx, server, &q, rd, p.RequireAD, maxTCP)
	} else {
		var truncated bool
		answer, truncated, err = dnsExchangeUDP(ctx, server, &q, rd, p.RequireAD)
		if err == nil && truncated {
			answer, err = dnsExchangeTCP(ctx, server, &q, rd, p.RequireAD, maxTCP)
		}
	}
	elapsed := time.Since(start)
	if err != nil {
		return failResult(probewire.StatusFail, dnsReason(ctx, err))
	}
	return evaluateDNS(answer, &p, wantRcode, elapsed)
}

// evaluateDNS turns a matching answer into the result.
func evaluateDNS(answer []byte, p *dnsParams, wantRcode dnsmessage.RCode, elapsed time.Duration) probewire.Result {
	var parser dnsmessage.Parser
	h, err := parser.Start(answer)
	if err != nil {
		return failResult(probewire.StatusFail, probewire.WarnAnswerMismatch)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return failResult(probewire.StatusFail, probewire.WarnAnswerMismatch)
	}
	var texts []string
	var serial *uint32
	count := 0
	for {
		rh, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return failResult(probewire.StatusFail, probewire.WarnAnswerMismatch)
		}
		count++
		text, soa, err := dnsRecordText(&parser, &rh)
		if err != nil {
			return failResult(probewire.StatusFail, probewire.WarnAnswerMismatch)
		}
		if soa != nil && serial == nil {
			serial = soa
		}
		texts = append(texts, strings.ToLower(text))
	}
	rtt := float64(elapsed.Microseconds()) / 1000
	r := probewire.Result{
		Status: probewire.StatusOK,
		RTTMS:  &rtt,
		Values: map[string]float64{
			ValueDNSResponseSeconds: elapsed.Seconds(),
			ValueDNSRcode:           float64(h.RCode),
			ValueDNSAnswerCount:     float64(count),
		},
	}
	if serial != nil {
		r.Values[ValueDNSSOASerial] = float64(*serial)
	}
	var missing []string
	for _, want := range p.ExpectContains {
		w := strings.ToLower(want)
		found := false
		for _, t := range texts {
			if strings.Contains(t, w) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, want)
		}
	}
	if p.RequireAD && !h.AuthenticData {
		missing = append(missing, "AD")
	}
	if h.RCode == wantRcode && len(missing) == 0 {
		return r
	}
	r.Code = probewire.WarnAnswerMismatch
	if len(missing) > 0 {
		r.Labels = map[string]string{LabelDNSMissing: strings.Join(missing, ",")}
	}
	if p.Severity != "degraded" {
		r.Status = probewire.StatusFail
	}
	return r
}

// dnsRecordText renders an answer record for the expect_contains match;
// for SOA it also returns the serial.
func dnsRecordText(p *dnsmessage.Parser, rh *dnsmessage.ResourceHeader) (text string, ttl *uint32, err error) {
	switch rh.Type {
	case dnsmessage.TypeA:
		r, rerr := p.AResource()
		return netip.AddrFrom4(r.A).String(), nil, rerr
	case dnsmessage.TypeAAAA:
		r, rerr := p.AAAAResource()
		return netip.AddrFrom16(r.AAAA).String(), nil, rerr
	case dnsmessage.TypeCNAME:
		r, rerr := p.CNAMEResource()
		return r.CNAME.String(), nil, rerr
	case dnsmessage.TypeMX:
		r, rerr := p.MXResource()
		return fmt.Sprintf("%d %s", r.Pref, r.MX.String()), nil, rerr
	case dnsmessage.TypeNS:
		r, rerr := p.NSResource()
		return r.NS.String(), nil, rerr
	case dnsmessage.TypeSOA:
		r, rerr := p.SOAResource()
		serial := r.Serial
		return fmt.Sprintf("%s %s %d %d %d %d %d", r.NS.String(), r.MBox.String(), r.Serial, r.Refresh, r.Retry, r.Expire, r.MinTTL), &serial, rerr
	case dnsmessage.TypeTXT:
		r, rerr := p.TXTResource()
		return strings.Join(r.TXT, ""), nil, rerr
	case dnsmessage.TypePTR:
		r, rerr := p.PTRResource()
		return r.PTR.String(), nil, rerr
	case dnsmessage.TypeSRV:
		r, rerr := p.SRVResource()
		return fmt.Sprintf("%d %d %d %s", r.Priority, r.Weight, r.Port, r.Target.String()), nil, rerr
	}
	r, err := p.UnknownResource()
	if err != nil {
		return "", nil, err
	}
	if rh.Type == dnsTypeCAA && len(r.Data) >= 2 && len(r.Data) >= 2+int(r.Data[1]) {
		tagLen := int(r.Data[1])
		return fmt.Sprintf("%d %s %q", r.Data[0], r.Data[2:2+tagLen], r.Data[2+tagLen:]), nil, nil
	}
	return string(r.Data), nil, nil
}

// dnsQuery builds a query with a random ID: EDNS0 with a 4096-byte buffer,
// RD as configured and the AD bit when require_ad asks for it (RFC 6840).
func dnsQuery(q *dnsmessage.Question, rd, ad bool) (id uint16, msg []byte, err error) {
	var idb [2]byte
	_, err = rand.Read(idb[:])
	if err != nil {
		return 0, nil, err
	}
	id = binary.BigEndian.Uint16(idb[:])
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: rd, AuthenticData: ad})
	err = b.StartQuestions()
	if err != nil {
		return 0, nil, err
	}
	err = b.Question(*q)
	if err != nil {
		return 0, nil, err
	}
	err = b.StartAdditionals()
	if err != nil {
		return 0, nil, err
	}
	var opt dnsmessage.ResourceHeader
	err = opt.SetEDNS0(DNSMaxUDPResponse, dnsmessage.RCodeSuccess, false)
	if err != nil {
		return 0, nil, err
	}
	err = b.OPTResource(opt, dnsmessage.OPTResource{})
	if err != nil {
		return 0, nil, err
	}
	msg, err = b.Finish()
	return id, msg, err
}

// dnsMatches reports whether msg is the answer to the query id/q.
func dnsMatches(msg []byte, id uint16, q *dnsmessage.Question) bool {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || h.ID != id || !h.Response {
		return false
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) != 1 {
		return false
	}
	return qs[0].Type == q.Type && qs[0].Class == q.Class && strings.EqualFold(qs[0].Name.String(), q.Name.String())
}

// dnsHeaderMatches checks the ID and the response flag only; used before
// the size check, which must not depend on parsing an oversized message.
func dnsHeaderMatches(msg []byte, id uint16) bool {
	return len(msg) >= 12 && binary.BigEndian.Uint16(msg[:2]) == id && msg[2]&0x80 != 0
}

func dnsExchangeUDP(ctx context.Context, server string, q *dnsmessage.Question, rd, ad bool) (resp []byte, truncated bool, err error) {
	id, query, err := dnsQuery(q, rd, ad)
	if err != nil {
		return nil, false, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	_, err = conn.Write(query)
	if err != nil {
		return nil, false, err
	}
	buf := make([]byte, 65536)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, false, err
		}
		msg := buf[:n]
		if !dnsHeaderMatches(msg, id) {
			continue // another answer: discard and wait until the deadline
		}
		if n > DNSMaxUDPResponse {
			return nil, false, errDNSTooLarge
		}
		if !dnsMatches(msg, id, q) {
			continue
		}
		truncated := msg[2]&0x02 != 0
		return append([]byte(nil), msg...), truncated, nil
	}
}

func dnsExchangeTCP(ctx context.Context, server string, q *dnsmessage.Question, rd, ad bool, maxResponse int) ([]byte, error) {
	id, query, err := dnsQuery(q, rd, ad)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	framed := binary.BigEndian.AppendUint16(nil, uint16(len(query))) //nolint:gosec // a query is far below 64 KiB
	if _, err := conn.Write(append(framed, query...)); err != nil {
		return nil, err
	}
	for {
		var l [2]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return nil, err
		}
		size := int(binary.BigEndian.Uint16(l[:]))
		if size > maxResponse {
			// Only the header is read to tell our answer from another one.
			head := make([]byte, min(size, 12))
			if _, err := io.ReadFull(conn, head); err != nil {
				return nil, err
			}
			if dnsHeaderMatches(head, id) {
				return nil, errDNSTooLarge
			}
			if _, err := io.CopyN(io.Discard, conn, int64(size-len(head))); err != nil {
				return nil, err
			}
			continue
		}
		msg := make([]byte, size)
		if _, err := io.ReadFull(conn, msg); err != nil {
			return nil, err
		}
		if dnsMatches(msg, id, q) {
			return msg, nil
		}
	}
}

// dnsReason maps an exchange error to a reason code.
func dnsReason(ctx context.Context, err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, errDNSTooLarge):
		return probewire.ReasonResponseTooLarge
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return probewire.ReasonTimeout
	case errors.As(err, &ne) && ne.Timeout():
		return probewire.ReasonTimeout
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return probewire.ReasonRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return probewire.ReasonUnreachable
	}
	return probewire.ReasonUnreachable
}

// dnsServer builds host:port of the server from the target and the port.
func dnsServer(target string, port int) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" || port < 1 || port > 65535 {
		return "", false
	}
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return net.JoinHostPort(host, strconv.Itoa(port)), true
}

func dnsCanonical(name string) string {
	name = strings.TrimSpace(name)
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}
