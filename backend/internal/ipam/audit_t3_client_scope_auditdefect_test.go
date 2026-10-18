//go:build auditdefect

package ipam_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-081 (NET-08, TEN-05, TEN-06) P2 "Subnets and interfaces can reference
// VLANs of another client (FK bypasses RLS)": the composite FK checks run
// without RLS and ipam Create/Update Subnet/Interface do not verify that the
// VLAN is visible to the principal. Requirement: a client-scoped principal
// references only VLANs it can see; a client-2 VLAN is refused for a client-1
// principal and nothing is stored. Same-client VLANs work (control).
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
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3VLANReferenceMustBeVisible|TestAuditT3VRFWritesNeedOrgScope' ./internal/ipam/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

func auditT3Site(t *testing.T, f *scopetest.Fixture, client, name string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		f.OrgA, client, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func auditT3Str(t *testing.T, f *scopetest.Fixture, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func TestAuditT3VLANReferenceMustBeVisible(t *testing.T) {
	f := scopetest.Seed(t, "fb")
	repo := ipam.NewPGRepository(f.App)
	org := f.OrgCtx(f.OrgA)
	c1 := f.ClientCtx(f.Client1)
	siteA := auditT3Site(t, f, f.Client1, "audit-t3-site-a")
	siteB := auditT3Site(t, f, f.Client2, "audit-t3-site-b")
	own := &ipam.VLAN{OrganizationID: f.OrgA, SiteID: siteA, VLANID: 10, Name: "audit-t3-client1-vlan"}
	foreign := &ipam.VLAN{OrganizationID: f.OrgA, SiteID: siteB, VLANID: 20, Name: "audit-t3-client2-vlan"}
	for _, v := range []*ipam.VLAN{own, foreign} {
		if err := repo.CreateVLAN(org, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.GetVLAN(c1, f.OrgA, foreign.ID); err == nil {
		t.Fatalf("precondition: the client-2 VLAN is visible to the client-1 principal")
	}
	ciID := f.CI(t, f.OrgA, f.Client1, "audit-t3-if-ci")

	// Controls: the principal's own VLAN is accepted.
	ownSubnet := &ipam.Subnet{OrganizationID: f.OrgA, SiteID: siteA, ClientID: f.Client1, CIDR: "10.194.0.0/24", VLANID: own.ID}
	if err := repo.CreateSubnet(c1, ownSubnet); err != nil {
		t.Fatalf("control: subnet with the client-1 VLAN: %v", err)
	}
	ownIf := &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: "eth0", IfType: "ethernet", VLANID: own.ID, AdminStatus: "up", OperStatus: "up"}
	if err := repo.CreateInterface(c1, ownIf); err != nil {
		t.Fatalf("control: interface with the client-1 VLAN: %v", err)
	}

	t.Run("CreateSubnet", func(t *testing.T) {
		sn := &ipam.Subnet{OrganizationID: f.OrgA, SiteID: siteA, ClientID: f.Client1, CIDR: "10.195.0.0/24", VLANID: foreign.ID}
		err := repo.CreateSubnet(c1, sn)
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM subnet WHERE organization_id = $1 AND vlan_id = $2`, f.OrgA, foreign.ID); err == nil || n != "0" {
			t.Errorf("client-1 CreateSubnet with the client-2 VLAN: err=%v, subnets stored with it=%s; want an error and 0", err, n)
		}
	})
	t.Run("UpdateSubnet", func(t *testing.T) {
		vlan := foreign.ID
		_, err := repo.UpdateSubnet(c1, f.OrgA, ownSubnet.ID, ipam.UpdateSubnetRequest{VLANID: &vlan})
		if got := auditT3Str(t, f, `SELECT coalesce(vlan_id::text, '') FROM subnet WHERE id = $1`, ownSubnet.ID); err == nil || got != own.ID {
			t.Errorf("client-1 UpdateSubnet vlan_id -> client-2 VLAN: err=%v, stored vlan_id=%s; want an error and %s", err, got, own.ID)
		}
	})
	t.Run("CreateInterface", func(t *testing.T) {
		ni := &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: "eth1", IfType: "ethernet", VLANID: foreign.ID, AdminStatus: "up", OperStatus: "up"}
		err := repo.CreateInterface(c1, ni)
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM network_interface WHERE ci_id = $1 AND vlan_id = $2`, ciID, foreign.ID); err == nil || n != "0" {
			t.Errorf("client-1 CreateInterface with the client-2 VLAN: err=%v, interfaces stored with it=%s; want an error and 0", err, n)
		}
	})
	t.Run("UpdateInterface", func(t *testing.T) {
		vlan := foreign.ID
		_, err := repo.UpdateInterface(c1, f.OrgA, ownIf.ID, ipam.UpdateInterfaceRequest{VLANID: &vlan})
		if got := auditT3Str(t, f, `SELECT coalesce(vlan_id::text, '') FROM network_interface WHERE id = $1`, ownIf.ID); err == nil || got != own.ID {
			t.Errorf("client-1 UpdateInterface vlan_id -> client-2 VLAN: err=%v, stored vlan_id=%s; want an error and %s", err, got, own.ID)
		}
	})
}

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
