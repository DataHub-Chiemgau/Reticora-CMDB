package probe

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeTCP is the register key of the TCP connect probe.
const TypeTCP = "tcp"

func init() { Register(TypeTCP, &TCPExecutor{}) }

// DialFunc opens a connection; net.Dialer.DialContext satisfies it.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// TCPExecutor checks that a TCP connection to target and port can be
// established (WP-950 AK3). It closes the connection right away and reports
// ok with the connect time as rtt_ms. Banner, thresholds and the contract
// entry of the tcp type follow in WP-970.
type TCPExecutor struct {
	// Dial replaces the dialer in tests; nil uses net.Dialer.
	Dial DialFunc
}

type tcpParams struct {
	Port int `json:"port"`
}

// Execute implements Executor.
func (e *TCPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	addr, ok := tcpAddress(&spec)
	if !ok {
		// Name the field like a rejected validation does (E-177); the
		// parameter schema makes port mandatory with WP-970.
		r := failResult(probewire.StatusError, probewire.ReasonConfig)
		r.Labels = map[string]string{LabelField: "params.port", LabelMessage: "port is required when the target has none"}
		return r
	}
	dial := e.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	start := time.Now()
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return failResult(probewire.StatusFail, tcpReason(ctx, err))
	}
	rtt := msSince(start)
	_ = conn.Close()
	return probewire.Result{Status: probewire.StatusOK, RTTMS: rtt}
}

// tcpAddress builds host:port from target and params.port; a target that
// already carries a port is used as is when params.port is absent.
func tcpAddress(spec *probewire.ProbeSpec) (string, bool) {
	var p tcpParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return "", false
		}
	}
	target := strings.TrimSpace(spec.Target)
	if target == "" {
		return "", false
	}
	if p.Port > 0 {
		host := target
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		return net.JoinHostPort(host, strconv.Itoa(p.Port)), true
	}
	if _, port, err := net.SplitHostPort(target); err == nil && port != "" {
		return target, true
	}
	return "", false
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
