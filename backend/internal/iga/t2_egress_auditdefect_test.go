//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the SSRF
// validation of the IGA connector update path.
//
//   - WP-049 (SEC-08), gap (high) "IGA SCIM egress regression test": the
//     proposed test requires "create and update connector with metadata,
//     RFC1918, loopback, ULA, localhost and http -> 400". CreateConnector
//     validates the base URL (registry.ValidateBaseURL), UpdateConnector
//     stores any base_url unchecked. The runtime egress client still blocks
//     the request (t2_egress_test.go), so the stored URL is not reachable;
//     the contract of the write path is violated nevertheless.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/iga/
package iga

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT2IGAUpdateValidatesBaseURL(t *testing.T) {
	mux := t2IGAMux(egress.Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/iga/connectors", strings.NewReader(`{"name":"t2","type":"scim","base_url":"https://scim.example.com/scim/v2"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: t2IGAOrg, UserID: "u"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create connector: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created connector %s: %v", w.Body.String(), err)
	}
	for _, url := range []string{
		"https://169.254.169.254/scim/v2", "https://10.0.0.5/scim/v2", "https://127.0.0.1/scim/v2",
		"https://[fd00::1]/scim/v2", "https://localhost/scim/v2", "http://scim.example.com/scim/v2",
	} {
		if got := t2IGARequest(mux, http.MethodPatch, "/api/v1/iga/connectors/"+created.ID, `{"base_url":"`+url+`"}`); got != http.StatusBadRequest {
			t.Errorf("update SCIM connector base_url to %s: %d, want 400 (as on create)", url, got)
		}
	}
}
