// Phase-1 acceptance audit, group T6-epic-b – shared test harness.
//
// Builds the complete API router as cmd/server/main.go does (PostgreSQL
// repositories, PG notification center, outbox event emitter, CI service with
// the metamodel field resolver, egress-checked webhook dispatcher) and calls
// it with an authenticated principal, so tests run through the production
// authorization (authz.go route map), tenant middleware and RLS.
//
// Used by audit_t6_*_test.go (regression, no build tag) and
// audit_t6_*_auditdefect_test.go (defect, build tag auditdefect).
// WP-116, WP-120, WP-128, WP-129, WP-133, WP-136, WP-137, WP-139; also
// discovery_job_tracking_integration_test.go (WP-502).
//
// Retest (regression tests using it):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -count=1 -v -run 'TestAuditT6' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// t6Env is a production-like router on the scopetest fixture.
type t6Env struct {
	F *scopetest.Fixture
	// Repos are the repositories the router was built with.
	Repos   server.Repositories
	handler http.Handler
}

// t6Response is a recorded response.
type t6Response struct {
	Code int
	Body []byte
}

// t6As is the caller of a request: organization, user and permissions with an
// org-wide scope.
type t6As struct {
	Org   string
	User  string
	Perms []identity.Permission
}

// t6NewEnv seeds the fixture with tag and builds the router; ra replaces the
// route auditor when not nil (failure injection).
func t6NewEnv(t *testing.T, tag string, ra middleware.RouteAuditor) *t6Env {
	t.Helper()
	f := scopetest.Seed(t, tag)
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	policy := egress.Options{AllowPrivate: true}
	emitter := events.NewPGEmitter(f.App)
	dispatcher := webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
		Deliveries: repos.WebhookDeliveries, PollInterval: -1, Events: emitter})
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	notifications := &notify.Center{
		Channels:    notify.NewPGRepository(f.App, notify.NewSealer(encryptor)),
		Inbox:       notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App),
		Deliveries:  notify.NewPGDeliveryStore(f.App),
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "audit-t6", Entitlements: entitlements, Dispatcher: dispatcher,
		CIService:     ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials:   credential.NewService(repos.Credential, encryptor),
		OIDC:          identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:        policy,
		AuditPool:     f.App,
		Audit:         audit.NewHandler(f.App),
		RouteAuditor:  ra,
		Notifications: notifications,
		Events:        emitter,
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return &t6Env{F: f, Repos: repos, handler: middleware.RequestID(middleware.TenantMiddleware(mux))}
}

// Call sends one request as the given caller.
func (e *t6Env) Call(t *testing.T, as t6As, method, path, body string) t6Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: as.User, OrganizationID: as.Org, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: as.Perms, Credential: "audit-t6"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req.WithContext(ctx))
	return t6Response{Code: w.Code, Body: w.Body.Bytes()}
}

// Count runs a count query on the maintenance pool.
func (e *t6Env) Count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.F.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// t6RolePerms returns the permissions of a standard role (RBA-02 matrix).
func t6RolePerms(t *testing.T, role string) []identity.Permission {
	t.Helper()
	keys, ok := permission.StandardRolePermissions(role)
	if !ok {
		t.Fatalf("standard role %s unknown", role)
	}
	out := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		out = append(out, identity.Permission(k))
	}
	return out
}

// t6ID returns the "id" of a JSON object response.
func t6ID(b []byte) string {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	s, _ := m["id"].(string)
	return s
}

// t6Short shortens a response body for messages.
func t6Short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}
