package probe

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TestParallelLimitPerType covers PRT-01 and WP-951 AK3: the semaphore per
// type (max_parallel_per_collector) and per target
// (max_parallel_per_target) of the register are enforced; a rejection is
// counted per type.
func TestParallelLimitPerType(t *testing.T) {
	tcp, _ := probewire.Lookup(TypeTCP)
	if tcp.MaxParallelPerCollector < 2 || tcp.MaxParallelPerTarget < 1 {
		t.Fatalf("unexpected register limits: %+v", tcp)
	}
	l := NewLimits()
	now := epochStart
	// Per target: max_parallel_per_target checks of one target.
	var releases []func()
	for i := 0; i < tcp.MaxParallelPerTarget; i++ {
		rel, ok := l.Acquire(TypeTCP, "192.0.2.1", now)
		if !ok {
			t.Fatalf("check %d of the target rejected", i+1)
		}
		releases = append(releases, rel)
	}
	if _, ok := l.Acquire(TypeTCP, "192.0.2.1", now); ok {
		t.Fatal("check above max_parallel_per_target accepted")
	}
	if got := l.Exceeded(TypeTCP); got != 1 {
		t.Fatalf("exceeded = %d, want 1", got)
	}
	// Other targets still have budget.
	if rel, ok := l.Acquire(TypeTCP, "192.0.2.2", now); !ok {
		t.Fatal("other target rejected")
	} else {
		rel()
	}
	for _, r := range releases {
		r()
		r() // a second release is ignored
	}
	if l.Running(TypeTCP) != 0 {
		t.Fatalf("running = %d after release", l.Running(TypeTCP))
	}

	// Per collector, with limits from a lookup and no rate limit.
	l = &Limits{Lookup: func(typ string) TypeLimits {
		if typ == "a" {
			return TypeLimits{ParallelPerCollector: 3}
		}
		return TypeLimits{ParallelPerCollector: 1}
	}}
	for i := 0; i < 3; i++ {
		if _, ok := l.Acquire("a", targetN(i), now); !ok {
			t.Fatalf("check %d of type a rejected", i+1)
		}
	}
	if _, ok := l.Acquire("a", "other", now); ok {
		t.Fatal("4th check of type a accepted")
	}
	// The budget is per type: type b is not affected by a.
	if _, ok := l.Acquire("b", "t", now); !ok {
		t.Fatal("type b rejected because of type a")
	}
	if l.Exceeded("a") != 1 || l.Exceeded("b") != 0 {
		t.Fatalf("exceeded a=%d b=%d", l.Exceeded("a"), l.Exceeded("b"))
	}

	// Through the scheduler: with a parallel limit of 1 per collector and
	// two probes due in the same tick while the first still runs, the
	// second gets error/limit_exceeded without execution.
	clock := newFakeClock(epochStart)
	var mu sync.Mutex
	var results []probewire.Result
	hold := make(chan struct{})
	var wg sync.WaitGroup
	executed := map[string]int{}
	s := &Scheduler{
		Clock:  clock,
		Limits: &Limits{Lookup: func(string) TypeLimits { return TypeLimits{ParallelPerCollector: 1} }},
		Run: func(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
			mu.Lock()
			executed[spec.ID]++
			mu.Unlock()
			<-hold
			return probewire.Result{ProbeID: spec.ID, Status: probewire.StatusOK}
		},
		Sink: func(r probewire.Result) { mu.Lock(); results = append(results, r); mu.Unlock() },
		Go:   func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() },
	}
	// Two probe IDs with the same phase start in the same tick.
	a, b := samePhasePair(30_000)
	s.Upsert(schedSpec(a, 30_000))
	s.Upsert(schedSpec(b, 30_000))
	due, _ := s.Next(a)
	drive(s, clock, due.Add(2*WheelResolution))
	close(hold)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if executed[a]+executed[b] != 1 {
		t.Fatalf("executed = %v, want exactly one check", executed)
	}
	var limited int
	for _, r := range results {
		if r.Status == probewire.StatusError && r.Code == probewire.ReasonLimitExceeded {
			limited++
			if err := r.Validate(); err != nil {
				t.Fatalf("limit_exceeded result invalid: %v", err)
			}
		}
	}
	if limited != 1 || s.Stats().Skipped != 1 {
		t.Fatalf("limit_exceeded results = %d, skipped = %d, want 1/1", limited, s.Stats().Skipped)
	}
}

