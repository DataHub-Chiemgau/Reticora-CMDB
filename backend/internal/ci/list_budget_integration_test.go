package ci_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// listSortIndex is the index of the default sort of the CI list.
const listSortIndex = "idx_ci_list_sort"

// loggedStatement is one statement a pool sent, with its arguments.
type loggedStatement struct {
	sql  string
	args []any
}

// statementLog records the statements a pool sends (ABN-02).
type statementLog struct {
	mu   sync.Mutex
	stmt []loggedStatement
}

func (l *statementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmt = append(l.stmt, loggedStatement{sql: data.SQL, args: data.Args})
	return ctx
}

func (l *statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// since returns the statements logged after the first n.
func (l *statementLog) since(n int) []loggedStatement {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]loggedStatement(nil), l.stmt[n:]...)
}

// mark returns the number of statements logged so far.
func (l *statementLog) mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.stmt)
}

// listPage is the body of GET /cis.
type listPage struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Total      int    `json:"total"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// planNodes flattens an EXPLAIN (FORMAT JSON) plan into its nodes.
func planNodes(t *testing.T, plan []byte) []map[string]any {
	t.Helper()
	var roots []map[string]any
	if err := json.Unmarshal(plan, &roots); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
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

// describePlan renders the plan nodes as "Node Type [index] on relation".
func describePlan(nodes []map[string]any) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		s := fmt.Sprint(n["Node Type"])
		if idx, ok := n["Index Name"].(string); ok {
			s += " using " + idx
		}
		if rel, ok := n["Relation Name"].(string); ok {
			s += " on " + rel
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " > ")
}

// listPlanProblem returns why the plan of a page of the CI list does not
// read the page from idx_ci_list_sort, or "" when it does: the CIs come
// from an index scan of idx_ci_list_sort, nothing sorts them and nothing
// reads the table sequentially.
func listPlanProblem(t *testing.T, plan []byte) string {
	t.Helper()
	nodes := planNodes(t, plan)
	var problems []string
	usesIndex := false
	for _, n := range nodes {
		switch n["Node Type"] {
		case "Index Scan", "Index Only Scan":
			if n["Index Name"] == listSortIndex && n["Relation Name"] == "ci" {
				usesIndex = true
			}
		case "Sort", "Incremental Sort":
			problems = append(problems, fmt.Sprintf("sorts by %v", n["Sort Key"]))
		case "Seq Scan":
			if n["Relation Name"] == "ci" {
				problems = append(problems, "reads ci sequentially")
			}
		}
	}
	if !usesIndex {
		problems = append([]string{"does not read the page from " + listSortIndex + " (missing index?)"}, problems...)
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") + ": " + describePlan(nodes)
}

// seedListCIs inserts n CIs of organization A as cmd/seed places them: in
// pairs with the same created_at (the order falls back on the id),
// alternately of client 1 and client 2, each in a room of its client, so
// ci_derive_location gives every CI a site; every tenth is in
// maintenance. With a client and a site on every CI, the planner estimates
// one CI visible to an org-wide principal (the client and site predicates
// of ci_isolation, 0.5 % each).
func seedListCIs(t *testing.T, f *scopetest.Fixture, n int) {
	t.Helper()
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	room1, room2 := f.Room(t, f.Client1, "list-room-1"), f.Room(t, f.Client2, "list-room-2")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status, created_at)
		SELECT $1, CASE WHEN i % 2 = 0 THEN $2::uuid ELSE $3::uuid END, CASE WHEN i % 2 = 0 THEN $4::uuid ELSE $5::uuid END,
			$6, 'list-' || i, CASE WHEN i % 10 = 0 THEN 'maintenance' ELSE 'active' END,
			'2026-01-01 00:00:00+00'::timestamptz + ((i / 2) || ' seconds')::interval
		FROM generate_series(1, $7::int) AS i`, f.OrgA, f.Client1, f.Client2, room1, room2, serverType, n); err != nil {
		t.Fatal(err)
	}
	var sited int
	if err := f.Admin.QueryRow(bg, `SELECT count(site_id) FROM ci WHERE organization_id = $1`, f.OrgA).Scan(&sited); err != nil || sited != n {
		t.Fatalf("%d of %d CIs with a site (%v)", sited, n, err)
	}
}

// explainAsApp returns the plan of stmt as the application role in the
// tenant context of an org-wide principal of orgID, so the row level
// policies are part of it. prepare runs first in the same transaction.
func explainAsApp(t *testing.T, app *pgxpool.Pool, orgID string, stmt loggedStatement, prepare ...string) []byte {
	t.Helper()
	return explainAsAppWith(t, app, orgID, "FORMAT JSON", stmt, prepare...)
}

