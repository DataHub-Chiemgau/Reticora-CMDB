package probe

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeSTAMP is the register key of the STAMP probe.
const TypeSTAMP = "stamp"

// Value and label names of a stamp result (WP-1013 AK3, AK4).
const (
	ValueSTAMPRTTSeconds    = "probe_stamp_rtt_seconds"
	ValueSTAMPJitterSeconds = "probe_stamp_jitter_seconds"
	ValueSTAMPLossRatio     = "probe_stamp_loss_ratio"
	ValueSTAMPOneWaySeconds = "probe_stamp_oneway_seconds"
	ValueSTAMPReordered     = "probe_stamp_reordered"
	ValueSTAMPDuplicates    = "probe_stamp_duplicates"
	LabelSTAMPStat          = "stat"
	LabelSTAMPDirection     = "direction"
)

// stampReplyWait is how long the sender waits for answers after the last
// test packet; probewire.Validate keeps (count - 1) x spacing_ms plus this
// wait below the timeout.
const stampReplyWait = time.Second

func init() { Register(TypeSTAMP, &STAMPExecutor{}) }

// STAMPExecutor is the probe type stamp (PRT-14, WP-1013, E-191): a
// Session-Sender after RFC 8762 in unauthenticated mode, compatible with
// TWAMP-Light reflectors. It sends count test packets of size bytes every
// spacing_ms to the target's port (DSCP as configured) and evaluates the
// answers: RTT per packet (T4 - T1) - (T3 - T2), min/avg/max, jitter (mean
// absolute difference of consecutive RTTs in sequence order), loss,
// reordering and duplicates (duplicates count for neither RTT nor loss).
// The one-way delays are reported only when the collector clock is
// synchronized (WP-966) and the reflector sets the S bit.
//
// Outcomes: every packet lost is fail/timeout, ICMP port unreachable
// without any answer fail/refused; a violated warn threshold is ok with
// threshold_exceeded (→ degraded), a violated critical threshold fail with
// threshold_exceeded (E-186). TWAMP-Control (RFC 5357) is not part of the
// type (WP-1014).
type STAMPExecutor struct {
	// TimeSync returns the synchronization of the collector clock (nil:
	// the capability report, CurrentReport).
	TimeSync func() string
}

type stampParams struct {
	Port      int `json:"port"`
	Count     int `json:"count"`
	SpacingMS int `json:"spacing_ms"`
	Size      int `json:"size"`
	DSCP      int `json:"dscp"`
	stampThresholds
}

// stampThresholds are the levels of the stamp type; only they are taken
// from the thresholds of the spec, the send parameters (count, spacing_ms,
// port, size, dscp) always come from params, as the validation of the send
// window assumes.
type stampThresholds struct {
	LossWarn     *float64 `json:"loss_warn"`
	LossCrit     *float64 `json:"loss_crit"`
	RTTWarnMS    *float64 `json:"rtt_warn_ms"`
	RTTCritMS    *float64 `json:"rtt_crit_ms"`
	JitterWarnMS *float64 `json:"jitter_warn_ms"`
	JitterCritMS *float64 `json:"jitter_crit_ms"`
}

