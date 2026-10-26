//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests through the
// production router (must fail until fixed). Harness: audit_t6_harness_test.go.
//
//   - TestAuditDefectRouteAuditStoresOldAndNewValues
//     WP-133 (AUD-07 "Wer, Wann, Was, Alt, Neu") P2 "Route-level audit entries
//     record only field names, no before/after values".
//
// TestAuditDefectDiscoveryJobInGenericJobModel (WP-129, F-146) is fixed by
// WP-502 and lives in discovery_job_tracking_integration_test.go;
// TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation (WP-133, F-149)
// is fixed by WP-504 and lives in route_audit_integration_test.go;
// TestAuditDefectForeignPrivateCITypeRejected (WP-120, F-144) is fixed by
// WP-396 and lives in ci_type_ref_integration_test.go;
// TestAuditDefectNotificationAdministrationNeedsNotificationManage (WP-139,
// WP-137, F-154) is fixed by WP-398 and lives in
// notification_authz_integration_test.go;
// TestAuditDefectDomainEventsEmitted (WP-136, F-043) is fixed by WP-400 and
// lives in domain_events_integration_test.go;
// TestAuditDefectManualEdgeSurvivesInterfaceSync (WP-128, F-042) is fixed by
// WP-424 and lives in manual_edge_integration_test.go.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefect' ./internal/server/
package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditDefectRouteAuditStoresOldAndNewValues(t *testing.T) {
	e := t6NewEnv(t, "d7", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d7-admin"), identity.AllPermissions()}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/vlans", `{"vlan_id":217,"name":"audit-d7-old"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /vlans = %d %s", r.Code, t6Short(r.Body))
	}
	vlan := t6ID(r.Body)
	r = e.Call(t, admin, http.MethodPatch, "/api/v1/vlans/"+vlan, `{"name":"audit-d7-new"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("PATCH /vlans = %d %s", r.Code, t6Short(r.Body))
	}
	var changes string
	// The PATCH is the latest write on the VLAN, so its entry is the newest.
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(changes::text, '') FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		ORDER BY seq DESC LIMIT 1`, f.OrgA, vlan).Scan(&changes); err != nil {
		t.Fatalf("audit entry of PATCH /vlans/%s: %v", vlan, err)
	}
	if !strings.Contains(changes, "audit-d7-old") || !strings.Contains(changes, "audit-d7-new") {
		t.Errorf("audit changes of the rename = %s; AUD-07 requires old (audit-d7-old) and new (audit-d7-new) values", changes)
	}
}
