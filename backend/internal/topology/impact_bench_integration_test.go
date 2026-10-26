package topology_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// leanObjects and leanRelationshipsPerCI are the volumes of NFR-01 the load
// job nfr01-volume seeds (cmd/seed -objects 10000 -relationships-per-ci 10).
const (
	leanObjects            = 10000
	leanRelationshipsPerCI = 10
)

// leanAllocBudget bounds the heap allocations of one compact impact request
// of the core switch. Before WP-611 it took 1,000,745 (the edges of the
// organization as five strings each, maps keyed by id strings), after it
// about 215,000.
const leanAllocBudget = 400000

// leanGolden holds the SHA-256 of each answer of TestImpactLeanAgainstPostgres
// as the implementation before WP-611 gave it on the same seed (recorded on
// commit f3d428a with RETICORA_IMPACT_DUMP=<dir>, which writes the compared
// answers there), normalized: the ids of the three CI types replaced by
// their keys (the database assigns them), and the orders that were the
// order of a Go map sorted (the nodes of a topology, the affected CIs,
// clients and sites of a blast radius). Every other byte is compared as it
// is.
var leanGolden = map[string]string{
	"impact compact":             "d1308a57df2a8dbe01beb12d5aaa704d73f4d19fee38abd47868f8cedc7f6e1d", // 2,130,112 bytes
	"impact full":                "c67e32b9e66d6c80f079c7a2c143afbf8c5811fc4653fe55492bda5c5b83904f", // 5,984,782 bytes
	"impact deprecated path":     "c67e32b9e66d6c80f079c7a2c143afbf8c5811fc4653fe55492bda5c5b83904f", // 5,984,782 bytes
	"impact pdu":                 "d391153e2ee16fd98f8d14d77bf9915d1f719ceab2372433850b12b2ef2359e2", // 5,588,218 bytes
	"impact categories and type": "c59b0f6c0076022ea4f1ad47d71b9525193b17e52a6728a1b524c32f244fe0b2", // 6,050,357 bytes
	"impact server depth 2":      "bf2e63a4056f5ee3273f30a10c7f79861f9142fc4a8f2bd75a1a9cc512eb04e7", // 3,448,422 bytes
	"impact two pdus":            "9cf6764c9778a2ec913f1131c5b83115e81e862bf7d9a2b7827b6dc6cebc528c", // 4,992,289 bytes
	"impact client 1":            "bcb2578ceb2b811f0b615ca0e143ebd1fdb76f9cb2f5f5112e1f86619e46f6f9", // 2,618,928 bytes
	"topology depth 1":           "6358e081469093fe06d8404ce339947ff3dc843ea295871bd02ef0c9a9129f74", // 2,192,196 bytes
	"topology depth 3":           "5edeb82c44417456a66950d304401a13114738df9d091cacef419a3cee5e4d7c", // 21,979,384 bytes
	"topology server depth 1":    "c94386587a36c775bfbf54e5b1d7e2d8ac0033eb1386a80d5fe3bd9547e48a39", // 8,053 bytes
	"topology client 1 depth 3":  "ac9cb8008c007ab51765cafe0545db4dc285767712a71f3962b26c4b647814ac", // 7,139,847 bytes
	"neighbors":                  "6358e081469093fe06d8404ce339947ff3dc843ea295871bd02ef0c9a9129f74", // 2,192,196 bytes
	"blast radius pdu":           "0d13f80bae2fdbcb3fe7c412213510857a16be4bafeabcd2e2f7c3c7ab07debe", // 3,805,489 bytes
	"blast radius core":          "d5529651ee069155c387f83b613a0208a01adeccdecbb9aa44480dfe192d6c0b", // 2,378,430 bytes
}

