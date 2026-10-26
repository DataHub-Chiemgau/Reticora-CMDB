//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests through the
// production router (must fail until fixed). Harness: audit_t6_harness_test.go.
//
//   - TestAuditDefectRouteAuditStoresOldAndNewValues
//     WP-133 (AUD-07 "Wer, Wann, Was, Alt, Neu") P2 "Route-level audit entries
//     record only field names, no before/after values".
//   - TestAuditDefectDiscoveryJobInGenericJobModel
//     WP-129 (JOB-01) P2 "Discovery jobs are never registered in the generic job
//     model (adapter not wired)". Gap (high).
//
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
	"encoding/json"
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

func TestAuditDefectDiscoveryJobInGenericJobModel(t *testing.T) {
	e := t6NewEnv(t, "d8", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d8-admin"), identity.AllPermissions()}
	otherOrg := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-d8-b"), identity.AllPermissions()}
	var collector string
	if err := f.Admin.QueryRow(bg, `INSERT INTO collector (organization_id, name) VALUES ($1, 'audit-d8-collector') RETURNING id::text`, f.OrgA).Scan(&collector); err != nil {
		t.Fatal(err)
	}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/discovery/jobs", `{"collector_id":"`+collector+`","job_type":"sweep"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /discovery/jobs = %d %s", r.Code, t6Short(r.Body))
	}
	discoveryJob := t6ID(r.Body)

	r = e.Call(t, admin, http.MethodGet, "/api/v1/jobs?kind=discovery.sweep", ``)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /jobs = %d %s", r.Code, t6Short(r.Body))
	}
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			RefID  string `json:"ref_id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("GET /jobs body: %v", err)
	}
	var jobID string
	for _, j := range list.Data {
		if j.RefID == discoveryJob {
			jobID = j.ID
			if j.Kind != "discovery.sweep" || j.Status != "queued" {
				t.Errorf("generic job of the discovery job: kind %q status %q, want discovery.sweep/queued", j.Kind, j.Status)
			}
		}
	}
	if jobID == "" {
		t.Fatalf("GET /api/v1/jobs?kind=discovery.sweep does not list discovery job %s (JOB-01: one job model; %d rows in table job)",
			discoveryJob, e.Count(t, `SELECT count(*) FROM job WHERE ref_id::text = $1`, discoveryJob))
	}
	if r = e.Call(t, otherOrg, http.MethodGet, "/api/v1/jobs/"+jobID, ``); r.Code != http.StatusNotFound {
		t.Errorf("organization B GET /jobs/%s = %d, want 404", jobID, r.Code)
	}
}
