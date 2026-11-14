package probe

import (
	"context"
	"hash/fnv"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Clock is the time source of the scheduler; tests inject a fake one.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock is the wall clock.
var SystemClock Clock = systemClock{}

// WheelResolution is the slot width of the timing wheel (WP-951 AK2).
const WheelResolution = 100 * time.Millisecond

// wheelSlots is the number of slots of the wheel (1024 × 100 ms ≈ 102 s
// per revolution). An entry due later than one revolution stays in its
// slot until its tick comes round.
const wheelSlots = 1024

// Phase returns the start phase of a probe within its interval (PRB-04,
// WP-951 AK1): FNV-1a-64(probe_id) mod interval_ms. It depends only on the
// probe ID and the interval and therefore stays the same across a restart
// and a change of collector.
func Phase(probeID string, intervalMS int64) int64 {
	if intervalMS <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(probeID))
	return int64(h.Sum64() % uint64(intervalMS)) //nolint:gosec // intervalMS > 0 and the remainder fits into int64
}

// NextStart returns the next instant t ≥ now with t ≡ phase (mod interval),
// counted from the Unix epoch in milliseconds (WP-951 AK1).
func NextStart(probeID string, intervalMS int64, now time.Time) time.Time {
	if intervalMS <= 0 {
		return now
	}
	phase := Phase(probeID, intervalMS)
	nowMS := now.UnixMilli()
	if now.Sub(time.UnixMilli(nowMS)) > 0 {
		nowMS++ // round sub-millisecond remainders up: t ≥ now
	}
	base := nowMS - mod(nowMS, intervalMS)
	t := base + phase
	if t < nowMS {
		t += intervalMS
	}
	return time.UnixMilli(t)
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// RunFunc executes one check; Run of this package satisfies it.
type RunFunc func(ctx context.Context, spec *probewire.ProbeSpec, creds map[string]string) probewire.Result

// SchedulerStats are the counters of the scheduler. Skipped counts checks
// rejected for lack of budget (limit_exceeded), reported in the alive
// message (WP-957); Overrun counts starts left out because the previous
// check of the same probe was still running (WP-951 AK4).
type SchedulerStats struct {
	Probes   int
	Started  uint64
	Skipped  uint64
	Overrun  uint64
	Finished uint64
}

// Scheduler starts the checks of the probes assigned to the collector
// (PRB-04, PRT-01). Every probe starts at its hash phase within its
// interval; a timing wheel with 100 ms resolution holds the due probes.
// Upsert and Remove take the diffs of the assignment set (WP-956).
//
// The scheduler hands every result, including the limit_exceeded results
// of checks it did not start, to Sink. It holds no probe state beyond the
// running flag and the previous-value store of each probe (collector/probe/
// rate, WP-994 AK7), which it puts into the context of every check; the
// state machine (WP-952) evaluates the results.
type Scheduler struct {
	// Clock is the time source; nil uses the wall clock.
	Clock Clock
	// Run executes one check; nil uses Run of this package.
	Run RunFunc
	// Creds returns the resolved credentials of a probe; nil passes none.
	Creds func(spec *probewire.ProbeSpec) map[string]string
	// Sink receives every result. It is called from the goroutine of the
	// check (limit_exceeded results from the caller of Advance) and must be
	// safe for concurrent use.
	Sink func(probewire.Result)
	// Limits enforces parallelism and rate per type; nil reads the type
	// register.
	Limits *Limits
	// Go starts a check; nil starts a goroutine. Tests run checks inline.
	Go func(func())

	mu       sync.Mutex
	ctx      context.Context
	entries  map[string]*schedEntry
	running  map[string]*runningCheck
	stores   map[string]*rate.Store
	triggers map[string]time.Time // last TriggerNow per probe
	slots    [wheelSlots][]wheelRef
	lastTick int64
	started  bool
	gen      uint64

	nStarted, nSkipped, nOverrun, nFinished atomic.Uint64

	inflight sync.WaitGroup
}

// runningCheck is a check in progress: cancel ends its context, removed
// marks a check whose probe was removed meanwhile (its result is dropped,
// WP-956 AK3).
type runningCheck struct {
	cancel  context.CancelFunc
	removed bool
}

type schedEntry struct {
	spec    probewire.ProbeSpec
	due     time.Time
	dueTick int64
	gen     uint64
}

// wheelRef is the reference of one generation of an entry in a slot. A
// re-scheduled or removed entry leaves its old generation behind, which
// the tick skips (lazy deletion).
type wheelRef struct {
	id      string
	gen     uint64
	dueTick int64
}

func (s *Scheduler) init() {
	if s.entries == nil {
		s.entries = map[string]*schedEntry{}
		s.running = map[string]*runningCheck{}
		s.stores = map[string]*rate.Store{}
		s.triggers = map[string]time.Time{}
	}
	if s.Limits == nil {
		s.Limits = NewLimits()
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
}

func (s *Scheduler) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

// tickOf returns the tick that serves a due time: a due time between two
// ticks is served by the following tick, so a check never starts before
// its due time and at most one resolution after it.
func tickOf(t time.Time) int64 {
	ms := t.UnixMilli()
	res := WheelResolution.Milliseconds()
	return (ms + res - 1) / res
}

// Upsert adds a probe or changes it (WP-951 AK2). A new probe starts at
// its hash phase; a changed interval keeps the hash phase of the probe for
// the new interval; any other change takes effect with the next start.
// A probe without a positive interval is never started: it would be due
// in every tick and exhaust the budget of its type; an existing entry of
// that probe is removed.
func (s *Scheduler) Upsert(spec probewire.ProbeSpec) { //nolint:gocritic // the spec is copied into the scheduler on purpose
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if spec.IntervalMS <= 0 {
		delete(s.entries, spec.ID)
		delete(s.stores, spec.ID)
		return
	}
	// The previous-value store of the probe (WP-994 AK7): created with a
	// new probe and kept with its previous values on an interval change,
	// which only moves the staleness bound. A changed target or type is
	// another device: its readings must not be compared with the previous
	// values of the old one, so the store starts empty.
	interval := time.Duration(spec.IntervalMS) * time.Millisecond
	e, ok := s.entries[spec.ID]
	st := s.stores[spec.ID]
	if st != nil && ok && (e.spec.Target != spec.Target || e.spec.Type != spec.Type) {
		st = nil
	}
	if st != nil {
		st.SetInterval(interval)
	} else {
		s.stores[spec.ID] = rate.NewStore(maxSeriesOf(spec.Type), interval)
	}
	if ok && e.spec.IntervalMS == spec.IntervalMS {
		e.spec = spec
		return
	}
	if !ok {
		e = &schedEntry{}
		s.entries[spec.ID] = e
	}
	e.spec = spec
	s.scheduleLocked(e, NextStart(spec.ID, spec.IntervalMS, s.now()))
}

// seriesPerRow is the number of series one row of a probe type may hold
// in the previous-value store: an interface row has in/out octets, errors
// and discards (WP-996) plus room for further counters and enum states.
const seriesPerRow = 8

// probeWideSeries is the room for series of the probe as a whole, such as
// rate.UptimeSeries.
const probeWideSeries = 16

// SeriesBounder is implemented by an executor whose probes may hold more
// series than max_rows × seriesPerRow, e.g. snmp_custom with up to 64
// counter columns of max_rows rows each (WP-997); MaxSeries is the series
// limit of one probe's previous-value store.
type SeriesBounder interface {
	MaxSeries() int
}

// maxSeriesOf returns the series limit of a probe's previous-value store
// (WP-994 AK3): the bound of the executor (SeriesBounder), else derived
// from max_rows of the type in the contract — a row is not a series, so
// max_rows × seriesPerRow plus probe-wide series — or the store default.
func maxSeriesOf(typ string) int {
	if e, ok := Lookup(typ); ok {
		if b, ok := e.(SeriesBounder); ok && b.MaxSeries() > 0 {
			return b.MaxSeries()
		}
	}
	if t, ok := probewire.Lookup(typ); ok && t.MaxRows > 0 {
		return int(t.MaxRows)*seriesPerRow + probeWideSeries
	}
	return rate.DefaultMaxSeries
}

// Store returns the previous-value store of a probe.
func (s *Scheduler) Store(id string) (*rate.Store, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stores[id]
	return st, ok
}

// Remove stops a probe; it is not started again. A check in progress is
// cancelled (its context ends) and its result is not reported (WP-956 AK3).
// The previous-value store of the probe is dropped (WP-994 AK7).
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	delete(s.stores, id)
	delete(s.triggers, id)
	if rc := s.running[id]; rc != nil {
		rc.removed = true
		rc.cancel()
	}
}

// Len returns the number of scheduled probes.
func (s *Scheduler) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Next returns the next start of a probe.
func (s *Scheduler) Next(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return time.Time{}, false
	}
	return e.due, true
}

