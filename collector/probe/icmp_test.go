package probe

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// icmpSpec is a valid icmp spec of target with params.
func icmpSpec(target, params string) probewire.ProbeSpec {
	s := probewire.ProbeSpec{ID: "p-icmp", Type: TypeICMP, Target: target, IntervalMS: 30_000, TimeoutMS: 2_000,
		DownCount: 5, UpCount: 1, Epoch: 3}
	if params != "" {
		s.Params = json.RawMessage(params)
	}
	return s
}

// fakeMux returns a multiplexer on one fake socket of family.
func fakeMux(conn *fakeICMP) *Mux {
	var calls atomic.Int32
	return NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return conn, nil }))
}

func near(got, want, tolerance float64) bool { return math.Abs(got-want) <= tolerance }

// TestICMPLossAndRTT covers AK2 (PRT-02): 3 packets, the second lost →
// loss 1/3; min, avg and max of the replies and the jitter (mean absolute
// difference of consecutive RTTs).
func TestICMPLossAndRTT(t *testing.T) {
	// The summary itself, exact.
	r := icmpResult([]PacketResult{{RTT: 10 * time.Millisecond}, {Lost: true}, {RTT: 30 * time.Millisecond}})
	if r.Status != probewire.StatusOK || r.Loss == nil || !near(*r.Loss, 1.0/3, 1e-9) {
		t.Fatalf("summary: %+v", r)
	}
	if *r.RTTMS != 20 || r.Values[ValueRTTMinMS] != 10 || r.Values[ValueRTTMaxMS] != 30 || r.Values[ValueJitterMS] != 20 {
		t.Errorf("summary: rtt %v, values %v; want avg 20, min 10, max 30, jitter 20", *r.RTTMS, r.Values)
	}
	if r := icmpResult([]PacketResult{{Lost: true}, {Lost: true}}); r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout ||
		r.Loss == nil || *r.Loss != 1 {
		t.Errorf("no reply: %+v, want fail/timeout with loss 1", r)
	}

	// Through the executor and the multiplexer: the second request is
	// dropped, the others answered after 10 and 30 ms.
	conn := newFakeICMP(4, false)
	var n atomic.Int32
	conn.drop = func(netip.Addr, int) bool { return n.Add(1) == 2 }
	delays := map[int]time.Duration{}
	conn.delay = func(_ netip.Addr, seq int) time.Duration {
		if len(delays) == 0 {
			delays[seq] = 10 * time.Millisecond
		} else if _, ok := delays[seq]; !ok {
			delays[seq] = 30 * time.Millisecond
		}
		return delays[seq]
	}
	e := &ICMPExecutor{Mux: fakeMux(conn)}
	defer e.Mux.Close()
	swapExecutor(t, TypeICMP, e)
	spec := icmpSpec("192.0.2.10", `{"count":3,"spacing_ms":50}`)
	res := Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || res.Loss == nil || !near(*res.Loss, 1.0/3, 1e-9) {
		t.Fatalf("executor: %+v", res)
	}
	if lo, hi := res.Values[ValueRTTMinMS], res.Values[ValueRTTMaxMS]; lo < 10 || lo > 25 || hi < 30 || hi > 60 ||
		!near(*res.RTTMS, (lo+hi)/2, 1e-6) || !near(res.Values[ValueJitterMS], hi-lo, 1e-6) {
		t.Errorf("executor: rtt %v, values %v", *res.RTTMS, res.Values)
	}
	if err := res.Validate(); err != nil {
		t.Errorf("result does not validate: %v", err)
	}
	if res.ProbeID != "p-icmp" || res.Epoch != 3 {
		t.Errorf("result carries %s/%d", res.ProbeID, res.Epoch)
	}
	// All requests go out within the timeout: 3 × 1 s spacing at 1 s
	// timeout are spread over the timeout.
	conn.drop = nil
	spec = icmpSpec("192.0.2.11", `{"count":3}`)
	spec.TimeoutMS = 1000
	start := time.Now()
	res = Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || *res.Loss != 0 || time.Since(start) > 1500*time.Millisecond {
		t.Errorf("default spacing within a 1 s timeout: %+v after %v", res, time.Since(start))
	}
}

