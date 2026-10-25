package topology_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// queryCounter counts the statements a pool sends (ABN-02).
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// ladderDepth is the deepest topology and impact (maxDepth).
const ladderDepth = 20

// TestTopologyQueryBudget covers ABN-02 and NFR-10 (WP-204) for the
// topology and the impact analysis against PostgreSQL: a hub with 400
// neighbours costs the same number of statements as one with 20 (no N+1
// per node), and the edge lookups by source and target use indexes. Beyond
// a frontier of 1,000 nodes the walk reads all edges of the organization
// at once (WP-335): a hub with 3,000 neighbours costs the same as one with
// 1,200; that regime has a fixed statement more than the level regime.
// Within a regime the count depends on the depth only: 40 chains of 20
// levels cost the same as one. The topology without a root is covered by
// checkTopologyWithoutRoot (WP-602).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTopologyQueryBudget(t *testing.T) {
	f := scopetest.Seed(t, "c6")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	hub := func(name string, leaves int) string {
		t.Helper()
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `WITH leaf AS (
				INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
				SELECT $1, $2, $3, $4 || '-leaf-' || n, 'active' FROM generate_series(1, $5) AS n RETURNING id)
			INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			SELECT $1, leaf.id, $6, 'powered_by' FROM leaf`, f.OrgA, f.Client1, serverType, name, leaves, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sizes := []int{20, 400, 1200, 3000}
	hubs := map[int]string{}
	for _, n := range sizes {
		hubs[n] = hub(fmt.Sprintf("budget-%d", n), n)
	}
	small := hubs[20]
	// ladder hangs width chains of ladderDepth CIs below a root, each CI
	// hosted_on the one a level up: a deep graph that stays in the level
	// regime.
	ladder := func(name string, width int) string {
		t.Helper()
		root := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
			SELECT md5($1 || $4 || l || '/' || w)::uuid, $1::uuid, $2, $3, $4 || '-' || l || '-' || w, 'active'
			FROM generate_series(1, $6::int) AS l, generate_series(1, $5::int) AS w`,
			f.OrgA, f.Client1, serverType, name, width, ladderDepth); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			SELECT $1::uuid, md5($1 || $2 || l || '/' || w)::uuid,
				CASE WHEN l = 1 THEN $3::uuid ELSE md5($1 || $2 || (l - 1) || '/' || w)::uuid END, 'hosted_on'
			FROM generate_series(1, $5::int) AS l, generate_series(1, $4::int) AS w`,
			f.OrgA, name, root, width, ladderDepth); err != nil {
			t.Fatal(err)
		}
		return root
	}
	ladders := map[int]string{1: ladder("budget-ladder-1", 1), 40: ladder("budget-ladder-40", 40)}

	cfg := f.App.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Before the ANALYZE below: the topology without a root answers on the
	// statistics from before its bulk inserts.
	t.Run("without root", func(t *testing.T) { checkTopologyWithoutRoot(t, f, pool, counter) })

	if _, err := f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship`); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(pool), relationship.NewPGRepository(pool)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(pool)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	get := func(path string, want int) int64 {
		t.Helper()
		before := counter.n.Load()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct {
			Nodes    []json.RawMessage `json:"nodes"`
			Impacted []json.RawMessage `json:"impacted"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if got := len(body.Nodes) + len(body.Impacted); got < want {
			t.Errorf("GET %s: %d nodes, want at least %d", path, got, want)
		}
		return counter.n.Load() - before
	}

	// Warm the pool: the first connection switches its role.
	get("/api/v1/topology?root_ci_id="+small+"&depth=1", 20)
	for _, c := range []struct {
		name     string
		hubDepth int
		path     func(id string, depth int) string
	}{
		{"topology", 2, func(id string, depth int) string {
			return fmt.Sprintf("/api/v1/topology?root_ci_id=%s&depth=%d", id, depth)
		}},
		{"impact", ladderDepth, func(id string, depth int) string {
			return fmt.Sprintf("/api/v1/impact/%s?depth=%d", id, depth)
		}},
	} {
		counts := map[int]int64{}
		for _, n := range sizes {
			counts[n] = get(c.path(hubs[n], c.hubDepth), n)
			const budget = 40
			if counts[n] > budget {
				t.Errorf("%s: %d statements for %d neighbours, budget %d", c.name, counts[n], n, budget)
			}
		}
		if counts[20] != counts[400] {
			t.Errorf("%s: %d statements for 20 neighbours, %d for 400 (N+1)", c.name, counts[20], counts[400])
		}
		if counts[1200] != counts[3000] {
			t.Errorf("%s: %d statements for 1,200 neighbours, %d for 3,000 (organization read)", c.name, counts[1200], counts[3000])
		}
		// Depth 20 in the level regime: the count grows with the depth,
		// by at most two statements per level (edges, then the CIs they
		// reach), and not with the width.
		narrow, wide := get(c.path(ladders[1], ladderDepth), ladderDepth), get(c.path(ladders[40], ladderDepth), 40*ladderDepth)
		if narrow != wide {
			t.Errorf("%s at depth 20: %d statements for one chain, %d for 40 (N+1)", c.name, narrow, wide)
		}
		if deep := counts[400] + 2*ladderDepth; wide > deep {
			t.Errorf("%s at depth 20: %d statements, budget %d", c.name, wide, deep)
		}
		t.Logf("%s: %d statements per level read, %d with the organization read, %d at depth 20", c.name, counts[400], counts[3000], wide)
	}

	for name, q := range map[string]string{
		"by source": `SELECT id FROM ci_relationship WHERE organization_id = $1 AND source_ci_id = $2`,
		"by target": `SELECT id FROM ci_relationship WHERE organization_id = $1 AND target_ci_id = $2`,
	} {
		var plan []byte
		scope := database.OrgWideScope(f.OrgA, "")
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+q, f.OrgA, small).Scan(&plan)
		}); err != nil {
			t.Fatalf("explain %s: %v", name, err)
		}
		if seqScan(t, plan, "ci_relationship") {
			t.Errorf("edges %s read ci_relationship with a sequential scan: %s", name, plan)
		}
	}
}

