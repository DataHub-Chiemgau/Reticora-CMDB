package scopetest

// The query budget helpers of ABN-02 (WP-436, WP-824): a database of its
// own per test, the statements a repository really sends (pgx tracer on a
// copy of the application pool), EXPLAIN (ANALYZE, FORMAT JSON) of them as
// the application role in the tenant transaction and the check of the plan
// against a budget (expected indexes, rows read of the tables that grow with
// the inventory). TestCIQueryBudget (internal/ci) and the Phase 2 budget
// tests of asset, ticket, savedview and graphqlbff use them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// MigrationsDirEnv names a directory with the up migrations BudgetDatabase
// applies instead of backend/migrations. The CI leaves it unset; locally it
// points at a copy whose TimescaleDB calls are stubbed, so the budget tests
// also run on a PostgreSQL without the extension.
const MigrationsDirEnv = "TEST_MIGRATIONS_DIR"

// PerfBudget reports whether the time limits of the budget tests apply
// (RETICORA_PERF_BUDGET=1, on the reference host of the load runs).
func PerfBudget() bool { return os.Getenv("RETICORA_PERF_BUDGET") == "1" }

// migrationsDir returns the directory of the up migrations: MigrationsDirEnv
// or backend/migrations next to this package.
func migrationsDir(t testing.TB) string {
	t.Helper()
	if dir := os.Getenv(MigrationsDirEnv); dir != "" {
		return dir
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("scopetest: no source path for the migrations")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}

// BudgetDatabase creates a database of its own for t on the server of
// TEST_DATABASE_URL, migrated from backend/migrations as the migrate CLI
// does (one simple-protocol Exec per file, an implicit transaction), and
// points TEST_DATABASE_URL and TEST_APP_DATABASE_URL at it for the duration
// of t, so Seed seeds and connects there; it returns the name. Every run
// starts from the same statistics: no rows of other tests, packages or
// earlier runs (F-280). The database is dropped when t ends. A template copy
// of the shared database is not possible: the TimescaleDB scheduler keeps a
// session in every database with the extension.
func BudgetDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	name := fmt.Sprintf("reticora_budget_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		conn, cerr := pgx.Connect(context.Background(), dsn)
		if cerr != nil {
			t.Errorf("drop database %s: %v", name, cerr)
			return
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if _, cerr = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); cerr != nil {
			t.Errorf("drop database %s: %v", name, cerr)
		}
	})

	urls := map[string]string{}
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_APP_DATABASE_URL"} {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		u, perr := url.Parse(v)
		if perr != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			t.Fatalf("%s is no postgres:// URL", key)
		}
		u.Path = "/" + name
		urls[key] = u.String()
	}
	conn, err := pgx.Connect(ctx, urls["TEST_DATABASE_URL"])
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	ups, err := filepath.Glob(filepath.Join(migrationsDir(t), "*.up.sql"))
	if err != nil || len(ups) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		sql, rerr := os.ReadFile(up) //nolint:gosec // the migrations of the repository or of MigrationsDirEnv
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err = conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migrate %s: %s: %v", name, filepath.Base(up), err)
		}
	}
	for key, v := range urls {
		t.Setenv(key, v)
	}
	return name
}

// LoggedStatement is one statement a pool sent, with its arguments.
type LoggedStatement struct {
	SQL  string
	Args []any
}

// StatementLog records the statements a pool sends (pgx.QueryTracer).
type StatementLog struct {
	mu   sync.Mutex
	stmt []LoggedStatement
}

// TraceQueryStart implements pgx.QueryTracer.
func (l *StatementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmt = append(l.stmt, LoggedStatement{SQL: data.SQL, Args: data.Args})
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer.
func (l *StatementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Mark returns the number of statements logged so far.
func (l *StatementLog) Mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.stmt)
}

// Since returns the statements logged after the first n.
func (l *StatementLog) Since(n int) []LoggedStatement {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LoggedStatement(nil), l.stmt[n:]...)
}

// QueryTrace is a copy of an application pool whose statements a pgx
// tracer (StatementLog) records, so a test can count and EXPLAIN the SQL
// the repositories really send.
type QueryTrace struct {
	Pool *pgxpool.Pool
	Log  *StatementLog
}

// NewQueryTrace returns a traced copy of app, closed when t ends.
func NewQueryTrace(t testing.TB, app *pgxpool.Pool) *QueryTrace {
	t.Helper()
	cfg := app.Config()
	log := &StatementLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &QueryTrace{Pool: pool, Log: log}
}

// Run runs fn and returns the statements it sent.
func (q *QueryTrace) Run(t testing.TB, fn func() error) []LoggedStatement {
	t.Helper()
	mark := q.Log.Mark()
	if err := fn(); err != nil {
		t.Fatal(err)
	}
	return q.Log.Since(mark)
}

