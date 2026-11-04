package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// ftsIndex is the GIN index over search_document.search_vector (000030).
const ftsIndex = "idx_search_document_fts"

// searchBudget is the NFR-02 budget of one search at 10,000 objects.
const searchBudget = 500 * time.Millisecond

// sentStatement is one statement a traced pool sent, with its arguments.
type sentStatement struct {
	sql  string
	args []any
}

// sentLog records the statements a pool sends (pgx tracer).
type sentLog struct {
	mu   sync.Mutex
	stmt []sentStatement
}

func (l *sentLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmt = append(l.stmt, sentStatement{sql: data.SQL, args: data.Args})
	return ctx
}

func (l *sentLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// last returns the newest statement whose SQL contains part.
func (l *sentLog) last(part string) (sentStatement, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.stmt) - 1; i >= 0; i-- {
		if strings.Contains(l.stmt[i].sql, part) {
			return l.stmt[i], true
		}
	}
	return sentStatement{}, false
}

// searchPlanNode is a node of an EXPLAIN (FORMAT JSON) plan.
type searchPlanNode map[string]any

// searchPlanNodes flattens a plan into its nodes.
func searchPlanNodes(t *testing.T, plan []byte) []searchPlanNode {
	t.Helper()
	var roots []map[string]any
	if err := json.Unmarshal(plan, &roots); err != nil {
		t.Fatal(err)
	}
	var nodes []searchPlanNode
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		nodes = append(nodes, node)
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				walk(child)
			}
		}
	}
	for _, root := range roots {
		if p, ok := root["Plan"].(map[string]any); ok {
			walk(p)
		}
	}
	return nodes
}

// describeSearchPlan renders the nodes as "Node Type using index on
// relation (rows)".
func describeSearchPlan(nodes []searchPlanNode) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		s := fmt.Sprint(n["Node Type"])
		if idx, ok := n["Index Name"].(string); ok {
			s += " using " + idx
		}
		if rel, ok := n["Relation Name"].(string); ok {
			s += " on " + rel
		}
		if rows, ok := n["Actual Rows"].(float64); ok {
			loops, _ := n["Actual Loops"].(float64)
			removed, _ := n["Rows Removed by Filter"].(float64)
			s += fmt.Sprintf(" (%d rows, %d removed, %d loops)", int64(rows), int64(removed), int64(loops))
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " > ")
}

