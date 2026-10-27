package rack_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAuditDefectRackHeightNotBelowMounts covers RCK-01 and LOC-06 (WP-108,
// WP-500, F-140): PATCH /racks/{id} with a height_units below the highest
// unit occupied by a mount (max(position_u + height_u − 1)) is 409 on
// height_units, the problem names that unit and the rack keeps its height;
// the highest occupied unit itself and any height of an empty rack are 200.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefectRackHeight' ./internal/rack/
func TestAuditDefectRackHeightNotBelowMounts(t *testing.T) {
	f := scopetest.Seed(t, "db")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)
	room := f.Room(t, f.Client1, "audit-db-room")

	mounted := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "audit-db-mounted", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(ctx, mounted); err != nil {
		t.Fatal(err)
	}
	ci := f.CI(t, f.OrgA, f.Client1, "audit-db-ci")
	if err := repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: mounted.ID, CIID: ci, PositionU: 40, HeightU: 2, Face: "front"}); err != nil {
		t.Fatal(err)
	}
	empty := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "audit-db-empty", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(ctx, empty); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	patch := func(id string, height int) *httptest.ResponseRecorder {
		body := `{"height_units":` + strconv.Itoa(height) + `}`
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/racks/"+id, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	stored := func(id string) int {
		var h int
		if err := f.Admin.QueryRow(bg, `SELECT height_units FROM rack WHERE id = $1`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Ordered: the mount occupies U40–41 of the 42U rack.
	for _, tc := range []struct {
		name       string
		rack       string
		height     int
		wantStatus int
		wantStored int
	}{
		{"shrink below the highest mount is refused", mounted.ID, 39, http.StatusConflict, 42},
		{"shrink far below the mount is refused", mounted.ID, 10, http.StatusConflict, 42},
		{"growing a mounted rack is allowed", mounted.ID, 44, http.StatusOK, 44},
		{"shrink to exactly the highest occupied unit is allowed", mounted.ID, 41, http.StatusOK, 41},
		{"rack without mounts may shrink to 1U", empty.ID, 1, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := patch(tc.rack, tc.height)
			got := stored(tc.rack)
			if w.Code != tc.wantStatus || got != tc.wantStored {
				t.Errorf("PATCH height_units=%d = %d (stored %d), want %d (stored %d): RCK-01 keeps position_u + height_u - 1 <= height_units; body %.200s",
					tc.height, w.Code, got, tc.wantStatus, tc.wantStored, w.Body.String())
			}
			if tc.wantStatus == http.StatusConflict {
				body := w.Body.String()
				if !strings.Contains(body, `"field":"height_units"`) {
					t.Errorf("409 should name the field height_units: %.200s", body)
				}
				if !strings.Contains(body, "unit 41") {
					t.Errorf("409 should name the highest occupied unit 41: %.200s", body)
				}
			}
		})
	}
}

