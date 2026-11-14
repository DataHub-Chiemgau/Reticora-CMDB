package probe

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// startRecorder records the start of every check: probe, due time
// (derived from the phase) and the fake clock at the start.
type startRecorder struct {
	mu     sync.Mutex
	clock  *fakeClock
	starts map[string][]time.Time
	block  map[string]chan struct{}
}

func newStartRecorder(c *fakeClock) *startRecorder {
	return &startRecorder{clock: c, starts: map[string][]time.Time{}, block: map[string]chan struct{}{}}
}

func (r *startRecorder) run(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
	r.mu.Lock()
	r.starts[spec.ID] = append(r.starts[spec.ID], r.clock.Now())
	ch := r.block[spec.ID]
	r.mu.Unlock()
	if ch != nil {
		<-ch
	}
	return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, Status: probewire.StatusOK}
}

func (r *startRecorder) startsOf(id string) []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.starts[id]...)
}

// unlimited are limits that never reject; the spread and drift tests are
// about timing, not about the per-type budget.
func unlimited() *Limits {
	return &Limits{Lookup: func(string) TypeLimits { return TypeLimits{} }}
}

func inline(f func()) { f() }

func schedSpec(id string, intervalMS int64) probewire.ProbeSpec {
	return probewire.ProbeSpec{
		ID: id, Type: TypeTCP, Target: "192.0.2.1", IntervalMS: intervalMS, TimeoutMS: 1000,
		DownCount: 3, UpCount: 1, Epoch: 1,
	}
}

// drive advances the fake clock from its current time to end in steps of
// the wheel resolution, calling Advance at every step.
func drive(s *Scheduler, c *fakeClock, end time.Time) {
	for t := c.Now(); !t.After(end); t = t.Add(WheelResolution) {
		c.Set(t)
		s.Advance(t)
	}
}

// maxDrift is the bound between due time and start (WP-951 AK5): one
// wheel resolution plus the offset of the driving ticker.
const maxDrift = 200 * time.Millisecond

// epochStart is an arbitrary instant that is not aligned to a 30 s
// boundary.
var epochStart = time.Date(2026, 11, 12, 8, 0, 7, 345_000_000, time.UTC)

func probeIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		// UUID-like IDs as the server assigns them.
		ids[i] = fmt.Sprintf("%08x-4b1e-4c3a-9d2f-%012x", i*2654435761, i)
	}
	return ids
}

// TestSchedulerSpreadsStartsEvenly covers PRB-04 and WP-951 AK5: 10,000
// probes at 30 s over 120 s of simulated time; no one-second window holds
// more than 1.5 × the mean (333/s).
func TestSchedulerSpreadsStartsEvenly(t *testing.T) {
	const probes, intervalMS = 10_000, 30_000
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
	for _, id := range probeIDs(probes) {
		s.Upsert(schedSpec(id, intervalMS))
	}
	end := epochStart.Add(120 * time.Second)
	drive(s, clock, end)

	perSecond := map[int64]int{}
	total := 0
	for _, id := range probeIDs(probes) {
		for _, at := range rec.startsOf(id) {
			perSecond[at.Unix()]++
			total++
		}
	}
	// Only whole seconds inside the window count.
	first, last := epochStart.Unix()+1, end.Unix()-1
	full, max := 0, 0
	for sec := first; sec <= last; sec++ {
		full += perSecond[sec]
		if perSecond[sec] > max {
			max = perSecond[sec]
		}
	}
	mean := float64(probes) / float64(intervalMS/1000)
	if max > int(1.5*mean) {
		t.Fatalf("max starts per second = %d, want ≤ %.0f (1.5 × mean %.0f)", max, 1.5*mean, mean)
	}
	// Every due time in the window starts; only a due time in the last
	// 200 ms may still be pending when the drive ends.
	minWant, maxWant := 0, 0
	for _, id := range probeIDs(probes) {
		minWant += len(dueTimes(id, intervalMS, epochStart, end.Add(-maxDrift)))
		maxWant += len(dueTimes(id, intervalMS, epochStart, end))
	}
	if total < minWant || total > maxWant {
		t.Fatalf("starts in 120 s = %d, want %d…%d", total, minWant, maxWant)
	}
	if st := s.Stats(); st.Overrun != 0 || st.Skipped != 0 {
		t.Fatalf("stats = %+v, want no overrun and no skip", st)
	}
	t.Logf("max %d/s, mean %.0f/s, %d starts", max, mean, total)
}

