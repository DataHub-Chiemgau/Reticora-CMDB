package ipam_test

import (
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// TestVRFModel covers WP-082 (TEN-10, CH10, NET-02, NET-03) against
// PostgreSQL:
//   - every organization gets exactly one default VRF "global" on creation;
//     it cannot be deleted; VRF names are unique per organization;
//   - subnets are unique per (organization, VRF, CIDR) and take the default
//     VRF when none is given;
//   - IP addresses carry the VRF of their subnet and are unique per VRF;
//     moving a subnet moves its addresses; a VRF in use cannot be deleted;
//   - VRF writes require vrf:manage.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVRFModel(t *testing.T) {
	f := scopetest.Seed(t, "75")
	ctx := f.OrgCtx(f.OrgA)
	repo := ipam.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 50}

	vrfs, _, err := repo.ListVRFs(ctx, f.OrgA, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(vrfs) != 1 || vrfs[0].Name != "global" || !vrfs[0].IsDefault {
		t.Fatalf("VRFs of a new organization: %+v, want only the default global", vrfs)
	}
	global := vrfs[0]
	if bVRFs, _, _ := repo.ListVRFs(f.OrgCtx(f.OrgB), f.OrgB, page); len(bVRFs) != 1 || bVRFs[0].ID == global.ID {
		t.Errorf("organization B VRFs: %+v", bVRFs)
	}

	customer := &ipam.VRF{OrganizationID: f.OrgA, Name: "customer-a"}
	if err = repo.CreateVRF(ctx, customer); err != nil {
		t.Fatal(err)
	}
	if customer.IsDefault {
		t.Error("a created VRF is the default")
	}
	if err = repo.CreateVRF(ctx, &ipam.VRF{OrganizationID: f.OrgA, Name: "customer-a"}); err == nil {
		t.Error("duplicate VRF name accepted")
	}
	if err = repo.DeleteVRF(ctx, f.OrgA, global.ID); err == nil {
		t.Error("the default VRF was deleted")
	}

	// Subnets: unique per VRF, default VRF when none is given.
	defaultSubnet := &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24"}
	if err = repo.CreateSubnet(ctx, defaultSubnet); err != nil {
		t.Fatal(err)
	}
	if defaultSubnet.VRFID != global.ID {
		t.Errorf("subnet without VRF: %q, want the default %q", defaultSubnet.VRFID, global.ID)
	}
	customerSubnet := &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24", VRFID: customer.ID}
	if err = repo.CreateSubnet(ctx, customerSubnet); err != nil {
		t.Errorf("same CIDR in another VRF: %v", err)
	}
	if err = repo.CreateSubnet(ctx, &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24"}); err == nil {
		t.Error("duplicate CIDR in the default VRF accepted")
	}

	// IP addresses: VRF of the subnet, unique per VRF.
	a1 := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: defaultSubnet.ID, Address: "10.75.0.10", Type: "static"}
	if err = repo.CreateIPAddress(ctx, a1); err != nil {
		t.Fatal(err)
	}
	a2 := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: customerSubnet.ID, Address: "10.75.0.10", Type: "static"}
	if err = repo.CreateIPAddress(ctx, a2); err != nil {
		t.Errorf("same address in another VRF: %v", err)
	}
	if a1.VRFID != global.ID || a2.VRFID != customer.ID {
		t.Errorf("address VRFs: %q, %q", a1.VRFID, a2.VRFID)
	}
	if err = repo.CreateIPAddress(ctx, &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: defaultSubnet.ID,
		Address: "10.75.0.10", Type: "static"}); err == nil {
		t.Error("duplicate address in the same VRF accepted")
	}

	// Moving the default subnet into a third VRF moves its address.
	third := &ipam.VRF{OrganizationID: f.OrgA, Name: "lab"}
	if err = repo.CreateVRF(ctx, third); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateSubnet(ctx, f.OrgA, defaultSubnet.ID, ipam.UpdateSubnetRequest{VRFID: &third.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetIPAddress(ctx, f.OrgA, a1.ID); got == nil || got.VRFID != third.ID {
		t.Errorf("address after moving its subnet: %+v", got)
	}
	if err = repo.DeleteVRF(ctx, f.OrgA, third.ID); err == nil {
		t.Error("a VRF with subnets was deleted")
	}
	renamed := "lab-2"
	if v, renameErr := repo.UpdateVRF(ctx, f.OrgA, third.ID, ipam.UpdateVRFRequest{Name: &renamed}); renameErr != nil || v.Name != renamed {
		t.Errorf("rename: %+v, %v", v, renameErr)
	}

	// Writes need vrf:manage, reads ipam:read.
	table, err := os.ReadFile("../server/testdata/route_permissions.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"POST /api/v1/vrfs vrf:manage", "PATCH /api/v1/vrfs/{id} vrf:manage",
		"DELETE /api/v1/vrfs/{id} vrf:manage", "GET /api/v1/vrfs ipam:read"} {
		if !strings.Contains(string(table), line+"\n") {
			t.Errorf("route table lacks %q", line)
		}
	}
}
