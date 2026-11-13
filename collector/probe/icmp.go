package probe

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeICMP is the register key of the ICMP ping probe.
const TypeICMP = "icmp"

// Value keys of an icmp result besides rtt_ms (the average) and loss.
const (
	ValueRTTMinMS = "rtt_min_ms"
	ValueRTTMaxMS = "rtt_max_ms"
	ValueJitterMS = "jitter_ms"
)

// Defaults of the parameters of the type icmp (api/probe-types.json, E-177).
const (
	icmpDefaultCount   = 3
	icmpDefaultSpacing = time.Second
	icmpDefaultPayload = 56
)

// icmpMargin ends the wait for replies this long before the deadline of
// the check, so the executor reports its loss before Run reports the
// deadline as timeout.
const icmpMargin = 20 * time.Millisecond

func init() { Register(TypeICMP, &ICMPExecutor{}) }

// ICMPExecutor is the probe type icmp (PRT-02, WP-953): per check it sends
// count echo requests (default 3) spacing_ms apart (default 1 s, shortened
// so that all go out within the timeout) over the shared socket of the
// family (Mux) and reports rtt_ms (average of the replies), values
// rtt_min_ms, rtt_max_ms and jitter_ms (mean absolute difference of
// consecutive round trip times) and loss (the share of requests without
// reply). ok with at least one reply, else fail with code timeout; a
// target that does not resolve is fail with unreachable; without an ICMP
// socket of the family error with icmp_unavailable (→ unknown). degraded
// over rtt_warn_ms or loss_warn is derived by the state machine (WP-952).
type ICMPExecutor struct {
	// Mux replaces the multiplexer of the process (tests); nil uses
	// DefaultMux.
	Mux *Mux
	// Resolve replaces the name resolution (tests); nil uses the system
	// resolver.
	Resolve func(ctx context.Context, host string) (netip.Addr, error)
}

type icmpParams struct {
	Count        *int `json:"count"`
	SpacingMS    *int `json:"spacing_ms"`
	PayloadBytes *int `json:"payload_bytes"`
}

// Execute implements Executor.
func (e *ICMPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p icmpParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	count, spacing, size := icmpDefaultCount, icmpDefaultSpacing, icmpDefaultPayload
	if p.Count != nil {
		count = *p.Count
	}
	if p.SpacingMS != nil {
		spacing = time.Duration(*p.SpacingMS) * time.Millisecond
	}
	if p.PayloadBytes != nil {
		size = *p.PayloadBytes
	}
	if count < 1 || count > 5 {
		return configResult("params.count", "count must be between 1 and 5")
	}
	ip, err := e.resolve(ctx, spec.Target)
	if err != nil {
		return failResult(probewire.StatusFail, probewire.ReasonUnreachable)
	}
	if ip.Is6() && !ip.Is4In6() && size > MaxICMPPayloadV6 {
		return configResult("params.payload_bytes", "payload_bytes must be at most "+strconv.Itoa(MaxICMPPayloadV6)+" over IPv6")
	}
	timeout := time.Duration(spec.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if d, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(d))
	}
	timeout = max(timeout-icmpMargin, time.Millisecond)
	// Every request goes out within the timeout (E-177).
	spacing = min(spacing, timeout/time.Duration(count))
	mux := e.Mux
	if mux == nil {
		mux = DefaultMux()
	}
	packets, err := mux.Ping(ctx, ip, timeout, count, size, spacing)
	switch {
	case errors.Is(err, ErrICMPUnavailable):
		return failResult(probewire.StatusError, probewire.ReasonICMPUnavailable)
	case errors.Is(err, ErrICMPPayload):
		return configResult("params.payload_bytes", "payload_bytes exceeds the limit of the address family")
	case err != nil:
		return failResult(probewire.StatusFail, probewire.ReasonUnreachable)
	}
	return icmpResult(packets)
}

// resolve returns the address of target: an IP address as it is, a host
// name through the resolver (the first address).
func (e *ICMPExecutor) resolve(ctx context.Context, target string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(target); err == nil {
		return ip.Unmap(), nil
	}
	if e.Resolve != nil {
		return e.Resolve(ctx, target)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", target)
	if err != nil || len(addrs) == 0 {
		return netip.Addr{}, errors.New("probe: target does not resolve")
	}
	for _, a := range addrs {
		if a.Unmap().Is4() {
			return a.Unmap(), nil
		}
	}
	return addrs[0], nil
}

// icmpResult summarizes the packets of a check (WP-953 AK2).
func icmpResult(packets []PacketResult) probewire.Result {
	if len(packets) == 0 {
		return failResult(probewire.StatusFail, probewire.ReasonTimeout)
	}
	var rtts []float64
	for _, p := range packets {
		if !p.Lost {
			rtts = append(rtts, float64(p.RTT.Microseconds())/1000)
		}
	}
	loss := float64(len(packets)-len(rtts)) / float64(len(packets))
	if len(rtts) == 0 {
		r := failResult(probewire.StatusFail, probewire.ReasonTimeout)
		r.Loss = &loss
		return r
	}
	lo, hi, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, v := range rtts {
		lo, hi, sum = math.Min(lo, v), math.Max(hi, v), sum+v
	}
	jitter := 0.0
	if len(rtts) > 1 {
		for i := 1; i < len(rtts); i++ {
			jitter += math.Abs(rtts[i] - rtts[i-1])
		}
		jitter /= float64(len(rtts) - 1)
	}
	avg := sum / float64(len(rtts))
	return probewire.Result{Status: probewire.StatusOK, RTTMS: &avg, Loss: &loss,
		Values: map[string]float64{ValueRTTMinMS: lo, ValueRTTMaxMS: hi, ValueJitterMS: jitter}}
}
