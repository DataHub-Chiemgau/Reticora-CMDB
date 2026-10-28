package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// downStore is a cache store whose backend is unreachable.
type downStore struct{}

func (downStore) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("redis down")
}
func (downStore) Set(context.Context, string, string, time.Duration) error {
	return errors.New("redis down")
}
func (downStore) Increment(context.Context, string, time.Duration) (int64, error) {
	return 0, errors.New("redis down")
}
func (downStore) Decrement(context.Context, string) (int64, error) {
	return 0, errors.New("redis down")
}
func (downStore) IncrementBelow(context.Context, string, int64, time.Duration, string) (cache.BoundedCount, error) {
	return cache.BoundedCount{}, errors.New("redis down")
}
func (downStore) Delete(context.Context, string) error { return errors.New("redis down") }
func (downStore) WindowAdd(context.Context, string, time.Time, time.Duration, int64) (cache.Window, error) {
	return cache.Window{}, errors.New("redis down")
}
func (downStore) WindowCount(context.Context, string, time.Time, time.Duration) (cache.Window, error) {
	return cache.Window{}, errors.New("redis down")
}
func (downStore) Close() error { return nil }

// TestRateLimiterExemptsOperationalProbesDuringARedisOutage covers WP-104
// (OPS-06): with Redis down the limiter fails closed for the API (429),
// while /healthz, /readyz and /metrics reach their handlers so readiness
// can report the outage itself (503 with "redis": "unavailable").
func TestRateLimiterExemptsOperationalProbesDuringARedisOutage(t *testing.T) {
	handler := RateLimiterWithStore(600, downStore{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/readyz", http.StatusTeapot},
		{http.MethodGet, "/healthz", http.StatusTeapot},
		{http.MethodGet, "/metrics", http.StatusTeapot},
		{http.MethodHead, "/readyz", http.StatusTeapot},
		{http.MethodPost, "/readyz", http.StatusTooManyRequests},
		{http.MethodGet, "/readyz/x", http.StatusTooManyRequests},
		{http.MethodGet, "/api/v1/cis", http.StatusTooManyRequests},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, http.NoBody))
		if rec.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}
