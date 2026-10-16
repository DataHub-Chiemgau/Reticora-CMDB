// Phase-1 acceptance audit, group T2-auth-authz: regression test of the user
// deactivation through the production authentication chain on Redis and
// PostgreSQL.
//
//   - WP-042 (TLC-04, AUT-02, AUT-01), gap (high) "Permanent full-chain
//     deactivation test with Redis (currently verifier probe only)". TLC-04:
//     deactivation ends sessions, refresh cookies and API keys at once. Here
//     the session blacklist and the refresh families live in Redis (as with
//     several replicas), requests pass the production
//     AuthMiddlewareWithAPIKeys (PG API keys with the owner's live rights) and
//     TenantMiddleware, and the deactivation is the real PATCH /users/{id}.
//     Before: bearer and X-API-Key 200. After: bearer 401, X-API-Key 401,
//     refresh cookie 401; another user of the organization 200. After the
//     reactivation the old token stays 401 and a token issued afterwards is
//     200.
//
// Retest (needs a migrated database and Redis):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run 'TestT2DeactivationFullChain' ./internal/identity/
package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

func TestT2DeactivationFullChain(t *testing.T) {
	f := scopetest.Seed(t, "ee")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("TEST_REDIS_URL is required next to TEST_DATABASE_URL for this integration test")
	}
	bg := context.Background()
	client, err := redisx.Connect(bg, redisURL)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisStore(client.Unwrap())

	users := user.NewPGRepository(f.App)
	target, err := users.EnsureUser(bg, f.OrgA, "t2-fullchain-subject-ee", "t2-fullchain@example.invalid", "T2 Fullchain")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	other := f.AppUser(t, f.OrgA, "t2-fullchain-other")
	admin := f.AppUser(t, f.OrgA, "t2-fullchain-admin")
	roleID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-fullchain-reader', '["ci:read"]')`, roleID, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $4), ($1, $3, $4)`, f.OrgA, target, other, roleID); err != nil {
		t.Fatal(err)
	}
	key, err := identity.GenerateAPIKey("live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, permissions, created_by) VALUES ($1, $2, 't2-fullchain', $3, $4, '{ci:read}', $5)`,
		f.ID(), f.OrgA, key.KeyHash, key.KeyPrefix, target); err != nil {
		t.Fatalf("seed API key: %v", err)
	}

	revocations := identity.NewSessionRevocations(store)
	sessions := newIssuer(t).WithRevocations(revocations)
	refresh := identity.NewRefreshSessions(store, revocations)
	apiKeys := identity.NewAPIKeyServiceWithStore(identity.NewPGAPIKeyStore(f.App)).WithOwnerAccess(permission.NewPGRepository(f.App))
	protected := middleware.Chain(middleware.AuthMiddlewareWithAPIKeys(sessions, apiKeys), middleware.TenantMiddleware)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	authHandler := identity.NewHandler(nil, sessions).WithProvisioning(users, "").
		WithAccessResolver(permission.NewPGRepository(f.App)).WithRefreshSessions(refresh)

	login := func(subject string) (token, cookie string) {
		t.Helper()
		now := time.Now().UTC()
		claims := identity.SessionClaims{Subject: subject, OrganizationID: f.OrgA, Permissions: []identity.Permission{identity.PermCIRead},
			ID: "t2-fullchain-" + subject + "-" + now.Format("150405.000000000"), IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		if token, err = sessions.Issue(claims); err != nil {
			t.Fatal(err)
		}
		if cookie, err = refresh.Issue(bg, &identity.RefreshGrant{Claims: claims}); err != nil {
			t.Fatal(err)
		}
		return token, cookie
	}
	get := func(header, value string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set(header, value)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		return w.Code
	}
	refreshCode := func(cookie string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
		w := httptest.NewRecorder()
		authHandler.Refresh(w, req)
		return w.Code
	}
	mux := chi.NewRouter()
	user.NewHandler(users).WithSessionRevoker(revocations).RegisterRoutes(mux)
	setStatus := func(status string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+target, strings.NewReader(`{"status":"`+status+`"}`))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, admin)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH status %s: %d %s", status, w.Code, w.Body.String())
		}
	}

	// The blacklist compares the token's iat in whole seconds: tokens of
	// this test are issued at least a second away from the status changes.
	token, cookie := login(target)
	otherToken, _ := login(other)
	if code := get("Authorization", "Bearer "+token); code != http.StatusOK {
		t.Fatalf("before: bearer of the user: %d, want 200", code)
	}
	if code := get("X-API-Key", key.Plaintext); code != http.StatusOK {
		t.Fatalf("before: API key of the user: %d, want 200", code)
	}

	time.Sleep(1100 * time.Millisecond)
	setStatus("inactive")
	for name, code := range map[string]int{
		"bearer of the deactivated user":  get("Authorization", "Bearer "+token),
		"API key of the deactivated user": get("X-API-Key", key.Plaintext),
		"refresh cookie of the user":      refreshCode(cookie),
	} {
		if code != http.StatusUnauthorized {
			t.Errorf("after deactivation: %s -> %d, want 401", name, code)
		}
	}
	if code := get("Authorization", "Bearer "+otherToken); code != http.StatusOK {
		t.Errorf("after deactivation: another user of the organization -> %d, want 200", code)
	}

	setStatus("active")
	if code := get("Authorization", "Bearer "+token); code != http.StatusUnauthorized {
		t.Errorf("after reactivation: the token issued before the deactivation -> %d, want 401", code)
	}
	time.Sleep(1100 * time.Millisecond)
	fresh, _ := login(target)
	if code := get("Authorization", "Bearer "+fresh); code != http.StatusOK {
		t.Errorf("after reactivation: a token issued afterwards -> %d, want 200", code)
	}
}
