package middleware

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// memoryRateLimiterStore is a simple in-memory rate limiter fallback.
type memoryRateLimiterStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rpm     int
}

// RateLimiter returns middleware that enforces a token-bucket rate limit.
// Default: 600 requests/minute per key (user ID or API key).
// The key is extracted from X-Organization-ID + authenticated user/API key.
// When a cache.Store is provided (Redis), it uses a sliding-window counter.
// Otherwise falls back to an in-memory token bucket.
func RateLimiter(requestsPerMinute int) func(http.Handler) http.Handler {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 600
	}

	store := &memoryRateLimiterStore{
		buckets: make(map[string]*tokenBucket),
		rpm:     requestsPerMinute,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rateLimitKey(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !store.allow(key) {
				httpx.RateLimited(w, r, "rate limit exceeded, try again later")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RateLimiterWithStore returns middleware using the provided cache store for
// distributed rate limiting via a sliding-window counter.
func RateLimiterWithStore(requestsPerMinute int, store cache.Store) func(http.Handler) http.Handler {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 600
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rateLimitKey(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, err := allowWithStore(r.Context(), store, key, requestsPerMinute)
			if err != nil {
				// On Redis errors, allow the request (fail-open)
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				httpx.RateLimited(w, r, "rate limit exceeded, try again later")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// allowWithStore implements a fixed-window rate limit using the cache store.
// Key format: ratelimit:{key}:{minute_timestamp}
func allowWithStore(ctx context.Context, store cache.Store, key string, rpm int) (bool, error) {
	now := time.Now().UTC()
	windowKey := fmt.Sprintf("ratelimit:%s:%d", key, now.Unix()/60)

	count, err := store.Increment(ctx, windowKey, 2*time.Minute)
	if err != nil {
		return false, err
	}

	return count <= int64(rpm), nil
}

func (s *memoryRateLimiterStore) allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket, exists := s.buckets[key]
	if !exists {
		bucket = &tokenBucket{
			tokens:     float64(s.rpm),
			maxTokens:  float64(s.rpm),
			refillRate: float64(s.rpm) / 60.0, // per second
			lastRefill: time.Now(),
		}
		s.buckets[key] = bucket
	}

	// Refill tokens
	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.tokens += elapsed * bucket.refillRate
	if bucket.tokens > bucket.maxTokens {
		bucket.tokens = bucket.maxTokens
	}
	bucket.lastRefill = now

	// Try to consume one token
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func rateLimitKey(r *http.Request) string {
	// Use org + user/apikey as the rate limit key
	orgID := r.Header.Get("X-Organization-ID")
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = r.RemoteAddr
	}
	if orgID == "" {
		return ""
	}
	return orgID + ":" + userID
}
