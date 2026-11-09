package savedview_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
)

// TestSavedViewQueryBudget covers ABN-02 for the filter queries of Phase 2
// (WP-824, after the pattern of TestCIQueryBudget, WP-436) against
// PostgreSQL in a database of its own (scopetest.BudgetDatabase) with
// 10,000 CIs and 10,000 assets of organization A, every CI linked to its
// asset, and two trees of relationships (depends_on, four sources per
// target) over seven levels: CIs 1 to 5,000 depend on CI n/4, so the CIs
// upstream of CI 1 fan out, and CI 5,000 + k/4 depends on CI 5,000 + k, so
// the CIs downstream of CI 5,001 fan out; within the depth of 5
// (savedview.MaxRelationshipDepth) each root reaches 1,364 CIs.
//
//   - savedview.PGQueryEngine.Query sends the same number of statements for
//     10 and 200 rows and stays within queryBudget: the filter AST with
//     asset fields (WP-309) on assets and on the linked asset of CIs, and
//     upstream_of and downstream_of to depth 5.
//   - The count and the page of upstream_of and downstream_of, the SQL the
//     engine really sends (pgx tracer on a copy of the application pool),
//     are EXPLAINed with ANALYZE as the application role in the tenant
//     transaction after ANALYZE: the walk reads the edges of ci_relationship
//     through idx_ci_rel_org_target (upstream_of, target_ci_id) and
//     idx_ci_rel_org_source or uq_ci_relationship_edge (downstream_of,
//     source_ci_id; both lead with organization_id, source_ci_id) and reads
//     at most 1 % of the organization's edges, from a CI that reaches 13
//     CIs. Without the index the check names it (negative case, dropped in
//     a transaction that is rolled back).
//   - The attribute filter on warranty_days_left (WP-718, the preset
//     warranty_expiring) compares the computed system attribute under row
//     level security (E-133): count and page read the hits through
//     idx_ci_warranty_days_left over the stored column
//     ci.warranty_days_left_num (000274, finding of WP-824) and at most
//     1 % of the organization's CIs; without the index the check names it.
//     TestCIQueryBudget covers the attribute filter of the CI list.
//
// The checks run with organization A alone and again with 5,000 CIs,
// assets and edges of organization B. With RETICORA_PERF_BUDGET=1 the
// pages of 200 rows must stay below the 500 ms of NFR-02 (best of five).
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestSavedViewQueryBudget ./internal/savedview/
func TestSavedViewQueryBudget(t *testing.T) {
	db := scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "8f")
	bg := context.Background()
	// The database goes with the test: CIs and assets are truncated before
	// the fixture cleanup, which would delete them row by row with their
	// triggers. Only in that database.
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE ci_relationship, asset, ci CASCADE`); err != nil {
			t.Errorf("truncate: %v", err)
		}
	})
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}

	// seed inserts the assets and CIs 1..to of org (asset SV-n with
	// manufacturer ACME, CI sv-n linked to it, warranty_days_left 5 on every
	// hundredth CI; in organization A clients 1 and 2 and no client in
	// turn) and the edges of the two trees of size CIs among them.
	seed := func(org string, clients []any, to, size int) {
		t.Helper()
		stmts := []struct {
			name string
			sql  string
			args []any
		}{
			{"assets", `INSERT INTO asset (organization_id, client_id, inventory_no, name, manufacturer, warranty_until)
				SELECT $1, (ARRAY[$2::uuid, $3::uuid, NULL])[n % 3 + 1], 'SV-' || n, 'sv-asset-' || n, 'ACME', DATE '2027-01-01' + n
				FROM generate_series(1, $4::int) AS n`, []any{org, clients[0], clients[1], to}},
			{"CIs", `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, asset_id, attributes)
				SELECT $1, a.client_id, $2, 'sv-' || n, 'active', a.id,
					CASE WHEN n % 100 = 0 THEN '{"warranty_days_left": 5}'::jsonb ELSE '{}'::jsonb END
				FROM generate_series(1, $3::int) AS n JOIN asset a ON a.organization_id = $1 AND a.inventory_no = 'SV-' || n`,
				[]any{org, serverType, to}},
			// Tree U: CI n depends on CI n/4; tree D: CI size + k/4
			// depends on CI size + k.
			{"edges", `WITH c AS (SELECT substring(name FROM 4)::int AS n, id FROM ci WHERE organization_id = $1)
				INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
				SELECT $1, src.id, dst.id, 'depends_on' FROM (
					SELECT n AS src, n / 4 AS dst FROM generate_series(4, $2::int) AS n
					UNION ALL SELECT $2::int + k / 4, $2::int + k FROM generate_series(4, $2::int) AS k
				) AS e JOIN c src ON src.n = e.src JOIN c dst ON dst.n = e.dst`, []any{org, size}},
		}
		for _, s := range stmts {
			if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
				t.Fatalf("seed %s of %d CIs: %v", s.name, to, err)
			}
		}
	}
	const orgCIs = 10000
	seed(f.OrgA, []any{f.Client1, f.Client2}, orgCIs, orgCIs/2)
	analyze := func() {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `ANALYZE ci, asset, ci_relationship`); err != nil {
			t.Fatal(err)
		}
	}
	analyze()
	ciOf := func(n int) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'sv-' || $2::int`, f.OrgA, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var edges int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1`, f.OrgA).Scan(&edges); err != nil || edges < orgCIs-10 {
		t.Fatalf("the fixture has %d edges: %v", edges, err)
	}

	trace := scopetest.NewQueryTrace(t, f.App)
	engine := savedview.NewPGQueryEngine(trace.Pool)
	ctx := f.OrgCtx(f.OrgA)
	query := func(spec *savedview.FilterSpec, limit int) ([]string, int, error) {
		rows, total, err := engine.Query(ctx, f.OrgA, spec, api.PaginationParams{Limit: limit})
		out := make([]string, 0, len(rows))
		for i := range rows {
			out = append(out, rows[i].ID)
		}
		return out, total, err
	}

	// Statements (no N+1): begin with the tenant context, count, page,
	// commit; the specs name no attribute, tag or relationship type that
	// resolveCatalog would read.
	const queryBudget = 4
	upRoot, downRoot := ciOf(1), ciOf(orgCIs/2+1)
	const reached = 4 + 16 + 64 + 256 + 1024
	statementCases := []struct {
		name  string
		spec  *savedview.FilterSpec
		total int
	}{
		{"filter AST with asset fields on assets", &savedview.FilterSpec{EntityKind: "asset", Where: &savedview.Expr{And: []savedview.Expr{
			{Field: "manufacturer", Op: savedview.OpEq, Value: "ACME"}, {Field: "inventory_no", Op: savedview.OpLike, Value: "SV-*"},
		}}}, orgCIs},
		{"filter AST with a field of the linked asset on CIs", &savedview.FilterSpec{EntityKind: "ci",
			Where: &savedview.Expr{Field: "asset.manufacturer", Op: savedview.OpEq, Value: "ACME"}}, orgCIs},
		{"upstream_of to depth 5", &savedview.FilterSpec{EntityKind: "ci", UpstreamOf: upRoot}, reached},
		{"downstream_of to depth 5", &savedview.FilterSpec{EntityKind: "ci", DownstreamOf: downRoot}, reached},
	}
	run := func(spec *savedview.FilterSpec, limit, total int) int {
		t.Helper()
		return len(trace.Run(t, func() error {
			ids, got, err := query(spec, limit)
			if err == nil && (len(ids) != limit || got != total) {
				t.Errorf("query %d: %d rows of %d, want %d", limit, len(ids), got, total)
			}
			return err
		}))
	}
	// Warm the pool: the first connection switches its role.
	run(statementCases[0].spec, 10, statementCases[0].total)
	for _, c := range statementCases {
		small, large := run(c.spec, 10, c.total), run(c.spec, 200, c.total)
		if small != large {
			t.Errorf("%s: %d statements for 10 rows, %d for 200 (N+1)", c.name, small, large)
		}
		if large > queryBudget {
			t.Errorf("%s: %d statements, budget %d", c.name, large, queryBudget)
		}
		scopetest.TimeBudget(t, c.name+", page of 200 rows", 5, 500*time.Millisecond, func() error {
			_, _, err := query(c.spec, 200)
			return err
		})
	}

	// The plans of the walk from CIs that reach 13 CIs within depth 5.
	const maxRows = orgCIs / 100
	edgeTables := []string{"ci_relationship"}
	upBudget := &scopetest.QueryBudget{Tables: edgeTables, Indexes: [][]string{{"idx_ci_rel_org_target"}}, MaxRows: maxRows}
	downBudget := &scopetest.QueryBudget{Tables: edgeTables, Indexes: [][]string{{"idx_ci_rel_org_source", "uq_ci_relationship_edge"}}, MaxRows: maxRows}
	// ids returns the CIs of organization A named sv-<n> for the numbers
	// in [from, to], sorted.
	ids := func(ranges ...[2]int) []string {
		t.Helper()
		var out []string
		for _, r := range ranges {
			rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND name LIKE 'sv-%'
				AND substring(name FROM 4)::int BETWEEN $2 AND $3`, f.OrgA, r[0], r[1])
			if err != nil {
				t.Fatal(err)
			}
			found, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, found...)
		}
		slices.Sort(out)
		return out
	}
	// CI 312 of tree U: its sources 1248 to 1251 and theirs 4992 to 5000
	// (the tree ends at 5000); the same in tree D from CI 5312.
	half := orgCIs / 2
	planCases := []struct {
		name   string
		spec   *savedview.FilterSpec
		budget *scopetest.QueryBudget
		want   []string
	}{
		{"upstream_of", &savedview.FilterSpec{EntityKind: "ci", UpstreamOf: ciOf(312)}, upBudget, ids([2]int{1248, 1251}, [2]int{4992, 5000})},
		{"downstream_of", &savedview.FilterSpec{EntityKind: "ci", DownstreamOf: ciOf(half + 312)}, downBudget,
			ids([2]int{half + 1248, half + 1251}, [2]int{half + 4992, half + 5000})},
	}
	warranty := &savedview.FilterSpec{EntityKind: "ci", Where: &savedview.Expr{Field: "attributes.warranty_days_left", Op: savedview.OpLte,
		Value: float64(savedview.WarrantyExpiringDays)}}
	warrantyBudget := &scopetest.QueryBudget{Tables: []string{"ci"}, Indexes: [][]string{{"idx_ci_warranty_days_left"}}, MaxRows: maxRows}
	// The samples of servers_without_monitoring: five in the last hour
	// for every tenth CI of organization A.
	const monitored = orgCIs / 10
	if _, err := f.Admin.Exec(bg, `INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value)
		SELECT now() - k * interval '5 minutes', c.organization_id, c.id, 'sweep_reachable', 1
		FROM ci c CROSS JOIN generate_series(1, 5) AS k
		WHERE c.organization_id = $1 AND substring(c.name FROM 4)::int % 10 = 0`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE metric_sample`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = $1`, f.OrgA); err != nil {
			t.Errorf("delete samples: %v", err)
		}
	})
	preset, ok := savedview.PresetByKey("servers_without_monitoring")
	if !ok {
		t.Fatal("no preset servers_without_monitoring")
	}
	monitoring := &savedview.FilterSpec{EntityKind: preset.EntityKind, Sort: preset.Sort}
	raw, err := json.Marshal(preset.FilterSpec)
	if err == nil {
		err = json.Unmarshal(raw, monitoring)
	}
	if err != nil {
		t.Fatal(err)
	}
	// The type tree of server: a few types, read once per query.
	const maxTypeRows = 200
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	check := func(state string) {
		t.Helper()
		for _, c := range planCases {
			var got []string
			stmts := trace.Run(t, func() error {
				var err error
				got, _, err = query(c.spec, 50)
				return err
			})
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("%s, %s: found %d CIs %v, want %d %v", state, c.name, len(got), got, len(c.want), c.want)
			}
			scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, c.name+" (count)", scopetest.Statement(t, stmts, "SELECT COUNT(*) FROM ci"), c.budget)
			scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, c.name+" (page)", scopetest.Statement(t, stmts, " ORDER BY "), c.budget)
		}

		// The attribute filter on warranty_days_left: the hits (every
		// hundredth CI) through idx_ci_warranty_days_left (000274).
		var got []string
		stmts := trace.Run(t, func() error {
			var err error
			got, _, err = query(warranty, 200)
			return err
		})
		if len(got) != orgCIs/100 {
			t.Errorf("%s, warranty_days_left <= %d: %d CIs, want %d", state, savedview.WarrantyExpiringDays, len(got), orgCIs/100)
		}
		name := fmt.Sprintf("attribute filter warranty_days_left <= %d", savedview.WarrantyExpiringDays)
		scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, name+" (count)", scopetest.Statement(t, stmts, "SELECT COUNT(*) FROM ci"), warrantyBudget)
		scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, name+" (page)", scopetest.Statement(t, stmts, " ORDER BY "), warrantyBudget)

		// The preset servers_without_monitoring: every CI is a server
		// without monitored_by or monitors, every tenth has samples of the
		// last hour. Count and page walk the type tree once and probe the
		// samples of each CI by idx_metric_sample_ci (finding of the load
		// run of PR #147: per CI the type tree again and every sample of
		// the window).
		var noMetric []string
		var noMetricTotal int
		stmts = trace.Run(t, func() error {
			var err error
			noMetric, noMetricTotal, err = query(monitoring, 50)
			return err
		})
		if noMetricTotal != orgCIs-monitored || len(noMetric) != 50 {
			t.Errorf("%s, servers_without_monitoring: %d rows of %d, want 50 of %d", state, len(noMetric), noMetricTotal, orgCIs-monitored)
		}
		for _, part := range []string{"SELECT COUNT(*) FROM ci", " ORDER BY "} {
			reads := scopetest.ReadPlan(t, scopetest.AnalyzeAsApp(t, f.App, &orgWide, scopetest.Statement(t, stmts, part)))
			var sampleRows int64
			for rel, n := range reads.Rows {
				// The chunks of the hypertable under TimescaleDB.
				if rel == "metric_sample" || strings.HasPrefix(rel, "_hyper_") {
					sampleRows += n
				}
			}
			if typeRows := reads.Rows["ci_type"]; typeRows > maxTypeRows {
				t.Errorf("%s, servers_without_monitoring %q: reads %d rows of ci_type, budget %d: %s", state, part, typeRows, maxTypeRows, reads.Plan)
			}
			if sampleRows > monitored {
				t.Errorf("%s, servers_without_monitoring %q: reads %d samples, budget %d: %s", state, part, sampleRows, monitored, reads.Plan)
			}
			t.Logf("%s, servers_without_monitoring %q: %d rows of ci_type, %d samples: %s", state, part, reads.Rows["ci_type"], sampleRows, reads.Plan)
		}
	}
	check("organization A alone")
	seed(f.OrgB, []any{nil, nil}, 5000, 2500)
	analyze()
	check("with 5,000 CIs, assets and edges of organization B")
}