// TestSchedulerPhaseIsStableAcrossRestart covers PRB-04 and AK1: a new
// scheduler (restart or other collector) at another time starts the probe
// at the same phase.
func TestSchedulerPhaseIsStableAcrossRestart(t *testing.T) {
	const intervalMS = 30_000
	id := "0b7e5a0e-3f43-4a51-9d0c-6c1f4c8e2a10"
	phase := Phase(id, intervalMS)
	if phase != Phase(id, intervalMS) || phase < 0 || phase >= intervalMS {
		t.Fatalf("phase %d not stable or out of range", phase)
	}
	for _, at := range []time.Time{epochStart, epochStart.Add(17*time.Hour + 123*time.Millisecond)} {
		clock := newFakeClock(at)
		rec := newStartRecorder(clock)
		s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
		s.Upsert(schedSpec(id, intervalMS))
		drive(s, clock, at.Add(65*time.Second))
		starts := rec.startsOf(id)
		if len(starts) < 2 {
			t.Fatalf("start at %s: %d starts", at, len(starts))
		}
		for _, st := range starts {
			if got := st.UnixMilli() % intervalMS; got < phase || got >= phase+maxDrift.Milliseconds() {
				t.Fatalf("start %s: offset %d ms in interval, want phase %d ms (+ < 200 ms)", st, got, phase)
			}
		}
	}
	// The phase follows the FNV-1a-64 hash of the probe ID.
	if Phase("a", 1000) == Phase("b", 1000) && Phase("a", 7919) == Phase("b", 7919) {
		t.Fatal("phase does not depend on the probe ID")
	}
}

// TestSchedulerIntervalChangeKeepsPhase covers AK2: a changed interval
// keeps the hash phase, a removed probe is not started again, a new probe
// starts in its phase.
func TestSchedulerIntervalChangeKeepsPhase(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
	id := "probe-interval"
	s.Upsert(schedSpec(id, 30_000))
	drive(s, clock, epochStart.Add(40*time.Second))

	s.Upsert(schedSpec(id, 60_000))
	changed := clock.Now()
	drive(s, clock, changed.Add(130*time.Second))
	var after []time.Time
	for _, st := range rec.startsOf(id) {
		if st.After(changed) {
			after = append(after, st)
		}
	}
	if len(after) < 2 {
		t.Fatalf("starts after interval change: %v", after)
	}
	phase := Phase(id, 60_000)
	for i, st := range after {
		if got := st.UnixMilli() % 60_000; got < phase || got >= phase+maxDrift.Milliseconds() {
			t.Fatalf("start %s after change: offset %d, want phase %d", st, got, phase)
		}
		if i > 0 && st.Sub(after[i-1]) != 60*time.Second {
			t.Fatalf("starts %s and %s are not 60 s apart", after[i-1], st)
		}
	}

	// Remove: no further start.
	s.Remove(id)
	removedAt := clock.Now()
	drive(s, clock, removedAt.Add(130*time.Second))
	for _, st := range rec.startsOf(id) {
		if st.After(removedAt) {
			t.Fatalf("removed probe started at %s", st)
		}
	}
	if _, ok := s.Next(id); ok || s.Len() != 0 {
		t.Fatal("removed probe still scheduled")
	}

	// A new probe added while running starts in its phase.
	other := "probe-new"
	s.Upsert(schedSpec(other, 30_000))
	addedAt := clock.Now()
	drive(s, clock, addedAt.Add(31*time.Second))
	starts := rec.startsOf(other)
	if len(starts) != 1 {
		t.Fatalf("new probe: %d starts in 31 s, want 1", len(starts))
	}
	if got := starts[0].UnixMilli() % 30_000; got < Phase(other, 30_000) || got >= Phase(other, 30_000)+maxDrift.Milliseconds() {
		t.Fatalf("new probe offset %d, want phase %d", got, Phase(other, 30_000))
	}
}

