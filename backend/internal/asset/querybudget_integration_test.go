package asset_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// budgetAccess resolves the roles of every user to the grants of the
// budget test (identity.AccessResolver).
type budgetAccess struct{ grants []identity.Grant }

func (a budgetAccess) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	return a.grants, nil
}

func (budgetAccess) RoleGrants(context.Context, string, []string) ([]identity.Grant, error) {
	return nil, nil
}

// TestAssetQueryBudget covers ABN-02 for the asset paths of Phase 2
// (WP-824, after the pattern of TestCIQueryBudget, WP-436) against
// PostgreSQL with 10,000 assets of organization A in a database of its own
// (scopetest.BudgetDatabase):
//
//   - asset.PGRepository.List and GET /api/v1/assets through asset.Handler
//     with the field projection of a role that may not read the purchase
//     price (WP-271, WP-265) send the same number of statements for 10 and
//     200 rows and stay within listBudget and handlerBudget; the field
//     permissions are read once per request (snapshot, teams, type chain),
//     not per row.
//   - The SQL the repositories really send (pgx tracer on a copy of the
//     application pool) is EXPLAINed with ANALYZE as the application role
//     in the tenant transaction after ANALYZE: the list filtered by
//     inventory number (filter[inventory_no]=) and the duplicate search of
//     the import (importer.PGRepository.FindAssets, WP-302) by inventory
//     number use idx_asset_tag_org, the duplicate search by the normalized
//     serial number idx_asset_org_serial_norm (WP-741); each reads at most
//     1 % of the organization's assets. Without the index the check names
//     it (negative case, dropped in a transaction that is rolled back).
//
// Both checks run with organization A alone and again with 5,000 assets of
// organization B. Whole lists with COUNT(*) and the search with ILIKE are
// left to k6 (WP-822). With RETICORA_PERF_BUDGET=1 the list of 200 rows
// and the lookups must stay below the 500 ms of NFR-02 (best of five).
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestAssetQueryBudget ./internal/asset/
func TestAssetQueryBudget(t *testing.T) {
	db := scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "2b")
	bg := context.Background()
	// The database goes with the test: the assets are truncated before the
	// fixture cleanup, which would delete them row by row with their
	// triggers. Only in that database.
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate asset: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE asset CASCADE`); err != nil {
			t.Errorf("truncate asset: %v", err)
		}
	})

	// seed inserts the assets from..to of org: inventory number BUDGET-n,
	// serial number " SN-n " (normal form sn-n), a purchase price; in
	// organization A clients 1 and 2 and no client in turn.
	seed := func(org string, clients []any, from, to int) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO asset (organization_id, client_id, inventory_no, name, serial_number,
				manufacturer, purchase_price, currency, created_at)
			SELECT $1, (ARRAY[$2::uuid, $3::uuid, NULL])[n % 3 + 1], 'BUDGET-' || n, 'budget-asset-' || n, ' SN-' || n || ' ',
				'ACME', 100 + n, 'EUR', '2026-01-01 00:00:00+00'::timestamptz + make_interval(secs => n)
			FROM generate_series($4::int, $5::int) AS n`, org, clients[0], clients[1], from, to); err != nil {
			t.Fatalf("seed assets %d-%d: %v", from, to, err)
		}
	}
	const orgAssets = 10000
	seed(f.OrgA, []any{f.Client1, f.Client2}, 1, orgAssets)
	seed(f.OrgB, []any{nil, nil}, 1, 50)
	analyze := func() {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `ANALYZE asset`); err != nil {
			t.Fatal(err)
		}
	}
	analyze()

	trace := scopetest.NewQueryTrace(t, f.App)
	repo := asset.NewPGRepository(trace.Pool)
	imports := importer.NewPGRepository(trace.Pool)
	ctx := f.OrgCtx(f.OrgA)

	// Statements of the repository list (no N+1).
	list := func(limit int) int {
		return len(trace.Run(t, func() error {
			items, total, err := repo.List(ctx, f.OrgA, &asset.FilterParams{}, api.PaginationParams{Limit: limit})
			if err == nil && (len(items) != limit || total != orgAssets) {
				t.Errorf("list %d: %d items of %d", limit, len(items), total)
			}
			return err
		}))
	}
	// Warm the pool: the first connection switches its role.
	list(10)
	small, large := list(10), list(200)
	if small != large {
		t.Errorf("asset list: %d statements for 10 rows, %d for 200 (N+1)", small, large)
	}
	// begin with the tenant context, count, sorting off for the page (the
	// default order without filters), page, commit.
	const listBudget = 5
	if large > listBudget {
		t.Errorf("asset list: %d statements, budget %d", large, listBudget)
	}
	scopetest.TimeBudget(t, "asset list of 200 rows", 5, 500*time.Millisecond, func() error {
		_, _, err := repo.List(ctx, f.OrgA, &asset.FilterParams{}, api.PaginationParams{Limit: 200})
		return err
	})

	// The same list through the handler with the field projection of a
	// role that may not read the purchase price (WP-271): the policy is
	// resolved by the field policy middleware from the PostgreSQL grants
	// at the traced pool, once per request.
	role := f.ID()
	perms := []identity.Permission{identity.PermAssetRead}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp824-asset-reader', '["asset:read"]')`,
		role, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level)
		VALUES ($1, $2, 'asset', 'core.purchase_cost', 'none')`, f.OrgA, role); err != nil {
		t.Fatal(err)
	}
	resolver := permission.NewFieldResolver(budgetAccess{grants: []identity.Grant{{RoleID: role, Permissions: perms}}}, nil,
		permission.NewPGFieldGrants(trace.Pool), nil)
	mux := chi.NewRouter()
	mux.Use(permission.FieldPolicyMiddleware(resolver))
	asset.NewHandler(repo).RegisterRoutes(mux)
	get := func(limit int) int {
		t.Helper()
		// Without the cached snapshot every request reads the field
		// permissions.
		resolver.Invalidate(bg, f.OrgA)
		var rec *httptest.ResponseRecorder
		stmts := trace.Run(t, func() error {
			reqCtx := tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
			reqCtx = identity.WithPrincipal(reqCtx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
				Type: identity.PrincipalTypeUser, Permissions: perms})
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/assets?limit=%d", limit), http.NoBody).WithContext(reqCtx))
			return nil
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/assets?limit=%d: %d %s", limit, rec.Code, rec.Body.String())
		}
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Data) != limit {
			t.Errorf("GET /api/v1/assets?limit=%d: %d rows", limit, len(page.Data))
		}
		for _, row := range page.Data {
			if _, ok := row["purchase_price"]; ok || row["inventory_no"] == nil {
				t.Fatalf("GET /api/v1/assets?limit=%d: not projected: %v", limit, row)
			}
		}
		return len(stmts)
	}
	get(10)
	smallHandler, largeHandler := get(10), get(200)
	if smallHandler != largeHandler {
		t.Errorf("GET /api/v1/assets with field projection: %d statements for 10 rows, %d for 200 (N+1)", smallHandler, largeHandler)
	}
	// The list and the field permissions of the request, each read in a
	// transaction of its own (begin with the tenant context, reads,
	// commit): the snapshot of the grants (field grants, object grants,
	// sensitive fields), the teams of the user and the type chain.
	const handlerBudget = listBudget + (2 + 3) + 3 + 3
	if largeHandler > handlerBudget {
		t.Errorf("GET /api/v1/assets with field projection: %d statements, budget %d", largeHandler, handlerBudget)
	}
	t.Logf("asset list: %d statements; through the handler with field projection: %d", large, largeHandler)

	// The plans.
	const maxRows = orgAssets / 100
	tables := []string{"asset"}
	byInventory := &scopetest.QueryBudget{Tables: tables, Indexes: [][]string{{"idx_asset_tag_org"}}, MaxRows: maxRows}
	bySerial := &scopetest.QueryBudget{Tables: tables, Indexes: [][]string{{"idx_asset_org_serial_norm"}}, MaxRows: maxRows}
	ids := func(cond string) []string {
		t.Helper()
		var out []string
		rows, err := f.Admin.Query(bg, `SELECT id::text FROM asset WHERE organization_id = $1 AND `+cond+` ORDER BY id`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		if len(out) != 1 {
			t.Fatalf("the fixture has %d assets with %s, want one", len(out), cond)
		}
		return out
	}
	want4242 := ids(`inventory_no = 'BUDGET-4242'`)
	calls := []struct {
		name   string
		match  string
		budget *scopetest.QueryBudget
		run    func() ([]string, error)
	}{
		{"list by inventory number (filter[inventory_no]=)", " ORDER BY ", byInventory, func() ([]string, error) {
			items, _, err := repo.List(ctx, f.OrgA, &asset.FilterParams{Filters: []api.Filter{{Field: "inventory_no", Op: api.OpExact,
				Values: []string{"BUDGET-4242"}}}}, api.PaginationParams{Limit: 50})
			out := make([]string, 0, len(items))
			for i := range items {
				out = append(out, items[i].ID)
			}
			return out, err
		}},
		{"import duplicate search by inventory number", "inventory_no = $2", byInventory, func() ([]string, error) {
			return imports.FindAssets(ctx, f.OrgA, "inventory_no", "BUDGET-4242")
		}},
		{"import duplicate search by normalized serial number", "serial_number_norm = $2", bySerial, func() ([]string, error) {
			return imports.FindAssets(ctx, f.OrgA, "serial_number", "  sn-4242")
		}},
	}
	// The default page of 50 assets.
	defaultPage := &scopetest.QueryBudget{Tables: tables, Indexes: [][]string{{"idx_asset_list_sort"}}, MaxRows: maxRows}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	check := func(state string) {
		t.Helper()
		for _, c := range calls {
			var got []string
			stmts := trace.Run(t, func() error {
				var err error
				got, err = c.run()
				return err
			})
			if strings.Join(got, ",") != want4242[0] {
				t.Errorf("%s, %s: found %v, want %v", state, c.name, got, want4242)
			}
			scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, c.name, scopetest.Statement(t, stmts, c.match), c.budget)
			scopetest.TimeBudget(t, state+", "+c.name, 5, 500*time.Millisecond, func() error {
				_, err := c.run()
				return err
			})
		}

		// The default list (created_at DESC, finding of the load run of
		// PR #147): the page reads its assets from idx_asset_list_sort with
		// the settings the list makes before it (enable_sort off), not the
		// whole organization sorted.
		stmts := trace.Run(t, func() error {
			items, total, err := repo.List(ctx, f.OrgA, &asset.FilterParams{}, api.PaginationParams{Limit: 50})
			if err == nil && (len(items) != 50 || total != orgAssets) {
				t.Errorf("%s: default list: %d items of %d, want 50 of %d", state, len(items), total, orgAssets)
			}
			return err
		})
		var plan []byte
		if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			for _, st := range stmts {
				if strings.Contains(st.SQL, " ORDER BY ") {
					break
				}
				if strings.HasPrefix(st.SQL, "SET LOCAL ") {
					if _, serr := tx.Exec(ctx, st.SQL); serr != nil {
						return serr
					}
				}
			}
			page := scopetest.Statement(t, stmts, " ORDER BY created_at ")
			return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+page.SQL, page.Args...).Scan(&plan)
		}); err != nil {
			t.Fatal(err)
		}
		reads := scopetest.ReadPlan(t, plan)
		if problem := reads.Problem(defaultPage); problem != "" {
			t.Errorf("ABN-02 violated: %s, default page: %s", state, problem)
		}
		t.Logf("%s, default page: %s", state, reads.Plan)
	}
	check("organization A alone")
	seed(f.OrgB, []any{nil, nil}, 51, 5050)
	analyze()
	check("with 5,000 assets of organization B")
}
