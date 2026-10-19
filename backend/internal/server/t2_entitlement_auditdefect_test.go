//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the
// entitlement enforcement through the production router and chain (chain
// helper of t2_auth_chain_test.go: router of server.NewRouter, auth, tenant,
// entitlement middleware, rate limit).
//
//   - WP-072 (ENT-05, ENT-03), finding P2 "Phase-1 features rack_view,
//     api_access and notifications_email are not enforced server-side (REST
//     and GraphQL)"; gap (high) "rack_view, api_access and
//     notifications_email enforcement" (rack_view and api_access here; the
//     e-mail channel is not codified). gatedRoutes has no rack entry, the
//     GraphQL rackLayout resolver checks cmdb_core, and API-key
//     authentication never checks api_access.
//   - WP-073 (ENT-03, ENT-02), finding P2 "Only max_cis is enforced; the
//     stored quotas max_users, max_api_keys and max_collectors have no
//     effect"; gap (high) "Quota enforcement for users, API keys and
//     collectors" (max_api_keys and max_users here).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2Entitlement' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestAuditT2EntitlementRackViewGated: an organization whose rack_view
// entitlement is disabled must not read racks through REST or GraphQL
// (ENT-05: enforcement in REST and GraphQL); cmdb_core reads stay available.
func TestAuditT2EntitlementRackViewGated(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true}) // production default (RETICORA_ENTITLEMENT_ENFORCEMENT)
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureRackView, Enabled: false})
	token, _ := s.login(t, identity.PermRackRead, identity.PermCIRead)
	if s.entitlements.IsEnabled(context.Background(), t2Org, entitlement.FeatureRackView) {
		t.Fatal("setup: rack_view still enabled")
	}

	const rack = "7a2a0000-0000-4000-8000-000000000301"
	for _, path := range []string{"/api/v1/racks", "/api/v1/racks/" + rack, "/api/v1/racks/" + rack + "/layout", "/api/v1/racks/" + rack + "/mounts"} {
		w := s.do(t2Request{method: http.MethodGet, path: path, bearer: token})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("GET %s without rack_view: %d %s, want 403 feature-not-entitled", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}

	w := s.do(t2Request{method: http.MethodPost, path: "/bff/graphql", bearer: token, contentType: "application/json",
		body: `{"query":"{ rackLayout(rackId: \"` + rack + `\") { freeFront } }"}`})
	var gql struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &gql)
	forbidden := w.Code == http.StatusForbidden
	for _, e := range gql.Errors {
		if e.Extensions["code"] == "FORBIDDEN" {
			forbidden = true
		}
	}
	if !forbidden {
		t.Errorf("GraphQL rackLayout without rack_view: %d %s, want a FORBIDDEN error", w.Code, strings.TrimSpace(w.Body.String()))
	}

	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusOK {
		t.Errorf("control: GET /api/v1/cis (cmdb_core) without rack_view: %d, want 200", w.Code)
	}
}

// TestAuditT2EntitlementAPIAccessGatesAPIKeys: API keys are the api_access
// feature (ENT-05, phase-1 feature). With api_access disabled an X-API-Key
// request is refused, while the same organization's UI session keeps
// working.
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

	if _, err := s.entitlements.Grant(context.Background(), entitlement.Entitlement{OrganizationID: t2Org, FeatureKey: entitlement.FeatureAPIAccess,
		Plan: entitlement.PlanEssential, Enabled: false, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); w.Code != http.StatusForbidden {
		t.Errorf("X-API-Key request of an organization without api_access: %d %s, want 403 feature-not-entitled", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: admin}); w.Code != http.StatusOK {
		t.Errorf("control: session request without api_access: %d, want 200", w.Code)
	}
}

// TestAuditT2EntitlementQuotasEnforced: a quota stored in the limits of a
// row is enforced on the REST write path (ENT-03): with max_api_keys 1 the
// second key and with max_users 1 the second user are refused with 403
// entitlement-limit.
func TestAuditT2EntitlementQuotasEnforced(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	t2ProvisionRows(t, s,
		entitlement.Entitlement{FeatureKey: entitlement.FeatureAPIAccess, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxAPIKeys: 1}},
		entitlement.Entitlement{FeatureKey: entitlement.FeatureCMDBCore, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxUsers: 1}},
	)
	if q, err := s.entitlements.Quota(context.Background(), t2Org, entitlement.LimitMaxAPIKeys); err != nil || q == nil || *q != 1 {
		t.Fatalf("setup: max_api_keys quota %v (err %v), want 1", q, err)
	}
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead, identity.PermUserManage, identity.PermUserRead)

	t.Run("max_api_keys", func(t *testing.T) {
		var codes []int
		for i := range 2 {
			w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
				body: `{"name":"t2-quota-` + string(rune('a'+i)) + `","permissions":["ci:read"]}`})
			codes = append(codes, w.Code)
			if i == 1 && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "entitlement-limit")) {
				t.Errorf("second API key with max_api_keys 1: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
		if codes[0] != http.StatusCreated {
			t.Errorf("first API key within the quota: %d, want 201", codes[0])
		}
	})
	t.Run("max_users", func(t *testing.T) {
		var codes []int
		for i := range 2 {
			w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/users", bearer: admin, contentType: "application/json",
				body: `{"email":"t2-quota-` + string(rune('a'+i)) + `@example.com","display_name":"T2 Quota"}`})
			codes = append(codes, w.Code)
			if i == 1 && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "entitlement-limit")) {
				t.Errorf("second user with max_users 1: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
		if codes[0] != http.StatusCreated {
			t.Errorf("first user within the quota: %d, want 201", codes[0])
		}
	})
}