// scheduleLocked puts a new generation of e into the slot of due. A due
// time in a tick the wheel has already served moves to the next tick.
func (s *Scheduler) scheduleLocked(e *schedEntry, due time.Time) {
	s.gen++
	e.gen = s.gen
	e.due = due
	e.dueTick = tickOf(due)
	if s.started && e.dueTick <= s.lastTick {
		e.dueTick = s.lastTick + 1
	}
	slot := mod(e.dueTick, wheelSlots)
	s.slots[slot] = append(s.slots[slot], wheelRef{id: e.spec.ID, gen: e.gen, dueTick: e.dueTick})
}

// Stats returns the counters of the scheduler.
func (s *Scheduler) Stats() SchedulerStats {
	return SchedulerStats{
		Probes:   s.Len(),
		Started:  s.nStarted.Load(),
		Skipped:  s.nSkipped.Load(),
		Overrun:  s.nOverrun.Load(),
		Finished: s.nFinished.Load(),
	}
}

// RunLoop drives the wheel with the clock every 100 ms until ctx ends.
// The context is passed to every check; its end cancels running checks.
func (s *Scheduler) RunLoop(ctx context.Context) {
	s.mu.Lock()
	s.init()
	s.ctx = ctx
	s.mu.Unlock()
	t := time.NewTicker(WheelResolution)
	defer t.Stop()
	s.Advance(s.now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Advance(s.now())
		}
	}
}

