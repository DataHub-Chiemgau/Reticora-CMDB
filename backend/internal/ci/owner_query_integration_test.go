// CI owner in the list query, the saved views and the merge (CI-07, CI-12;
// WP-752) against PostgreSQL.
//
//   - TestCIOwnerQueryFilterAndSort: filter[owner_user_id] and
//     filter[owner_team_id] with exact and in select by the owner; the
//     partial indexes idx_ci_owner_user/idx_ci_owner_team of WP-280 serve
//     both predicates under row level security (K23); sort by either owner
//     field pages with the cursor of NextCursor (the format of the memory
//     repository) in both directions over CIs with and without an owner,
//     each exactly once and in the order of the UUID.
//   - TestCIOwnerSavedView: a saved view filters on owner_user_id (the basis
//     of "my devices", WP-307) and sorts and shows the owner fields.
//   - TestCIOwnerMerge: without an asset the survivor keeps its owner and
//     takes the victim's where it has none; a survivor with an asset keeps
//     the asset's owner, and one that receives the victim's asset takes
//     that asset's owner (ci_mirror_asset_owner, DB-05).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCIOwner(Query|SavedView|Merge)' ./internal/ci/
package ci_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
)

// ownedCIs creates the CIs of the query tests (names oq-1 … oq-6): two of
// user A (one with team A), one of user A2, one of team A only and two
// without an owner. It returns their ids by name.
func ownedCIs(t *testing.T, o *ownerFixture) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"oq-1", map[string]any{"owner_user_id": o.userA, "owner_team_id": o.teamA}},
		{"oq-2", map[string]any{"owner_user_id": o.userA2}},
		{"oq-3", map[string]any{"owner_team_id": o.teamA}},
		{"oq-4", map[string]any{}},
		{"oq-5", map[string]any{"owner_user_id": o.userA}},
		{"oq-6", map[string]any{}},
	} {
		c.body["name"] = c.name
		ids[c.name] = o.createCI(t, o.api, c.body, http.StatusCreated).ID
	}
	return ids
}

// names returns the names of the CIs in order.
func names(items []ci.Item) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].Name)
	}
	return out
}

