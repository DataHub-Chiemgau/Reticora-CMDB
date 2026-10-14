package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestIngestToWebhook is the TST-02 scenario "Ingest bis Webhook" (WP-208)
// against the real services of the PostgreSQL profile: a device record
// ingested through the complete router creates a CI; its ci.created event
// leaves the outbox and the webhook dispatcher delivers it, signed, to the
// subscriber registered through the API.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestToWebhook(t *testing.T) {
	f := scopetest.Seed(t, "ca")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "tst02-admin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, user); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var received []map[string]any
	subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil && r.Header.Get("X-Webhook-Signature") != "" {
			payload["_event"] = r.Header.Get("X-Webhook-Event")
			mu.Lock()
			received = append(received, payload)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer subscriber.Close()

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
	dispatcher := webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
		Deliveries: repos.WebhookDeliveries, PollInterval: -1})
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "tst02", Entitlements: entitlements, Dispatcher: dispatcher,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.TenantMiddleware(mux)
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		principal := identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: identity.AllPermissions(), Credential: "tst02"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, w.Body.Bytes()
	}

	if code, body := call(http.MethodPost, "/api/v1/webhooks", map[string]any{
		"name": "tst02", "url": subscriber.URL + "/hook", "secret": "tst02-secret-0123456789", "events": []string{"ci.created"}}); code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", code, body)
	}
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "tst02-device", IP: "192.0.2.210", Serial: "TST02-WEBHOOK"},
		time.Now().Add(-time.Minute))
	if code, body := call(http.MethodPost, "/api/v1/ingest/bulk", plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}}); code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", code, body)
	}
	var ciID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'TST02-WEBHOOK'`, f.OrgA).Scan(&ciID); err != nil {
		t.Fatalf("ingested CI: %v", err)
	}

	// The outbox publisher of the events worker hands the event to the
	// dispatcher (EVT-01); NATS is not needed for the webhook path.
	publisher := events.NewPublisher(f.Admin, nil, dispatcher)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := publisher.PublishPending(bg); err != nil {
			t.Fatalf("publish: %v", err)
		}
		dispatcher.ProcessDue(bg)
		mu.Lock()
		got := false
		for _, p := range received {
			if p["id"] == ciID && p["_event"] == "ci.created" {
				got = true
			}
		}
		mu.Unlock()
		if got {
			return
		}
		if time.Now().After(deadline) {
			raw, _ := json.Marshal(received)
			t.Fatalf("no signed ci.created delivery for %s; received %s", ciID, raw)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestTST02ScenarioCoverage keeps the TST-02 scenarios against real services
// (WP-208) tied to their integration tests: each scenario names the test
// that proves it, and the test must exist.
func TestTST02ScenarioCoverage(t *testing.T) {
	scenarios := []struct{ scenario, file, test string }{
		{"RLS incl. client/site/team scope and WITH CHECK", "tenant/rls/matrix_integration_test.go", "TestRLSMatrix"},
		{"channel matrix (REST, GraphQL, search, export, …)", "tenant/rls/channel_matrix_integration_test.go", "TestChannelMatrix"},
		{"ingest to webhook", "server/e2e_scenarios_integration_test.go", "TestIngestToWebhook"},
		{"idempotency", "middleware/idempotency_integration_test.go", "TestIdempotencyIsAtomicAndPrincipalBound"},
		{"mTLS of the collector channel", "discovery/natsedge_integration_test.go", "TestCollectorNATSEndpointAuthorization"},
		{"certificate renewal", "discovery/ca_test.go", "TestNeedsRenewal"},
		{"impact with several failures", "topology/multi_integration_test.go", "TestMultiImpactAgainstPostgres"},
		{"entitlement with unlicensed_ci", "entitlement/unlicensed_integration_test.go", "TestUnlicensedCIs"},
		{"entitlement expiry", "entitlement/enforce_integration_test.go", "TestEntitlementEnforcement"},
		{"offline detector", "lifecycle/offline_integration_test.go", "TestOfflineDetector"},
		{"jobs/scheduler single runner", "jobs/scheduler_integration_test.go", "TestSchedulerReplicasAgainstPostgres"},
		{"notifications (SMTP stub)", "notify/channel_test.go", "TestEmailOverSMTP"},
		{"runtime DDL", "citype/ddl_job_integration_test.go", "TestAttributeIndexJob"},
	}
	for _, s := range scenarios {
		src, err := os.ReadFile(filepath.Join("..", s.file))
		if err != nil {
			t.Errorf("%s: %v", s.scenario, err)
			continue
		}
		if !strings.Contains(string(src), "func "+s.test+"(") {
			t.Errorf("%s: %s no longer defines %s", s.scenario, s.file, s.test)
		}
	}
}