// Wait blocks until every started check has returned and handed its
// result to Sink. After RunLoop has returned no check is started any more,
// so Wait then ends once the running checks end.
func (s *Scheduler) Wait() { s.inflight.Wait() }

// dueCheck is a check to start, collected under the lock and started
// after it.
type dueCheck struct {
	spec  probewire.ProbeSpec
	ctx   context.Context
	store *rate.Store
}

// Advance serves every tick up to now: it starts the due checks and puts
// each probe back into the wheel at its next start (due + interval, so the
// phase is kept). Advance is called by RunLoop; tests call it with a fake
// clock.
func (s *Scheduler) Advance(now time.Time) {
	nowTick := now.UnixMilli() / WheelResolution.Milliseconds()
	s.mu.Lock()
	s.init()
	if !s.started || nowTick < s.lastTick {
		// Probes added before the first tick are placed anew relative to
		// the wheel's starting position. A clock stepped backwards (NTP
		// step, VM restore) rebuilds the wheel the same way: otherwise every
		// entry would keep its due time on the old time line and no probe
		// would start until the clock caught up again.
		s.started = true
		s.lastTick = nowTick - 1
		s.slots = [wheelSlots][]wheelRef{}
		for _, e := range s.entries {
			s.scheduleLocked(e, NextStart(e.spec.ID, e.spec.IntervalMS, now))
		}
	}
	// A large jump (suspend, clock step) visits every slot at most once;
	// a probe overdue by a whole interval starts once and continues at its
	// next phase instead of catching up every missed start.
	from := s.lastTick + 1
	if nowTick-from >= wheelSlots {
		from = nowTick - wheelSlots + 1
	}
	var due []dueCheck
	for tick := from; tick <= nowTick; tick++ {
		slot := mod(tick, wheelSlots)
		refs := s.slots[slot]
		keep := refs[:0]
		var again []*schedEntry
		for _, ref := range refs {
			e, ok := s.entries[ref.id]
			if !ok || e.gen != ref.gen {
				continue // removed or re-scheduled
			}
			if ref.dueTick > nowTick {
				keep = append(keep, ref) // due in a later revolution
				continue
			}
			due = append(due, dueCheck{spec: e.spec, store: s.stores[e.spec.ID]})
			again = append(again, e)
		}
		s.slots[slot] = keep
		s.lastTick = tick
		for _, e := range again {
			next := e.due.Add(time.Duration(e.spec.IntervalMS) * time.Millisecond)
			if !next.After(now) {
				next = NextStart(e.spec.ID, e.spec.IntervalMS, now.Add(time.Millisecond))
			}
			s.scheduleLocked(e, next)
		}
	}
	if nowTick > s.lastTick {
		s.lastTick = nowTick
	}
	// Overrun and budget are decided under the lock, so that two ticks
	// cannot start the same probe twice.
	var start []dueCheck
	var releases []func()
	var runs []*runningCheck
	ctx := s.ctx
	var skipped []probewire.Result
	for i := range due {
		d := &due[i]
		if s.running[d.spec.ID] != nil {
			s.nOverrun.Add(1)
			continue
		}
		release, ok := s.Limits.Acquire(d.spec.Type, d.spec.Target, now)
		if !ok {
			// No budget: no check, error with code limit_exceeded
			// (→ unknown, WP-951 AK3), counted as skipped.
			s.nSkipped.Add(1)
			skipped = append(skipped, probewire.Result{
				ProbeID: d.spec.ID, Epoch: d.spec.Epoch, ObservedAt: now.UTC(),
				Status: probewire.StatusError, Code: probewire.ReasonLimitExceeded,
			})
			continue
		}
		rctx, cancel := context.WithCancel(rate.WithStore(ctx, d.store))
		rc := &runningCheck{cancel: cancel}
		s.running[d.spec.ID] = rc
		d.ctx = rctx
		start = append(start, *d)
		releases = append(releases, release)
		runs = append(runs, rc)
	}
	s.mu.Unlock()

	if s.Sink != nil {
		for i := range skipped {
			s.Sink(skipped[i])
		}
	}
	for i := range start {
		s.start(start[i].ctx, start[i].spec, releases[i], runs[i])
	}
}

