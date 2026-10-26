package ci_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// budgetCall is one repository call of TestCIQueryBudget: the statement
// whose SQL contains match is EXPLAINed against budget.
type budgetCall struct {
	name   string
	match  string
	budget queryBudget
	// statements bounds the statements of the call (no N+1).
	statements int
	// run calls the repository and returns the ids it found; ordered tells
	// whether their order is part of the result.
	run     func() ([]string, error)
	ordered bool
	want    []string
}

// TestCIQueryBudget covers ABN-02 and NFR-10 (WP-204, WP-436; retest of
// F-115 and F-280) for the CI list, its filters, the detail and the
// identity search of reconciliation against PostgreSQL with 10,000 CIs:
// the list sends the same number of statements for 10 and 200 rows (no
// N+1) and the detail stays within its budget; the SQL the repositories
// really send (pgx tracer on a copy of the application pool) is EXPLAINed
// with ANALYZE as the application role in the tenant transaction, after
// ANALYZE of the tables, and must use the expected index and read at most
// 1 % of the organization's CIs (rows read from ci and ci_identity_mac,
// including the rows a filter removes). Without the index the check names
// it (negative case, the index dropped in a transaction that is rolled
// back).
//
// Before WP-436 the test EXPLAINed rebuilt queries (lower(hostname)
// instead of hostname_lc, attributes @> instead of the attribute filter)
// and rejected only "Seq Scan" (F-115). Ten runs of that test on freshly
// migrated databases failed ten times ("hostname/attribute lookup reads
// ci with a sequential scan", Seq Scan with 9,999 and 9,750 rows removed):
// with organization A as the only organization in ci (n_distinct 1) the
// sequential scan is the cheapest way to read the whole organization. With
// 2,000, 10,000 or 40,000 CIs of another organization in the table, as in a
// database other tests had used, the same queries read the organization
// through idx_ci_org_type or idx_ci_org_status (Bitmap Heap Scan, again
// 9,999 and 9,750 rows removed by the filter) and the test passed (F-280).
// Both plans read every CI of the organization. The checks below therefore
// run in a database of their own (budgetDatabase) in both states: first
// with organization A alone, then with 5,000 CIs of organization B added
// and analyzed; the expectations hold in both and the negative cases fail
// in both.
//
// Known gaps (queryBudget.gap): the name filter of the list (search= with
// name ILIKE, filter[name]=) and the list filter on hostname have no index
// the application role can use; they read the organization once. ILIKE
// (texticlike) is not leakproof, so under row level security no index can
// serve it (SR-04 class); for filter[name]= an index on
// (organization_id, name) is missing (WP-438), for filter[hostname]= the
// filter compares hostname while idx_ci_hostname_lc holds lower(hostname)
// (WP-438). The hostname lookup of the identity search uses hostname_lc.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=10 -run TestCIQueryBudget ./internal/ci/
func TestCIQueryBudget(t *testing.T) {
	db := budgetDatabase(t)
	f := scopetest.Seed(t, "c5")
	bg := context.Background()
	// The database goes with the test: the CIs are truncated before the
	// fixture cleanup, which would delete them row by row with their
	// triggers (about ten seconds per run). Only in that database.
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate ci: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE ci CASCADE`); err != nil {
			t.Errorf("truncate ci: %v", err)
		}
	})
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	types := citype.NewPGRepository(f.App).WithIndexScheduler(ddl)
	rack := &citype.AttributeDefinition{Key: "wp436_rack", DataType: "text", Searchable: true}
	if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, rack); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	// seed inserts the CIs from..to of org with the identity values of n:
	// serial BUDGET-n, host name Budget-Host-n, management IP, primary MAC
	// 02:…, an interface MAC 06:… in the fingerprint, rack R(n % 1000) (ten
	// CIs each), created_at in the order of insertion; in organization A
	// clients 1 and 2 and no client in turn.
	seed := func(org string, clients []any, from, to int) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, serial_number, hostname,
				management_ip, primary_mac, attributes, created_at)
			SELECT $1, (ARRAY[$3::uuid, $4::uuid, NULL])[n % 3 + 1], $2, 'budget-' || n,
				CASE WHEN n % 10 = 0 THEN 'maintenance' ELSE 'active' END, 'BUDGET-' || n, 'Budget-Host-' || n,
				('10.77.' || (n / 250) || '.' || (n % 250 + 1))::inet,
				('02:00:00:00:' || lpad(to_hex((n >> 8) & 255), 2, '0') || ':' || lpad(to_hex(n & 255), 2, '0'))::macaddr,
				jsonb_build_object('wp436_rack', 'R' || (n % 1000), 'fingerprint', jsonb_build_object('mac_addresses',
					jsonb_build_array('06:00:00:00:' || lpad(to_hex((n >> 8) & 255), 2, '0') || ':' || lpad(to_hex(n & 255), 2, '0')))),
				'2026-01-01 00:00:00+00'::timestamptz + make_interval(secs => n)
			FROM generate_series($5::int, $6::int) AS n`, org, serverType, clients[0], clients[1], from, to); err != nil {
			t.Fatalf("seed CIs %d-%d: %v", from, to, err)
		}
	}
	const orgCIs = 10000
	seed(f.OrgA, []any{f.Client1, f.Client2}, 1, orgCIs)
	seed(f.OrgB, []any{nil, nil}, 1, 50)
	if err := ddl.Ensure(bg, f.OrgA, rack.ID); err != nil {
		t.Fatalf("build the index of %s: %v", rack.Key, err)
	}
	// A global, not unique definition: the index of its key (WP-607).
	rackIndexes := attrIndexesOf(t, f.Admin, rack.Key)
	if problem := keyIndexProblem(rackIndexes, rack.Key); problem != "" {
		t.Fatalf("E-73: %s", problem)
	}
	rackIndex := rackIndexes[0].name
	analyze := func() {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `ANALYZE ci, ci_identity_mac`); err != nil {
			t.Fatal(err)
		}
	}
	analyze()

	trace := newQueryTrace(t, f.App)
	repo := ci.NewPGRepository(trace.pool)
	identities := discovery.NewPGRepository(trace.pool)
	ctx := f.OrgCtx(f.OrgA)

	list := func(limit int, filter ci.FilterParams) int {
		return len(trace.run(t, func() error {
			items, total, err := repo.List(ctx, f.OrgA, filter, api.PaginationParams{Limit: limit})
			if err == nil && (len(items) != limit || total < limit) {
				t.Errorf("list %d: %d items of %d", limit, len(items), total)
			}
			return err
		}))
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

	// ids returns the CIs of organization A matching cond, ordered.
	ids := func(cond string, order string, args ...any) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND (`+cond+`) ORDER BY `+order,
			append([]any{f.OrgA}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	detailID := ids(`serial_number = 'BUDGET-9999'`, "id")[0]
	// begin, tenant context, row, overrides, commit.
	const detailBudget = 5
	// begin with the tenant context, the page of candidates, commit.
	const identityBudget = 3
	// The row budget: 1 % of the organization's CIs.
	const maxRows = orgCIs / 100
	listItems := func(filter ci.FilterParams) func() ([]string, error) {
		return func() ([]string, error) {
			items, _, err := repo.List(ctx, f.OrgA, filter, api.PaginationParams{Limit: 50})
			out := make([]string, 0, len(items))
			for _, it := range items {
				out = append(out, it.ID)
			}
			return out, err
		}
	}
	identity := func(q discovery.IdentityQuery) func() ([]string, error) {
		return func() ([]string, error) { return identities.IdentityCandidates(ctx, f.OrgA, &q) }
	}
	exact := func(field, value string) ci.FilterParams {
		return ci.FilterParams{Filters: []api.Filter{{Field: field, Op: api.OpExact, Values: []string{value}}}}
	}
	const (
		listPage      = " ORDER BY ci."
		candidatePage = "SELECT id::text, created_at FROM ci"
	)
	serial := []string{"uq_ci_org_serial", "idx_ci_serial"}
	ip := []string{"idx_ci_mgmt_ip_sys_object", "idx_ci_mgmt_ip"}
	byID := []string{"ci_pkey", "ci_id_organization_key"}
	calls := []budgetCall{
		{name: "list", match: listPage, statements: listBudget, ordered: true,
			budget: queryBudget{indexes: [][]string{{listSortIndex}}, maxRows: maxRows},
			run:    listItems(ci.FilterParams{}), want: ids("true", "created_at DESC, id DESC LIMIT 50")},
		{name: "name filter (search=)", match: listPage, statements: listBudget,
			budget: queryBudget{gap: "WP-438/SR-04: name ILIKE (texticlike) is not leakproof, no index can serve it under row level security"},
			run:    listItems(ci.FilterParams{Search: "budget-4242"}), want: ids(`name = 'budget-4242'`, "id")},
		{name: "name filter (filter[name]=)", match: listPage, statements: listBudget,
			budget: queryBudget{gap: "WP-438: no index on (organization_id, name); name = $n (texteq) is leakproof and could use one"},
			run:    listItems(exact("name", "budget-4242")), want: ids(`name = 'budget-4242'`, "id")},
		{name: "hostname filter of the list (filter[hostname]=)", match: listPage, statements: listBudget,
			budget: queryBudget{gap: "WP-438: compares hostname, idx_ci_hostname_lc holds lower(hostname)"},
			run:    listItems(exact("hostname", "Budget-Host-4242")), want: ids(`hostname = 'Budget-Host-4242'`, "id")},
		{name: "attribute filter", match: listPage, statements: listBudget,
			budget: queryBudget{indexes: [][]string{{rackIndex}}, maxRows: maxRows},
			run:    listItems(exact(api.AttributePrefix+rack.Key, "R7")), want: ids(`attributes ->> 'wp436_rack' = 'R7'`, "id")},
		{name: "detail", match: " FROM ci WHERE id = $1", statements: detailBudget,
			budget: queryBudget{indexes: [][]string{byID}, maxRows: maxRows},
			run: func() ([]string, error) {
				item, err := repo.GetByID(ctx, f.OrgA, detailID)
				if err != nil {
					return nil, err
				}
				return []string{item.ID}, nil
			}, want: []string{detailID}},
		{name: "identity search by host name", match: candidatePage, statements: identityBudget,
			budget: queryBudget{indexes: [][]string{{"idx_ci_hostname_lc"}, {"idx_ci_fqdn_lc"}}, maxRows: maxRows},
			run:    identity(discovery.IdentityQuery{Hostnames: []string{"budget-host-4242"}}), want: ids(`hostname_lc = 'budget-host-4242'`, "id")},
		{name: "identity search by serial", match: candidatePage, statements: identityBudget,
			budget: queryBudget{indexes: [][]string{serial}, maxRows: maxRows},
			run:    identity(discovery.IdentityQuery{SerialNumber: "BUDGET-4242"}), want: ids(`serial_number = 'BUDGET-4242'`, "id")},
		{name: "identity search by interface MAC", match: candidatePage, statements: identityBudget,
			budget: queryBudget{indexes: [][]string{{"idx_ci_identity_mac"}, byID}, maxRows: maxRows},
			run:    identity(discovery.IdentityQuery{MACs: []string{"06:00:00:00:10:92"}}), want: ids(`serial_number = 'BUDGET-4242'`, "id")},
		{name: "identity search by management IP", match: candidatePage, statements: identityBudget,
			budget: queryBudget{indexes: [][]string{ip}, maxRows: maxRows},
			run:    identity(discovery.IdentityQuery{ManagementIP: "10.77.16.243"}), want: ids(`management_ip = '10.77.16.243'`, "id")},
		{name: "identity search by serial, MAC, IP and host name", match: candidatePage, statements: identityBudget,
			budget: queryBudget{indexes: [][]string{serial, {"idx_ci_identity_mac"}, byID, ip, {"idx_ci_hostname_lc"}, {"idx_ci_fqdn_lc"}}, maxRows: maxRows},
			run: identity(discovery.IdentityQuery{SerialNumber: "BUDGET-17", MACs: []string{"02:00:00:00:10:92"},
				ManagementIP: "10.77.39.250", Hostnames: []string{"budget-host-1234"}}),
			want: ids(`serial_number IN ('BUDGET-17', 'BUDGET-4242', 'BUDGET-9999', 'BUDGET-1234')`, "id")},
	}
	for _, c := range calls {
		if len(c.want) == 0 {
			t.Fatalf("%s: the fixture has no matching CI", c.name)
		}
	}

	// constraintIndex reports whether idx belongs to a constraint (primary
	// key, unique constraint); those are not dropped in the negative cases.
	constraintIndex := func(idx string) bool {
		var ok bool
		if err := f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conindid = to_regclass($1))`, idx).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	check := func(state string) {
		t.Helper()
		for _, c := range calls {
			var got []string
			stmts := trace.run(t, func() error {
				var err error
				got, err = c.run()
				return err
			})
			if !c.ordered {
				slices.Sort(got)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("%s, %s: found %v, want %v", state, c.name, got, c.want)
			}
			if len(stmts) > c.statements {
				t.Errorf("%s, %s: %d statements, budget %d", state, c.name, len(stmts), c.statements)
			}
			var stmt *loggedStatement
			for i, s := range stmts {
				if strings.Contains(s.sql, c.match) {
					if stmt != nil {
						t.Fatalf("%s, %s: more than one statement contains %q", state, c.name, c.match)
					}
					stmt = &stmts[i]
				}
			}
			if stmt == nil {
				t.Fatalf("%s, %s: no statement contains %q", state, c.name, c.match)
			}
			reads := readPlan(t, analyzeAsApp(t, f.App, &orgWide, *stmt))
			if problem := reads.problem(c.budget); problem != "" {
				t.Errorf("ABN-02 violated: %s, %s: %s\n%s", state, c.name, problem, strings.Join(strings.Fields(stmt.sql), " "))
			}
			if c.budget.gap != "" {
				t.Logf("%s, %s: known gap (%s): %d rows read: %s", state, c.name, c.budget.gap, reads.budgetRows(), reads.plan)
				continue
			}
			t.Logf("%s, %s: %d rows read: %s", state, c.name, reads.budgetRows(), reads.plan)

			// Negative case: without the index the check names it. Over ci
			// the fallback reads the whole organization; without
			// idx_ci_identity_mac the planner walks the primary key of
			// ci_identity_mac (ci_id, mac) entirely, which fetches few rows,
			// so there the index name is what fails.
			for _, group := range c.budget.indexes {
				if slices.ContainsFunc(group, constraintIndex) {
					continue
				}
				without := readPlan(t, analyzeWithout(t, f.Admin, f.OrgA, *stmt, group...))
				if problem := without.problem(c.budget); !strings.Contains(problem, "no scan using "+strings.Join(group, " or ")+" (missing index?)") {
					t.Errorf("%s, %s without %v: the check reports %q, want the missing index named", state, c.name, group, problem)
					continue
				}
				t.Logf("%s, %s without %v: %d rows read: %s", state, c.name, group, without.budgetRows(), without.plan)
			}
		}
	}

	// Organization A alone in ci: here the queries of the old check read the
	// organization with a sequential scan and failed.
	check("organization A alone")
	// CIs of another organization with the same identity values, as in a
	// shared database: here they read it through an organization index and
	// passed.
	seed(f.OrgB, []any{nil, nil}, 51, 5050)
	analyze()
	check("with 5,000 CIs of organization B")
}
