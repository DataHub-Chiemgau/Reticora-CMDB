package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// recordingAuditor keeps the route audit entries and the open intents in
// memory.
type recordingAuditor struct {
	mu      sync.Mutex
	entries []audit.Entry
	open    map[string]bool
	intents int
	seq     int
}

func (a *recordingAuditor) RecordRoute(_ context.Context, e *audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *e)
	return nil
}

func (a *recordingAuditor) Intend(_ context.Context, in *audit.RouteIntent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	in.ID = fmt.Sprint(a.seq)
	if a.open == nil {
		a.open = map[string]bool{}
	}
	a.open[in.ID] = true
	a.intents++
	return nil
}

func (a *recordingAuditor) Complete(_ context.Context, in *audit.RouteIntent, e *audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open[in.ID] {
		delete(a.open, in.ID)
		a.entries = append(a.entries, *e)
	}
	return nil
}

func (a *recordingAuditor) Discard(_ context.Context, in *audit.RouteIntent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.open, in.ID)
	return nil
}

// take returns the entries written since the last call; open intents fail
// the test.
func (a *recordingAuditor) take(t *testing.T) []audit.Entry {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.open) != 0 {
		t.Errorf("%d intents left open", len(a.open))
		a.open = nil
	}
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

// snapshotRegisterGaps checks the route snapshot register (AUD-07, WP-505)
// against the audited write routes: every route without a domain recorder
// needs an entry (a table, or the reason it has no row values), and every
// entry names such a route with a valid spec.
func snapshotRegisterGaps(writes []string) []string {
	var gaps []string
	want := map[string]bool{}
	for _, key := range writes {
		if _, exempt := auditExemptions[key]; exempt || domainRecordedRoutes[key] != "" {
			continue
		}
		want[key] = true
		if _, ok := audit.RouteSnapshotFor(key); !ok {
			gaps = append(gaps, key+": no entry in the route snapshot register (audit/route_snapshot.go)")
		}
	}
	for _, key := range audit.RouteSnapshotRoutes() {
		spec, _ := audit.RouteSnapshotFor(key)
		if !want[key] {
			gaps = append(gaps, key+": register entry for no audited write route without a domain recorder")
		}
		if err := spec.Validate(); err != nil {
			gaps = append(gaps, fmt.Sprintf("%s: %v", key, err))
		}
	}
	sort.Strings(gaps)
	return gaps
}