// TestRelationshipWalkStarBudget covers the walk of upstream_of from a CI
// with thousands of sources (finding of load run 42619469662,
// relation_search: 13.8 s per count, 60 s and EOF for the page) in a
// database of its own (scopetest.BudgetDatabase) shaped like the load seed:
// 10,000 CIs of organization A, every CI with a client and a site (the
// planner then takes the row level security filters of ci and
// ci_relationship for so selective that it expects about one row per
// level), a switch CIs 1 to 5,000 are connected to and a PDU that powers
// CIs 5,001 to 10,000.
//
// The count and the page of upstream_of the switch, the SQL the engine
// really sends, are EXPLAINed with ANALYZE as the application role: the
// walk reads the 5,000 edges into the switch, probes the edges into each
// source through idx_ci_rel_org_target (none) and reads at most 18,000
// rows of ci_relationship and ci, though the planner expects 5,000 edges
// per probe. Flattened into a join, the step of the
// walk read every CI of the organization with its edges per level and
// joined them to the work table (100 million join rows in the load run).
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestRelationshipWalkStarBudget ./internal/savedview/
func TestRelationshipWalkStarBudget(t *testing.T) {
	db := scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "8f")
	bg := context.Background()
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE ci_relationship, ci CASCADE`); err != nil {
			t.Errorf("truncate: %v", err)
		}
	})
	f.Room(t, f.Client1, "star-1")
	f.Room(t, f.Client2, "star-2")
	const orgCIs, sources = 10000, 5000
	seed := []struct {
		name, sql string
		args      []any
	}{
		{"CIs", `INSERT INTO ci (organization_id, client_id, site_id, ci_type_id, name, status)
			SELECT $1, s.client_id, s.id, t.id, 'st-' || n, 'active'
			FROM generate_series(1, $2::int) AS n
			JOIN site s ON s.organization_id = $1 AND s.name = 'star-' || (n % 2 + 1)
			JOIN ci_type t ON t.key = 'server' AND t.organization_id IS NULL`, []any{f.OrgA, orgCIs}},
		// The switch and the PDU of the load seed (cmd/seed): every CI
		// is connected to one of them or powered by it, so ci_relationship
		// has two targets and the planner expects 5,000 edges into any CI.
		{"hubs", `INSERT INTO ci (organization_id, client_id, site_id, ci_type_id, name, status)
			SELECT $1, s.client_id, s.id, t.id, t.key, 'active' FROM site s
			JOIN ci_type t ON t.key IN ('switch', 'pdu') AND t.organization_id IS NULL
			WHERE s.organization_id = $1 AND s.name = 'star-1'`, []any{f.OrgA}},
		{"edges", `WITH c AS (SELECT substring(name FROM 4)::int AS n, id FROM ci WHERE organization_id = $1 AND name LIKE 'st-%')
			INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			SELECT $1, c.id, h.id, CASE WHEN h.name = 'switch' THEN 'connected_to' ELSE 'powered_by' END
			FROM c JOIN ci h ON h.organization_id = $1 AND h.name = CASE WHEN c.n <= $2::int THEN 'switch' ELSE 'pdu' END`,
			[]any{f.OrgA, sources}},
	}
	for _, s := range seed {
		if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci, ci_relationship`); err != nil {
		t.Fatal(err)
	}
	var hub string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'switch'`, f.OrgA).Scan(&hub); err != nil {
		t.Fatal(err)
	}

	trace := scopetest.NewQueryTrace(t, f.App)
	engine := savedview.NewPGQueryEngine(trace.Pool)
	spec := &savedview.FilterSpec{EntityKind: "ci", UpstreamOf: hub}
	query := func() (int, int, error) {
		rows, total, err := engine.Query(f.OrgCtx(f.OrgA), f.OrgA, spec, api.PaginationParams{Limit: 50})
		return len(rows), total, err
	}
	var page, total int
	stmts := trace.Run(t, func() error {
		var err error
		page, total, err = query()
		return err
	})
	if page != 50 || total != sources {
		t.Errorf("upstream_of the switch: %d rows of %d, want 50 of %d", page, total, sources)
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	budget := &scopetest.QueryBudget{Tables: []string{"ci_relationship", "ci"}, Indexes: [][]string{{"idx_ci_rel_org_target"}}, MaxRows: 18000}
	for _, part := range []string{"SELECT COUNT(*) FROM ci", " ORDER BY "} {
		scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, "star of 5,000 sources", "upstream_of the switch ("+strings.TrimSpace(part)+")",
			scopetest.Statement(t, stmts, part), budget)
	}
	scopetest.TimeBudget(t, "upstream_of the switch, page of 50 rows", 5, 500*time.Millisecond, func() error {
		_, _, err := query()
		return err
	})
}