// perNodeEdges hides the batch edge read (relationship.EdgeReader) of the
// PostgreSQL repository: the topology without a root then lists the edges
// of each CI, the path before WP-602.
type perNodeEdges struct{ relationship.Repository }

// checkTopologyWithoutRoot covers NFR-02, IMP-06 and ABN-02 (WP-602) for the
// topology without a root against PostgreSQL. Every answer is taken right
// after the bulk inserts, without ANALYZE:
//   - 2,000 nodes cost at most 10 statements and 20 nodes one fewer: the
//     edges of the node set are one statement (and one for the visibility
//     of their CIs), not one read per node. The 20 CIs end the page of the
//     CI list, so it takes their count from the page and skips its COUNT
//     (WP-605); the full page of 2,000 counts.
//   - 2,000 nodes answer within 3 s (NFR-02).
//   - Graph without a root, the source of the BFF query topology, sends the
//     same statements and returns the same graph.
//
// Each answer, for the organization, a client-1 and a site-1 principal,
// then equals the per-node read (the path before WP-602) on the same data:
// the same nodes and edges, the edges ordered by id. It holds the stored
// edges and the projected containment between visible CIs of the set as the
// pattern of the data predicts, and no edge to a CI outside the set, a
// deleted CI or a location (located_in).
func checkTopologyWithoutRoot(t *testing.T, f *scopetest.Fixture, pool *pgxpool.Pool, counter *queryCounter) {
	bg := context.Background()
	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "rootless "+s); err != nil {
			t.Fatal(err)
		}
	}
	outside := f.CI(t, f.OrgA, f.Client1, "rootless-outside")
	// graph inserts n CIs of the global type key: every third of client 2,
	// every third org-wide, the others of client 1 at site 1 or site 2. CI i
	// is connected_to i+1, every tenth depends_on the first, every 17th is
	// also powered_by i+1; every 13th runs_on a CI of another type, every
	// 19th depends_on a deleted CI of the type. Every seventh is contained
	// in i-1, but every eleventh in the other CI and every 23rd in a deleted
	// one. It returns the id of the type.
	graph := func(key string, n int) string {
		t.Helper()
		var typeID string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&typeID); err != nil {
			t.Fatal(err)
		}
		prefix := "rootless-" + key
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status)
				SELECT md5($1 || $2 || i)::uuid, $1::uuid, CASE i % 3 WHEN 0 THEN $4::uuid WHEN 1 THEN $3::uuid END,
					CASE WHEN i % 3 = 1 THEN CASE WHEN i % 2 = 1 THEN $5::uuid ELSE $6::uuid END END,
					$7::uuid, $2 || '-' || i, 'active'
				FROM generate_series(1, $8::int) AS i
				UNION ALL
				SELECT md5($1 || $2 || 'deleted' || k)::uuid, $1::uuid, $3::uuid, NULL, $7::uuid, $2 || '-deleted-' || k, 'active'
				FROM generate_series(0, 2) AS k`,
				[]any{f.OrgA, prefix, f.Client1, f.Client2, site1, site2, typeID, n}},
			{`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
				SELECT $1::uuid, md5($1 || $2 || e.src)::uuid, COALESCE(e.other, md5($1 || $2 || e.dst)::uuid), e.rel FROM (
					SELECT i AS src, (i + 1)::text AS dst, NULL::uuid AS other, 'connected_to' AS rel FROM generate_series(1, $3::int - 1) AS i
					UNION ALL SELECT i, '1', NULL, 'depends_on' FROM generate_series(2, $3::int) AS i WHERE i % 10 = 0
					UNION ALL SELECT i, (i + 1)::text, NULL, 'powered_by' FROM generate_series(1, $3::int - 1) AS i WHERE i % 17 = 0
					UNION ALL SELECT i, NULL, $4::uuid, 'runs_on' FROM generate_series(1, $3::int) AS i WHERE i % 13 = 0
					UNION ALL SELECT i, 'deleted' || (i % 3), NULL, 'depends_on' FROM generate_series(1, $3::int) AS i WHERE i % 19 = 0) e`,
				[]any{f.OrgA, prefix, n, outside}},
			{`UPDATE ci SET parent_ci_id = CASE WHEN g.i % 11 = 0 THEN $4::uuid
					WHEN g.i % 23 = 0 THEN md5($1 || $2 || 'deleted0')::uuid ELSE md5($1 || $2 || (g.i - 1))::uuid END
				FROM generate_series(2, $3::int) AS g(i)
				WHERE (g.i % 7 = 0 OR g.i % 11 = 0 OR g.i % 23 = 0) AND ci.id = md5($1 || $2 || g.i)::uuid`,
				[]any{f.OrgA, prefix, n, outside}},
			{`UPDATE ci SET deleted_at = now() WHERE id IN (SELECT md5($1 || $2 || 'deleted' || k)::uuid FROM generate_series(0, 2) AS k)`,
				[]any{f.OrgA, prefix}},
		} {
			if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
				t.Fatalf("seed %s: %v", prefix, err)
			}
		}
		return typeID
	}
	small, large := graph("switch", 20), graph("router", topology.MaxTopologyNodes)

	mux := chi.NewRouter()
	h := topology.NewHandler(ci.NewPGRepository(pool), relationship.NewPGRepository(pool))
	h.RegisterRoutes(mux)
	perNode := topology.NewHandler(ci.NewPGRepository(pool), perNodeEdges{relationship.NewPGRepository(pool)})
	as := func(scope *database.TenantScope) context.Context {
		return tenant.WithTenant(database.ContextWithTenantScope(bg, scope),
			tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: f.User})
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	get := func(path string) (topology.Graph, int64, time.Duration) {
		t.Helper()
		before, start := counter.n.Load(), time.Now()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(as(&orgWide)))
		elapsed, statements := time.Since(start), counter.n.Load()-before
		var g topology.Graph
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &g) != nil {
			t.Fatalf("GET %s: %d %.300s", path, w.Code, w.Body.String())
		}
		return g, statements, elapsed
	}

	// Statements and time: the first answers after the inserts.
	smallGraph, smallCount, _ := get("/api/v1/topology?ci_type=" + small)
	largeGraph, largeCount, elapsed := get("/api/v1/topology?ci_type=" + large)
	if len(smallGraph.Nodes) != 20 || len(largeGraph.Nodes) != topology.MaxTopologyNodes {
		t.Fatalf("topology without a root: %d and %d nodes, want 20 and %d", len(smallGraph.Nodes), len(largeGraph.Nodes), topology.MaxTopologyNodes)
	}
	const budget = 10
	if smallCount+1 != largeCount || largeCount > budget {
		t.Errorf("topology without a root: %d statements for 20 nodes, %d for 2,000, want one more for the COUNT of the full page, at most %d (N+1)", smallCount, largeCount, budget)
	}
	if elapsed > 3*time.Second {
		t.Errorf("topology without a root: 2,000 nodes in %v without ANALYZE, budget 3 s (NFR-02)", elapsed)
	}
	t.Logf("topology without a root: %d statements for 20 nodes, %d for 2,000; 2,000 nodes and %d edges in %v without ANALYZE",
		smallCount, largeCount, len(largeGraph.Edges), elapsed.Round(time.Millisecond))

	// The source of the BFF query topology.
	before := counter.n.Load()
	viaGraph, err := h.Graph(as(&orgWide), f.OrgA, "", topology.ParseDepth(""), &ci.FilterParams{TypeID: large})
	if statements := counter.n.Load() - before; err != nil || statements != largeCount || !reflect.DeepEqual(viaGraph, largeGraph) {
		t.Errorf("Graph without a root: %d statements, %v, equal to GET /topology %v", statements, err, reflect.DeepEqual(viaGraph, largeGraph))
	}

	// The answers under each scope, before any ANALYZE. CI i of the pattern
	// is visible to the client-1 principal unless it belongs to client 2,
	// to the site-1 principal unless it is at site 2.
	client1, site1Scope := database.OrgWideScope(f.OrgA, f.User), database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	site1Scope.Sites = database.ScopeIDs(site1)
	cases := []struct {
		name    string
		scope   *database.TenantScope
		typeID  string
		n       int
		visible func(i int) bool
		answer  topology.Graph
	}{
		{"organization, 20 CIs", &orgWide, small, 20, func(int) bool { return true }, smallGraph},
		{"organization", &orgWide, large, topology.MaxTopologyNodes, func(int) bool { return true }, largeGraph},
		{"client 1", &client1, large, topology.MaxTopologyNodes, func(i int) bool { return i%3 != 0 }, topology.Graph{}},
		{"site 1", &site1Scope, large, topology.MaxTopologyNodes, func(i int) bool { return i%3 != 1 || i%2 == 1 }, topology.Graph{}},
	}
	for i := range cases {
		c := &cases[i]
		if c.answer.Nodes != nil {
			continue
		}
		start := time.Now()
		if c.answer, err = h.Graph(as(c.scope), f.OrgA, "", topology.ParseDepth(""), &ci.FilterParams{TypeID: c.typeID}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		t.Logf("%s: %d nodes and %d edges in %v without ANALYZE", c.name, len(c.answer.Nodes), len(c.answer.Edges), time.Since(start).Round(time.Millisecond))
	}

	// The per-node reference reads after an ANALYZE only: on the
	// statistics of a fresh database it needs about 100 ms per CI, minutes
	// for 2,000 CIs (the defect of WP-602).
	if _, err := f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship`); err != nil {
		t.Fatal(err)
	}
	for i := range cases {
		c := &cases[i]
		ctx := as(c.scope)
		start := time.Now()
		old, err := perNode.Graph(ctx, f.OrgA, "", topology.ParseDepth(""), &ci.FilterParams{TypeID: c.typeID})
		if err != nil {
			t.Fatalf("%s, per node: %v", c.name, err)
		}
		t.Logf("%s, per node: %v after ANALYZE", c.name, time.Since(start).Round(time.Millisecond))
		if !reflect.DeepEqual(c.answer, old) {
			t.Errorf("%s: %d nodes and %d edges, the per-node read %d and %d", c.name, len(c.answer.Nodes), len(c.answer.Edges), len(old.Nodes), len(old.Edges))
		}
		if again, err := h.Graph(ctx, f.OrgA, "", topology.ParseDepth(""), &ci.FilterParams{TypeID: c.typeID}); err != nil || !reflect.DeepEqual(again, c.answer) {
			t.Errorf("%s: the answer after ANALYZE differs (%v)", c.name, err)
		}
		if !slices.IsSortedFunc(c.answer.Edges, func(a, b topology.Edge) int { return strings.Compare(a.ID, b.ID) }) {
			t.Errorf("%s: edges not ordered by id", c.name)
		}

		// The expectation from the pattern: the edges between two visible
		// CIs of the type, stored and projected (contains, parent → child).
		nodes, stored, contained := 0, 0, 0
		for i := 1; i <= c.n; i++ {
			if !c.visible(i) {
				continue
			}
			nodes++
			if i < c.n && c.visible(i+1) {
				stored++ // connected_to
				if i%17 == 0 {
					stored++ // powered_by
				}
			}
			if i%10 == 0 && c.visible(1) {
				stored++ // depends_on the first
			}
			if i%7 == 0 && i%11 != 0 && i%23 != 0 && c.visible(i-1) {
				contained++
			}
		}
		ids := map[string]bool{}
		for _, n := range c.answer.Nodes {
			ids[n.ID] = true
		}
		gotStored, gotContained := 0, 0
		for _, e := range c.answer.Edges {
			if !ids[e.SourceCIID] || !ids[e.TargetCIID] {
				t.Errorf("%s: edge %+v leaves the node set", c.name, e)
			}
			if e.Source == relationship.ProjectionSource && e.RelType == "contains" {
				gotContained++
			} else {
				gotStored++
			}
		}
		if len(c.answer.Nodes) != nodes || gotStored != stored || gotContained != contained {
			t.Errorf("%s: %d nodes, %d stored and %d projected edges, want %d, %d and %d",
				c.name, len(c.answer.Nodes), gotStored, gotContained, nodes, stored, contained)
		}
	}
}

