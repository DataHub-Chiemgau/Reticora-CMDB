// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// authentication endpoints and the api-keys resource through the production
// request chain (router of server.NewRouter plus the middleware chain of
// cmd/server/main.go: request id, recovery, security headers, pre-auth
// limit, auth, tenant, metrics, entitlement, rate limit; validation and
// idempotency as route middleware).
//
//   - WP-066 (AUT-02, SEC-06), gap (critical) "No API test of refresh/logout
//     through the production middleware chain": logout through the chain
//     blacklists the access token and revokes the refresh family.
//   - WP-364 (AUT-02, API-04), finding F-018: the cookie-only refresh
//     (no body, no Content-Type) passes the validation stage and rotates
//     the cookie; after the logout it gets 401.
//   - WP-067 (AUT-04, API-05), gap (high) "API key endpoints through the
//     production router (authz, validator, entitlement)": create shows the
//     key once, list hides key and hash, rotate without body uses the 24 h
//     overlap, a viewer gets 403, an API key cannot mint keys.
//   - WP-355 (RBA-08, API-05), finding F-008: assigning roles to a service
//     account is managing roles; PUT /service-accounts/{id}/roles needs
//     role:manage, user:manage alone gets 403 (route table and production
//     chain).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2Audit|TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage|TestAuditT2CookieOnlyRefreshThroughProductionChain' ./internal/server/
package server_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

const (
	t2Org  = "7a2a0000-0000-4000-8000-000000000001"
	t2User = "7a2a0000-0000-4000-8000-0000000000f1"
)

// t2Stack is the production request chain over the in-memory repositories.
type t2Stack struct {
	handler      http.Handler
	issuer       *identity.SessionIssuer
	refresh      *identity.RefreshSessions
	apiKeys      *identity.APIKeyService
	entitlements *entitlement.Service
	repos        server.Repositories
	sessions     int
}

func newT2Stack(t *testing.T) *t2Stack {
	t.Helper()
	return newT2StackWith(t, entitlement.Options{})
}

// newT2StackWith builds the stack with the given entitlement options (for
// example Enforce for the quotas).
func newT2StackWith(t *testing.T, entOpts entitlement.Options) *t2Stack {
	t.Helper()
	return newT2StackFrom(t, entOpts, server.MemoryRepositories())
}

// newT2StackFrom builds the stack over the given repositories.
func newT2StackFrom(t *testing.T, entOpts entitlement.Options, repos server.Repositories) *t2Stack {
	t.Helper()
	store := cache.NewMemoryStore()
	revocations := identity.NewSessionRevocations(store)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}))
	if err != nil {
		t.Fatal(err)
	}
	issuer = issuer.WithRevocations(revocations)
	refresh := identity.NewRefreshSessions(store, revocations)
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	entitlements := entitlement.NewService(repos.Entitlement, entOpts)
	mux, httpMetrics, err := server.NewRouter(repos, server.Options{
		Version: "t2-audit", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Dispatcher:  webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Sessions:    issuer, RefreshSessions: refresh,
		RouteMiddleware: []func(http.Handler) http.Handler{
			middleware.OpenAPIValidation,
			middleware.IdempotencyWithStore(store),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	apiKeys := identity.NewAPIKeyServiceWithStore(repos.APIKeys)
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.SecurityHeaders,
		middleware.PreAuthRateLimiter(middleware.DefaultPreAuthPerMinute, store),
		middleware.AuthMiddlewareWithAPIKeys(issuer, apiKeys),
		middleware.TenantMiddleware,
		httpMetrics,
		entitlements.Middleware,
		middleware.RateLimiterWithLimits(middleware.RateLimits{}, store),
	)(mux)
	return &t2Stack{handler: handler, issuer: issuer, refresh: refresh, apiKeys: apiKeys, entitlements: entitlements, repos: repos}
}

// login starts a session as the login callback does: an access token and a
// refresh cookie of a new refresh family.
func (s *t2Stack) login(t *testing.T, perms ...identity.Permission) (token, cookie string) {
	t.Helper()
	s.sessions++
	claims := identity.SessionClaims{
		Subject: t2User, OrganizationID: t2Org, Permissions: perms,
		Scope: &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
	}
	cookie, err := s.refresh.Issue(context.Background(), &identity.RefreshGrant{Claims: claims})
	if err != nil {
		t.Fatalf("start refresh family: %v", err)
	}
	now := time.Now().UTC()
	claims.ID, claims.IssuedAt, claims.ExpiresAt = "t2-jti-"+strconv.Itoa(s.sessions), now, now.Add(15*time.Minute)
	token, err = s.issuer.Issue(claims)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	return token, cookie
}

type t2Request struct {
	method, path, body, contentType string
	bearer, apiKey, cookie          string
	remoteAddr                      string
}

func (s *t2Stack) do(r t2Request) *httptest.ResponseRecorder {
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.apiKey != "" {
		req.Header.Set("X-API-Key", r.apiKey)
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: r.cookie})
	}
	req.RemoteAddr = "203.0.113.70:41000"
	if r.remoteAddr != "" {
		req.RemoteAddr = r.remoteAddr
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	return w
}

