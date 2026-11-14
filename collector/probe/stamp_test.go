package probe

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakestamp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func startStamp(t *testing.T) *fakestamp.Reflector {
	t.Helper()
	r, err := fakestamp.Listen("127.0.0.1:0")
	if err != nil {
		t.Skipf("fakestamp: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func stampSpec(port int, params map[string]any) probewire.ProbeSpec {
	p := map[string]any{"port": port, "spacing_ms": 10}
	for k, v := range params {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	return probewire.ProbeSpec{
		ID: "stamp-1", Type: TypeSTAMP, Target: "127.0.0.1", IntervalMS: 60_000, TimeoutMS: 5_000,
		DownCount: 3, UpCount: 1, Params: raw, Epoch: 1,
	}
}

func runStamp(t *testing.T, e *STAMPExecutor, spec probewire.ProbeSpec) probewire.Result { //nolint:gocritic // test helper
	t.Helper()
	if errs := probewire.Validate(&spec); len(errs) > 0 {
		t.Fatalf("spec rejected: %v", errs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.TimeoutMS)*time.Millisecond)
	defer cancel()
	r := e.Execute(ctx, spec, nil)
	r.ProbeID = spec.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid wire result %+v: %v", r, err)
	}
	return r
}

func rttStat(r *probewire.Result, stat string) float64 {
	return r.Values[probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: stat})]
}

func oneWay(r *probewire.Result, dir string) (float64, bool) {
	v, ok := r.Values[probewire.ValueKey(ValueSTAMPOneWaySeconds, map[string]string{LabelSTAMPDirection: dir})]
	return v, ok
}

// TestSTAMPMetricsWithDelay covers AK3: a fixed delay of 20 ms gives RTT
// min/avg/max within a tolerance band.
func TestSTAMPMetricsWithDelay(t *testing.T) {
	refl := startStamp(t)
	refl.SetDelay(20 * time.Millisecond)
	r := runStamp(t, &STAMPExecutor{}, stampSpec(refl.Addr().Port, map[string]any{"count": 5}))
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("result = %+v", r)
	}
	lo, avg, hi := rttStat(&r, "min"), rttStat(&r, "avg"), rttStat(&r, "max")
	if lo < 0.019 || hi > 0.2 || lo > avg || avg > hi {
		t.Fatalf("rtt min/avg/max = %v/%v/%v, want about 20 ms", lo, avg, hi)
	}
	if r.Values[ValueSTAMPLossRatio] != 0 || r.Loss == nil || *r.Loss != 0 || r.RTTMS == nil {
		t.Fatalf("loss %v, rtt %v", r.Values[ValueSTAMPLossRatio], r.RTTMS)
	}
}

// TestSTAMPLossRatio covers AK3: 30 % loss at count 100.
func TestSTAMPLossRatio(t *testing.T) {
	refl := startStamp(t)
	refl.SetLoss(0.3)
	spec := stampSpec(refl.Addr().Port, map[string]any{"count": 100})
	spec.TimeoutMS = 3_000
	r := runStamp(t, &STAMPExecutor{}, spec)
	if r.Status != probewire.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	if got := r.Values[ValueSTAMPLossRatio]; got < 0.29 || got > 0.31 {
		t.Fatalf("loss = %v, want 0.3", got)
	}
}