// ftsScanProblem returns why the plan does not show the documented scan of
// the full-text search under row level security (E-133 option 3), or "":
// search_document is read by exactly one scan that is not of
// idx_search_document_fts, the match (@@) is a filter of that scan and no
// index or recheck condition.
func ftsScanProblem(nodes []searchPlanNode) string {
	var problems []string
	scans := 0
	for _, n := range nodes {
		if idx, _ := n["Index Name"].(string); idx == ftsIndex {
			problems = append(problems, "uses "+ftsIndex+": the index serves the application role now; "+
				"replace the documented scan by an index check and update docs/acceptance/lasttest-g1.md")
		}
		for _, key := range []string{"Index Cond", "Recheck Cond"} {
			if cond, _ := n[key].(string); strings.Contains(cond, "@@") {
				problems = append(problems, "the match is the "+key+" of "+fmt.Sprint(n["Node Type"]))
			}
		}
		// The policy of the custom field rows (search_document_field,
		// WP-308) looks their document up by id: no read of the index.
		if n["Relation Name"] != "search_document" || n["Index Name"] == "search_document_pkey" {
			continue
		}
		scans++
		if filter, _ := n["Filter"].(string); !strings.Contains(filter, "search_vector @@") {
			problems = append(problems, fmt.Sprintf("the %s of search_document does not filter on search_vector @@: %q", n["Node Type"], filter))
		}
	}
	if scans != 1 {
		problems = append(problems, fmt.Sprintf("reads search_document %d times, want once", scans))
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ")
}

// TestSearchPlanUnderRLS covers WP-836 (SRC-01, NFR-02; SR-04, E-133
// option 3, E-17): the plan of GET /api/v1/search?q= over the PostgreSQL
// index for the application role under row level security, at 10,000 CIs
// of organization A (clients 1 and 2 and no client in turn, plus 50 CIs of
// organization B), indexed by ReindexTenant. The SQL is the one the handler
// sends (pgx tracer on a copy of the application pool); it is explained as
// reticora_app in the tenant transaction, org-wide and restricted to client
// 1. @@ (ts_match_vq) is not leakproof, so idx_search_document_fts cannot
// serve the match under the policies of search_document: the plan reads
// search_document once with the match as a filter (the documented scan,
// linear up to 10,000 objects). A plan that uses the index fails the test
// with the request to tighten it. For a selective term (one hit) and a term
// every CI carries (10,000 hits) the hits are those of the principal's
// scope; the response time is logged (best of five), and with
// RETICORA_PERF_BUDGET=1 each must stay within the NFR-02 budget of 500 ms.
// Without a PII vault the search sends no blind tokens (contactTokenMatch).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  RETICORA_PERF_BUDGET=1 go test -count=1 -v -run 'TestSearchPlanUnderRLS' ./internal/search/
func TestSearchPlanUnderRLS(t *testing.T) {
	f := scopetest.Seed(t, "25")
	bg := context.Background()
	const objects = 10000
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, hostname, manufacturer)
		SELECT $1, (ARRAY[$2::uuid, $3::uuid, NULL])[i % 3 + 1], `+server+`, 'wp836 host ' || i, 'active',
			'wp836-node-' || i, 'Vendor ' || (i % 50)
		FROM generate_series(1, $4::int) AS i`, f.OrgA, f.Client1, f.Client2, objects); err != nil {
		t.Fatalf("seed CIs of organization A: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, `+server+`, 'wp836 host ' || i, 'active' FROM generate_series(1, 50) AS i`, f.OrgB); err != nil {
		t.Fatalf("seed CIs of organization B: %v", err)
	}
	repo := search.NewPGRepository(f.App)
	for _, org := range []string{f.OrgA, f.OrgB} {
		if _, err := repo.ReindexTenant(f.OrgCtx(org), org); err != nil {
			t.Fatalf("reindex organization %s: %v", org, err)
		}
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci, search_document`); err != nil {
		t.Fatal(err)
	}

	// The handler on a traced copy of the application pool.
	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	mux := chi.NewRouter()
	search.NewHandler(search.NewPGRepository(traced), permissionStub{granted: map[string]bool{"ci:read": true, "asset:read": true}}).RegisterRoutes(mux)

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	get := func(scope database.TenantScope, q string) (total int, elapsed time.Duration) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {q}}.Encode(), nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		req = req.WithContext(database.ContextWithTenantScope(ctx, &scope))
		w := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(w, req)
		elapsed = time.Since(start)
		var body struct {
			Data []json.RawMessage `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("GET /api/v1/search?q=%s: %d %s", q, w.Code, w.Body.String())
		}
		return len(body.Data), elapsed
	}
	// matches runs stmt as the application role in scope and returns the
	// number of matching documents (the window count of the first row).
	matches := func(scope *database.TenantScope, stmt sentStatement) (n int64) {
		t.Helper()
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, stmt.sql, stmt.args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			if rows.Next() {
				values, verr := rows.Values()
				if verr != nil {
					return verr
				}
				n, _ = values[len(values)-1].(int64)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("run %s: %v", stmt.sql, err)
		}
		return n
	}
	explain := func(scope *database.TenantScope, stmt sentStatement, analyze bool) []byte {
		t.Helper()
		opts := "FORMAT JSON"
		if analyze {
			opts = "ANALYZE, FORMAT JSON"
		}
		var plan []byte
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN ("+opts+") "+stmt.sql, stmt.args...).Scan(&plan)
		}); err != nil {
			t.Fatalf("explain %s: %v", stmt.sql, err)
		}
		return plan
	}

	// CI i belongs to client 1 for i % 3 == 0, to client 2 for i % 3 == 1
	// and to no client for i % 3 == 2; client 1 sees its own and those
	// without a client, never those of organization B.
	inClient1 := 0
	for i := 1; i <= objects; i++ {
		if i%3 != 1 {
			inClient1++
		}
	}
	for _, c := range []struct {
		name, q  string
		scope    database.TenantScope
		wantHits int
	}{
		{"selective term, org-wide", "wp836-node-4242", orgWide, 1},
		{"selective term, client 1", "wp836-node-4242", client1, 1},
		{"selective term of client 2, client 1", "wp836-node-4243", client1, 0},
		{"every CI, org-wide", "wp836", orgWide, objects},
		{"every CI, client 1", "wp836", client1, inClient1},
	} {
		best := time.Duration(-1)
		var page int
		for range 5 {
			n, elapsed := get(c.scope, c.q)
			page = n
			if best < 0 || elapsed < best {
				best = elapsed
			}
		}
		stmt, ok := log.last(" FROM search_document WHERE ")
		if !ok {
			t.Fatalf("%s: the search sent no statement on search_document", c.name)
		}
		if !strings.Contains(stmt.sql, "search_vector @@ websearch_to_tsquery('simple', $2)") {
			t.Fatalf("%s: the statement does not match search_vector: %s", c.name, stmt.sql)
		}
		scope := c.scope
		total := matches(&scope, stmt)
		if total != int64(c.wantHits) || page != min(c.wantHits, api.DefaultPageLimit) {
			t.Errorf("%s: %d matching documents, %d on the page; want %d", c.name, total, page, c.wantHits)
		}
		nodes := searchPlanNodes(t, explain(&scope, stmt, false))
		if problem := ftsScanProblem(nodes); problem != "" {
			t.Errorf("E-133: %s: the plan of the full-text search for the application role is not the documented scan: %s\n%s\n%s",
				c.name, problem, describeSearchPlan(nodes), stmt.sql)
		}
		analyzed := searchPlanNodes(t, explain(&scope, stmt, true))
		t.Logf("%s: %d hits, best of 5 %v; plan as reticora_app: %s", c.name, total, best.Round(time.Millisecond), describeSearchPlan(analyzed))
		if os.Getenv("RETICORA_PERF_BUDGET") == "1" && best > searchBudget {
			t.Errorf("NFR-02: %s: GET /api/v1/search at %d objects took %v, budget %v", c.name, objects, best, searchBudget)
		}
	}
}
