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

	// Close releases any resources held by the store.
	Close() error
}
