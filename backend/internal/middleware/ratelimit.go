package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
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
// Default: 600 requests/minute per principal (user ID or API key).
// The key is derived exclusively from the authenticated principal context;
// unauthenticated requests (public auth endpoints) are keyed by client IP.
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
			if !store.allow(rateLimitKey(r)) {
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
			allowed, err := allowWithStore(r.Context(), store, rateLimitKey(r), requestsPerMinute)
			if err != nil {
				// Fail closed: a cache outage must not turn the rate limiter
				// off, otherwise brute-force protection silently disappears
				// exactly when the system is under stress.
				slog.Warn("rate limiter backend error, rejecting request", "error", err)
				httpx.RateLimited(w, r, "rate limiter unavailable, try again later")
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

// rateLimitKey derives the bucket key from the authenticated principal so the
// caller cannot influence its own limit by rotating headers. Requests without
// a principal (public auth endpoints) are bucketed by client IP, which keeps
// credential-stuffing and code-exchange brute force throttled.
func rateLimitKey(r *http.Request) string {
	if principal, ok := PrincipalFromContext(r.Context()); ok && principal.OrganizationID != "" {
		subject := principal.Subject
		if subject == "" {
			subject = "anonymous"
		}
		return "principal:" + principal.OrganizationID + ":" + string(principal.Type) + ":" + subject
	}
	return "ip:" + clientIP(r)
}

// clientIP extracts the client IP from RemoteAddr, stripping the port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || host == "" {
		if trimmed := strings.TrimSpace(r.RemoteAddr); trimmed != "" {
			return trimmed
		}
		return "unknown"
	}
	return host
}
