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
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// Default limits of API-04: 600 requests per minute per API key or
// session, 6,000 per minute per organization.
const (
	DefaultCredentialPerMinute   = 600
	DefaultOrganizationPerMinute = 6000
)

// RateLimits are the per-minute budgets of the authenticated rate limit
// (API-04). PerCredential counts one API key or one session (token id);
// PerOrganization counts all requests of an organization. Requests without
// a principal are counted per client IP with PerCredential.
type RateLimits struct {
	PerCredential   int
	PerOrganization int
}

func (l RateLimits) withDefaults() RateLimits {
	if l.PerCredential <= 0 {
		l.PerCredential = DefaultCredentialPerMinute
	}
	if l.PerOrganization <= 0 {
		l.PerOrganization = DefaultOrganizationPerMinute
	}
	return l
}

// RateLimiter returns the rate limit of API-04 with an in-memory store
// (single instance, tests); the organization budget is the default.
func RateLimiter(requestsPerMinute int) func(http.Handler) http.Handler {
	return RateLimiterWithLimits(RateLimits{PerCredential: requestsPerMinute}, cache.NewMemoryStore())
}

// RateLimiterWithStore returns the rate limit of API-04 on a shared store
// (Redis) with the default organization budget.
func RateLimiterWithStore(requestsPerMinute int, store cache.Store) func(http.Handler) http.Handler {
	return RateLimiterWithLimits(RateLimits{PerCredential: requestsPerMinute}, store)
}

// RateLimiterWithLimits enforces the per-credential and the per-organization
// budget (API-04) with fixed one-minute windows in the store, so all
// replicas share them. Every response carries X-RateLimit-Limit,
// X-RateLimit-Remaining and X-RateLimit-Reset for the tighter budget; a
// refused request gets 429 with Retry-After. A store error fails closed.
func RateLimiterWithLimits(limits RateLimits, store cache.Store) func(http.Handler) http.Handler {
	limits = limits.withDefaults()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The operational probes must report the state of the
			// dependencies themselves: a Redis outage would otherwise turn
			// /readyz into 429 instead of 503 with "redis": "unavailable"
			// (OPS-06).
			if isOperationalProbe(r) {
				next.ServeHTTP(w, r)
				return
			}
			decision, err := limits.take(r, store, time.Now().UTC())
			if err != nil {
				// Fail closed: a cache outage must not turn the rate limiter
				// off, otherwise brute-force protection silently disappears
				// exactly when the system is under stress.
				slog.Warn("rate limiter backend error, rejecting request", "error", err)
				httpx.RateLimited(w, r, "rate limiter unavailable, try again later")
				return
			}
			decision.writeHeaders(w)
			if !decision.allowed {
				w.Header().Set("Retry-After", strconv.Itoa(decision.reset))
				httpx.RateLimited(w, r, "rate limit exceeded for this "+decision.scope+", try again later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateDecision is the outcome of one request against its budgets: the
// budget closest to exhaustion decides the headers.
type rateDecision struct {
	allowed   bool
	limit     int
	remaining int
	reset     int // seconds until the window ends
	scope     string
}

func (d rateDecision) writeHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(d.limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(d.remaining))
	h.Set("X-RateLimit-Reset", strconv.Itoa(d.reset))
}

// take counts the request in its budgets and reports the decision.
func (l RateLimits) take(r *http.Request, store cache.Store, now time.Time) (rateDecision, error) {
	window := now.Unix() / 60
	reset := int(60 - now.Unix()%60)
	type budget struct {
		key, scope string
		limit      int
	}
	budgets := []budget{{rateLimitKey(r), "credential", l.PerCredential}}
	if principal, ok := PrincipalFromContext(r.Context()); ok && principal.OrganizationID != "" {
		budgets = append(budgets, budget{"org:" + principal.OrganizationID, "organization", l.PerOrganization})
	}
	d := rateDecision{allowed: true, remaining: -1, reset: reset}
	for _, b := range budgets {
		count, err := store.Increment(r.Context(), fmt.Sprintf("ratelimit:%s:%d", b.key, window), 2*time.Minute)
		if err != nil {
			return rateDecision{}, err
		}
		remaining := max(int64(b.limit)-count, 0)
		if count > int64(b.limit) {
			d.allowed = false
			if d.scope == "" {
				d.scope = b.scope
			}
		}
		if d.remaining < 0 || int(remaining) < d.remaining {
			d.remaining, d.limit = int(remaining), b.limit
		}
	}
	if d.scope == "" {
		d.scope = "credential"
	}
	return d, nil
}

// isOperationalProbe reports the unauthenticated health and metrics
// endpoints (/healthz, /readyz, /metrics), which are exempt from the
// Redis-backed rate limit.
func isOperationalProbe(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}

// rateLimitKey derives the bucket key from the authenticated principal so the
// caller cannot influence its own limit by rotating headers: the credential
// (API key or session token id) when known, otherwise the principal.
// Requests without a principal (public auth endpoints) are bucketed by
// client IP, which keeps credential-stuffing and code-exchange brute force
// throttled.
func rateLimitKey(r *http.Request) string {
	if principal, ok := PrincipalFromContext(r.Context()); ok && principal.OrganizationID != "" {
		if principal.Credential != "" {
			return "cred:" + principal.OrganizationID + ":" + principal.Credential
		}
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

// ClientIP returns the client address as the IP-based limits see it: the
// peer of the connection, or X-Real-IP when the peer is a trusted proxy.
func ClientIP(r *http.Request) string { return clientIP(r) }

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
// login code exchange, session refresh, collector enrollment and self-signup.
var preAuthPaths = map[string]bool{
	"/api/v1/auth/callback":     true,
	"/api/v1/auth/refresh":      true,
	"/api/v1/collectors/enroll": true,
	// Invitation acceptance (TLC-03).
	"/api/v1/invitations/accept": true,
	// Self-signup request and verification (TLC-01).
	"/api/v1/signup":        true,
	"/api/v1/signup/verify": true,
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
