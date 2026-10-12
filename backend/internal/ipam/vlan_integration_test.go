package ipam_test

import (
	"context"
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
	ni := &ipam.NetworkInterface{OrganizationID: f.OrgA, CIID: ciID, Name: "eth0", InterfaceType: "ethernet",
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
