package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Filters of the subnet and address lists of IPM-20 (WP-909) against
// PostgreSQL. They run only against PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed).

// filterEnv is the router of the IPAM handler on the PostgreSQL repository
// with an org-wide and a client principal.
type filterEnv struct {
	f            *scopetest.Fixture
	mux          *chi.Mux
	org, client1 context.Context
	repo         *ipam.PGRepository
	t            *testing.T
}

func newFilterEnv(t *testing.T) *filterEnv {
	f := scopetest.Seed(t, "cc")
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	return &filterEnv{f: f, mux: mux, repo: repo, t: t,
		org:     tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}),
		client1: tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}
}

func (e *filterEnv) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// create posts body to path as the org principal and returns the id.
func (e *filterEnv) create(path, body string) string {
	e.t.Helper()
	w := e.call(e.org, http.MethodPost, path, body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("POST %s %s: %d %s", path, body, w.Code, w.Body.String())
	}
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return v.ID
}

// subnets lists the CIDRs of GET /subnets?query in the order of the answer.
func (e *filterEnv) subnets(ctx context.Context, query string) []string {
	e.t.Helper()
	w := e.call(ctx, http.MethodGet, "/api/v1/subnets?limit=200&"+query, "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /subnets?%s = %d %s", query, w.Code, w.Body.String())
	}
	var page api.ListResponse[ipam.Subnet]
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	out := []string{}
	for _, s := range page.Data {
		out = append(out, s.CIDR)
	}
	return out
}

// addresses lists the hosts of GET /ips?query, sorted.
func (e *filterEnv) addresses(ctx context.Context, query string) string {
	e.t.Helper()
	w := e.call(ctx, http.MethodGet, "/api/v1/ips?limit=200&"+query, "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /ips?%s = %d %s", query, w.Code, w.Body.String())
	}
	var page api.ListResponse[ipam.IPAddress]
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	out := []string{}
	for _, a := range page.Data {
		out = append(out, host(a.Address))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// refused checks that GET path answers 422 with field.
func (e *filterEnv) refused(path, field string) {
	e.t.Helper()
	w := e.call(e.org, http.MethodGet, path, "")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
		e.t.Errorf("GET %s = %d %s, want 422 %s", path, w.Code, w.Body.String(), field)
	}
}

// TestSubnetFilterContainsChain: contains=10.1.1.5 lists 10.1.1.0/24,
// 10.1.0.0/16 and 10.0.0.0/8 in this order, not the same CIDR of another
// VRF; with vrf_id the chain of that VRF; a client principal sees only its
// visible subnets of the chain; no address is 422 contains.
func TestSubnetFilterContainsChain(t *testing.T) {
	e := newFilterEnv(t)
	e.create("/api/v1/subnets", `{"cidr":"10.0.0.0/8"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.1.0.0/16","client_id":"`+e.f.Client2+`"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.1.1.0/24","client_id":"`+e.f.Client1+`"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.2.0.0/16"}`)
	vrf := e.create("/api/v1/vrfs", `{"name":"filter-vrf"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.1.1.0/24","vrf_id":"`+vrf+`"}`)

	if got := strings.Join(e.subnets(e.org, "contains=10.1.1.5"), ","); got != "10.1.1.0/24,10.1.0.0/16,10.0.0.0/8" {
		t.Errorf("contains=10.1.1.5 = %s, want 10.1.1.0/24,10.1.0.0/16,10.0.0.0/8", got)
	}
	if got := strings.Join(e.subnets(e.org, "contains=10.1.1.5&vrf_id="+vrf), ","); got != "10.1.1.0/24" {
		t.Errorf("contains in the other VRF = %s, want 10.1.1.0/24", got)
	}
	if got := strings.Join(e.subnets(e.client1, "contains=10.1.1.5"), ","); got != "10.1.1.0/24,10.0.0.0/8" {
		t.Errorf("client 1 contains = %s, want its subnet and the org-wide one", got)
	}
	if got := e.subnets(e.org, "contains=192.168.1.1"); len(got) != 0 {
		t.Errorf("contains of an address outside every subnet = %v", got)
	}
	e.refused("/api/v1/subnets?contains=10.1.1", "contains")
}