func t2RefreshCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == identity.RefreshCookieName {
			return c
		}
	}
	return nil
}

// TestT2AuditLogoutThroughProductionChain: AUT-02 logout through the real
// chain blacklists the presented access token at once and revokes the
// refresh family; another session of the same user is not affected.
func TestT2AuditLogoutThroughProductionChain(t *testing.T) {
	s := newT2Stack(t)
	token, cookie := s.login(t, identity.PermPermissionRead)
	other, _ := s.login(t, identity.PermPermissionRead)

	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/me", bearer: token}); w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me before logout: %d %s", w.Code, w.Body.String())
	}
	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/logout", bearer: token, cookie: cookie})
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v1/auth/logout: %d %s", w.Code, w.Body.String())
	}
	if c := t2RefreshCookie(w); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout must clear the refresh cookie, got %+v", c)
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/me", bearer: token}); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me with the logged-out token: %d, want 401", w.Code)
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/cis with the logged-out token: %d, want 401", w.Code)
	}
	// The SPA's cookie-only refresh: the refresh family must be revoked.
	if w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh", cookie: cookie}); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh with the logged-out cookie: %d %s, want 401", w.Code, w.Body.String())
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/me", bearer: other}); w.Code != http.StatusOK {
		t.Errorf("another session of the user after the logout: %d, want 200", w.Code)
	}
}

// TestAuditT2CookieOnlyRefreshThroughProductionChain covers WP-364 (AUT-02,
// F-018): the SPA renews the 15-minute access token with a POST that carries only the HttpOnly refresh
// cookie (frontend/src/auth/session.ts:22-25; OpenAPI refreshSession has no
// requestBody). Through the production chain it must get 200 with a new
// token and a rotated cookie; a missing cookie and a reused cookie get 401.
func TestAuditT2CookieOnlyRefreshThroughProductionChain(t *testing.T) {
	s := newT2Stack(t)
	_, cookie := s.login(t, identity.PermPermissionRead)

	t.Run("cookie only refresh rotates", func(t *testing.T) {
		w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh", cookie: cookie})
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/auth/refresh with only the refresh cookie: %d %s, want 200", w.Code, strings.TrimSpace(w.Body.String()))
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Token == "" {
			t.Errorf("refresh body %s: no token", w.Body.String())
		}
		c := t2RefreshCookie(w)
		if c == nil || c.Value == "" || c.Value == cookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/auth" {
			t.Errorf("rotated refresh cookie %+v, want a new HttpOnly, Secure, SameSite=Strict cookie for /api/v1/auth", c)
		}
		if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/me", bearer: body.Token}); w.Code != http.StatusOK {
			t.Errorf("GET /api/v1/me with the refreshed token: %d", w.Code)
		}
		if w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh", cookie: cookie}); w.Code != http.StatusUnauthorized {
			t.Errorf("second use of the consumed cookie: %d %s, want 401", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})
	t.Run("refresh without cookie", func(t *testing.T) {
		if w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh"}); w.Code != http.StatusUnauthorized {
			t.Errorf("POST /api/v1/auth/refresh without cookie: %d %s, want 401", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})
}

