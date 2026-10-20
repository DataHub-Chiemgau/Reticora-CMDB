package ipam_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// TestVLANCatalog covers WP-081 (NET-08, NET-01, NET-02, DB-05, E-24)
// against PostgreSQL:
//   - the VLAN number is unique per site and, for VLANs without site, per
//     organization (two partial indexes, no sentinel);
//   - the client follows the site and scopes the VLAN under RLS;
//   - subnets and interfaces reference the catalog by id; a VLAN of another
//     organization is refused;
//   - migration 000091 turns integer VLAN numbers of subnets into catalog
//     entries.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVLANCatalog(t *testing.T) {
	f := scopetest.Seed(t, "73")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := ipam.NewPGRepository(f.App)
	newSite := func(clientID, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, clientID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	siteA := newSite(f.Client1, "vlan-site-a")
	siteB := newSite(f.Client2, "vlan-site-b")
	create := func(site string, number int) (*ipam.VLAN, error) {
		v := &ipam.VLAN{OrganizationID: f.OrgA, SiteID: site, VLANID: number, Name: "vlan"}
		return v, repo.CreateVLAN(ctx, v)
	}

	a10, err := create(siteA, 10)
	if err != nil {
		t.Fatal(err)
	}
	if a10.ClientID != f.Client1 {
		t.Errorf("client of a site VLAN: %q, want the site's client %q", a10.ClientID, f.Client1)
	}
	if _, err = create(siteB, 10); err != nil {
		t.Errorf("same number at another site: %v", err)
	}
	if _, err = create(siteA, 10); err == nil {
		t.Error("duplicate number at the same site accepted")
	}
	global, err := create("", 10)
	if err != nil {
		t.Fatalf("same number without site: %v", err)
	}
	if _, err = create("", 10); err == nil {
		t.Error("duplicate number without site accepted")
	}
	if _, err = create(siteA, 4095); err == nil {
		t.Error("number 4095 accepted")
	}

	// RLS: a client-2 principal sees the site-B and the org-wide VLAN only.
	list, _, err := repo.ListVLANs(f.ClientCtx(f.Client2), f.OrgA, "", api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list {
		if v.ID == a10.ID {
			t.Error("client-2 principal sees a client-1 VLAN")
		}
	}
	if len(list) != 2 {
		t.Errorf("client-2 VLANs: %+v", list)
	}

	// Subnet and interface reference the catalog.
	sn := &ipam.Subnet{OrganizationID: f.OrgA, SiteID: siteA, ClientID: f.Client1, CIDR: "10.73.0.0/24", VLANID: a10.ID}
	if err = repo.CreateSubnet(ctx, sn); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetSubnet(ctx, f.OrgA, sn.ID); got == nil || got.VLANID != a10.ID {
		t.Errorf("subnet VLAN %+v", got)
	}
	ciID := f.CI(t, f.OrgA, f.Client1, "vlan-ci")
	ni := &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: "eth0", IfType: "ethernet",
		AdminStatus: "up", OperStatus: "up", VLANID: global.ID}
	if err = repo.CreateInterface(ctx, ni); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetInterface(ctx, f.OrgA, ni.ID); got == nil || got.VLANID != global.ID {
		t.Errorf("interface VLAN %+v", got)
	}
	var foreign string
	if err = f.Admin.QueryRow(bg, `INSERT INTO vlan (organization_id, vlan_id, name) VALUES ($1, 99, 'foreign') RETURNING id::text`,
		f.OrgB).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if err = repo.CreateSubnet(ctx, &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.73.1.0/24", VLANID: foreign}); err == nil {
		t.Error("subnet referenced a VLAN of another organization")
	}
	if err = repo.DeleteVLAN(ctx, f.OrgA, a10.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetSubnet(ctx, f.OrgA, sn.ID); got == nil || got.VLANID != "" {
		t.Errorf("subnet after deleting its VLAN: %+v", got)
	}
}

// TestMigration91ConvertsVLANNumbers runs the subnet section of migration
// 000091 on integer VLAN numbers inside a rolled-back transaction.
func TestMigration91ConvertsVLANNumbers(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "74")
	bg := context.Background()
	raw, err := os.ReadFile("../../migrations/000091_vlan_catalog.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "INSERT INTO vlan (organization_id, site_id, vlan_id, name)")
	end := strings.Index(text, "ALTER TABLE network_interface ADD COLUMN vlan_id uuid;")
	if start < 0 || end < start {
		t.Fatal("subnet section of 000091 not found")
	}

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.50s: %v", sql, execErr)
		}
	}
	exec(`DROP INDEX idx_subnet_vlan; ALTER TABLE subnet DROP CONSTRAINT subnet_vlan_fkey;
		ALTER TABLE subnet DROP COLUMN vlan_id; ALTER TABLE subnet ADD COLUMN vlan_id integer`)
	exec(`INSERT INTO subnet (organization_id, cidr, vlan_id) VALUES ($1, '10.74.0.0/24', 20), ($1, '10.74.1.0/24', 20),
		($1, '10.74.2.0/24', 5000)`, f.OrgA)
	exec(text[start:end])

	var refs, catalog, quarantined int
	if err = tx.QueryRow(bg, `SELECT count(DISTINCT s.vlan_id) FROM subnet s JOIN vlan v ON v.id = s.vlan_id
		WHERE s.organization_id = $1 AND v.vlan_id = 20 AND v.site_id IS NULL`, f.OrgA).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(bg, `SELECT count(*) FROM vlan WHERE organization_id = $1`, f.OrgA).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE organization_id = $1 AND migration = '000091'`,
		f.OrgA).Scan(&quarantined); err != nil {
		t.Fatal(err)
	}
	if refs != 1 || catalog != 1 || quarantined != 1 {
		t.Errorf("converted: %d referenced catalog entries, %d catalog rows, %d quarantined; want 1, 1, 1", refs, catalog, quarantined)
	}
}

// TestAuditT3VLANReferenceMustBeVisible covers WP-081 (NET-08, TEN-05,
// TEN-06; F-088, WP-371): a client-2 VLAN is not visible to a client-1
// principal, and CreateSubnet, UpdateSubnet, CreateInterface and
// UpdateInterface of that principal refuse it; nothing is stored. The
// principal's own VLAN is accepted (control).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
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

// TestVLANReferenceStaysWithinTheClient covers WP-371 (NET-08, NET-01,
// NET-02, TEN-05; F-088) on the write path under RLS: a subnet references a
// VLAN the principal can see, of the subnet's client or org-wide (E-09); an
// interface one of the client of its CI or org-wide. A VLAN the principal
// cannot see, or an unknown one, is 422 vlan_id "VLAN not found"; a visible
// VLAN of another client, also for an org-wide subnet or interface, is 422
// vlan_id "the VLAN belongs to another client". Nothing is written then. A
// subnet that moves to another client keeps its VLAN only when it fits the
// new client.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVLANReferenceStaysWithinTheClient(t *testing.T) {
	f := scopetest.Seed(t, "0d")
	repo := ipam.NewPGRepository(f.App)
	org, c1 := f.OrgCtx(f.OrgA), f.ClientCtx(f.Client1)
	own := &ipam.VLAN{OrganizationID: f.OrgA, SiteID: auditT3Site(t, f, f.Client1, "vlan-scope-a"), VLANID: 10, Name: "client-1"}
	foreign := &ipam.VLAN{OrganizationID: f.OrgA, SiteID: auditT3Site(t, f, f.Client2, "vlan-scope-b"), VLANID: 20, Name: "client-2"}
	shared := &ipam.VLAN{OrganizationID: f.OrgA, VLANID: 30, Name: "org-wide"}
	for _, v := range []*ipam.VLAN{own, foreign, shared} {
		if err := repo.CreateVLAN(org, v); err != nil {
			t.Fatal(err)
		}
	}
	if own.ClientID != f.Client1 || foreign.ClientID != f.Client2 || shared.ClientID != "" {
		t.Fatalf("precondition: VLAN clients %q, %q, %q", own.ClientID, foreign.ClientID, shared.ClientID)
	}
	unknown := f.ID()
	const notFound, otherClient = "VLAN not found", "the VLAN belongs to another client"
	// check requires err to be the 422 of vlan_id with message want, or no
	// error when want is empty.
	check := func(t *testing.T, err error, want string) {
		t.Helper()
		if want == "" {
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			return
		}
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "vlan_id" || fe.Status != http.StatusUnprocessableEntity || fe.Message != want {
			t.Fatalf("err = %v, want 422 vlan_id %q", err, want)
		}
	}
	str := func(s string) *string { return &s }

	t.Run("CreateSubnet", func(t *testing.T) {
		for i, tc := range []struct {
			name         string
			ctx          context.Context
			client, vlan string
			want         string
		}{
			{"client-1 principal, own VLAN", c1, f.Client1, own.ID, ""},
			{"client-1 principal, org-wide VLAN", c1, f.Client1, shared.ID, ""},
			{"client-1 principal, client-2 VLAN", c1, f.Client1, foreign.ID, notFound},
			{"client-1 principal, unknown VLAN", c1, f.Client1, unknown, notFound},
			{"org-wide principal, same client", org, f.Client1, own.ID, ""},
			{"org-wide principal, org-wide VLAN", org, f.Client1, shared.ID, ""},
			{"org-wide principal, client-2 VLAN", org, f.Client1, foreign.ID, otherClient},
			{"org-wide subnet, client-1 VLAN", org, "", own.ID, otherClient},
			{"org-wide subnet, org-wide VLAN", org, "", shared.ID, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cidr := fmt.Sprintf("10.13.%d.0/24", i)
				check(t, repo.CreateSubnet(tc.ctx, &ipam.Subnet{OrganizationID: f.OrgA, ClientID: tc.client, CIDR: cidr, VLANID: tc.vlan}), tc.want)
				stored, want := auditT3Str(t, f, `SELECT coalesce(string_agg(coalesce(vlan_id::text, '-'), ','), '') FROM subnet
					WHERE organization_id = $1 AND cidr = $2::cidr`, f.OrgA, cidr), tc.vlan
				if tc.want != "" {
					want = ""
				}
				if stored != want {
					t.Errorf("stored subnets with VLAN %q, want %q", stored, want)
				}
			})
		}
	})

	t.Run("UpdateSubnet", func(t *testing.T) {
		sn := &ipam.Subnet{OrganizationID: f.OrgA, ClientID: f.Client1, CIDR: "10.13.100.0/24"}
		if err := repo.CreateSubnet(org, sn); err != nil {
			t.Fatal(err)
		}
		state := func() string {
			return auditT3Str(t, f, `SELECT coalesce(client_id::text, '-') || ' ' || coalesce(vlan_id::text, '-') FROM subnet WHERE id = $1`, sn.ID)
		}
		for _, tc := range []struct {
			name        string
			ctx         context.Context
			req         ipam.UpdateSubnetRequest
			want, state string
		}{
			{"client-1 principal, client-2 VLAN", c1, ipam.UpdateSubnetRequest{VLANID: str(foreign.ID)}, notFound, f.Client1 + " -"},
			{"client-1 principal, unknown VLAN", c1, ipam.UpdateSubnetRequest{VLANID: str(unknown)}, notFound, f.Client1 + " -"},
			{"org-wide principal, client-2 VLAN", org, ipam.UpdateSubnetRequest{VLANID: str(foreign.ID)}, otherClient, f.Client1 + " -"},
			{"client-1 principal, org-wide VLAN", c1, ipam.UpdateSubnetRequest{VLANID: str(shared.ID)}, "", f.Client1 + " " + shared.ID},
			{"client-1 principal, own VLAN", c1, ipam.UpdateSubnetRequest{VLANID: str(own.ID)}, "", f.Client1 + " " + own.ID},
			{"move to client 2 with the client-1 VLAN", org, ipam.UpdateSubnetRequest{ClientID: str(f.Client2)}, otherClient, f.Client1 + " " + own.ID},
			{"move to org-wide with the client-1 VLAN", org, ipam.UpdateSubnetRequest{ClientID: str("")}, otherClient, f.Client1 + " " + own.ID},
			{"move to client 2 with a client-2 VLAN", org, ipam.UpdateSubnetRequest{ClientID: str(f.Client2), VLANID: str(foreign.ID)}, "", f.Client2 + " " + foreign.ID},
			{"move to org-wide without VLAN", org, ipam.UpdateSubnetRequest{ClientID: str(""), VLANID: str("")}, "", "- -"},
			{"org-wide subnet, org-wide VLAN", org, ipam.UpdateSubnetRequest{VLANID: str(shared.ID)}, "", "- " + shared.ID},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := repo.UpdateSubnet(tc.ctx, f.OrgA, sn.ID, tc.req)
				check(t, err, tc.want)
				if got := state(); got != tc.state {
					t.Errorf("client and VLAN after the update %q, want %q", got, tc.state)
				}
			})
		}
		foreignNet := &ipam.Subnet{OrganizationID: f.OrgA, ClientID: f.Client2, CIDR: "10.13.101.0/24"}
		if err := repo.CreateSubnet(org, foreignNet); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.UpdateSubnet(c1, f.OrgA, foreignNet.ID, ipam.UpdateSubnetRequest{VLANID: str(own.ID)}); !errors.Is(err, ipam.ErrNotFound) {
			t.Errorf("client-1 principal sets a VLAN on a client-2 subnet: err=%v, want ErrNotFound", err)
		}
	})

	t.Run("CreateInterface", func(t *testing.T) {
		ci1 := f.CI(t, f.OrgA, f.Client1, "vlan-scope-c1")
		ciOrg := f.CI(t, f.OrgA, "", "vlan-scope-org")
		for i, tc := range []struct {
			name     string
			ctx      context.Context
			ci, vlan string
			want     string
		}{
			{"client-1 principal, own VLAN", c1, ci1, own.ID, ""},
			{"client-1 principal, org-wide VLAN", c1, ci1, shared.ID, ""},
			{"client-1 principal, client-2 VLAN", c1, ci1, foreign.ID, notFound},
			{"client-1 principal, unknown VLAN", c1, ci1, unknown, notFound},
			{"org-wide principal, same client", org, ci1, own.ID, ""},
			{"org-wide principal, client-2 VLAN", org, ci1, foreign.ID, otherClient},
			{"org-wide CI, client-1 VLAN", org, ciOrg, own.ID, otherClient},
			{"org-wide CI, org-wide VLAN", org, ciOrg, shared.ID, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				name := fmt.Sprintf("eth%d", i)
				check(t, repo.CreateInterface(tc.ctx, &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: tc.ci, Name: name, IfType: "ethernet",
					VLANID: tc.vlan, AdminStatus: "up", OperStatus: "up"}), tc.want)
				stored, want := auditT3Str(t, f, `SELECT coalesce(string_agg(coalesce(vlan_id::text, '-'), ','), '') FROM network_interface
					WHERE ci_id = $1 AND name = $2`, tc.ci, name), tc.vlan
				if tc.want != "" {
					want = ""
				}
				if stored != want {
					t.Errorf("stored interfaces with VLAN %q, want %q", stored, want)
				}
			})
		}
	})

	t.Run("UpdateInterface", func(t *testing.T) {
		nic := func(ciID string) *ipam.NetworkInterface {
			ni := &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: "eth0", IfType: "ethernet", AdminStatus: "up", OperStatus: "up"}
			if err := repo.CreateInterface(org, ni); err != nil {
				t.Fatal(err)
			}
			return ni
		}
		ni := nic(f.CI(t, f.OrgA, f.Client1, "vlan-scope-update-c1"))
		vlanOf := func(id string) string {
			return auditT3Str(t, f, `SELECT coalesce(vlan_id::text, '-') FROM network_interface WHERE id = $1`, id)
		}
		for _, tc := range []struct {
			name             string
			ctx              context.Context
			vlan, want, left string
		}{
			{"client-1 principal, client-2 VLAN", c1, foreign.ID, notFound, "-"},
			{"client-1 principal, unknown VLAN", c1, unknown, notFound, "-"},
			{"org-wide principal, client-2 VLAN", org, foreign.ID, otherClient, "-"},
			{"client-1 principal, org-wide VLAN", c1, shared.ID, "", shared.ID},
			{"client-1 principal, own VLAN", c1, own.ID, "", own.ID},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := repo.UpdateInterface(tc.ctx, f.OrgA, ni.ID, ipam.UpdateInterfaceRequest{VLANID: str(tc.vlan)})
				check(t, err, tc.want)
				if got := vlanOf(ni.ID); got != tc.left {
					t.Errorf("VLAN after the update %q, want %q", got, tc.left)
				}
			})
		}
		other := nic(f.CI(t, f.OrgA, f.Client2, "vlan-scope-update-c2"))
		if _, err := repo.UpdateInterface(c1, f.OrgA, other.ID, ipam.UpdateInterfaceRequest{VLANID: str(own.ID)}); !errors.Is(err, ipam.ErrNotFound) {
			t.Errorf("client-1 principal sets a VLAN on a client-2 interface: err=%v, want ErrNotFound", err)
		}
		if got := vlanOf(other.ID); got != "-" {
			t.Errorf("VLAN of the client-2 interface %q, want none", got)
		}
	})
}
