package assignment_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAssignmentRepositoryClientScope runs the assignment service and the
// projection with the principal's tenant scope (TEN-06, WP-017, WP-747): a
// principal restricted to client 1 cannot assign a client-2 asset and
// neither lists nor reads its assignment; the projection
// assignment_projection reads stock_movement under the caller's row level
// security.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2c")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2c-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2c-foreign")
	user := f.AppUser(t, f.OrgA, "holder")

	repo := assignment.NewPGRepository(f.App)
	svc := assignment.NewService(f.App, movement.NewService(movement.NewPGRepository(f.App)), events.NewOutbox())
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: user})
	assign := func(ctx context.Context, assetID string) (*assignment.Assignment, error) {
		return svc.Assign(ctx, f.OrgA, &assignment.AssignInput{AssetID: assetID, Holder: assignment.Holder{UserID: user}})
	}
	foreignA, err := assign(orgCtx, foreign)
	if err != nil {
		t.Fatalf("org-wide assignment: %v", err)
	}

	if _, err = assign(ctx, foreign); !errors.Is(err, assignment.ErrAssetNotFound) {
		t.Fatalf("client-1 assignment of a client-2 asset: %v, want ErrAssetNotFound", err)
	}
	ownA, err := assign(ctx, own)
	if err != nil {
		t.Fatalf("client-1 assignment of own asset: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, &assignment.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != ownA.ID || list[0].Status != "active" || list[0].AssignedTo != user {
		t.Fatalf("client-1 assignments: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignA.ID); err == nil {
		t.Fatal("client-1 principal read the assignment of a client-2 asset")
	}
	if list, total, err = repo.List(ctx, f.OrgA, &assignment.FilterParams{AssetID: foreign}, api.PaginationParams{Limit: 100}); err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("client-1 assignments of the client-2 asset: total=%d %+v err=%v, want none", total, list, err)
	}
	// The org-wide principal sees both.
	if _, total, err = repo.List(orgCtx, f.OrgA, &assignment.FilterParams{AssignedTo: user}, api.PaginationParams{Limit: 100}); err != nil || total != 2 {
		t.Fatalf("org-wide assignments: total=%d err=%v, want 2", total, err)
	}
	if got, gerr := repo.GetByID(orgCtx, f.OrgA, foreignA.ID); gerr != nil || got.AssetID != foreign || got.AssignedBy != user {
		t.Fatalf("org-wide read of the client-2 assignment = %+v, %v", got, gerr)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, &assignment.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestAssignmentForeignClientReturnAndTransfer covers the writes on an
// existing assignment (TEN-06, AST-01, WP-537, WP-275, WP-746, WP-747): a
// principal restricted to client 1 can neither return nor transfer a
// client-2 asset through POST /api/v1/assets/{id}/return|transfer (404);
// its ledger, holder cache and projected assignment stay unchanged. Its own
// assets it assigns, returns and transfers through the assignment service,
// and the projection shows them returned and transferred.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentForeignClientReturnAndTransfer(t *testing.T) {
	f := scopetest.Seed(t, "2c")
	bg := context.Background()
	ownReturnAsset := f.Asset(t, f.OrgA, f.Client1, "scopetest-2c-own")
	ownTransferAsset := f.Asset(t, f.OrgA, f.Client1, "scopetest-2c-own2")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2c-foreign")
	holder := f.AppUser(t, f.OrgA, "holder")
	successor := f.AppUser(t, f.OrgA, "successor")

	mux := chi.NewRouter()
	assignment.NewHandler(assignment.NewPGRepository(f.App)).
		WithService(assignment.NewService(f.App, movement.NewService(movement.NewPGRepository(f.App)), events.NewOutbox())).
		RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: holder})
	post := func(ctx context.Context, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	created := func(ctx context.Context, assetID string) string {
		w := post(ctx, "/api/v1/assets/"+assetID+"/assign", `{"assigned_user_id":"`+holder+`","notes":"original"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("assignment of %s = %d %s, want 201", assetID, w.Code, w.Body)
		}
		var a assignment.Assignment
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	foreignA := created(orgCtx, foreign)
	ownReturn, ownTransfer := created(ctx, ownReturnAsset), created(ctx, ownTransferAsset)
	type row struct{ status, notes, condition, assetID, holder string }
	// stored reads the projected assignment through the maintenance pool
	// (no row level security) and the holder cache of its asset.
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT p.status, COALESCE(p.notes, ''), COALESCE(p.return_condition, ''), p.asset_id::text,
				COALESCE(a.assigned_user_id::text, '')
			FROM assignment_projection p JOIN asset a ON a.id = p.asset_id WHERE p.id = $1`, id).
			Scan(&r.status, &r.notes, &r.condition, &r.assetID, &r.holder); err != nil {
			t.Fatalf("stored assignment %s: %v", id, err)
		}
		return r
	}
	movements := func(assetID string) int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stock_movement WHERE asset_id = $1`, assetID).Scan(&n); err != nil {
			t.Fatalf("count movements: %v", err)
		}
		return n
	}
	transfers := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM assignment_projection WHERE asset_id = ANY ($1::uuid[]) AND assignment_type = 'transfer'`,
			[]string{foreign, ownReturnAsset, ownTransferAsset}).Scan(&n); err != nil {
			t.Fatalf("count transfers: %v", err)
		}
		return n
	}
	want := row{status: "active", notes: "original", assetID: foreign, holder: holder}
	booked := movements(foreign)

	if w := post(ctx, "/api/v1/assets/"+foreign+"/return", `{"expected_holder":"`+holder+`","return_condition":"damaged","notes":"hijacked"}`); w.Code != http.StatusNotFound {
		t.Errorf("client-1 return of a client-2 assignment = %d %s, want 404", w.Code, w.Body)
	}
	if got := stored(foreignA); got != want {
		t.Errorf("client-2 assignment after foreign return: %+v, want %+v", got, want)
	}
	if w := post(ctx, "/api/v1/assets/"+foreign+"/transfer", `{"expected_holder":"`+holder+`","assigned_user_id":"`+successor+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("client-1 transfer of a client-2 assignment = %d %s, want 404", w.Code, w.Body)
	}
	if got := stored(foreignA); got != want {
		t.Errorf("client-2 assignment after foreign transfer: %+v, want %+v", got, want)
	}
	if n := movements(foreign); n != booked {
		t.Errorf("foreign return and transfer booked %d movements", n-booked)
	}
	if n := transfers(); n != 0 {
		t.Errorf("foreign transfer created %d transfer assignments, want 0", n)
	}

	// Positive control: the client-1 principal returns and transfers its own
	// assignments.
	if w := post(ctx, "/api/v1/assets/"+ownReturnAsset+"/return", `{"expected_holder":"`+holder+`","return_condition":"good","notes":"back"}`); w.Code != http.StatusOK {
		t.Fatalf("client-1 return of own assignment = %d %s, want 200", w.Code, w.Body)
	}
	if got := stored(ownReturn); got.status != "returned" || got.condition != "good" || got.notes != "original" || got.holder != "" {
		t.Errorf("own assignment after return: %+v, want returned/good without holder", got)
	}
	if w := post(ctx, "/api/v1/assets/"+ownTransferAsset+"/transfer", `{"expected_holder":"`+holder+`","assigned_user_id":"`+successor+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("client-1 transfer of own assignment = %d %s, want 201", w.Code, w.Body)
	}
	if got := stored(ownTransfer); got.status != "transferred" || got.holder != successor {
		t.Errorf("own assignment after transfer: %+v, want transferred", got)
	}
	if n := transfers(); n != 1 {
		t.Errorf("own transfer: %d transfer assignments, want 1", n)
	}
}