// TestICMPIPv6 covers AK3: ICMPv6 echo over the IPv6 socket.
func TestICMPIPv6(t *testing.T) {
	conn := newFakeICMP(6, false)
	var families []int
	var calls atomic.Int32
	m := NewMux(fakeListen(&calls, func(f int) (*fakeICMP, error) {
		families = append(families, f)
		return conn, nil
	}))
	defer m.Close()
	e := &ICMPExecutor{Mux: m}
	swapExecutor(t, TypeICMP, e)
	spec := icmpSpec("2001:db8::10", `{"count":2,"spacing_ms":10}`)
	res := Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || *res.Loss != 0 {
		t.Fatalf("IPv6: %+v", res)
	}
	if len(families) != 1 || families[0] != 6 {
		t.Errorf("sockets opened for families %v, want [6]", families)
	}
	// A host name resolves through the resolver.
	e.Resolve = func(context.Context, string) (netip.Addr, error) { return netip.MustParseAddr("2001:db8::11"), nil }
	spec = icmpSpec("router.example", `{"count":1}`)
	if res := Run(context.Background(), &spec, nil); res.Status != probewire.StatusOK {
		t.Errorf("host name: %+v", res)
	}
	e.Resolve = func(context.Context, string) (netip.Addr, error) { return netip.Addr{}, errors.New("nxdomain") }
	if res := Run(context.Background(), &spec, nil); res.Status != probewire.StatusFail || res.Code != probewire.ReasonUnreachable {
		t.Errorf("unresolvable host: %+v, want fail/unreachable", res)
	}
}

// TestICMPWithoutCapabilityIsUnknown covers AK3: without an ICMP socket of
// the family the result is error with icmp_unavailable (→ unknown).
func TestICMPWithoutCapabilityIsUnknown(t *testing.T) {
	var calls atomic.Int32
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return nil, errors.New("operation not permitted") }))
	e := &ICMPExecutor{Mux: m}
	swapExecutor(t, TypeICMP, e)
	spec := icmpSpec("192.0.2.20", "")
	res := Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusError || res.Code != probewire.ReasonICMPUnavailable {
		t.Fatalf("without capability: %+v, want error/icmp_unavailable", res)
	}
	if m.Available(4) {
		t.Error("Available(4) without a socket")
	}
	dir := t.TempDir()
	seq, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := OpenStateMachine(dir, seq)
	if err != nil {
		t.Fatal(err)
	}
	spec.DownCount = 1
	if _, _, err := sm.Observe(&spec, res); err != nil {
		t.Fatal(err)
	}
	if c, _ := sm.Counters(spec.ID); c.State != probewire.StateUnknown || c.Reason != probewire.ReasonICMPUnavailable {
		t.Errorf("state after icmp_unavailable: %s (%s), want unknown (icmp_unavailable)", c.State, c.Reason)
	}
}

// TestICMPPayloadLimits covers AK2: the payload is at most 1472 bytes over
// IPv4 and 1452 over IPv6.
func TestICMPPayloadLimits(t *testing.T) {
	for _, c := range []struct {
		family int
		target string
		size   int
		ok     bool
	}{
		{4, "192.0.2.30", 0, true}, {4, "192.0.2.30", 1472, true}, {4, "192.0.2.30", 1473, false},
		{6, "2001:db8::30", 1452, true}, {6, "2001:db8::30", 1453, false},
	} {
		conn := newFakeICMP(c.family, false)
		m := fakeMux(conn)
		res, err := m.Ping(context.Background(), netip.MustParseAddr(c.target), time.Second, 1, c.size, 0)
		if c.ok && (err != nil || res[0].Lost) {
			t.Errorf("IPv%d %d bytes: %+v, %v", c.family, c.size, res, err)
		}
		if !c.ok && !errors.Is(err, ErrICMPPayload) {
			t.Errorf("IPv%d %d bytes: %v, want ErrICMPPayload", c.family, c.size, err)
		}
		m.Close()
	}
	// The executor names the field.
	e := &ICMPExecutor{Mux: fakeMux(newFakeICMP(6, false))}
	defer e.Mux.Close()
	spec := icmpSpec("2001:db8::31", `{"payload_bytes":1460}`)
	res := e.Execute(context.Background(), spec, nil)
	if res.Status != probewire.StatusError || res.Code != probewire.ReasonConfig || res.Labels[LabelField] != "params.payload_bytes" {
		t.Errorf("1460 bytes over IPv6: %+v, want error/config on params.payload_bytes", res)
	}
}