// TriggerDebounce is the shortest time between two immediate checks of a
// probe by TriggerNow (protection against trap storms, WP-979 AK4).
const TriggerDebounce = 10 * time.Second

// TriggerNow starts an immediate check, outside its phase, of every probe
// whose target is the address target (WP-979 AK4: after a linkDown,
// linkUp, coldStart or warmStart trap). The check runs under the
// parallelism and rate budget of its type; without budget it is skipped
// and counted (limit_exceeded), nothing is queued. A probe is triggered at
// most once in TriggerDebounce, a check still running is not started a
// second time, and the phase of the probe stays as it is. It returns the
// number of checks started.
func (s *Scheduler) TriggerNow(target string) int {
	addr, err := netip.ParseAddr(strings.TrimSpace(target))
	if err != nil {
		return 0
	}
	addr = addr.Unmap()
	now := s.now()
	s.mu.Lock()
	s.init()
	var start []dueCheck
	var releases []func()
	var runs []*runningCheck
	var skipped []probewire.Result
	ctx := s.ctx
	for id, e := range s.entries {
		if !sameTargetAddr(e.spec.Target, addr) {
			continue
		}
		if last, ok := s.triggers[id]; ok && now.Sub(last) < TriggerDebounce {
			continue
		}
		if s.running[id] != nil {
			continue
		}
		s.triggers[id] = now
		release, ok := s.Limits.Acquire(e.spec.Type, e.spec.Target, now)
		if !ok {
			s.nSkipped.Add(1)
			skipped = append(skipped, probewire.Result{ProbeID: id, Epoch: e.spec.Epoch, ObservedAt: now.UTC(),
				Status: probewire.StatusError, Code: probewire.ReasonLimitExceeded})
			continue
		}
		rctx, cancel := context.WithCancel(rate.WithStore(ctx, s.stores[id]))
		rc := &runningCheck{cancel: cancel}
		s.running[id] = rc
		start = append(start, dueCheck{spec: e.spec, ctx: rctx})
		releases = append(releases, release)
		runs = append(runs, rc)
	}
	s.mu.Unlock()
	if s.Sink != nil {
		for i := range skipped {
			s.Sink(skipped[i])
		}
	}
	for i := range start {
		s.start(start[i].ctx, start[i].spec, releases[i], runs[i])
	}
	return len(start)
}

// sameTargetAddr reports whether the target of a probe (an address,
// optionally with a port) is addr.
func sameTargetAddr(target string, addr netip.Addr) bool {
	host := strings.TrimSpace(target)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && a.Unmap() == addr
}

func (s *Scheduler) start(ctx context.Context, spec probewire.ProbeSpec, release func(), rc *runningCheck) { //nolint:gocritic // the spec is copied into the job on purpose
	s.nStarted.Add(1)
	runFn := s.Run
	if runFn == nil {
		runFn = Run
	}
	var creds map[string]string
	if s.Creds != nil {
		creds = s.Creds(&spec)
	}
	s.inflight.Add(1)
	job := func() {
		defer s.inflight.Done()
		r := runFn(ctx, &spec, creds)
		release()
		s.mu.Lock()
		delete(s.running, spec.ID)
		removed := rc.removed
		s.mu.Unlock()
		rc.cancel()
		s.nFinished.Add(1)
		if s.Sink != nil && !removed {
			s.Sink(r)
		}
	}
	if s.Go != nil {
		s.Go(job)
		return
	}
	go job()
}
