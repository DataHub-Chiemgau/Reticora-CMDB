//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the
// entitlement enforcement through the production router and chain (chain
// helper of t2_auth_chain_test.go: router of server.NewRouter, auth, tenant,
// entitlement middleware, rate limit).
//
//   - WP-073 (ENT-03, ENT-02), finding P2 "Only max_cis is enforced; the
//     stored quotas max_users, max_api_keys and max_collectors have no
//     effect"; gap (high) "Quota enforcement for users, API keys and
//     collectors" (max_api_keys and max_users here).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2EntitlementQuotasEnforced' ./internal/server/
package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

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
