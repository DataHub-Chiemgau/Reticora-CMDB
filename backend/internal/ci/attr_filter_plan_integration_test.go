package ci_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// explainInScope returns the plan (EXPLAIN FORMAT JSON) of stmt as the
// application role inside a transaction of scope, so the row level
// policies are part of it.
func explainInScope(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt loggedStatement) []byte {
	t.Helper()
	var plan []byte
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain %s: %v", stmt.sql, err)
	}
	return plan
}

// attrIndexProblem returns why the plan of an attribute filter does not
// read the CIs through the attribute index, or "" when it does: an index
// scan of index on ci whose index condition compares the attribute value
// (reticora_attr_text over key), and no sequential scan of ci.
func attrIndexProblem(t *testing.T, plan []byte, index, key string) string {
	t.Helper()
	nodes := planNodes(t, plan)
	value := "reticora_attr_text(attributes, '" + key + "'::text)"
	var problems []string
	uses := false
	for _, n := range nodes {
		switch n["Node Type"] {
		case "Index Scan", "Index Only Scan", "Bitmap Index Scan":
			if cond, _ := n["Index Cond"].(string); n["Index Name"] == index && strings.Contains(cond, value) {
				uses = true
			}
		case "Seq Scan":
			if n["Relation Name"] == "ci" {
				problems = append(problems, "reads ci sequentially")
			}
		}
	}
	if !uses {
		problems = append([]string{"no index scan using " + index + " with " + value + " in the index condition (missing index?)"}, problems...)
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") + ": " + describePlan(nodes)
}

// explainGenericInScope returns the generic plan of stmt (EXPLAIN
// (GENERIC_PLAN, FORMAT JSON)) as the application role inside a transaction
// of scope: the plan a prepared statement runs once PostgreSQL stops
// planning it per execution, with every parameter unknown.
func explainGenericInScope(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt loggedStatement) []byte {
	t.Helper()
	var plan []byte
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		// The simple protocol leaves the parameters unbound; pgx would
		// require arguments for them.
		res, err := tx.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+stmt.sql).ReadAll()
		if err != nil {
			return err
		}
		if len(res) != 1 || len(res[0].Rows) != 1 {
			return fmt.Errorf("EXPLAIN returned %d results", len(res))
		}
		plan = res[0].Rows[0][0]
		return nil
	}); err != nil {
		t.Fatalf("explain (generic plan) %s: %v", stmt.sql, err)
	}
	return plan
}