// TestSubnetFilterCombinations: section, VRF, VLAN, client, pool and q
// combined with AND; malformed ids are 422 with their field, unknown ids an
// empty list.
func TestSubnetFilterCombinations(t *testing.T) {
	e := newFilterEnv(t)
	section := e.create("/api/v1/ipam-sections", `{"name":"filter-section"}`)
	vrf := e.create("/api/v1/vrfs", `{"name":"filter-vrf-2"}`)
	vlan := e.create("/api/v1/vlans", `{"vlan_id":42,"name":"filter-vlan"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.30.0.0/24","name":"Office Berlin","section_id":"`+section+`","vrf_id":"`+vrf+`",
		"vlan_id":"`+vlan+`","client_id":"`+e.f.Client1+`"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.30.1.0/24","name":"office munich","section_id":"`+section+`","client_id":"`+e.f.Client1+`"}`)
	e.create("/api/v1/subnets", `{"cidr":"10.30.2.0/24","description":"lab 100%","is_pool":true}`)
	e.create("/api/v1/subnets", `{"cidr":"10.31.0.0/24","name":"datacenter","client_id":"`+e.f.Client2+`"}`)

	for query, want := range map[string]string{
		"section_id=" + section:                    "10.30.0.0/24,10.30.1.0/24",
		"section_id=" + section + "&vrf_id=" + vrf: "10.30.0.0/24",
		"vlan_id=" + vlan:                          "10.30.0.0/24",
		"q=OFFICE":                                 "10.30.0.0/24,10.30.1.0/24",
		"q=office&client_id=" + e.f.Client1 + "&vrf_id=" + vrf: "10.30.0.0/24",
		"q=10.31":                      "10.31.0.0/24",
		"q=" + url.QueryEscape("100%"): "10.30.2.0/24",
		"q=" + url.QueryEscape("0%"):   "10.30.2.0/24",
		"is_pool=true":                 "10.30.2.0/24",
		"is_pool=false&q=office":       "10.30.0.0/24,10.30.1.0/24",
		"section_id=" + e.f.ID():       "",
		"client_id=" + e.f.Client2:     "10.31.0.0/24",
	} {
		got := e.subnets(e.org, query)
		sort.Strings(got)
		if strings.Join(got, ",") != want {
			t.Errorf("GET /subnets?%s = %v, want %s", query, got, want)
		}
	}
	for field, value := range map[string]string{"section_id": "x", "vrf_id": "x", "vlan_id": "x", "client_id": "x", "site_id": "x", "parent_id": "x",
		"is_pool": "maybe"} {
		e.refused("/api/v1/subnets?"+field+"="+value, field)
	}
}

