package database_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// analyzeLockClass is the class of the session advisory lock of
// database.AnalyzeAfterBulkEvery ("RAnz", key 0).
const analyzeLockClass int32 = 0x52416e7a

// identityCapture is a pgx tracer that keeps the batch candidate query of
// the discovery repository with its arguments.
type identityCapture struct {
	mu   sync.Mutex
	sql  string
	args []any
}

func (c *identityCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "serial_number_lc") {
		c.mu.Lock()
		c.sql, c.args = data.SQL, append([]any(nil), data.Args...)
		c.mu.Unlock()
	}
	return ctx
}

func (c *identityCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestAnalyzeAfterBulk covers WP-834 (SR-08, NFR-03) against PostgreSQL:
//   - role switch: the application role, which owns no table, analyzes
//     ci, ci_field_value, search_document, event_outbox and audit_log
//     through AnalyzeAfterBulkEvery (SET ROLE reticora_owner on a
//     connection of its own); the pool's connections stay the application
//     role;
//   - throttling: a table analyzed within the interval is left out
//     (pg_stat_user_tables), so AnalyzeAfterBulk right after a run analyzes
//     nothing; while another session holds the lock of the runs, a run
//     returns at once;
//   - index use after a mass creation: 5,000 new CIs of one organization
//     are created in one transaction with autovacuum off for ci, the run is
//     registered with database.AfterCommit as the ingest does; before the
//     commit no ANALYZE ran, after it the batch candidate query of the
//     discovery repository reads ci only through the identity indexes
//     under row level security (the check of TestIdentityHitsMatchSingle).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAnalyzeAfterBulk(t *testing.T) {
	f := scopetest.Seed(t, "60")
	bg := context.Background()
	lastAnalyze := func(table string) time.Time {
		t.Helper()
		var at time.Time
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(GREATEST(last_analyze, last_autoanalyze), '-infinity')
			FROM pg_stat_user_tables WHERE schemaname = 'public' AND relname = $1`, table).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if _, err := f.Admin.Exec(bg, `ALTER TABLE ci SET (autovacuum_enabled = false)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `ALTER TABLE ci RESET (autovacuum_enabled)`); err != nil {
			t.Errorf("reset autovacuum of ci: %v", err)
		}
	})

	t.Run("role switch", func(t *testing.T) {
		tables, err := database.AnalyzeAfterBulkEvery(bg, f.App, 0)
		if err != nil {
			t.Fatalf("the application role cannot analyze: %v", err)
		}
		if !slices.Equal(tables, database.BulkAnalyzeTables) {
			t.Errorf("analyzed %v, want %v", tables, database.BulkAnalyzeTables)
		}
		var role string
		if err = f.App.QueryRow(bg, `SELECT current_user`).Scan(&role); err != nil || role != database.DefaultAppRole {
			t.Errorf("a pooled connection runs as %q (%v), want %s", role, err, database.DefaultAppRole)
		}
	})

	t.Run("throttling", func(t *testing.T) {
		before := lastAnalyze("ci")
		tables, err := database.AnalyzeAfterBulk(bg, f.App)
		if err != nil || len(tables) != 0 {
			t.Errorf("AnalyzeAfterBulk right after a run analyzed %v (%v), want nothing", tables, err)
		}
		if after := lastAnalyze("ci"); !after.Equal(before) {
			t.Errorf("ci analyzed again within the interval: %v after %v", after, before)
		}
		// Another session holds the lock of the runs: the run returns at
		// once without analyzing.
		conn, err := f.Admin.Acquire(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		if _, err = conn.Exec(bg, `SELECT pg_advisory_lock($1, 0)`, analyzeLockClass); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = conn.Exec(bg, `SELECT pg_advisory_unlock($1, 0)`, analyzeLockClass) }()
		start := time.Now()
		tables, err = database.AnalyzeAfterBulkEvery(bg, f.App, 0)
		if err != nil || len(tables) != 0 {
			t.Errorf("a run beside a running one analyzed %v (%v), want nothing", tables, err)
		}
		if waited := time.Since(start); waited > 5*time.Second {
			t.Errorf("a run beside a running one waited %v", waited)
		}
	})

	t.Run("index use after a mass creation", func(t *testing.T) {
		var typeID string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&typeID); err != nil {
			t.Fatal(err)
		}
		// The statistics know organization A without CIs.
		if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
			t.Fatal(err)
		}
		statsBefore := lastAnalyze("ci")
		const n = 5000
		items := make([]*ci.Item, n)
		for i := range items {
			items[i] = &ci.Item{OrganizationID: f.OrgA, CITypeID: typeID, Name: fmt.Sprintf("bulk-%d", i), Status: "active",
				SerialNumber: fmt.Sprintf("BULK-%d", i), Attributes: map[string]any{}}
		}
		repo := ci.NewPGRepository(f.App)
		var analyzed []string
		var analyzeErr error
		var beforeCommit time.Time
		if err := database.WithRequestTenantTx(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context) error {
			res, err := repo.CreateMany(ctx, f.OrgA, items)
			if err != nil {
				return err
			}
			if len(res.Refused) != 0 {
				return fmt.Errorf("refused: %v", res.Refused)
			}
			database.AfterCommit(ctx, func(ctx context.Context) {
				analyzed, analyzeErr = database.AnalyzeAfterBulkEvery(ctx, f.App, 0)
			})
			beforeCommit = lastAnalyze("ci")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !beforeCommit.Equal(statsBefore) {
			t.Errorf("ci was analyzed before the commit (%v, before the creation %v)", beforeCommit, statsBefore)
		}
		if analyzeErr != nil || !slices.Contains(analyzed, "ci") {
			t.Fatalf("after the commit: analyzed %v (%v), want ci among them", analyzed, analyzeErr)
		}
		if after := lastAnalyze("ci"); !after.After(statsBefore) {
			t.Errorf("last analysis of ci %v, not after %v", after, statsBefore)
		}

		capture := &identityCapture{}
		cfg := f.App.Config()
		cfg.ConnConfig.Tracer = capture
		cfg.MinConns = 0
		traced, err := pgxpool.NewWithConfig(bg, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(traced.Close)
		serials := make([]string, 20)
		for i := range serials {
			serials[i] = fmt.Sprintf("bulk-%d", 100+37*i)
		}
		hits, err := discovery.NewPGRepository(traced).IdentityHits(f.OrgCtx(f.OrgA), f.OrgA, &discovery.IdentityKeys{Serials: serials})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != len(serials) {
			t.Errorf("batch candidate query: %d hits for %d serial numbers", len(hits), len(serials))
		}
		capture.mu.Lock()
		query, args := capture.sql, capture.args
		capture.mu.Unlock()
		if query == "" {
			t.Fatal("the batch candidate query was not captured")
		}
		allowed := []string{"idx_ci_serial_number_lc", "ci_pkey", "ci_id_organization_key"}
		reads := ciPlanReads(t, f, query, args...)
		if len(reads) == 0 {
			t.Fatalf("no read of ci in the plan of %s", query)
		}
		for _, r := range reads {
			if !slices.Contains(allowed, r.index) {
				t.Errorf("after the analysis the batch candidate query reads ci with %s %q, want one of %v", r.node, r.index, allowed)
			}
		}
	})
}