// explainAsAppWith is explainAsApp with the EXPLAIN options (e.g. ANALYZE,
// VERBOSE; FORMAT JSON among them).
func explainAsAppWith(t *testing.T, app *pgxpool.Pool, orgID, options string, stmt loggedStatement, prepare ...string) []byte {
	t.Helper()
	var plan []byte
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(context.Background(), app, &scope, func(ctx context.Context, tx pgx.Tx) error {
		for _, p := range prepare {
			if _, err := tx.Exec(ctx, p); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, "EXPLAIN ("+options+") "+stmt.sql, stmt.args...).Scan(&plan)
	})
	if err != nil {
		t.Fatalf("explain %s: %v", stmt.sql, err)
	}
	return plan
}

// TestCIListBudget covers NFR-02 and ABN-02 for GET /cis (WP-605, WP-438,
// F-117) against PostgreSQL with 10,000 CIs that carry a client and a site
// like the seed's (with them the planner expects one visible CI): a page of
// the list sends a fixed number of statements (no N+1), reads its rows from
// idx_ci_list_sort as the application role without sorting the
// organization (also ascending and on the next page of its cursor), and
// total stays the exact count, which a page shorter than the limit gives
// without counting. The plan is the one of the request's transaction: the
// settings the request made before the page apply. Without the index the
// plan check names it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIListBudget(t *testing.T) {
	f := scopetest.Seed(t, "f7")
	bg := context.Background()
	// A deleted CI and the CIs of organization B stay out of the list.
	const n = 10000
	seedListCIs(t, f, n)
	f.CI(t, f.OrgB, "", "list-other-org")
	deleted := f.CI(t, f.OrgA, f.Client1, "list-deleted")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}
	var order []string
	rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC, id DESC`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(order) != n {
		t.Fatalf("seeded %d CIs, want %d", len(order), n)
	}

	cfg := f.App.Config()
	trace := &statementLog{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(pool))).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	get := func(query url.Values) (listPage, []loggedStatement) {
		t.Helper()
		before := trace.mark()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /cis?%s: %d %s", query.Encode(), w.Code, w.Body.String())
		}
		var page listPage
		if jerr := json.Unmarshal(w.Body.Bytes(), &page); jerr != nil {
			t.Fatal(jerr)
		}
		return page, trace.since(before)
	}
	ids := func(page listPage) []string {
		out := make([]string, len(page.Data))
		for i, d := range page.Data {
			out[i] = d.ID
		}
		return out
	}
	// with returns the statements whose SQL contains part.
	with := func(stmts []loggedStatement, part string) []loggedStatement {
		var out []loggedStatement
		for _, s := range stmts {
			if strings.Contains(s.sql, part) {
				out = append(out, s)
			}
		}
		return out
	}
	pageStatement := func(stmts []loggedStatement) loggedStatement {
		t.Helper()
		pages := with(stmts, "ORDER BY")
		if len(pages) != 1 {
			t.Fatalf("%d page statements among %d statements, want 1", len(pages), len(stmts))
		}
		return pages[0]
	}
	// pagePlan EXPLAINs the page as the application role after the
	// settings (SET LOCAL) the request made before it.
	pagePlan := func(stmts []loggedStatement) []byte {
		t.Helper()
		var settings []string
		for _, s := range stmts {
			if strings.Contains(s.sql, "ORDER BY") {
				break
			}
			if strings.HasPrefix(s.sql, "SET LOCAL ") {
				settings = append(settings, s.sql)
			}
		}
		return explainAsApp(t, f.App, f.OrgA, pageStatement(stmts), settings...)
	}
	counts := func(stmts []loggedStatement) int { return len(with(stmts, "COUNT(*)")) }

	// Warm the pool: the first connection switches its role.
	get(url.Values{"limit": {"1"}})

	// begin with the tenant context, sorting off for the page (the default
	// order without filters), page, count, overrides of the page, commit.
	const listStatements = 6
	page, stmts := get(url.Values{"limit": {"50"}})
	if got := ids(page); strings.Join(got, ",") != strings.Join(order[:50], ",") {
		t.Errorf("first page: %v, want the first 50 of created_at DESC, id DESC", got)
	}
	if page.Total != n || !page.HasMore || page.NextCursor == "" {
		t.Errorf("first page: total %d, has_more %v, next_cursor %q; want %d, true, a cursor", page.Total, page.HasMore, page.NextCursor, n)
	}
	if len(stmts) != listStatements || counts(stmts) != 1 {
		t.Errorf("GET /cis?limit=50: %d statements (%d counts), want %d with one count", len(stmts), counts(stmts), listStatements)
	}
	if problem := listPlanProblem(t, pagePlan(stmts)); problem != "" {
		t.Errorf("GET /cis?limit=50: the page %s", problem)
	}
	for _, limit := range []string{"10", "200"} {
		if _, s := get(url.Values{"limit": {limit}}); len(s) != listStatements {
			t.Errorf("GET /cis?limit=%s: %d statements, want %d (no N+1)", limit, len(s), listStatements)
		}
	}

	// The next page through the cursor and the ascending order.
	next, stmts := get(url.Values{"limit": {"50"}, "cursor": {page.NextCursor}})
	if got := ids(next); strings.Join(got, ",") != strings.Join(order[50:100], ",") {
		t.Errorf("cursor page: %v, want rows 51-100", got)
	}
	if problem := listPlanProblem(t, pagePlan(stmts)); problem != "" {
		t.Errorf("cursor page: the page %s", problem)
	}
	asc, stmts := get(url.Values{"limit": {"50"}, "sort_dir": {"asc"}})
	if got := ids(asc); len(got) != 50 || got[0] != order[n-1] || got[49] != order[n-50] {
		t.Errorf("ascending page: %v, want the last 50 of the descending order reversed", got)
	}
	if problem := listPlanProblem(t, pagePlan(stmts)); problem != "" {
		t.Errorf("ascending page: the page %s", problem)
	}

	// A page shorter than the limit gives total without counting; past the
	// end, the count runs.
	last, stmts := get(url.Values{"limit": {"50"}, "offset": {"9990"}})
	if len(last.Data) != 10 || last.Total != n || last.HasMore || counts(stmts) != 0 || len(stmts) != listStatements-1 {
		t.Errorf("last page: %d rows, total %d, has_more %v, %d statements (%d counts); want 10, %d, false, %d without count",
			len(last.Data), last.Total, last.HasMore, len(stmts), counts(stmts), n, listStatements-1)
	}
	beyond, stmts := get(url.Values{"limit": {"50"}, "offset": {"20000"}})
	if len(beyond.Data) != 0 || beyond.Total != n || counts(stmts) != 1 {
		t.Errorf("past the end: %d rows, total %d, %d counts; want 0, %d, 1", len(beyond.Data), beyond.Total, counts(stmts), n)
	}
	found, stmts := get(url.Values{"limit": {"50"}, "search": {"list-4242"}})
	if len(found.Data) != 1 || found.Total != 1 || counts(stmts) != 0 {
		t.Errorf("search: %d rows, total %d, %d counts; want 1, 1, 0", len(found.Data), found.Total, counts(stmts))
	}
	filtered, stmts := get(url.Values{"limit": {"50"}, "status": {"maintenance"}})
	if filtered.Total != n/10 {
		t.Errorf("filter status=maintenance: total %d, want %d", filtered.Total, n/10)
	}
	if s := with(stmts, "enable_sort"); len(s) != 0 {
		t.Errorf("filter status=maintenance: %d statements set enable_sort; a filter keeps the planner's choice", len(s))
	}

	// In a caller's transaction the list switches sorting back on for the
	// statements that follow it.
	var sorting string
	if err = database.WithRequestTenant(f.OrgCtx(f.OrgA), pool, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		items, _, lerr := ci.NewPGRepository(pool).List(database.ContextWithTx(ctx, tx), f.OrgA, ci.FilterParams{}, api.PaginationParams{Limit: 50})
		if lerr != nil || len(items) != 50 {
			return fmt.Errorf("list in the caller's transaction: %d items, %v", len(items), lerr)
		}
		return tx.QueryRow(ctx, `SELECT current_setting('enable_sort')`).Scan(&sorting)
	}); err != nil {
		t.Fatal(err)
	}
	if sorting != "on" {
		t.Errorf("enable_sort %q in the caller's transaction after the list, want on", sorting)
	}

	// The index holds the columns ci_isolation reads, so an index-only scan
	// of it can serve the count of an unfiltered list. Whether the planner
	// takes it depends on the visibility map and random_page_cost, so the
	// definition is checked here.
	var def string
	if err = f.Admin.QueryRow(bg, `SELECT pg_get_indexdef($1::regclass)`, listSortIndex).Scan(&def); err != nil {
		t.Fatal(err)
	}
	const wantDef = "ON public.ci USING btree (organization_id, created_at DESC, id DESC) INCLUDE (client_id, site_id) WHERE (deleted_at IS NULL)"
	if !strings.HasSuffix(def, wantDef) {
		t.Errorf("%s: %s, want ... %s", listSortIndex, def, wantDef)
	}

	// Negative case: without the index the check names it. The index is
	// dropped in a transaction that is rolled back; the request's setting
	// applies.
	_, stmts = get(url.Values{"limit": {"50"}})
	stmt := pageStatement(stmts)
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	var plan []byte
	for _, s := range []string{
		"SET LOCAL lock_timeout = '30s'",
		"DROP INDEX " + listSortIndex,
		"SET LOCAL enable_sort = off",
		"SET LOCAL ROLE reticora_app",
	} {
		if _, err = tx.Exec(bg, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err = tx.Exec(bg, `SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
		set_config('app.client_scope', '', true), set_config('app.site_scope', '', true), set_config('app.team_scope', '', true)`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(bg, "EXPLAIN (FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if problem := listPlanProblem(t, plan); !strings.Contains(problem, listSortIndex) {
		t.Errorf("without %s the check reports %q, want the index named", listSortIndex, problem)
	} else {
		t.Logf("without the index: %s", problem)
	}
}

// planWork returns the rows the nodes of an EXPLAIN (ANALYZE, FORMAT JSON)
// plan produced and discarded in joins, over all their loops, without the
// scans of ci (their filters also see the CIs of other organizations).
func planWork(t *testing.T, plan []byte) float64 {
	t.Helper()
	var work float64
	for _, n := range planNodes(t, plan) {
		if n["Relation Name"] == "ci" {
			continue
		}
		rows, _ := n["Actual Rows"].(float64)
		removed, _ := n["Rows Removed by Join Filter"].(float64)
		loops, _ := n["Actual Loops"].(float64)
		work += (rows + removed) * loops
	}
	return work
}

// TestCINodeReadBudget covers NFR-02 for the node read of topology and
// impact (ci.ListNodes; WP-438, F-117) against PostgreSQL with 10,000 CIs
// that carry a client and a site and 500 manual overrides, as the bulk
// scenario of tests/load/api.js writes them: the nodes carry the effective
// values, and as the application role the work of the statement grows with
// the CIs and their overrides, not with their product. The planner expects
// one visible CI; with the overrides of the organization aggregated in a
// joined subquery it aggregated all of them again for every CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCINodeReadBudget(t *testing.T) {
	f := scopetest.Seed(t, "f7")
	bg := context.Background()
	const n, overridden = 10000, 500
	seedListCIs(t, f, n)
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		SELECT organization_id, id, 'status', '"retired"', 'node read budget', now(), true
		FROM ci WHERE organization_id = $1 AND name LIKE 'list-%' ORDER BY name LIMIT $2`, f.OrgA, overridden); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `VACUUM (ANALYZE) ci, ci_field_value`); err != nil {
		t.Fatal(err)
	}
	var ids []string
	retired := map[string]bool{}
	rows, err := f.Admin.Query(bg, `SELECT c.id::text, v.ci_id IS NOT NULL FROM ci c
		LEFT JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'status' AND v.override_at IS NOT NULL
		WHERE c.organization_id = $1 ORDER BY c.name`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var over bool
		if err = rows.Scan(&id, &over); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		retired[id] = over
	}
	if err = rows.Err(); err != nil || len(ids) != n || len(retired) != n {
		t.Fatalf("%d CIs (%v), want %d", len(ids), err, n)
	}

	cfg := f.App.Config()
	trace := &statementLog{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := ci.NewPGRepository(pool)
	ctx := f.OrgCtx(f.OrgA)
	// All CIs (the read of the organization, above 2,000 ids) and 100 of
	// them, half of those overridden (by ids).
	for _, c := range []struct {
		name string
		ids  []string
	}{{"organization", ids}, {"by ids", ids[overridden-50 : overridden+50]}} {
		before := trace.mark()
		nodes, lerr := repo.ListNodes(ctx, f.OrgA, c.ids)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if len(nodes) != len(c.ids) {
			t.Errorf("%s: %d nodes, want %d", c.name, len(nodes), len(c.ids))
		}
		wrong := 0
		for _, node := range nodes {
			if retired[node.ID] != (node.Status == "retired") {
				wrong++
			}
		}
		if wrong > 0 {
			t.Errorf("%s: %d nodes without their effective status", c.name, wrong)
		}
		stmts := trace.since(before)
		var read *loggedStatement
		for i := range stmts {
			if strings.Contains(stmts[i].sql, "FROM ci c") {
				read = &stmts[i]
			}
		}
		if read == nil {
			t.Fatalf("%s: no node read among %d statements", c.name, len(stmts))
		}
		plan := explainAsAppWith(t, f.App, f.OrgA, "ANALYZE, FORMAT JSON", *read)
		budget := 10 * float64(len(c.ids)+overridden)
		if work := planWork(t, plan); work > budget {
			t.Errorf("%s: the node read handles %.0f rows besides the scan of ci, want at most %.0f (10 per CI and override): %s",
				c.name, work, budget, describePlan(planNodes(t, plan)))
		} else {
			t.Logf("%s: %.0f rows besides the scan of ci: %s", c.name, work, describePlan(planNodes(t, plan)))
		}
	}
}
