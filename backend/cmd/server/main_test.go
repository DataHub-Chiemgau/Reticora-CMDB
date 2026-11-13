package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

func testSessionKeyPath(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadSessionIssuerFailsWithoutKeyInSecureMode proves that startup fails
// fast instead of silently accepting unsigned tokens when no session key is
// configured and the insecure development mode was not explicitly enabled.
func TestLoadSessionIssuerFailsWithoutKeyInSecureMode(t *testing.T) {
	issuer, err := loadSessionIssuer(&config.Config{})
	if err == nil {
		t.Fatal("expected an error when no session key is configured in secure mode")
	}
	if issuer != nil {
		t.Fatal("expected no issuer in secure mode without a key")
	}
}

// TestLoadSessionIssuerFailsOnUnreadableKeyInSecureMode proves that a key
// file that cannot be read is fatal in secure mode.
func TestLoadSessionIssuerFailsOnUnreadableKeyInSecureMode(t *testing.T) {
	cfg := &config.Config{SessionKeyPath: filepath.Join(t.TempDir(), "missing.pem")}
	if _, err := loadSessionIssuer(cfg); err == nil {
		t.Fatal("expected an error for an unreadable session key in secure mode")
	}
}

// TestLoadSessionIssuerDevModeOptIn proves that the insecure development mode
// is only reachable through the explicit opt-in flag.
func TestLoadSessionIssuerDevModeOptIn(t *testing.T) {
	issuer, err := loadSessionIssuer(&config.Config{AllowInsecureDevAuth: true})
	if err != nil {
		t.Fatalf("expected dev mode to start without a key, got %v", err)
	}
	if issuer != nil {
		t.Fatal("expected nil issuer in insecure dev mode")
	}

	// Even an unreadable key degrades to dev mode only with the opt-in.
	cfg := &config.Config{
		SessionKeyPath:       filepath.Join(t.TempDir(), "missing.pem"),
		AllowInsecureDevAuth: true,
	}
	issuer, err = loadSessionIssuer(cfg)
	if err != nil {
		t.Fatalf("expected dev mode to tolerate an unreadable key, got %v", err)
	}
	if issuer != nil {
		t.Fatal("expected nil issuer in insecure dev mode")
	}
}

// TestLoadSessionIssuerReadsValidKey proves the happy path: a readable RSA
// key produces a verifying issuer.
func TestLoadSessionIssuerReadsValidKey(t *testing.T) {
	cfg := &config.Config{SessionKeyPath: testSessionKeyPath(t)}
	issuer, err := loadSessionIssuer(cfg)
	if err != nil {
		t.Fatalf("expected a valid key to load, got %v", err)
	}
	if issuer == nil {
		t.Fatal("expected a session issuer for a valid key")
	}
}

// TestLoadSessionIssuerRejectsInvalidKey proves that a corrupt key file is
// fatal even in development mode, since it indicates a misconfiguration.
func TestLoadSessionIssuerRejectsInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SessionKeyPath: path, AllowInsecureDevAuth: true}
	if _, err := loadSessionIssuer(cfg); err == nil {
		t.Fatal("expected an error for a corrupt session key")
	}
}

const (
	chainOrg  = "7a2a0000-0000-4000-8000-000000000364"
	chainUser = "7a2a0000-0000-4000-8000-0000000003f1"
)

// chainStack is the handler of newHTTPHandler over the in-memory
// repositories with the production stages, each wrapped in a marker that
// records the stages a request passes.
type chainStack struct {
	handler http.Handler
	issuer  *identity.SessionIssuer
	refresh *identity.RefreshSessions
	trace   []string
	logins  int
}

func newChainStack(t *testing.T) *chainStack {
	t.Helper()
	return newChainStackWith(t, nil)
}

// newChainStackWith builds the chain stack with the given /readyz checks.
func newChainStackWith(t *testing.T, readiness []server.ReadinessCheck) *chainStack {
	t.Helper()
	cs := &chainStack{}
	repos := server.MemoryRepositories()
	store := cache.NewMemoryStore()
	issuer, err := loadSessionIssuer(&config.Config{SessionKeyPath: testSessionKeyPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	issuer = issuer.WithRevocations(identity.NewSessionRevocations(store))
	cs.issuer = issuer
	cs.refresh = identity.NewRefreshSessions(store, issuer.Revocations())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	// mark records name when a request enters the stage; the last stage
	// also records the handler it hands the request to.
	mark := func(name string, stage func(http.Handler) http.Handler, last bool) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			if last {
				handler := next
				next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					cs.trace = append(cs.trace, "handler")
					handler.ServeHTTP(w, r)
				})
			}
			inner := stage(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cs.trace = append(cs.trace, name)
				inner.ServeHTTP(w, r)
			})
		}
	}
	handler, err := newHTTPHandler(repos, server.Options{
		Version: "wp-364", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Dispatcher:  webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Sessions:    issuer, RefreshSessions: cs.refresh,
		Readiness: readiness,
	}, httpStages{
		preAuth:     mark("pre-auth", middleware.PreAuthRateLimiter(middleware.DefaultPreAuthPerMinute, store), false),
		auth:        mark("auth", middleware.AuthMiddlewareWithAPIKeys(issuer, identity.NewAPIKeyServiceWithStore(repos.APIKeys)), false),
		tenant:      mark("tenant", middleware.TenantMiddleware, false),
		entitlement: mark("entitlement", entitlements.Middleware, false),
		rateLimit:   mark("rate-limit", middleware.RateLimiterWithLimits(middleware.RateLimits{}, store), false),
		validation:  mark("validation", middleware.OpenAPIValidation, false),
		idempotency: mark("idempotency", middleware.IdempotencyWithStore(store), true),
	})
	if err != nil {
		t.Fatal(err)
	}
	cs.handler = handler
	return cs
}

// login starts a session as the login callback does: an access token and a
// refresh cookie of a new refresh family.
func (cs *chainStack) login(t *testing.T, perms ...identity.Permission) (token, cookie string) {
	t.Helper()
	cs.logins++
	claims := identity.SessionClaims{
		Subject: chainUser, OrganizationID: chainOrg, Permissions: perms,
		Scope: &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
	}
	cookie, err := cs.refresh.Issue(context.Background(), &identity.RefreshGrant{Claims: claims})
	if err != nil {
		t.Fatalf("start refresh family: %v", err)
	}
	now := time.Now().UTC()
	claims.ID, claims.IssuedAt, claims.ExpiresAt = "wp364-jti-"+strconv.Itoa(cs.logins), now, now.Add(15*time.Minute)
	if token, err = cs.issuer.Issue(&claims); err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	return token, cookie
}