// Statement returns the one statement of stmts whose SQL contains match;
// none or more than one fails t.
func Statement(t testing.TB, stmts []LoggedStatement, match string) LoggedStatement {
	t.Helper()
	var found []LoggedStatement
	for _, s := range stmts {
		if strings.Contains(s.SQL, match) {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d statements contain %q, want one", len(found), match)
	}
	return found[0]
}

// QueryBudget is the expectation for the plan of one product query (ABN-02,
// WP-436), EXPLAIN ANALYZE as the application role.
type QueryBudget struct {
	// Tables are the tables whose rows grow with the inventory of an
	// organization; MaxRows counts their rows. The first one is the table
	// the query lists.
	Tables []string
	// Indexes are groups of index names: every group needs a scan using
	// one of its indexes. A group holds more than one name only where the
	// planner may choose between equivalent indexes at equal cost.
	Indexes [][]string
	// MaxRows bounds the rows the query reads from Tables: returned rows
	// plus rows removed by a filter or an index recheck, for every loop.
	MaxRows int64
	// Gap, when set, names a known gap: no index the application role can
	// use serves the query yet. Instead of Indexes and MaxRows the check
	// requires a single scan of the first table and the rows read are
	// logged. Setting Indexes and MaxRows and removing Gap tightens the
	// check.
	Gap string
}

// PlanReads is what an EXPLAIN (ANALYZE, FORMAT JSON) plan shows about the
// tables and indexes a statement reads.
type PlanReads struct {
	Indexes   map[string]bool  // indexes of index and bitmap index scans
	Rows      map[string]int64 // rows read per table
	Scans     map[string]int64 // scans per table, loops counted
	SeqTables []string         // tables read sequentially
	Plan      string           // the nodes, for messages
}

// PlanNodes flattens an EXPLAIN (FORMAT JSON) plan into its nodes, the
// nodes of init plans and subplans included.
func PlanNodes(t testing.TB, plan []byte) []map[string]any {
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

// ReadPlan sums the scan nodes of plan.
func ReadPlan(t testing.TB, plan []byte) PlanReads {
	t.Helper()
	nodes := PlanNodes(t, plan)
	r := PlanReads{Indexes: map[string]bool{}, Rows: map[string]int64{}, Scans: map[string]int64{}}
	num := func(n map[string]any, key string) int64 {
		v, _ := n[key].(float64)
		return int64(v)
	}
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		part := fmt.Sprint(n["Node Type"])
		if idx, ok := n["Index Name"].(string); ok {
			r.Indexes[idx] = true
			part += " using " + idx
		}
		if rel, ok := n["Relation Name"].(string); ok {
			loops := num(n, "Actual Loops")
			// Rows and removed rows are averages per loop.
			read := (num(n, "Actual Rows") + num(n, "Rows Removed by Filter") + num(n, "Rows Removed by Index Recheck")) * loops
			r.Rows[rel] += read
			r.Scans[rel] += loops
			if n["Node Type"] == "Seq Scan" {
				r.SeqTables = append(r.SeqTables, rel)
			}
			part += fmt.Sprintf(" on %s (%d rows read)", rel, read)
		}
		parts = append(parts, part)
	}
	r.Plan = strings.Join(parts, " > ")
	return r
}

// BudgetRows returns the rows read from tables.
func (r *PlanReads) BudgetRows(tables []string) int64 {
	var n int64
	for _, table := range tables {
		n += r.Rows[table]
	}
	return n
}

// Problem returns why the plan misses budget, or "": a group of indexes
// without a scan names its indexes ("missing index?"); a sequential scan
// of a budget table and rows beyond MaxRows are reported. For a gap only a
// single scan of the first table is required.
func (r *PlanReads) Problem(budget *QueryBudget) string {
	var problems []string
	if budget.Gap != "" {
		if len(budget.Tables) > 0 && r.Scans[budget.Tables[0]] != 1 {
			problems = append(problems, fmt.Sprintf("scans %s %d times, want once", budget.Tables[0], r.Scans[budget.Tables[0]]))
		}
	} else {
		for _, group := range budget.Indexes {
			if !slices.ContainsFunc(group, func(idx string) bool { return r.Indexes[idx] }) {
				problems = append(problems, "no scan using "+strings.Join(group, " or ")+" (missing index?)")
			}
		}
		for _, table := range r.SeqTables {
			if slices.Contains(budget.Tables, table) {
				problems = append(problems, "reads "+table+" sequentially")
			}
		}
		if n := r.BudgetRows(budget.Tables); n > budget.MaxRows {
			problems = append(problems, fmt.Sprintf("reads %d rows of %s, budget %d", n, strings.Join(budget.Tables, " and "), budget.MaxRows))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") + ": " + r.Plan
}

// MissingIndex is the part of Problem that names a group of indexes
// without a scan; a negative case without them must report it.
func MissingIndex(group []string) string {
	return "no scan using " + strings.Join(group, " or ") + " (missing index?)"
}

// AnalyzeAsApp runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role inside a transaction of scope, so the row level
// policies are part of the plan.
func AnalyzeAsApp(t testing.TB, app *pgxpool.Pool, scope *database.TenantScope, stmt LoggedStatement) []byte {
	t.Helper()
	var plan []byte
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.SQL, stmt.Args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain %s: %v", stmt.SQL, err)
	}
	return plan
}

// AnalyzeWithout runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role in the tenant context of an org-wide principal of orgID
// after dropping indexes, in a transaction of the admin pool that is rolled
// back (the indexes stay). The index of a constraint (primary key, unique
// constraint) is dropped with its constraint and the foreign keys that
// depend on it.
func AnalyzeWithout(t testing.TB, admin *pgxpool.Pool, orgID string, stmt LoggedStatement, indexes ...string) []byte {
	t.Helper()
	ctx := context.Background()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout = '30s'"); err != nil {
		t.Fatal(err)
	}
	for _, idx := range indexes {
		var table, constraint string
		if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT conrelid::regclass::text FROM pg_constraint WHERE conindid = to_regclass($1) AND contype IN ('p', 'u')), ''),
			COALESCE((SELECT conname FROM pg_constraint WHERE conindid = to_regclass($1) AND contype IN ('p', 'u')), '')`, idx).Scan(&table, &constraint); err != nil {
			t.Fatalf("constraint of %s: %v", idx, err)
		}
		drop := "DROP INDEX " + pgx.Identifier{idx}.Sanitize()
		if constraint != "" {
			drop = "ALTER TABLE " + table + " DROP CONSTRAINT " + pgx.Identifier{constraint}.Sanitize() + " CASCADE"
		}
		if _, err = tx.Exec(ctx, drop); err != nil {
			t.Fatalf("%s: %v", drop, err)
		}
	}
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{database.DefaultAppRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
		set_config('app.client_scope', '', true), set_config('app.site_scope', '', true), set_config('app.team_scope', '', true)`, orgID); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.SQL, stmt.Args...).Scan(&plan); err != nil {
		t.Fatalf("explain without %v: %v", indexes, err)
	}
	return plan
}

