package readonly

import (
	"context"
	"sync"
	"time"
)

// Operational guards of an account (ADM-03): a token bucket with the
// account's rate_limit_rps and burst 1, and a circuit breaker.

// clock is the time source of the guards (tests replace it).
type clock struct {
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func realClock() clock {
	return clock{now: time.Now, sleep: func(ctx context.Context, d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}}
}

// bucket is a token bucket with burst 1: one request every 1/rps.
type bucket struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newBucket(rps float64) *bucket {
	return &bucket{interval: time.Duration(float64(time.Second) / rps)}
}

// reserve returns how long the caller waits for its token and takes it;
// slot is the time of the token for unreserve.
func (b *bucket) reserve(now time.Time) (wait time.Duration, slot time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	at := b.next
	if at.Before(now) {
		at = now
	}
	b.next = at.Add(b.interval)
	return at.Sub(now), at
}

// unreserve gives back the token of slot when the caller gave up waiting
// and no later token was taken since.
func (b *bucket) unreserve(slot time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.next.Equal(slot.Add(b.interval)) {
		b.next = slot
	}
}

// Breaker thresholds: open after this many consecutive failures, for this
// long; then half open with one probe.
const (
	breakerThreshold = 5
	breakerOpenFor   = 60 * time.Second
)

// breaker is the circuit breaker of an account.
type breaker struct {
	mu       sync.Mutex
	failures int
	openedAt time.Time
	open     bool
	// probing: the half-open probe is running; other calls are refused.
	probing bool
}

// allow reports whether a call may go out; when the breaker is open and
// its time is up, the first caller becomes the probe.
func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.probing || now.Before(b.openedAt.Add(breakerOpenFor)) {
		return false
	}
	b.probing = true
	return true
}

// done records the outcome of an allowed call; failed counts against the
// breaker. It reports whether the breaker is open afterwards.
func (b *breaker) done(now time.Time, failed bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	wasProbe := b.probing
	b.probing = false
	if !failed {
		b.failures, b.open = 0, false
		return false
	}
	b.failures++
	if wasProbe || b.failures >= breakerThreshold {
		b.open, b.openedAt = true, now
	}
	return b.open
}

// release ends a probe that sent nothing (no outcome to record).
func (b *breaker) release() {
	b.mu.Lock()
	b.probing = false
	b.mu.Unlock()
}

// isOpen reports whether the breaker refuses calls at now.
func (b *breaker) isOpen(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open && (b.probing || now.Before(b.openedAt.Add(breakerOpenFor)))
}
