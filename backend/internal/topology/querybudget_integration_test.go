package topology_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

// TestTopologyQueryBudget covers ABN-02 and NFR-10 (WP-204) for the
// topology and the impact analysis against PostgreSQL: a hub with 400
// neighbours costs the same number of statements as one with 20 (no N+1
// per node), and the edge lookups by source and target use indexes.
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
	small, large := hub("budget-small", 20), hub("budget-large", 400)
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
		s, l := get(c.path(small), 20), get(c.path(large), 400)
		if l != s {
			t.Errorf("%s: %d statements for 20 neighbours, %d for 400 (N+1)", c.name, s, l)
		}
		const budget = 40
		if l > budget {
			t.Errorf("%s: %d statements, budget %d", c.name, l, budget)
		}
		t.Logf("%s: %d statements", c.name, l)
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
