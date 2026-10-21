//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests through the
// production router (must fail until fixed). Harness: audit_t6_harness_test.go.
//
//   - TestAuditDefectManualEdgeSurvivesInterfaceSync
//     WP-128 (REL-02, REL-05) P1 "Manual verified port-level edges are silently
//     deleted when discovery removes an interface (ON DELETE CASCADE)".
//     Gap (high): manual edges survive discovery's interface removal.
//   - TestAuditDefectDomainEventsEmitted
//     WP-136 (EVT-02) P1 "EVT-02 event types never emitted (relationship.created,
//     collector.enrolled, review.resolved; relationship.removed only on
//     discovery cleanup)". Gap (critical): producer test per EVT-02 type.
//   - TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation
//     WP-133 (WP table AUD-05, AUD-09; criterion "jede schreibende Route
//     erzeugt Audit"; requirement texts AUD-04 capture through an outbox,
//     AUD-07) P2 "Route audit is written after the response in a
//     separate transaction; failures are only logged (not gap-free)".
//     Gap (high): behaviour on audit write failure.
//   - TestAuditDefectRouteAuditStoresOldAndNewValues
//     WP-133 (AUD-07 "Wer, Wann, Was, Alt, Neu") P2 "Route-level audit entries
//     record only field names, no before/after values".
//   - TestAuditDefectDiscoveryJobInGenericJobModel
//     WP-129 (JOB-01) P2 "Discovery jobs are never registered in the generic job
//     model (adapter not wired)". Gap (high).
//   - TestAuditDefectNotificationAdministrationNeedsNotificationManage
//     WP-139 / WP-137 (RBA-01, RBA-02, NTF-01) P2 "Viewer and engineer can read all
//     notification deliveries and channel configs (webhook:read instead of
//     notification:manage)" and "Webhook channel URL (token) returned unredacted".
//     Gap (high): channel read authorization and redaction through the router.
//
// TestAuditDefectForeignPrivateCITypeRejected (WP-120, F-144) is fixed by
// WP-396 and lives in ci_type_ref_integration_test.go.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefect' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditDefectManualEdgeSurvivesInterfaceSync(t *testing.T) {
	e := t6NewEnv(t, "d4", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d4-admin"), identity.AllPermissions()}
	sw1, sw2 := f.CI(t, f.OrgA, f.Client1, "audit-d4-sw1"), f.CI(t, f.OrgA, f.Client1, "audit-d4-sw2")
	var port string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'Gi1/0/1') RETURNING id::text`,
		f.OrgA, sw1).Scan(&port); err != nil {
		t.Fatal(err)
	}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships",
		`{"source_ci_id":"`+sw1+`","target_ci_id":"`+sw2+`","rel_type":"connected_to","source":"manual","source_interface_id":"`+port+`"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /relationships = %d %s", r.Code, t6Short(r.Body))
	}
	edge := t6ID(r.Body)
	var before string
	if err := f.Admin.QueryRow(bg, `SELECT source || '/' || verification_state FROM ci_relationship WHERE id = $1`, edge).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Discovery reports sw1 without Gi1/0/1 often enough for REC-07 to remove it.
	repo := ipam.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA})
	for i := 0; i < ipam.MaxMissedObservations; i++ {
		if _, err := repo.SyncDiscoveredInterfaces(ctx, f.OrgA, sw1, []ipam.ObservedInterface{{Name: "Gi1/0/2"}}); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
	}

	var after string
	err := f.Admin.QueryRow(bg, `SELECT source || '/' || verification_state FROM ci_relationship WHERE id = $1`, edge).Scan(&after)
	if err != nil {
		t.Fatalf("manual edge %s (%s) is gone after %d discovery syncs without its interface: %v – REL-05: manual edges never disappear through discovery",
			edge, before, ipam.MaxMissedObservations, err)
	}
	if after != before {
		t.Errorf("manual edge changed by discovery: %s → %s", before, after)
	}
}