// TestSchedulerSkipsOverrun covers AK4: while the previous check of a
// probe still runs, no second check starts and overrun rises.
func TestSchedulerSkipsOverrun(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	id := "probe-slow"
	hold := make(chan struct{})
	rec.block[id] = hold
	var wg sync.WaitGroup
	s := &Scheduler{Clock: clock, Run: rec.run, Limits: unlimited(), Go: func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}}
	s.Upsert(schedSpec(id, 10_000))
	// Wait for the first start before driving further, so that the second
	// due time finds the check running.
	first, _ := s.Next(id)
	drive(s, clock, first.Add(2*WheelResolution))
	waitFor(t, func() bool { return len(rec.startsOf(id)) == 1 })
	drive(s, clock, first.Add(25*time.Second))
	if n := len(rec.startsOf(id)); n != 1 {
		t.Fatalf("starts while running = %d, want 1", n)
	}
	if got := s.Stats().Overrun; got != 2 {
		t.Fatalf("overrun = %d, want 2", got)
	}
	close(hold)
	wg.Wait()
	rec.mu.Lock()
	delete(rec.block, id)
	rec.mu.Unlock()
	drive(s, clock, first.Add(31*time.Second))
	wg.Wait()
	if n := len(rec.startsOf(id)); n != 2 {
		t.Fatalf("starts after the slow check finished = %d, want 2", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSchedulerDriftBelow200ms covers AK5: between due time and start lie
// less than 200 ms, also for intervals that are not a multiple of the
// wheel resolution and for due times beyond one wheel revolution.
func TestSchedulerDriftBelow200ms(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
	specs := map[string]int64{}
	for i, id := range probeIDs(2000) {
		iv := []int64{10_000, 30_000, 30_050, 60_000, 299_990}[i%5]
		specs[id] = iv
		s.Upsert(schedSpec(id, iv))
	}
	end := epochStart.Add(10 * time.Minute)
	drive(s, clock, end)
	var worst time.Duration
	n := 0
	for id, iv := range specs {
		// Every due time of the window has its own start, in order and
		// less than 200 ms late; only a due time in the last 200 ms may
		// still be pending when the drive ends.
		dues := dueTimes(id, iv, epochStart, end)
		must := len(dueTimes(id, iv, epochStart, end.Add(-maxDrift)))
		starts := rec.startsOf(id)
		if len(starts) < must || len(starts) > len(dues) {
			t.Fatalf("probe %s (interval %d ms): %d starts, want %d…%d", id, iv, len(starts), must, len(dues))
		}
		for i, st := range starts {
			drift := st.Sub(dues[i])
			if drift < 0 || drift >= maxDrift {
				t.Fatalf("probe %s: start %s for due time %s, drift %s", id, st, dues[i], drift)
			}
			if drift > worst {
				worst = drift
			}
			n++
		}
	}
	t.Logf("%d starts, worst drift %s", n, worst)
}

// dueTimes lists the due times of a probe in [from, to]: from its next
// start at from in steps of the interval.
func dueTimes(id string, intervalMS int64, from, to time.Time) []time.Time {
	var dues []time.Time
	for t := NextStart(id, intervalMS, from); !t.After(to); t = t.Add(time.Duration(intervalMS) * time.Millisecond) {
		dues = append(dues, t)
	}
	return dues
}

// TestSchedulerClockStepBackward: a wall clock stepped back by an hour
// (NTP step, VM restore) does not stall the scheduler; every probe starts
// again within one interval, in its phase.
func TestSchedulerClockStepBackward(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
	specs := map[string]int64{}
	for i, id := range probeIDs(50) {
		iv := []int64{10_000, 30_000, 60_000}[i%3]
		specs[id] = iv
		s.Upsert(schedSpec(id, iv))
	}
	drive(s, clock, epochStart.Add(2*time.Minute))

	back := clock.Now().Add(-time.Hour)
	clock.Set(back)
	drive(s, clock, back.Add(60*time.Second+maxDrift))
	for id, iv := range specs {
		var after []time.Time
		for _, st := range rec.startsOf(id) {
			if !st.Before(back) && st.Before(epochStart) {
				after = append(after, st)
			}
		}
		if len(after) == 0 {
			t.Fatalf("probe %s (interval %d ms): no start within one interval after the clock stepped back", id, iv)
		}
		for _, st := range after {
			if got := mod(st.UnixMilli()-Phase(id, iv), iv); got >= maxDrift.Milliseconds() {
				t.Fatalf("probe %s: start %s off its phase by %d ms", id, st, got)
			}
		}
	}
}

// TestSchedulerRejectsNonPositiveInterval: a probe with interval 0 (a
// faulty diff) is never started, and an existing probe changed to it is
// removed, instead of starting in every tick.
func TestSchedulerRejectsNonPositiveInterval(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := newStartRecorder(clock)
	s := &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}
	s.Upsert(schedSpec("zero", 0))
	s.Upsert(schedSpec("negative", -1000))
	s.Upsert(schedSpec("changed", 10_000))
	s.Upsert(schedSpec("changed", 0))
	if s.Len() != 0 {
		t.Fatalf("scheduled probes = %d, want 0", s.Len())
	}
	drive(s, clock, epochStart.Add(30*time.Second))
	if st := s.Stats(); st.Started != 0 {
		t.Fatalf("started = %d, want 0", st.Started)
	}
}

// TestNextStartIsAlignedToTheEpoch covers AK1 directly.
func TestNextStartIsAlignedToTheEpoch(t *testing.T) {
	id := "x"
	phase := Phase(id, 30_000)
	now := time.UnixMilli(30_000*1000 + phase + 1)
	next := NextStart(id, 30_000, now)
	if next.UnixMilli() != 30_000*1001+phase {
		t.Fatalf("next = %d, want %d", next.UnixMilli(), 30_000*1001+phase)
	}
	if exact := time.UnixMilli(30_000*1000 + phase); !NextStart(id, 30_000, exact).Equal(exact) {
		t.Fatal("a due time equal to now must be now")
	}
}

// storeRecorder records the previous-value store each check of a probe
// finds in its context (WP-994 AK7).
type storeRecorder struct {
	mu     sync.Mutex
	stores map[string][]*rate.Store
}

func (r *storeRecorder) run(ctx context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
	st := rate.FromContext(ctx)
	r.mu.Lock()
	r.stores[spec.ID] = append(r.stores[spec.ID], st)
	r.mu.Unlock()
	return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, Status: probewire.StatusOK}
}