// do serves one request; body and Content-Type are set only when given.
func (cs *chainStack) do(method, path, bearer, cookie, contentType, body string) *httptest.ResponseRecorder {
	cs.trace = nil
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
	}
	req.RemoteAddr = "203.0.113.64:41000"
	w := httptest.NewRecorder()
	cs.handler.ServeHTTP(w, req)
	return w
}

func refreshCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == identity.RefreshCookieName {
			return c
		}
	}
	return nil
}

// TestHTTPHandlerRunsTheStagesInAPI04Order covers WP-364 (API-04): the
// handler the server serves runs Pre-Auth -> Auth -> Tenant -> Entitlement
// -> Rate-Limit -> Authz -> Validation -> Idempotency -> Handler. A request
// without credentials stops at the auth stage, a forbidden one at the
// route authorization before validation.
func TestHTTPHandlerRunsTheStagesInAPI04Order(t *testing.T) {
	cs := newChainStack(t)
	token, _ := cs.login(t, identity.PermPermissionRead)

	for _, tc := range []struct {
		name, method, path, bearer string
		code                       int
		trace                      string
	}{
		{"permitted", http.MethodGet, "/api/v1/me", token, http.StatusOK,
			"pre-auth,auth,tenant,entitlement,rate-limit,validation,idempotency,handler"},
		{"forbidden by the route authorization", http.MethodGet, "/api/v1/cis", token, http.StatusForbidden,
			"pre-auth,auth,tenant,entitlement,rate-limit"},
		{"unauthenticated", http.MethodGet, "/api/v1/me", "", http.StatusUnauthorized,
			"pre-auth,auth"},
	} {
		w := cs.do(tc.method, tc.path, tc.bearer, "", "", "")
		if w.Code != tc.code || strings.Join(cs.trace, ",") != tc.trace {
			t.Errorf("%s: %s %s -> %d, stages %v; want %d after %s", tc.name, tc.method, tc.path, w.Code, cs.trace, tc.code, tc.trace)
		}
	}
}

// TestHTTPHandlerServesCookieOnlyRefreshAndLogout covers WP-364 (AUT-02,
// API-04, F-018) through the handler the server serves: the SPA's refresh
// with only the refresh cookie (no body, no Content-Type) reaches the
// handler and rotates the cookie; logout answers 204 and revokes token and
// refresh family. POST /cis without body still gets 400 from the
// validation stage.
func TestHTTPHandlerServesCookieOnlyRefreshAndLogout(t *testing.T) {
	cs := newChainStack(t)
	writer, cookie := cs.login(t, identity.PermPermissionRead, identity.PermCIWrite)

	w := cs.do(http.MethodPost, "/api/v1/auth/refresh", "", cookie, "", "")
	if w.Code != http.StatusOK || !strings.Contains(strings.Join(cs.trace, ","), "validation,idempotency,handler") {
		t.Fatalf("cookie-only POST /api/v1/auth/refresh: %d %s, stages %v; want 200 from the handler", w.Code, strings.TrimSpace(w.Body.String()), cs.trace)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("refresh body %s: no token", w.Body.String())
	}
	rotated := refreshCookieOf(w)
	if rotated == nil || rotated.Value == "" || rotated.Value == cookie || !rotated.HttpOnly {
		t.Fatalf("rotated refresh cookie %+v, want a new HttpOnly cookie", rotated)
	}
	token := body.Token
	if me := cs.do(http.MethodGet, "/api/v1/me", token, "", "", ""); me.Code != http.StatusOK {
		t.Errorf("GET /api/v1/me with the refreshed token: %d", me.Code)
	}

	// Positive control of the validation stage: a required body is enforced.
	// (The refreshed token carries the roles read again, here none; the
	// login token still has ci:write.)
	w = cs.do(http.MethodPost, "/api/v1/cis", writer, "", "application/json", "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "request body is required") || strings.Contains(strings.Join(cs.trace, ","), "handler") {
		t.Errorf("POST /api/v1/cis without body: %d %s, stages %v; want 400 from the validation stage", w.Code, strings.TrimSpace(w.Body.String()), cs.trace)
	}

	w = cs.do(http.MethodPost, "/api/v1/auth/logout", token, rotated.Value, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v1/auth/logout: %d %s", w.Code, w.Body.String())
	}
	if me := cs.do(http.MethodGet, "/api/v1/me", token, "", "", ""); me.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/me with the logged-out token: %d, want 401", me.Code)
	}
	if again := cs.do(http.MethodPost, "/api/v1/auth/refresh", "", rotated.Value, "", ""); again.Code != http.StatusUnauthorized {
		t.Errorf("cookie-only refresh after the logout: %d %s, want 401", again.Code, strings.TrimSpace(again.Body.String()))
	}
}

