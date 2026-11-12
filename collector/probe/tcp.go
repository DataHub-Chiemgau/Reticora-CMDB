package probe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeTCP is the register key of the TCP connect probe.
const TypeTCP = "tcp"

// TCPReadCap is the most the tcp probe reads of an answer (PRT-03).
const TCPReadCap = 1024

// tcpReadMargin ends reading this long before the deadline of the check,
// so that the executor reports expect_mismatch for an answer without match
// before Run reports the deadline as timeout.
const tcpReadMargin = 50 * time.Millisecond

func init() { Register(TypeTCP, &TCPExecutor{}) }

// DialFunc opens a connection; net.Dialer.DialContext satisfies it.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// TCPExecutor is the probe type tcp (PRT-03, WP-950, WP-970): it connects
// to target and params.port and reports the connect time as rtt_ms
// (probe_tcp_connect_seconds). Only with send set it sends that string
// (at most 256 bytes, escapes decoded); only with expect set it reads the
// answer, at most 1 KiB or until the timeout, and matches the RE2
// expression against it. Without send and expect the connection is closed
// right after the connect.
//
// Outcomes: ok on connect (and a match of expect); fail with refused
// (RST), timeout (no connect, or no answer until the timeout),
// unreachable (ICMP unreachable) or expect_mismatch (an answer without a
// match). degraded over rtt_warn_ms is derived by the state machine
// (WP-952).
type TCPExecutor struct {
	// Dial replaces the dialer in tests; nil uses net.Dialer.
	Dial DialFunc
}

type tcpParams struct {
	Port   int    `json:"port"`
	Send   string `json:"send"`
	Expect string `json:"expect"`
	Family string `json:"family"`
}

// Execute implements Executor.
func (e *TCPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p tcpParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	addr, ok := tcpAddress(spec.Target, p.Port)
	if !ok {
		// Name the field like a rejected validation does (E-177).
		return configResult("params.port", "port is required")
	}
	var expect *regexp.Regexp
	if p.Expect != "" {
		re, err := regexp.Compile(p.Expect)
		if err != nil {
			return configResult("params.expect", "must be a valid RE2 regular expression")
		}
		expect = re
	}
	send := probewire.UnescapeSend(p.Send)
	if len(send) > probewire.MaxTCPSend {
		return configResult("params.send", "must be at most 256 bytes")
	}
	network := "tcp"
	switch p.Family {
	case "v4":
		network = "tcp4"
	case "v6":
		network = "tcp6"
	}

	dial := e.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	start := time.Now()
	conn, err := dial(ctx, network, addr)
	if err != nil {
		return failResult(probewire.StatusFail, tcpReason(ctx, err))
	}
	rtt := msSince(start)
	defer func() { _ = conn.Close() }()
	okRes := probewire.Result{Status: probewire.StatusOK, RTTMS: rtt}
	if send == "" && expect == nil {
		return okRes
	}
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	// Close the connection when the caller cancels, so a blocked write or
	// read returns.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if send != "" {
		if _, err := io.WriteString(conn, send); err != nil {
			r := failResult(probewire.StatusFail, tcpIOReason(ctx, err))
			r.RTTMS = rtt
			return r
		}
	}
	if expect == nil {
		return okRes
	}
	code := readExpect(ctx, conn, expect)
	if code == "" {
		return okRes
	}
	r := failResult(probewire.StatusFail, code)
	r.RTTMS = rtt
	return r
}

// readExpect reads the answer until expect matches, 1 KiB is read, the
// peer closes or the deadline (less a margin) is reached. It returns ""
// on a match, expect_mismatch for an answer without a match, timeout when
// nothing arrived and refused for a reset without an answer.
func readExpect(ctx context.Context, conn net.Conn, expect *regexp.Regexp) string {
	if deadline, has := ctx.Deadline(); has {
		readBy := deadline.Add(-tcpReadMargin)
		if time.Until(readBy) <= 0 {
			readBy = deadline
		}
		_ = conn.SetReadDeadline(readBy)
	}
	buf := make([]byte, 0, TCPReadCap)
	chunk := make([]byte, TCPReadCap)
	for len(buf) < TCPReadCap {
		n, err := conn.Read(chunk[:TCPReadCap-len(buf)])
		buf = append(buf, chunk[:n]...)
		if n > 0 && expect.Match(buf) {
			return ""
		}
		if err == nil {
			continue
		}
		switch {
		case len(buf) > 0:
			return probewire.ReasonExpectMismatch
		case errors.Is(err, io.EOF):
			// Closed without an answer: an answer that cannot match.
			return probewire.ReasonExpectMismatch
		default:
			return tcpIOReason(ctx, err)
		}
	}
	return probewire.ReasonExpectMismatch
}

// configResult is error/config with the field in the labels (E-177).
func configResult(field, msg string) probewire.Result {
	r := failResult(probewire.StatusError, probewire.ReasonConfig)
	r.Labels = map[string]string{LabelField: field, LabelMessage: msg}
	return r
}

// tcpAddress builds host:port from target and port; a port in the target
// is replaced by port.
func tcpAddress(target string, port int) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" || port <= 0 || port > 65535 {
		return "", false
	}
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return net.JoinHostPort(host, strconv.Itoa(port)), true
}

// tcpReason maps a dial error to a reason code: refused (ECONNREFUSED),
// unreachable (EHOSTUNREACH, ENETUNREACH, failed name resolution) or
// timeout (deadline). A cancellation by the caller is mapped to timeout as
// well; Run turns it into error (→ unknown), never fail.
func tcpReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return probewire.ReasonRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return probewire.ReasonUnreachable
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return probewire.ReasonTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return probewire.ReasonTimeout
	}
	return probewire.ReasonUnreachable
}

// tcpIOReason maps an error of a write or read after the connect: a
// deadline is timeout, a reset (RST) refused, anything else unreachable.
func tcpIOReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded), ctx.Err() != nil:
		return probewire.ReasonTimeout
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNREFUSED):
		return probewire.ReasonRefused
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return probewire.ReasonTimeout
	}
	return probewire.ReasonUnreachable
}
