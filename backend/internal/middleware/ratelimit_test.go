package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func limitedHandler(limits RateLimits, store cache.Store) http.Handler {
	return RateLimiterWithLimits(limits, store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func asPrincipal(org, credential string, kind identity.PrincipalType) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	return req.WithContext(identity.WithPrincipal(req.Context(), identity.Principal{
		Subject: "user-1", OrganizationID: org, Type: kind, Credential: credential,
	}))
}

// TestRateLimitPerCredential covers API-04: every session and every API
// key has its own budget, even when they belong to the same user; the
// limit is exact.
func TestRateLimitPerCredential(t *testing.T) {
	h := limitedHandler(RateLimits{PerCredential: 3, PerOrganization: 100}, cache.NewMemoryStore())
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for i := 1; i <= 3; i++ {
		w := serve(asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
		if w.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, w.Code)
		}
		if got := w.Header().Get("X-RateLimit-Remaining"); got != strconv.Itoa(3-i) {
			t.Errorf("request %d: remaining %s", i, got)
		}
	}
	w := serve(asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || w.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("4th request: %d, headers %v", w.Code, w.Header())
	}
	// Another session of the same user and an API key keep their budgets.
	for _, cred := range []string{"session:b", "apikey:rk_1"} {
		if w = serve(asPrincipal("org-1", cred, identity.PrincipalTypeUser)); w.Code != http.StatusNoContent {
			t.Errorf("%s: %d", cred, w.Code)
		}
	}
}

// TestRateLimitPerOrganization: the organization budget caps all
// credentials of the organization together; another organization is not
// affected.
func TestRateLimitPerOrganization(t *testing.T) {
	h := limitedHandler(RateLimits{PerCredential: 3, PerOrganization: 5}, cache.NewMemoryStore())
	codes := []int{}
	for i := range 6 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, asPrincipal("org-1", "session:"+strconv.Itoa(i), identity.PrincipalTypeUser))
		codes = append(codes, w.Code)
		if i == 4 && (w.Header().Get("X-RateLimit-Limit") != "5" || w.Header().Get("X-RateLimit-Remaining") != "0") {
			t.Errorf("5th request: the organization budget is the tighter one: %v", w.Header())
		}
	}
	if codes[4] != http.StatusNoContent || codes[5] != http.StatusTooManyRequests {
		t.Errorf("codes %v", codes)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, asPrincipal("org-2", "session:x", identity.PrincipalTypeUser))
	if w.Code != http.StatusNoContent {
		t.Errorf("other organization: %d", w.Code)
	}
}

// TestRateLimitHeadersAndDefaults: unauthenticated requests are counted
// per client IP and carry the headers; the defaults are 600 and 6,000;
// the reset lies within the minute.
func TestRateLimitHeadersAndDefaults(t *testing.T) {
	if l := (RateLimits{}).withDefaults(); l.PerCredential != 600 || l.PerOrganization != 6000 {
		t.Errorf("defaults %+v", l)
	}
	h := limitedHandler(RateLimits{PerCredential: 2}, cache.NewMemoryStore())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	reset, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Reset"))
	if w.Header().Get("X-RateLimit-Limit") != "2" || w.Header().Get("X-RateLimit-Remaining") != "1" || reset < 1 || reset > 60 {
		t.Errorf("headers %v", w.Header())
	}
	d, err := (RateLimits{PerCredential: 1, PerOrganization: 1}).take(asPrincipal("o", "c", identity.PrincipalTypeAPIKey), cache.NewMemoryStore(),
		time.Date(2026, 1, 1, 12, 0, 45, 0, time.UTC))
	if err != nil || !d.allowed || d.reset != 15 {
		t.Errorf("decision %+v %v", d, err)
	}
}

type failingStore struct{ cache.Store }

func (failingStore) Increment(context.Context, string, time.Duration) (int64, error) {
	return 0, errors.New("redis down")
}

// TestRateLimitFailsClosed: without the store every request is refused.
func TestRateLimitFailsClosed(t *testing.T) {
	w := httptest.NewRecorder()
	limitedHandler(RateLimits{}, failingStore{}).ServeHTTP(w, asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status %d", w.Code)
	}
}
