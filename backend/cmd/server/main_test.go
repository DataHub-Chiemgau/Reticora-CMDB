package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
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
	if token, err = cs.issuer.Issue(claims); err != nil {
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

// TestSystemJobsCoverJOB02 covers the scheduler wiring of JOB-02 and OPS-03
// (WP-425, F-044): registerSystemJobs, which main runs, registers on a
// database every system job of the JOB-02 [B] list — offline detector (with
// its worker switch on, WP-447),
// edge cleanup (TOP-02), hard delete of soft-deleted CIs (CI-05) and the
// retention runs (AUD-09), index DDL (DB-04), certificate renewal (the
// certificate.notices run of COL-04) and entitlement warnings (ENT-07). The
// audit runner (AUD-04) is not a scheduled job: the audit chain is written
// synchronously with each change (E-56, F-316 deferred). Without a database
// the notice runs are not registered.
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
	} {
		if !registered[job.kind] {
			t.Errorf("JOB-02 %s: kind %s is not registered (registered: %v)", job.requirement, job.kind, registered)
		}
	}

	// --no-db: the notice runs need the database and are not registered.
	for kind := range registeredSystemJobs(t, systemJobTestDeps(t, cfg, false)) {
		if kind == jobs.EntitlementNoticesKind || kind == jobs.CertificateNoticesKind {
			t.Errorf("without a database %s is registered", kind)
		}
	}
}

// TestOfflineDetectorRunsUnderItsWorkerSwitch covers WP-447 (OPS-02,
// F-289): ci.offline_detector is registered only while the switch of the
// worker offline_detector is on. RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED
// is the switch; the former RETICORA_OFFLINE_DETECTOR_ENABLED still
// applies without it and is reported as deprecated, next to it it is
// ignored.
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
	return &systemJobDeps{cfg: cfg, pool: pool, encryptor: encryptor, repos: &repos, modules: modules,
		entitlements: entitlement.NewService(repos.Entitlement, entitlement.Options{}), dispatcher: dispatcher}
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
