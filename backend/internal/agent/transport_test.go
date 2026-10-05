package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func testIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

// TestEnrollmentIssuesAgentCredential covers WP-051 (AGT-03): enrollment
// returns a signed credential bound to the agent (subject agent:<id>), its
// organization and client, holding agent:ingest only.
func TestEnrollmentIssuesAgentCredential(t *testing.T) {
	repo := NewMemoryRepository()
	issuer := testIssuer(t)
	h := NewHandler(repo, nil, nil).WithAgentTokens(issuer)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "admin"})
	if err := repo.CreateEnrollmentToken(ctx, "org-1", &EnrollmentToken{ClientID: "client-1", ExpiresAt: time.Now().Add(time.Hour)}, hashEnrollmentToken("agt_secret")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/enroll", bytes.NewBufferString(`{"agent_id":"laptop-1","hostname":"laptop-1","enrollment_token":"agt_secret"}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var enrolled Agent
	if err := json.Unmarshal(w.Body.Bytes(), &enrolled); err != nil || enrolled.Token == "" {
		t.Fatalf("enrollment response without agent_token: %s", w.Body.String())
	}
	claims, err := issuer.Validate(enrolled.Token)
	if err != nil {
		t.Fatalf("agent token invalid: %v", err)
	}
	if claims.Subject != "agent:laptop-1" || claims.OrganizationID != "org-1" ||
		len(claims.Permissions) != 1 || claims.Permissions[0] != identity.PermAgentIngest ||
		claims.Scope == nil || claims.Scope.Clients.All || len(claims.Scope.Clients.IDs) != 1 || claims.Scope.Clients.IDs[0] != "client-1" {
		t.Errorf("agent token claims %+v", claims)
	}
	if !claims.ExpiresAt.After(time.Now().Add(300 * 24 * time.Hour)) {
		t.Errorf("agent token expires %s", claims.ExpiresAt)
	}
}

// TestTelemetryRequiresTheMatchingAgentCredential covers WP-051 (AGT-03): an
// agent credential reports only for its own agent, in the backend contract
// that the relay message carries.
func TestTelemetryRequiresTheMatchingAgentCredential(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	if err := repo.CreateEnrollmentToken(ctx, "org-1", &EnrollmentToken{ClientID: "client-1", ExpiresAt: time.Now().Add(time.Hour)}, hashEnrollmentToken("t1")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Enroll(ctx, &Agent{OrganizationID: "org-1", AgentID: "laptop-1", Policy: DefaultPolicy()}, hashEnrollmentToken("t1"), ""); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, nil, nil).RegisterRoutes(mux)

	// The relay message an agent sends decodes into the backend contract.
	line, err := json.Marshal(RelayMessage{Token: "tok", Telemetry: TelemetryPayload{AgentID: "laptop-1", Hostname: "laptop-1", Metrics: map[string]float64{"num_cpu": 8}}})
	if err != nil {
		t.Fatal(err)
	}
	var relayed struct {
		Token     string          `json:"token"`
		Telemetry json.RawMessage `json:"telemetry"`
	}
	if err = json.Unmarshal(line, &relayed); err != nil || relayed.Token != "tok" {
		t.Fatalf("relay message: %v", err)
	}

	post := func(subject string, body []byte) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body))
		rctx := identity.WithPrincipal(ctx, identity.Principal{Subject: subject, OrganizationID: "org-1", Permissions: []identity.Permission{identity.PermAgentIngest}})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r.WithContext(rctx))
		return w.Code
	}
	if code := post("agent:laptop-1", relayed.Telemetry); code >= 300 {
		t.Errorf("own telemetry: status %d", code)
	}
	if code := post("agent:laptop-2", relayed.Telemetry); code != http.StatusForbidden {
		t.Errorf("telemetry for another agent: status %d, want 403", code)
	}
}