func TestAuditDefectDomainEventsEmitted(t *testing.T) {
	e := t6NewEnv(t, "d5", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d5-admin"), identity.AllPermissions()}
	outbox := func(typ, entity string) int {
		return e.Count(t, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2 AND ($3 = '' OR entity_id = $3)`, f.OrgA, typ, entity)
	}
	a, b := f.CI(t, f.OrgA, "", "audit-d5-a"), f.CI(t, f.OrgA, "", "audit-d5-b")

	var edge string
	t.Run("relationship.created on POST /relationships", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+a+`","target_ci_id":"`+b+`","rel_type":"depends_on"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /relationships = %d %s", r.Code, t6Short(r.Body))
		}
		edge = t6ID(r.Body)
		if n := outbox("relationship.created", edge); n != 1 {
			t.Errorf("relationship.created events for edge %s = %d, want 1 (EVT-02)", edge, n)
		}
	})
	t.Run("duplicate relationship (409) emits nothing more", func(t *testing.T) {
		before := outbox("relationship.created", "")
		r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+a+`","target_ci_id":"`+b+`","rel_type":"depends_on"}`)
		if r.Code != http.StatusConflict {
			t.Fatalf("duplicate POST /relationships = %d, want 409", r.Code)
		}
		if after := outbox("relationship.created", ""); after != before {
			t.Errorf("relationship.created after refused duplicate = %d, want %d", after, before)
		}
	})
	t.Run("relationship.removed on DELETE /relationships/{id}", func(t *testing.T) {
		if edge == "" {
			t.Fatal("no edge created")
		}
		r := e.Call(t, admin, http.MethodDelete, "/api/v1/relationships/"+edge, ``)
		if r.Code != http.StatusNoContent {
			t.Fatalf("DELETE /relationships = %d %s", r.Code, t6Short(r.Body))
		}
		if n := outbox("relationship.removed", edge); n != 1 {
			t.Errorf("relationship.removed events for edge %s = %d, want 1 (EVT-02)", edge, n)
		}
	})
	t.Run("collector.enrolled on POST /collectors/enroll", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodPost, "/api/v1/collectors/enrollment-codes", `{"label":"audit-d5"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /collectors/enrollment-codes = %d %s", r.Code, t6Short(r.Body))
		}
		var code struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(r.Body, &code); err != nil {
			t.Fatal(err)
		}
		r = e.Call(t, t6As{}, http.MethodPost, "/api/v1/collectors/enroll", `{"code":"`+code.Code+`","name":"audit-d5-collector"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /collectors/enroll = %d %s", r.Code, t6Short(r.Body))
		}
		collector := t6ID(r.Body)
		if n := outbox("collector.enrolled", collector); n != 1 {
			t.Errorf("collector.enrolled events for collector %s = %d, want 1 (EVT-02)", collector, n)
		}
		// A second use of the code is refused and emits nothing.
		before := outbox("collector.enrolled", "")
		if r = e.Call(t, t6As{}, http.MethodPost, "/api/v1/collectors/enroll", `{"code":"`+code.Code+`","name":"audit-d5-collector-2"}`); r.Code == http.StatusCreated {
			t.Fatalf("second enrollment with one code = 201")
		}
		if after := outbox("collector.enrolled", ""); after != before {
			t.Errorf("collector.enrolled after refused enrollment = %d, want %d", after, before)
		}
	})
	t.Run("review.resolved on POST /review-items/{id}/resolve", func(t *testing.T) {
		var item string
		if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, payload) VALUES ($1, 'unclassified_device', '{"ip":"192.0.2.55"}') RETURNING id::text`,
			f.OrgA).Scan(&item); err != nil {
			t.Fatal(err)
		}
		r := e.Call(t, admin, http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"dismiss"}`)
		if r.Code != http.StatusOK {
			t.Fatalf("resolve = %d %s", r.Code, t6Short(r.Body))
		}
		if n := outbox("review.resolved", item); n != 1 {
			t.Errorf("review.resolved events for item %s = %d, want 1 (EVT-02)", item, n)
		}
	})
}

// t6FailingAuditor simulates an unavailable audit store.
type t6FailingAuditor struct{}

func (t6FailingAuditor) RecordRoute(context.Context, *audit.Entry) error {
	return errors.New("audit-t6: audit store unavailable")
}

func TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation(t *testing.T) {
	e := t6NewEnv(t, "d6", t6FailingAuditor{})
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d6-admin"), identity.AllPermissions()}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":"audit-d6-vrf"}`)
	persisted := e.Count(t, `SELECT count(*) FROM vrf WHERE organization_id = $1 AND name = 'audit-d6-vrf'`, f.OrgA)
	audited := e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = 'vrfs'`, f.OrgA)
	t.Logf("POST /vrfs with failing audit store = %d; vrf rows %d; audit rows %d", r.Code, persisted, audited)
	if persisted > 0 && audited == 0 {
		t.Errorf("a committed VRF has no audit entry (response %d): AUD-07/WP-133 require gap-free capture – the mutation must roll back or the entry must be retried", r.Code)
	}
	if r.Code < 300 && persisted == 0 {
		t.Errorf("response %d reports success but nothing was stored", r.Code)
	}
}

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

func TestAuditDefectNotificationAdministrationNeedsNotificationManage(t *testing.T) {
	e := t6NewEnv(t, "d9", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-admin"), t6RolePerms(t, "org_admin")}
	const token = "AUDITD9TOKENXYZ"
	r := e.Call(t, admin, http.MethodPost, "/api/v1/notification-channels",
		`{"name":"audit-d9-chat","provider":"webhook","config":{"url":"https://hooks.example.invalid/services/T0/B0/`+token+`"},"enabled":true}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org_admin POST /notification-channels = %d %s", r.Code, t6Short(r.Body))
	}
	channel := t6ID(r.Body)

	for _, role := range []string{"viewer", "engineer"} {
		caller := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-"+role), t6RolePerms(t, role)}
		for _, path := range []string{"/api/v1/notification-deliveries", "/api/v1/notification-channels", "/api/v1/notification-channels/" + channel} {
			t.Run(role+" GET "+path, func(t *testing.T) {
				r := e.Call(t, caller, http.MethodGet, path, ``)
				if r.Code != http.StatusForbidden {
					t.Errorf("%s GET %s = %d, want 403 (RBA-02: notification:manage is org_admin only); token leaked: %v",
						role, path, r.Code, strings.Contains(string(r.Body), token))
				}
			})
		}
	}
	t.Run("org_admin GET channel does not reveal the webhook token", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodGet, "/api/v1/notification-channels/"+channel, ``)
		if r.Code != http.StatusOK {
			t.Fatalf("org_admin GET channel = %d", r.Code)
		}
		if strings.Contains(string(r.Body), token) {
			t.Errorf("channel response contains the incoming-webhook token: %s", t6Short(r.Body))
		}
	})
}
