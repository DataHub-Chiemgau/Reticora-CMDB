// Package rate is the previous-value store of the collector probes (E-185,
// WP-994). A Store belongs to one probe and keeps, per series, the last
// observation with its time stamp:
//
//   - counter series (Observe): the store turns two readings of a 32- or
//     64-bit counter into a rate per second, handles the wrap of the counter
//     and produces no rate after a counter discontinuity (changed marker
//     such as ifCounterDiscontinuityTime, or a reboot seen through a
//     backward step of sysUpTime);
//   - gauge and enum series (Previous, Swap): the store hands out the
//     previous value, so that an executor can detect a reboot or a changed
//     enum state without holding state itself (WP-950 AK4).
//
// The number of series per probe is bounded (max_series); a series not
// observed for three intervals is dropped. The scheduler of collector/probe
// keeps one Store per probe and puts it into the context of every check
// (WithStore); executors read it with FromContext.
//
// The package imports nothing from collector/plugins or collector/probe and
// needs no network (WP-994 AK5).
package rate

import (
	"context"
	"math"
	"sync"
	"time"
)

// DefaultMaxSeries bounds the series of a probe whose type sets no max_rows
// in the contract.
const DefaultMaxSeries = 4096

// staleIntervals is the number of intervals without an observation after
// which a series is dropped (WP-994 AK3).
const staleIntervals = 3

// UptimeSeries is the series name Uptime keeps sysUpTime under; executors
// reading the previous uptime use it with Previous.
const UptimeSeries = "sysUpTime"

// Value is a gauge or enum value of a Swap series: a number or, when Enum
// is set, the name of an enum state.
type Value struct {
	Number float64
	Enum   string
}

// Num returns a numeric value.
func Num(v float64) Value { return Value{Number: v} }

// Enum returns an enum value.
func Enum(name string) Value { return Value{Enum: name} }

// IsEnum reports whether the value is an enum state.
func (v Value) IsEnum() bool { return v.Enum != "" }

// Stats are the counters of a store.
type Stats struct {
	// Series is the number of series held at present.
	Series int
	// Rejected counts observations of new series refused at the
	// max_series limit.
	Rejected uint64
	// Implausible counts rates above max_rate that were dropped.
	Implausible uint64
	// Discontinuities counts observations without a rate because the
	// marker changed or the device rebooted.
	Discontinuities uint64
	// Evicted counts series dropped after three intervals without an
	// observation.
	Evicted uint64
}

type counterEntry struct {
	value         uint64
	at            time.Time
	discontinuity uint64
	boot          uint64
}

type swapEntry struct {
	value Value
	at    time.Time
}

// Store is the previous-value store of one probe. It is safe for
// concurrent use. The methods of a nil Store observe nothing and report
// ok = false, so that an executor run outside the scheduler needs no
// special case.
type Store struct {
	mu        sync.Mutex
	maxSeries int
	interval  time.Duration
	counters  map[string]counterEntry
	swaps     map[string]swapEntry
	boot      uint64
	lastSweep time.Time
	stats     Stats
}

// NewStore returns the store of a probe with the series limit maxSeries
// (≤ 0: DefaultMaxSeries) and the probe interval (staleness of series; ≤ 0:
// series never go stale).
func NewStore(maxSeries int, interval time.Duration) *Store {
	s := &Store{counters: map[string]counterEntry{}, swaps: map[string]swapEntry{}}
	s.SetMaxSeries(maxSeries)
	s.SetInterval(interval)
	return s
}

// SetMaxSeries changes the series limit (≤ 0: DefaultMaxSeries). Series
// already held beyond a lowered limit stay until they go stale.
func (s *Store) SetMaxSeries(n int) {
	if s == nil {
		return
	}
	if n <= 0 {
		n = DefaultMaxSeries
	}
	s.mu.Lock()
	s.maxSeries = n
	s.mu.Unlock()
}

// SetInterval changes the probe interval the staleness is measured in; the
// scheduler calls it when the interval of the probe changes and keeps the
// previous values.
func (s *Store) SetInterval(d time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interval = d
	s.mu.Unlock()
}

// Stats returns the counters of the store.
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Series = len(s.counters) + len(s.swaps)
	return st
}

// Observe records a reading of a counter of bits (32 or 64; any other value
// is taken as 64) and returns the rate per second since the previous
// reading of the same series (WP-994 AK1, AK2). There is no rate (ok =
// false) for
//
//   - the first reading of a series,
//   - a reading not later than the previous one (the previous value stays),
//   - a changed discontinuity marker (ifCounterDiscontinuityTime or 0 if the
//     device has none),
//   - a reboot reported by Uptime since the previous reading,
//   - a new series refused at the max_series limit.
//
// Except for the second case the reading becomes the new previous value. A
// counter smaller than its previous value without a discontinuity has
// wrapped: the difference is taken modulo 2^bits.
//
// Order within a poll: an executor that reads sysUpTime calls Uptime
// before it observes the counters of the same poll. Counters observed
// before Uptime are compared with the readings from before a reboot, and
// the reset counter is taken for a wrap.
func (s *Store) Observe(series string, value uint64, bits int, at time.Time, discontinuity uint64) (rate float64, ok bool) {
	return s.ObserveMax(series, value, bits, at, discontinuity, 0)
}

