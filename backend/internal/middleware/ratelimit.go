package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

// Default limits of API-04: 600 requests per minute per API key or
// session, 6,000 per minute per organization.
const (
	DefaultCredentialPerMinute   = 600
	DefaultOrganizationPerMinute = 6000
)

// RateLimits are the per-minute budgets of the authenticated rate limit
// (API-04). PerCredential counts one API key or one session (sid, the
// refresh family of a login); PerOrganization counts the requests of an
// organization that their credential budget admitted. Requests without a
// principal are counted per client IP with PerCredential.
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
// replicas share them. The credential budget decides first; only a request
// it admits counts against the organization budget. Every response carries
// X-RateLimit-Limit, X-RateLimit-Remaining and X-RateLimit-Reset for the
// tighter budget; a refused request gets 429 with Retry-After. A store
// error fails closed.
func RateLimiterWithLimits(limits RateLimits, store cache.Store) func(http.Handler) http.Handler {
	return rateLimiter(limits, store, time.Now)
}

// rateLimiter is RateLimiterWithLimits with the clock that picks the
// one-minute window.
func rateLimiter(limits RateLimits, store cache.Store, now func() time.Time) func(http.Handler) http.Handler {
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
			decision, err := limits.take(r, store, now().UTC())
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

// take counts the request against its budgets and reports the decision.
// The credential budget comes first: a request it refuses is not counted
// against the organization budget, so a client that keeps retrying after
// 429 cannot use up the budget of the other credentials of its
// organization (API-04). Each budget decides on the value its atomic
// increment returns, so concurrent requests on all replicas never admit
// more than the limit.
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
	d := rateDecision{allowed: true, remaining: -1, reset: reset, scope: "credential"}
	for _, b := range budgets {
		count, err := store.Increment(r.Context(), fmt.Sprintf("ratelimit:%s:%d", b.key, window), 2*time.Minute)
		if err != nil {
			return rateDecision{}, err
		}
		if count > int64(b.limit) {
			// Refused by this budget: the budgets after it are not counted.
			return rateDecision{limit: b.limit, remaining: 0, reset: reset, scope: b.scope}, nil
		}
		if remaining := int(int64(b.limit) - count); d.remaining < 0 || remaining < d.remaining {
			d.remaining, d.limit = remaining, b.limit
		}
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
// (API key or session) when known, otherwise the principal.
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

// refreshPath is the pre-auth endpoint whose successful answers do not
// count: behind one NAT address the token renewals of many users would
// otherwise spend the budget and log them out (F-063).
const refreshPath = "/api/v1/auth/refresh"

// preAuthMaxWait bounds how long a request waits for a unit of its IP's
// budget that credential checks in flight hold (F-387). After it the
// request gets 429.
var preAuthMaxWait = 10 * time.Second

// The waiting request looks again after these intervals (doubling, with
// jitter), and at once when a unit is given back on this replica.
const (
	preAuthPollMin = 2 * time.Millisecond
	preAuthPollMax = 50 * time.Millisecond
)

// PreAuthRateLimiter limits authentication attempts per client IP before any
// credential is checked (AUT-10, API-04). Each request to a pre-auth
// endpoint or to an authenticated endpoint takes one unit of the budget of
// the current minute with an atomic increment before it is served; the
// value the increment returns decides, so concurrent requests on all
// replicas never have more than the budget checked. What counts:
//   - login callback, signup, signup verification, invitation acceptance,
//     enrollment and operator callback: every request;
//   - the session refresh: only answers outside 2xx, a successful refresh
//     gives its unit back;
//   - authenticated endpoints (API keys, sessions, operator path): only
//     failed authentications (401); a request gives its unit back as soon as
//     its authentication succeeds (or, on the operator path, when it is
//     answered with anything but 401).
//
// A unit that is not given back is spent: the budget counts it until the
// minute is over. A request that finds all units taken waits while some
// of them are only held by checks in flight (F-387: concurrent requests
// with valid credentials, e.g. of many users behind one NAT address, are
// not refused) and takes the first unit given back, at most
// preAuthMaxWait. Once the spent units fill the budget, pre-auth requests
// and requests to authenticated endpoints get 429 with Retry-After at once
// until the minute is over. Every response of a limited path carries
// X-RateLimit-Limit, -Remaining and -Reset of the IP budget; the rate limit
// after authentication overwrites them with the credential budget (API-04).
// A store error fails closed.
func PreAuthRateLimiter(perMinute int, store cache.Store) func(http.Handler) http.Handler {
	if perMinute <= 0 {
		perMinute = DefaultPreAuthPerMinute
	}
	l := &preAuthLimiter{store: store, limit: int64(perMinute), freed: make(chan struct{})}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			preAuth := preAuthPaths[r.URL.Path]
			// The operator path authenticates itself; its failed attempts
			// count against the same budget.
			if !preAuth && !requiresAuth(r) && !strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
				next.ServeHTTP(w, r)
				return
			}
			a, err := l.admit(r.Context(), clientIP(r))
			if err != nil {
				slog.Warn("pre-auth rate limiter backend error, rejecting request", "error", err)
				httpx.RateLimited(w, r, "rate limiter unavailable, try again later")
				return
			}
			h := w.Header()
			h.Set("X-RateLimit-Limit", strconv.Itoa(perMinute))
			h.Set("X-RateLimit-Remaining", strconv.FormatInt(max(int64(perMinute)-a.count, 0), 10))
			h.Set("X-RateLimit-Reset", strconv.FormatInt(a.win.reset, 10))
			if !a.admitted {
				h.Set("Retry-After", strconv.FormatInt(a.win.reset, 10))
				httpx.RateLimited(w, r, "too many authentication attempts, try again later")
				return
			}
			slot := &preAuthSlot{limiter: l, win: a.win, held: true}
			if preAuth && r.URL.Path != refreshPath {
				// Every request is an attempt.
				slot.spend(r.Context())
				next.ServeHTTP(w, r)
				return
			}
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			if preAuth {
				next.ServeHTTP(rw, r)
				if rw.status >= 200 && rw.status < 300 {
					slot.release(r.Context())
				} else {
					slot.spend(r.Context())
				}
				return
			}
			next.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), preAuthSlotKey{}, slot)))
			switch {
			case rw.status != http.StatusUnauthorized:
				slot.release(r.Context())
			case slot.released():
				// Authenticated, but answered 401 after all: a failed
				// authentication counts.
				l.countFailure(r.Context(), a.win)
			default:
				slot.spend(r.Context())
			}
		})
	}
}

