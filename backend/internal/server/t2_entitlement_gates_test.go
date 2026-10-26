// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// entitlement gates through the production router and chain (chain helper
// of t2_auth_chain_test.go) instead of stub handlers, with enforcement on as
// in production (RETICORA_ENTITLEMENT_ENFORCEMENT defaults to true).
//
//   - WP-072 (ENT-05, ENT-03, ENT-07, CH21), gap (high) "Gates through the
//     production router instead of stub handlers": an organization without
//     entitlement rows (no DefaultPlan fallback) gets 403
//     feature-not-entitled for export, bulk ingest, the ingest alias,
//     GraphQL topology, racks and API keys (WP-471), while cmdb_core reads
//     stay 200. With an expired discovery license only ingest is refused
//     (license-expired); reading discovery jobs and exporting stay
//     available.
//   - WP-471 (ENT-05, API-04), finding F-070: racks, mounts and
//     rack-mounts need rack_view in REST and GraphQL (rackLayout); managing
//     API keys and every X-API-Key request need api_access, a session
//     without api_access keeps working (moved from
//     t2_entitlement_auditdefect_test.go).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2EntitlementGates|TestAuditT2EntitlementRackViewGated|TestAuditT2EntitlementAPIAccessGatesAPIKeys' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	rackpkg "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
)

func t2GraphQLForbidden(body []byte) bool {
	var gql struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &gql) != nil {
		return false
	}
	for _, e := range gql.Errors {
		if e.Extensions["code"] == "FORBIDDEN" {
			return true
		}
	}
	return false
}

func TestT2EntitlementGatesWithoutRows(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	token, _ := s.login(t, identity.AllPermissions()...)

	for _, r := range []t2Request{
		{method: http.MethodGet, path: "/api/v1/export/cis?format=csv"},
		{method: http.MethodPost, path: "/api/v1/ingest/bulk", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodPost, path: "/api/v1/discovery/ingest", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodGet, path: "/api/v1/racks"},
		{method: http.MethodGet, path: "/api/v1/api-keys"},
	} {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s for an organization without entitlement rows: %d %s, want 403 feature-not-entitled", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	w := s.do(t2Request{method: http.MethodPost, path: "/bff/graphql", bearer: token, contentType: "application/json",
		body: `{"query":"{ topology { nodes { id } } }"}`})
	if !t2GraphQLForbidden(w.Body.Bytes()) {
		t.Errorf("GraphQL topology without the topology entitlement: %d %s, want a FORBIDDEN error", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/cis (cmdb_core, always active): %d, want 200", w.Code)
	}
}

