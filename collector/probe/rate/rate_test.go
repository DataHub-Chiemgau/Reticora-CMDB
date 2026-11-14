package rate

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestRateFirstObservation covers WP-994 AK2: the first reading and a
// reading not later than the previous one yield no rate.
func TestRateFirstObservation(t *testing.T) {
	s := NewStore(0, time.Minute)
	if _, ok := s.Observe("in", 100, 64, t0, 0); ok {
		t.Fatal("first observation yielded a rate")
	}
	if _, ok := s.Observe("in", 200, 64, t0, 0); ok {
		t.Fatal("observation at the same instant yielded a rate")
	}
	if _, ok := s.Observe("in", 200, 64, t0.Add(-time.Second), 0); ok {
		t.Fatal("observation before the previous one yielded a rate")
	}
	// The previous value stayed (100 at t0): 10 s later 1100 is 100/s.
	r, ok := s.Observe("in", 1100, 64, t0.Add(10*time.Second), 0)
	if !ok || !near(r, 100) {
		t.Fatalf("rate = %v, %v; want 100, true", r, ok)
	}
	var nilStore *Store
	if _, ok := nilStore.Observe("in", 1, 64, t0, 0); ok {
		t.Fatal("nil store yielded a rate")
	}
}

// TestRateWrap32 covers WP-994 AK1: a 32-bit counter passing 2^32.
func TestRateWrap32(t *testing.T) {
	s := NewStore(0, time.Minute)
	s.Observe("in", math.MaxUint32-99, 32, t0, 0)
	r, ok := s.Observe("in", 100, 32, t0.Add(10*time.Second), 0)
	// 99 to the top, 1 to 0, 100 above: 200 in 10 s.
	if !ok || !near(r, 20) {
		t.Fatalf("rate = %v, %v; want 20, true", r, ok)
	}
}

// TestRateWrap64 covers WP-994 AK1: a 64-bit counter passing 2^64.
func TestRateWrap64(t *testing.T) {
	s := NewStore(0, time.Minute)
	s.Observe("in", math.MaxUint64-499, 64, t0, 0)
	r, ok := s.Observe("in", 500, 64, t0.Add(10*time.Second), 0)
	if !ok || !near(r, 100) {
		t.Fatalf("rate = %v, %v; want 100, true", r, ok)
	}
}

// TestRateRebootNoRate covers WP-994 AK1: after sysUpTime stepped back the
// counters yield no rate; the reading is the new previous value.
func TestRateRebootNoRate(t *testing.T) {
	s := NewStore(0, time.Minute)
	if s.Uptime(1_000_000, t0) {
		t.Fatal("first uptime reported a reboot")
	}
	s.Observe("in", 5_000_000, 64, t0, 0)
	if !s.Uptime(300, t0.Add(time.Minute)) {
		t.Fatal("uptime step back not reported as reboot")
	}
	if r, ok := s.Observe("in", 1000, 64, t0.Add(time.Minute), 0); ok {
		t.Fatalf("rate %v after reboot", r)
	}
	if s.Uptime(6300, t0.Add(2*time.Minute)) {
		t.Fatal("rising uptime reported as reboot")
	}
	r, ok := s.Observe("in", 7000, 64, t0.Add(2*time.Minute), 0)
	if !ok || !near(r, 100) {
		t.Fatalf("rate = %v, %v; want 100, true", r, ok)
	}
	if got := s.Stats().Discontinuities; got != 1 {
		t.Fatalf("discontinuities = %d, want 1", got)
	}
}

// TestRateDiscontinuityNoRate covers WP-994 AK1: a changed
// ifCounterDiscontinuityTime yields no rate.
func TestRateDiscontinuityNoRate(t *testing.T) {
	s := NewStore(0, time.Minute)
	s.Observe("in", 1000, 64, t0, 42)
	if _, ok := s.Observe("in", 50, 64, t0.Add(10*time.Second), 4711); ok {
		t.Fatal("rate across a discontinuity")
	}
	r, ok := s.Observe("in", 1050, 64, t0.Add(20*time.Second), 4711)
	if !ok || !near(r, 100) {
		t.Fatalf("rate = %v, %v; want 100, true", r, ok)
	}
}

// TestRateImplausibleDropped covers WP-994 AK4.
func TestRateImplausibleDropped(t *testing.T) {
	s := NewStore(0, time.Minute)
	// 1 Gbit/s × 1.1 as bytes per second.
	maxRate := 1e9 * 1.1 / 8
	s.ObserveMax("in", 0, 64, t0, 0, maxRate)
	if _, ok := s.ObserveMax("in", 1e12, 64, t0.Add(10*time.Second), 0, maxRate); ok {
		t.Fatal("implausible rate accepted")
	}
	if got := s.Stats().Implausible; got != 1 {
		t.Fatalf("implausible = %d, want 1", got)
	}
	r, ok := s.ObserveMax("in", 1e12+1e9, 64, t0.Add(20*time.Second), 0, maxRate)
	if !ok || !near(r, 1e8) {
		t.Fatalf("rate = %v, %v; want 1e8, true", r, ok)
	}
}

// TestRateSeriesLimit covers WP-994 AK3: series beyond max_series are
// refused and counted.
func TestRateSeriesLimit(t *testing.T) {
	s := NewStore(3, time.Minute)
	for i := range 5 {
		s.Observe(fmt.Sprintf("s%d", i), 1, 64, t0, 0)
	}
	st := s.Stats()
	if st.Series != 3 || st.Rejected != 2 {
		t.Fatalf("stats = %+v, want 3 series and 2 rejected", st)
	}
	// A refused series never yields a rate.
	if _, ok := s.Observe("s4", 100, 64, t0.Add(time.Second), 0); ok {
		t.Fatal("refused series yielded a rate")
	}
	if _, ok := s.Observe("s0", 101, 64, t0.Add(time.Second), 0); !ok {
		t.Fatal("admitted series yielded no rate")
	}
}