func (r *storeRecorder) of(id string) []*rate.Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*rate.Store(nil), r.stores[id]...)
}

func newStoreScheduler(clock *fakeClock) (*Scheduler, *storeRecorder) {
	rec := &storeRecorder{stores: map[string][]*rate.Store{}}
	return &Scheduler{Clock: clock, Run: rec.run, Go: inline, Limits: unlimited()}, rec
}

// TestSchedulerPutsRateStoreInContext covers WP-994 AK7: two checks of the
// same probe find the same store in their context, and a value swapped in
// the first run is the previous value of the second.
func TestSchedulerPutsRateStoreInContext(t *testing.T) {
	clock := newFakeClock(epochStart)
	rec := &storeRecorder{stores: map[string][]*rate.Store{}}
	var seen []rate.Value
	run := func(ctx context.Context, spec *probewire.ProbeSpec, creds map[string]string) probewire.Result {
		st := rate.FromContext(ctx)
		prev, _, ok := st.Swap("sysUpTime", rate.Num(float64(len(rec.of(spec.ID)))), clock.Now())
		if ok {
			seen = append(seen, prev)
		}
		return rec.run(ctx, spec, creds)
	}
	s := &Scheduler{Clock: clock, Run: run, Go: inline, Limits: unlimited()}
	id := "probe-rate"
	s.Upsert(schedSpec(id, 10_000))
	drive(s, clock, epochStart.Add(21*time.Second))
	stores := rec.of(id)
	if len(stores) < 2 {
		t.Fatalf("checks = %d, want at least 2", len(stores))
	}
	if stores[0] == nil || stores[0] != stores[1] {
		t.Fatalf("stores of two runs differ: %p %p", stores[0], stores[1])
	}
	if own, ok := s.Store(id); !ok || own != stores[0] {
		t.Fatal("scheduler store differs from the store in the context")
	}
	if len(seen) == 0 || seen[0].Number != 0 {
		t.Fatalf("second run did not see the value of the first: %v", seen)
	}
}

// TestSchedulerRateStorePerProbe covers WP-994 AK7: two probes have
// separate stores.
func TestSchedulerRateStorePerProbe(t *testing.T) {
	clock := newFakeClock(epochStart)
	s, rec := newStoreScheduler(clock)
	s.Upsert(schedSpec("probe-a", 10_000))
	s.Upsert(schedSpec("probe-b", 10_000))
	drive(s, clock, epochStart.Add(11*time.Second))
	a, b := rec.of("probe-a"), rec.of("probe-b")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("checks a=%d b=%d", len(a), len(b))
	}
	if a[0] == nil || b[0] == nil || a[0] == b[0] {
		t.Fatal("two probes share a store")
	}
}