// TestSTAMPAllLostIsFail covers AK5: 100 % loss is fail/timeout.
func TestSTAMPAllLostIsFail(t *testing.T) {
	refl := startStamp(t)
	refl.SetSilent(true)
	r := runStamp(t, &STAMPExecutor{}, stampSpec(refl.Addr().Port, map[string]any{"count": 3}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout || r.Loss == nil || *r.Loss != 1 {
		t.Fatalf("result = %+v, want fail/timeout", r)
	}
}

// TestSTAMPReorderAndDuplicatesCounted covers AK4: reordered answers are
// counted, duplicates counted and kept out of RTT and loss.
func TestSTAMPReorderAndDuplicatesCounted(t *testing.T) {
	refl := startStamp(t)
	refl.SetReorder(3)
	refl.SetDuplicate(4)
	r := runStamp(t, &STAMPExecutor{}, stampSpec(refl.Addr().Port, map[string]any{"count": 10, "spacing_ms": 20}))
	if r.Status != probewire.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	if r.Values[ValueSTAMPReordered] < 1 || r.Values[ValueSTAMPDuplicates] < 1 {
		t.Fatalf("reordered %v, duplicates %v", r.Values[ValueSTAMPReordered], r.Values[ValueSTAMPDuplicates])
	}
	// Every packet is answered (the held-back ones after the next); the
	// duplicates neither count as answers nor push the loss below zero.
	if got := r.Values[ValueSTAMPLossRatio]; got != 0 {
		t.Fatalf("loss = %v, want 0", got)
	}

	// RTT side, against the reflector: with a fixed delay and every
	// second answer sent twice the RTT stays in the band of the delay.
	refl.SetReorder(0)
	refl.SetDuplicate(2)
	refl.SetDelay(20 * time.Millisecond)
	r = runStamp(t, &STAMPExecutor{}, stampSpec(refl.Addr().Port, map[string]any{"count": 6}))
	if r.Status != probewire.StatusOK || r.Values[ValueSTAMPDuplicates] < 1 {
		t.Fatalf("result = %+v", r)
	}
	if lo, hi := rttStat(&r, "min"), rttStat(&r, "max"); lo < 0.019 || hi > 0.2 {
		t.Fatalf("rtt min/max = %v/%v with duplicates, want about 20 ms", lo, hi)
	}

	// Deterministic: the duplicates change neither RTT, jitter nor loss.
	base := time.Unix(1_800_000_000, 0)
	var replies []stampReply
	for i, rtt := range []time.Duration{10, 40, 20} {
		t1 := base.Add(time.Duration(i) * time.Second)
		t2 := t1.Add(time.Millisecond)
		replies = append(replies, stampReply{seq: uint32(i), t1: t1, t4: t1.Add(rtt * time.Millisecond), //nolint:gosec // test
			pkt: StampReflectedPacket{ReceiveTimestamp: ToNTP(t2), Timestamp: ToNTP(t2)}, ordered: true})
	}
	clean := stampEvaluate(replies, 0, &stampParams{Count: 3}, false)
	withDups := stampEvaluate(replies, 5, &stampParams{Count: 3}, false)
	for _, k := range []string{
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "min"}),
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "avg"}),
		probewire.ValueKey(ValueSTAMPRTTSeconds, map[string]string{LabelSTAMPStat: "max"}),
		ValueSTAMPJitterSeconds, ValueSTAMPLossRatio,
	} {
		if clean.Values[k] != withDups.Values[k] {
			t.Fatalf("%s = %v with duplicates, %v without", k, withDups.Values[k], clean.Values[k])
		}
	}
	if withDups.Values[ValueSTAMPDuplicates] != 5 || rttStat(&withDups, "max") != 0.04 || withDups.Values[ValueSTAMPLossRatio] != 0 {
		t.Fatalf("values = %v", withDups.Values)
	}
}

// TestSTAMPJitter covers AK3: the jitter is the mean absolute difference
// of consecutive RTTs.
func TestSTAMPJitter(t *testing.T) {
	replies := []stampReply{}
	base := time.Unix(1_800_000_000, 0)
	for i, rtt := range []time.Duration{10, 30, 20, 20} {
		t1 := base.Add(time.Duration(i) * time.Second)
		t2 := t1.Add(time.Millisecond)
		replies = append(replies, stampReply{seq: uint32(i), t1: t1, t4: t1.Add(rtt*time.Millisecond + 0),
			pkt: StampReflectedPacket{ReceiveTimestamp: ToNTP(t2), Timestamp: ToNTP(t2)}, ordered: true}) //nolint:gosec // test
	}
	r := stampEvaluate(replies, 0, &stampParams{Count: 4}, false)
	// |30-10| + |20-30| + |20-20| = 30 ms over 3 differences.
	if got := r.Values[ValueSTAMPJitterSeconds]; got < 0.0099 || got > 0.0101 {
		t.Fatalf("jitter = %v, want 0.010", got)
	}
	// Against the reflector: a constant delay gives a small jitter.
	refl := startStamp(t)
	refl.SetDelay(10 * time.Millisecond)
	res := runStamp(t, &STAMPExecutor{}, stampSpec(refl.Addr().Port, map[string]any{"count": 5}))
	if j, ok := res.Values[ValueSTAMPJitterSeconds]; !ok || j < 0 || j > 0.05 {
		t.Fatalf("jitter = %v (%v)", j, ok)
	}
}