// TestRackHeightAndMountsSerialize covers the row lock of RCK-01 (WP-500):
// UpdateRack, CreateMount and UpdateMount lock the (target) rack row FOR
// UPDATE before they compare mounts and height, so a mount and a reduction
// of the height that run in parallel never leave a mount above the rack.
//
// Each case holds the rack row locked in a maintenance transaction, starts
// the repository call, waits until that call is blocked by the transaction
// and then commits the competing write: the repository call must see it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackHeightAndMountsSerialize(t *testing.T) {
	f := scopetest.Seed(t, "db")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)
	room := f.Room(t, f.Client1, "lock-db-room")
	seq := 0
	newRack := func(t *testing.T) string {
		t.Helper()
		seq++
		rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: fmt.Sprintf("lock-db-rack-%d", seq), HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
		if err := repo.CreateRack(ctx, rk); err != nil {
			t.Fatal(err)
		}
		return rk.ID
	}
	newCI := func(t *testing.T) string {
		t.Helper()
		seq++
		return f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("lock-db-ci-%d", seq))
	}
	// topAndHeight reads the highest mounted unit and the height of a rack.
	topAndHeight := func(t *testing.T, rackID string) (int, int) {
		t.Helper()
		var top, height int
		if err := f.Admin.QueryRow(bg, `
			SELECT COALESCE((SELECT MAX(position_u + height_u - 1) FROM rack_mount WHERE rack_id = $1), 0), height_units
			FROM rack WHERE id = $1`, rackID).Scan(&top, &height); err != nil {
			t.Fatal(err)
		}
		return top, height
	}
	// whileLocked locks the rack row in a maintenance transaction, runs call
	// in parallel, waits until call is blocked by that transaction, applies
	// competing in it and commits; it returns the error of call.
	whileLocked := func(t *testing.T, rackID, competing string, args []any, call func() error) error {
		t.Helper()
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		var pid int
		if err = tx.QueryRow(bg, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(bg, `SELECT 1 FROM rack WHERE id = $1 FOR UPDATE`, rackID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- call() }()
		deadline := time.Now().Add(15 * time.Second)
		for {
			var blocked bool
			if err = f.Admin.QueryRow(bg,
				`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY (pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			select {
			case callErr := <-done:
				t.Fatalf("the call finished without waiting for the locked rack row (err %v)", callErr)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("the call did not wait for the locked rack row")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, err = tx.Exec(bg, competing, args...); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(bg); err != nil {
			t.Fatal(err)
		}
		select {
		case callErr := <-done:
			return callErr
		case <-time.After(15 * time.Second):
			t.Fatal("the call did not finish after the commit")
			return nil
		}
	}

	t.Run("a mount waits for a reduction of the height and sees it", func(t *testing.T) {
		rackID, ci := newRack(t), newCI(t)
		err := whileLocked(t, rackID, `UPDATE rack SET height_units = 39 WHERE id = $1`, []any{rackID}, func() error {
			return repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rackID, CIID: ci, PositionU: 40, HeightU: 2, Face: "front"})
		})
		if !errors.Is(err, rack.ErrConflict) {
			t.Errorf("CreateMount at U40 after the rack shrank to 39U = %v, want ErrConflict", err)
		}
		if top, height := topAndHeight(t, rackID); top > height {
			t.Errorf("mount up to U%d in a %dU rack (RCK-01)", top, height)
		}
	})

	t.Run("a reduction of the height waits for a mount and sees it", func(t *testing.T) {
		rackID, ci := newRack(t), newCI(t)
		err := whileLocked(t, rackID,
			`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face) VALUES ($1, $2, $3, 40, 2, 'front')`,
			[]any{f.OrgA, rackID, ci}, func() error {
				h := 39
				_, updErr := repo.UpdateRack(ctx, f.OrgA, rackID, rack.UpdateRackRequest{HeightUnits: &h})
				return updErr
			})
		var conflict *api.ConflictError
		if !errors.As(err, &conflict) || conflict.Field != "height_units" {
			t.Errorf("UpdateRack to 39U after a mount at U40 = %v, want a conflict on height_units", err)
		}
		if top, height := topAndHeight(t, rackID); top > height || height != 42 {
			t.Errorf("rack %dU with a mount up to U%d, want 42U unchanged", height, top)
		}
	})

	t.Run("moving a mount into a rack waits for a reduction of its height and sees it", func(t *testing.T) {
		source, target, ci := newRack(t), newRack(t), newCI(t)
		m := &rack.RackMount{OrganizationID: f.OrgA, RackID: source, CIID: ci, PositionU: 40, HeightU: 2, Face: "front"}
		if err := repo.CreateMount(ctx, m); err != nil {
			t.Fatal(err)
		}
		err := whileLocked(t, target, `UPDATE rack SET height_units = 39 WHERE id = $1`, []any{target}, func() error {
			_, updErr := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &target})
			return updErr
		})
		if !errors.Is(err, rack.ErrConflict) {
			t.Errorf("UpdateMount into a rack that shrank to 39U = %v, want ErrConflict", err)
		}
		if top, height := topAndHeight(t, target); top > height {
			t.Errorf("mount up to U%d in a %dU rack (RCK-01)", top, height)
		}
	})

	t.Run("parallel mounts and reductions keep every mount inside its rack", func(t *testing.T) {
		for round := 0; round < 10; round++ {
			rackID, ci := newRack(t), newCI(t)
			var wg sync.WaitGroup
			var mountErr, shrinkErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				mountErr = repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rackID, CIID: ci, PositionU: 40, HeightU: 2, Face: "front"})
			}()
			go func() {
				defer wg.Done()
				h := 39
				_, shrinkErr = repo.UpdateRack(ctx, f.OrgA, rackID, rack.UpdateRackRequest{HeightUnits: &h})
			}()
			wg.Wait()
			if (mountErr == nil) == (shrinkErr == nil) {
				t.Errorf("round %d: exactly one of mount (%v) and reduction (%v) must succeed", round, mountErr, shrinkErr)
			}
			if top, height := topAndHeight(t, rackID); top > height {
				t.Errorf("round %d: mount up to U%d in a %dU rack (RCK-01)", round, top, height)
			}
		}
	})
}

// TestAuditK4RackHeightCoversInvisibleMounts covers RCK-01 and TEN-06 for
// mounts the caller cannot see (WP-634, F-360): a principal restricted to
// client 1, to the site of the rack or to both sees the rack of client 1 but
// not the mount of a client-2 CI in it (rack_mount_isolation filters by the
// client and site of the mounted CI). PATCH /racks/{id} below that mount is
// still 409 on height_units; the problem names the unit but neither the CI
// nor the mount. rack_highest_mounted_unit (migration 000172) counts every
// mount of a rack the caller sees, returns NULL for a rack it cannot see
// (other client, other organization), leaves the caller's scope as it was
// and is a SECURITY DEFINER function of the non-superuser table owner with a
// fixed search_path.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestAuditK4RackHeightCoversInvisibleMounts' ./internal/rack/
func TestAuditK4RackHeightCoversInvisibleMounts(t *testing.T) {
	f := scopetest.Seed(t, "db")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)

	siteOf := func(roomID string) string {
		t.Helper()
		var siteID string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, roomID).Scan(&siteID); err != nil {
			t.Fatal(err)
		}
		return siteID
	}

	// A 42U rack in a room of client 1 on site 1; an org-wide principal
	// mounts a CI of client 2 on site 2 at U40-41 (shared colocation rack).
	room := f.Room(t, f.Client1, "k4-scoped-room")
	site := siteOf(room)
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "k4-scoped-rack", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(orgCtx, rk); err != nil {
		t.Fatal(err)
	}
	// A rack of client 2 on site 2 that the client-1 and site-1 principals
	// cannot see.
	foreignRoom := f.Room(t, f.Client2, "k4-scoped-room-c2")
	foreignRack := &rack.Rack{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "k4-scoped-rack-c2", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(orgCtx, foreignRack); err != nil {
		t.Fatal(err)
	}
	// The CI of client 2 stands in the room of site 2 (its site follows its
	// location) and is mounted in the rack of site 1.
	const foreignName = "k4-scoped-ci-c2"
	foreignCI := f.CI(t, f.OrgA, f.Client2, foreignName)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, foreignCI, foreignRoom); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateMount(orgCtx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: foreignCI, PositionU: 40, HeightU: 2, Face: "front"}); err != nil {
		t.Fatalf("org-wide mount of the client-2 CI: %v", err)
	}
	var mountClient, mountSite string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM rack_mount WHERE ci_id = $1`,
		foreignCI).Scan(&mountClient, &mountSite); err != nil {
		t.Fatal(err)
	}
	if mountClient != f.Client2 || mountSite != siteOf(foreignRoom) {
		t.Fatalf("scope of the mount: client %q site %q, want client 2 and site 2", mountClient, mountSite)
	}
	scoped := func(clients, sites database.ScopeSet) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Clients, scope.Sites = clients, sites
		return tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	}
	principals := []struct {
		name string
		ctx  context.Context
	}{
		{"client(c1)", scoped(database.ScopeIDs(f.Client1), database.AllScopes())},
		{"site(s1)", scoped(database.AllScopes(), database.ScopeIDs(site))},
		{"client+site(c1,s1)", scoped(database.ScopeIDs(f.Client1), database.ScopeIDs(site))},
	}

	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	patch := func(ctx context.Context, height int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/racks/"+rk.ID,
			strings.NewReader(`{"height_units":`+strconv.Itoa(height)+`}`)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	stored := func() (height, top int) {
		if err := f.Admin.QueryRow(bg, `SELECT r.height_units, COALESCE(MAX(m.position_u + m.height_u - 1), 0)
			FROM rack r LEFT JOIN rack_mount m ON m.rack_id = r.id WHERE r.id = $1 GROUP BY r.height_units`, rk.ID).Scan(&height, &top); err != nil {
			t.Fatal(err)
		}
		return height, top
	}
	// highest calls rack_highest_mounted_unit as the principal of ctx and
	// reports whether the client and site scope after the call equal those
	// before it.
	highest := func(t *testing.T, ctx context.Context, orgID, rackID string) (top *int, restored bool) {
		t.Helper()
		const scopeQuery = `SELECT current_setting('app.client_scope', true) || '|' || current_setting('app.site_scope', true)`
		if err := database.WithRequestTenant(ctx, f.App, orgID, func(ctx context.Context, tx pgx.Tx) error {
			var before, after string
			if err := tx.QueryRow(ctx, scopeQuery).Scan(&before); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT rack_highest_mounted_unit($1)`, rackID).Scan(&top); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, scopeQuery).Scan(&after); err != nil {
				return err
			}
			restored = before == after
			return nil
		}); err != nil {
			t.Fatalf("rack_highest_mounted_unit: %v", err)
		}
		return top, restored
	}

	for _, p := range principals {
		t.Run(p.name, func(t *testing.T) {
			if _, err := repo.GetRack(p.ctx, f.OrgA, rk.ID); err != nil {
				t.Fatalf("the rack of client 1 on site 1 is not visible: %v", err)
			}
			mounts, total, err := repo.ListMounts(p.ctx, f.OrgA, rk.ID, api.PaginationParams{Limit: 100})
			if err != nil || total != 0 || len(mounts) != 0 {
				t.Fatalf("mounts of the rack: total=%d %+v err=%v; want none visible, otherwise the case proves nothing", total, mounts, err)
			}

			// Mounting into the occupied units stays refused: the exclusion
			// constraint rack_mount_no_overlap is not subject to row-level
			// security.
			ownCI := f.CI(t, f.OrgA, f.Client1, "k4-scoped-ci-c1-"+p.name)
			if err = repo.CreateMount(p.ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: ownCI, PositionU: 40, HeightU: 1, Face: "front"}); err == nil {
				t.Error("mount into U40, occupied by the client-2 mount: accepted; want a conflict")
			}

			w := patch(p.ctx, 39)
			height, top := stored()
			body := w.Body.String()
			if w.Code != http.StatusConflict || height != 42 {
				t.Errorf("PATCH height_units=39 of a rack with a client-2 mount at U40-41: %d, stored height %d, highest mounted unit %d; "+
					"want 409 on height_units and height 42 (RCK-01 holds for every mount, also those the caller cannot see); body %.200s",
					w.Code, height, top, body)
			}
			if !strings.Contains(body, `"field":"height_units"`) || !strings.Contains(body, "unit 41") {
				t.Errorf("409 should name the field height_units and the highest occupied unit 41: %.300s", body)
			}
			if strings.Contains(body, foreignCI) || strings.Contains(body, foreignName) {
				t.Errorf("409 discloses the invisible CI: %.300s", body)
			}

			got, restored := highest(t, p.ctx, f.OrgA, rk.ID)
			if got == nil || *got != 41 {
				t.Errorf("rack_highest_mounted_unit of the visible rack = %v, want 41", got)
			}
			if !restored {
				t.Error("rack_highest_mounted_unit left the client or site scope widened")
			}
			if got, _ = highest(t, p.ctx, f.OrgA, foreignRack.ID); got != nil {
				t.Errorf("rack_highest_mounted_unit of the invisible client-2 rack = %d, want NULL", *got)
			}
		})
	}

	// The rack is invisible to another organization: NULL, no number.
	if got, _ := highest(t, f.OrgCtx(f.OrgB), f.OrgB, rk.ID); got != nil {
		t.Errorf("rack_highest_mounted_unit of a rack of organization A as organization B = %d, want NULL", *got)
	}

	// The highest occupied unit itself remains a valid height.
	if w := patch(principals[0].ctx, 41); w.Code != http.StatusOK {
		t.Errorf("PATCH height_units=41 (the highest occupied unit) = %d, want 200; body %.200s", w.Code, w.Body.String())
	}
	if height, _ := stored(); height != 41 {
		t.Errorf("stored height %d after PATCH 41, want 41", height)
	}

	// SECURITY DEFINER of the table owner (no superuser, no BYPASSRLS, so
	// FORCE ROW LEVEL SECURITY keeps applying) with a fixed search_path;
	// executable by the application role, not by PUBLIC.
	var (
		definer           bool
		config            []string
		owner             string
		superuser, bypass bool
		appExec, pubExec  bool
	)
	if err := f.Admin.QueryRow(bg, `SELECT p.prosecdef, COALESCE(p.proconfig, '{}'), r.rolname, r.rolsuper, r.rolbypassrls,
			has_function_privilege('reticora_app', p.oid, 'EXECUTE'),
			EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
			         WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
		FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
		WHERE p.oid = 'public.rack_highest_mounted_unit(uuid)'::regprocedure`).
		Scan(&definer, &config, &owner, &superuser, &bypass, &appExec, &pubExec); err != nil {
		t.Fatal(err)
	}
	if !definer || !slices.Equal(config, []string{"search_path=pg_catalog, public, pg_temp"}) || owner != "reticora_owner" ||
		superuser || bypass || !appExec || pubExec {
		t.Errorf("rack_highest_mounted_unit: security definer %v, config %v, owner %s (superuser %v, bypassrls %v), "+
			"reticora_app execute %v, PUBLIC execute %v; want definer, search_path=pg_catalog, public, pg_temp, "+
			"reticora_owner without superuser and bypassrls, reticora_app yes, PUBLIC no",
			definer, config, owner, superuser, bypass, appExec, pubExec)
	}
}