// CheckPlan checks the plan of stmt against budget as the application role
// with scope (ABN-02) and, unless the budget names a gap, the negative case
// for every group of budget.Indexes: without the group the check must name
// it. It returns the rows read from budget.Tables. state and name label the
// messages.
func CheckPlan(t *testing.T, admin, app *pgxpool.Pool, scope *database.TenantScope, state, name string, stmt LoggedStatement, budget *QueryBudget) int64 {
	t.Helper()
	sql := strings.Join(strings.Fields(stmt.SQL), " ")
	reads := ReadPlan(t, AnalyzeAsApp(t, app, scope, stmt))
	if problem := reads.Problem(budget); problem != "" {
		t.Errorf("ABN-02 violated: %s, %s: %s\n%s", state, name, problem, sql)
	}
	rows := reads.BudgetRows(budget.Tables)
	if budget.Gap != "" {
		t.Logf("%s, %s: known gap (%s): %d rows read: %s", state, name, budget.Gap, rows, reads.Plan)
		return rows
	}
	t.Logf("%s, %s: %d rows read: %s", state, name, rows, reads.Plan)
	for _, group := range budget.Indexes {
		without := ReadPlan(t, AnalyzeWithout(t, admin, scope.OrgID, stmt, group...))
		if problem := without.Problem(budget); !strings.Contains(problem, MissingIndex(group)) {
			t.Errorf("%s, %s without %v: the check reports %q, want the missing index named", state, name, group, problem)
			continue
		}
		t.Logf("%s, %s without %v: %d rows read: %s", state, name, group, without.BudgetRows(budget.Tables), without.Plan)
	}
	return rows
}

// TimeBudget times fn best of n and, with PerfBudget, fails t when the best
// run exceeds budget (NFR-02); without it the time is only logged. Local
// times are no evidence by E-58.
func TimeBudget(t *testing.T, name string, n int, budget time.Duration, fn func() error) time.Duration {
	t.Helper()
	best := time.Duration(-1)
	for range n {
		start := time.Now()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if elapsed := time.Since(start); best < 0 || elapsed < best {
			best = elapsed
		}
	}
	t.Logf("%s: best of %d %v (budget %v with RETICORA_PERF_BUDGET=1)", name, n, best.Round(time.Microsecond), budget)
	if PerfBudget() && best > budget {
		t.Errorf("NFR-02: %s took %v, budget %v", name, best, budget)
	}
	return best
}