func TestCIOwnerQueryFilterAndSort(t *testing.T) {
	o := newOwnerFixture(t)
	f := o.f
	ids := ownedCIs(t, o)
	ctx := o.orgScope

	list := func(filter *ci.FilterParams, page api.PaginationParams) []ci.Item {
		t.Helper()
		filter.Search = "oq-"
		items, _, err := o.repo.List(ctx, f.OrgA, filter, page)
		if err != nil {
			t.Fatalf("list %+v: %v", filter.Filters, err)
		}
		return items
	}
	for _, c := range []struct {
		field, op string
		values    []string
		want      []string
	}{
		{"owner_user_id", api.OpExact, []string{o.userA}, []string{"oq-1", "oq-5"}},
		{"owner_user_id", api.OpIn, []string{o.userA2, strings.ToUpper(o.userA)}, []string{"oq-1", "oq-2", "oq-5"}},
		{"owner_user_id", api.OpExact, []string{o.userB}, nil},
		{"owner_team_id", api.OpExact, []string{o.teamA}, []string{"oq-1", "oq-3"}},
		{"owner_team_id", api.OpIn, []string{o.teamA, o.teamA2}, []string{"oq-1", "oq-3"}},
		{"owner_team_id", api.OpIn, []string{o.teamA2}, nil},
	} {
		filter := &ci.FilterParams{SortBy: "name", SortDir: "asc",
			Filters: []api.Filter{{Field: c.field, Op: c.op, Kind: api.KindUUID, Values: c.values}}}
		if got := names(list(filter, api.PaginationParams{Limit: 50})); !slices.Equal(got, c.want) {
			t.Errorf("filter[%s][%s]=%v: %v, want %v", c.field, c.op, c.values, got, c.want)
		}
	}

	// K23: the partial indexes of WP-280 carry the filters of the list
	// (= and = ANY on the uuid, strict, so they imply IS NOT NULL) as the
	// application role under row level security. 2,000 CIs without an
	// owner (names k23-n, outside the search oq-) make the organization
	// large enough that reading it through idx_ci_org is no longer the
	// cheaper plan, whatever the statistics of earlier tests said.
	o.exec(t, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, $2, 'k23-' || n, 'active' FROM generate_series(1, 2000) AS n`, f.OrgA, o.serverType)
	o.exec(t, `ANALYZE ci`)
	for index, query := range map[string]string{
		"idx_ci_owner_user": `SELECT id FROM ci WHERE (owner_user_id)::uuid = $1::uuid`,
		"idx_ci_owner_team": `SELECT id FROM ci WHERE (owner_team_id)::uuid = ANY($1::uuid[])`,
	} {
		arg := any(o.userA)
		if index == "idx_ci_owner_team" {
			arg = []string{o.teamA}
		}
		var plan []string
		err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `EXPLAIN `+query, arg)
			if err != nil {
				return err
			}
			plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatalf("explain %s: %v", query, err)
		}
		if !strings.Contains(strings.Join(plan, "\n"), index) {
			t.Errorf("%s does not use %s:\n%s", query, index, strings.Join(plan, "\n"))
		}
	}

	// Sorting with the cursor: pages of two over six CIs, four of them
	// sharing an owner value or having none. The order is the UUID (its
	// lower-case text), the CIs without the field first ascending and last
	// descending, ties by id; no CI is skipped or repeated.
	owners := map[string]map[string]string{
		"owner_user_id": {"oq-1": o.userA, "oq-2": o.userA2, "oq-5": o.userA},
		"owner_team_id": {"oq-1": o.teamA, "oq-3": o.teamA},
	}
	for field, owner := range owners {
		for _, dir := range []string{"asc", "desc"} {
			want := make([]string, 0, len(ids))
			for name := range ids {
				want = append(want, name)
			}
			slices.SortFunc(want, func(a, b string) int {
				c := strings.Compare(owner[a], owner[b])
				if c == 0 {
					c = strings.Compare(ids[a], ids[b])
				}
				if dir == "desc" {
					c = -c
				}
				return c
			})
			filter := &ci.FilterParams{SortBy: field, SortDir: dir}
			var got []string
			page := api.PaginationParams{Limit: 2}
			for range 10 {
				items := list(filter, page)
				got = append(got, names(items)...)
				next := ci.NextCursor(items, filter, page.Limit)
				if next == "" {
					break
				}
				cursor, err := api.DecodeCursor(next)
				if err != nil {
					t.Fatal(err)
				}
				page.Cursor = &cursor
			}
			if !slices.Equal(got, want) {
				t.Errorf("sort %s %s with cursor: %v, want %v", field, dir, got, want)
			}
			// The first page without cursor has the same order.
			if first := names(list(filter, api.PaginationParams{Limit: 6})); !slices.Equal(first, want) {
				t.Errorf("sort %s %s: %v, want %v", field, dir, first, want)
			}
		}
	}
}

func TestCIOwnerSavedView(t *testing.T) {
	o := newOwnerFixture(t)
	f := o.f
	ownedCIs(t, o)
	req := &savedview.UpsertRequest{Name: "my devices", EntityKind: "ci",
		FilterSpec: map[string]any{"where": map[string]any{"or": []any{
			map[string]any{"field": "owner_user_id", "op": "eq", "value": o.userA},
			map[string]any{"field": "owner_team_id", "op": "in", "value": []any{o.teamA}},
		}}},
		Sort:    []savedview.SortKey{{Field: "owner_team_id", Dir: "asc"}, {Field: "name", Dir: "desc"}},
		Columns: []string{"owner_user_id", "owner_team_id"}}
	if err := savedview.ValidateView("ci", req); err != nil {
		t.Fatalf("validate view: %v", err)
	}
	spec := &savedview.FilterSpec{EntityKind: "ci", Search: "oq-", Sort: req.Sort, Columns: req.Columns,
		Where: &savedview.Expr{Or: []savedview.Expr{
			{Field: "owner_user_id", Op: savedview.OpEq, Value: o.userA},
			{Field: "owner_team_id", Op: "in", Value: []any{o.teamA}},
		}}}
	rows, total, err := savedview.NewPGQueryEngine(f.App).Query(o.orgScope, f.OrgA, spec, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Name)
		if row.Columns["owner_user_id"] != nil && row.Columns["owner_user_id"] != o.userA {
			t.Errorf("%s: column owner_user_id %v", row.Name, row.Columns["owner_user_id"])
		}
	}
	// Team A first (oq-3, oq-1 by name descending), then no team (oq-5).
	if want := []string{"oq-3", "oq-1", "oq-5"}; total != 3 || !slices.Equal(got, want) {
		t.Errorf("view rows %v (total %d), want %v", got, total, want)
	}
	if len(rows) == 3 && (rows[0].Columns["owner_team_id"] != o.teamA || rows[2].Columns["owner_team_id"] != nil) {
		t.Errorf("owner columns: %v, %v", rows[0].Columns, rows[2].Columns)
	}
}

func TestCIOwnerMerge(t *testing.T) {
	o := newOwnerFixture(t)
	f, a := o.f, o.api
	merge := func(survivor, victim string) {
		t.Helper()
		if _, err := o.repo.Merge(o.orgTenantCtx, f.OrgA, survivor, victim, ""); err != nil {
			t.Fatalf("merge %s into %s: %v", victim, survivor, err)
		}
	}
	create := func(name string, body map[string]any) string {
		t.Helper()
		body["name"] = name
		return o.createCI(t, a, body, http.StatusCreated).ID
	}

	// Without an asset: the survivor's own user wins, the victim's team
	// fills the missing one.
	survivor := create("merge-own", map[string]any{"owner_user_id": o.userA2})
	victim := create("merge-own-victim", map[string]any{"owner_user_id": o.userA, "owner_team_id": o.teamA})
	merge(survivor, victim)
	if got, want := o.owner(t, "ci", survivor), o.userA2+"|"+o.teamA; got != want {
		t.Errorf("survivor without asset: owner %s, want %s", got, want)
	}
	// Without an owner the survivor takes the victim's.
	survivor = create("merge-none", map[string]any{})
	victim = create("merge-none-victim", map[string]any{"owner_user_id": o.userA})
	merge(survivor, victim)
	if got, want := o.owner(t, "ci", survivor), o.userA+"|"; got != want {
		t.Errorf("survivor without owner: owner %s, want %s", got, want)
	}

	// With an asset the survivor keeps the asset's owner.
	asset := f.Asset(t, f.OrgA, "", "MERGE-OWNER")
	o.exec(t, `UPDATE asset SET owner_user_id = $2, owner_team_id = $3 WHERE id = $1`, asset, o.userA2, o.teamA2)
	survivor = create("merge-asset", map[string]any{"asset_id": asset})
	victim = create("merge-asset-victim", map[string]any{"owner_user_id": o.userA, "owner_team_id": o.teamA})
	merge(survivor, victim)
	if got, want := o.owner(t, "ci", survivor), o.userA2+"|"+o.teamA2; got != want {
		t.Errorf("survivor with asset: owner %s, want the asset's %s", got, want)
	}

	// The victim's asset passes to a survivor without one: its owner
	// follows the asset, not the survivor's own.
	victimAsset := f.Asset(t, f.OrgA, "", "MERGE-OWNER-VICTIM")
	o.exec(t, `UPDATE asset SET owner_user_id = $2 WHERE id = $1`, victimAsset, o.userA)
	survivor = create("merge-takes-asset", map[string]any{"owner_user_id": o.userA2, "owner_team_id": o.teamA2})
	victim = create("merge-takes-asset-victim", map[string]any{"asset_id": victimAsset})
	merge(survivor, victim)
	if got, want := o.scalar(t, `SELECT COALESCE(asset_id::text, '') FROM ci WHERE id = $1`, survivor), victimAsset; got != want {
		t.Fatalf("survivor asset %s, want %s", got, want)
	}
	if got, want := o.owner(t, "ci", survivor), o.userA+"|"; got != want {
		t.Errorf("survivor with the victim's asset: owner %s, want the asset's %s", got, want)
	}
}