// TestSchedulerRateStoreKeptOnIntervalChange covers WP-994 AK7: an
// interval change keeps the store and its previous values.
func TestSchedulerRateStoreKeptOnIntervalChange(t *testing.T) {
	clock := newFakeClock(epochStart)
	s, rec := newStoreScheduler(clock)
	id := "probe-interval-rate"
	s.Upsert(schedSpec(id, 10_000))
	before, _ := s.Store(id)
	before.Swap("g", rate.Num(1), epochStart)
	s.Upsert(schedSpec(id, 30_000))
	after, ok := s.Store(id)
	if !ok || after != before {
		t.Fatal("interval change replaced the store")
	}
	if _, _, ok := after.Previous("g"); !ok {
		t.Fatal("interval change lost the previous value")
	}
	drive(s, clock, epochStart.Add(31*time.Second))
	if got := rec.of(id); len(got) == 0 || got[0] != before {
		t.Fatal("check after the interval change ran with another store")
	}
}

// TestSchedulerRateStoreDroppedOnRemove covers WP-994 AK7: Remove drops
// the store; a probe added again starts with an empty one.
func TestSchedulerRateStoreDroppedOnRemove(t *testing.T) {
	clock := newFakeClock(epochStart)
	s, _ := newStoreScheduler(clock)
	id := "probe-removed-rate"
	s.Upsert(schedSpec(id, 10_000))
	old, _ := s.Store(id)
	old.Swap("g", rate.Num(1), epochStart)
	s.Remove(id)
	if _, ok := s.Store(id); ok {
		t.Fatal("store kept after Remove")
	}
	s.Upsert(schedSpec(id, 10_000))
	fresh, ok := s.Store(id)
	if !ok || fresh == old {
		t.Fatal("re-added probe reuses the removed store")
	}
	if _, _, ok := fresh.Previous("g"); ok {
		t.Fatal("re-added probe sees a previous value of the removed one")
	}
}

// TestSchedulerRateStoreResetOnTargetChange covers WP-994 AK7: a changed
// target is another device, so its readings start with an empty store
// instead of being compared with the old device's counters.
func TestSchedulerRateStoreResetOnTargetChange(t *testing.T) {
	clock := newFakeClock(epochStart)
	s, _ := newStoreScheduler(clock)
	id := "probe-target-rate"
	s.Upsert(schedSpec(id, 10_000))
	old, _ := s.Store(id)
	old.Observe("in", 5_000_000, 64, epochStart, 0)
	spec := schedSpec(id, 10_000)
	spec.Target = "192.0.2.99"
	s.Upsert(spec)
	fresh, ok := s.Store(id)
	if !ok || fresh == old {
		t.Fatal("target change kept the store of the old device")
	}
	if r, ok := fresh.Observe("in", 1000, 64, epochStart.Add(10*time.Second), 0); ok {
		t.Fatalf("first reading of the new device yielded rate %v", r)
	}
	// The same target again (e.g. a threshold change) keeps the store.
	s.Upsert(spec)
	if again, _ := s.Store(id); again != fresh {
		t.Fatal("unchanged target replaced the store")
	}
}

// TestSchedulerRateStoreLimitCoversCountersPerRow covers WP-994 AK3: the
// series limit is derived from max_rows, but a row holds several counters;
// max_rows rows with four counters each and sysUpTime all fit.
func TestSchedulerRateStoreLimitCoversCountersPerRow(t *testing.T) {
	var typ probewire.Type
	for _, c := range probewire.Types() {
		if c.MaxRows > typ.MaxRows {
			typ = c
		}
	}
	if typ.MaxRows <= 1 {
		t.Skip("no probe type with max_rows > 1 in the contract")
	}
	clock := newFakeClock(epochStart)
	s, _ := newStoreScheduler(clock)
	spec := schedSpec("probe-rows", 10_000)
	spec.Type = typ.Key
	s.Upsert(spec)
	st, _ := s.Store(spec.ID)
	st.Uptime(100, epochStart)
	for row := range int(typ.MaxRows) {
		for _, c := range []string{"in", "out", "errors", "discards"} {
			st.Observe(fmt.Sprintf("%d/%s", row, c), 1, 64, epochStart, 0)
		}
	}
	if got := st.Stats(); got.Rejected != 0 {
		t.Fatalf("%s: %d series rejected below max_rows × counters per row", typ.Key, got.Rejected)
	}
}