// seqScan reports whether a JSON plan reads table with a sequential scan.
func seqScan(t *testing.T, plan []byte, table string) bool {
	t.Helper()
	var nodes []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	var walk func(node map[string]any) bool
	walk = func(node map[string]any) bool {
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == table {
			return true
		}
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok && walk(child) {
				return true
			}
		}
		return false
	}
	for _, n := range nodes {
		if walk(n.Plan) {
			return true
		}
	}
	return false
}

// loadGraph inserts the graph of the load seed (cmd/seed: CIs and
// relationships for n objects) into orgID: two switches connected to each
// other, two PDUs, and servers each connected_to a switch and powered_by a
// PDU. Like the seed, the first 40 CIs and every even one belong to the
// first client and site, the others to the second; the site is set as the
// location, from which the database derives site_id (the seed's site_id
// without a location ends up empty), so the full entries are the larger
// ones. It returns the core switch sw-00001.
func loadGraph(t *testing.T, f *scopetest.Fixture, orgID string, n int, clients, sites [2]string) string {
	t.Helper()
	bg := context.Background()
	if _, err := f.Admin.Exec(bg, `
		INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status, management_ip)
		SELECT md5($1 || '/ci/' || i)::uuid, $1::uuid,
			CASE WHEN i <= 40 OR i % 2 = 0 THEN $3::uuid ELSE $4::uuid END,
			CASE WHEN i <= 40 OR i % 2 = 0 THEN $5::uuid ELSE $6::uuid END,
			t.id, CASE WHEN i <= 2 THEN 'sw-' WHEN i <= 4 THEN 'pdu-' ELSE 'srv-' END || lpad(i::text, 5, '0'), 'active',
			('10.200.' || (i / 250) || '.' || (i % 250 + 1))::inet
		FROM generate_series(1, $2::int) AS i
		JOIN ci_type t ON t.organization_id IS NULL
			AND t.key = CASE WHEN i <= 2 THEN 'switch' WHEN i <= 4 THEN 'pdu' ELSE 'server' END`,
		orgID, n, clients[0], clients[1], sites[0], sites[1]); err != nil {
		t.Fatalf("load cis: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `
		INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		SELECT $1::uuid, md5($1 || '/ci/' || e.src)::uuid, md5($1 || '/ci/' || e.dst)::uuid, e.rel, 'discovery'
		FROM (SELECT i AS src, 1 + i % 2 AS dst, 'connected_to' AS rel FROM generate_series(5, $2::int) AS i
			UNION ALL SELECT i, 3 + i % 2, 'powered_by' FROM generate_series(5, $2::int) AS i
			UNION ALL SELECT 1, 2, 'connected_to') e`, orgID, n); err != nil {
		t.Fatalf("load relationships: %v", err)
	}
	var core string
	if err := f.Admin.QueryRow(bg, `SELECT md5($1 || '/ci/1')::uuid::text`, orgID).Scan(&core); err != nil {
		t.Fatal(err)
	}
	return core
}

// TestImpactWorstCase covers NFR-02 and ABN-02 (WP-335) on the worst case
// of the load seed: the impact of the core switch of 10,000 objects
// (depth 20) reaches 9,997 CIs. The compact answer the topology view asks
// for (fields=, API-02) costs at most 250 bytes per impacted CI, the
// statements are those of a graph of 3,000 objects, and the paths of the
// full answer are the ones a client rebuilds from via. The single request
// is timed (best of five); RETICORA_PERF_BUDGET=1 enforces 250 ms.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestImpactWorstCase(t *testing.T) {
	f := scopetest.Seed(t, "f3")
	bg := context.Background()
	clientsB := [2]string{f.ID(), f.ID()}
	for i, c := range clientsB {
		if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, $3, $3)`,
			c, f.OrgB, fmt.Sprintf("scopetest-f3-b%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	sites := func(org string, clients [2]string) [2]string {
		out := [2]string{f.ID(), f.ID()}
		for i, s := range out {
			if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
				s, org, clients[i], fmt.Sprintf("worst case %d", i)); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	clientsA := [2]string{f.Client1, f.Client2}
	core := loadGraph(t, f, f.OrgA, 10000, clientsA, sites(f.OrgA, clientsA))
	coreB := loadGraph(t, f, f.OrgB, 3000, clientsB, sites(f.OrgB, clientsB))
	if _, err := f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship; ANALYZE ci_field_value`); err != nil {
		t.Fatal(err)
	}

	cfg := f.App.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(pool), relationship.NewPGRepository(pool)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(pool)).RegisterRoutes(mux)
	type answer struct {
		statements int64
		elapsed    time.Duration
		body       []byte
		count      int
		impacted   []map[string]json.RawMessage
	}
	get := func(org, path string) answer {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
		before := counter.n.Load()
		w := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		a := answer{elapsed: time.Since(start), statements: counter.n.Load() - before, body: w.Body.Bytes()}
		var body struct {
			Count    int                          `json:"count"`
			Impacted []map[string]json.RawMessage `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(a.body, &body) != nil {
			t.Fatalf("GET %s: %d %.300s", path, w.Code, a.body)
		}
		a.count, a.impacted = body.Count, body.Impacted
		return a
	}
	// The fields the topology view requests (frontend IMPACT_COMPACT_FIELDS).
	const compact = "?depth=20&fields=name,ci_type,status,heuristic,hops,via,relay"

	get(f.OrgB, "/api/v1/impact/"+coreB+compact) // warms the pool
	small := get(f.OrgB, "/api/v1/impact/"+coreB+compact)
	get(f.OrgA, "/api/v1/impact/"+core+compact)
	var best answer
	for i := 0; i < 5; i++ {
		if a := get(f.OrgA, "/api/v1/impact/"+core+compact); i == 0 || a.elapsed < best.elapsed {
			best = a
		}
	}

	// The answer: every CI but the core switch and the two PDUs, at one or
	// two hops, with via and without paths or client and site.
	if best.count != 9997 || len(best.impacted) != 9997 || small.count != 2997 {
		t.Fatalf("impacted %d (%d entries), %d in the 3,000-object graph", best.count, len(best.impacted), small.count)
	}
	for _, entry := range best.impacted {
		keys := make([]string, 0, len(entry))
		for k := range entry {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "ci_type,heuristic,hops,id,name,status,via" {
			t.Fatalf("compact entry with fields %v", keys)
		}
		if hops := string(entry["hops"]); hops != "1" && hops != "2" {
			t.Fatalf("compact entry at %s hops", hops)
		}
	}

	// Statements independent of the graph size; bytes per impacted CI.
	if best.statements != small.statements {
		t.Errorf("impact: %d statements for 10,000 objects, %d for 3,000", best.statements, small.statements)
	}
	perCI := len(best.body) / best.count
	if perCI > 250 {
		t.Errorf("compact impact: %d bytes per impacted CI, budget 250", perCI)
	}

	// The full answer (default, API-01: additive only) keeps path, client
	// and site; every path equals the one rebuilt from via and relay.
	full := get(f.OrgA, "/api/v1/impact/"+core+"?depth=20")
	if full.statements != best.statements || full.count != best.count {
		t.Errorf("full answer: %d statements, %d CIs; compact %d, %d", full.statements, full.count, best.statements, best.count)
	}
	rules := map[string]relationshiptype.Type{}
	for _, typ := range relationshiptype.Catalogue() {
		rules[typ.Key] = typ
	}
	type step struct {
		CIID    string `json:"ci_id"`
		RelType string `json:"rel_type"`
	}
	type entry struct {
		ID        string              `json:"id"`
		ClientID  string              `json:"client_id"`
		SiteID    string              `json:"site_id"`
		Heuristic bool                `json:"heuristic"`
		Hops      int                 `json:"hops"`
		Via       step                `json:"via"`
		Relay     *step               `json:"relay"`
		Path      []topology.PathStep `json:"path"`
	}
	decode := func(raw []map[string]json.RawMessage) map[string]entry {
		out := make(map[string]entry, len(raw))
		for _, r := range raw {
			b, _ := json.Marshal(r)
			var e entry
			if err := json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			out[e.ID] = e
		}
		return out
	}
	viaOf, pathOf := decode(best.impacted), decode(full.impacted)
	for id, e := range pathOf {
		if e.ClientID == "" || e.SiteID == "" || len(e.Path) != e.Hops {
			t.Fatalf("full entry %+v", e)
		}
		target := viaOf[id]
		rebuilt := make([]topology.PathStep, target.Hops)
		to := id
		for i := target.Hops - 1; i >= 0; i-- {
			at := viaOf[to]
			s := at.Via
			if target.Heuristic && at.Relay != nil {
				s = *at.Relay
			}
			rule := rules[s.RelType]
			rebuilt[i] = topology.PathStep{FromCIID: s.CIID, ToCIID: to, RelType: s.RelType, Category: rule.Category,
				Heuristic: rule.ImpactDirection == relationshiptype.ImpactUndirected}
			to = s.CIID
		}
		if !reflect.DeepEqual(rebuilt, e.Path) {
			t.Fatalf("path to %s rebuilt from via %+v, full answer %+v", id, rebuilt, e.Path)
		}
	}

	t.Logf("worst case (10,000 objects, %d impacted): compact %d bytes (%d per CI), best of 5 %v, %d statements; full %d bytes (%d per CI) in %v",
		best.count, len(best.body), perCI, best.elapsed.Round(time.Millisecond), best.statements,
		len(full.body), len(full.body)/full.count, full.elapsed.Round(time.Millisecond))
	if os.Getenv("RETICORA_PERF_BUDGET") == "1" && best.elapsed > 250*time.Millisecond {
		t.Errorf("worst-case impact: %v, budget 250 ms", best.elapsed)
	}
	if !bytes.Contains(full.body, []byte(`"path":[`)) {
		t.Error("the full answer carries no path")
	}
}