// TestEveryWriteRouteHasASnapshotEntry is the architecture test of the
// route snapshot register (AUD-07, WP-505): every audited write route of
// the authorization catalogue without a domain recorder has an entry, and
// the register names no other route; a new write route without a recorder
// and without an entry fails it.
func TestEveryWriteRouteHasASnapshotEntry(t *testing.T) {
	table, err := os.ReadFile(routePermissionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, line := range strings.Split(strings.TrimSpace(string(table)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && isWriteMethod(fields[0]) {
			writes = append(writes, fields[0]+" "+fields[1])
		}
	}
	for _, gap := range snapshotRegisterGaps(writes) {
		t.Error(gap)
	}
	// A new route without a recorder and without an entry is a gap.
	const added = "POST /api/v1/vlans/{id}/a5-new-action"
	if gaps := snapshotRegisterGaps(append(writes, added)); len(gaps) != 1 || !strings.HasPrefix(gaps[0], added+": no entry") {
		t.Errorf("a new write route without an entry: gaps %q", gaps)
	}
	// So is an entry without a table and without a reason.
	if err := (&audit.RouteSnapshot{}).Validate(); err == nil {
		t.Error("an empty register entry is valid")
	}
}

// TestEveryAuditedRouteHasAScopeRule is the architecture test of the audit
// list scope (TEN-06, AUD-05, WP-660, F-389): the route audit writes the
// first route segment as resource_type, and the audit list shows a
// restricted principal only entries of a type with a scope rule
// (audit/scope.go). Every audited write route has one, so a new route's
// entries are filtered by the row they name instead of being hidden.
func TestEveryAuditedRouteHasAScopeRule(t *testing.T) {
	table, err := os.ReadFile(routePermissionsFile)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(table)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !isWriteMethod(fields[0]) {
			continue
		}
		key := fields[0] + " " + fields[1]
		if _, exempt := auditExemptions[key]; exempt {
			continue
		}
		resourceType, _ := middleware.RouteAction(fields[0], fields[1])
		if !audit.HasScopeRule(resourceType) {
			t.Errorf("%s writes resource_type %q, which has no scope rule in audit/scope.go", key, resourceType)
		}
		checked++
	}
	if checked < 200 {
		t.Fatalf("only %d audited write routes checked", checked)
	}
	if audit.HasScopeRule("zz-new-segment") {
		t.Error("an unknown resource type has a scope rule")
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
	entries := auditor.take(t)
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
	if entries = auditor.take(t); w.Code != http.StatusOK || len(entries) != 1 || entries[0].Action != "vrfs.update" ||
		entries[0].Changes["resource_key"] != vrfID || entries[0].Source != audit.SourceAPI || entries[0].ActorType != "api_key" ||
		entries[0].CorrelationID != entries[0].RequestID {
		t.Errorf("update: %d %+v", w.Code, entries)
	}
	for _, refused := range [][3]string{
		{http.MethodPost, "/api/v1/vrfs", `{"name":""}`},
		{http.MethodGet, "/api/v1/vrfs", ""},
		{http.MethodPost, "/api/v1/subnets", `{"cidr":"10.0.0.0/24"}`}, // no ipam:write
	} {
		if call(refused[0], refused[1], refused[2], "", identity.PrincipalTypeUser); len(auditor.take(t)) != 0 {
			t.Errorf("%s %s wrote an audit entry", refused[0], refused[1])
		}
	}
	if w = call(http.MethodDelete, "/api/v1/vrfs/"+vrfID, "", "", identity.PrincipalTypeUser); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if entries = auditor.take(t); len(entries) != 1 || entries[0].Action != "vrfs.delete" || entries[0].Changes["resource_key"] != vrfID {
		t.Errorf("delete: %+v", entries)
	}

	// A domain recorder wrote the entry: no second one, the intent of a
	// route without a listed recorder is discarded and a route with a
	// listed recorder stores no intent; an exempt route writes none but
	// carries the request context to the domain recorders.
	direct := chi.NewRouter()
	var seen audit.RequestInfo
	recorded := func(w http.ResponseWriter, r *http.Request) {
		audit.MarkRecorded(r.Context())
		w.WriteHeader(http.StatusCreated)
	}
	direct.With(middleware.AuditRoute(http.MethodPost, "/api/v1/vlans", auditor, middleware.AuditIntent, "")).Post("/api/v1/vlans", recorded)
	direct.With(middleware.AuditRoute(http.MethodPost, "/api/v1/cis", auditor, middleware.AuditDomain, "")).Post("/api/v1/cis", recorded)
	direct.With(middleware.AuditRoute(http.MethodPost, "/api/v1/ingest/bulk", auditor, middleware.AuditNone, audit.SourceCollector)).
		Post("/api/v1/ingest/bulk", func(w http.ResponseWriter, r *http.Request) {
			seen, _ = audit.RequestInfoFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		})
	direct.With(middleware.AuditRoute(http.MethodPatch, "/api/v1/cis/{id}", auditor, middleware.AuditDomain, "")).
		Patch("/api/v1/cis/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	send := func(method, path string) {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
		middleware.RequestID(direct).ServeHTTP(httptest.NewRecorder(), req)
	}
	auditor.intents = 0
	for _, path := range []string{"/api/v1/vlans", "/api/v1/cis", "/api/v1/ingest/bulk"} {
		send(http.MethodPost, path)
	}
	if entries = auditor.take(t); len(entries) != 0 || auditor.intents != 1 {
		t.Errorf("entries with a domain recorder or an exemption: %+v; %d intents, want 1 (POST /vlans)", entries, auditor.intents)
	}
	if seen.Source != audit.SourceCollector || seen.RequestID == "" {
		t.Errorf("request context of an ingest: %+v", seen)
	}
	// A listed recorder that wrote nothing for a successful request: the
	// route audit writes the entry after the response, without an intent.
	send(http.MethodPatch, "/api/v1/cis/c1")
	if entries = auditor.take(t); len(entries) != 1 || entries[0].Action != "cis.update" || auditor.intents != 1 {
		t.Errorf("unrecorded write of a route with a domain recorder: %+v; %d intents", entries, auditor.intents)
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

// countingRouteAuditor is the production route auditor; it counts the
// intents per route.
type countingRouteAuditor struct {
	*audit.PGRouteAuditor
	mu      sync.Mutex
	intents map[string]int
}

func (c *countingRouteAuditor) Intend(ctx context.Context, in *audit.RouteIntent) error {
	c.mu.Lock()
	c.intents[in.Route]++
	c.mu.Unlock()
	return c.PGRouteAuditor.Intend(ctx, in)
}

func (c *countingRouteAuditor) count(route string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.intents[route]
}

// auditRouteCreates are the creating write routes of
// TestEveryWriteRouteAuditedOnPostgreSQL in the order they run, with their
// bodies; the other write routes follow, the deletes last. Placeholders:
// <client> a client, <user> the caller, <uuid> a new id, <citype> the
// server type, <ci2> and <ci3> further CIs, <asset2> a further asset,
// <id:path> the id the create of path returned.
var auditRouteCreates = []struct{ route, body string }{
	{"POST /api/v1/clients", `{"name":"a5 client","slug":"a5-client"}`},
	{"POST /api/v1/sites", `{"client_id":"<client>","name":"a5-site"}`},
	{"POST /api/v1/buildings", `{"site_id":"<id:/api/v1/sites>","name":"a5-building"}`},
	{"POST /api/v1/rooms", `{"building_id":"<id:/api/v1/buildings>","name":"a5-room"}`},
	{"POST /api/v1/racks", `{"room_id":"<id:/api/v1/rooms>","name":"a5-rack","height_units":42}`},
	{"POST /api/v1/locations", `{"kind":"site","name":"a5-location","client_id":"<client>"}`},
	{"POST /api/v1/ci-types", `{"key":"a5_type","name":"a5 type"}`},
	{"POST /api/v1/attribute-definitions", `{"ci_type_id":"<id:/api/v1/ci-types>","key":"a5_attr","data_type":"string"}`},
	{"POST /api/v1/cis", `{"ci_type_id":"<citype>","name":"a5-ci","client_id":"<client>"}`},
	{"POST /api/v1/assets", `{"asset_tag":"a5-asset","name":"a5-asset","client_id":"<client>"}`},
	{"POST /api/v1/contacts", `{"name":"a5 contact","email":"a5-contact@example.com"}`},
	{"POST /api/v1/vrfs", `{"name":"a5-vrf"}`},
	{"POST /api/v1/vlans", `{"vlan_id":305,"name":"a5-vlan"}`},
	{"POST /api/v1/subnets", `{"cidr":"10.205.0.0/24"}`},
	{"POST /api/v1/ips", `{"address":"10.205.0.10"}`},
	{"POST /api/v1/ip-addresses", `{"address":"10.205.0.11"}`},
	{"POST /api/v1/users", `{"email":"a5-user@example.com","display_name":"a5 user"}`},
	{"POST /api/v1/teams", `{"name":"a5-team"}`},
	{"POST /api/v1/roles", `{"name":"a5-role","permissions":["ci:read"]}`},
	{"POST /api/v1/api-keys", `{"name":"a5-key","permissions":["ci:read"]}`},
	{"POST /api/v1/service-accounts", `{"name":"a5-sa"}`},
	{"POST /api/v1/webhooks", `{"name":"a5-hook","url":"https://hook.example.com/a5","secret":"a5-secret-a5-secret-a5","events":["ci.created"]}`},
	{"POST /api/v1/notification-channels", `{"name":"a5-channel","provider":"inapp","config":{}}`},
	{"POST /api/v1/collectors", `{"name":"a5-collector"}`},
	{"POST /api/v1/discovery-scopes", `{"collector_id":"<id:/api/v1/collectors>","kind":"cidr","target":"10.205.1.0/24"}`},
	{"POST /api/v1/discovery/jobs", `{"collector_id":"<id:/api/v1/collectors>","job_type":"sweep"}`},
	{"POST /api/v1/documents", `{"title":"a5-doc","file_name":"a5.txt"}`},
	{"POST /api/v1/slas", `{"name":"a5-sla","priority":"low","response_target_minutes":10,"resolution_target_minutes":60}`},
	{"POST /api/v1/tickets", `{"title":"a5-ticket","priority":"low"}`},
	{"POST /api/v1/maintenance-windows", `{"title":"a5-mw","starts_at":"2030-01-01T00:00:00Z","ends_at":"2030-01-01T02:00:00Z","ci_ids":["<id:/api/v1/cis>"]}`},
	{"POST /api/v1/monitoring/alerts", `{"name":"a5-alert","metric_name":"cpu","condition":"gt","threshold":90}`},
	{"POST /api/v1/saved-views", `{"name":"a5-view","entity_kind":"ci","filter_spec":{}}`},
	{"POST /api/v1/compliance/rules", `{"name":"a5-rule","severity":"low","category":"a5","expression":{"field":"name","op":"eq","value":"a5"}}`},
	{"POST /api/v1/consumables", `{"name":"a5-consumable"}`},
	{"POST /api/v1/desks", `{"name":"a5-desk","room_id":"<id:/api/v1/rooms>"}`},
	{"POST /api/v1/keys", `{"name":"a5-key"}`},
	{"POST /api/v1/orders", `{"title":"a5-order"}`},
	{"POST /api/v1/stocktakes", `{"title":"a5-stocktake"}`},
	{"POST /api/v1/trainings", `{"title":"a5-training"}`},
	{"POST /api/v1/forms", `{"name":"a5-form","schema":{"type":"object"}}`},
	{"POST /api/v1/workflows", `{"name":"a5-workflow","trigger":{"event":"ci.created"},"actions":[{"type":"notify"}]}`},
	{"POST /api/v1/relationship-types", `{"key":"a5_rel","display_name":"a5 rel","reverse_name":"a5 rel inverse"}`},
	{"POST /api/v1/relationships", `{"source_ci_id":"<id:/api/v1/cis>","target_ci_id":"<ci2>","rel_type":"connected_to"}`},
	{"POST /api/v1/mounts", `{"ci_id":"<ci3>","rack_id":"<id:/api/v1/racks>","position_u":1}`},
	{"POST /api/v1/rack-mounts", `{"ci_id":"<ci3>","rack_id":"<id:/api/v1/racks>","position_u":3}`},
	{"POST /api/v1/security/findings", `{"kind":"vulnerability","title":"a5-finding","ci_id":"<id:/api/v1/cis>"}`},
	{"POST /api/v1/invitations", `{"email":"a5-invite@example.com","role_id":"<id:/api/v1/roles>"}`},
	{"POST /api/v1/assignments", `{"assigned_to":"<user>","asset_id":"<asset2>"}`},
	{"POST /api/v1/compositions", `{"parent_asset_id":"<id:/api/v1/assets>","child_asset_id":"<asset2>"}`},
	{"POST /api/v1/inventory/items", `{"name":"a5-item"}`},
	{"POST /api/v1/credentials/", `{"name":"a5-credential","kind":"snmp_v2c","secret":{"community":"public"}}`},
	{"POST /api/v1/asset-locations", `{"asset_id":"<id:/api/v1/assets>","lat":47.8,"lon":12.6}`},
	{"POST /api/v1/lifecycle-definitions", `{"key":"a5_lc","name":"a5 lifecycle","applies_to":"asset","states":[{"key":"new","label":"New","is_initial":true},{"key":"used","label":"Used"}],"transitions":[{"from_state_key":"new","to_state_key":"used","key":"use"}]}`},
	{"POST /api/v1/reservations", `{"item_kind":"asset","asset_id":"<id:/api/v1/assets>","quantity":1}`},
	{"POST /api/v1/stock-movements", `{"consumable_id":"<id:/api/v1/consumables>","direction":"in","quantity":1}`},
	{"POST /api/v1/disposal-records", `{"method":"reuse","asset_id":"<asset2>"}`},
	{"POST /api/v1/export/jobs", `{"format":"csv"}`},
	{"POST /api/v1/exports", `{"format":"csv"}`},
	{"POST /api/v1/history/snapshots", `{"entity_type":"ci","entity_id":"<id:/api/v1/cis>"}`},
	{"POST /api/v1/agents/enrollment-tokens", `{"client_id":"<client>"}`},
	{"POST /api/v1/collectors/enrollment-codes", `{}`},
	{"POST /api/v1/iga/connectors", `{"name":"a5-connector","type":"scim","base_url":"https://idp.example.com/scim"}`},
	{"POST /api/v1/iga/access-requests", `{"subject_user_id":"<id:/api/v1/users>","entitlement":"a5-entitlement"}`},
	{"POST /api/v1/iga/access-reviews", `{"name":"a5-review"}`},
	{"POST /api/v1/iga/jml-policies", `{"name":"a5-jml"}`},
	{"POST /api/v1/role-assignments", `{"user_id":"<id:/api/v1/users>","custom_role_id":"<id:/api/v1/roles>"}`},
	{"POST /api/v1/roles/assign", `{"user_id":"<user2>","custom_role_id":"<id:/api/v1/roles>"}`},
	{"POST /scim/v2/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"a5-scim@example.com","active":true}`},
	{"POST /scim/v2/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"a5-group"}`},
}

// auditRouteRequests are the bodies and paths of further write routes;
// a route without an entry gets {} and its path with the created ids.
var auditRouteRequests = map[string]struct{ path, body string }{
	"PUT /api/v1/notifications/preferences":            {"", `{"time_zone":"Europe/Berlin","muted_categories":[]}`},
	"PUT /api/v1/roles/{id}/permissions":               {"", `{"permission_keys":["ci:read","ci:write"]}`},
	"PUT /api/v1/service-accounts/{id}/roles":          {"", `{"roles":[]}`},
	"PUT /api/v1/racks/{id}/mounts/{ci_id}":            {"", `{"position_u":10}`},
	"POST /api/v1/racks/{id}/mounts":                   {"", `{"ci_id":"<ci2>","position_u":20}`},
	"POST /api/v1/cis/{id}/contacts":                   {"", `{"contact_id":"<id:/api/v1/contacts>"}`},
	"POST /api/v1/cis/{id}/interfaces":                 {"", `{"name":"eth0"}`},
	"POST /api/v1/cis/{id}/type":                       {"", `{"ci_type_id":"<citype2>"}`},
	"POST /api/v1/cis/{id}/unmerge":                    {"/api/v1/cis/<ci4>/unmerge", `{}`},
	"PATCH /api/v1/relationship-types/{key}":           {"/api/v1/relationship-types/a5_rel", `{"display_name":"a5 rel renamed"}`},
	"DELETE /api/v1/relationship-types/{key}":          {"/api/v1/relationship-types/a5_rel", ""},
	"DELETE /api/v1/relationship-suppressions/{id}":    {"/api/v1/relationship-suppressions/<suppression>", ""},
	"POST /api/v1/workflows/{id}/runs":                 {"", `{"trigger":{"event":"ci.created"},"context":{}}`},
	"PATCH /api/v1/cis/{id}":                           {"", `{"name":"a5-ci-renamed"}`},
	"POST /api/v1/cis/bulk":                            {"", `{"ids":["<ci2>"],"status":"maintenance"}`},
	"POST /api/v1/cis/{id}/merge":                      {"", `{"victim_id":"<ci4>"}`},
	"POST /api/v1/cis/{id}/restore":                    {"/api/v1/cis/<ci5>/restore", `{}`},
	"POST /api/v1/cables":                              {"", `{"label":"a5-cable"}`},
	"POST /api/v1/desks/{id}/bookings":                 {"", `{"starts_at":"2030-01-02T08:00:00Z","ends_at":"2030-01-02T12:00:00Z"}`},
	"POST /api/v1/keys/{id}/issue":                     {"", `{"assigned_to":"<user>"}`},
	"POST /api/v1/trainings/{id}/assignments":          {"", `{"user_id":"<user>"}`},
	"POST /api/v1/assets/{id}/lifecycle-transitions":   {"", `{"to_state":"used"}`},
	"PUT /api/v1/reconciliation/source-policy":         {"", `{"name":"a5-policy","priorities":["manual","discovery"]}`},
	"PUT /api/v1/cis/{id}/fields/{name}/override":      {"/api/v1/cis/<id:/api/v1/cis>/fields/description/override", `{"value":"a5","reason":"a5 override"}`},
	"DELETE /api/v1/cis/{id}/fields/{name}/override":   {"/api/v1/cis/<id:/api/v1/cis>/fields/description/override", ""},
	"DELETE /api/v1/users/{id}":                        {"/api/v1/users/<user3>", ""},
	"POST /api/v1/teams/{id}/members":                  {"", `{"user_id":"<id:/api/v1/users>"}`},
	"POST /api/v1/tickets/{id}/comments":               {"", `{"content":"a5 comment"}`},
	"POST /api/v1/orders/{id}/items":                   {"", `{"description":"a5 item","quantity":1}`},
	"POST /api/v1/consumables/{id}/movements":          {"", `{"direction":"in","quantity":1}`},
	"POST /api/v1/documents/{id}/links":                {"", `{"entity_type":"ci","entity_id":"<id:/api/v1/cis>"}`},
	"POST /api/v1/stocktakes/{id}/scans":               {"", `{"scan_result":"found","asset_id":"<id:/api/v1/assets>"}`},
	"POST /api/v1/agents/{id}/site":                    {"", `{"site_id":"<id:/api/v1/sites>"}`},
	"POST /api/v1/assignments/{id}/transfer":           {"", `{"new_assignee":"<id:/api/v1/users>"}`},
	"PATCH /api/v1/org":                                {"", `{}`},
	"PUT /api/v1/documents/{id}/content":               {"", `a5 content`},
	"DELETE /api/v1/ci-fields/global/{name}":           {"/api/v1/ci-fields/global/a5_global", ""},
	"DELETE /api/v1/ci-types/{id}/fields/{name}":       {"/api/v1/ci-types/<id:/api/v1/ci-types>/fields/a5_field", ""},
	"DELETE /api/v1/cis/{id}/field-definitions/{name}": {"/api/v1/cis/<id:/api/v1/cis>/field-definitions/a5_ci_field", ""},
	"PUT /api/v1/ci-fields/global":                     {"", `{"name":"a5_global","data_type":"string"}`},
	"PUT /api/v1/ci-types/{id}/fields":                 {"", `{"name":"a5_field","data_type":"string"}`},
	"PUT /api/v1/cis/{id}/field-definitions":           {"", `{"name":"a5_ci_field","data_type":"string"}`},
}

// auditRouteSubCreates create rows under another resource; they run first
// among the remaining write routes, and their ids serve the routes of the
// named collection.
var auditRouteSubCreates = []struct{ route, collection string }{
	{"POST /api/v1/cis/{id}/interfaces", "/api/v1/network-interfaces"},
	{"POST /api/v1/cis/{id}/contacts", "/api/v1/ci-contacts"},
	{"POST /api/v1/cables", "/api/v1/cables"},
	{"POST /api/v1/desks/{id}/bookings", "/api/v1/desk-bookings"},
	{"POST /api/v1/trainings/{id}/assignments", "/api/v1/training-assignments"},
	{"POST /api/v1/keys/{id}/issue", ""},
	{"POST /api/v1/workflows/{id}/runs", "/api/v1/workflow-runs"},
	{"POST /api/v1/cis/{id}/merge", ""},
}

// TestEveryWriteRouteAuditedOnPostgreSQL runs every write route of the
// authorization catalogue (testdata/route_permissions.txt) through the full
// router on PostgreSQL with the production route auditor (AUD-05, AUD-07,
// WP-504): a successful request of a route without a domain recorder
// writes exactly one audit_log entry (the route entry); a route of
// domainRecordedRoutes stores no intent and its domain recorder writes the
// entry, the route audit none; a refused request writes no route entry; no
// intent outlives its request. A domain entry on an unlisted route fails
// the test, so the list follows the recorders.
func TestEveryWriteRouteAuditedOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "a5")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := PostgresRepositories(f.App, audit.NewPGRecorder())
	policy := egress.Options{AllowPrivate: true}
	emitter := events.NewPGEmitter(f.App)
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	auditor := &countingRouteAuditor{PGRouteAuditor: audit.NewPGRouteAuditor(f.App), intents: map[string]int{}}
	mux, _, err := NewRouter(repos, Options{
		Version: "audit-a5", Entitlements: entitlements,
		Dispatcher: webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
			Deliveries: repos.WebhookDeliveries, PollInterval: -1}),
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      policy, AuditPool: f.App, Audit: audit.NewHandler(f.App), RouteAuditor: auditor,
		Notifications: &notify.Center{
			Channels: notify.NewPGRepository(f.App, notify.NewSealer(encryptor)), Inbox: notify.NewPGInbox(f.App),
			Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App),
		},
		Events: emitter,
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	handler := middleware.RequestID(middleware.TenantMiddleware(mux))
	user := f.AppUser(t, f.OrgA, "audit-a5-admin")
	var citype string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&citype); err != nil {
		t.Fatal(err)
	}
	var citype2 string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'application' AND organization_id IS NULL`).Scan(&citype2); err != nil {
		t.Fatal(err)
	}
	fixed := map[string]string{"<client>": f.Client1, "<user>": user, "<citype>": citype,
		"<ci2>": f.CI(t, f.OrgA, f.Client1, "a5-ci2"), "<ci3>": f.CI(t, f.OrgA, f.Client1, "a5-ci3"),
		"<ci4>": f.CI(t, f.OrgA, f.Client1, "a5-ci4"), "<ci5>": f.CI(t, f.OrgA, f.Client1, "a5-ci5"),
		"<asset2>": f.Asset(t, f.OrgA, f.Client1, "a5-asset2"), "<citype2>": citype2,
		"<user2>": f.AppUser(t, f.OrgA, "audit-a5-user2"), "<user3>": f.AppUser(t, f.OrgA, "audit-a5-user3")}
	// A soft-deleted CI for POST /cis/{id}/restore.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, fixed["<ci5>"]); err != nil {
		t.Fatal(err)
	}
	// A suppression for DELETE /relationship-suppressions/{id} (F-273).
	var suppression string
	if err = f.Admin.QueryRow(bg, `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
		SELECT $1, $2, $3, id FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL RETURNING id::text`,
		f.OrgA, f.CI(t, f.OrgA, f.Client1, "a5-suppressed-src"), f.CI(t, f.OrgA, f.Client1, "a5-suppressed-dst")).Scan(&suppression); err != nil {
		t.Fatal(err)
	}
	fixed["<suppression>"] = suppression
	ids := map[string]string{}
	fill := func(s string) string {
		for k, v := range fixed {
			s = strings.ReplaceAll(s, k, v)
		}
		for strings.Contains(s, "<uuid>") {
			s = strings.Replace(s, "<uuid>", f.ID(), 1)
		}
		for {
			i := strings.Index(s, "<id:")
			if i < 0 {
				return s
			}
			j := strings.Index(s[i:], ">")
			id := ids[s[i+4:i+j]]
			if id == "" {
				id = f.ID()
			}
			s = s[:i] + id + s[i+j+1:]
		}
	}
	// pathOf fills the parameters of a pattern with the ids the creates
	// returned, else with a new id.
	pathOf := func(pattern string) string {
		segs := strings.Split(pattern, "/")
		for i, seg := range segs {
			if !strings.HasPrefix(seg, "{") {
				continue
			}
			collection := strings.Join(segs[:i], "/")
			switch seg {
			case "{ci_id}":
				collection = "/api/v1/cis"
			case "{userId}":
				collection = "/api/v1/users"
			}
			if id := ids[collection]; id != "" {
				segs[i] = id
			} else {
				segs[i] = f.ID()
			}
		}
		return strings.Join(segs, "/")
	}

	type outcome struct {
		status, routeEntries, domainEntries int
		// values: the route entry holds the row values (WP-505);
		// unavailable: the read after the request failed.
		values, unavailable bool
	}
	results := map[string]outcome{}
	call := func(key, path, body string) {
		method, _, _ := strings.Cut(key, " ")
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: identity.AllPermissions(), Credential: "audit-a5"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		var o outcome
		o.status = w.Code
		if qErr := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE changes ? 'route'), count(*) FILTER (WHERE NOT changes ? 'route'),
				COALESCE(bool_or(changes ? 'values') FILTER (WHERE changes ? 'route'), false),
				COALESCE(bool_or(changes ? 'values_unavailable'), false)
			FROM audit_log WHERE organization_id = $1 AND request_id = $2`, f.OrgA, w.Header().Get("X-Request-ID")).
			Scan(&o.routeEntries, &o.domainEntries, &o.values, &o.unavailable); qErr != nil {
			t.Fatalf("%s: audit entries: %v", key, qErr)
		}
		var open int
		if qErr := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_route_intent WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB).Scan(&open); qErr != nil {
			t.Fatal(qErr)
		}
		if open != 0 {
			t.Errorf("%s %s = %d left %d audit intents", key, path, w.Code, open)
			if _, dErr := f.Admin.Exec(bg, `DELETE FROM audit_route_intent WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB); dErr != nil {
				t.Fatal(dErr)
			}
		}
		collection := strings.TrimSuffix(strings.TrimPrefix(key, "POST "), "/")
		for _, c := range auditRouteSubCreates {
			if c.route == key {
				collection = c.collection
			}
		}
		if method == http.MethodPost && w.Code < 300 && collection != "" && !strings.Contains(collection, "{") {
			var obj struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &obj)
			if obj.ID != "" {
				ids[collection] = obj.ID
			} else if obj.Key != "" {
				ids[collection] = obj.Key
			}
		}
		if prev, ok := results[key]; !ok || prev.status >= 300 {
			results[key] = o
		}
		if testing.Verbose() {
			t.Logf("%-60s %d route=%d domain=%d %s", key, o.status, o.routeEntries, o.domainEntries, strings.TrimSpace(w.Body.String()[:min(w.Body.Len(), 160)]))
		}
	}

	table, err := os.ReadFile(routePermissionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, line := range strings.Split(strings.TrimSpace(string(table)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !isWriteMethod(fields[0]) {
			continue
		}
		key := fields[0] + " " + fields[1]
		if _, exempt := auditExemptions[key]; !exempt {
			writes = append(writes, key)
		}
	}
	done := map[string]bool{}
	for _, c := range auditRouteCreates {
		call(c.route, strings.TrimPrefix(c.route, "POST "), fill(c.body))
		done[c.route] = true
	}
	request := func(key string) {
		_, pattern, _ := strings.Cut(key, " ")
		path, body := pathOf(pattern), "{}"
		if r, ok := auditRouteRequests[key]; ok {
			if r.path != "" {
				path = fill(r.path)
			}
			body = fill(r.body)
		}
		if strings.HasPrefix(key, "DELETE ") {
			body = ""
		}
		call(key, path, body)
		done[key] = true
	}
	for _, c := range auditRouteSubCreates {
		request(c.route)
	}
	var deletes []string
	for _, key := range writes {
		switch {
		case done[key]:
		case strings.HasPrefix(key, "DELETE "):
			deletes = append(deletes, key)
		default:
			request(key)
		}
	}
	// Dependent rows first: the deeper paths, then against the create order.
	order := map[string]int{}
	for i, c := range auditRouteCreates {
		order[strings.TrimSuffix(strings.TrimPrefix(c.route, "POST "), "/")] = i
	}
	sort.SliceStable(deletes, func(i, j int) bool {
		di, dj := strings.Count(deletes[i], "/"), strings.Count(deletes[j], "/")
		if di != dj {
			return di > dj
		}
		ci := order[strings.Join(strings.Split(strings.TrimPrefix(deletes[i], "DELETE "), "/")[:4], "/")]
		cj := order[strings.Join(strings.Split(strings.TrimPrefix(deletes[j], "DELETE "), "/")[:4], "/")]
		return ci > cj
	})
	for _, key := range deletes {
		request(key)
	}

	succeeded := 0
	for _, key := range writes {
		o, ok := results[key]
		recorder := domainRecordedRoutes[key]
		switch {
		case !ok:
			t.Errorf("%s was not called", key)
		case o.status < 200 || o.status >= 300:
			if o.routeEntries != 0 {
				t.Errorf("%s refused with %d wrote %d route entries", key, o.status, o.routeEntries)
			}
		case recorder != "":
			succeeded++
			if o.routeEntries != 0 || o.domainEntries == 0 {
				t.Errorf("%s (domain recorder %s) = %d: %d route and %d domain entries, want 0 and at least 1", key, recorder, o.status,
					o.routeEntries, o.domainEntries)
			}
		default:
			succeeded++
			if o.routeEntries+o.domainEntries != 1 || o.routeEntries != 1 {
				t.Errorf("%s = %d: %d route and %d domain entries, want exactly one route entry (a domain recorder belongs in domainRecordedRoutes)",
					key, o.status, o.routeEntries, o.domainEntries)
			}
			// The register finds the row of every route with a table: the
			// entry holds its values (AUD-07, WP-505).
			spec, _ := audit.RouteSnapshotFor(key)
			if o.unavailable || (spec.Table != "") != o.values {
				t.Errorf("%s = %d: route entry with values %v (unavailable %v), register table %q", key, o.status, o.values,
					o.unavailable, spec.Table)
			}
		}
		if recorder != "" && auditor.count(key) != 0 {
			t.Errorf("%s has a domain recorder but stored %d audit intents", key, auditor.count(key))
		}
	}
	for key := range domainRecordedRoutes {
		if o := results[key]; o.status < 200 || o.status >= 300 {
			t.Errorf("domainRecordedRoutes: %s did not succeed (%d); the test does not show its recorder", key, o.status)
		}
	}
	// The rest needs what this router lacks (blob storage, invitations,
	// enrolled agents, review items, IGA drift and tasks) or a state the
	// run does not build; a drop below the floor means bodies or fixtures
	// broke.
	const minSucceeded = 220
	t.Logf("%d of %d audited write routes succeeded", succeeded, len(writes))
	if succeeded < minSucceeded {
		t.Errorf("only %d write routes succeeded, want at least %d: the test no longer shows their audit", succeeded, minSucceeded)
	}
	// Secrets and personal values of the bodies never reach a route entry
	// (CH16, WP-505): the webhook secret, the credential, the e-mail
	// addresses of the contact, the user and the invitation.
	for _, plain := range []string{"a5-secret-a5-secret-a5", `"community"`, "a5-contact@example.com", "a5-user@example.com",
		"a5-invite@example.com", "a5-scim@example.com"} {
		if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND changes ? 'route'
			AND strpos(changes::text, $2) > 0`, f.OrgA, plain); n != 0 {
			t.Errorf("%d route entries hold %s", n, plain)
		}
	}
}

func countRows(t *testing.T, f *scopetest.Fixture, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
