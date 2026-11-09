package consumable_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestConsumableListQueryBudget covers ABN-02 for the consumable list
// (finding of the load run of PR #147, phase2.js consumable_list) against
// PostgreSQL with 10,000 consumables of organization A, in clients 1 and 2
// and no client in turn, in a database of its own
// (scopetest.BudgetDatabase): the page of GET /consumables (sorted by
// name), the SQL the repository really sends, is EXPLAINed with
// ANALYZE as the application role in the tenant transaction after ANALYZE.
// The page reads its first entries from idx_consumable_list_name (000279)
// and at most 1 % of the organization's consumables, instead of reading
// and sorting all of them. Without the index the check names it (negative
// case, dropped in a transaction that is rolled back). Whether the count
// takes the index-only scan depends on the visibility map; it is left to
// k6.
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestConsumableListQueryBudget ./internal/consumable/
func TestConsumableListQueryBudget(t *testing.T) {
	db := scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "c7")
	bg := context.Background()
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE consumable CASCADE`); err != nil {
			t.Errorf("truncate: %v", err)
		}
	})
	const orgItems = 10000
	if _, err := f.Admin.Exec(bg, `INSERT INTO consumable (organization_id, client_id, name, sku)
		SELECT $1, (ARRAY[$2::uuid, $3::uuid, NULL])[n % 3 + 1], 'budget item ' || lpad(n::text, 5, '0'), 'BUDGET-' || n
		FROM generate_series(1, $4::int) AS n`, f.OrgA, f.Client1, f.Client2, orgItems); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `VACUUM ANALYZE consumable`); err != nil {
		t.Fatal(err)
	}

	trace := scopetest.NewQueryTrace(t, f.App)
	repo := consumable.NewPGRepository(trace.Pool)
	ctx := f.OrgCtx(f.OrgA)
	list := func() []scopetest.LoggedStatement {
		return trace.Run(t, func() error {
			items, total, err := repo.List(ctx, f.OrgA, consumable.FilterParams{}, api.PaginationParams{Limit: 50})
			if err == nil && (len(items) != 50 || total != orgItems || items[0].Name != "budget item 00001") {
				t.Errorf("list: %d items of %d, want 50 of %d from budget item 00001", len(items), total, orgItems)
			}
			return err
		})
	}
	// Warm the pool: the first connection switches its role.
	list()
	stmts := list()
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	page := &scopetest.QueryBudget{Tables: []string{"consumable"}, Indexes: [][]string{{"idx_consumable_list_name"}}, MaxRows: orgItems / 100}
	scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, "organization A", "page sorted by name", scopetest.Statement(t, stmts, " ORDER BY name "), page)
}
