package probe

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func openMachine(t *testing.T, dir string) *StateMachine {
	t.Helper()
	seq, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := OpenStateMachine(dir, seq)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func f64(v float64) *float64 { return &v }

func okResult() probewire.Result { return probewire.Result{Status: probewire.StatusOK, RTTMS: f64(1)} }
func failResultOf(code string) probewire.Result {
	return probewire.Result{Status: probewire.StatusFail, Code: code}
}
func errResult(code string) probewire.Result {
	return probewire.Result{Status: probewire.StatusError, Code: code}
}
func warnResult() probewire.Result { return probewire.Result{Status: probewire.StatusOK, RTTMS: f64(500)} }

// stateSpec is a tcp probe with rtt_warn_ms 100 as threshold.
func stateSpec(down, up int) probewire.ProbeSpec {
	return probewire.ProbeSpec{
		ID: "probe-s", Type: TypeTCP, Target: "192.0.2.1", IntervalMS: 30_000, TimeoutMS: 10_000,
		DownCount: down, UpCount: up, Epoch: 4, Thresholds: json.RawMessage(`{"rtt_warn_ms":100}`),
	}
}

// feed observes the results and returns the states after each.
func feed(t *testing.T, m *StateMachine, spec *probewire.ProbeSpec, results ...probewire.Result) []probewire.State {
	t.Helper()
	var out []probewire.State
	for _, r := range results {
		if _, _, err := m.Observe(spec, r); err != nil {
			t.Fatal(err)
		}
		c, _ := m.Counters(spec.ID)
		out = append(out, c.State)
	}
	return out
}

func repeat(r probewire.Result, n int) []probewire.Result {
	out := make([]probewire.Result, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// TestStateTransitions covers PRB-05 and WP-952 AK1: every transition,
// table driven, including degraded.
func TestStateTransitions(t *testing.T) {
	const (
		U  = probewire.StateUp
		D  = probewire.StateDown
		G  = probewire.StateDegraded
		Un = probewire.StateUnknown
	)
	fail := failResultOf(probewire.ReasonTimeout)
	cases := []struct {
		name     string
		down, up int
		results  []probewire.Result
		want     []probewire.State
	}{
		{"unknown → up after up_count", 3, 2, []probewire.Result{okResult(), okResult()}, []probewire.State{Un, U}},
		{"unknown → down after down_count", 3, 1, repeat(fail, 3), []probewire.State{Un, Un, D}},
		{"up → down after down_count", 3, 1, append([]probewire.Result{okResult()}, repeat(fail, 3)...), []probewire.State{U, U, U, D}},
		{"a success in between resets the down count", 3, 1,
			[]probewire.Result{okResult(), fail, fail, okResult(), fail, fail, fail}, []probewire.State{U, U, U, U, U, U, D}},
		{"down → up after up_count", 2, 2,
			[]probewire.Result{okResult(), okResult(), fail, fail, okResult(), okResult()}, []probewire.State{Un, U, U, D, D, U}},
		{"up → degraded after down_count warnings", 3, 1,
			[]probewire.Result{okResult(), warnResult(), warnResult(), warnResult()}, []probewire.State{U, U, U, G}},
		{"degraded → up after up_count clean successes", 2, 2,
			[]probewire.Result{okResult(), okResult(), warnResult(), warnResult(), okResult(), okResult()}, []probewire.State{Un, U, U, G, G, U}},
		{"degraded → down after down_count failures", 2, 1,
			[]probewire.Result{warnResult(), fail, fail}, []probewire.State{G, G, D}},
		{"down → degraded when the return violates a threshold", 1, 1,
			[]probewire.Result{fail, warnResult()}, []probewire.State{D, G}},
		{"unknown → degraded when the first successes violate a threshold", 3, 1,
			[]probewire.Result{warnResult()}, []probewire.State{G}},
		{"a warning in between resets the clean count", 1, 2,
			[]probewire.Result{warnResult(), warnResult(), okResult(), warnResult(), okResult(), okResult()}, []probewire.State{Un, G, G, G, G, U}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := openMachine(t, t.TempDir())
			spec := stateSpec(c.down, c.up)
			got := feed(t, m, &spec, c.results...)
			if len(got) != len(c.want) {
				t.Fatalf("got %v", got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("states = %v, want %v", got, c.want)
				}
			}
		})
	}

	// The state change carries seq (stream state), epoch, reason,
	// observed_at in UTC and the last result (PRB-05, AK4).
	m := openMachine(t, t.TempDir())
	spec := stateSpec(1, 1)
	local := time.Date(2026, 11, 12, 9, 0, 0, 0, time.FixedZone("CET", 3600))
	r := okResult()
	r.ObservedAt = local
	res, ch, err := m.Observe(&spec, r)
	if err != nil || ch == nil {
		t.Fatalf("first success: change %v, err %v", ch, err)
	}
	if ch.From != probewire.StateUnknown || ch.To != probewire.StateUp || ch.Epoch != 4 || ch.Seq != 1 || ch.ProbeID != spec.ID {
		t.Fatalf("change = %+v", ch)
	}
	if ch.ObservedAt.Location() != time.UTC || !ch.ObservedAt.Equal(local) {
		t.Fatalf("observed_at = %v, want %v in UTC", ch.ObservedAt, local)
	}
	if ch.LastResult == nil || ch.LastResult.Seq != res.Seq || res.Seq != 1 || res.Epoch != 4 {
		t.Fatalf("last result = %+v, result = %+v", ch.LastResult, res)
	}
	if _, err := probewire.Encode(ch); err != nil {
		t.Fatal(err)
	}
	res2, ch2, _ := m.Observe(&spec, failResultOf(probewire.ReasonRefused))
	if ch2 == nil || ch2.To != probewire.StateDown || ch2.Reason != probewire.ReasonRefused || ch2.Seq != 2 || res2.Seq != 2 {
		t.Fatalf("down change = %+v (result seq %d)", ch2, res2.Seq)
	}
	if _, ch3, _ := m.Observe(&spec, failResultOf(probewire.ReasonRefused)); ch3 != nil {
		t.Fatalf("no change expected while down: %+v", ch3)
	}
}

// TestErrorResultLeadsToUnknownNeverDown covers AK2 and E-177: error
// results lead to unknown with their code and reset the counters.
func TestErrorResultLeadsToUnknownNeverDown(t *testing.T) {
	for _, code := range []string{
		probewire.ReasonUnsupported, probewire.ReasonLimitExceeded, probewire.ReasonICMPUnavailable,
		probewire.ReasonAuthFailed, probewire.ReasonConfig,
	} {
		t.Run(code, func(t *testing.T) {
			m := openMachine(t, t.TempDir())
			spec := stateSpec(2, 1)
			feed(t, m, &spec, okResult())
			// One failure, then many errors: never down.
			states := feed(t, m, &spec, append([]probewire.Result{failResultOf(probewire.ReasonTimeout)}, repeat(errResult(code), 10)...)...)
			for _, s := range states {
				if s == probewire.StateDown {
					t.Fatalf("error %s led to down: %v", code, states)
				}
			}
			c, _ := m.Counters(spec.ID)
			if c.State != probewire.StateUnknown || c.Reason != code || c.ConsecutiveFail != 0 || c.ConsecutiveOK != 0 {
				t.Fatalf("counters = %+v", c)
			}
			// The failure before the errors does not count: one more
			// failure is not yet down (down_count 2).
			if s := feed(t, m, &spec, failResultOf(probewire.ReasonTimeout)); s[0] == probewire.StateDown {
				t.Fatal("down count was not reset by the error")
			}
		})
	}
	// The change to unknown carries the reason; another reason while
	// unknown is reported as well.
	m := openMachine(t, t.TempDir())
	spec := stateSpec(1, 1)
	feed(t, m, &spec, okResult())
	_, ch, _ := m.Observe(&spec, errResult(probewire.ReasonAuthFailed))
	if ch == nil || ch.To != probewire.StateUnknown || ch.Reason != probewire.ReasonAuthFailed {
		t.Fatalf("change = %+v", ch)
	}
	_, ch, _ = m.Observe(&spec, errResult(probewire.ReasonUnsupported))
	if ch == nil || ch.From != probewire.StateUnknown || ch.Reason != probewire.ReasonUnsupported {
		t.Fatalf("reason change while unknown = %+v", ch)
	}
	if _, ch, _ = m.Observe(&spec, errResult(probewire.ReasonUnsupported)); ch != nil {
		t.Fatalf("same reason again: %+v", ch)
	}
}

// TestOkWithWarnCodeLeadsToDegraded covers AK3: ok with a warn code
// (threshold_exceeded and a code added to WarnCodes) leads to degraded;
// RTT and loss thresholds of the spec are derived here.
func TestOkWithWarnCodeLeadsToDegraded(t *testing.T) {
	const added = "test_added_warn"
	probewire.WarnCodes[added] = true
	t.Cleanup(func() { delete(probewire.WarnCodes, added) })
	for _, code := range []string{probewire.WarnThresholdExceeded, probewire.WarnCertExpiring, added} {
		m := openMachine(t, t.TempDir())
		spec := stateSpec(1, 1)
		spec.Thresholds = nil
		feed(t, m, &spec, okResult())
		_, ch, _ := m.Observe(&spec, probewire.Result{Status: probewire.StatusOK, Code: code})
		if ch == nil || ch.To != probewire.StateDegraded || ch.Reason != code {
			t.Fatalf("%s: change = %+v", code, ch)
		}
	}
	// RTT above rtt_warn_ms (thresholds) → threshold_exceeded.
	m := openMachine(t, t.TempDir())
	spec := stateSpec(1, 1)
	feed(t, m, &spec, okResult())
	res, ch, _ := m.Observe(&spec, warnResult())
	if ch == nil || ch.To != probewire.StateDegraded || ch.Reason != probewire.WarnThresholdExceeded || res.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("rtt: change = %+v, result code %q", ch, res.Code)
	}
	// rtt_warn_ms from params; RTT equal to the threshold is no violation.
	spec.Thresholds = nil
	spec.Params = json.RawMessage(`{"port":22,"rtt_warn_ms":500}`)
	if s := feed(t, m, &spec, warnResult()); s[0] != probewire.StateUp {
		t.Fatalf("rtt equal to threshold: %v", s)
	}
	// Loss: icmp has loss_warn 0.5 by default (E-177).
	icmp := probewire.ProbeSpec{ID: "probe-icmp", Type: "icmp", Target: "192.0.2.1", IntervalMS: 30_000, TimeoutMS: 10_000, DownCount: 1, UpCount: 1}
	feed(t, m, &icmp, probewire.Result{Status: probewire.StatusOK, Loss: f64(0)})
	if s := feed(t, m, &icmp, probewire.Result{Status: probewire.StatusOK, Loss: f64(1.0 / 3)}); s[0] != probewire.StateUp {
		t.Fatalf("loss 1/3 below default 0.5: %v", s)
	}
	if s := feed(t, m, &icmp, probewire.Result{Status: probewire.StatusOK, Loss: f64(2.0 / 3)}); s[0] != probewire.StateDegraded {
		t.Fatalf("loss 2/3 above default 0.5: %v", s)
	}
}

// TestPingDefaultDownAfterFifthFailure covers AK7: ICMP standard (30 s,
// down count 5, up count 1): down with the 5th failure, 120 s after the
// first one, and up with the first success.
func TestPingDefaultDownAfterFifthFailure(t *testing.T) {
	icmp, _ := probewire.Lookup("icmp")
	spec := probewire.ProbeSpec{
		ID: "ping-default", Type: "icmp", Target: "192.0.2.10",
		IntervalMS: icmp.DefaultIntervalS * 1000, TimeoutMS: icmp.DefaultTimeoutS * 1000,
		DownCount: icmp.DefaultDownCount, UpCount: icmp.DefaultUpCount,
	}
	if spec.IntervalMS != 30_000 || spec.DownCount != 5 || spec.UpCount != 1 {
		t.Fatalf("icmp defaults = %+v", spec)
	}
	m := openMachine(t, t.TempDir())
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	up := okResult()
	up.ObservedAt = start
	feed(t, m, &spec, up)
	firstFail := start.Add(30 * time.Second)
	var downAt time.Time
	for i := 0; i < 5; i++ {
		r := failResultOf(probewire.ReasonTimeout)
		r.ObservedAt = firstFail.Add(time.Duration(i) * 30 * time.Second)
		_, ch, err := m.Observe(&spec, r)
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 && ch != nil {
			t.Fatalf("change after failure %d: %+v", i+1, ch)
		}
		if i == 4 {
			if ch == nil || ch.To != probewire.StateDown {
				t.Fatalf("no down after the 5th failure: %+v", ch)
			}
			downAt = ch.ObservedAt
		}
	}
	if d := downAt.Sub(firstFail); d != 120*time.Second {
		t.Fatalf("down %s after the first failure, want 120 s", d)
	}
	// Outage begins just after the last success: at most interval +
	// 120 s + timeout ≈ 160 s, the plan's "about 150 s".
	if d := downAt.Sub(start); d > 150*time.Second {
		t.Fatalf("down %s after the outage began, want ≤ 150 s", d)
	}
	r := okResult()
	r.ObservedAt = downAt.Add(30 * time.Second)
	if _, ch, _ := m.Observe(&spec, r); ch == nil || ch.To != probewire.StateUp {
		t.Fatalf("no up after the first success: %+v", ch)
	}
}

// TestCountersSurviveRestart covers AK6: the counters are saved and a
// restart does not reset a running down count; the state survives too.
func TestCountersSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	m := openMachine(t, dir)
	spec := stateSpec(5, 1)
	feed(t, m, &spec, okResult())
	feed(t, m, &spec, repeat(failResultOf(probewire.ReasonTimeout), 3)...)
	// The periodic save runs until the context ends and saves once more.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.RunPersist(ctx, 10*time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	feed(t, m, &spec, failResultOf(probewire.ReasonTimeout)) // 4th failure, saved at the end
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	m2 := openMachine(t, dir)
	c, ok := m2.Counters(spec.ID)
	if !ok || c.State != probewire.StateUp || c.ConsecutiveFail != 4 {
		t.Fatalf("after restart: %+v, %v", c, ok)
	}
	_, ch, err := m2.Observe(&spec, failResultOf(probewire.ReasonTimeout))
	if err != nil || ch == nil || ch.To != probewire.StateDown {
		t.Fatalf("5th failure after restart: %+v, %v", ch, err)
	}
	// Sequence numbers of the restarted machine are above the old ones.
	if ch.Seq <= 1 {
		t.Fatalf("state seq after restart = %d", ch.Seq)
	}
	// A removed probe is forgotten after the next save.
	m2.Remove(spec.ID)
	if err := m2.Save(); err != nil {
		t.Fatal(err)
	}
	if _, ok := openMachine(t, dir).Counters(spec.ID); ok {
		t.Fatal("removed probe survived the restart")
	}
}