// TestRateStaleSeriesEvicted covers WP-994 AK3: a series without an
// observation for three intervals is dropped and frees its slot.
func TestRateStaleSeriesEvicted(t *testing.T) {
	s := NewStore(2, time.Minute)
	s.Observe("a", 1, 64, t0, 0)
	s.Observe("b", 1, 64, t0, 0)
	for i := 1; i <= 3; i++ {
		s.Observe("a", uint64(1+i), 64, t0.Add(time.Duration(i)*time.Minute), 0)
	}
	// b was last seen at t0; 3 min later it is not yet older than three
	// intervals, a moment later it is.
	s.Observe("a", 10, 64, t0.Add(3*time.Minute+time.Second+time.Minute), 0)
	st := s.Stats()
	if st.Evicted != 1 || st.Series != 1 {
		t.Fatalf("stats = %+v, want 1 evicted and 1 series", st)
	}
	if _, ok := s.Observe("c", 1, 64, t0.Add(5*time.Minute), 0); ok {
		t.Fatal("first observation of c yielded a rate")
	}
	if got := s.Stats().Rejected; got != 0 {
		t.Fatalf("c rejected although b was evicted (rejected = %d)", got)
	}
}

// TestPreviousSwapGauge covers WP-994 AK6 with the sysUpTime previous
// value.
func TestPreviousSwapGauge(t *testing.T) {
	s := NewStore(0, time.Minute)
	if _, _, ok := s.Previous(UptimeSeries); ok {
		t.Fatal("previous value before the first swap")
	}
	if _, _, ok := s.Swap(UptimeSeries, Num(1000), t0); ok {
		t.Fatal("first swap reported a previous value")
	}
	v, at, ok := s.Previous(UptimeSeries)
	if !ok || v.Number != 1000 || !at.Equal(t0) || v.IsEnum() {
		t.Fatalf("previous = %+v %v %v", v, at, ok)
	}
	prev, prevAt, ok := s.Swap(UptimeSeries, Num(7000), t0.Add(time.Minute))
	if !ok || prev.Number != 1000 || !prevAt.Equal(t0) {
		t.Fatalf("swap returned %+v %v %v", prev, prevAt, ok)
	}
	if v, _, _ := s.Previous(UptimeSeries); v.Number != 7000 {
		t.Fatalf("previous after swap = %v, want 7000", v.Number)
	}
}

// TestSwapEnumState covers WP-994 AK6 with an enum previous state.
func TestSwapEnumState(t *testing.T) {
	s := NewStore(0, time.Minute)
	s.Swap("peer/192.0.2.1", Enum("established"), t0)
	prev, _, ok := s.Swap("peer/192.0.2.1", Enum("idle"), t0.Add(time.Minute))
	if !ok || !prev.IsEnum() || prev.Enum != "established" {
		t.Fatalf("previous state = %+v %v, want established", prev, ok)
	}
}

// TestSwapSeriesLimit covers WP-994 AK6: swap and counter series share the
// limit and swap series go stale.
func TestSwapSeriesLimit(t *testing.T) {
	s := NewStore(2, time.Minute)
	s.Observe("c", 1, 64, t0, 0)
	s.Swap("g1", Num(1), t0)
	if _, _, ok := s.Swap("g2", Num(1), t0); ok {
		t.Fatal("swap beyond the limit reported a previous value")
	}
	if _, _, ok := s.Previous("g2"); ok {
		t.Fatal("refused swap series was stored")
	}
	if st := s.Stats(); st.Rejected != 1 || st.Series != 2 {
		t.Fatalf("stats = %+v, want 1 rejected, 2 series", st)
	}
	// After four intervals without an observation both are dropped.
	s.Swap("g3", Num(1), t0.Add(4*time.Minute))
	if _, _, ok := s.Previous("g1"); ok {
		t.Fatal("stale swap series not evicted")
	}
	if st := s.Stats(); st.Evicted != 2 || st.Series != 1 {
		t.Fatalf("stats = %+v, want 2 evicted, 1 series", st)
	}
}

// TestStoreFromContext covers WP-994 AK6.
func TestStoreFromContext(t *testing.T) {
	if FromContext(context.Background()) != nil {
		t.Fatal("store in an empty context")
	}
	s := NewStore(0, time.Minute)
	ctx, cancel := context.WithTimeout(WithStore(context.Background(), s), time.Second)
	defer cancel()
	if FromContext(ctx) != s {
		t.Fatal("store not found in a derived context")
	}
}

// TestRateUptimeAdmittedAtLimit covers WP-994 AK3: sysUpTime is admitted
// even when the counters fill max_series, so reboot detection keeps
// working.
func TestRateUptimeAdmittedAtLimit(t *testing.T) {
	s := NewStore(2, time.Minute)
	s.Observe("a", 1, 64, t0, 0)
	s.Observe("b", 1, 64, t0, 0)
	s.Uptime(1000, t0)
	if !s.Uptime(10, t0.Add(time.Minute)) {
		t.Fatal("reboot not detected at the series limit")
	}
	if _, ok := s.Observe("a", 2, 64, t0.Add(time.Minute), 0); ok {
		t.Fatal("rate after a reboot detected at the series limit")
	}
}