// TestSTAMPOneWayOnlyWhenSynced covers AK3: one-way delays only with a
// synchronized collector and the S bit of the reflector.
func TestSTAMPOneWayOnlyWhenSynced(t *testing.T) {
	refl := startStamp(t)
	synced := &STAMPExecutor{TimeSync: func() string { return probewire.TimeSynchronized }}
	unsynced := &STAMPExecutor{TimeSync: func() string { return probewire.TimeUnknown }}
	spec := stampSpec(refl.Addr().Port, map[string]any{"count": 3})

	r := runStamp(t, synced, spec)
	if _, ok := oneWay(&r, "forward"); ok {
		t.Fatal("one-way value without S bit")
	}
	refl.SetSynchronized(true)
	r = runStamp(t, unsynced, spec)
	if _, ok := oneWay(&r, "forward"); ok {
		t.Fatal("one-way value without a synchronized collector")
	}
	refl.SetDelay(20 * time.Millisecond)
	r = runStamp(t, synced, spec)
	fwd, okF := oneWay(&r, "forward")
	bwd, okB := oneWay(&r, "backward")
	if !okF || !okB || bwd < 0.019 || fwd > bwd {
		t.Fatalf("one-way forward %v (%v), backward %v (%v)", fwd, okF, bwd, okB)
	}
}

// TestSTAMPThresholds covers AK5 and E-186: a warn threshold is ok with
// threshold_exceeded, a critical one fail with threshold_exceeded.
func TestSTAMPThresholds(t *testing.T) {
	refl := startStamp(t)
	refl.SetDelay(20 * time.Millisecond)
	port := refl.Addr().Port
	r := runStamp(t, &STAMPExecutor{}, stampSpec(port, map[string]any{"count": 3, "rtt_warn_ms": 5, "rtt_crit_ms": 500}))
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("warn: %+v", r)
	}
	r = runStamp(t, &STAMPExecutor{}, stampSpec(port, map[string]any{"count": 3, "rtt_warn_ms": 5, "rtt_crit_ms": 10}))
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("crit: %+v", r)
	}
	refl.SetDelay(0)
	refl.SetLoss(0.5)
	spec := stampSpec(port, map[string]any{"count": 4})
	spec.Thresholds = json.RawMessage(`{"loss_warn":0.1,"loss_crit":0.4}`)
	if r = runStamp(t, &STAMPExecutor{}, spec); r.Status != probewire.StatusFail || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("loss crit from thresholds: %+v", r)
	}

	// Boundary: a loss equal to loss_warn reaches the level, as in the
	// state machine (thresholdViolated: loss >= loss_warn).
	base := time.Unix(1_800_000_000, 0)
	var replies []stampReply
	for i := range 9 {
		t1 := base.Add(time.Duration(i) * time.Second)
		replies = append(replies, stampReply{seq: uint32(i), t1: t1, t4: t1.Add(5 * time.Millisecond), //nolint:gosec // test
			pkt: StampReflectedPacket{ReceiveTimestamp: ToNTP(t1), Timestamp: ToNTP(t1)}, ordered: true})
	}
	warn, crit := 0.1, 0.5
	p := stampParams{Count: 10, stampThresholds: stampThresholds{LossWarn: &warn, LossCrit: &crit}}
	r = stampEvaluate(replies, 0, &p, false)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded || r.Labels["threshold"] != "loss" {
		t.Fatalf("loss at loss_warn: %+v", r)
	}
	rttWarn := 5.0
	p = stampParams{Count: 9, stampThresholds: stampThresholds{RTTWarnMS: &rttWarn}}
	if r = stampEvaluate(replies, 0, &p, false); r.Code != "" {
		t.Fatalf("rtt equal to rtt_warn_ms must not exceed it: %+v", r)
	}

	// Send parameters in thresholds are ignored: count stays that of the
	// params, so the send window fits the timeout.
	refl.SetLoss(0)
	spec = stampSpec(port, map[string]any{"count": 3})
	spec.TimeoutMS = 2_000
	spec.Thresholds = json.RawMessage(`{"count":100,"spacing_ms":100}`)
	if r = runStamp(t, &STAMPExecutor{}, spec); r.Status != probewire.StatusOK || r.Values[ValueSTAMPLossRatio] != 0 {
		t.Fatalf("count from thresholds: %+v", r)
	}
}

