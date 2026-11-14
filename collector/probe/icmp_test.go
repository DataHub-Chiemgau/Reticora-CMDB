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

// newStateMachine opens a state machine in a temporary directory.
func newStateMachine(t *testing.T) *StateMachine {
	t.Helper()
	dir := t.TempDir()
	seq, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := OpenStateMachine(dir, seq)
	if err != nil {
		t.Fatal(err)
	}
	return sm
}

// TestICMPResultStates covers WP-969 AK4 and AK5 (PRT-02, MON-04): ok with
// at least one reply, fail at 100 % loss, degraded over loss_warn and
// rtt_warn_ms in the state machine, error/icmp_unavailable without a
// socket of the family (unknown, never down).
func TestICMPResultStates(t *testing.T) {
	conn := newFakeICMP(4, false)
	e := &ICMPExecutor{Mux: fakeMux(conn)}
	defer e.Mux.Close()
	swapExecutor(t, TypeICMP, e)
	run := func(spec *probewire.ProbeSpec) probewire.Result {
		t.Helper()
		r := Run(context.Background(), spec, nil)
		if err := r.Validate(); err != nil {
			t.Fatalf("result does not validate: %v", err)
		}
		return r
	}
	observe := func(sm *StateMachine, spec *probewire.ProbeSpec, r probewire.Result) ProbeCounters {
		t.Helper()
		if _, _, err := sm.Observe(spec, r); err != nil {
			t.Fatal(err)
		}
		c, _ := sm.Counters(spec.ID)
		return c
	}

	// ok: every reply.
	spec := icmpSpec("192.0.2.10", `{"count":2,"spacing_ms":200}`)
	spec.DownCount = 1
	ok := run(&spec)
	if ok.Status != probewire.StatusOK || *ok.Loss != 0 || ok.Code != "" {
		t.Fatalf("all replies: %+v, want ok without loss", ok)
	}
	if c := observe(newStateMachine(t), &spec, ok); c.State != probewire.StateUp {
		t.Errorf("all replies: state %s, want up", c.State)
	}

	// fail: 100 % loss.
	conn.drop = func(netip.Addr, int) bool { return true }
	lost := run(&spec)
	if lost.Status != probewire.StatusFail || lost.Code != probewire.ReasonTimeout || *lost.Loss != 1 {
		t.Fatalf("no reply: %+v, want fail/timeout with loss 1", lost)
	}
	if c := observe(newStateMachine(t), &spec, lost); c.State != probewire.StateDown {
		t.Errorf("no reply (down_count 1): state %s, want down", c.State)
	}

	// degraded: one of three lost is at loss_warn 0.3.
	var n atomic.Int32
	conn.drop = func(netip.Addr, int) bool { return n.Add(1)%3 == 2 }
	spec = icmpSpec("192.0.2.11", `{"count":3,"spacing_ms":200,"loss_warn":0.3}`)
	spec.DownCount = 1
	partial := run(&spec)
	if partial.Status != probewire.StatusOK || !near(*partial.Loss, 1.0/3, 1e-9) {
		t.Fatalf("one of three lost: %+v, want ok with loss 1/3", partial)
	}
	if c := observe(newStateMachine(t), &spec, partial); c.State != probewire.StateDegraded || c.Reason != probewire.WarnThresholdExceeded {
		t.Errorf("loss 1/3 at loss_warn 0.3: %s (%s), want degraded (threshold_exceeded)", c.State, c.Reason)
	}
	// The default loss_warn 0.5 keeps the same loss up.
	spec.Params = json.RawMessage(`{"count":3,"spacing_ms":200}`)
	if c := observe(newStateMachine(t), &spec, partial); c.State != probewire.StateUp {
		t.Errorf("loss 1/3 at the default loss_warn 0.5: %s, want up", c.State)
	}

	// degraded: the average RTT above rtt_warn_ms.
	conn.drop = nil
	conn.delay = func(netip.Addr, int) time.Duration { return 30 * time.Millisecond }
	spec = icmpSpec("192.0.2.12", `{"count":1,"rtt_warn_ms":5}`)
	spec.DownCount = 1
	slow := run(&spec)
	if slow.Status != probewire.StatusOK || *slow.RTTMS < 25 {
		t.Fatalf("slow replies: %+v", slow)
	}
	if c := observe(newStateMachine(t), &spec, slow); c.State != probewire.StateDegraded {
		t.Errorf("rtt %.1f ms at rtt_warn_ms 5: %s, want degraded", *slow.RTTMS, c.State)
	}
	conn.delay = nil

	// error/icmp_unavailable without a socket of the family: unknown.
	var calls atomic.Int32
	m := NewMux(fakeListen(&calls, func(int) (*fakeICMP, error) { return nil, errors.New("operation not permitted") }))
	swapExecutor(t, TypeICMP, &ICMPExecutor{Mux: m})
	spec = icmpSpec("192.0.2.20", `{"count":1}`)
	spec.DownCount = 1
	res := run(&spec)
	if res.Status != probewire.StatusError || res.Code != probewire.ReasonICMPUnavailable {
		t.Fatalf("without capability: %+v, want error/icmp_unavailable", res)
	}
	if m.Available(4) {
		t.Error("Available(4) without a socket")
	}
	sm := newStateMachine(t)
	for range 3 {
		if c := observe(sm, &spec, res); c.State != probewire.StateUnknown || c.Reason != probewire.ReasonICMPUnavailable {
			t.Fatalf("state after icmp_unavailable: %s (%s), want unknown (icmp_unavailable)", c.State, c.Reason)
		}
	}
}

