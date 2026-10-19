// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// entitlement gates through the production router and chain (chain helper
// of t2_auth_chain_test.go) instead of stub handlers, with enforcement on as
// in production (RETICORA_ENTITLEMENT_ENFORCEMENT defaults to true).
//
//   - WP-072 (ENT-05, ENT-03, ENT-07, CH21), gap (high) "Gates through the
//     production router instead of stub handlers": an organization without
//     entitlement rows (no DefaultPlan fallback) gets 403
//     feature-not-entitled for export, bulk ingest, the ingest alias and
//     GraphQL topology, while cmdb_core reads stay 200. With an expired
//     discovery license only ingest is refused (license-expired); reading
//     discovery jobs and exporting stay available.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2EntitlementGates' ./internal/server/
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
