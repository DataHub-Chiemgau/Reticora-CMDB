// Package cache provides a Cache interface with Redis and in-memory implementations
// for rate limiting, idempotency, and general-purpose caching.
package cache

import (
	"context"
	"time"
)

// Store defines the cache operations needed by the middleware layer.
type Store interface {
	// Get retrieves a value by key. Returns empty string and false if not found.
	Get(ctx context.Context, key string) (string, bool, error)

	// Set stores a value with a TTL. If ttl is 0, the entry does not expire.
	Set(ctx context.Context, key string, value string, ttl time.Duration) error

	// Increment atomically increments a counter and returns the new value.
	// If the key does not exist, it is created with value 1 and the given TTL.
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// Decrement atomically decrements an existing counter and returns the
	// new value; the TTL stays unchanged. A missing or expired key is not
	// created (a released reservation must not outlive its window): the
	// result is 0.
	Decrement(ctx context.Context, key string) (int64, error)

	// Delete removes a key; a missing key is not an error.
	Delete(ctx context.Context, key string) error

	// WindowAdd keeps a sliding window of events under key: it drops the
	// events at or before now-window and records one at now when fewer
	// than limit remain (never for limit <= 0), atomically for all clients
	// of the store, so a window holds at most limit events. The result
	// counts the events in (now-window, now] afterwards.
	WindowAdd(ctx context.Context, key string, now time.Time, window time.Duration, limit int64) (Window, error)

	// WindowCount returns the events of key in (now-window, now] without
	// recording one.
	WindowCount(ctx context.Context, key string, now time.Time, window time.Duration) (Window, error)

	// Close releases any resources held by the store.
	Close() error
}

// Window is the state of a sliding window (WindowAdd, WindowCount).
type Window struct {
	// Added reports whether WindowAdd recorded the event.
	Added bool
	// Count is the number of events in the window.
	Count int64
	// Oldest is the time of the oldest event in the window; zero when the
	// window is empty. It leaves the window at Oldest+window.
	Oldest time.Time
}

// RetryAfter is the time from now until the oldest event leaves the
// window, at least a second; zero for an empty window.
func (w Window) RetryAfter(now time.Time, window time.Duration) time.Duration {
	if w.Oldest.IsZero() {
		return 0
	}
	d := w.Oldest.Add(window).Sub(now)
	if d < time.Second {
		return time.Second
	}
	return d
}