// TestHTTPHandlerSetsRateLimitHeadersBeforeTheLimiter covers WP-462 (API-04,
// AUT-10, F-217) through the handler the server serves: answers refused
// before the rate limit stage carry the X-RateLimit headers of the pre-auth
// budget of the client IP (401 for an invalid bearer, 429 of the pre-auth
// limit with Retry-After); an authenticated answer, here 403 of the route
// authorization, carries those of the credential budget, which overwrite
// them. A successful refresh and an authenticated request leave the
// pre-auth budget untouched.
func TestHTTPHandlerSetsRateLimitHeadersBeforeTheLimiter(t *testing.T) {
	if left := 60 - time.Now().UTC().Unix()%60; left < 5 {
		// All requests of the test share one minute window.
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
	cs := newChainStack(t)
	token, cookie := cs.login(t, identity.PermPermissionRead)
	type headers struct{ limit, remaining, reset, retry string }
	headersOf := func(w *httptest.ResponseRecorder) headers {
		h := w.Header()
		return headers{h.Get("X-RateLimit-Limit"), h.Get("X-RateLimit-Remaining"), h.Get("X-RateLimit-Reset"), h.Get("Retry-After")}
	}
	check := func(what string, w *httptest.ResponseRecorder, code int, limit, remaining string, retry bool) {
		t.Helper()
		got := headersOf(w)
		reset, err := strconv.Atoi(got.reset)
		if w.Code != code || got.limit != limit || got.remaining != remaining || err != nil || reset < 1 || reset > 60 || (got.retry != "") != retry {
			t.Errorf("%s: %d %+v; want %d with X-RateLimit-Limit %s, -Remaining %s, -Reset 1..60, Retry-After %v",
				what, w.Code, got, code, limit, remaining, retry)
		}
		if retry && got.retry != got.reset {
			t.Errorf("%s: Retry-After %s, want the seconds until the window ends (%s)", what, got.retry, got.reset)
		}
	}

	// A successful refresh passes the rate limit stage too (counted per IP
	// there) and does not count against the pre-auth budget: the first
	// failed attempt below leaves 19.
	w := cs.do(http.MethodPost, "/api/v1/auth/refresh", "", cookie, "", "")
	check("successful refresh", w, http.StatusOK, "600", "599", false)
	w = cs.do(http.MethodGet, "/api/v1/cis", token, "", "", "")
	check("403 of the route authorization", w, http.StatusForbidden, "600", "599", false)
	for i := 1; i <= middleware.DefaultPreAuthPerMinute; i++ {
		w = cs.do(http.MethodGet, "/api/v1/me", "not-a-token", "", "", "")
		check("401 for an invalid bearer, attempt "+strconv.Itoa(i), w, http.StatusUnauthorized, "20", strconv.Itoa(middleware.DefaultPreAuthPerMinute-i), false)
	}
	w = cs.do(http.MethodGet, "/api/v1/me", token, "", "", "")
	check("pre-auth 429 after 20 failed attempts", w, http.StatusTooManyRequests, "20", "0", true)
	if strings.Join(cs.trace, ",") != "pre-auth" {
		t.Errorf("pre-auth 429: stages %v, want only pre-auth", cs.trace)
	}
}

// TestHTTPHandlerSetsLifecycleHeadersBeforeAuth covers WP-568 (API-01,
// F-220) through the handler the server serves: a deprecated operation
// (/ip-addresses, successor /ips) answers 401 without credentials and 403
// from the route authorization with Deprecation, Sunset and the successor
// link, which an early stage of the chain sets before the auth stage; the
// link names the requested item. A current operation carries none.
func TestHTTPHandlerSetsLifecycleHeadersBeforeAuth(t *testing.T) {
	cs := newChainStack(t)
	token, _ := cs.login(t, identity.PermPermissionRead)
	const ip = "7a2a0000-0000-4000-8000-0000000005a8"
	deprecation := "@" + strconv.FormatInt(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC).Unix(), 10)
	for _, tc := range []struct {
		name, path, bearer string
		code               int
		trace, link        string
	}{
		{"401 without credentials", "/api/v1/ip-addresses", "", http.StatusUnauthorized, "pre-auth,auth", "</api/v1/ips>"},
		{"401 for an invalid bearer", "/api/v1/ip-addresses/" + ip, "not-a-token", http.StatusUnauthorized, "pre-auth,auth", "</api/v1/ips/" + ip + ">"},
		{"403 of the route authorization", "/api/v1/ip-addresses", token, http.StatusForbidden, "pre-auth,auth,tenant,entitlement,rate-limit", "</api/v1/ips>"},
		{"403 of the route authorization, one item", "/api/v1/ip-addresses/" + ip, token, http.StatusForbidden, "pre-auth,auth,tenant,entitlement,rate-limit", "</api/v1/ips/" + ip + ">"},
	} {
		w := cs.do(http.MethodGet, tc.path, tc.bearer, "", "", "")
		h := w.Header()
		if w.Code != tc.code || strings.Join(cs.trace, ",") != tc.trace {
			t.Errorf("%s: GET %s -> %d, stages %v; want %d after %s", tc.name, tc.path, w.Code, cs.trace, tc.code, tc.trace)
		}
		if h.Get("Deprecation") != deprecation || h.Get("Sunset") != "Thu, 14 Oct 2027 00:00:00 GMT" ||
			h.Get("Link") != tc.link+`; rel="successor-version"` {
			t.Errorf("%s: GET %s -> Deprecation %q, Sunset %q, Link %q; want %s, Thu, 14 Oct 2027 00:00:00 GMT, %s",
				tc.name, tc.path, h.Get("Deprecation"), h.Get("Sunset"), h.Get("Link"), deprecation, tc.link)
		}
	}
	w := cs.do(http.MethodGet, "/api/v1/ips", "", "", "", "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("Deprecation") != "" || w.Header().Get("Sunset") != "" {
		t.Errorf("GET /api/v1/ips: %d, lifecycle headers %v; want 401 without them", w.Code, w.Header())
	}
}

