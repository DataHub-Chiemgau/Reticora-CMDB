package ticket_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// TestTicketQueryBudget covers ABN-02 for the ticket list of Phase 2
// (WP-824, after the pattern of TestCIQueryBudget, WP-436) against
// PostgreSQL with 10,000 tickets of organization A, related to 10,000 CIs
// and 10,000 assets, in a database of its own (scopetest.BudgetDatabase):
//
//   - ticket.PGRepository.List sends the same number of statements for 10
//     and 200 rows and stays within listBudget.
//   - The page of the list sorted by ticket_number, the SQL the repository
//     really sends (pgx tracer on a copy of the application pool), is
//     EXPLAINed with ANALYZE as the application role in the tenant
//     transaction after ANALYZE: it reads the page from the unique index of
//     (organization_id, ticket_number), ticket_organization_number_key
//     (000214 replaced idx_ticket_number by that constraint), and reads at
//     most 1 % of the organization's tickets. Without the index the check
//     names it (negative case, dropped with its constraint in a transaction
//     that is rolled back).
//   - The checks of ticketVisible look the related CI and asset of each
//     ticket of the page up by id (ci_pkey, asset_pkey) and the whole
//     statement reads at most 1 % of the organization's tickets, CIs and
//     assets (visibleBudget; until the WP-824 finding was fixed they read
//     every CI and asset of the organization through hashed subplans).
//
// Both run with organization A alone and again with 5,000 tickets of
// organization B. The default sort (created_at), the COUNT(*) of the whole
// list and the search with ILIKE are left to k6 (WP-822). With
// RETICORA_PERF_BUDGET=1 the list of 200 rows and the page sorted by
// number must stay below the 500 ms of NFR-02 (best of five).
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestTicketQueryBudget ./internal/ticket/
func TestTicketQueryBudget(t *testing.T) {
	db := scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	// The database goes with the test: tickets, CIs and assets are
	// truncated before the fixture cleanup, which would delete them row by
	// row with their triggers. Only in that database.
	t.Cleanup(func() {
		var current string
		if err := f.Admin.QueryRow(bg, `SELECT current_database()`).Scan(&current); err != nil || current != db {
			t.Errorf("truncate: connected to %q, want %s: %v", current, db, err)
			return
		}
		if _, err := f.Admin.Exec(bg, `TRUNCATE ticket, asset, ci CASCADE`); err != nil {
			t.Errorf("truncate: %v", err)
		}
	})
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	reporterA := f.AppUser(t, f.OrgA, "wp824-reporter")
	var reporterB string
	if err := f.Admin.QueryRow(bg, `INSERT INTO app_user (organization_id, oidc_subject, email, display_name)
		VALUES ($1, 'wp824-reporter-b', 'wp824-reporter-b@scopetest.invalid', 'Reporter B') RETURNING id::text`, f.OrgB).Scan(&reporterB); err != nil {
		t.Fatal(err)
	}

	// seed inserts the CIs, assets and tickets from..to of org: ticket n
	// relates to CI n when n is even, to asset n when n is divisible by
	// three (both, one or none); the CIs and assets of organization A are
	// in clients 1 and 2 and no client in turn. The trigger numbers the
	// tickets in the order of insertion.
	seed := func(org, reporter string, clients []any, from, to int) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
			SELECT $1, (ARRAY[$3::uuid, $4::uuid, NULL])[n % 3 + 1], $2, 'ticket-ci-' || n, 'active'
			FROM generate_series($5::int, $6::int) AS n`, org, serverType, clients[0], clients[1], from, to); err != nil {
			t.Fatalf("seed CIs %d-%d: %v", from, to, err)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO asset (organization_id, client_id, inventory_no, name)
			SELECT $1, (ARRAY[$2::uuid, $3::uuid, NULL])[n % 3 + 1], 'TICKET-' || n, 'ticket-asset-' || n
			FROM generate_series($4::int, $5::int) AS n`, org, clients[0], clients[1], from, to); err != nil {
			t.Fatalf("seed assets %d-%d: %v", from, to, err)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id, related_asset_id, created_at)
			SELECT $1, 'budget ticket ' || n, $2,
				CASE WHEN n % 2 = 0 THEN (SELECT id FROM ci WHERE organization_id = $1 AND name = 'ticket-ci-' || n) END,
				CASE WHEN n % 3 = 0 THEN (SELECT id FROM asset WHERE organization_id = $1 AND inventory_no = 'TICKET-' || n) END,
				'2026-01-01 00:00:00+00'::timestamptz + make_interval(secs => n)
			FROM generate_series($3::int, $4::int) AS n ORDER BY n`, org, reporter, from, to); err != nil {
			t.Fatalf("seed tickets %d-%d: %v", from, to, err)
		}
	}
	const orgTickets = 10000
	seed(f.OrgA, reporterA, []any{f.Client1, f.Client2}, 1, orgTickets)
	seed(f.OrgB, reporterB, []any{nil, nil}, 1, 50)
	analyze := func() {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `ANALYZE ticket, ci, asset`); err != nil {
			t.Fatal(err)
		}
	}
	analyze()

	trace := scopetest.NewQueryTrace(t, f.App)
	repo := ticket.NewPGRepository(trace.Pool)
	ctx := f.OrgCtx(f.OrgA)

	list := func(limit int) int {
		return len(trace.Run(t, func() error {
			items, total, err := repo.List(ctx, f.OrgA, &ticket.FilterParams{}, api.PaginationParams{Limit: limit})
			if err == nil && (len(items) != limit || total != orgTickets) {
				t.Errorf("list %d: %d items of %d", limit, len(items), total)
			}
			return err
		}))
	}
	// Warm the pool: the first connection switches its role.
	list(10)
	small, large := list(10), list(200)
	if small != large {
		t.Errorf("ticket list: %d statements for 10 rows, %d for 200 (N+1)", small, large)
	}
	// begin with the tenant context, count, page, commit.
	const listBudget = 4
	if large > listBudget {
		t.Errorf("ticket list: %d statements, budget %d", large, listBudget)
	}
	scopetest.TimeBudget(t, "ticket list of 200 rows", 5, 500*time.Millisecond, func() error {
		_, _, err := repo.List(ctx, f.OrgA, &ticket.FilterParams{}, api.PaginationParams{Limit: 200})
		return err
	})

	// The page of 20 tickets sorted by number, the first ones of the
	// organization: their related CIs and assets are read through the
	// checks of ticketVisible.
	const page = 20
	byNumber := func() ([]int, error) {
		items, _, err := repo.List(ctx, f.OrgA, &ticket.FilterParams{SortBy: "ticket_number", SortDir: "asc"}, api.PaginationParams{Limit: page})
		out := make([]int, 0, len(items))
		for i := range items {
			out = append(out, items[i].TicketNumber)
		}
		return out, err
	}
	rows, err := f.Admin.Query(bg, `SELECT ticket_number FROM ticket WHERE organization_id = $1 ORDER BY ticket_number LIMIT $2`, f.OrgA, page)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		t.Fatal(err)
	}
	var related int
	if err = f.Admin.QueryRow(bg, `SELECT count(related_ci_id) + count(related_asset_id) FROM (SELECT related_ci_id, related_asset_id FROM ticket
		WHERE organization_id = $1 ORDER BY ticket_number LIMIT $2) AS p`, f.OrgA, page).Scan(&related); err != nil || related == 0 {
		t.Fatalf("the first page relates to %d CIs and assets: %v", related, err)
	}
	// The page reads the tickets from the index of their numbers.
	byNumberBudget := &scopetest.QueryBudget{Tables: []string{"ticket"},
		Indexes: [][]string{{"ticket_organization_number_key"}}, MaxRows: orgTickets / 100}
	// The checks of ticketVisible look the related CI and asset of every
	// ticket of the page up by id (scalar subqueries on the primary key;
	// as EXISTS under OR they were hashed subplans that read every CI and
	// asset of the organization, finding of WP-824). Without the primary
	// key the planner falls back to ci_id_organization_key and
	// asset_id_organization_key, so each group holds both.
	visibleBudget := &scopetest.QueryBudget{Tables: []string{"ticket", "ci", "asset"},
		Indexes: [][]string{{"ci_pkey", "ci_id_organization_key"}, {"asset_pkey", "asset_id_organization_key"}}, MaxRows: orgTickets / 100}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	check := func(state string) {
		t.Helper()
		var got []int
		stmts := trace.Run(t, func() error {
			var lerr error
			got, lerr = byNumber()
			return lerr
		})
		if !slices.Equal(got, want) {
			t.Errorf("%s: page by number %v, want %v", state, got, want)
		}
		if len(stmts) > listBudget {
			t.Errorf("%s: page by number: %d statements, budget %d", state, len(stmts), listBudget)
		}
		stmt := scopetest.Statement(t, stmts, " ORDER BY ticket_number ")
		scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, "page by ticket_number", stmt, byNumberBudget)
		scopetest.CheckPlan(t, f.Admin, f.App, &orgWide, state, "checks of ticketVisible on ci.id and asset.id", stmt, visibleBudget)
		scopetest.TimeBudget(t, state+", page by ticket_number", 5, 500*time.Millisecond, func() error {
			_, lerr := byNumber()
			return lerr
		})
	}
	check("organization A alone")
	seed(f.OrgB, reporterB, []any{nil, nil}, 51, 5050)
	analyze()
	check("with 5,000 tickets of organization B")
}
