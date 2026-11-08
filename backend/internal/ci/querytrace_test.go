package ci_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// The query budget helpers of TestCIQueryBudget (ABN-02, WP-436) live in
// scopetest (querybudget.go, WP-824) and serve the Phase 2 budget tests as
// well; these are their bindings for the CI tables.

// budgetDatabase creates a database of its own for t
// (scopetest.BudgetDatabase): every run starts from the same statistics,
// no rows of other tests, packages or earlier runs in ci (F-280).
func budgetDatabase(t *testing.T) string {
	t.Helper()
	return scopetest.BudgetDatabase(t)
}

// queryBudget is the expectation for the plan of one product query of the
// CI tables (scopetest.QueryBudget with budgetTables).
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

// shared returns the budget for scopetest.
func (b *queryBudget) shared() *scopetest.QueryBudget {
	return &scopetest.QueryBudget{Tables: budgetTables, Indexes: b.indexes, MaxRows: b.maxRows, Gap: b.gap}
}

// budgetTables are the tables whose rows grow with the inventory of an
// organization; maxRows counts their rows.
var budgetTables = []string{"ci", "ci_identity_mac"}

// planReads is what an EXPLAIN (ANALYZE, FORMAT JSON) plan shows about the
// tables and indexes a statement reads (scopetest.PlanReads).
type planReads struct {
	rows  map[string]int64 // rows read per table
	plan  string           // the nodes, for messages
	reads scopetest.PlanReads
}

// readPlan sums the scan nodes of plan.
func readPlan(t *testing.T, plan []byte) planReads {
	t.Helper()
	r := scopetest.ReadPlan(t, plan)
	return planReads{rows: r.Rows, plan: r.Plan, reads: r}
}

// budgetRows returns the rows read from budgetTables.
func (r *planReads) budgetRows() int64 { return r.reads.BudgetRows(budgetTables) }

// problem returns why the plan misses budget, or "" (scopetest.PlanReads.Problem).
func (r *planReads) problem(budget queryBudget) string {
	return r.reads.Problem(budget.shared())
}

// shared returns the statement for scopetest.
func (s loggedStatement) shared() scopetest.LoggedStatement {
	return scopetest.LoggedStatement{SQL: s.sql, Args: s.args}
}

// analyzeAsApp runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role inside a transaction of scope (scopetest.AnalyzeAsApp).
func analyzeAsApp(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt loggedStatement) []byte {
	t.Helper()
	return scopetest.AnalyzeAsApp(t, app, scope, stmt.shared())
}

// analyzeWithout runs EXPLAIN (ANALYZE, FORMAT JSON) of stmt as the
// application role of an org-wide principal of orgID after dropping
// indexes in a transaction that is rolled back (scopetest.AnalyzeWithout).
func analyzeWithout(t *testing.T, admin *pgxpool.Pool, orgID string, stmt loggedStatement, indexes ...string) []byte {
	t.Helper()
	return scopetest.AnalyzeWithout(t, admin, orgID, stmt.shared(), indexes...)
}

// queryTrace is a copy of an application pool whose statements a pgx
// tracer records (scopetest.QueryTrace), so a test can EXPLAIN the SQL the
// repositories really send.
type queryTrace struct {
	pool  *pgxpool.Pool
	trace *scopetest.QueryTrace
}

// newQueryTrace returns a traced copy of app, closed when t ends.
func newQueryTrace(t *testing.T, app *pgxpool.Pool) *queryTrace {
	t.Helper()
	q := scopetest.NewQueryTrace(t, app)
	return &queryTrace{pool: q.Pool, trace: q}
}

// run runs fn and returns the statements it sent.
func (q *queryTrace) run(t *testing.T, fn func() error) []loggedStatement {
	t.Helper()
	stmts := q.trace.Run(t, fn)
	out := make([]loggedStatement, len(stmts))
	for i, s := range stmts {
		out[i] = loggedStatement{sql: s.SQL, args: s.Args}
	}
	return out
}