// TestImpactLeanAgainstPostgres covers NFR-02 and IMP-07 (WP-611) on the
// volumes of NFR-01: 10,000 CIs and 100,000 relationships around a core
// switch, as the load seed lays them out (every server connected_to a
// switch and powered_by a PDU, filled up with depends_on between servers),
// plus redundant power for every fifth server, containment projected from
// parent_ci_id, deleted CIs and overrides of name, status and management IP.
// Read through the production repositories as the application role:
//   - The answers are byte-identical to the implementation before WP-611
//     (leanGolden): impact of the core switch compact and full, the
//     deprecated impact path, impact of a PDU (redundancy), with categories
//     and type, of a server at depth 2 (level reads), of two PDUs together,
//     topology at depth 1 and 3, neighbours, blast radius of a PDU and of the
//     core switch, and impact and topology for a client-1 principal (IMP-07).
//   - A compact impact request of the core switch allocates at most
//     leanAllocBudget times (three requests after two to warm up).
//   - With RETICORA_IMPACT_BENCH=1 it measures one impact request of the
//     core switch, compact and full: wall time, CPU time of the process
//     (getrusage), allocations and statements per request
//     (RETICORA_IMPACT_BENCH_N requests each, default 10, after two to warm
//     up); RETICORA_IMPACT_CPUPROFILE=<file> writes a CPU profile of them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestImpactLeanAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "f3")
	bg := context.Background()
	g := seedLeanGraph(t, f, leanObjects, leanRelationshipsPerCI)

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
	as := func(scope *database.TenantScope) context.Context {
		return tenant.WithTenant(database.ContextWithTenantScope(bg, scope),
			tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: f.User})
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	type answer struct {
		body       []byte
		statements int64
	}
	get := func(ctx context.Context, path string) answer {
		t.Helper()
		before := counter.n.Load()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %.300s", path, w.Code, w.Body.String())
		}
		return answer{body: w.Body.Bytes(), statements: counter.n.Load() - before}
	}
	get(as(&orgWide), "/api/v1/topology?root_ci_id="+g.server+"&depth=1") // warms the pool

	const compact = "?depth=20&fields=name,ci_type,status,heuristic,hops,via,relay"
	cases := []struct {
		name  string
		scope *database.TenantScope
		path  string
		norm  func(t *testing.T, body []byte) []byte
	}{
		{"impact compact", &orgWide, "/api/v1/impact/" + g.core + compact, nil},
		{"impact full", &orgWide, "/api/v1/impact/" + g.core + "?depth=20", nil},
		{"impact deprecated path", &orgWide, "/api/v1/topology/cis/" + g.core + "/impact", nil},
		{"impact pdu", &orgWide, "/api/v1/impact/" + g.pdu, nil},
		{"impact categories and type", &orgWide, "/api/v1/impact/" + g.core + "?categories=network,power&rel_type=connected_to&depth=3", nil},
		{"impact server depth 2", &orgWide, "/api/v1/impact/" + g.server + "?depth=2", nil},
		{"impact two pdus", &orgWide, "/api/v1/impact?ci_ids=" + g.pdu + "," + g.otherPDU + "&categories=power", nil},
		{"impact client 1", &client1, "/api/v1/impact/" + g.core + "?depth=20", nil},
		{"topology depth 1", &orgWide, "/api/v1/topology?root_ci_id=" + g.core + "&depth=1", sortedTopology},
		{"topology depth 3", &orgWide, "/api/v1/topology?root_ci_id=" + g.core + "&depth=3", sortedTopology},
		{"topology server depth 1", &orgWide, "/api/v1/topology?root_ci_id=" + g.server + "&depth=1", sortedTopology},
		{"topology client 1 depth 3", &client1, "/api/v1/topology?root_ci_id=" + g.core + "&depth=3", sortedTopology},
		{"neighbors", &orgWide, "/api/v1/topology/cis/" + g.core + "/neighbors", sortedTopology},
		{"blast radius pdu", &orgWide, "/api/v1/cis/" + g.pdu + "/blast-radius", sortedBlastRadius},
		{"blast radius core", &orgWide, "/api/v1/cis/" + g.core + "/blast-radius", sortedBlastRadius},
	}
	dump := os.Getenv("RETICORA_IMPACT_DUMP")
	for _, c := range cases {
		a := get(as(c.scope), c.path)
		body := a.body
		if c.norm != nil {
			body = c.norm(t, body)
		}
		for key, id := range g.types {
			body = bytes.ReplaceAll(body, []byte(`"`+id+`"`), []byte(`"type:`+key+`"`))
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if dump != "" {
			name := strings.ReplaceAll(c.name, " ", "-") + ".json"
			if err := os.WriteFile(filepath.Join(dump, name), body, 0o600); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("\t%q: %q, // %d bytes, %d statements\n", c.name, got, len(body), a.statements)
		}
		t.Logf("%s: %d bytes, %d statements, sha256 %s", c.name, len(body), a.statements, got)
		if want, ok := leanGolden[c.name]; !ok || got != want {
			t.Errorf("%s: sha256 %s, the implementation before WP-611 answered %q", c.name, got, want)
		}
	}

	// measure runs n requests of path after two to warm up and returns the
	// allocations per request; it logs the cost of one request.
	measure := func(name, path string, n int) uint64 {
		t.Helper()
		ctx := as(&orgWide)
		get(ctx, path)
		get(ctx, path)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu := processCPU(t)
		start := time.Now()
		var statements int64
		for i := 0; i < n; i++ {
			statements += get(ctx, path).statements
		}
		wall := time.Since(start)
		used := processCPU(t) - cpu
		runtime.ReadMemStats(&after)
		allocs := (after.Mallocs - before.Mallocs) / uint64(n)
		t.Logf("%s (%d requests): %.1f ms wall, %.1f ms CPU, %d allocs, %.1f MB allocated, %d statements per request",
			name, n, float64(wall.Microseconds())/1000/float64(n), float64(used.Microseconds())/1000/float64(n),
			allocs, float64(after.TotalAlloc-before.TotalAlloc)/1e6/float64(n), statements/int64(n))
		return allocs
	}
	if allocs := measure("impact compact", "/api/v1/impact/"+g.core+compact, 3); allocs > leanAllocBudget {
		t.Errorf("compact impact of the core switch: %d allocations per request, budget %d", allocs, leanAllocBudget)
	}

	if os.Getenv("RETICORA_IMPACT_BENCH") != "1" {
		return
	}
	n := 10
	if v, err := strconv.Atoi(os.Getenv("RETICORA_IMPACT_BENCH_N")); err == nil && v > 0 {
		n = v
	}
	if file := os.Getenv("RETICORA_IMPACT_CPUPROFILE"); file != "" {
		out, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(out); err != nil {
			t.Fatal(err)
		}
		defer func() {
			pprof.StopCPUProfile()
			_ = out.Close()
		}()
	}
	measure("bench impact compact", "/api/v1/impact/"+g.core+compact, n)
	measure("bench impact full", "/api/v1/impact/"+g.core+"?depth=20", n)
}

