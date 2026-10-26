package ci_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// budgetDatabase creates a database of its own for t on the server of
// TEST_DATABASE_URL, migrated from backend/migrations as the migrate CLI
// does (one simple-protocol Exec per file, an implicit transaction), and
// points TEST_DATABASE_URL and TEST_APP_DATABASE_URL at it for the duration
// of t, so scopetest.Seed seeds and connects there; it returns the name.
// Every run starts from the same statistics: no rows of other tests,
// packages or earlier runs in ci (F-280). The database is dropped when t
// ends. A template copy of the shared database is not possible: the
// TimescaleDB scheduler keeps a session in every database with the
// extension.
func budgetDatabase(t *testing.T) string {
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
	name := fmt.Sprintf("reticora_wp436_budget_%d", time.Now().UnixNano())
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
	ups, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(ups) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		sql, rerr := os.ReadFile(up)
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

// queryBudget is the expectation for the plan of one product query (ABN-02,
// WP-436), EXPLAIN ANALYZE as the application role.
type queryBudget struct {
	// indexes are groups of index names: every group needs a scan using one
	// of its indexes. A group holds more than one name only where the
	// planner may choose between equivalent indexes at equal cost.
	indexes [][]string
	// maxRows bounds the rows the query reads from ci and ci_identity_mac:
	// returned rows plus rows removed by a filter or an index recheck, for
	// every loop.
	maxRows int64
	// gap, when set, names a known gap: no index the application role can
	// use serves the query yet. Instead of indexes and maxRows the check
	// requires a single scan of ci and the rows read are logged. Setting
	// indexes and maxRows and removing gap tightens the check.
	gap string
}

// budgetTables are the tables whose rows grow with the inventory of an
// organization; maxRows counts their rows.
var budgetTables = []string{"ci", "ci_identity_mac"}

// planReads is what an EXPLAIN (ANALYZE, FORMAT JSON) plan shows about the
// tables and indexes a statement reads.
type planReads struct {
	indexes   map[string]bool  // indexes of index and bitmap index scans
	rows      map[string]int64 // rows read per table
	scans     map[string]int64 // scans per table, loops counted
	seqTables []string         // tables read sequentially
	plan      string           // the nodes, for messages
}

// readPlan sums the scan nodes of plan.
func readPlan(t *testing.T, plan []byte) planReads {
	t.Helper()
	nodes := planNodes(t, plan)
	r := planReads{indexes: map[string]bool{}, rows: map[string]int64{}, scans: map[string]int64{}}
	num := func(n map[string]any, key string) int64 {
		v, _ := n[key].(float64)
		return int64(v)
	}
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		part := fmt.Sprint(n["Node Type"])
		if idx, ok := n["Index Name"].(string); ok {
			r.indexes[idx] = true
			part += " using " + idx
		}
		if rel, ok := n["Relation Name"].(string); ok {
			loops := num(n, "Actual Loops")
			// Rows and removed rows are averages per loop.
			read := (num(n, "Actual Rows") + num(n, "Rows Removed by Filter") + num(n, "Rows Removed by Index Recheck")) * loops
			r.rows[rel] += read
			r.scans[rel] += loops
			if n["Node Type"] == "Seq Scan" {
				r.seqTables = append(r.seqTables, rel)
			}
			part += fmt.Sprintf(" on %s (%d rows read)", rel, read)
		}
		parts = append(parts, part)
	}
	r.plan = strings.Join(parts, " > ")
	return r
}

// budgetRows returns the rows read from budgetTables.
func (r planReads) budgetRows() int64 {
	var n int64
	for _, table := range budgetTables {
		n += r.rows[table]
	}
	return n
}

// problem returns why the plan misses budget, or "": a group of indexes
// without a scan names its indexes ("missing index?"); a sequential scan
// of a budget table and rows beyond maxRows are reported. For a gap only a
// single scan of ci is required.
func (r planReads) problem(budget queryBudget) string {
	var problems []string
	if budget.gap != "" {
		if r.scans["ci"] != 1 {
			problems = append(problems, fmt.Sprintf("scans ci %d times, want once", r.scans["ci"]))
		}
	} else {
		for _, group := range budget.indexes {
			if !slices.ContainsFunc(group, func(idx string) bool { return r.indexes[idx] }) {
				problems = append(problems, "no scan using "+strings.Join(group, " or ")+" (missing index?)")
			}
		}
		for _, table := range r.seqTables {
			if slices.Contains(budgetTables, table) {
				problems = append(problems, "reads "+table+" sequentially")
			}
		}
		if n := r.budgetRows(); n > budget.maxRows {
			problems = append(problems, fmt.Sprintf("reads %d rows of %s, budget %d", n, strings.Join(budgetTables, " and "), budget.maxRows))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") + ": " + r.plan
}

// analyzeAsApp runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role inside a transaction of scope, so the row level
// policies are part of the plan.
func analyzeAsApp(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt loggedStatement) []byte {
	t.Helper()
	var plan []byte
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain %s: %v", stmt.sql, err)
	}
	return plan
}

// analyzeWithout runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role in the tenant context of an org-wide principal of orgID
// after dropping indexes, in a transaction of the admin pool that is rolled
// back (the indexes stay).
func analyzeWithout(t *testing.T, admin *pgxpool.Pool, orgID string, stmt loggedStatement, indexes ...string) []byte {
	t.Helper()
	ctx := context.Background()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stmts := []string{"SET LOCAL lock_timeout = '30s'"}
	for _, idx := range indexes {
		stmts = append(stmts, "DROP INDEX "+pgx.Identifier{idx}.Sanitize())
	}
	stmts = append(stmts, "SET LOCAL ROLE "+pgx.Identifier{database.DefaultAppRole}.Sanitize())
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
	if err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan); err != nil {
		t.Fatalf("explain without %v: %v", indexes, err)
	}
	return plan
}

// queryTrace is a copy of an application pool whose statements a pgx
// tracer (statementLog) records, so a test can EXPLAIN the SQL the
// repositories really send.
type queryTrace struct {
	pool *pgxpool.Pool
	log  *statementLog
}

// newQueryTrace returns a traced copy of app, closed when t ends.
func newQueryTrace(t *testing.T, app *pgxpool.Pool) *queryTrace {
	t.Helper()
	cfg := app.Config()
	log := &statementLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &queryTrace{pool: pool, log: log}
}

// run runs fn and returns the statements it sent.
func (q *queryTrace) run(t *testing.T, fn func() error) []loggedStatement {
	t.Helper()
	mark := q.log.mark()
	if err := fn(); err != nil {
		t.Fatal(err)
	}
	return q.log.since(mark)
}
