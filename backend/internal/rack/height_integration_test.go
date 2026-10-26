package rack_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