// TestICMPLossAndJitter covers WP-969 AK4: loss quota, RTT min/avg/max
// and jitter (mean absolute difference of consecutive RTTs, from two
// replies on) with 1, 3 and 5 packets, in the summary and through the
// executor and the multiplexer.
func TestICMPLossAndJitter(t *testing.T) {
	ms := func(v int) PacketResult { return PacketResult{RTT: time.Duration(v) * time.Millisecond} }
	lostPkt := PacketResult{Lost: true}
	for _, c := range []struct {
		name                  string
		packets               []PacketResult
		loss, avg, lo, hi     float64
		jitter                float64
		hasJitter, ok, failed bool
	}{
		{"1 packet answered", []PacketResult{ms(12)}, 0, 12, 12, 12, 0, false, true, false},
		{"1 packet lost", []PacketResult{lostPkt}, 1, 0, 0, 0, 0, false, false, true},
		{"3 packets, second lost", []PacketResult{ms(10), lostPkt, ms(30)}, 1.0 / 3, 20, 10, 30, 20, true, true, false},
		{"3 packets answered", []PacketResult{ms(10), ms(14), ms(12)}, 0, 12, 10, 14, 3, true, true, false},
		{"5 packets, two lost", []PacketResult{ms(10), lostPkt, ms(20), lostPkt, ms(14)}, 0.4, 44.0 / 3, 10, 20, 8, true, true, false},
		{"5 packets answered", []PacketResult{ms(10), ms(20), ms(10), ms(20), ms(10)}, 0, 14, 10, 20, 10, true, true, false},
		{"5 packets lost", []PacketResult{lostPkt, lostPkt, lostPkt, lostPkt, lostPkt}, 1, 0, 0, 0, 0, false, false, true},
	} {
		r := icmpResult(c.packets)
		if r.Loss == nil || !near(*r.Loss, c.loss, 1e-9) {
			t.Errorf("%s: loss %v, want %v", c.name, r.Loss, c.loss)
			continue
		}
		if c.failed {
			if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout || r.RTTMS != nil {
				t.Errorf("%s: %+v, want fail/timeout without rtt", c.name, r)
			}
			continue
		}
		if r.Status != probewire.StatusOK || !near(*r.RTTMS, c.avg, 1e-9) ||
			r.Values[ValueRTTMinMS] != c.lo || r.Values[ValueRTTMaxMS] != c.hi {
			t.Errorf("%s: %+v (values %v), want avg %v min %v max %v", c.name, r, r.Values, c.avg, c.lo, c.hi)
		}
		j, has := r.Values[ValueJitterMS]
		if has != c.hasJitter || !near(j, c.jitter, 1e-9) {
			t.Errorf("%s: jitter %v (%v), want %v (%v)", c.name, j, has, c.jitter, c.hasJitter)
		}
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
	spec := icmpSpec("192.0.2.10", `{"count":3,"spacing_ms":200}`)
	res := Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || res.Loss == nil || !near(*res.Loss, 1.0/3, 1e-9) {
		t.Fatalf("executor: %+v", res)
	}
	if lo, hi := res.Values[ValueRTTMinMS], res.Values[ValueRTTMaxMS]; lo < 10 || lo > 25 || hi < 30 || hi > 60 ||
		!near(*res.RTTMS, (lo+hi)/2, 1e-6) || !near(res.Values[ValueJitterMS], hi-lo, 1e-6) {
		t.Errorf("executor: rtt %v, values %v", *res.RTTMS, res.Values)
	}
	if res.ProbeID != "p-icmp" || res.Epoch != 3 {
		t.Errorf("result carries %s/%d", res.ProbeID, res.Epoch)
	}
	if got := conn.sent.Load(); got != 3 {
		t.Errorf("%d echo requests, want count 3", got)
	}
	// Five requests 200 ms apart all go out within the timeout.
	conn.drop, conn.delay = nil, nil
	spec = icmpSpec("192.0.2.11", `{"count":5,"spacing_ms":200}`)
	spec.TimeoutMS = 1000
	start := time.Now()
	res = Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || *res.Loss != 0 || time.Since(start) > 1500*time.Millisecond {
		t.Errorf("5 × 200 ms within a 1 s timeout: %+v after %v", res, time.Since(start))
	}
}

// TestICMPPayloadAndFamily covers WP-969 AK1 and AK3: the payload size
// goes onto the wire (at most 1472 bytes over IPv4, 1452 over IPv6), the
// family picks the address: auto the family of a literal and IPv6 first
// for a host name, v4 and v6 only their family; a literal of the other
// family is a field error.
func TestICMPPayloadAndFamily(t *testing.T) {
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

	// size on the wire, default 56; below 8 bytes the request still
	// carries the 8-byte token of the multiplexer (WP-953 AK5).
	conn4 := newFakeICMP(4, false)
	e := &ICMPExecutor{Mux: fakeMux(conn4)}
	defer e.Mux.Close()
	swapExecutor(t, TypeICMP, e)
	for params, want := range map[string]int64{`{"count":1}`: 56, `{"count":1,"size":1000}`: 1000, `{"count":1,"size":7}`: 8, `{"count":1,"size":0}`: 8} {
		spec := icmpSpec("192.0.2.31", params)
		if res := Run(context.Background(), &spec, nil); res.Status != probewire.StatusOK {
			t.Fatalf("%s: %+v", params, res)
		}
		if got := conn4.dataLen.Load(); got != want {
			t.Errorf("%s: %d payload bytes on the wire, want %d", params, got, want)
		}
	}
	// The executor names the field of an IPv6 payload above 1452 bytes.
	e6 := &ICMPExecutor{Mux: fakeMux(newFakeICMP(6, false))}
	defer e6.Mux.Close()
	spec := icmpSpec("2001:db8::31", `{"size":1460}`)
	res := e6.Execute(context.Background(), spec, nil)
	if res.Status != probewire.StatusError || res.Code != probewire.ReasonConfig || res.Labels[LabelField] != "params.size" {
		t.Errorf("1460 bytes over IPv6: %+v, want error/config on params.size", res)
	}

	// family: the address chosen for a host name.
	both := []netip.Addr{netip.MustParseAddr("192.0.2.40"), netip.MustParseAddr("2001:db8::40")}
	var networks []string
	lookup := func(_ context.Context, network, _ string) ([]netip.Addr, error) {
		networks = append(networks, network)
		return both, nil
	}
	ex := &ICMPExecutor{Resolve: lookup}
	for family, want := range map[string]string{"auto": "2001:db8::40", "v4": "192.0.2.40", "v6": "2001:db8::40"} {
		networks = nil
		ip, err := ex.resolve(context.Background(), "router.example", family)
		if err != nil || ip.String() != want {
			t.Errorf("family %s: %v, %v; want %s", family, ip, err, want)
		}
		if wantNet := map[string]string{"auto": "ip", "v4": "ip4", "v6": "ip6"}[family]; len(networks) != 1 || networks[0] != wantNet {
			t.Errorf("family %s looked up %v, want %s", family, networks, wantNet)
		}
	}
	// Only IPv4 addresses: auto falls back to IPv4, v6 does not resolve.
	ex.Resolve = func(context.Context, string, string) ([]netip.Addr, error) { return both[:1], nil }
	if ip, err := ex.resolve(context.Background(), "v4only.example", "auto"); err != nil || ip.String() != "192.0.2.40" {
		t.Errorf("auto with IPv4 only: %v, %v", ip, err)
	}
	if _, err := ex.resolve(context.Background(), "v4only.example", "v6"); err == nil {
		t.Error("v6 with IPv4 only resolved")
	}
	// Through the executor: an IPv6 socket for a host name with auto.
	conn6 := newFakeICMP(6, false)
	var families []int
	var calls atomic.Int32
	m := NewMux(fakeListen(&calls, func(f int) (*fakeICMP, error) {
		families = append(families, f)
		return conn6, nil
	}))
	defer m.Close()
	swapExecutor(t, TypeICMP, &ICMPExecutor{Mux: m, Resolve: lookup})
	spec = icmpSpec("router.example", `{"count":1}`)
	if res := Run(context.Background(), &spec, nil); res.Status != probewire.StatusOK {
		t.Errorf("host name: %+v", res)
	}
	if len(families) != 1 || families[0] != 6 {
		t.Errorf("sockets opened for families %v, want [6]", families)
	}
	swapExecutor(t, TypeICMP, &ICMPExecutor{Mux: m, Resolve: func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("nxdomain")
	}})
	if res := Run(context.Background(), &spec, nil); res.Status != probewire.StatusFail || res.Code != probewire.ReasonUnreachable {
		t.Errorf("unresolvable host: %+v, want fail/unreachable", res)
	}
	// A literal of the other family: Validate rejects it before the
	// executor runs (field error on params.family).
	for target, family := range map[string]string{"192.0.2.50": "v6", "2001:db8::50": "v4"} {
		spec := icmpSpec(target, `{"count":1,"family":"`+family+`"}`)
		res := Run(context.Background(), &spec, nil)
		if res.Status != probewire.StatusError || res.Code != probewire.ReasonConfig || res.Labels[LabelField] != "params.family" {
			t.Errorf("%s with family %s: %+v, want error/config on params.family", target, family, res)
		}
	}
}

// TestICMPIPv6 covers WP-953 AK3: ICMPv6 echo over the IPv6 socket.
func TestICMPIPv6(t *testing.T) {
	conn := newFakeICMP(6, false)
	var families []int
	var calls atomic.Int32
	m := NewMux(fakeListen(&calls, func(f int) (*fakeICMP, error) {
		families = append(families, f)
		return conn, nil
	}))
	defer m.Close()
	swapExecutor(t, TypeICMP, &ICMPExecutor{Mux: m})
	spec := icmpSpec("2001:db8::10", `{"count":2,"spacing_ms":200,"family":"v6"}`)
	res := Run(context.Background(), &spec, nil)
	if res.Status != probewire.StatusOK || *res.Loss != 0 {
		t.Fatalf("IPv6: %+v", res)
	}
	if len(families) != 1 || families[0] != 6 {
		t.Errorf("sockets opened for families %v, want [6]", families)
	}
}
