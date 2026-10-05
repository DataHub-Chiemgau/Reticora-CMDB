package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
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

// trustedProxies are the peers whose X-Real-IP header names the client
// (SetTrustedProxies). The bundled nginx sets X-Real-IP to the address it
// sees, so behind it every client keeps its own pre-auth budget.
var trustedProxies = mustPrefixList("127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")

func mustPrefixList(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// SetTrustedProxies replaces the trusted reverse proxies
// (RETICORA_TRUSTED_PROXIES, comma-separated CIDRs; empty trusts none).
func SetTrustedProxies(cidrs string) error {
	var out []netip.Prefix
	for _, c := range strings.Split(cidrs, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("trusted proxy %q: %w", c, err)
		}
		out = append(out, p)
	}
	trustedProxies = out
	return nil
}

// clientIP returns the client address: the peer of the connection, or the
// X-Real-IP header when the peer is a trusted reverse proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || host == "" {
		if trimmed := strings.TrimSpace(r.RemoteAddr); trimmed != "" {
			return trimmed
		}
		return "unknown"
	}
	if peer, parseErr := netip.ParseAddr(host); parseErr == nil {
		for _, p := range trustedProxies {
			if p.Contains(peer.Unmap()) {
				if forwarded, realErr := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); realErr == nil {
					return forwarded.Unmap().String()
				}
				break
			}
		}
	}
	return host
}

// DefaultPreAuthPerMinute is the pre-auth budget per client IP (AUT-10).
const DefaultPreAuthPerMinute = 20

// preAuthPaths are the unauthenticated endpoints that verify a credential:
// login code exchange, session refresh and collector enrollment.
var preAuthPaths = map[string]bool{
	"/api/v1/auth/callback":     true,
	"/api/v1/auth/refresh":      true,
	"/api/v1/collectors/enroll": true,
	// Operator login (SEC-07).
	"/api/v1/admin/auth/callback": true,
}

// PreAuthRateLimiter limits authentication attempts per client IP before any
// credential is checked (AUT-10, API-04). Every request to a pre-auth
// endpoint and every request whose authentication failed (401, including
// invalid API keys and session tokens) counts against the budget of the
// current minute. Once it is spent, pre-auth requests and requests to
// authenticated endpoints get 429 until the minute is over. A store error
// fails closed.
func PreAuthRateLimiter(perMinute int, store cache.Store) func(http.Handler) http.Handler {
	if perMinute <= 0 {
		perMinute = DefaultPreAuthPerMinute
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			preAuth := preAuthPaths[r.URL.Path]
			// The operator path authenticates itself; its failed attempts
			// count against the same budget.
			if !preAuth && !requiresAuth(r) && !strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
				next.ServeHTTP(w, r)
				return
			}
			windowKey := fmt.Sprintf("preauth:%s:%d", clientIP(r), time.Now().UTC().Unix()/60)
			used, err := preAuthUsed(r.Context(), store, windowKey)
			if err != nil {
				slog.Warn("pre-auth rate limiter backend error, rejecting request", "error", err)
				httpx.RateLimited(w, r, "rate limiter unavailable, try again later")
				return
			}
			if used >= int64(perMinute) {
				httpx.RateLimited(w, r, "too many authentication attempts, try again later")
				return
			}
			if preAuth {
				if _, err = store.Increment(r.Context(), windowKey, 2*time.Minute); err != nil {
					slog.Warn("pre-auth rate limiter backend error, rejecting request", "error", err)
					httpx.RateLimited(w, r, "rate limiter unavailable, try again later")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			if rw.status == http.StatusUnauthorized {
				if _, err = store.Increment(r.Context(), windowKey, 2*time.Minute); err != nil {
					slog.Warn("pre-auth rate limiter: failed attempt not counted", "error", err)
				}
			}
		})
	}
}

func preAuthUsed(ctx context.Context, store cache.Store, key string) (int64, error) {
	value, found, err := store.Get(ctx, key)
	if err != nil || !found {
		return 0, err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}