// TestSTAMPPortUnreachable covers AK5: ICMP port unreachable is
// fail/refused.
func TestSTAMPPortUnreachable(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	r := runStamp(t, &STAMPExecutor{}, stampSpec(port, map[string]any{"count": 2}))
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonRefused {
		t.Fatalf("result = %+v, want fail/refused", r)
	}
}

// TestSTAMPPacketRoundTrip covers AK1: 44 bytes and size 1472, the
// reflector format and the NTP timestamps.
func TestSTAMPPacketRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	for _, size := range []int{StampPacketLen, StampMaxPacket} {
		p := StampTestPacket{Seq: 42, Timestamp: ToNTP(now), ErrEstimate: StampErrorEstimate(true)}
		b, err := p.Encode(size)
		if err != nil || len(b) != size {
			t.Fatalf("size %d: %d bytes, %v", size, len(b), err)
		}
		if !slices.Equal(b[14:StampPacketLen], make([]byte, StampPacketLen-14)) {
			t.Fatal("MBZ not zero")
		}
		got, err := DecodeStampTestPacket(b)
		if err != nil || got != p || !StampSynchronized(got.ErrEstimate) {
			t.Fatalf("decoded %+v, %v", got, err)
		}
	}
	if _, err := (&StampTestPacket{}).Encode(43); err == nil {
		t.Fatal("43 bytes accepted")
	}
	if _, err := (&StampTestPacket{}).Encode(1473); err == nil {
		t.Fatal("1473 bytes accepted")
	}
	if d := ToNTP(now).Time().Sub(now); d < -time.Microsecond || d > time.Microsecond {
		t.Fatalf("NTP round trip off by %v", d)
	}
	rp := StampReflectedPacket{Seq: 1, Timestamp: ToNTP(now), ErrEstimate: 1, ReceiveTimestamp: ToNTP(now.Add(-time.Millisecond)),
		SenderSeq: 42, SenderTimestamp: ToNTP(now.Add(-2 * time.Millisecond)), SenderErrEstimate: 0x8001, SenderTTL: 255}
	b := rp.Encode(StampPacketLen)
	if got, err := DecodeStampReflectedPacket(b); err != nil || got != rp {
		t.Fatalf("reflected %+v, %v", got, err)
	}
	if _, err := DecodeStampReflectedPacket(b[:43]); err == nil {
		t.Fatal("short reflected packet accepted")
	}
}

// TestSTAMPLimits covers AK2, AK6 and PRT-21 (limits test): field errors
// of probewire.Validate.
func TestSTAMPLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeSTAMP)
	if !ok || !typ.Available() || typ.MinIntervalS != 30 || typ.DefaultIntervalS != 60 || typ.MaxTimeoutS != 10 ||
		typ.MaxPayloadBytes != 1472 || typ.MaxParallelPerTarget != 1 {
		t.Fatalf("register entry = %+v", typ)
	}
	for name, c := range map[string]struct {
		params map[string]any
		field  string
	}{
		"count 101":     {map[string]any{"count": 101}, "params.count"},
		"spacing_ms 9":  {map[string]any{"spacing_ms": 9}, "params.spacing_ms"},
		"size 1473":     {map[string]any{"size": 1473}, "params.size"},
		"dscp 64":       {map[string]any{"dscp": 64}, "params.dscp"},
		"window > 5 s":  {map[string]any{"count": 50, "spacing_ms": 100}, "params.count"},
		"loss_crit > 1": {map[string]any{"loss_crit": 1.5}, "params.loss_crit"},
	} {
		spec := stampSpec(862, c.params)
		errs := probewire.Validate(&spec)
		if !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == c.field }) {
			t.Errorf("%s: errs = %v, want %s", name, errs, c.field)
		}
	}
	spec := stampSpec(862, map[string]any{"dscp": 46, "size": 1472})
	spec.IntervalMS = 29_000
	if errs := probewire.Validate(&spec); !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == "interval_s" }) {
		t.Errorf("interval 29 s: %v", errs)
	}
}
