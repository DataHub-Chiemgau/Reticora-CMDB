package importer

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// WP-920 (IPM-09): CSV import of subnets and addresses with the phpIPAM
// columns over the import job (PostgreSQL, application login). The dry run
// writes no subnet, address or history row; the real run builds the
// hierarchy (children under the pool), assigns an address without subnet
// to the narrowest subnet, binds over the MAC, writes ipam_change with
// source import and origin import; a pinned address keeps binding and DNS
// name; error rows (unknown section, ambiguous VLAN) are in the report
// while the other rows are written; a duplicate address opens exactly one
// duplicate_ip review. Without ipam:write the import does not start; a
// client principal creates no subnet of another client.
//
// Retest:
//   cd backend && go test -p 1 ./internal/importer/ -run TestImportIPAM

var ipamPerms = []identity.Permission{identity.PermImportRun, identity.PermIPAMWrite}

type pgIPAM struct {
	*pgImport
	repo *ipam.PGRepository
}

func newPGIPAM(t *testing.T) *pgIPAM {
	t.Helper()
	s := newPGImport(t)
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE role SET permissions = '["import:run","ipam:write"]' WHERE id = $1`,
		s.role); err != nil {
		t.Fatal(err)
	}
	p := &pgIPAM{pgImport: s, repo: ipam.NewPGRepository(s.f.App)}
	svc := reviews.NewService(reviews.NewPGRepository(s.f.App))
	s.h.WithWriter(NewSubnetWriter(p.repo, s.profile)).WithWriter(NewAddressWriter(p.repo, s.profile, svc))
	return p
}

func (p *pgIPAM) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := p.f.Admin.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (p *pgIPAM) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := p.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (p *pgIPAM) text(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v string
	if err := p.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// launch starts an import of entity with the identity mapping of the
// header of content in ctx.
func (p *pgIPAM) launch(t *testing.T, ctx context.Context, entity string, dryRun bool, content string) string {
	t.Helper()
	header, _, _ := strings.Cut(content, "\n")
	cols := map[string]ColumnMapping{}
	for _, c := range strings.Split(header, ",") {
		cols[c] = ColumnMapping{Target: c}
	}
	prof := &Profile{OrganizationID: p.f.OrgA, Name: entity + "-" + p.f.ID(), Entity: entity, Format: FormatCSV, Mode: "both",
		Mapping: Mapping{Columns: cols}}
	if err := p.profile.Create(p.f.OrgCtx(p.f.OrgA), prof); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return p.start(t, ctx, p.document(t, content), prof.ID, dryRun)
}

func (p *pgIPAM) run(t *testing.T, entity string, dryRun bool, content string) (JobResult, [][]string) {
	t.Helper()
	c := &pgCI{pgImport: p.pgImport}
	return c.finish(t, p.launch(t, requestContextWith(p.f.OrgA, p.user, nil, ipamPerms...), entity, dryRun, content))
}

func example(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ipamRows counts the subnets, addresses and history rows of organization A.
func (p *pgIPAM) ipamRows(t *testing.T) [3]int {
	t.Helper()
	return [3]int{
		p.count(t, `SELECT count(*) FROM subnet WHERE organization_id = $1`, p.f.OrgA),
		p.count(t, `SELECT count(*) FROM ip_address WHERE organization_id = $1`, p.f.OrgA),
		p.count(t, `SELECT count(*) FROM ipam_change WHERE organization_id = $1`, p.f.OrgA),
	}
}

func (p *pgIPAM) subnetID(t *testing.T, cidr string) string {
	t.Helper()
	return p.text(t, `SELECT COALESCE(max(id::text), '') FROM subnet WHERE organization_id = $1 AND cidr = $2::cidr`, p.f.OrgA, cidr)
}

// seedReferences creates the section lab, the nameserver set intern, the
// org-wide VLAN 200 and VLAN 300 at two sites (ambiguous).
func (p *pgIPAM) seedReferences(t *testing.T) {
	t.Helper()
	org := p.f.OrgA
	p.exec(t, `INSERT INTO ipam_section (organization_id, name) VALUES ($1, 'lab')`, org)
	p.exec(t, `INSERT INTO nameserver_set (organization_id, name, servers) VALUES ($1, 'intern', '{10.0.0.53}')`, org)
	p.exec(t, `INSERT INTO vlan (organization_id, vlan_id, name) VALUES ($1, 200, 'office')`, org)
	for _, name := range []string{"Werk Nord", "Werk Süd"} {
		site := p.f.ID()
		p.exec(t, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, site, org, p.f.Client1, name)
		p.exec(t, `INSERT INTO vlan (organization_id, site_id, vlan_id, name) VALUES ($1, $2, 300, 'users')`, org, site)
	}
}

func TestImportIPAMSubnetsAndAddresses(t *testing.T) {
	p := newPGIPAM(t)
	p.seedReferences(t)
	subnets := example(t, "phpipam_subnets.csv")

	// Dry run: the rows report what the real run does and write nothing.
	before := p.ipamRows(t)
	got, report := p.run(t, "subnet", true, subnets)
	wantResult(t, got, 4, 0, 0, 3, report)
	if after := p.ipamRows(t); after != before {
		t.Fatalf("the dry run wrote rows: %v → %v", before, after)
	}

	got, report = p.run(t, "subnet", false, subnets)
	wantResult(t, got, 4, 0, 0, 3, report)
	if !hasLine(report, 6, SubnetSectionTarget, CodeReferenceNotFound) {
		t.Fatalf("unknown section: %v", report)
	}
	if !hasLine(report, 7, SubnetVLANTarget, CodeReferenceAmbiguous) {
		t.Fatalf("ambiguous VLAN: %v", report)
	}
	if !hasLine(report, 8, SubnetCIDRTarget, CodeInvalidValue) {
		t.Fatalf("contradiction: %v", report)
	}
	pool, office, lab := p.subnetID(t, "10.92.0.0/16"), p.subnetID(t, "10.92.1.0/24"), p.subnetID(t, "10.92.2.0/24")
	if pool == "" || office == "" || lab == "" || p.subnetID(t, "10.92.4.0/24") != "" || p.subnetID(t, "10.92.5.0/24") != "" {
		t.Fatalf("subnets: pool %q office %q lab %q", pool, office, lab)
	}
	// The hierarchy: the /24 are children of the pool.
	if parent := p.text(t, `SELECT COALESCE(parent_id::text, '') FROM subnet WHERE id = $1`, office); parent != pool {
		t.Fatalf("parent of the office %q, want the pool %q", parent, pool)
	}
	if isPool := p.text(t, `SELECT is_pool::text FROM subnet WHERE id = $1`, pool); isPool != "true" {
		t.Fatalf("is_pool of the campus %s", isPool)
	}
	if v := p.text(t, `SELECT concat_ws('|', host(gateway), (SELECT vlan_id FROM vlan WHERE id = subnet.vlan_id),
			(SELECT name FROM nameserver_set WHERE id = subnet.nameserver_set_id)) FROM subnet WHERE id = $1`, office); v != "10.92.1.1|200|intern" {
		t.Fatalf("office gateway|vlan|nameserver = %q", v)
	}
	if c := p.text(t, `SELECT COALESCE(client_id::text, '') FROM subnet WHERE id = $1`, lab); c != p.f.Client1 {
		t.Fatalf("client of the lab %q", c)
	}
	if s := p.text(t, `SELECT name FROM ipam_section WHERE id = (SELECT section_id FROM subnet WHERE cidr = '10.92.3.0/24' AND organization_id = $1)`,
		p.f.OrgA); s != "lab" {
		t.Fatalf("section of 10.92.3.0/24 %q", s)
	}
	if n := p.count(t, `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'subnet' AND change_type = 'create'
		AND object_id IN ($2::uuid, $3::uuid, $4::uuid) AND source = 'import'`, p.f.OrgA, pool, office, lab); n != 3 {
		t.Fatalf("history rows with source import: %d", n)
	}
	// The same file again changes nothing.
	got, report = p.run(t, "subnet", false, subnets)
	wantResult(t, got, 0, 0, 4, 3, report)

	// Addresses: two devices with an interface each; an unbound pinned
	// address and one on the interface of the first device.
	ciA, ciB := p.f.CI(t, p.f.OrgA, "", "ipam-import-a"), p.f.CI(t, p.f.OrgA, "", "ipam-import-b")
	ifA, ifB := p.f.ID(), p.f.ID()
	p.exec(t, `INSERT INTO network_interface (id, organization_id, ci_id, name, mac) VALUES ($1, $2, $3, 'eth0', '02:00:00:09:20:01')`, ifA, p.f.OrgA, ciA)
	p.exec(t, `INSERT INTO network_interface (id, organization_id, ci_id, name, mac) VALUES ($1, $2, $3, 'eth0', '02:00:00:09:20:02')`, ifB, p.f.OrgA, ciB)
	pinned := p.f.ID()
	p.exec(t, `INSERT INTO ip_address (id, organization_id, subnet_id, address, type, dns_name, description, pinned)
		VALUES ($1, $2, $3, '10.92.1.20', 'static', 'keep.example.test', 'kept', true)`, pinned, p.f.OrgA, office)
	p.exec(t, `INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type)
		VALUES ($1, $2, $3, '10.92.1.30', 'static')`, p.f.OrgA, office, ifA)
	addresses := example(t, "phpipam_addresses.csv")

	before = p.ipamRows(t)
	got, report = p.run(t, "ip_address", true, addresses)
	wantResult(t, got, 4, 1, 1, 1, report)
	if after := p.ipamRows(t); after != before {
		t.Fatalf("the dry run wrote rows: %v → %v", before, after)
	}
	if n := p.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'duplicate_ip'`, p.f.OrgA); n != 0 {
		t.Fatalf("the dry run opened %d reviews", n)
	}

	got, report = p.run(t, "ip_address", false, addresses)
	// .10 .11 .5 and the second row of .30 are created; .1 exists since
	// the gateway of the office (IPM-04) and is changed; the pinned .20 is
	// unchanged (kept fields), Bogus is a row error.
	wantResult(t, got, 4, 1, 1, 1, report)
	if !hasLine(report, 8, AddressStateTarget, CodeInvalidValue) {
		t.Fatalf("unknown state: %v", report)
	}
	addr := func(host, extra string) string {
		t.Helper()
		return p.text(t, `SELECT concat_ws('|', COALESCE(subnet_id::text, '-'), COALESCE(interface_id::text, '-'), COALESCE(ci_id::text, '-'),
			origin, state, type, COALESCE(dns_name, '-')) FROM ip_address WHERE organization_id = $1 AND address_host = $2 `+extra+` LIMIT 1`,
			p.f.OrgA, host)
	}
	if v := addr("10.92.1.10", ""); v != strings.Join([]string{office, ifA, ciA, "import", "active", "static", "srv1.example.test"}, "|") {
		t.Fatalf(".10 bound over the MAC: %s", v)
	}
	if v := addr("10.92.1.11", ""); v != strings.Join([]string{office, "-", "-", "import", "offline", "static", "-"}, "|") {
		t.Fatalf(".11 without interface: %s", v)
	}
	if !hasLine(report, 4, AddressMACTarget, CodeInterfaceNotFound) {
		t.Fatalf("warning of the unknown MAC: %v", report)
	}
	// Without subnet column: the narrowest subnet (WP-912), type from DHCP.
	if v := addr("10.92.2.5", ""); !strings.HasPrefix(v, lab+"|") || !strings.Contains(v, "|import|unknown|dhcp|") {
		t.Fatalf(".5 in the lab: %s", v)
	}
	if g := p.text(t, `SELECT concat_ws('|', is_gateway::text, dns_name) FROM ip_address WHERE organization_id = $1 AND address_host = '10.92.1.1'`,
		p.f.OrgA); g != "true|gw.example.test" {
		t.Fatalf("gateway address %s", g)
	}
	if n := p.count(t, `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'ip_address' AND change_type = 'update'
		AND field_name = 'dns_name' AND source = 'import'`, p.f.OrgA); n != 1 {
		t.Fatalf("history of the changed gateway address: %d", n)
	}
	// The pinned address keeps its binding, DNS name and description.
	if v := p.text(t, `SELECT concat_ws('|', COALESCE(interface_id::text, '-'), dns_name, description, origin) FROM ip_address WHERE id = $1`, pinned); v != "-|keep.example.test|kept|manual" {
		t.Fatalf("pinned address %s", v)
	}
	if !hasLine(report, 6, "description,dns_name,interface_id", CodePinnedKept) {
		t.Fatalf("kept fields of the pinned address: %v", report)
	}
	// .30 now on both interfaces: one duplicate_ip review.
	if n := p.count(t, `SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.92.1.30'`, p.f.OrgA); n != 2 {
		t.Fatalf("rows of .30: %d", n)
	}
	if n := p.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'duplicate_ip' AND payload->>'address' = '10.92.1.30'`,
		p.f.OrgA); n != 1 {
		t.Fatalf("duplicate_ip reviews of .30: %d", n)
	}
	if n := p.count(t, `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'ip_address' AND change_type = 'create'
		AND source <> 'import' AND object_id IN (SELECT id FROM ip_address WHERE origin = 'import' AND organization_id = $1)`, p.f.OrgA); n != 0 {
		t.Fatalf("%d history rows of imported addresses without source import", n)
	}
	if n := p.count(t, `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'ip_address' AND change_type = 'create'
		AND source = 'import'`, p.f.OrgA); n != 5 {
		// The four addresses of this run and the gateway address the
		// subnet import created with the gateway of the office.
		t.Fatalf("history rows with source import of the addresses: %d", n)
	}
	// A second run reuses the open review.
	got, report = p.run(t, "ip_address", false, addresses)
	wantResult(t, got, 0, 0, 6, 1, report)
	if n := p.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'duplicate_ip'`, p.f.OrgA); n != 1 {
		t.Fatalf("duplicate_ip reviews after the second run: %d", n)
	}
}

