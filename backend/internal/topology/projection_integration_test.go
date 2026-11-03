package topology_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// TestNetworkProjectionAgainstPostgres covers IMP-11 (WP-298) through the
// production repositories under RLS:
//   - Nodes and edges: a CI reaches a subnet over an address on one of its
//     interfaces (in_subnet), a VLAN over an interface (in_vlan), a subnet
//     its VLAN (in_vlan); two addresses of one interface in one subnet are
//     one edge; an address without interface or without subnet is none.
//     Edges carry source projection and the stable md5 id.
//   - The ci_type filter narrows the CIs; a subnet or VLAN without a CI of
//     the type is gone.
//   - Without a root the projected nodes count towards the 2,000 nodes:
//     1,999 CIs answer 200 without include and 422 naming include with it.
//   - Client and site scope: a subnet or VLAN of client 2 or site 2 on a
//     visible CI is neither a node nor an edge for a client-1 or site-1
//     principal.
//   - An unknown include is 422 on include, include=network without
//     ipam:read 403.
//   - The impact of /impact/{id} and /impact?ci_ids= with include=network.
//   - EXPLAIN of the captured projection SQL as the application role (the
//     plan frame of WP-436): idx_network_interface_ci on network_interface
//     and idx_ip_address_interface on ip_address; without the index the
//     check reports it missing.
//
// It shares its fixture tag with TestTopologyRootAndLimitAgainstPostgres:
// the tests of a package run one after another.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNetworkProjectionAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b8")
	bg := context.Background()
	execT := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		execT(t, sql, args...)
	}
	site1, site2, siteC2 := f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $4, $5, 'net site1'), ($2, $4, $5, 'net site2'), ($3, $4, $6, 'net client2')`,
		site1, site2, siteC2, f.OrgA, f.Client1, f.Client2)
	var switchType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'switch' AND organization_id IS NULL`).Scan(&switchType); err != nil {
		t.Fatal(err)
	}
	ciAt := func(name string) string {
		id := f.CI(t, f.OrgA, f.Client1, name)
		exec(`UPDATE ci SET location_id = $2 WHERE id = $1`, id, site1)
		return id
	}
	// leaf powered_by hub; sw (a switch) and other share subnet s1 with
	// leaf but no relationship.
	hub, leaf, sw, other := ciAt("net-hub"), ciAt("net-leaf"), ciAt("net-switch"), ciAt("net-other")
	exec(`UPDATE ci SET ci_type_id = $2 WHERE id = $1`, sw, switchType)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'powered_by')`, f.OrgA, leaf, hub)
	vlan := func(number int, name string, site any) string {
		id := f.ID()
		exec(`INSERT INTO vlan (id, organization_id, site_id, vlan_id, name) VALUES ($1, $2, $3, $4, $5)`, id, f.OrgA, site, number, name)
		return id
	}
	v10, v20, vC2, vS2 := vlan(10, "prod", nil), vlan(20, "mgmt", nil), vlan(30, "foreign", siteC2), vlan(40, "site2", site2)
	subnet := func(cidr string, name, client, site, vlanID any) string {
		id := f.ID()
		exec(`INSERT INTO subnet (id, organization_id, client_id, site_id, cidr, name, vlan_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, f.OrgA, client, site, cidr, name, vlanID)
		return id
	}
	s1 := subnet("10.98.0.0/24", "lan", f.Client1, nil, v10)
	s2 := subnet("10.98.1.0/24", nil, nil, nil, nil)
	sC2 := subnet("10.98.2.0/24", "foreign", f.Client2, nil, nil)
	sVC2 := subnet("10.98.3.0/24", "vlan-foreign", f.Client1, nil, vC2)
	sS2 := subnet("10.98.4.0/24", "site2", f.Client1, site2, nil)
	nic := func(ciID string, vlanID any) string {
		id := f.ID()
		exec(`INSERT INTO network_interface (id, organization_id, ci_id, name, vlan_id) VALUES ($1, $2, $3, 'eth0', $4)`, id, f.OrgA, ciID, vlanID)
		return id
	}
	nicLeaf, nicHub, nicSw, nicOther := nic(leaf, v20), nic(hub, vC2), nic(sw, nil), nic(other, vS2)
	for _, a := range []struct {
		subnet, nic any
		address     string
	}{
		{s1, nicLeaf, "10.98.0.1"}, {s1, nicLeaf, "10.98.0.2"}, {nil, nicLeaf, "192.0.2.98"},
		{s2, nicHub, "10.98.1.1"}, {sC2, nicHub, "10.98.2.1"}, {sVC2, nicHub, "10.98.3.1"}, {sS2, nicHub, "10.98.4.1"},
		{s2, nil, "10.98.1.9"}, {s1, nicSw, "10.98.0.3"}, {s1, nicOther, "10.98.0.4"},
	} {
		exec(`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address) VALUES ($1, $2, $3, $4)`, f.OrgA, a.subnet, a.nic, a.address)
	}

	counter := &queryCounter{}
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(pool), relationship.NewPGRepository(pool)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(pool)).WithCITypes(citype.NewPGRepository(pool)).
		WithTypeSubtrees(citype.NewPGRepository(pool)).WithNetworkProjection(ipam.NewPGRepository(pool)).RegisterRoutes(mux)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	site1Scope := database.OrgWideScope(f.OrgA, f.User)
	site1Scope.Sites = database.ScopeIDs(site1)
	as := func(scope *database.TenantScope, ipamRead bool) context.Context {
		perms := []identity.Permission{identity.PermTopologyRead}
		if ipamRead {
			perms = append(perms, identity.PermIPAMRead)
		}
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, scope), tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: f.User})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: scope.OrgID, Permissions: perms})
	}
	call := func(ctx context.Context, path string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w.Code, w.Body.String()
	}
	sub, vl := topology.SubnetNodeID, topology.VLANNodeID
	// graph reads the topology and returns its projected nodes and all
	// edges other than powered_by as "from>to/type".
	graph := func(scope *database.TenantScope, path string) (nodes, edges []string) {
		t.Helper()
		code, body := call(as(scope, true), path)
		var g topology.Graph
		if code != http.StatusOK || json.Unmarshal([]byte(body), &g) != nil {
			t.Fatalf("GET %s: %d %.300s", path, code, body)
		}
		for i := range g.Nodes {
			if n := &g.Nodes[i]; n.NodeKind != "" {
				nodes = append(nodes, n.ID)
				if n.CIType != "" || n.Status != "" {
					t.Errorf("projected node with CI fields: %+v", n)
				}
			}
		}
		for _, e := range g.Edges {
			if e.RelType == "powered_by" {
				continue
			}
			if e.Source != relationship.ProjectionSource || len(e.ID) != 36 {
				t.Errorf("edge %+v: source or id", e)
			}
			edges = append(edges, e.SourceCIID+">"+e.TargetCIID+"/"+e.RelType)
		}
		slices.Sort(nodes)
		slices.Sort(edges)
		return nodes, edges
	}
	sorted := func(list ...string) []string { slices.Sort(list); return list }

	t.Run("nodes and edges", func(t *testing.T) {
		nodes, edges := graph(&orgWide, "/api/v1/topology?include=network&depth=1&root_ci_id="+hub)
		wantNodes := sorted(sub(s1), sub(s2), sub(sC2), sub(sVC2), sub(sS2), vl(v10), vl(v20), vl(vC2))
		wantEdges := sorted(
			hub+">"+sub(s2)+"/in_subnet", hub+">"+sub(sC2)+"/in_subnet", hub+">"+sub(sVC2)+"/in_subnet", hub+">"+sub(sS2)+"/in_subnet",
			hub+">"+vl(vC2)+"/in_vlan", sub(sVC2)+">"+vl(vC2)+"/in_vlan",
			leaf+">"+sub(s1)+"/in_subnet", leaf+">"+vl(v20)+"/in_vlan", sub(s1)+">"+vl(v10)+"/in_vlan")
		if !slices.Equal(nodes, wantNodes) || !slices.Equal(edges, wantEdges) {
			t.Errorf("org-wide:\nnodes %v\nwant  %v\nedges %v\nwant  %v", nodes, wantNodes, edges, wantEdges)
		}
		// The ids are stable from read to read.
		_, again := graph(&orgWide, "/api/v1/topology?include=network&depth=1&root_ci_id="+hub)
		if !slices.Equal(edges, again) {
			t.Errorf("second read: %v", again)
		}
	})

	t.Run("type filter", func(t *testing.T) {
		nodes, edges := graph(&orgWide, "/api/v1/topology?include=network&ci_type="+switchType+"&site_id="+site1)
		if want := sorted(sub(s1), vl(v10)); !slices.Equal(nodes, want) ||
			!slices.Equal(edges, sorted(sw+">"+sub(s1)+"/in_subnet", sub(s1)+">"+vl(v10)+"/in_vlan")) {
			t.Errorf("switches: nodes %v, edges %v", nodes, edges)
		}
		nodes, _ = graph(&orgWide, "/api/v1/topology?include=network&depth=1&root_ci_id="+hub+"&ci_type="+switchType)
		if want := sorted(sub(s2), sub(sC2), sub(sVC2), sub(sS2), vl(vC2)); !slices.Equal(nodes, want) {
			t.Errorf("from hub, switches only: %v, want the network of the root %v", nodes, want)
		}
	})

	t.Run("client and site scope", func(t *testing.T) {
		nodes, edges := graph(&client1, "/api/v1/topology?include=network&depth=1&root_ci_id="+hub)
		for _, hidden := range []string{sub(sC2), vl(vC2)} {
			if slices.Contains(nodes, hidden) || strings.Contains(strings.Join(edges, " "), hidden) {
				t.Errorf("client 1 sees %s: %v %v", hidden, nodes, edges)
			}
		}
		if !slices.Contains(nodes, sub(sVC2)) || !slices.Contains(nodes, sub(sS2)) {
			t.Errorf("client 1 lacks its subnets: %v", nodes)
		}
		nodes, edges = graph(&site1Scope, "/api/v1/topology?include=network&depth=1&root_ci_id="+hub)
		for _, hidden := range []string{sub(sS2), vl(vC2)} {
			if slices.Contains(nodes, hidden) || strings.Contains(strings.Join(edges, " "), hidden) {
				t.Errorf("site 1 sees %s: %v %v", hidden, nodes, edges)
			}
		}
		nodes, _ = graph(&site1Scope, "/api/v1/topology?include=network&root_ci_id="+other)
		if slices.Contains(nodes, vl(vS2)) || !slices.Contains(nodes, sub(s1)) {
			t.Errorf("site 1 from other: %v, want s1 without the VLAN of site 2", nodes)
		}
	})

	t.Run("include and permission", func(t *testing.T) {
		for _, path := range []string{"/api/v1/topology?include=networks", "/api/v1/topology?include=network,services",
			"/api/v1/impact/" + hub + "?include=network,subnets", "/api/v1/impact?include=x&ci_ids=" + hub} {
			if code, body := call(as(&orgWide, true), path); code != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"include"`) {
				t.Errorf("%s: %d %s", path, code, body)
			}
		}
		for _, path := range []string{"/api/v1/topology?include=network", "/api/v1/impact/" + hub + "?include=network",
			"/api/v1/impact?include=network&ci_ids=" + hub} {
			if code, body := call(as(&orgWide, false), path); code != http.StatusForbidden {
				t.Errorf("%s without ipam:read: %d %s", path, code, body)
			}
		}
	})

	t.Run("impact", func(t *testing.T) {
		type answer struct {
			Count    int                    `json:"count"`
			Impacted []topology.ImpactNode  `json:"impacted"`
			Summary  topology.ImpactSummary `json:"summary"`
		}
		for scope, want := range map[*database.TenantScope][]string{
			// Projected nodes by kind and name: subnets 10.98.1.0/24 (s2),
			// foreign, lan (s1), site2, vlan-foreign; VLANs 10, 20, 30.
			&orgWide: {leaf + "//1/" + hub,
				sub(s2) + "/subnet/1/" + hub, sub(sC2) + "/subnet/1/" + hub, sub(s1) + "/subnet/2/" + leaf, sub(sS2) + "/subnet/1/" + hub, sub(sVC2) + "/subnet/1/" + hub,
				vl(v10) + "/vlan/3/" + sub(s1), vl(v20) + "/vlan/2/" + leaf, vl(vC2) + "/vlan/1/" + hub},
			&client1: {leaf + "//1/" + hub,
				sub(s2) + "/subnet/1/" + hub, sub(s1) + "/subnet/2/" + leaf, sub(sS2) + "/subnet/1/" + hub, sub(sVC2) + "/subnet/1/" + hub,
				vl(v10) + "/vlan/3/" + sub(s1), vl(v20) + "/vlan/2/" + leaf},
		} {
			for _, path := range []string{"/api/v1/impact/" + hub + "?include=network", "/api/v1/impact?include=network,services&ci_ids=" + hub} {
				code, body := call(as(scope, true), path)
				var a answer
				if code != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil {
					t.Fatalf("%s: %d %.300s", path, code, body)
				}
				var got []string
				for i := range a.Impacted {
					n := &a.Impacted[i]
					got = append(got, n.ID+"/"+n.NodeKind+"/"+string(rune('0'+n.Hops))+"/"+n.Via.CIID)
					if n.NodeKind != "" && (!n.Heuristic || n.Path[len(n.Path)-1].Category != "network") {
						t.Errorf("%s: projected %+v", path, n)
					}
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s (clients %v):\n%v\nwant\n%v", path, scope.Clients, got, want)
				}
				if a.Count != 1 || a.Summary.Impacted != 1 || a.Summary.Projected == nil || *a.Summary.Projected != len(want)-1 {
					t.Errorf("%s: count %d, summary %+v", path, a.Count, a.Summary)
				}
			}
		}
	})

	t.Run("explain", func(t *testing.T) {
		// Enough interfaces and addresses that a scan of the whole table
		// costs more than the index (WP-436 reads the real plan).
		execT(t, `WITH c AS (
				INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
				SELECT $1, $2, t.id, 'net-fill-' || g, 'active' FROM generate_series(1, 3000) g
				JOIN ci_type t ON t.key = 'server' AND t.organization_id IS NULL RETURNING id),
			n AS (INSERT INTO network_interface (organization_id, ci_id, name) SELECT $1, c.id, 'eth' || k FROM c, generate_series(0, 1) k RETURNING id)
			INSERT INTO ip_address (organization_id, interface_id, address)
			SELECT $1, n.id, '10.200.0.0'::inet + row_number() OVER () FROM n`, f.OrgA, f.Client1)
		execT(t, `ANALYZE network_interface; ANALYZE ip_address; ANALYZE ci; ANALYZE subnet; ANALYZE vlan`)
		counter.mu.Lock()
		counter.recorded = nil
		counter.mu.Unlock()
		counter.recording.Store(true)
		if code, body := call(as(&orgWide, true), "/api/v1/impact/"+hub+"?include=network"); code != http.StatusOK {
			t.Fatalf("impact: %d %s", code, body)
		}
		counter.recording.Store(false)
		var links *pgx.TraceQueryStartData
		for i := range counter.recorded {
			if q := &counter.recorded[i]; strings.Contains(q.SQL, "FROM network_interface") && strings.Contains(q.SQL, "JOIN ip_address") {
				links = q
			}
		}
		if links == nil {
			t.Fatalf("no projection statement among %d", len(counter.recorded))
		}
		// The interfaces of the CIs by ci_id: idx_network_interface_ci or
		// the unique index on (ci_id, name), whose prefix the planner
		// takes alike at equal cost; the addresses of the interfaces by
		// idx_ip_address_interface. drop removes the indexes of a table
		// for the negative case.
		indexes := map[string][]string{
			"network_interface": {"idx_network_interface_ci", "network_interface_ci_name_key"},
			"ip_address":        {"idx_ip_address_interface"},
		}
		drop := map[string][]string{
			"network_interface": {"DROP INDEX idx_network_interface_ci", "ALTER TABLE network_interface DROP CONSTRAINT network_interface_ci_name_key"},
			"ip_address":        {"DROP INDEX idx_ip_address_interface"},
		}
		// problem names each table the plan reads without one of its
		// indexes.
		problem := func(plan []byte) string {
			var missing []string
			for table, group := range indexes {
				if !slices.ContainsFunc(group, func(index string) bool { return planUsesIndex(t, plan, index) }) {
					missing = append(missing, table+" without "+strings.Join(group, " or "))
				}
			}
			slices.Sort(missing)
			return strings.Join(missing, ", ")
		}
		plan := explainAsApp(t, f.App, &orgWide, links)
		if p := problem(plan); p != "" {
			t.Errorf("projection reads %s: %s", p, compactPlan(t, plan))
		}
		t.Logf("projection plan: %s", compactPlan(t, plan))
		// Negative case: without the index the check names it.
		for table, group := range indexes {
			without := explainWithout(t, f.Admin, f.OrgA, links, drop[table]...)
			if p := problem(without); p != table+" without "+strings.Join(group, " or ") {
				t.Errorf("without %v the check reports %q", group, p)
			}
			t.Logf("without %v: %s", group, compactPlan(t, without))
		}
	})

	t.Run("limit", func(t *testing.T) {
		// 1,999 routers, one of them in s1: with include=network s1 and v10
		// make 2,001 nodes.
		var routerType string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'router' AND organization_id IS NULL`).Scan(&routerType); err != nil {
			t.Fatal(err)
		}
		execT(t, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
			SELECT $1, $2, $3, 'net-router-' || g, 'active' FROM generate_series(1, 1999) g`, f.OrgA, f.Client1, routerType)
		execT(t, `WITH n AS (INSERT INTO network_interface (organization_id, ci_id, name)
				SELECT $1, id, 'eth0' FROM ci WHERE organization_id = $1 AND name = 'net-router-1' RETURNING id)
			INSERT INTO ip_address (organization_id, subnet_id, interface_id, address) SELECT $1, $2, n.id, '10.98.0.77' FROM n`, f.OrgA, s1)
		path := "/api/v1/topology?ci_type=" + routerType
		if code, body := call(as(&orgWide, true), path); code != http.StatusOK {
			t.Errorf("1,999 routers: %d %.200s", code, body)
		}
		code, body := call(as(&orgWide, true), path+"&include=network")
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, "include") || !strings.Contains(body, "2001 nodes") {
			t.Errorf("1,999 routers with their network: %d %.300s", code, body)
		}
	})
}

// explainAsApp runs EXPLAIN (ANALYZE, FORMAT JSON) of a captured statement
// as the application role under the tenant scope (the plan frame of
// WP-436).
func explainAsApp(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt *pgx.TraceQueryStartData) []byte {
	t.Helper()
	var plan []byte
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.SQL, stmt.Args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain %s: %v", stmt.SQL, err)
	}
	return plan
}

// explainWithout is explainAsApp for an org-wide principal of orgID after
// the drops (of indexes or constraints), in a transaction of the admin pool
// that is rolled back (the indexes stay).
func explainWithout(t *testing.T, admin *pgxpool.Pool, orgID string, stmt *pgx.TraceQueryStartData, drops ...string) []byte {
	t.Helper()
	ctx := context.Background()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stmts := append(append([]string{"SET LOCAL lock_timeout = '30s'"}, drops...),
		"SET LOCAL ROLE "+pgx.Identifier{database.DefaultAppRole}.Sanitize())
	for _, s := range stmts {
		if _, err = tx.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
		set_config('app.client_scope', '', true), set_config('app.site_scope', '', true), set_config('app.team_scope', '', true)`, orgID); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.SQL, stmt.Args...).Scan(&plan); err != nil {
		t.Fatalf("explain without %v: %v", drops, err)
	}
	return plan
}