// TestIPFilterStateTypeCI: state, type, client, VRF, CI (through the
// interface) and q of the address list; unknown values are 422 with their
// field.
func TestIPFilterStateTypeCI(t *testing.T) {
	e := newFilterEnv(t)
	s1 := e.create("/api/v1/subnets", `{"cidr":"10.40.0.0/24","client_id":"`+e.f.Client1+`"}`)
	s2 := e.create("/api/v1/subnets", `{"cidr":"10.41.0.0/24","client_id":"`+e.f.Client2+`"}`)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "filter-ci")
	nic := e.create("/api/v1/cis/"+ci+"/interfaces", `{"name":"eth0"}`)
	a1 := e.create("/api/v1/ips", `{"address":"10.40.0.10","subnet_id":"`+s1+`","interface_id":"`+nic+`","dns_name":"web01.example.test"}`)
	e.create("/api/v1/ips", `{"address":"10.40.0.11","subnet_id":"`+s1+`","type":"dhcp","dns_name":"printer.example.test"}`)
	e.create("/api/v1/ips", `{"address":"10.41.0.12","subnet_id":"`+s2+`","type":"reserved"}`)
	s6 := e.create("/api/v1/subnets", `{"cidr":"2001:db8:40::/64","client_id":"`+e.f.Client2+`"}`)
	e.create("/api/v1/ips", `{"address":"2001:db8:40::a","subnet_id":"`+s6+`"}`)
	if w := e.call(e.org, http.MethodPatch, "/api/v1/ips/"+a1, `{"state":"active"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH state: %d %s", w.Code, w.Body.String())
	}
	for query, want := range map[string]string{
		"state=active":                          "10.40.0.10",
		"state=unknown&type=dhcp":               "10.40.0.11",
		"type=reserved":                         "10.41.0.12",
		"client_id=" + e.f.Client1:              "10.40.0.10,10.40.0.11",
		"ci_id=" + ci:                           "10.40.0.10",
		"ci_id=" + e.f.ID():                     "",
		"q=10.40.0.1":                           "10.40.0.10,10.40.0.11",
		"q=printer":                             "10.40.0.11",
		"q=0.10":                                "",
		"q=" + url.QueryEscape("2001:DB8:40::"): "2001:db8:40::a",
		"client_id=" + e.f.Client2 + "&type=reserved":   "10.41.0.12",
		"client_id=" + e.f.Client1 + "&state=offline":   "",
		"vrf_id=" + e.f.ID():                            "",
		"subnet_id=" + s1 + "&q=web&state=active":       "10.40.0.10",
		"subnet_id=" + s2 + "&client_id=" + e.f.Client1: "",
	} {
		if got := e.addresses(e.org, query); got != want {
			t.Errorf("GET /ips?%s = %s, want %s", query, got, want)
		}
	}
	if got := e.addresses(e.client1, ""); got != "10.40.0.10,10.40.0.11" {
		t.Errorf("client 1 sees %s", got)
	}
	for field, value := range map[string]string{"state": "on", "type": "manual", "client_id": "x", "vrf_id": "x", "ci_id": "x", "subnet_id": "x"} {
		e.refused("/api/v1/ips?"+field+"="+value, field)
		e.refused("/api/v1/ip-addresses?"+field+"="+value, field)
	}
}

// TestIPAMListPlansUnderRLS: the filter statements of the lists, explained
// as the application role in a transaction restricted to client 1, read
// subnet through idx_subnet_org_vrf_parent and idx_subnet_org_section and
// ip_address through idx_ip_address_org_state, idx_ip_address_subnet and an
// index on interface_id (Konvention 22): the index condition carries the
// filtered column. Sequential scans are switched off so the small fixture
// shows which indexes are usable under row level security.
func TestIPAMListPlansUnderRLS(t *testing.T) {
	e := newFilterEnv(t)
	f := e.f
	bg := context.Background()
	section := e.create("/api/v1/ipam-sections", `{"name":"plan-section"}`)
	subnet := e.create("/api/v1/subnets", `{"cidr":"10.50.0.0/16","client_id":"`+f.Client1+`","section_id":"`+section+`"}`)
	// 512 further subnets in the default section, 1,990 addresses without
	// subnet and ten in the subnet: the filters are selective, as in an
	// organization with many subnets.
	if _, err := f.Admin.Exec(bg, `INSERT INTO subnet (organization_id, cidr)
		SELECT $1, ('10.' || (100 + i / 256) || '.' || (i % 256) || '.0/24')::cidr FROM generate_series(0, 511) i`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ip_address (organization_id, subnet_id, address)
		SELECT $1, CASE WHEN i < 10 THEN $2::uuid END, ('10.' || CASE WHEN i < 10 THEN 50 ELSE 51 END || '.' || (i / 250) || '.' || (i % 250 + 1))::inet
		FROM generate_series(0, 1999) i`, f.OrgA, subnet); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"subnet", "ip_address", "network_interface"} {
		if _, err := f.Admin.Exec(bg, "ANALYZE "+tbl); err != nil {
			t.Fatal(err)
		}
	}
	var vrf string
	if err := f.Admin.QueryRow(bg, `SELECT vrf_id::text FROM subnet WHERE id = $1`, subnet).Scan(&vrf); err != nil {
		t.Fatal(err)
	}
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Clients = database.ScopeIDs(f.Client1)
	explain := func(sql string, args []any) string {
		t.Helper()
		var plan string
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sql, args...)
			if err != nil {
				return err
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			plan = strings.Join(lines, "\n")
			return err
		}); err != nil {
			t.Fatalf("explain %s: %v", sql, err)
		}
		return plan
	}
	subnetPlan := func(sf ipam.SubnetFilter) string {
		where, args, order := ipam.SubnetFilterWhere(f.OrgA, &sf)
		return explain("SELECT "+ipam.SubnetColumns+" FROM subnet WHERE "+where+" ORDER BY "+order+" LIMIT 200", args)
	}
	ipPlan := func(ipf ipam.IPFilter) string {
		where, args := ipam.IPFilterWhere(f.OrgA, &ipf)
		return explain("SELECT "+ipam.IPColumns+" FROM ip_address WHERE "+where+" ORDER BY created_at DESC LIMIT 200", args)
	}
	for _, c := range []struct {
		name, plan, index, column string
	}{
		{"subnets by section", subnetPlan(ipam.SubnetFilter{SectionID: section}), "idx_subnet_org_section", "section_id"},
		{"subnets contains", subnetPlan(ipam.SubnetFilter{Contains: "10.50.1.1", VRFID: vrf}), "idx_subnet_org_vrf_parent", "vrf_id"},
		{"subnets of a parent", subnetPlan(ipam.SubnetFilter{ParentID: subnet}), "idx_subnet_org_vrf_parent", "parent_id"},
		{"addresses by state", ipPlan(ipam.IPFilter{State: "active"}), "idx_ip_address_org_state", "state"},
		{"addresses of a subnet", ipPlan(ipam.IPFilter{SubnetID: subnet}), "idx_ip_address_subnet", "subnet_id"},
		// The interfaces of the CI, then their addresses by an index on
		// interface_id (idx_ip_address_interface or the unique index of
		// interface and address).
		{"addresses of a CI", ipPlan(ipam.IPFilter{CIID: f.ID()}), "idx_ip_address_interface|uq_ip_address_org_vrf_interface_address",
			"interface_id"},
	} {
		uses := false
		for _, index := range strings.Split(c.index, "|") {
			uses = uses || strings.Contains(c.plan, "using "+index+" on ")
		}
		if !uses || !planHasIndexCond(c.plan, c.column) {
			t.Errorf("%s: plan does not use %s with %s in the index condition:\n%s", c.name, c.index, c.column, c.plan)
		}
		if strings.Contains(c.plan, "Seq Scan on subnet") || strings.Contains(c.plan, "Seq Scan on ip_address") {
			t.Errorf("%s: sequential scan under row level security:\n%s", c.name, c.plan)
		}
	}
}

// planHasIndexCond reports whether an index condition of plan compares
// column.
func planHasIndexCond(plan, column string) bool {
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Index Cond:") && (strings.Contains(line, column+" =") || strings.Contains(line, column+" >>=")) {
			return true
		}
	}
	return false
}