// preAuthLimiter is the state of one PreAuthRateLimiter: the shared store
// and the signal that wakes the requests of this replica waiting for a
// unit.
type preAuthLimiter struct {
	store cache.Store
	limit int64
	mu    sync.Mutex
	freed chan struct{} // closed and replaced when a unit is given back or spent
}

// preAuthWindow names the counters of one client IP in one minute window:
// taken counts the admitted units (held by requests in flight or spent),
// spent the units that are never given back.
type preAuthWindow struct {
	taken, spent string
	reset        int64 // seconds until the window ends
}

func newPreAuthWindow(ip string, now time.Time) preAuthWindow {
	sec := now.UTC().Unix()
	return preAuthWindow{
		taken: fmt.Sprintf("preauth:%s:%d", ip, sec/60),
		spent: fmt.Sprintf("preauth-spent:%s:%d", ip, sec/60),
		reset: 60 - sec%60,
	}
}

// preAuthAdmission is the outcome of admit: the window, the value of its
// taken counter that decided, and whether the request holds a unit.
type preAuthAdmission struct {
	win      preAuthWindow
	count    int64
	admitted bool
}

// admit takes one unit of the IP's budget. Only the atomic increment of
// taken admits, so the budget bounds the credentials checked at the same
// time and the failed attempts of the window together. When all units are
// taken but not all spent, the rest are held by checks in flight that give
// them back on success: the request waits for one instead of being refused
// (F-387). It is refused when the spent units fill the budget, when
// preAuthMaxWait is over or when the client is gone.
func (l *preAuthLimiter) admit(ctx context.Context, ip string) (preAuthAdmission, error) {
	deadline := time.Now().Add(preAuthMaxWait)
	poll := preAuthPollMin
	for attempt := 0; ; attempt++ {
		freed := l.freedSignal()
		win := newPreAuthWindow(ip, time.Now())
		refused := func(count int64) (preAuthAdmission, error) {
			return preAuthAdmission{win: win, count: count}, nil
		}
		// A store error of a client that is gone is no outage.
		fail := func(err error) (preAuthAdmission, error) {
			if ctx.Err() != nil {
				return refused(l.limit + 1)
			}
			return preAuthAdmission{win: win}, err
		}
		// A waiting request increments only when a unit looks free, so
		// the waiters' tries do not crowd out each other.
		tryTake := attempt == 0
		if !tryTake {
			taken, err := l.value(ctx, win.taken)
			if err != nil {
				return fail(err)
			}
			tryTake = taken < l.limit
		}
		count := l.limit + 1
		if tryTake {
			var err error
			if count, err = l.store.Increment(ctx, win.taken, 2*time.Minute); err != nil {
				return fail(err)
			}
			if count <= l.limit {
				return preAuthAdmission{win: win, count: count, admitted: true}, nil
			}
			// A refused request is no attempt: its unit is given back, so
			// the counter holds only the admitted ones. The budget renews
			// with the next minute window.
			if _, err = l.store.Decrement(context.WithoutCancel(ctx), win.taken); err != nil {
				slog.Warn("pre-auth rate limiter: refused unit not given back", "error", err)
			}
		}
		spent, err := l.value(ctx, win.spent)
		if err != nil {
			return fail(err)
		}
		if spent >= l.limit || !time.Now().Before(deadline) {
			return refused(count)
		}
		wait := min(poll/2+rand.N(poll/2+1), time.Until(deadline))
		poll = min(2*poll, preAuthPollMax)
		timer := time.NewTimer(wait)
		select {
		case <-freed:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return refused(count)
		}
		timer.Stop()
	}
}