// ObserveMax is Observe with a plausibility bound (WP-994 AK4): a rate
// above maxRate (> 0; for interfaces ifHighSpeed × 1.1) is dropped and
// counted in Stats().Implausible; the reading still becomes the new
// previous value, so the next interval yields a rate again.
func (s *Store) ObserveMax(series string, value uint64, bits int, at time.Time, discontinuity uint64, maxRate float64) (rate float64, ok bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(at)
	prev, known := s.counters[series]
	if !known {
		if !s.admitLocked() {
			s.stats.Rejected++
			return 0, false
		}
		s.counters[series] = counterEntry{value: value, at: at, discontinuity: discontinuity, boot: s.boot}
		return 0, false
	}
	elapsed := at.Sub(prev.at)
	if elapsed <= 0 {
		return 0, false
	}
	s.counters[series] = counterEntry{value: value, at: at, discontinuity: discontinuity, boot: s.boot}
	if prev.discontinuity != discontinuity || prev.boot != s.boot {
		s.stats.Discontinuities++
		return 0, false
	}
	delta := value - prev.value // wraps modulo 2^64
	if bits == 32 {
		delta = (uint64(uint32(value)) - uint64(uint32(prev.value))) & math.MaxUint32
	}
	rate = float64(delta) / elapsed.Seconds()
	if maxRate > 0 && rate > maxRate {
		s.stats.Implausible++
		return 0, false
	}
	return rate, true
}

// Uptime records sysUpTime (in TimeTicks or any monotonic unit) of the
// device under UptimeSeries and reports whether it stepped back since the
// previous reading, which is a reboot: the counters of the device were
// reset, so the next Observe of every counter series yields no rate. The
// previous uptime is readable with Previous(UptimeSeries).
//
// Uptime must be called before the counter readings of the same poll (see
// Observe). The UptimeSeries is always admitted, even at the max_series
// limit, so that reboot detection never stops silently.
func (s *Store) Uptime(ticks uint64, at time.Time) (rebooted bool) {
	if s == nil {
		return false
	}
	prev, _, ok := s.Swap(UptimeSeries, Num(float64(ticks)), at)
	if ok && !prev.IsEnum() && float64(ticks) < prev.Number {
		s.mu.Lock()
		s.boot++
		s.mu.Unlock()
		return true
	}
	return false
}

// Previous returns the previous value of a gauge or enum series (WP-994
// AK6) without changing it.
func (s *Store) Previous(series string) (Value, time.Time, bool) {
	if s == nil {
		return Value{}, time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.swaps[series]
	if !ok {
		return Value{}, time.Time{}, false
	}
	return e.value, e.at, true
}

// Swap stores v as the value of a gauge or enum series and returns the
// previous one (ok = false: none, or the new series is refused at the
// max_series limit and v is not stored). Swap series count against
// max_series and go stale like counter series.
func (s *Store) Swap(series string, v Value, at time.Time) (prev Value, prevAt time.Time, ok bool) {
	if s == nil {
		return Value{}, time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(at)
	e, known := s.swaps[series]
	if !known && series != UptimeSeries && !s.admitLocked() {
		s.stats.Rejected++
		return Value{}, time.Time{}, false
	}
	s.swaps[series] = swapEntry{value: v, at: at}
	if !known {
		return Value{}, time.Time{}, false
	}
	return e.value, e.at, true
}

// admitLocked reports whether one more series fits into the limit.
func (s *Store) admitLocked() bool {
	return len(s.counters)+len(s.swaps) < s.maxSeries
}

// sweepLocked drops the series not observed for three intervals (WP-994
// AK3). It runs at most once per interval, measured in observation time.
func (s *Store) sweepLocked(now time.Time) {
	if s.interval <= 0 {
		return
	}
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.interval {
		return
	}
	s.lastSweep = now
	cutoff := now.Add(-staleIntervals * s.interval)
	for k, e := range s.counters {
		if e.at.Before(cutoff) {
			delete(s.counters, k)
			s.stats.Evicted++
		}
	}
	for k, e := range s.swaps {
		if e.at.Before(cutoff) {
			delete(s.swaps, k)
			s.stats.Evicted++
		}
	}
}

type ctxKey struct{}

// WithStore returns a context carrying the store of the probe (WP-994 AK6).
func WithStore(ctx context.Context, s *Store) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the store of the probe from the context of a check,
// or nil if the context carries none (the methods of a nil store report no
// previous value).
func FromContext(ctx context.Context) *Store {
	s, _ := ctx.Value(ctxKey{}).(*Store)
	return s
}
