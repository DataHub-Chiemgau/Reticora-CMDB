package ipam_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// TestIPAMRepositoryClientScope runs the IPAM repository with the principal's
// tenant scope (TEN-06, WP-013): a principal restricted to client 1 neither
// sees nor changes a client-2 subnet, the addresses in it or the interfaces
// of a client-2 CI, and cannot attach its own address to them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIPAMRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "1f")
	ownCI := f.CI(t, f.OrgA, f.Client1, "ipam-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "ipam-c2")

	repo := ipam.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownNet := &ipam.Subnet{OrganizationID: f.OrgA, ClientID: f.Client1, CIDR: "10.31.1.0/24"}
	foreignNet := &ipam.Subnet{OrganizationID: f.OrgA, ClientID: f.Client2, CIDR: "10.31.2.0/24"}
	for _, sn := range []*ipam.Subnet{ownNet, foreignNet} {
		if err := repo.CreateSubnet(orgCtx, sn); err != nil {
			t.Fatalf("org-wide subnet %s: %v", sn.CIDR, err)
		}
	}
	nic := func(ciID, name string) *ipam.NetworkInterface {
		return &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: name, InterfaceType: "ethernet", AdminStatus: "up", OperStatus: "unknown"}
	}
	foreignNIC := nic(foreignCI, "eth0")
	if err := repo.CreateInterface(orgCtx, foreignNIC); err != nil {
		t.Fatalf("org-wide interface: %v", err)
	}
	foreignIP := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: foreignNet.ID, Address: "10.31.2.10", Status: "active"}
	if err := repo.CreateIPAddress(orgCtx, foreignIP); err != nil {
		t.Fatalf("org-wide address: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	subnets, _, err := repo.ListSubnets(ctx, f.OrgA, ipam.SubnetFilter{}, page)
	if err != nil || len(subnets) != 1 || subnets[0].ID != ownNet.ID {
		t.Fatalf("client-1 subnets: %+v err=%v, want only the client-1 subnet", subnets, err)
	}
	ips, total, err := repo.ListIPAddresses(ctx, f.OrgA, "", page)
	if err != nil || total != 0 || len(ips) != 0 {
		t.Fatalf("client-1 addresses: total=%d %+v err=%v, want none", total, ips, err)
	}
	if _, err = repo.GetIPAddress(ctx, f.OrgA, foreignIP.ID); err == nil {
		t.Fatal("client-1 principal read an address of a client-2 subnet")
	}
	if err = repo.DeleteIPAddress(ctx, f.OrgA, foreignIP.ID); err == nil {
		t.Fatal("client-1 principal deleted an address of a client-2 subnet")
	}
	if nics, _, listErr := repo.ListInterfacesForCI(ctx, f.OrgA, foreignCI, page); listErr != nil || len(nics) != 0 {
		t.Fatalf("client-1 interfaces of client-2 CI: %+v err=%v, want none", nics, listErr)
	}
	if err = repo.DeleteInterface(ctx, f.OrgA, foreignNIC.ID); err == nil {
		t.Fatal("client-1 principal deleted an interface of a client-2 CI")
	}
	if err = repo.CreateInterface(ctx, nic(foreignCI, "eth9")); !errors.Is(err, ipam.ErrNotFound) {
		t.Fatalf("client-1 interface on client-2 CI: got %v, want ErrNotFound", err)
	}

	// Addresses created or moved by client 1 must stay within client 1.
	if err = repo.CreateIPAddress(ctx, &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: foreignNet.ID, Address: "10.31.2.11", Status: "active"}); !errors.Is(err, ipam.ErrNotFound) {
		t.Fatalf("client-1 address in client-2 subnet: got %v, want ErrNotFound", err)
	}
	ownIP := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: ownNet.ID, Address: "10.31.1.10", Status: "active"}
	if err = repo.CreateIPAddress(ctx, ownIP); err != nil {
		t.Fatalf("client-1 address in own subnet: %v", err)
	}
	if _, err = repo.UpdateIPAddress(ctx, f.OrgA, ownIP.ID, ipam.UpdateIPAddressRequest{InterfaceID: &foreignNIC.ID}); !errors.Is(err, ipam.ErrNotFound) {
		t.Fatalf("client-1 address moved to client-2 interface: got %v, want ErrNotFound", err)
	}
	ownNIC := nic(ownCI, "eth0")
	if err = repo.CreateInterface(ctx, ownNIC); err != nil {
		t.Fatalf("client-1 interface on own CI: %v", err)
	}
	if _, err = repo.UpdateIPAddress(ctx, f.OrgA, ownIP.ID, ipam.UpdateIPAddressRequest{InterfaceID: &ownNIC.ID}); err != nil {
		t.Fatalf("client-1 address on own interface: %v", err)
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM ip_address WHERE id = $1) + (SELECT count(*) FROM network_interface WHERE id = $2)`,
		foreignIP.ID, foreignNIC.ID).Scan(&count); err != nil {
		t.Fatalf("count client-2 rows: %v", err)
	}
	if count != 2 {
		t.Fatal("client-2 address or interface was deleted")
	}
	if _, _, err = repo.ListSubnets(context.Background(), f.OrgA, ipam.SubnetFilter{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
