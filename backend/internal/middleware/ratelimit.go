package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// rateLimiterStore is a simple in-memory rate limiter.
// In production, this should be backed by Redis Token-Bucket.
type rateLimiterStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rpm     int
}

// RateLimiter returns middleware that enforces a token-bucket rate limit.
// Default: 600 requests/minute per key (user ID or API key).
// The key is extracted from X-Organization-ID + authenticated user/API key.
func RateLimiter(requestsPerMinute int) func(http.Handler) http.Handler {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 600
	}

	store := &rateLimiterStore{
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

func (s *rateLimiterStore) allow(key string) bool {
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
