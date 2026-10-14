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

type startKey struct{}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	q := strings.Join(strings.Fields(d.SQL), " ")
	if len(q) > 90 {
		q = q[:90]
	}
	return context.WithValue(ctx, startKey{}, [2]any{time.Now(), q})
}

func (c *queryCounter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	v, _ := ctx.Value(startKey{}).([2]any)
	if v[0] != nil && time.Since(v[0].(time.Time)) > time.Millisecond {
		fmt.Printf("SQL %8.2fms %s\n", float64(time.Since(v[0].(time.Time)).Microseconds())/1000, v[1])
	}
}

// TestTopologyQueryBudget covers ABN-02 and NFR-10 (WP-204) for the
// topology and the impact analysis against PostgreSQL: a hub with 400
// neighbours costs the same number of statements as one with 20 (no N+1
// per node), and the edge lookups by source and target use indexes. Beyond
// a frontier of 1,000 nodes the walk reads all edges of the organization
// at once (WP-335): a hub with 3,000 neighbours costs the same as one with
// 1,200; that regime has a fixed statement more than the level regime.
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
	if _, err := f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship`); err != nil {
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
		name string
		path func(id string) string
	}{
		{"topology", func(id string) string { return "/api/v1/topology?root_ci_id=" + id + "&depth=2" }},
		{"impact", func(id string) string { return "/api/v1/impact/" + id }},
	} {
		counts := map[int]int64{}
		for _, n := range sizes {
			counts[n] = get(c.path(hubs[n]), n)
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
		t.Logf("%s: %d statements per level read, %d with the organization read", c.name, counts[400], counts[3000])
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