// planRead is a node of a plan that reads ci with its index.
type planRead struct{ node, index string }

// ciPlanReads returns the nodes of the plan of query that read ci, as the
// application role in the tenant context of organization A; a bitmap heap
// scan is reported with the indexes of its bitmap.
func ciPlanReads(t *testing.T, f *scopetest.Fixture, query string, args ...any) []planRead {
	t.Helper()
	var plan []byte
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	children := func(node map[string]any) []map[string]any {
		var out []map[string]any
		list, _ := node["Plans"].([]any)
		for _, c := range list {
			if child, ok := c.(map[string]any); ok {
				out = append(out, child)
			}
		}
		return out
	}
	var out []planRead
	var bitmap func(node map[string]any)
	bitmap = func(node map[string]any) {
		if name, ok := node["Index Name"].(string); ok {
			out = append(out, planRead{"Bitmap Heap Scan", name})
		}
		for _, c := range children(node) {
			bitmap(c)
		}
	}
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		nodeType, _ := node["Node Type"].(string)
		if node["Relation Name"] == "ci" {
			if nodeType == "Bitmap Heap Scan" {
				for _, c := range children(node) {
					bitmap(c)
				}
				return
			}
			index, _ := node["Index Name"].(string)
			out = append(out, planRead{nodeType, index})
		}
		for _, c := range children(node) {
			walk(c)
		}
	}
	for _, n := range nodes {
		if p, ok := n["Plan"].(map[string]any); ok {
			walk(p)
		}
	}
	return out
}