// pageInScope runs stmt, a page of the CI list, with args as the
// application role in a transaction of scope, in a generic plan of the
// prepared statement when generic is set (else a custom plan), and returns
// the ids sorted. It fails the test when PostgreSQL did not run the plan
// asked for.
func pageInScope(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt loggedStatement, args []any, generic bool) []string {
	t.Helper()
	var ids []string
	mode := "force_custom_plan"
	if generic {
		mode = "force_generic_plan"
	}
	plans := func(ctx context.Context, tx pgx.Tx) int64 {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(generic_plans), 0)::bigint FROM pg_prepared_statements WHERE statement = $1`,
			stmt.sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = "+mode); err != nil {
			return err
		}
		before := plans(ctx, tx)
		rows, err := tx.Query(ctx, stmt.sql, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			values, verr := rows.Values()
			if verr != nil {
				rows.Close()
				return verr
			}
			ids = append(ids, values[0].(string))
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		// The statement is prepared on the session (pgx statement cache);
		// a generic plan shows in its counter.
		if ran := plans(ctx, tx) > before; ran != generic {
			t.Errorf("%s: the page ran a generic plan %v, want %v", mode, ran, generic)
		}
		return nil
	}); err != nil {
		t.Fatalf("run %s (%s): %v", stmt.sql, mode, err)
	}
	slices.Sort(ids)
	return ids
}

// tracedList serves GET /cis from a pool of one session (so the plan cache
// of a statement can be inspected) whose statements are logged.
type tracedList struct {
	pool *pgxpool.Pool
	log  *statementLog
	mux  *chi.Mux
}

func newTracedList(t *testing.T, app *pgxpool.Pool) *tracedList {
	t.Helper()
	l := &tracedList{log: &statementLog{}, mux: chi.NewRouter()}
	cfg := app.Config()
	cfg.ConnConfig.Tracer = l.log
	cfg.MinConns = 0
	cfg.MaxConns = 1
	var err error
	if l.pool, err = pgxpool.NewWithConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.pool.Close)
	ci.NewHandler(ci.NewService(ci.NewPGRepository(l.pool))).RegisterRoutes(l.mux)
	return l
}

// list runs GET /cis with query, requires 200 and returns the ids and the
// list statement it sent.
func (l *tracedList) list(t *testing.T, ctx context.Context, query url.Values) ([]string, loggedStatement) {
	t.Helper()
	mark := l.log.mark()
	w := httptest.NewRecorder()
	l.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /cis?%s: %d %s", query.Encode(), w.Code, w.Body.String())
	}
	var page listPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var stmt *loggedStatement
	for _, s := range l.log.since(mark) {
		if strings.Contains(s.sql, " FROM ci WHERE ") && strings.Contains(s.sql, " ORDER BY ci.") {
			stmt = &s
		}
	}
	if stmt == nil {
		t.Fatalf("GET /cis?%s sent no list statement", query.Encode())
	}
	ids := make([]string, 0, len(page.Data))
	for _, row := range page.Data {
		ids = append(ids, row.ID)
	}
	slices.Sort(ids)
	return ids, *stmt
}

// TestAttributeFilterUsesKeyIndex covers WP-607 (NFR-02, ABN-02; E-73,
// supplement of 2026-10-26): organizations A and B make the same key
// searchable (not unique, no CI type), the DDL job builds the one index of
// the key, and the attribute filter of GET /cis is served by it for the
// application role under row level security, in the custom plan and in the
// generic plan of the prepared statement: the filter SQL holds the key as
// a literal like the index (a parameter for the key matched no index
// expression in a generic plan) and names the organization, which leads the
// index, so organization_id and the value are the index condition in both
// plans; no sequential scan of ci. The pages the custom and the generic
// plan return hold the CIs of organization A with the value within the
// principal's scope (org-wide and client 1), the same as the former
// predicate (->>), never CIs of organization B with the same values. The
// index holds the CIs of both organizations; row level security keeps them
// apart unchanged: the list statement of A run with the id of organization
// B in the tenant transaction of A returns nothing in either plan, while
// it returns B's CIs in a transaction of B. Before WP-607 the key was a
// parameter and each organization had its own index partial on it: the
// generic plan read the organization through idx_ci_org or
// idx_ci_org_status (409 buffers instead of 210 at 10,000 CIs).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAttributeFilterUsesKeyIndex' ./internal/ci/
func TestAttributeFilterUsesKeyIndex(t *testing.T) {
	f := scopetest.Seed(t, "cf")
	bg := context.Background()
	// A key of this run (see TestAttributeIndexPerKey).
	key := fmt.Sprintf("wp607_site_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, x := range attrIndexesOf(t, f.Admin, key) {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+pgx.Identifier{x.name}.Sanitize())
		}
	})
	ctxOf := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	types := citype.NewPGRepository(f.App)
	for _, org := range []string{f.OrgA, f.OrgB} {
		d := &citype.AttributeDefinition{Key: key, DataType: "text", Searchable: true}
		if err := types.CreateAttributeDefinition(ctxOf(org), org, d); err != nil {
			t.Fatalf("create the definition in organization %s: %v", org, err)
		}
		defer func(org, id string) {
			if err := types.DeleteAttributeDefinition(ctxOf(org), org, id); err == nil {
				_ = ddl.Ensure(bg, org, id)
			}
		}(org, d.ID)
	}
	// 10,000 CIs in organization A (clients 1 and 2 and no client in turn,
	// values S0…S999, ten CIs each, created_at in the order of insertion),
	// 5,000 in organization B with the same values.
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes, created_at)
		SELECT $1, (ARRAY[$3::uuid, $4::uuid, NULL])[i % 3 + 1], `+server+`, 'wp607-' || i, 'active',
			jsonb_build_object($2::text, 'S' || (i % 1000)), now() - make_interval(secs => 10000 - i)
		FROM generate_series(1, 10000) AS i`, f.OrgA, key, f.Client1, f.Client2); err != nil {
		t.Fatalf("seed CIs of organization A: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, attributes)
		SELECT $1, `+server+`, 'wp607-b-' || i, 'active', jsonb_build_object($2::text, 'S' || (i % 1000))
		FROM generate_series(1, 5000) AS i`, f.OrgB, key); err != nil {
		t.Fatalf("seed CIs of organization B: %v", err)
	}
	// The index jobs of both definitions, run directly.
	for _, org := range []string{f.OrgA, f.OrgB} {
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM attribute_definition WHERE organization_id = $1 AND key = $2`, org, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := ddl.Ensure(bg, org, id); err != nil {
			t.Fatalf("index job of organization %s: %v", org, err)
		}
	}
	indexes := attrIndexesOf(t, f.Admin, key)
	if problem := keyIndexProblem(indexes, key); problem != "" {
		t.Fatalf("E-73: %s", problem)
	}
	keyIndex := indexes[0].name
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}

	traced := newTracedList(t, f.App)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgB := database.OrgWideScope(f.OrgB, f.User)
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	// expected returns the CIs of org with one of values (->>), within
	// clients (all when empty).
	expected := func(org string, values []string, clients []string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci
			WHERE organization_id = $1 AND deleted_at IS NULL AND attributes ->> $2::text = ANY($3::text[])
			  AND (COALESCE(cardinality($4::uuid[]), 0) = 0 OR client_id IS NULL OR client_id = ANY($4::uuid[]))
			ORDER BY id`, org, key, values, clients)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	for _, c := range []struct {
		name    string
		ctx     context.Context
		scope   *database.TenantScope
		clients []string
		values  []string
		query   url.Values
	}{
		{"org-wide", ctxOf(f.OrgA), &orgWide, nil, []string{"S7"}, url.Values{"filter[attributes." + key + "]": {"S7"}}},
		{"client 1", client1Ctx, &client1, []string{f.Client1}, []string{"S7"}, url.Values{"filter[attributes." + key + "]": {"S7"}}},
		{"org-wide, in", ctxOf(f.OrgA), &orgWide, nil, []string{"S17", "S18", "S999"},
			url.Values{"filter[attributes." + key + "][in]": {"S17,S18,S999"}}},
	} {
		ids, stmt := traced.list(t, c.ctx, c.query)
		want := expected(f.OrgA, c.values, c.clients)
		if !slices.Equal(ids, want) || len(want) == 0 {
			t.Errorf("%s: hits %v, want the CIs of organization A %v", c.name, ids, want)
		}
		if !strings.Contains(stmt.sql, "reticora_attr_text(attributes, '"+key+"') ") {
			t.Errorf("%s: the filter does not hold the key as a literal: %s", c.name, stmt.sql)
		}
		for _, generic := range []bool{false, true} {
			plan, kind := explainInScope(t, f.App, c.scope, stmt), "custom"
			if generic {
				plan, kind = explainGenericInScope(t, f.App, c.scope, stmt), "generic"
			}
			problem := attrIndexProblem(t, plan, keyIndex, key)
			for _, n := range planNodes(t, plan) {
				if cond, _ := n["Index Cond"].(string); n["Index Name"] == keyIndex && !strings.Contains(cond, "organization_id = ") {
					problem += "; the index condition does not name the organization: " + cond
				}
			}
			if problem != "" {
				t.Errorf("NFR-02/ABN-02 violated: %s, %s plan: the attribute filter does not use the index of the key for the application role: %s\n%s",
					c.name, kind, problem, stmt.sql)
			}
			t.Logf("%s, %s plan: %s", c.name, kind, describePlan(planNodes(t, plan)))

			// The rows of the plan: those of organization A in scope.
			if got := pageInScope(t, f.App, c.scope, stmt, stmt.args, generic); !slices.Equal(got, want) {
				t.Errorf("%s, %s plan: rows %v, want the CIs of organization A %v", c.name, kind, got, want)
			}
			// The statement of A with the id of organization B: nothing in
			// the transaction of A, B's CIs in a transaction of B.
			other := slices.Clone(stmt.args)
			replaced := false
			for i, a := range other {
				if s, ok := a.(string); ok && s == f.OrgA {
					other[i], replaced = f.OrgB, true
				}
			}
			if !replaced {
				t.Fatalf("%s: the list statement does not name organization A: %v", c.name, stmt.args)
			}
			if got := pageInScope(t, f.App, c.scope, stmt, other, generic); len(got) != 0 {
				t.Errorf("tenant isolation violated: %s, %s plan: the filter of organization A with the id of organization B returns %d CIs of B",
					c.name, kind, len(got))
			}
			if c.clients == nil {
				if got, wantB := pageInScope(t, f.App, &orgB, stmt, other, generic), expected(f.OrgB, c.values, nil); !slices.Equal(got, wantB) || len(wantB) == 0 {
					t.Errorf("%s, %s plan: in a transaction of organization B the statement returns %v, want %v", c.name, kind, got, wantB)
				}
			}
		}
	}
}

// TestAttributeFilterUsesIndexUnderRLS covers WP-435 (ABN-02, NFR-02;
// retest of F-114, attribute part; E-73 option A): an attribute filter of
// GET /cis is served by the attribute index idx_attr_<id> for the
// application role under row level security. Before migration 000162 the
// filter compared attributes ->> key and the DDL job built the index over
// the same expression; jsonb_object_field_text is not leakproof, so below
// the RLS barrier the comparison was evaluated after the policies of ci and
// reticora_app read every CI of the organization ("Seq Scan", 10,119 rows
// removed by the filter at 10,000 CIs), while the owner got the index.
// Index and filter now use reticora_attr_text, a leakproof wrapper the
// planner does not inline; an inlinable variant would be resolved to ->>
// again and the plan would fall back to the scan.
//
// The test creates a type definition and a global unique definition
// through the citype repository, lets the DDL job (Ensure) build their
// indexes over 10,000 CIs, captures the SQL the list sends (pgx tracer on a copy of the
// application pool) and EXPLAINs it as the application role in the tenant
// transaction, org-wide and restricted to a client: an index scan of the
// definition's index with the attribute value in the index condition, no
// sequential scan of ci, for equality on a type definition (with its type
// filter: the index is partial on the type; and without one, WP-566: the
// list restricts the hits to the types that make the key searchable) and
// on a global unique
// definition, and for an in list of five values. The hits equal those of
// the former predicate (->>) within the principal's scope (client 1 does
// not see the hits of client 2); CIs of organization B with the same
// values never appear. created_at follows the order of insertion, which
// makes the competing plan (the page from idx_ci_list_sort, the filter on
// every CI) cheap: without COST 1000 on the function the in list took it.
// A prepared list statement keeps the index after the plan cache's custom
// plans. Negative case: without the index the check names it.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAttributeFilterUsesIndexUnderRLS' ./internal/ci/
func TestAttributeFilterUsesIndexUnderRLS(t *testing.T) {
	f := scopetest.Seed(t, "cf")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	types := citype.NewPGRepository(f.App).WithIndexScheduler(ddl)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp435-rack-host", Name: "wp435-rack-host", IsObservable: true}
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	rack := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp435_rack", DataType: "text", Searchable: true}
	tag := &citype.AttributeDefinition{Key: "wp435_tag", DataType: "text", Searchable: true, Unique: true}
	for _, d := range []*citype.AttributeDefinition{rack, tag} {
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
			t.Fatalf("create definition %s: %v", d.Key, err)
		}
	}
	rackIndex, tagIndex := citype.IndexName(rack.ID, false), citype.IndexName(tag.ID, true)
	t.Cleanup(func() {
		for _, name := range []string{rackIndex, tagIndex} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+name)
		}
	})

	// 10,000 CIs of the type in organization A: clients 1 and 2 and no
	// client in turn, rack R0…R999 (ten CIs each), a unique tag, created_at
	// in the order of insertion as in a grown inventory (this makes reading
	// the page through idx_ci_list_sort cheap for the planner); organization
	// B holds CIs with the same values.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes, created_at)
		SELECT $1, (ARRAY[$3::uuid, $4::uuid, NULL])[i % 3 + 1], $2, 'wp435-' || i, 'active',
			jsonb_build_object('wp435_rack', 'R' || (i % 1000), 'wp435_tag', 'T' || i), now() - make_interval(secs => 10000 - i)
		FROM generate_series(1, 10000) AS i`, f.OrgA, typ.ID, f.Client1, f.Client2); err != nil {
		t.Fatalf("seed 10k CIs: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, attributes)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp435-b-' || i, 'active',
			jsonb_build_object('wp435_rack', 'R' || (i % 1000), 'wp435_tag', 'T' || i)
		FROM generate_series(1, 50) AS i`, f.OrgB); err != nil {
		t.Fatalf("seed CIs of organization B: %v", err)
	}
	// The index jobs the definitions queued, run directly (other queued jobs
	// of a shared database do not delay them).
	for _, d := range []*citype.AttributeDefinition{rack, tag} {
		if err := ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
			t.Fatalf("build the index of %s: %v", d.Key, err)
		}
	}
	for _, name := range []string{rackIndex, tagIndex} {
		var valid bool
		if err := f.Admin.QueryRow(bg, `SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, name).Scan(&valid); err != nil || !valid {
			t.Fatalf("the DDL job did not build %s: %v", name, err)
		}
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}

	traced := newTracedList(t, f.App)
	list := traced.list
	// former returns the CIs of organization A the former predicate on
	// attributes ->> key matches, within the clients (all when empty).
	former := func(t *testing.T, key string, values []string, typeID string, clients []string) []string {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT id::text FROM ci
			WHERE organization_id = $1 AND deleted_at IS NULL AND attributes ->> $2::text = ANY($3::text[])
			  AND ($4::text = '' OR ci_type_id = NULLIF($4::text, '')::uuid)
			  AND (COALESCE(cardinality($5::uuid[]), 0) = 0 OR client_id IS NULL OR client_id = ANY($5::uuid[]))
			ORDER BY id`, f.OrgA, key, values, typeID, clients)
		if qerr != nil {
			t.Fatal(qerr)
		}
		ids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			t.Fatal(qerr)
		}
		return ids
	}

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	principals := []struct {
		name    string
		ctx     context.Context
		scope   *database.TenantScope
		clients []string
	}{
		{"org-wide", orgCtx, &orgWide, nil},
		{"client 1", client1Ctx, &client1, []string{f.Client1}},
	}
	cases := []struct {
		name, index, key, typeID string
		values                   []string
		query                    url.Values
	}{
		{"type definition with its type", rackIndex, rack.Key, typ.ID, []string{"R7"},
			url.Values{"filter[attributes.wp435_rack]": {"R7"}, "filter[ci_type_id]": {typ.ID}}},
		{"global unique definition", tagIndex, tag.Key, "", []string{"T4242"},
			url.Values{"filter[attributes.wp435_tag]": {"T4242"}}},
		{"global unique definition, in", tagIndex, tag.Key, "", []string{"T17", "T18", "T4243", "T9999", "T10001"},
			url.Values{"filter[attributes.wp435_tag][in]": {"T17,T18,T4243,T9999,T10001"}}},
		// WP-566: without a type filter the list restricts the hits to the
		// types that make the key searchable, here the one of the
		// definition, and its index serves that restriction.
		{"type definition without a type filter", rackIndex, rack.Key, typ.ID, []string{"R7"},
			url.Values{"filter[attributes.wp435_rack]": {"R7"}}},
	}
	var rackStmt loggedStatement
	for _, c := range cases {
		for _, p := range principals {
			ids, stmt := list(t, p.ctx, c.query)
			if want := former(t, c.key, c.values, c.typeID, p.clients); !slices.Equal(ids, want) || len(want) == 0 {
				t.Errorf("%s, %s: hits %v, former predicate (->>) %v", c.name, p.name, ids, want)
			}
			plan := explainInScope(t, f.App, p.scope, stmt)
			if problem := attrIndexProblem(t, plan, c.index, c.key); problem != "" {
				t.Errorf("NFR-02/ABN-02 violated: %s, %s: the attribute filter is not index-based for the application role: %s\n%s",
					c.name, p.name, problem, stmt.sql)
			}
			t.Logf("%s, %s: %d hits, %s", c.name, p.name, len(ids), describePlan(planNodes(t, plan)))
			if c.name == cases[0].name && p.scope == &orgWide {
				rackStmt = stmt
			}
		}
	}

	// The list statement is prepared on the session (pgx statement cache).
	// After the plan cache's five custom plans PostgreSQL may switch to a
	// generic plan, which cannot use the index of a type definition (it is
	// partial on the organization and the type, parameters there); it must
	// keep planning per execution.
	for range 8 {
		list(t, orgCtx, cases[0].query)
	}
	var generic, custom int64
	if err := traced.pool.QueryRow(bg, `SELECT generic_plans, custom_plans FROM pg_prepared_statements WHERE statement = $1`,
		rackStmt.sql).Scan(&generic, &custom); err != nil {
		t.Fatalf("prepared list statement: %v", err)
	}
	if generic != 0 || custom < 8 {
		t.Errorf("the prepared list statement ran %d generic and %d custom plans; a generic plan cannot use %s", generic, custom, rackIndex)
	}
	t.Logf("prepared list statement: %d custom, %d generic plans", custom, generic)

	// Without the index the check names it (negative case).
	if _, err := f.Admin.Exec(bg, `DROP INDEX `+rackIndex); err != nil {
		t.Fatal(err)
	}
	problem := attrIndexProblem(t, explainInScope(t, f.App, &orgWide, rackStmt), rackIndex, rack.Key)
	if !strings.Contains(problem, "no index scan using "+rackIndex) {
		t.Errorf("without %s the check reports %q, want the missing index", rackIndex, problem)
	}
}

// TestAttrTextIsLeakproof covers the security promise of
// reticora_attr_text (migration 000162, WP-435, E-73): LEAKPROOF lets the
// planner evaluate the function on rows the policies of ci hide, so it must
// not raise an error or reveal anything for any input. For a table of
// inputs (NULL document or key, objects with values of every JSON type,
// arrays, scalars, JSON null, empty, quoted and non-ASCII keys, a key and a
// value of 10 MB, a document nested 1,000 levels deep) it returns exactly
// attributes ->> key as the application role and raises nothing. The
// catalog shows a PL/pgSQL function (never inlined; EXPLAIN keeps the call)
// that is LEAKPROOF, IMMUTABLE, STRICT, PARALLEL SAFE, SECURITY INVOKER
// with search_path pinned to pg_catalog and owned by a superuser, so
// nobody else can redefine it; it runs in a parallel worker.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttrTextIsLeakproof(t *testing.T) {
	f := scopetest.Seed(t, "cf")
	bg := context.Background()

	var (
		lang, volatile, parallel, owner string
		leakproof, strict, definer      bool
		superuser                       bool
		config                          []string
		cost                            float64
	)
	if err := f.Admin.QueryRow(bg, `SELECT l.lanname, p.proleakproof, p.provolatile::text, p.proparallel::text, p.proisstrict,
			p.prosecdef, COALESCE(p.proconfig, '{}'), r.rolname, r.rolsuper, p.procost
		FROM pg_proc p JOIN pg_language l ON l.oid = p.prolang JOIN pg_roles r ON r.oid = p.proowner
		WHERE p.oid = 'public.reticora_attr_text(jsonb, text)'::regprocedure`).
		Scan(&lang, &leakproof, &volatile, &parallel, &strict, &definer, &config, &owner, &superuser, &cost); err != nil {
		t.Fatalf("reticora_attr_text: %v", err)
	}
	if lang != "plpgsql" || !leakproof || volatile != "i" || parallel != "s" || !strict || definer ||
		!slices.Equal(config, []string{"search_path=pg_catalog"}) || !superuser || cost != 1000 {
		t.Errorf("reticora_attr_text: language %s, leakproof %v, volatility %s, parallel %s, strict %v, security definer %v, config %v, owner %s (superuser %v), cost %v; "+
			"want plpgsql, leakproof, i, s, strict, invoker, search_path=pg_catalog, a superuser, 1000",
			lang, leakproof, volatile, parallel, strict, definer, config, owner, superuser, cost)
	}

	huge := strings.Repeat("k", 10<<20)
	nested := `{"k":` + strings.Repeat("[", 1000) + strings.Repeat("]", 1000) + `}`
	inputs := []struct {
		name      string
		doc, key  any
		wantFound bool // the result is not NULL
	}{
		{"NULL document", nil, "k", false},
		{"NULL key", `{"k": "v"}`, nil, false},
		{"both NULL", nil, nil, false},
		{"empty object", `{}`, "k", false},
		{"string", `{"k": "v"}`, "k", true},
		{"other key", `{"k": "v"}`, "x", false},
		{"number", `{"k": 1.50}`, "k", true},
		{"large number", `{"k": 1e1000}`, "k", true},
		{"negative zero", `{"k": -0}`, "k", true},
		{"boolean", `{"k": true}`, "k", true},
		{"JSON null value", `{"k": null}`, "k", false},
		{"object value", `{"k": {"a": [1, "x"]}}`, "k", true},
		{"array value", `{"k": [1, 2]}`, "k", true},
		{"array document", `[1, 2, 3]`, "k", false},
		{"array document, index as key", `[1, 2, 3]`, "0", false},
		{"string document", `"k"`, "k", false},
		{"number document", `42`, "k", false},
		{"boolean document", `true`, "k", false},
		{"JSON null document", `null`, "k", false},
		{"empty key", `{"": "e"}`, "", true},
		{"quoted key", `{"a'b\"c\\d": "q"}`, `a'b"c\d`, true},
		{"non-ASCII key", `{"Größe ✓": "g"}`, "Größe ✓", true},
		{"huge key, missing", `{"k": "v"}`, huge, false},
		{"huge key, present", `{"` + huge + `": "h"}`, huge, true},
		{"huge value", `{"k": "` + huge + `"}`, "k", true},
		{"deeply nested value", nested, "k", true},
	}
	scope := database.OrgWideScope(f.OrgA, f.User)
	for _, in := range inputs {
		var got, want *string
		err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT reticora_attr_text($1::jsonb, $2::text), $1::jsonb ->> $2::text`, in.doc, in.key).Scan(&got, &want)
		})
		if err != nil {
			t.Errorf("%s: reticora_attr_text raised %.300v; a leakproof function must not raise for any input", in.name, err)
			continue
		}
		if (got == nil) != (want == nil) || (got != nil && *got != *want) || (got != nil) != in.wantFound {
			t.Errorf("%s: reticora_attr_text = %s, ->> = %s (want found %v)", in.name, short(got), short(want), in.wantFound)
		}
	}

	// Not inlined: the plan keeps the call (an inlined body would show ->>,
	// which is not leakproof), also in a parallel worker.
	err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL debug_parallel_query = on`); err != nil {
			return err
		}
		const q = `SELECT count(reticora_attr_text(jsonb_build_object('k', i::text), 'k')) FROM generate_series(1, 1000) AS i`
		rows, err := tx.Query(ctx, "EXPLAIN (VERBOSE, COSTS OFF) "+q)
		if err != nil {
			return err
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "reticora_attr_text(") || strings.Contains(plan, "->>") || !strings.Contains(plan, "Gather") {
			t.Errorf("reticora_attr_text is inlined or not run in a parallel worker:\n%s", plan)
		}
		var n int64
		if err := tx.QueryRow(ctx, q).Scan(&n); err != nil || n != 1000 {
			return fmt.Errorf("parallel call: %d, %w", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("plan of a call: %v", err)
	}
}

// short shortens a result for a message.
func short(s *string) string {
	if s == nil {
		return "NULL"
	}
	if len(*s) > 40 {
		return fmt.Sprintf("%q… (%d bytes)", (*s)[:40], len(*s))
	}
	return fmt.Sprintf("%q", *s)
}