// TestReadyzNamesAMissingOpenAPISpec covers WP-568 (F-218): a server that
// runs outside production without the OpenAPI document reports it under
// details.openapi of /readyz, without changing the status.
func TestReadyzNamesAMissingOpenAPISpec(t *testing.T) {
	cs := newChainStackWith(t, []server.ReadinessCheck{openAPINotLoadedReadiness()})
	w := cs.do(http.MethodGet, "/readyz", "", "", "", "")
	var body struct {
		Status  string            `json:"status"`
		Details map[string]string `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body.Status != "ready" || body.Details["openapi"] != "not loaded" {
		t.Errorf("GET /readyz: %d %s (%v); want 200, ready, details.openapi \"not loaded\"", w.Code, strings.TrimSpace(w.Body.String()), err)
	}
}

// TestSystemJobsCoverJOB02 covers the scheduler wiring of JOB-02 and OPS-03
// (WP-425, F-044): registerSystemJobs, which main runs, registers on a
// database every system job of the JOB-02 [B] list — offline detector (with
// its worker switch on, WP-447),
// edge cleanup (TOP-02), hard delete of soft-deleted CIs (CI-05) and the
// retention runs (AUD-09), index DDL (DB-04), certificate renewal (the
// certificate.notices run of COL-04) and entitlement warnings (ENT-07). The
// audit runner (AUD-04) is not a scheduled job: the audit chain is written
// synchronously with each change (E-56, F-316 deferred). The digest run
// notify.digest (NTF-04, WP-508) is registered with them. Without a
// database the notice and digest runs are not registered.
func TestSystemJobsCoverJOB02(t *testing.T) {
	t.Setenv(config.WorkerEnvKey(config.WorkerOfflineDetector), "true")
	t.Setenv(config.OfflineDetectorAlias, "")
	cfg := config.Load()
	registered := registeredSystemJobs(t, systemJobTestDeps(t, cfg, true))
	for _, job := range []struct{ requirement, kind string }{
		{"offline detector", lifecycle.OfflineDetectorKind},
		{"edge cleanup (TOP-02)", relationship.CleanupKind},
		{"hard delete of soft-deleted CIs (CI-05)", jobs.RetentionSoftDeleted},
		{"retention of the audit log (AUD-09)", jobs.RetentionAuditLog},
		{"retention of the history (AUD-09)", jobs.RetentionHistory},
		{"index DDL (DB-04)", citype.IndexJobKind},
		{"certificate renewal (COL-04)", jobs.CertificateNoticesKind},
		{"entitlement warnings (ENT-07)", jobs.EntitlementNoticesKind},
		{"route audit intents (AUD-07, WP-504)", audit.RouteIntentsKind},
		{"timeout reaper of the jobs (JOB-01, WP-503)", jobs.ReapKind},
		{"notification digests (NTF-04, WP-508)", notify.DigestKind},
		{"trial end (ENT-07, WP-884)", operator.TrialEndKind},
		{"credential age check (CRD-05, WP-897)", credential.AgeCheckKind},
		{"retention of the configuration versions (ALR-07, WP-1052)", jobs.RetentionConfigSnapshot},
		{"retention of the workflow runs (WFL-02, E-241, WP-1093)", jobs.RetentionWorkflowRuns},
	} {
		if !registered[job.kind] {
			t.Errorf("JOB-02 %s: kind %s is not registered (registered: %v)", job.requirement, job.kind, registered)
		}
	}

	// The cleanup of the full exports (TLC-05, WP-1151): hourly with the
	// module org_lifecycle and the database; without the module not.
	lifecycleModules, err := server.ParseModules("org_lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	for _, withDB := range []bool{true, false} {
		deps := systemJobTestDeps(t, cfg, withDB)
		deps.modules = lifecycleModules
		if kinds := registeredSystemJobs(t, deps); kinds[export.OrgExportCleanupKind] != withDB {
			t.Errorf("JOB-02 cleanup of the full exports (TLC-05) with the module org_lifecycle (database %v): registered %v",
				withDB, kinds[export.OrgExportCleanupKind])
		}
		for _, job := range systemJobs(deps) {
			if job.Kind == export.OrgExportCleanupKind && job.Spec != "23 * * * *" {
				t.Errorf("%s: spec %q, want hourly 23 * * * *", job.Kind, job.Spec)
			}
		}
	}
	noLifecycle, err := server.ParseModules(server.ModuleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	deps := systemJobTestDeps(t, cfg, true)
	deps.modules = noLifecycle
	if registeredSystemJobs(t, deps)[export.OrgExportCleanupKind] {
		t.Errorf("without the module org_lifecycle %s is registered", export.OrgExportCleanupKind)
	}

	// The alarm retention (ALR-01, WP-981): alarm.retention daily with the
	// module monitoring and the database; without the module or the
	// database not.
	if registered[alarm.RetentionKind] {
		t.Errorf("without the module monitoring %s is registered", alarm.RetentionKind)
	}
	monitoringModules, err := server.ParseModules(server.ModuleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	for _, withDB := range []bool{true, false} {
		deps := systemJobTestDeps(t, cfg, withDB)
		deps.modules = monitoringModules
		kinds := registeredSystemJobs(t, deps)
		if kinds[alarm.RetentionKind] != withDB {
			t.Errorf("JOB-02 alarm retention (ALR-01) with the module monitoring (database %v): registered %v",
				withDB, kinds[alarm.RetentionKind])
		}
		for _, job := range systemJobs(deps) {
			if job.Kind == alarm.RetentionKind && (job.Spec != "37 4 * * *" || job.MaxCatchUp != 1) {
				t.Errorf("%s: spec %q, max catch-up %d; want daily 37 4 * * * and 1", job.Kind, job.Spec, job.MaxCatchUp)
			}
			if job.Kind == probe.AssignKind && job.Spec != "@every 10s" {
				t.Errorf("%s: spec %q, want @every 10s", job.Kind, job.Spec)
			}
		}
		// The probe assignment (PRB-03, WP-941): probe.assign with the
		// module monitoring and the database, the worker probe_assign.
		if kinds[probe.AssignKind] != withDB {
			t.Errorf("JOB-02 probe assignment (PRB-03) with the module monitoring (database %v): registered %v",
				withDB, kinds[probe.AssignKind])
		}
		// The probe suspension (PKG-09, WP-942): every minute with the
		// module monitoring and the database.
		if kinds[probe.SuspendKind] != withDB {
			t.Errorf("JOB-02 probe suspension (PKG-09) with the module monitoring (database %v): registered %v",
				withDB, kinds[probe.SuspendKind])
		}
		for _, job := range systemJobs(deps) {
			if job.Kind == probe.SuspendKind && (job.Spec != "@every 1m" || job.MaxCatchUp != 1) {
				t.Errorf("%s: spec %q, max catch-up %d; want @every 1m and 1", job.Kind, job.Spec, job.MaxCatchUp)
			}
		}
		// The dispatch of the automatic ping probes (MON-02, WP-949): every
		// 15 minutes with the module monitoring, the database and the job
		// queue.
		if kinds[probe.AutocreateDispatchKind] {
			t.Errorf("%s is registered without the job queue", probe.AutocreateDispatchKind)
		}
		deps.jobQueue = jobs.NewQueue(jobs.NewService(deps.repos.Jobs), nil)
		queued := registeredSystemJobs(t, deps)
		if queued[probe.AutocreateDispatchKind] != withDB {
			t.Errorf("JOB-02 autocreate dispatch (MON-02) with the module monitoring and the job queue (database %v): registered %v",
				withDB, queued[probe.AutocreateDispatchKind])
		}
		for _, job := range systemJobs(deps) {
			if job.Kind == probe.AutocreateDispatchKind && job.Spec != "@every 15m" {
				t.Errorf("%s: spec %q, want @every 15m", job.Kind, job.Spec)
			}
		}
	}
	if registered[probe.AssignKind] {
		t.Errorf("without the module monitoring %s is registered", probe.AssignKind)
	}
	// The nightly compliance evaluation (SEC-03, WP-1126): with the module
	// compliance and the database, daily 02:30.
	if registered[compliance.NightlyKind] {
		t.Errorf("without the module compliance %s is registered", compliance.NightlyKind)
	}
	complianceModules, err := server.ParseModules("compliance")
	if err != nil {
		t.Fatal(err)
	}
	for _, withDB := range []bool{true, false} {
		deps := systemJobTestDeps(t, cfg, withDB)
		deps.modules = complianceModules
		if kinds := registeredSystemJobs(t, deps); kinds[compliance.NightlyKind] != withDB {
			t.Errorf("JOB-02 nightly compliance evaluation with the module compliance (database %v): registered %v",
				withDB, kinds[compliance.NightlyKind])
		}
		for _, job := range systemJobs(deps) {
			if job.Kind == compliance.NightlyKind && (job.Spec != "30 2 * * *" || job.MaxCatchUp != 1) {
				t.Errorf("%s: spec %q, max catch-up %d; want 30 2 * * * and 1", job.Kind, job.Spec, job.MaxCatchUp)
			}
		}
	}
	// Its worker switch off: not registered.
	t.Setenv(config.WorkerEnvKey(config.WorkerProbeAssign), "false")
	offDeps := systemJobTestDeps(t, config.Load(), true)
	offDeps.modules = monitoringModules
	if registeredSystemJobs(t, offDeps)[probe.AssignKind] {
		t.Errorf("%s is registered with %s=false", probe.AssignKind, config.WorkerEnvKey(config.WorkerProbeAssign))
	}

	// The serial sweep of the workflow runs (WFL-02, EVT-01, WP-1089):
	// workflow.serial_sweep every minute with the module workflows and the
	// database; without the module or the database not.
	if registered[workflow.SerialSweepKind] {
		t.Errorf("without the module workflows %s is registered", workflow.SerialSweepKind)
	}
	workflowModules, err := server.ParseModules("workflows")
	if err != nil {
		t.Fatal(err)
	}
	for _, withDB := range []bool{true, false} {
		deps := systemJobTestDeps(t, cfg, withDB)
		deps.modules = workflowModules
		kinds := registeredSystemJobs(t, deps)
		for _, kind := range []string{workflow.SerialSweepKind, workflow.ScheduleKind} {
			if kinds[kind] != withDB {
				t.Errorf("JOB-02 %s (database %v): registered %v", kind, withDB, kinds[kind])
			}
		}
		for _, job := range systemJobs(deps) {
			if (job.Kind == workflow.SerialSweepKind || job.Kind == workflow.ScheduleKind) && (job.Spec != "* * * * *" || job.MaxCatchUp != 1) {
				t.Errorf("%s: spec %q, max catch-up %d; want every minute and 1", job.Kind, job.Spec, job.MaxCatchUp)
			}
			if job.Kind == workflow.ScheduleKind && job.Timeout != time.Minute {
				t.Errorf("%s: timeout %v, want 1 min", job.Kind, job.Timeout)
			}
		}
	}
	// Workflow schedules (JOB-02 [P3], WP-1091): workflow.schedule runs
	// with the module workflows only (checked above).
	if registered[workflow.ScheduleKind] {
		t.Errorf("without the module workflows %s is registered", workflow.ScheduleKind)
	}

	// The ADMP cache (ADM-03, WP-929): admp.refresh_linked every 15
	// minutes and admp.expire hourly with the module admp, in both profiles
	// (--no-db on the memory repositories); without the module neither.
	if registered[admp.RefreshLinkedKind] || registered[admp.ExpireKind] {
		t.Errorf("without the module admp its system jobs are registered: %v", registered)
	}
	admpModules, err := server.ParseModules("admp")
	if err != nil {
		t.Fatal(err)
	}
	for _, withDB := range []bool{true, false} {
		deps := systemJobTestDeps(t, cfg, withDB)
		deps.modules = admpModules
		specs := map[string]string{}
		for _, job := range systemJobs(deps) {
			specs[job.Kind] = job.Spec
		}
		if specs[admp.RefreshLinkedKind] != "@every 15m" || specs[admp.ExpireKind] != "@every 1h" {
			t.Errorf("with the module admp (database %v): refresh_linked %q, expire %q; want @every 15m and @every 1h",
				withDB, specs[admp.RefreshLinkedKind], specs[admp.ExpireKind])
		}
		if kinds := registeredSystemJobs(t, deps); !kinds[admp.RefreshLinkedKind] || !kinds[admp.ExpireKind] {
			t.Errorf("JOB-02 ADMP cache (database %v): not registered (registered: %v)", withDB, kinds)
		}
	}

	// --no-db: the notice runs need the database and are not registered.
	for kind := range registeredSystemJobs(t, systemJobTestDeps(t, cfg, false)) {
		if kind == jobs.EntitlementNoticesKind || kind == jobs.CertificateNoticesKind || kind == audit.RouteIntentsKind ||
			kind == jobs.ReapKind || kind == notify.DigestKind || kind == operator.TrialEndKind || kind == credential.AgeCheckKind ||
			kind == jobs.RetentionConfigSnapshot {
			t.Errorf("without a database %s is registered", kind)
		}
	}
}

// TestOfflineDetectorRunsUnderItsWorkerSwitch covers WP-447 (OPS-02,
// F-289): ci.offline_detector is registered only while the switch of the
// worker offline_detector is on. RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED
// is the switch; the former RETICORA_OFFLINE_DETECTOR_ENABLED still
// applies without it and is reported as deprecated, next to it it is
// ignored. The system job list main registers (systemJobs) holds
// ci.offline_detector every five minutes exactly while the switch is on
// (WP-585, REC-09, LCY-05; the registration proof of WP-157).
func TestOfflineDetectorRunsUnderItsWorkerSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, convention, alias string
		registered              bool
		warning                 string
	}{
		{"default", "", "", true, ""},
		{"convention on", "true", "", true, ""},
		{"convention off", "false", "", false, ""},
		{"alias off", "", "false", false, "is deprecated: use RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED"},
		{"alias on", "", "true", true, "is deprecated: use RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED"},
		{"convention before alias", "true", "false", true, "is deprecated and ignored"},
		{"convention off, alias on", "false", "true", false, "is deprecated and ignored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.WorkerEnvKey(config.WorkerOfflineDetector), tc.convention)
			t.Setenv(config.OfflineDetectorAlias, tc.alias)
			cfg := config.Load()
			if got := registeredSystemJobs(t, systemJobTestDeps(t, cfg, true))[lifecycle.OfflineDetectorKind]; got != tc.registered {
				t.Errorf("%s registered: %v, want %v", lifecycle.OfflineDetectorKind, got, tc.registered)
			}
			var specs []string
			for _, job := range systemJobs(systemJobTestDeps(t, cfg, true)) {
				if job.Kind == lifecycle.OfflineDetectorKind {
					specs = append(specs, job.Spec)
				}
			}
			if want := map[bool][]string{true: {"@every 5m"}, false: nil}[tc.registered]; !slices.Equal(specs, want) {
				t.Errorf("system jobs %s: schedules %q, want %q", lifecycle.OfflineDetectorKind, specs, want)
			}
			warnings := strings.Join(cfg.Warnings(), "\n")
			if tc.warning == "" && strings.Contains(warnings, config.OfflineDetectorAlias) ||
				tc.warning != "" && !strings.Contains(warnings, config.OfflineDetectorAlias+" "+tc.warning) {
				t.Errorf("warnings %q, want %q", warnings, tc.warning)
			}
		})
	}
}

// systemJobTestDeps are the dependencies of the system jobs with cfg; with
// withDB on a pool that never connects (registration only builds the runs),
// without as with --no-db.
func systemJobTestDeps(t *testing.T, cfg *config.Config, withDB bool) *systemJobDeps {
	t.Helper()
	var pool *pgxpool.Pool
	repos := server.MemoryRepositories()
	if withDB {
		var err error
		if pool, err = pgxpool.New(context.Background(), "postgres://reticora@127.0.0.1:1/reticora?sslmode=disable"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		repos = server.PostgresRepositories(pool, audit.NewPGRecorder())
	}
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	modules, err := server.ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{PollInterval: -1})
	t.Cleanup(func() { _ = dispatcher.Shutdown(context.Background()) })
	center := notify.NewMemoryCenter()
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(nil))
	return &systemJobDeps{cfg: cfg, pool: pool, encryptor: encryptor, repos: &repos, modules: modules,
		entitlements: entitlement.NewService(repos.Entitlement, entitlement.Options{}), dispatcher: dispatcher,
		notifier: notifier}
}

// registeredSystemJobs registers the system jobs as main does and returns
// the kinds of the scheduler.
func registeredSystemJobs(t *testing.T, d *systemJobDeps) map[string]bool {
	t.Helper()
	scheduler := jobs.NewScheduler(jobs.NewMemoryLocker(), jobs.NewMemoryStateStore())
	if err := registerSystemJobs(scheduler, d); err != nil {
		t.Fatalf("register the system jobs: %v", err)
	}
	registered := map[string]bool{}
	for _, kind := range scheduler.Kinds() {
		registered[kind] = true
	}
	return registered
}

// TestHealthWorkerIsWiredInG1 covers WP-480 (DB-05, MON-02, F-087): the
// workers main builds (buildWorkers) and starts (workerSwitch) include the
// worker health under the G1 default module set (RETICORA_MODULES unset),
// while the alert evaluator (monitoring) stays off there. With
// RETICORA_MODULES=alerting both run and ci.health is refreshed exactly once
// per cycle of the worker health: the evaluator no longer refreshes it a
// second time.
func TestHealthWorkerIsWiredInG1(t *testing.T) {
	t.Setenv(config.WorkerEnvKey(config.WorkerMonitoring), "true")
	t.Setenv(config.WorkerEnvKey(config.WorkerHealth), "true")
	cfg := config.Load()
	refresher, alerts := &countingHealth{}, &countingAlerts{}
	repos := server.MemoryRepositories()
	repos.Metrics = evaluatingMetrics{MetricStore: repos.Metrics, alerts: alerts, health: refresher}
	built, err := buildWorkers(&workerDeps{cfg: cfg, repos: &repos, cycle: testCycle})
	if err != nil {
		t.Fatal(err)
	}
	var pair []server.Worker
	for _, w := range built {
		if w.Name == config.WorkerHealth || w.Name == config.WorkerMonitoring {
			pair = append(pair, w)
		}
	}
	if len(pair) != 2 {
		t.Fatalf("buildWorkers: %d of the workers health and monitoring, want both", len(pair))
	}

	g1, err := server.ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	if enabled := workerSwitch(cfg, g1); !enabled(config.WorkerHealth) || enabled(config.WorkerMonitoring) {
		t.Errorf("RETICORA_MODULES unset: health starts %v, monitoring starts %v; want true, false",
			enabled(config.WorkerHealth), enabled(config.WorkerMonitoring))
	}

	alerting, err := server.ParseModules("alerting")
	if err != nil {
		t.Fatal(err)
	}
	runsBefore := testutil.ToFloat64(observability.WorkerRuns.WithLabelValues(config.WorkerHealth))
	set, err := server.StartWorkers(workerSwitch(cfg, alerting), pair...)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for (refresher.calls.Load() < 3 || alerts.calls.Load() < 3) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = set.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if started := set.Started(); len(started) != 2 {
		t.Errorf("RETICORA_MODULES=alerting: started %v, want monitoring and health", started)
	}
	cycles := testutil.ToFloat64(observability.WorkerRuns.WithLabelValues(config.WorkerHealth)) - runsBefore
	if alerts.calls.Load() < 3 || cycles < 3 {
		t.Fatalf("evaluations %d, health cycles %v; want at least 3 each", alerts.calls.Load(), cycles)
	}
	if got := float64(refresher.calls.Load()); got != cycles {
		t.Errorf("ci.health refreshed %v times in %v cycles of the worker health (%d evaluations); want exactly once per cycle",
			got, cycles, alerts.calls.Load())
	}
}

// countingHealth counts the refreshes of ci.health.
type countingHealth struct{ calls atomic.Int64 }

func (h *countingHealth) RefreshHealth(context.Context) error {
	h.calls.Add(1)
	return nil
}

// countingAlerts holds no alert rule and counts the evaluations.
type countingAlerts struct {
	monitoring.AlertStore
	calls atomic.Int64
}

func (a *countingAlerts) ListEnabled(context.Context) ([]monitoring.AlertRule, error) {
	a.calls.Add(1)
	return nil, nil
}

// captureLog makes slog write JSON records into the returned buffer until
// the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// logRecords returns the JSON records of buf with the message msg.
func logRecords(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("log line %q: %v", scanner.Text(), err)
		}
		if record["msg"] == msg {
			out = append(out, record)
		}
	}
	return out
}

// TestSignupServiceNamesTheMissingPrerequisites covers the start log of
// the self-signup (TLC-01): a default install without SMTP, Keycloak admin
// client or verify URL answers every signup with 503, so the log names
// exactly the settings that are missing, at WARN; with all of them the
// service is available and the log says so at INFO. The operator path
// invites the first org_admin only with the central SMTP server (the
// realm's mail), so an admin client alone does not turn the invitations
// on.
func TestSignupServiceNamesTheMissingPrerequisites(t *testing.T) {
	// The pool never connects: building the service only stores it.
	pool, err := pgxpool.New(context.Background(), "postgres://reticora@127.0.0.1:1/reticora?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sender, err := notify.NewSMTPSender(&notify.SMTPConfig{Host: "smtp.example.com", Port: 587,
		From: "cmdb@example.com", TLS: "starttls"})
	if err != nil {
		t.Fatal(err)
	}
	complete := func() *config.Config {
		return &config.Config{
			OIDCIssuerURL:             "https://id.example.com/realms/reticora",
			KeycloakAdminClientID:     "reticora-server-admin",
			KeycloakAdminClientSecret: "kc-admin-client-secret-value",
			SignupVerifyURL:           "https://cmdb.example.com/onboarding",
			SMTPHost:                  "smtp.example.com",
			TrialPlan:                 "standard",
			TrialDays:                 30,
			TrialMaxCIs:               100,
		}
	}
	noSMTP := func(c *config.Config) { c.SMTPHost = "" }
	for _, tc := range []struct {
		name            string
		mutate          []func(*config.Config)
		noDB            bool
		want            []string
		operatorInvites bool
	}{
		{name: "complete", operatorInvites: true},
		{name: "no SMTP server", mutate: []func(*config.Config){noSMTP},
			want: []string{"RETICORA_SMTP_HOST"}},
		{name: "no admin client", mutate: []func(*config.Config){func(c *config.Config) { c.KeycloakAdminClientID = "" }},
			want: []string{"RETICORA_KEYCLOAK_ADMIN_CLIENT_ID"}},
		{name: "no admin client secret", mutate: []func(*config.Config){func(c *config.Config) { c.KeycloakAdminClientSecret = "" }},
			want: []string{"RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET"}},
		{name: "no verify URL", mutate: []func(*config.Config){func(c *config.Config) { c.SignupVerifyURL = "" }},
			want: []string{"RETICORA_SIGNUP_VERIFY_URL"}, operatorInvites: true},
		{name: "default install", mutate: []func(*config.Config){noSMTP, func(c *config.Config) {
			c.KeycloakAdminClientID, c.KeycloakAdminClientSecret, c.SignupVerifyURL = "", "", ""
		}}, want: []string{"RETICORA_SMTP_HOST", "RETICORA_KEYCLOAK_ADMIN_CLIENT_ID", "RETICORA_SIGNUP_VERIFY_URL"}},
		{name: "without database", noDB: true, want: []string{"database (the server runs with --no-db)"}, operatorInvites: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := complete()
			for _, m := range tc.mutate {
				m(cfg)
			}
			smtp := sender
			if cfg.SMTPHost == "" {
				smtp = nil
			}
			db := pool
			if tc.noDB {
				db = nil
			}
			logs := captureLog(t)
			svc := signupService(cfg, db, smtp)
			if svc.Available() != (len(tc.want) == 0) {
				t.Errorf("Available() = %v with missing %v", svc.Available(), tc.want)
			}
			records := logRecords(t, logs, "self-signup configured")
			if len(records) != 1 {
				t.Fatalf("want one \"self-signup configured\" record, got %v\n%s", records, logs)
			}
			record := records[0]
			if record["operator_invitations"] != tc.operatorInvites {
				t.Errorf("operator_invitations = %v, want %v", record["operator_invitations"], tc.operatorInvites)
			}
			if len(tc.want) == 0 {
				if record["level"] != "INFO" || record["available"] != true || record["missing"] != nil {
					t.Errorf("available service logged %v, want INFO available=true", record)
				}
				return
			}
			var missing []string
			if list, ok := record["missing"].([]any); ok {
				for _, v := range list {
					missing = append(missing, v.(string))
				}
			}
			if record["level"] != "WARN" || record["available"] != false || !slices.Equal(missing, tc.want) {
				t.Errorf("logged %v, want WARN available=false missing=%v", record, tc.want)
			}
			if strings.Contains(logs.String(), "kc-admin-client-secret-value") {
				t.Errorf("the log repeats the client secret:\n%s", logs)
			}
		})
	}
}

// TestMissingSignupPrerequisitesNamesABrokenAdminClient: with id and secret
// set but a client that could not be built (an unreadable CA bundle, an
// issuer that is no realm URL) the log points to the error of the admin
// client instead of naming a setting that is there.
func TestMissingSignupPrerequisitesNamesABrokenAdminClient(t *testing.T) {
	cfg := &config.Config{KeycloakAdminClientID: "reticora-server-admin", KeycloakAdminClientSecret: "secret",
		SignupVerifyURL: "https://cmdb.example.com/onboarding"}
	got := missingSignupPrerequisites(cfg, true, true, false)
	if len(got) != 1 || !strings.Contains(got[0], "Keycloak admin client disabled") {
		t.Errorf("missing = %v, want the hint to the disabled admin client", got)
	}
	if got := missingSignupPrerequisites(cfg, true, true, true); len(got) != 0 {
		t.Errorf("complete prerequisites: missing = %v", got)
	}
}

// TestInvitationAccountsNeedTheCentralSMTPServer: an accepted invitation
// creates a verified IdP account only where the invitation mail is
// delivered. With the admin client the handler sends the token to the
// invitee's mailbox only (no token in the issue response), so without the
// central SMTP server the client is not wired: the inviter keeps the link
// of the response, and the acceptance links an existing IdP account at the
// first login instead of creating one for an address nobody proved.
func TestInvitationAccountsNeedTheCentralSMTPServer(t *testing.T) {
	withClient := &config.Config{
		OIDCIssuerURL:             "https://id.example.com/realms/reticora",
		KeycloakAdminClientID:     "reticora-server-admin",
		KeycloakAdminClientSecret: "kc-admin-client-secret-value",
	}
	const noSMTPMsg = "no central SMTP server (RETICORA_SMTP_HOST): accepted invitations create no IdP account, the invitee signs in with an existing one"

	logs := captureLog(t)
	if got := invitationAccounts(withClient, true); got == nil {
		t.Error("admin client and central SMTP: no accounts client for invitations")
	}
	if records := logRecords(t, logs, noSMTPMsg); len(records) != 0 {
		t.Errorf("with central SMTP the log says %v", records)
	}

	logs = captureLog(t)
	if got := invitationAccounts(withClient, false); got != nil {
		t.Errorf("admin client without central SMTP: accounts client %T wired, want none", got)
	}
	if records := logRecords(t, logs, noSMTPMsg); len(records) != 1 || records[0]["level"] != "INFO" {
		t.Errorf("without central SMTP: records %v, want one INFO %q", records, noSMTPMsg)
	}

	logs = captureLog(t)
	if got := invitationAccounts(&config.Config{OIDCIssuerURL: withClient.OIDCIssuerURL}, true); got != nil {
		t.Errorf("no admin client: accounts client %T wired, want none", got)
	}
	if got := invitationAccounts(&config.Config{OIDCIssuerURL: withClient.OIDCIssuerURL}, false); got != nil {
		t.Errorf("neither admin client nor central SMTP: accounts client %T wired", got)
	}
	if records := logRecords(t, logs, noSMTPMsg); len(records) != 0 {
		t.Errorf("without admin client the log says %v", records)
	}
}

// TestInvitationMailerNeedsTheCentralSMTPServer: the invitation mailer,
// which the handler needs to wire the accounts client (WithAccounts), sends
// over the central SMTP server; without that server it is a nil interface,
// not a mailer without sender, so issue and renewal keep the link in their
// response.
func TestInvitationMailerNeedsTheCentralSMTPServer(t *testing.T) {
	if got := invitationMailer(nil); got != nil {
		t.Errorf("no central SMTP: invitation mailer %#v, want none", got)
	}
	sender, err := notify.NewSMTPSender(&notify.SMTPConfig{Host: "smtp.example.com", From: "noreply@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := invitationMailer(sender).(notify.InvitationMailer); !ok || got.Sender != sender {
		t.Errorf("central SMTP: invitation mailer %#v, want notify.InvitationMailer over the central sender", got)
	}
}

// TestVulnfeedCSAFJobRegistration covers WP-1118 AK4: vulnfeed.csaf runs
// with the module findings and the database every
// RETICORA_VULNFEED_CSAF_INTERVAL; without the module, without the
// database or air-gapped without an explicit provider list it is not
// registered.
func TestVulnfeedCSAFJobRegistration(t *testing.T) {
	t.Setenv("RETICORA_VULNFEED_CSAF_PROVIDERS", "")
	t.Setenv("RETICORA_VULNFEED_CSAF_INTERVAL", "3h")
	t.Setenv("RETICORA_AIR_GAPPED", "")
	findings, err := server.ParseModules("findings")
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(withDB bool, modules *server.ModuleSet) map[string]string {
		deps := systemJobTestDeps(t, config.Load(), withDB)
		if modules != nil {
			deps.modules = modules
		}
		out := map[string]string{}
		for _, job := range systemJobs(deps) {
			out[job.Kind] = job.Spec
		}
		return out
	}
	if spec := kinds(true, findings)[vulnfeed.CSAFKind]; spec != "@every 3h0m0s" {
		t.Fatalf("with the module findings: %s spec %q, want @every 3h0m0s", vulnfeed.CSAFKind, spec)
	}
	if _, ok := kinds(true, nil)[vulnfeed.CSAFKind]; ok {
		t.Errorf("without the module findings %s is registered", vulnfeed.CSAFKind)
	}
	if _, ok := kinds(false, findings)[vulnfeed.CSAFKind]; ok {
		t.Errorf("without a database %s is registered", vulnfeed.CSAFKind)
	}
	if spec := kinds(true, findings)[vulnfeed.NVDKind]; spec != "@every 2h0m0s" {
		t.Errorf("with the module findings: %s spec %q, want @every 2h0m0s", vulnfeed.NVDKind, spec)
	}
	if _, ok := kinds(true, nil)[vulnfeed.NVDKind]; ok {
		t.Errorf("without the module findings %s is registered", vulnfeed.NVDKind)
	}
	t.Setenv("RETICORA_AIR_GAPPED", "true")
	if _, ok := kinds(true, findings)[vulnfeed.NVDKind]; ok {
		t.Errorf("air-gapped %s is registered", vulnfeed.NVDKind)
	}
	if _, ok := kinds(true, findings)[vulnfeed.CSAFKind]; ok {
		t.Errorf("air-gapped without RETICORA_VULNFEED_CSAF_PROVIDERS %s is registered", vulnfeed.CSAFKind)
	}
	t.Setenv("RETICORA_VULNFEED_CSAF_PROVIDERS", "https://csaf-mirror.internal.example/.well-known/csaf/provider-metadata.json")
	if _, ok := kinds(true, findings)[vulnfeed.CSAFKind]; !ok {
		t.Errorf("air-gapped with an own mirror %s is not registered", vulnfeed.CSAFKind)
	}
}
