package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// recordingAuditor keeps the route audit entries in memory.
type recordingAuditor struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *recordingAuditor) RecordRoute(_ context.Context, e *audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *e)
	return nil
}

func (a *recordingAuditor) take() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.entries
	a.entries = nil
	return out
}

// TestEveryWriteRouteIsAudited covers AUD-05/AUD-07 (WP-133) as an
// architecture test over the route table of the authorization catalogue
// (testdata/route_permissions.txt): every write route is registered through
// the authorizing router with the audit middleware, and it writes an audit
// entry unless it is listed in auditExemptions with a reason; the list names
// only existing write routes.
func TestEveryWriteRouteIsAudited(t *testing.T) {
	testRouter(t)
	table, err := os.ReadFile(routePermissionsFile)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(table)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !isWriteMethod(fields[0]) {
			continue
		}
		key := fields[0] + " " + fields[1]
		writes[key] = true
		audited, ok := auditedRoutes.Load(key)
		_, exempt := auditExemptions[key]
		switch {
		case !ok:
			t.Errorf("%s is not registered through the authorizing router: no audit middleware", key)
		case audited.(bool) == exempt:
			t.Errorf("%s: audited %v, exempt %v", key, audited, exempt)
		}
	}
	if len(writes) < 200 {
		t.Fatalf("only %d write routes in %s", len(writes), routePermissionsFile)
	}
	for key, reason := range auditExemptions {
		if !writes[key] {
			t.Errorf("exemption %q names no write route", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("exemption %q has no reason", key)
		}
	}
}

// TestWriteRequestsWriteAuditEntries runs write requests through the real
// router: a successful write stores who, what, which fields, the source,
// the request and the correlation id; a refused write and a read store
// nothing; an exempt route stores nothing; a request whose domain recorder
// already wrote an entry gets no second one.
func TestWriteRequestsWriteAuditEntries(t *testing.T) {
	repos := MemoryRepositories()
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	auditor := &recordingAuditor{}
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	mux, _, err := NewRouter(repos, Options{
		Version: "test", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Dispatcher:  webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}), Audit: &audit.Handler{}, RouteAuditor: auditor,
	})
	if err != nil {
		t.Fatal(err)
	}
	const org, user = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	handler := middleware.RequestID(mux)
	call := func(method, path, body, correlation string, typ identity.PrincipalType) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if correlation != "" {
			req.Header.Set("X-Correlation-ID", correlation)
		}
		ctx := identity.WithPrincipal(req.Context(), identity.Principal{Subject: user, OrganizationID: org, Type: typ,
			Permissions: []identity.Permission{identity.PermVRFManage, identity.PermIPAMRead}})
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: user})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}

	w := call(http.MethodPost, "/api/v1/vrfs", `{"name":"customer-a","description":"x"}`, "op-42", identity.PrincipalTypeUser)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	entries := auditor.take()
	if len(entries) != 1 {
		t.Fatalf("entries after a create: %+v", entries)
	}
	e := entries[0]
	fields, _ := e.Changes["fields"].([]string)
	// The memory repository's ids are no UUIDs: they stay in the changes.
	vrfID, _ := e.Changes["resource_key"].(string)
	if e.OrganizationID != org || e.ActorID != user || e.ActorType != "user" || e.Action != "vrfs.create" ||
		e.ResourceType != "vrfs" || e.ResourceID != "" || vrfID == "" || !strings.Contains(w.Body.String(), vrfID) ||
		e.Source != audit.SourceUI ||
		e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-ID") || e.CorrelationID != "op-42" ||
		strings.Join(fields, ",") != "description,name" || e.Changes["status"] != http.StatusCreated {
		t.Errorf("entry of a create: %+v", e)
	}

	w = call(http.MethodPatch, "/api/v1/vrfs/"+vrfID, `{"name":"customer-b"}`, "", identity.PrincipalTypeAPIKey)
	if entries = auditor.take(); w.Code != http.StatusOK || len(entries) != 1 || entries[0].Action != "vrfs.update" ||
		entries[0].Changes["resource_key"] != vrfID || entries[0].Source != audit.SourceAPI || entries[0].ActorType != "api_key" ||
		entries[0].CorrelationID != entries[0].RequestID {
		t.Errorf("update: %d %+v", w.Code, entries)
	}
	for _, refused := range [][3]string{
		{http.MethodPost, "/api/v1/vrfs", `{"name":""}`},
		{http.MethodGet, "/api/v1/vrfs", ""},
		{http.MethodPost, "/api/v1/subnets", `{"cidr":"10.0.0.0/24"}`}, // no ipam:write
	} {
		if call(refused[0], refused[1], refused[2], "", identity.PrincipalTypeUser); len(auditor.take()) != 0 {
			t.Errorf("%s %s wrote an audit entry", refused[0], refused[1])
		}
	}
	if w = call(http.MethodDelete, "/api/v1/vrfs/"+vrfID, "", "", identity.PrincipalTypeUser); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if entries = auditor.take(); len(entries) != 1 || entries[0].Action != "vrfs.delete" || entries[0].Changes["resource_key"] != vrfID {
		t.Errorf("delete: %+v", entries)
	}

	// A domain recorder wrote the entry: no second one; an exempt route
	// writes none but carries the request context to the domain recorders.
	direct := chi.NewRouter()
	var seen audit.RequestInfo
	direct.With(middleware.AuditRoute(http.MethodPost, "/api/v1/cis", auditor, true, "")).
		Post("/api/v1/cis", func(w http.ResponseWriter, r *http.Request) {
			audit.MarkRecorded(r.Context())
			w.WriteHeader(http.StatusCreated)
		})
	direct.With(middleware.AuditRoute(http.MethodPost, "/api/v1/ingest/bulk", auditor, false, audit.SourceCollector)).
		Post("/api/v1/ingest/bulk", func(w http.ResponseWriter, r *http.Request) {
			seen, _ = audit.RequestInfoFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		})
	for _, path := range []string{"/api/v1/cis", "/api/v1/ingest/bulk"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
		middleware.RequestID(direct).ServeHTTP(httptest.NewRecorder(), req)
	}
	if entries = auditor.take(); len(entries) != 0 {
		t.Errorf("entries with a domain recorder or an exemption: %+v", entries)
	}
	if seen.Source != audit.SourceCollector || seen.RequestID == "" {
		t.Errorf("request context of an ingest: %+v", seen)
	}
}

// TestRouteAction pins the action names of the route audit.
func TestRouteAction(t *testing.T) {
	for _, tc := range []struct{ method, pattern, resource, action string }{
		{http.MethodPost, "/api/v1/assets", "assets", "assets.create"},
		{http.MethodPatch, "/api/v1/assets/{id}", "assets", "assets.update"},
		{http.MethodPut, "/api/v1/roles/{id}/permissions", "roles", "roles.permissions"},
		{http.MethodPost, "/api/v1/orders/{id}/approve", "orders", "orders.approve"},
		{http.MethodDelete, "/api/v1/teams/{teamId}/members/{userId}", "teams", "teams.members.delete"},
		{http.MethodDelete, "/api/v1/cis/{id}", "cis", "cis.delete"},
		{http.MethodPost, "/scim/v2/Users", "Users", "Users.create"},
	} {
		resource, action := middleware.RouteAction(tc.method, tc.pattern)
		if resource != tc.resource || action != tc.action {
			t.Errorf("%s %s: %s %s, want %s %s", tc.method, tc.pattern, resource, action, tc.resource, tc.action)
		}
	}
}
