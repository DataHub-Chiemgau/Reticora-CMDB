package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
)

// redisStore connects to TEST_REDIS_URL; without it the test is skipped.
func redisStore(t *testing.T) cache.Store {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping Redis integration test")
	}
	client, err := redisx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return cache.NewRedisStore(client.Unwrap())
}

// replica returns a session issuer and a refresh store on the shared Redis,
// as one server replica builds them (cmd/server).
func replica(store cache.Store, issuer *SessionIssuer) (*SessionIssuer, *RefreshSessions) {
	revocations := NewSessionRevocations(store)
	copyIssuer := *issuer
	copyIssuer.revocations = revocations
	return &copyIssuer, NewRefreshSessions(store, revocations)
}

// TestRevocationInRedis covers WP-066 (AUT-02) against a real Redis shared by
// two replicas: a session started on one replica refreshes on the other,
// reuse of a rotated cookie revokes the session everywhere, logout on one
// replica blacklists the access token and the refresh family for the other,
// and a deactivation ends refresh at once. The blacklist entries expire.
//
// It runs only with TEST_REDIS_URL.
func TestRevocationInRedis(t *testing.T) {
	store := redisStore(t)
	base := testSessionIssuer(t)
	issuerA, refreshA := replica(store, base)
	issuerB, refreshB := replica(store, base)
	ctx := context.Background()

	t.Run("refresh on another replica, reuse revokes everywhere", func(t *testing.T) {
		_, _, cookie := loginHandler(t, issuerA, refreshA)
		handlerB := NewHandler(nil, issuerB).WithRefreshSessions(refreshB)
		w := doRefresh(handlerB, cookie.Value)
		if w.Code != http.StatusOK {
			t.Fatalf("refresh on replica B: %d %s", w.Code, w.Body.String())
		}
		next := refreshCookie(t, w)
		handlerA := NewHandler(nil, issuerA).WithRefreshSessions(refreshA)
		if w = doRefresh(handlerA, cookie.Value); w.Code != http.StatusUnauthorized {
			t.Fatalf("reuse on replica A: %d, want 401", w.Code)
		}
		if w = doRefresh(handlerB, next.Value); w.Code != http.StatusUnauthorized {
			t.Fatalf("successor on replica B after reuse: %d, want 401", w.Code)
		}
	})

	t.Run("logout on one replica", func(t *testing.T) {
		handlerA, token, cookie := loginHandler(t, issuerA, refreshA)
		req := cookieRequest(http.MethodPost, "/api/v1/auth/logout", cookie.Value)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handlerA.Logout(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("logout: %d", w.Code)
		}
		if _, err := issuerB.Validate(token); !errors.Is(err, ErrSessionRevoked) {
			t.Errorf("access token on replica B after logout: %v, want revoked", err)
		}
		if w = doRefresh(NewHandler(nil, issuerB).WithRefreshSessions(refreshB), cookie.Value); w.Code != http.StatusUnauthorized {
			t.Errorf("refresh on replica B after logout: %d, want 401", w.Code)
		}
		// The blacklist entry of the token lives until the token expires.
		parsed := jwtPart(t, token, 1)
		ttl, err := redisTTL(ctx, t, tokenRevocationKey(parsed["jti"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		if ttl <= 0 || ttl > sessionLifetime {
			t.Errorf("token blacklist TTL %v, want within the token lifetime", ttl)
		}
	})

	t.Run("deactivation ends refresh", func(t *testing.T) {
		handlerA, token, cookie := loginHandler(t, issuerA, refreshA)
		claims, err := issuerA.Validate(token)
		if err != nil {
			t.Fatal(err)
		}
		if err = issuerB.Revocations().RevokeUser(ctx, claims.Subject, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if w := doRefresh(handlerA, cookie.Value); w.Code != http.StatusUnauthorized {
			t.Errorf("refresh after deactivation: %d, want 401", w.Code)
		}
		if _, err = issuerA.Validate(token); !errors.Is(err, ErrSessionRevoked) {
			t.Errorf("access token after deactivation: %v, want revoked", err)
		}
	})
}

// redisTTL reads the remaining lifetime of a key.
func redisTTL(ctx context.Context, t *testing.T, key string) (time.Duration, error) {
	t.Helper()
	client, err := redisx.Connect(ctx, os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.Unwrap().TTL(ctx, key).Result()
}