func TestT2EntitlementGatesExpiredDiscovery(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	past := time.Now().UTC().Add(-time.Hour)
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureDiscovery, Enabled: true, ValidUntil: &past})
	token, _ := s.login(t, identity.AllPermissions()...)

	for _, r := range []t2Request{
		{method: http.MethodPost, path: "/api/v1/ingest/bulk", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodPost, path: "/api/v1/discovery/ingest", contentType: "application/json", body: `{"records":[]}`},
	} {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "license-expired") {
			t.Errorf("%s %s after the discovery license expired: %d %s, want 403 license-expired", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	for _, path := range []string{"/api/v1/discovery/jobs", "/api/v1/export/cis?format=csv"} {
		if w := s.do(t2Request{method: http.MethodGet, path: path, bearer: token}); w.Code != http.StatusOK {
			t.Errorf("GET %s after the discovery license expired: %d %s, want 200 (CH21: only ingest stops)", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}

// TestAuditT2EntitlementRackViewGated: an organization whose rack_view
// entitlement is disabled must not read or write racks and mounts through
// REST or GraphQL (ENT-05: enforcement in REST and GraphQL); cmdb_core reads
// stay available. Enabled again, the same requests pass.
func TestAuditT2EntitlementRackViewGated(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true}) // production default (RETICORA_ENTITLEMENT_ENFORCEMENT)
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureRackView, Enabled: false})
	token, _ := s.login(t, identity.PermRackRead, identity.PermRackWrite, identity.PermCIRead)
	if s.entitlements.IsEnabled(context.Background(), t2Org, entitlement.FeatureRackView) {
		t.Fatal("setup: rack_view still enabled")
	}

	rk := &rackpkg.Rack{OrganizationID: t2Org, RoomID: "7a2a0000-0000-4000-8000-000000000201", Name: "R1", HeightUnits: 42}
	if err := s.repos.Rack.CreateRack(context.Background(), rk); err != nil {
		t.Fatal(err)
	}
	rack := rk.ID
	gated := []t2Request{
		{method: http.MethodGet, path: "/api/v1/racks"},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack + "/layout"},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack + "/mounts"},
		{method: http.MethodGet, path: "/api/v1/mounts"},
		{method: http.MethodGet, path: "/api/v1/mounts/" + rack},
		{method: http.MethodGet, path: "/api/v1/rack-mounts/" + rack},
		{method: http.MethodPost, path: "/api/v1/racks", contentType: "application/json", body: `{"name":"R1","height_units":42}`},
		{method: http.MethodDelete, path: "/api/v1/rack-mounts/" + rack},
	}
	for _, r := range gated {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s without rack_view: %d %s, want 403 feature-not-entitled", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}

	rackLayout := t2Request{method: http.MethodPost, path: "/bff/graphql", bearer: token, contentType: "application/json",
		body: `{"query":"{ rackLayout(rackId: \"` + rack + `\") { freeFront } }"}`}
	if w := s.do(rackLayout); w.Code != http.StatusForbidden && !t2GraphQLForbidden(w.Body.Bytes()) {
		t.Errorf("GraphQL rackLayout without rack_view: %d %s, want a FORBIDDEN error", w.Code, strings.TrimSpace(w.Body.String()))
	}

	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusOK {
		t.Errorf("control: GET /api/v1/cis (cmdb_core) without rack_view: %d, want 200", w.Code)
	}

	// Enabled again: the gate opens (the grant invalidates the cache).
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureRackView, Enabled: true})
	for _, path := range []string{"/api/v1/racks", "/api/v1/racks/" + rack + "/layout", "/api/v1/mounts?rack_id=" + rack} {
		if w := s.do(t2Request{method: http.MethodGet, path: path, bearer: token}); w.Code != http.StatusOK {
			t.Errorf("GET %s with rack_view: %d %s, want 200", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if w := s.do(rackLayout); t2GraphQLForbidden(w.Body.Bytes()) || !strings.Contains(w.Body.String(), `"freeFront":[`) {
		t.Errorf("GraphQL rackLayout with rack_view: %d %s, want the layout", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// TestAuditT2EntitlementAPIAccessGatesAPIKeys: API keys are the api_access
// feature (ENT-05, phase-1 feature). With api_access disabled an X-API-Key
// request is refused on every path and managing keys is refused, while the
// same organization's UI session keeps working on the other paths. Enabled
// again, the key works.
func TestAuditT2EntitlementAPIAccessGatesAPIKeys(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true}) // production default (RETICORA_ENTITLEMENT_ENFORCEMENT)
	t2ProvisionRows(t, s)
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead)
	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
		body: `{"name":"t2-api-access","permissions":["ci:read"]}`})
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); w.Code != http.StatusOK {
		t.Fatalf("setup: key with api_access enabled: %d", w.Code)
	}

	grant := func(enabled bool) {
		t.Helper()
		if _, err := s.entitlements.Grant(context.Background(), entitlement.Entitlement{OrganizationID: t2Org, FeatureKey: entitlement.FeatureAPIAccess,
			Plan: entitlement.PlanEssential, Enabled: enabled, Source: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	grant(false)
	for _, r := range []t2Request{
		{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key},
		{method: http.MethodGet, path: "/api/v1/entitlements", apiKey: created.Key},
		{method: http.MethodGet, path: "/api/v1/api-keys", bearer: admin},
		{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
			body: `{"name":"t2-api-access-2","permissions":["ci:read"]}`},
	} {
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s (api key %t) of an organization without api_access: %d %s, want 403 feature-not-entitled",
				r.method, r.path, r.apiKey != "", w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: admin}); w.Code != http.StatusOK {
		t.Errorf("control: session request without api_access: %d, want 200", w.Code)
	}

	grant(true)
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); w.Code != http.StatusOK {
		t.Errorf("X-API-Key request after api_access was enabled again: %d %s, want 200", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/api-keys", bearer: admin}); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/api-keys after api_access was enabled again: %d, want 200", w.Code)
	}
}

// t2ProvisionRows gives the organization the essential plan and stores the
// given rows on top.
func t2ProvisionRows(t *testing.T, s *t2Stack, rows ...entitlement.Entitlement) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.entitlements.Provision(ctx, t2Org, entitlement.PlanEssential); err != nil {
		t.Fatalf("provision: %v", err)
	}
	for _, row := range rows {
		row.OrganizationID, row.Plan, row.Source = t2Org, entitlement.PlanEssential, "manual"
		if _, err := s.entitlements.Grant(ctx, row); err != nil {
			t.Fatalf("grant %s: %v", row.FeatureKey, err)
		}
	}
}
