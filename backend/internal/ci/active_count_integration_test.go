package ci_test

// WP-674 (ENT-03, ENT-08), finding F-403: max_cis counts the active CIs of
// the organization, not those the caller sees.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestCIActiveCountIgnoresCallerScope' ./internal/ci/

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestCIActiveCountIgnoresCallerScope covers F-403 at the repositories:
// ci.PGRepository.CountActive (POST /cis, restore, unmerge, the review
// resolutions) and discovery.PGRepository.LockCICount (ingest under the
// max_cis lock) count every active CI of the organization for a principal
// restricted to client 1 or to site 1 (a collector bound to a client or
// site ingests in that scope), who lists fewer: CIs of client 2, on site 2
// and org-wide ones, not the deleted one and none of organization B. Inside
// the caller's transaction the count leaves its client and site scope as
// they were. Without a tenant (WithSystem) the count is 0. ci_active_count()
// (migration 000186) is no SECURITY DEFINER, has a fixed search_path and is
// executable by reticora_app, not by PUBLIC.
func TestCIActiveCountIgnoresCallerScope(t *testing.T) {
	f := scopetest.Seed(t, "cf")
	bg := context.Background()
	f.CI(t, f.OrgA, f.Client1, "count-c1")
	located := f.CI(t, f.OrgA, f.Client2, "count-c2-a")
	f.CI(t, f.OrgA, f.Client2, "count-c2-b")
	// count-c2-a stands in a room of client 2: its site follows the room.
	siteOf := func(room string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).
			Scan(&site); err != nil {
			t.Fatal(err)
		}
		return site
	}
	room2 := f.Room(t, f.Client2, "count-room-c2")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, located, room2); err != nil {
		t.Fatal(err)
	}
	site1 := siteOf(f.Room(t, f.Client1, "count-room-c1"))
	var locatedSite string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM ci WHERE id = $1`, located).Scan(&locatedSite); err != nil ||
		locatedSite != siteOf(room2) || locatedSite == site1 {
		t.Fatalf("setup: site of the located CI %q (err %v), want the site of its room, not %s", locatedSite, err, site1)
	}
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	site1Ctx := database.ContextWithTenantScope(bg, &siteScope)
	f.CI(t, f.OrgA, "", "count-orgwide")
	deleted := f.CI(t, f.OrgA, f.Client2, "count-c2-deleted")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	f.CI(t, f.OrgB, "", "count-org-b")
	const want = 4

	repo := ci.NewPGRepository(f.App)
	client1 := f.ClientCtx(f.Client1)
	_, visible, err := repo.List(client1, f.OrgA, &ci.FilterParams{}, api.PaginationParams{Limit: 1})
	if err != nil || visible != 2 {
		t.Fatalf("precondition: client 1 lists %d CIs (err %v), want 2 (its own and the org-wide one)", visible, err)
	}
	if _, visible, err = repo.List(site1Ctx, f.OrgA, &ci.FilterParams{}, api.PaginationParams{Limit: 1}); err != nil || visible != want-1 {
		t.Fatalf("precondition: site 1 lists %d CIs (err %v), want %d (not the one on site 2)", visible, err, want-1)
	}
	for name, ctx := range map[string]context.Context{"client 1": client1, "site 1": site1Ctx, "org-wide": f.OrgCtx(f.OrgA)} {
		if n, cerr := repo.CountActive(ctx, f.OrgA); cerr != nil || n != want {
			t.Errorf("CountActive as %s: %d (err %v), want %d", name, n, cerr, want)
		}
		if n, cerr := ci.CountActive(ctx, repo, f.OrgA); cerr != nil || n != want {
			t.Errorf("ci.CountActive as %s: %d (err %v), want %d", name, n, cerr, want)
		}
	}
	if n, cerr := repo.CountActive(f.OrgCtx(f.OrgB), f.OrgB); cerr != nil || n != 1 {
		t.Errorf("CountActive of organization B: %d (err %v), want 1", n, cerr)
	}
	// The ingest of a collector bound to a client or site runs in its scope.
	for name, ctx := range map[string]context.Context{"client 1": client1, "site 1": site1Ctx} {
		if n, cerr := discovery.NewPGRepository(f.App).LockCICount(ctx, f.OrgA, false); cerr != nil || n != want {
			t.Errorf("LockCICount as %s: %d (err %v), want %d", name, n, cerr, want)
		}
	}

	// In the caller's transaction: same count, scope unchanged afterwards.
	scopeOf := func(ctx context.Context, tx pgx.Tx) []string {
		t.Helper()
		var clients, sites string
		if qerr := tx.QueryRow(ctx, `SELECT current_setting('app.client_scope', true), current_setting('app.site_scope', true)`).
			Scan(&clients, &sites); qerr != nil {
			t.Fatal(qerr)
		}
		return []string{clients, sites}
	}
	if err = database.WithRequestTenant(client1, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		before := scopeOf(ctx, tx)
		n, cerr := repo.CountActive(database.ContextWithTx(ctx, tx), f.OrgA)
		if cerr != nil || n != want {
			t.Errorf("CountActive in the client-1 transaction: %d (err %v), want %d", n, cerr, want)
		}
		if after := scopeOf(ctx, tx); !slices.Equal(before, after) || before[0] == "" {
			t.Errorf("scope after the count %v, before %v; want the client-1 scope unchanged", after, before)
		}
		var seen int
		if qerr := tx.QueryRow(ctx, `SELECT count(*) FROM ci WHERE deleted_at IS NULL`).Scan(&seen); qerr != nil || seen != 2 {
			t.Errorf("client 1 reads %d CIs after the count (err %v), want 2", seen, qerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var system int64
	if err = database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ci_active_count()`).Scan(&system)
	}); err != nil || system != 0 {
		t.Errorf("ci_active_count without a tenant: %d (err %v), want 0", system, err)
	}

	var (
		definer          bool
		config           []string
		appExec, pubExec bool
	)
	if err = f.Admin.QueryRow(bg, `SELECT p.prosecdef, COALESCE(p.proconfig, '{}'),
			has_function_privilege('reticora_app', p.oid, 'EXECUTE'),
			EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
			         WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
		FROM pg_proc p WHERE p.oid = 'public.ci_active_count()'::regprocedure`).Scan(&definer, &config, &appExec, &pubExec); err != nil {
		t.Fatal(err)
	}
	if definer || !slices.Equal(config, []string{"search_path=pg_catalog, public, pg_temp"}) || !appExec || pubExec {
		t.Errorf("ci_active_count: security definer %v, config %v, reticora_app execute %v, PUBLIC execute %v; "+
			"want invoker, search_path=pg_catalog, public, pg_temp, reticora_app yes, PUBLIC no", definer, config, appExec, pubExec)
	}
}
