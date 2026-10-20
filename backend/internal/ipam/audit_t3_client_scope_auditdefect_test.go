//go:build auditdefect

package ipam_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). The VLAN references of WP-081 (F-088) are
// fixed by WP-371; its test TestAuditT3VLANReferenceMustBeVisible is in
// vlan_integration_test.go, the helpers auditT3Site and auditT3Str are in
// audit_t3_scope_harness_test.go.
//
// WP-082 (CH10, TEN-05: "Ein client-gescopter Nutzer kann keine org-weiten
// Zeilen anlegen oder ändern"; vrf is named among the tables under the org and
// client condition) P2 "Client-scoped principals can create, rename and
// delete org-wide VRFs, including the default 'global'": migration 000092
// creates only vrf_isolation with an org condition. Requirement: CreateVRF,
// UpdateVRF and DeleteVRF under a client scope are refused and the VRF table
// is unchanged; reading the org VRFs stays allowed (control).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3VRFWritesNeedOrgScope' ./internal/ipam/

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

func TestAuditT3VRFWritesNeedOrgScope(t *testing.T) {
	f := scopetest.Seed(t, "fd")
	repo := ipam.NewPGRepository(f.App)
	org := f.OrgCtx(f.OrgA)
	c1 := f.ClientCtx(f.Client1)
	orgVRF := &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-org-vrf"}
	if err := repo.CreateVRF(org, orgVRF); err != nil {
		t.Fatalf("control: org-wide CreateVRF: %v", err)
	}
	vrfs, _, err := repo.ListVRFs(c1, f.OrgA, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatalf("control: client-1 ListVRFs: %v", err)
	}
	var global *ipam.VRF
	for i := range vrfs {
		if vrfs[i].IsDefault {
			global = &vrfs[i]
		}
	}
	if global == nil {
		t.Fatalf("control: the client-1 principal does not see the default VRF (read must stay allowed)")
	}

	t.Run("CreateVRF", func(t *testing.T) {
		err := repo.CreateVRF(c1, &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-scoped-vrf"})
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE organization_id = $1 AND name = 'audit-t3-scoped-vrf'`, f.OrgA); err == nil || n != "0" {
			t.Errorf("client-1 CreateVRF: err=%v rows=%s; want an error and no org-wide row", err, n)
		}
	})
	t.Run("UpdateVRF default", func(t *testing.T) {
		name := "audit-t3-renamed-by-client1"
		_, err := repo.UpdateVRF(c1, f.OrgA, global.ID, ipam.UpdateVRFRequest{Name: &name})
		if got := auditT3Str(t, f, `SELECT name FROM vrf WHERE id = $1`, global.ID); err == nil || got != global.Name {
			t.Errorf("client-1 UpdateVRF of the default VRF: err=%v name=%q; want an error and %q", err, got, global.Name)
		}
	})
	t.Run("DeleteVRF", func(t *testing.T) {
		err := repo.DeleteVRF(c1, f.OrgA, orgVRF.ID)
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE id = $1`, orgVRF.ID); err == nil || n != "1" {
			t.Errorf("client-1 DeleteVRF of an org VRF: err=%v rows left=%s; want an error and 1", err, n)
		}
	})
}
