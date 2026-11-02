package main

// WP-365 (AGT-03): the agent credential through the production chain. The
// handler is newHTTPHandler with the stages cmd/server/main.go uses
// (Pre-Auth -> Auth -> Tenant -> Entitlement -> Rate-Limit -> Authz ->
// Validation -> Idempotency), over the PostgreSQL repositories of the
// runtime login (scopetest.AppDSN, RLS active), with the module agents on
// and the entitlement endpoint_agent enforced. The enrollment returns the
// agent credential; telemetry with it lands in PostgreSQL. A credential of
// organization B never reaches an agent of organization A, a revoked
// credential is refused.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//	  go test -count=1 -v -run '^TestAgentCredentialThroughTheProductionChain$' ./cmd/server/

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// agentChain is the production handler over PostgreSQL.
type agentChain struct {
	t       *testing.T
	f       *scopetest.Fixture
	handler http.Handler
	issuer  *identity.SessionIssuer
}

func newAgentChain(t *testing.T) *agentChain {
	t.Helper()
	f := scopetest.Seed(t, "7e")
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	store := cache.NewMemoryStore()
	issuer, err := loadSessionIssuer(&config.Config{SessionKeyPath: testSessionKeyPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	issuer = issuer.WithRevocations(identity.NewSessionRevocations(store))
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Cache: store, Enforce: true})
	if _, err = entitlements.ProvisionOrganizations(context.Background(), map[string]entitlement.Plan{
		f.OrgA: entitlement.PlanEnterprise, f.OrgB: entitlement.PlanEnterprise,
	}); err != nil {
		t.Fatalf("provision entitlements: %v", err)
	}
	modules, err := server.ParseModules("agents")
	if err != nil {
		t.Fatal(err)
	}
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	apiKeys := identity.NewAPIKeyServiceWithStore(repos.APIKeys).WithOwnerAccess(repos.Permission).
		WithServiceAccountAccess(user.ServiceAccountAccess{Repo: repos.ServiceAccounts})
	handler, err := newHTTPHandler(repos, server.Options{
		Version: "wp-365", Modules: modules, Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Dispatcher:  webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries, PollInterval: -1}),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Sessions:    issuer, RefreshSessions: identity.NewRefreshSessions(store, issuer.Revocations()),
	}, httpStages{
		preAuth:     middleware.PreAuthRateLimiter(middleware.DefaultPreAuthPerMinute, store),
		auth:        middleware.AuthMiddlewareWithAPIKeys(issuer, apiKeys),
		tenant:      middleware.TenantMiddleware,
		entitlement: entitlements.Middleware,
		rateLimit:   middleware.RateLimiterWithLimits(middleware.RateLimits{}, store),
		validation:  middleware.OpenAPIValidation,
		idempotency: middleware.IdempotencyWithStore(store),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &agentChain{t: t, f: f, handler: handler, issuer: issuer}
}

// adminToken is the session of an organization admin with agent:manage.
func (c *agentChain) adminToken(orgID string) string {
	c.t.Helper()
	now := time.Now().UTC()
	token, err := c.issuer.Issue(identity.SessionClaims{
		ID: "wp365-admin-" + orgID, Subject: c.f.AppUser(c.t, orgID, "wp365-admin-"+orgID[len(orgID)-4:]), OrganizationID: orgID,
		Permissions: []identity.Permission{identity.PermAgentManage, identity.PermAgentRead},
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		IssuedAt:    now, ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return token
}

func (c *agentChain) post(path, bearer string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		c.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.RemoteAddr = "203.0.113.65:41000"
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, req)
	return w
}

// enroll creates an enrollment token for the client and enrolls the agent
// with it; it returns the agent credential.
func (c *agentChain) enroll(admin, clientID, agentID string) string {
	c.t.Helper()
	w := c.post("/api/v1/agents/enrollment-tokens", admin, map[string]any{"client_id": clientID})
	var tok agent.EnrollmentToken
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &tok) != nil || tok.Token == "" {
		c.t.Fatalf("enrollment token for %s: %d %s", clientID, w.Code, w.Body.String())
	}
	w = c.post("/api/v1/agents/enroll", admin, map[string]any{"agent_id": agentID, "hostname": agentID, "enrollment_token": tok.Token})
	var enrolled agent.Agent
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &enrolled) != nil || enrolled.Token == "" {
		c.t.Fatalf("enroll %s: %d %s; want 201 with agent_token", agentID, w.Code, w.Body.String())
	}
	return enrolled.Token
}

// telemetry posts a report for agentID with the credential.
func (c *agentChain) telemetry(credential, agentID string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.post("/api/v1/agents/telemetry", credential, agent.TelemetryPayload{
		AgentID: agentID, Hostname: agentID, Version: "0.1.0", OS: "linux", Arch: "amd64",
		Metrics: map[string]float64{"num_cpu": 4}, CollectedAt: time.Now().UTC(),
	})
}