func TestImportIPAMNeedsIPAMWrite(t *testing.T) {
	p := newPGIPAM(t)
	cols := map[string]ColumnMapping{"cidr": {Target: "cidr"}}
	prof := &Profile{OrganizationID: p.f.OrgA, Name: "subnet-" + p.f.ID(), Entity: "subnet", Format: FormatCSV, Mode: "both",
		Mapping: Mapping{Columns: cols}}
	if err := p.profile.Create(p.f.OrgCtx(p.f.OrgA), prof); err != nil {
		t.Fatal(err)
	}
	doc := p.document(t, "cidr\n10.93.0.0/24\n")
	ctx := requestContextWith(p.f.OrgA, p.user, nil, identity.PermImportRun)
	w := p.call(t, ctx, http.MethodPost, "/api/v1/imports", `{"document_id":"`+doc+`","profile_id":"`+prof.ID+`"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("start without ipam:write: %d %s", w.Code, w.Body.String())
	}
	if p.subnetID(t, "10.93.0.0/24") != "" {
		t.Fatal("subnet written")
	}
}

func TestImportIPAMClientPrincipal(t *testing.T) {
	p := newPGIPAM(t)
	scope := database.OrgWideScope(p.f.OrgA, p.user)
	scope.Clients = database.ScopeIDs(p.f.Client1)
	ctx := requestContextWith(p.f.OrgA, p.user, &scope, ipamPerms...)
	c := &pgCI{pgImport: p.pgImport}
	got, report := c.finish(t, p.launch(t, ctx, "subnet", false,
		"cidr,customer\n10.94.1.0/24,Client 1\n10.94.2.0/24,Client 2\n"))
	wantResult(t, got, 1, 0, 0, 1, report)
	if !hasLine(report, 3, SubnetCustomerTarget, CodeReferenceNotFound) {
		t.Fatalf("the other client: %v", report)
	}
	if p.subnetID(t, "10.94.2.0/24") != "" {
		t.Fatal("a subnet of the other client was created")
	}
	if c := p.text(t, `SELECT COALESCE(client_id::text, '') FROM subnet WHERE organization_id = $1 AND cidr = '10.94.1.0/24'`, p.f.OrgA); c != p.f.Client1 {
		t.Fatalf("client of the own subnet %q", c)
	}
}