// processCPU is the user and system CPU time of the test process so far.
func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// leanSeed names the CIs of seedLeanGraph the answers start from and the ids
// of its CI types by key.
type leanSeed struct {
	core, pdu, otherPDU, server string
	types                       map[string]string
}

// seedLeanGraph inserts the graph of the load seed into organization A with
// fixed ids (cmd/seed: relationships per CI filled up with dependencies):
// sw-00001 (the core switch) and sw-00002 connected to each other, the PDUs
// pdu-00003 and pdu-00004, and n-4 servers, each connected_to a switch and
// powered_by a PDU, every fifth also powered_by the other PDU (redundant
// power, IMP-04). depends_on between servers fills the relationships of the
// organization up to perCI·n. The first 40 CIs and every even one belong to
// client 1 and its site, the others to client 2 and its site. Every 97th
// server is contained in the server two before it (projected containment),
// every 499th is deleted (IMP-07); overrides set the status of every 37th
// CI, the name of every 101st (one of them to null) and the management IP of
// every 211th, a discovered value without override and an override of a
// field outside the node change nothing. The relationships are inserted
// with session_replication_role = replica, as the RLS matrix seeds its rows:
// the row trigger that derives their client and site from the CIs (about
// 35 s for 100,000 rows) is skipped, and the insert sets those columns from
// the same CIs itself.
func seedLeanGraph(t *testing.T, f *scopetest.Fixture, n, perCI int) leanSeed {
	t.Helper()
	bg := context.Background()
	org := f.OrgA
	sites := [2]string{f.ID(), f.ID()}
	for i, client := range []string{f.Client1, f.Client2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			sites[i], org, client, fmt.Sprintf("lean %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	s := leanSeed{types: map[string]string{}}
	rows, err := f.Admin.Query(bg, `SELECT key, id::text FROM ci_type WHERE organization_id IS NULL AND key IN ('switch', 'pdu', 'server')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			t.Fatal(err)
		}
		s.types[key] = id
	}
	rows.Close()
	if len(s.types) != 3 {
		t.Fatalf("ci types %v", s.types)
	}
	redundant := n / 5
	base := 2*(n-4) + 1 + redundant
	// The relationships go before the fixture: deleting them with the
	// CIs of the organization takes longer.
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `DELETE FROM ci_relationship WHERE organization_id = $1`, org); err != nil {
			t.Errorf("remove relationships: %v", err)
		}
	})
	for _, q := range []struct {
		name    string
		sql     string
		args    []any
		replica bool
	}{
		{"cis", `INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status, management_ip)
			SELECT md5($1 || '/ci/' || i)::uuid, $1::uuid,
				CASE WHEN i <= 40 OR i % 2 = 0 THEN $3::uuid ELSE $4::uuid END,
				CASE WHEN i <= 40 OR i % 2 = 0 THEN $5::uuid ELSE $6::uuid END,
				t.id, CASE WHEN i <= 2 THEN 'sw-' WHEN i <= 4 THEN 'pdu-' ELSE 'srv-' END || lpad(i::text, 5, '0'), 'active',
				('10.200.' || (i / 250) || '.' || (i % 250 + 1))::inet
			FROM generate_series(1, $2::int) AS i
			JOIN ci_type t ON t.organization_id IS NULL
				AND t.key = CASE WHEN i <= 2 THEN 'switch' WHEN i <= 4 THEN 'pdu' ELSE 'server' END`,
			[]any{org, n, f.Client1, f.Client2, sites[0], sites[1]}, false},
		{"statistics", `ANALYZE ci`, nil, false},
		{"relationships", `INSERT INTO ci_relationship (id, organization_id, source_ci_id, target_ci_id, rel_type, source,
				source_client_id, source_site_id, target_client_id, target_site_id)
			SELECT md5($1 || '/rel/' || e.kind || '/' || e.i)::uuid, $1::uuid, s.id, d.id, e.rel, e.source,
				s.client_id, s.site_id, d.client_id, d.site_id
			FROM (
				SELECT 'lldp' AS kind, i, i AS src, 1 + i % 2 AS dst, 'connected_to' AS rel, 'discovery' AS source
				FROM generate_series(5, $2::int) AS i
				UNION ALL SELECT 'power', i, i, 3 + i % 2, 'powered_by', 'manual' FROM generate_series(5, $2::int) AS i
				UNION ALL SELECT 'power2', i, i, 4 - i % 2, 'powered_by', 'manual' FROM generate_series(5, $2::int) AS i WHERE i % 5 = 0
				UNION ALL SELECT 'uplink', 1, 1, 2, 'connected_to', 'discovery'
				UNION ALL SELECT 'dependency', d, 5 + (d - 1) % ($2::int - 4),
					5 + ((d - 1) % ($2::int - 4) + 1 + (d - 1) / ($2::int - 4)) % ($2::int - 4), 'depends_on', 'manual'
				FROM generate_series(1, $3::int) AS d
			) e
			JOIN ci s ON s.id = md5($1 || '/ci/' || e.src)::uuid AND s.organization_id = $1::uuid
			JOIN ci d ON d.id = md5($1 || '/ci/' || e.dst)::uuid AND d.organization_id = $1::uuid`, []any{org, n, perCI*n - base}, true},
		{"containment", `UPDATE ci SET parent_ci_id = md5($1 || '/ci/' || (g.i - 2))::uuid
			FROM generate_series(7, $2::int) AS g(i) WHERE g.i % 97 = 0 AND ci.id = md5($1 || '/ci/' || g.i)::uuid`, []any{org, n}, false},
		{"overrides", `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, discovered_value)
			SELECT $1::uuid, md5($1 || '/ci/' || o.i)::uuid, o.field, o.value, 'lean seed', o.at, o.discovered
			FROM (
				SELECT i, 'status' AS field, '"retired"'::jsonb AS value, now() AS at, NULL::jsonb AS discovered
				FROM generate_series(1, $2::int) AS i WHERE i % 37 = 0
				UNION ALL SELECT i, 'name', CASE WHEN i = 1010 THEN NULL ELSE to_jsonb('renamed-' || (($2::int - i) * 7919 % $2::int)) END, now(), NULL
				FROM generate_series(1, $2::int) AS i WHERE i % 101 = 0
				UNION ALL SELECT i, 'management_ip', to_jsonb('192.0.2.' || (i % 250)), now(), NULL
				FROM generate_series(1, $2::int) AS i WHERE i % 211 = 0
				UNION ALL SELECT i, 'hostname', '"not-a-node-field"', now(), NULL FROM generate_series(1, $2::int) AS i WHERE i % 53 = 0
				UNION ALL SELECT i, 'status', NULL, NULL, '"discovered-only"' FROM generate_series(1, $2::int) AS i WHERE i % 41 = 0 AND i % 37 <> 0
			) o`, []any{org, n}, false},
		{"deleted", `UPDATE ci SET deleted_at = now()
			WHERE id IN (SELECT md5($1 || '/ci/' || i)::uuid FROM generate_series(5, $2::int) AS i WHERE i % 499 = 0)`, []any{org, n}, false},
	} {
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		if q.replica {
			if _, err = tx.Exec(bg, `SET LOCAL session_replication_role = replica`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec(bg, q.sql, q.args...); err != nil {
			_ = tx.Rollback(bg)
			t.Fatalf("seed %s: %v", q.name, err)
		}
		if err = tx.Commit(bg); err != nil {
			t.Fatalf("seed %s: %v", q.name, err)
		}
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship; ANALYZE ci_field_value`); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1`, org).Scan(&stored); err != nil || stored != perCI*n {
		t.Fatalf("%d relationships (%v), want %d", stored, err, perCI*n)
	}
	id := func(i int) string {
		var out string
		if err := f.Admin.QueryRow(bg, `SELECT md5($1 || '/ci/' || $2::int)::uuid::text`, org, i).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	s.core, s.pdu, s.otherPDU, s.server = id(1), id(3), id(4), id(42)
	return s
}

// sortedTopology orders the nodes of a topology answer by id; their order
// was the order of a Go map before WP-611. The edges stay as they are.
func sortedTopology(t *testing.T, body []byte) []byte {
	t.Helper()
	var g struct {
		Nodes []json.RawMessage `json:"nodes"`
		Edges json.RawMessage   `json:"edges"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}
	encode := func(nodes []json.RawMessage) []byte {
		var b bytes.Buffer
		b.WriteString(`{"nodes":[`)
		for i, n := range nodes {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(n)
		}
		b.WriteString(`],"edges":`)
		b.Write(g.Edges)
		b.WriteString("}\n")
		return b.Bytes()
	}
	if !bytes.Equal(encode(g.Nodes), body) {
		t.Fatalf("topology answer is not {nodes, edges}: %.300s", body)
	}
	sortRawByID(t, g.Nodes)
	return encode(g.Nodes)
}

