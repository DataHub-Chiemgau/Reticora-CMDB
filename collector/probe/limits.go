package probe

import (
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeLimits are the runtime limits of one probe type on a collector
// (PRT-01, E-154): concurrent checks per collector and per target and
// checks per second per collector. Zero disables a limit.
type TypeLimits struct {
	ParallelPerCollector int
	ParallelPerTarget    int
	RatePerCollector     int
}

// limitsOf reads the limits of a type from the type register.
func limitsOf(typ string) TypeLimits {
	t, ok := probewire.Lookup(typ)
	if !ok {
		return TypeLimits{}
	}
	return TypeLimits{
		ParallelPerCollector: t.MaxParallelPerCollector,
		ParallelPerTarget:    t.MaxParallelPerTarget,
		RatePerCollector:     t.MaxRatePerCollector,
	}
}

// Limits enforces parallelism and rate per probe type at runtime (WP-951
// AK3): a semaphore per type (max_parallel_per_collector), a semaphore per
// type and target (max_parallel_per_target) and a token bucket per type
// (max_rate_per_collector). A check that finds no free budget is not
// executed; the caller reports error with code limit_exceeded (→ unknown)
// and Limits counts the rejection per type.
//
// Limits is safe for concurrent use.
type Limits struct {
	// Lookup returns the limits of a type; nil reads the type register.
	Lookup func(typ string) TypeLimits

	mu       sync.Mutex
	types    map[string]*typeBudget
	exceeded map[string]uint64
}

type typeBudget struct {
	limits    TypeLimits
	running   int
	perTarget map[string]int
	tokens    float64
	last      time.Time
}

// NewLimits returns limits read from the type register.
func NewLimits() *Limits { return &Limits{} }

func (l *Limits) budget(typ string) *typeBudget {
	if l.types == nil {
		l.types = map[string]*typeBudget{}
		l.exceeded = map[string]uint64{}
	}
	b, ok := l.types[typ]
	if !ok {
		lookup := l.Lookup
		if lookup == nil {
			lookup = limitsOf
		}
		lim := lookup(typ)
		// The bucket starts full: a collector may start one second's worth
		// of checks right away.
		b = &typeBudget{limits: lim, perTarget: map[string]int{}, tokens: float64(lim.RatePerCollector)}
		l.types[typ] = b
	}
	return b
}

// Acquire reserves budget for one check of a probe of type typ against
// target at now. On success it returns a release function the caller
// calls exactly once after the check; otherwise ok is false and the
// rejection is counted for the type.
func (l *Limits) Acquire(typ, target string, now time.Time) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.budget(typ)
	lim := b.limits
	if lim.RatePerCollector > 0 {
		if !b.last.IsZero() && now.After(b.last) {
			b.tokens += now.Sub(b.last).Seconds() * float64(lim.RatePerCollector)
			if limit := float64(lim.RatePerCollector); b.tokens > limit {
				b.tokens = limit
			}
		}
		if b.last.IsZero() || now.After(b.last) {
			b.last = now
		}
	}
	switch {
	case lim.ParallelPerCollector > 0 && b.running >= lim.ParallelPerCollector,
		lim.ParallelPerTarget > 0 && b.perTarget[target] >= lim.ParallelPerTarget,
		lim.RatePerCollector > 0 && b.tokens < 1:
		l.exceeded[typ]++
		return nil, false
	}
	if lim.RatePerCollector > 0 {
		b.tokens--
	}
	b.running++
	b.perTarget[target]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			b.running--
			b.perTarget[target]--
			if b.perTarget[target] <= 0 {
				delete(b.perTarget, target)
			}
		})
	}, true
}

// Exceeded returns the number of checks of type typ rejected for lack of
// budget (counter per type, PRT-01).
func (l *Limits) Exceeded(typ string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exceeded[typ]
}

// Running returns the number of checks of type typ in progress.
func (l *Limits) Running(typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.types[typ]; ok {
		return b.running
	}
	return 0
}