// value reads a counter of the store; a missing one is 0.
func (l *preAuthLimiter) value(ctx context.Context, key string) (int64, error) {
	raw, ok, err := l.store.Get(ctx, key)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pre-auth counter %q: %w", key, err)
	}
	return n, nil
}

// freedSignal returns the channel that is closed when the next unit is
// given back or spent on this replica.
func (l *preAuthLimiter) freedSignal() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.freed
}

// signal wakes the waiting requests of this replica; those of the other
// replicas look again after their poll interval.
func (l *preAuthLimiter) signal() {
	l.mu.Lock()
	close(l.freed)
	l.freed = make(chan struct{})
	l.mu.Unlock()
}

// countFailure counts a failed authentication of a request that had given
// its unit back already: a new unit, spent at once.
func (l *preAuthLimiter) countFailure(ctx context.Context, win preAuthWindow) {
	ctx = context.WithoutCancel(ctx)
	if _, err := l.store.Increment(ctx, win.taken, 2*time.Minute); err != nil {
		slog.Warn("pre-auth rate limiter: failed attempt not counted", "error", err)
		return
	}
	if _, err := l.store.Increment(ctx, win.spent, 2*time.Minute); err != nil {
		slog.Warn("pre-auth rate limiter: failed attempt not marked as spent", "error", err)
	}
	l.signal()
}

// preAuthSlot is the unit of the pre-auth budget a request holds while it
// is served.
type preAuthSlot struct {
	limiter *preAuthLimiter
	win     preAuthWindow
	mu      sync.Mutex
	held    bool
}

type preAuthSlotKey struct{}

// take ends the hold once and reports whether the slot was held.
func (s *preAuthSlot) take() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.held
	s.held = false
	return held
}

// release gives the unit back once; a store error leaves it counted.
func (s *preAuthSlot) release(ctx context.Context) {
	if !s.take() {
		return
	}
	// The client may be gone by now; the unit is given back regardless.
	if _, err := s.limiter.store.Decrement(context.WithoutCancel(ctx), s.win.taken); err != nil {
		slog.Warn("pre-auth rate limiter: unit not given back", "error", err)
	}
	s.limiter.signal()
}

// spend keeps the unit counted until the window ends: an attempt that is
// not given back. Waiting requests of the IP are refused once the spent
// units fill the budget. A store error leaves the unit taken but not marked
// spent: waiting requests are then refused after preAuthMaxWait, the
// budget still holds.
func (s *preAuthSlot) spend(ctx context.Context) {
	if !s.take() {
		return
	}
	if _, err := s.limiter.store.Increment(context.WithoutCancel(ctx), s.win.spent, 2*time.Minute); err != nil {
		slog.Warn("pre-auth rate limiter: attempt not marked as spent", "error", err)
	}
	s.limiter.signal()
}

func (s *preAuthSlot) released() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.held
}

// releasePreAuthSlot gives back the pre-auth unit of a request whose
// authentication succeeded, so a valid credential holds the IP budget only
// while it is checked, not while the handler runs.
func releasePreAuthSlot(ctx context.Context) {
	if slot, ok := ctx.Value(preAuthSlotKey{}).(*preAuthSlot); ok {
		slot.release(ctx)
	}
}