// override sets every level that th carries.
func (t *stampThresholds) override(th *stampThresholds) {
	for _, f := range []struct{ dst, src **float64 }{
		{&t.LossWarn, &th.LossWarn}, {&t.LossCrit, &th.LossCrit},
		{&t.RTTWarnMS, &th.RTTWarnMS}, {&t.RTTCritMS, &th.RTTCritMS},
		{&t.JitterWarnMS, &th.JitterWarnMS}, {&t.JitterCritMS, &th.JitterCritMS},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
}

// stampReply is one answer as the sender saw it.
type stampReply struct {
	seq     uint32
	t1, t4  time.Time
	pkt     StampReflectedPacket
	ordered bool
}

// Execute implements Executor.
func (e *STAMPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p := stampParams{Port: 862, Count: 10, SpacingMS: 100, Size: StampPacketLen}
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	// Thresholds of the spec override those of the params (E-186).
	if len(spec.Thresholds) > 0 {
		var th stampThresholds
		if err := json.Unmarshal(spec.Thresholds, &th); err != nil {
			return configResult("thresholds", "thresholds cannot be decoded")
		}
		p.override(&th)
	}
	switch {
	case p.Count < 1 || p.Count > 100:
		return configResult("params.count", "must be between 1 and 100")
	case p.SpacingMS < 10 || p.SpacingMS > 1000:
		return configResult("params.spacing_ms", "must be between 10 and 1000")
	case p.Size < StampPacketLen || p.Size > StampMaxPacket:
		return configResult("params.size", "must be between 44 and 1472")
	case p.DSCP < 0 || p.DSCP > 63:
		return configResult("params.dscp", "must be between 0 and 63")
	}
	addr, ok := tcpAddress(spec.Target, p.Port)
	if !ok {
		return configResult("target", "the target must be the address of the reflector")
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return failResult(probewire.StatusFail, dnsReason(ctx, err))
	}
	conn := c.(*net.UDPConn)
	defer func() { _ = conn.Close() }()
	if derr := stampSetDSCP(conn, p.DSCP); derr != nil {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	synced := e.synchronized()

	replies, dups, refused, err := stampExchange(ctx, conn, &p, synced)
	if err != nil {
		return failResult(probewire.StatusError, probewire.ReasonConfig)
	}
	if len(replies) == 0 {
		code := probewire.ReasonTimeout
		if refused {
			code = probewire.ReasonRefused
		}
		r := failResult(probewire.StatusFail, code)
		loss := 1.0
		r.Loss = &loss
		return r
	}
	return stampEvaluate(replies, dups, &p, synced)
}

// synchronized reports whether the collector clock is synchronized.
func (e *STAMPExecutor) synchronized() bool {
	if e.TimeSync != nil {
		return e.TimeSync() == probewire.TimeSynchronized
	}
	r := CurrentReport()
	return r != nil && r.TimeSync == probewire.TimeSynchronized
}

// stampSetDSCP sets the DSCP of the socket (TOS resp. traffic class).
func stampSetDSCP(conn *net.UDPConn, dscp int) error {
	if dscp == 0 {
		return nil
	}
	remote, _ := conn.RemoteAddr().(*net.UDPAddr)
	if remote != nil && remote.IP.To4() == nil {
		return ipv6.NewConn(conn).SetTrafficClass(dscp << 2)
	}
	return ipv4.NewConn(conn).SetTOS(dscp << 2)
}

// stampExchange sends the test packets and collects the answers until all
// arrived, the reply wait after the last packet ended or ctx ends.
func stampExchange(ctx context.Context, conn *net.UDPConn, p *stampParams, synced bool) (replies []stampReply, dups int, refused bool, err error) {
	var mu sync.Mutex
	sent := make(map[uint32]time.Time, p.Count)
	var lastSent time.Time
	allSent := make(chan struct{})
	errEst := StampErrorEstimate(synced)

	sendCtx, stopSend := context.WithCancel(ctx)
	defer stopSend()
	var sendErr error
	go func() {
		defer close(allSent)
		for i := 0; i < p.Count; i++ {
			if i > 0 {
				select {
				case <-sendCtx.Done():
					return
				case <-time.After(time.Duration(p.SpacingMS) * time.Millisecond):
				}
			}
			now := time.Now()
			pkt := StampTestPacket{Seq: uint32(i), Timestamp: ToNTP(now), ErrEstimate: errEst} //nolint:gosec // at most 100
			b, encErr := pkt.Encode(p.Size)
			if encErr != nil {
				mu.Lock()
				sendErr = encErr
				mu.Unlock()
				return
			}
			mu.Lock()
			sent[pkt.Seq] = now
			lastSent = now
			mu.Unlock()
			// A write error (ICMP port unreachable of an earlier packet)
			// shows up again on the read side.
			_, _ = conn.Write(b)
		}
	}()

	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	seen := map[uint32]bool{}
	var highest uint32
	buf := make([]byte, 2048)
	done := false
	for !done {
		// Until all packets are out, read with a short deadline so the
		// reply wait can start from the last packet.
		select {
		case <-allSent:
			mu.Lock()
			last := lastSent
			mu.Unlock()
			if last.IsZero() {
				last = time.Now()
			}
			_ = conn.SetReadDeadline(stampEarlier(ctx, last.Add(stampReplyWait)))
		default:
			_ = conn.SetReadDeadline(stampEarlier(ctx, time.Now().Add(50*time.Millisecond)))
		}
		if ctx.Err() != nil {
			break
		}
		n, rerr := conn.Read(buf)
		t4 := time.Now()
		if rerr != nil {
			switch {
			case errors.Is(rerr, syscall.ECONNREFUSED):
				refused = true
				continue
			case errors.Is(rerr, os.ErrDeadlineExceeded):
				select {
				case <-allSent:
					mu.Lock()
					last := lastSent
					mu.Unlock()
					if !time.Now().Before(last.Add(stampReplyWait)) || ctx.Err() != nil || last.IsZero() {
						done = true
					}
				default:
				}
				continue
			default:
				done = true
				continue
			}
		}
		pkt, derr := DecodeStampReflectedPacket(buf[:n])
		if derr != nil {
			continue
		}
		mu.Lock()
		t1, ok := sent[pkt.SenderSeq]
		mu.Unlock()
		if !ok {
			continue // not one of ours
		}
		if seen[pkt.SenderSeq] {
			dups++
			continue
		}
		seen[pkt.SenderSeq] = true
		ordered := len(replies) == 0 || pkt.SenderSeq > highest
		if pkt.SenderSeq > highest || len(replies) == 0 {
			highest = pkt.SenderSeq
		}
		replies = append(replies, stampReply{seq: pkt.SenderSeq, t1: t1, t4: t4, pkt: pkt, ordered: ordered})
		if len(replies) == p.Count {
			done = true
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return replies, dups, refused, sendErr
}

// stampEarlier returns t or the deadline of ctx, whichever is earlier.
func stampEarlier(ctx context.Context, t time.Time) time.Time {
	if dl, ok := ctx.Deadline(); ok && dl.Before(t) {
		return dl
	}
	return t
}

// stampRTT is (T4 - T1) - (T3 - T2), at least 0.
func stampRTT(r *stampReply) time.Duration {
	rtt := r.t4.Sub(r.t1) - r.pkt.Timestamp.Time().Sub(r.pkt.ReceiveTimestamp.Time())
	return max(rtt, 0)
}

// stampEvaluate computes the measurements and the result (AK3 to AK5).
func stampEvaluate(replies []stampReply, dups int, p *stampParams, synced bool) probewire.Result {
	reordered := 0
	for i := range replies {
		if !replies[i].ordered {
			reordered++
		}
	}
	bySeq := append([]stampReply(nil), replies...)
	sort.Slice(bySeq, func(i, j int) bool { return bySeq[i].seq < bySeq[j].seq })
	rtts := make([]float64, len(bySeq))
	lo, hi, sum := math.Inf(1), math.Inf(-1), 0.0
	for i := range bySeq {
		v := stampRTT(&bySeq[i]).Seconds()
		rtts[i] = v
		lo, hi, sum = math.Min(lo, v), math.Max(hi, v), sum+v
	}
	avg := sum / float64(len(rtts))
	jitter := 0.0
	if len(rtts) > 1 {
		for i := 1; i < len(rtts); i++ {
			jitter += math.Abs(rtts[i] - rtts[i-1])
		}
		jitter /= float64(len(rtts) - 1)
	}
	loss := float64(p.Count-len(replies)) / float64(p.Count)

	values := map[string]float64{
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "min"}): lo,
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "avg"}): avg,
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "max"}): hi,
		ValueSTAMPJitterSeconds: jitter,
		ValueSTAMPLossRatio:     loss,
		ValueSTAMPReordered:     float64(reordered),
		ValueSTAMPDuplicates:    float64(dups),
	}
	if synced {
		// One-way delays only from answers whose reflector is synchronized
		// as well (S bit).
		var fwd, bwd float64
		n := 0
		for i := range bySeq {
			r := &bySeq[i]
			if !StampSynchronized(r.pkt.ErrEstimate) {
				continue
			}
			fwd += r.pkt.ReceiveTimestamp.Time().Sub(r.t1).Seconds()
			bwd += r.t4.Sub(r.pkt.Timestamp.Time()).Seconds()
			n++
		}
		if n > 0 {
			values[probewire.ValueKey(ValueSTAMPOneWaySeconds, map[string]string{LabelSTAMPDirection: "forward"})] = fwd / float64(n)
			values[probewire.ValueKey(ValueSTAMPOneWaySeconds, map[string]string{LabelSTAMPDirection: "backward"})] = bwd / float64(n)
		}
	}
	avgMS := avg * 1000
	r := probewire.Result{Status: probewire.StatusOK, RTTMS: &avgMS, Loss: &loss, Values: values}

	var warn, crit []string
	// The comparison is that of the state machine (thresholdViolated):
	// a loss reaches its level at equality, a time must exceed it.
	check := func(name string, v float64, w, c *float64, reached func(v, level float64) bool) {
		switch {
		case c != nil && reached(v, *c):
			crit = append(crit, name)
		case w != nil && reached(v, *w):
			warn = append(warn, name)
		}
	}
	atLeast := func(v, level float64) bool { return v >= level }
	above := func(v, level float64) bool { return v > level }
	check("loss", loss, p.LossWarn, p.LossCrit, atLeast)
	check("rtt", avg*1000, p.RTTWarnMS, p.RTTCritMS, above)
	check("jitter", jitter*1000, p.JitterWarnMS, p.JitterCritMS, above)
	switch {
	case len(crit) > 0:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnThresholdExceeded
		r.Labels = map[string]string{"threshold": strings.Join(crit, ",")}
	case len(warn) > 0:
		r.Code = probewire.WarnThresholdExceeded
		r.Labels = map[string]string{"threshold": strings.Join(warn, ",")}
	}
	return r
}