// TestT2AuditAPIKeysThroughProductionChain: the api-keys resource (AUT-04,
// API-05) through the real router and chain.
func TestT2AuditAPIKeysThroughProductionChain(t *testing.T) {
	s := newT2Stack(t)
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead)
	viewer, _ := s.login(t, identity.PermCIRead)

	// Create: 201 and the plaintext exactly once.
	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
		body: `{"name":"t2-integration","permissions":["ci:read"]}`})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/api-keys: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID, Key, KeyPrefix string
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	created.ID, _ = raw["id"].(string)
	created.Key, _ = raw["key"].(string)
	created.KeyPrefix, _ = raw["key_prefix"].(string)
	if created.ID == "" || !strings.HasPrefix(created.Key, "rk_live_") || created.KeyPrefix == "" || !strings.HasPrefix(created.Key, "rk_live_"+created.KeyPrefix) {
		t.Fatalf("created key: %s", w.Body.String())
	}

	// The key authenticates through the chain.
	if kw := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); kw.Code != http.StatusOK {
		t.Errorf("GET /api/v1/cis with the new key: %d %s", kw.Code, kw.Body.String())
	}

	// List: no plaintext and no hash.
	w = s.do(t2Request{method: http.MethodGet, path: "/api/v1/api-keys", bearer: admin})
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/api-keys: %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); strings.Contains(body, created.Key) || strings.Contains(body, `"key"`) || strings.Contains(body, "hash") {
		t.Errorf("list exposes key material: %s", body)
	}
	if !strings.Contains(w.Body.String(), created.ID) {
		t.Errorf("list misses the key %s: %s", created.ID, w.Body.String())
	}

	// A viewer without apikey:manage gets 403 on every api-keys route.
	for _, r := range []t2Request{
		{method: http.MethodPost, path: "/api/v1/api-keys", contentType: "application/json", body: `{"name":"x","permissions":["ci:read"]}`},
		{method: http.MethodGet, path: "/api/v1/api-keys"},
		{method: http.MethodPost, path: "/api/v1/api-keys/" + created.ID + "/rotate"},
		{method: http.MethodDelete, path: "/api/v1/api-keys/" + created.ID},
	} {
		r.bearer = viewer
		if vw := s.do(r); vw.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", r.method, r.path, vw.Code)
		}
	}

	// A key with apikey:manage cannot mint further keys: keys are managed
	// by users only.
	w = s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
		body: `{"name":"t2-manager-key","permissions":["apikey:manage","ci:read"]}`})
	if w.Code != http.StatusCreated {
		t.Fatalf("create manager key: %d %s", w.Code, w.Body.String())
	}
	var managerKey struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &managerKey)
	if mw := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", apiKey: managerKey.Key, contentType: "application/json",
		body: `{"name":"minted-by-key","permissions":["ci:read"]}`}); mw.Code != http.StatusForbidden {
		t.Errorf("X-API-Key request creating a key: %d %s, want 403", mw.Code, mw.Body.String())
	}

	// Rotate without body: 201, a new key, the old one valid for the 24 h
	// overlap.
	before := time.Now().UTC()
	w = s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys/" + created.ID + "/rotate", bearer: admin})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/api-keys/{id}/rotate without body: %d %s", w.Code, w.Body.String())
	}
	var rotated struct {
		Key struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"key"`
		Previous struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"previous"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Key.Key == "" || rotated.Key.Key == created.Key || rotated.Key.ID == created.ID {
		t.Errorf("rotation returned no new key: %s", w.Body.String())
	}
	if exp := rotated.Previous.ExpiresAt; exp == nil || exp.Before(before.Add(identity.APIKeyRotationOverlap-time.Minute)) || exp.After(before.Add(identity.APIKeyRotationOverlap+time.Minute)) {
		t.Errorf("previous key expires_at %v, want now + 24 h", exp)
	}
	for _, key := range []string{created.Key, rotated.Key.Key} {
		if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: key}); w.Code != http.StatusOK {
			t.Errorf("GET /api/v1/cis with %s during the overlap: %d", key[:12], w.Code)
		}
	}

	// Revoke: the key stops working at once.
	if w := s.do(t2Request{method: http.MethodDelete, path: "/api/v1/api-keys/" + rotated.Key.ID, bearer: admin}); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/api-keys/{id}: %d %s", w.Code, w.Body.String())
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: rotated.Key.Key}); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d, want 401", w.Code)
	}
}

// authorizeAs runs AuthorizeRoute for a principal holding exactly perms.
func authorizeAs(method, path string, perms ...identity.Permission) int {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(method, path, nil)
	p := identity.Principal{Subject: "u", OrganizationID: "o", Type: identity.PrincipalTypeUser, Permissions: perms}
	w := httptest.NewRecorder()
	server.AuthorizeRoute(method, path)(ok).ServeHTTP(w, req.WithContext(identity.WithPrincipal(req.Context(), p)))
	return w.Code
}

// TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage: service accounts
// are on a par with users (RBA-08); assigning roles is managing roles
// (RBA-01 role:manage, as POST /role-assignments). user:manage alone must not
// assign roles to a service account.
func TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage(t *testing.T) {
	const path = "/api/v1/service-accounts/7a2a0000-0000-4000-8000-000000000201/roles"
	if got := authorizeAs(http.MethodPut, path, identity.PermUserManage, identity.PermUserRead); got != http.StatusForbidden {
		t.Errorf("PUT %s with user:manage but without role:manage -> %d, want 403", path, got)
	}
	if got := authorizeAs(http.MethodPut, path, identity.PermUserManage, identity.PermUserRead, identity.PermRoleManage); got != http.StatusOK {
		t.Errorf("control: PUT %s with user:manage and role:manage -> %d, want 200", path, got)
	}
	if got := authorizeAs(http.MethodPost, "/api/v1/role-assignments", identity.PermUserManage); got != http.StatusForbidden {
		t.Errorf("control: POST /api/v1/role-assignments with only user:manage -> %d, want 403", got)
	}
}

// TestT2AuditServiceAccountRolesNeedRoleManageThroughProductionChain: the
// role assignment of a service account through the production chain needs
// role:manage (RBA-08, F-008); creating the account stays user:manage.
func TestT2AuditServiceAccountRolesNeedRoleManageThroughProductionChain(t *testing.T) {
	s := newT2Stack(t)
	userManager, _ := s.login(t, identity.PermUserManage, identity.PermUserRead)
	roleManager, _ := s.login(t, identity.PermUserManage, identity.PermUserRead, identity.PermRoleManage)

	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/service-accounts", bearer: userManager, contentType: "application/json",
		body: `{"name":"t2-chain-sa"}`})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/service-accounts with user:manage: %d %s", w.Code, w.Body.String())
	}
	var sa struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil || sa.ID == "" {
		t.Fatalf("created service account %s: %v", w.Body.String(), err)
	}
	path := "/api/v1/service-accounts/" + sa.ID + "/roles"
	if w := s.do(t2Request{method: http.MethodPut, path: path, bearer: userManager, contentType: "application/json", body: `{"roles":[]}`}); w.Code != http.StatusForbidden {
		t.Errorf("PUT %s with user:manage but without role:manage: %d %s, want 403", path, w.Code, w.Body.String())
	}
	if w := s.do(t2Request{method: http.MethodPut, path: path, bearer: roleManager, contentType: "application/json", body: `{"roles":[]}`}); w.Code != http.StatusOK {
		t.Errorf("control: PUT %s with role:manage: %d %s, want 200", path, w.Code, w.Body.String())
	}
}