// sortedBlastRadius orders the affected CIs, clients and sites of a blast
// radius; their order was the order of a Go map before WP-611.
func sortedBlastRadius(t *testing.T, body []byte) []byte {
	t.Helper()
	var b struct {
		CIID                  json.RawMessage   `json:"ci_id"`
		AffectedCIs           []json.RawMessage `json:"affected_cis"`
		AffectedCount         json.RawMessage   `json:"affected_count"`
		AffectedServices      []json.RawMessage `json:"affected_services,omitempty"`
		ClientIDs             []string          `json:"client_ids,omitempty"`
		SiteIDs               []string          `json:"site_ids,omitempty"`
		SinglePointsOfFailure []json.RawMessage `json:"single_points_of_failure,omitempty"`
		ProtectedByRedundancy []json.RawMessage `json:"protected_by_redundancy,omitempty"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	encode := func() []byte {
		out, err := json.Marshal(&b)
		if err != nil {
			t.Fatal(err)
		}
		return append(out, '\n')
	}
	if !bytes.Equal(encode(), body) {
		t.Fatalf("blast radius answer does not round-trip: %.300s", body)
	}
	sortRawByID(t, b.AffectedCIs)
	slices.Sort(b.ClientIDs)
	slices.Sort(b.SiteIDs)
	return encode()
}

// sortRawByID orders JSON objects by their id.
func sortRawByID(t *testing.T, objects []json.RawMessage) {
	t.Helper()
	ids := make(map[string]string, len(objects))
	for _, o := range objects {
		var n struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(o, &n); err != nil {
			t.Fatal(err)
		}
		ids[string(o)] = n.ID
	}
	slices.SortFunc(objects, func(a, b json.RawMessage) int { return strings.Compare(ids[string(a)], ids[string(b)]) })
}