// heartbeat is the last heartbeat of the agent in PostgreSQL (empty when
// none), read through the maintenance pool.
func (c *agentChain) heartbeat(orgID, agentID string) string {
	c.t.Helper()
	var at string
	if err := c.f.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(last_heartbeat::text, '') FROM endpoint_agent WHERE organization_id = $1 AND agent_id = $2`,
		orgID, agentID).Scan(&at); err != nil {
		c.t.Fatalf("read endpoint_agent %s/%s: %v", orgID, agentID, err)
	}
	return at
}

// withOrganization re-encodes the claims of a JWT with another organization
// and keeps its signature.
func withOrganization(t *testing.T, token, orgID string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	claims["organization_id"] = orgID
	if raw, err = json.Marshal(claims); err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	return strings.Join(parts, ".")
}

// TestAgentCredentialThroughTheProductionChain covers WP-365 (AGT-03).
func TestAgentCredentialThroughTheProductionChain(t *testing.T) {
	c := newAgentChain(t)
	f := c.f
	clientB := f.ID()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', $3)`,
		clientB, f.OrgB, "scopetest-7e-cb"); err != nil {
		t.Fatalf("seed client of organization B: %v", err)
	}
	adminA, adminB := c.adminToken(f.OrgA), c.adminToken(f.OrgB)

	// Enrollment -> agent credential; organization B has an agent with the
	// same agent_id and one of its own.
	const shared, onlyB = "wp365-host", "wp365-b-only"
	credA := c.enroll(adminA, f.Client1, shared)
	credBShared := c.enroll(adminB, clientB, shared)
	credBOnly := c.enroll(adminB, clientB, onlyB)

	// Telemetry with the credential through the chain -> 202 and the
	// heartbeat in PostgreSQL.
	if w := c.telemetry(credA, shared); w.Code != http.StatusAccepted {
		t.Fatalf("telemetry with the own credential: %d %s; want 202", w.Code, w.Body.String())
	}
	beatA := c.heartbeat(f.OrgA, shared)
	if beatA == "" {
		t.Fatal("telemetry accepted, but endpoint_agent of organization A has no heartbeat")
	}
	var metrics int
	var ciClient string
	if err := f.Admin.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM metric_sample WHERE organization_id = $1),
		       COALESCE((SELECT c.client_id::text FROM endpoint_agent a JOIN ci c ON c.id = a.ci_id
		                  WHERE a.organization_id = $1 AND a.agent_id = $2), '')`, f.OrgA, shared).Scan(&metrics, &ciClient); err != nil {
		t.Fatalf("read telemetry rows: %v", err)
	}
	if metrics == 0 {
		t.Error("telemetry accepted, but no metric_sample row of organization A in PostgreSQL")
	}
	// The endpoint CI belongs to the client of the enrollment, the scope of
	// the credential.
	if ciClient != f.Client1 {
		t.Errorf("endpoint CI of the agent in client %q, want client 1 %s", ciClient, f.Client1)
	}

	// A credential of organization B never reaches the agent of
	// organization A.
	for _, tc := range []struct {
		name, credential, agentID string
		codes                     []int
	}{
		// The claims are signed: moving the credential to organization A
		// breaks the signature.
		{"credential of B rewritten to organization A", withOrganization(t, credBShared, f.OrgA), shared, []int{http.StatusUnauthorized}},
		// The credential binds its agent: B's agent cannot report for A's.
		{"credential of B's agent for A's agent_id", credBOnly, shared, []int{http.StatusForbidden}},
	} {
		if w := c.telemetry(tc.credential, tc.agentID); !containsCode(tc.codes, w.Code) {
			t.Errorf("%s: %d %s; want %v", tc.name, w.Code, strings.TrimSpace(w.Body.String()), tc.codes)
		}
	}
	// B's agent with the same agent_id reports into organization B only.
	if w := c.telemetry(credBShared, shared); w.Code != http.StatusAccepted {
		t.Errorf("telemetry of B's agent %s: %d %s; want 202", shared, w.Code, w.Body.String())
	}
	if c.heartbeat(f.OrgB, shared) == "" {
		t.Error("telemetry of B's agent did not reach endpoint_agent of organization B")
	}
	if got := c.heartbeat(f.OrgA, shared); got != beatA {
		t.Errorf("heartbeat of A's agent changed by credentials of organization B: %s -> %s", beatA, got)
	}

	// A revoked credential is refused by the auth stage.
	if err := c.issuer.Revocations().RevokeUser(context.Background(), agent.AgentSubject(shared), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if w := c.telemetry(credA, shared); w.Code != http.StatusUnauthorized {
		t.Errorf("telemetry with the revoked credential: %d %s; want 401", w.Code, w.Body.String())
	}
	if got := c.heartbeat(f.OrgA, shared); got != beatA {
		t.Errorf("heartbeat of A's agent changed by a revoked credential: %s -> %s", beatA, got)
	}
}

func containsCode(codes []int, code int) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}