func targetN(i int) string { return "198.51.100." + string(rune('1'+i)) }

// samePhasePair finds two probe IDs whose phases fall into the same tick.
func samePhasePair(intervalMS int64) (string, string) {
	ids := sameTickIDs(2, intervalMS)
	return ids[0], ids[1]
}

// sameTickIDs finds n probe IDs whose phases fall into the same tick.
func sameTickIDs(n int, intervalMS int64) []string {
	seen := map[int64][]string{}
	for _, id := range probeIDs(100_000) {
		tick := Phase(id, intervalMS) / WheelResolution.Milliseconds()
		seen[tick] = append(seen[tick], id)
		if len(seen[tick]) == n {
			return seen[tick]
		}
	}
	panic("no probe IDs with a common tick")
}

// TestRateLimitPerTypeYieldsLimitExceeded covers PRT-01 and AK3: the
// token bucket per type (max_rate_per_collector) admits at most rate
// checks per second; the rest are rejected and counted, and the bucket
// refills with time.
func TestRateLimitPerTypeYieldsLimitExceeded(t *testing.T) {
	tcp, _ := probewire.Lookup(TypeTCP)
	rate := tcp.MaxRatePerCollector
	l := &Limits{Lookup: func(string) TypeLimits { return TypeLimits{RatePerCollector: rate} }}
	now := epochStart
	admitted := 0
	for i := 0; i < rate+50; i++ {
		if rel, ok := l.Acquire(TypeTCP, "t", now); ok {
			admitted++
			rel()
		}
	}
	if admitted != rate {
		t.Fatalf("admitted in one instant = %d, want %d", admitted, rate)
	}
	if got := l.Exceeded(TypeTCP); got != 50 {
		t.Fatalf("exceeded = %d, want 50", got)
	}
	// After 100 ms a tenth of the rate is available again.
	now = now.Add(100 * time.Millisecond)
	admitted = 0
	for i := 0; i < rate; i++ {
		if rel, ok := l.Acquire(TypeTCP, "t", now); ok {
			admitted++
			rel()
		}
	}
	if want := rate / 10; admitted != want {
		t.Fatalf("admitted after 100 ms = %d, want %d", admitted, want)
	}
	// The register's limits are used without a lookup.
	if got := limitsOf(TypeTCP); got.RatePerCollector != rate || got.ParallelPerCollector != tcp.MaxParallelPerCollector {
		t.Fatalf("limitsOf(tcp) = %+v", got)
	}

	// Through the scheduler: 3 probes due in one tick, rate 2/s → one
	// limit_exceeded result, no execution for it.
	clock := newFakeClock(epochStart)
	var mu sync.Mutex
	var results []probewire.Result
	runs := 0
	s := &Scheduler{
		Clock:  clock,
		Limits: &Limits{Lookup: func(string) TypeLimits { return TypeLimits{RatePerCollector: 2} }},
		Run: func(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
			mu.Lock()
			runs++
			mu.Unlock()
			return probewire.Result{ProbeID: spec.ID, Status: probewire.StatusOK}
		},
		Sink: func(r probewire.Result) { mu.Lock(); results = append(results, r); mu.Unlock() },
		Go:   inline,
	}
	ids := sameTickIDs(3, 30_000)
	for _, id := range ids {
		s.Upsert(schedSpec(id, 30_000))
	}
	due, _ := s.Next(ids[0])
	drive(s, clock, due.Add(2*WheelResolution))
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || s.Stats().Skipped != 1 {
		t.Fatalf("runs %d, skipped %d, want 2/1", runs, s.Stats().Skipped)
	}
	limited := 0
	for _, r := range results {
		if r.Code == probewire.ReasonLimitExceeded {
			limited++
			if r.Status != probewire.StatusError || r.ProbeID == "" || r.Epoch != 1 {
				t.Fatalf("limit_exceeded result = %+v", r)
			}
		}
	}
	if limited != 1 {
		t.Fatalf("limit_exceeded results = %d, want 1", limited)
	}
}
