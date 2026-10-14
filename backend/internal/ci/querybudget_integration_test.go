package ci_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// queryCounter counts the statements a pool sends (ABN-02).
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// countingPool is the application pool with a statement counter.
func countingPool(t *testing.T, app *pgxpool.Pool) (*pgxpool.Pool, *queryCounter) {
	t.Helper()
	cfg := app.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, counter
}

// statements runs fn and returns the number of statements it sent.
func statements(t *testing.T, counter *queryCounter, fn func() error) int64 {
	t.Helper()
	before := counter.n.Load()
	if err := fn(); err != nil {
		t.Fatal(err)
	}
	return counter.n.Load() - before
}

// planUsesSeqScan reports whether the plan of query reads table with a
// sequential scan; the plan is taken in the tenant context of orgID, so the
// row-level policies are part of it.
func planUsesSeqScan(t *testing.T, app *pgxpool.Pool, orgID, table, query string, args ...any) bool {
	t.Helper()
	var plan []byte
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(context.Background(), app, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan)
	})
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	var nodes []map[string]any
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
		if plan, ok := n["Plan"].(map[string]any); ok && walk(plan) {
			return true
		}
	}
	return false
}

// TestCIQueryBudget covers ABN-02 and NFR-10 (WP-204) for the CI list and
// detail against PostgreSQL with 10,000 CIs: the list sends the same number
// of statements for 10 and 200 rows (no N+1), the detail stays within its
// budget, and the identity lookups of reconciliation use indexes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIQueryBudget(t *testing.T) {
	f := scopetest.Seed(t, "c5")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, serial_number, hostname,
			management_ip, attributes)
		SELECT $1, $2, $3, 'budget-' || n, CASE WHEN n % 10 = 0 THEN 'maintenance' ELSE 'active' END, 'BUDGET-' || n,
			'budget-host-' || n, ('10.77.' || (n / 250) || '.' || (n % 250 + 1))::inet,
			jsonb_build_object('rack', 'R' || (n % 40))
		FROM generate_series(1, 10000) AS n`, f.OrgA, f.Client1, serverType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}
	pool, counter := countingPool(t, f.App)
	repo := ci.NewPGRepository(pool)
	ctx := f.OrgCtx(f.OrgA)

	list := func(limit int, filter ci.FilterParams) int64 {
		return statements(t, counter, func() error {
			items, total, err := repo.List(ctx, f.OrgA, filter, api.PaginationParams{Limit: limit})
			if err == nil && (len(items) != limit || total < limit) {
				t.Errorf("list %d: %d items of %d", limit, len(items), total)
			}
			return err
		})
	}
	// Warm the pool: the first connection switches its role.
	list(10, ci.FilterParams{})
	small, large := list(10, ci.FilterParams{}), list(200, ci.FilterParams{})
	if small != large {
		t.Errorf("list: %d statements for 10 rows, %d for 200 (N+1)", small, large)
	}
	// begin, tenant context, count, page, overrides of the page, commit.
	const listBudget = 6
	if large > listBudget {
		t.Errorf("list: %d statements, budget %d", large, listBudget)
	}
	if filtered := list(200, ci.FilterParams{Status: "maintenance"}); filtered > listBudget {
		t.Errorf("filtered list: %d statements, budget %d", filtered, listBudget)
	}

	var id string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'BUDGET-9999'`, f.OrgA).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// begin, tenant context, row, overrides, commit.
	const detailBudget = 5
	if n := statements(t, counter, func() error { _, err := repo.GetByID(ctx, f.OrgA, id); return err }); n > detailBudget {
		t.Errorf("detail: %d statements, budget %d", n, detailBudget)
	}

	for name, q := range map[string]struct {
		sql  string
		args []any
	}{
		"serial":        {`SELECT id FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, []any{f.OrgA, "BUDGET-42"}},
		"hostname":      {`SELECT id FROM ci WHERE organization_id = $1 AND lower(hostname) = $2 AND deleted_at IS NULL`, []any{f.OrgA, "budget-host-42"}},
		"management ip": {`SELECT id FROM ci WHERE organization_id = $1 AND management_ip = $2::inet AND deleted_at IS NULL`, []any{f.OrgA, "10.77.0.43"}},
		"detail":        {`SELECT id FROM ci WHERE id = $1 AND organization_id = $2`, []any{id, f.OrgA}},
		"attribute":     {`SELECT id FROM ci WHERE organization_id = $1 AND attributes @> $2::jsonb`, []any{f.OrgA, `{"rack":"R7"}`}},
	} {
		if planUsesSeqScan(t, f.App, f.OrgA, "ci", q.sql, q.args...) {
			t.Errorf("%s lookup reads ci with a sequential scan: %s", name, strings.Join(strings.Fields(q.sql), " "))
		}
	}
}
