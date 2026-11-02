package assignment_test

import (
	"context"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAssignmentRepositoryClientScope runs the assignment repository with the
// principal's tenant scope (TEN-06, WP-017): a principal restricted to client
// 1 neither sees nor changes the assignment of a client-2 asset and cannot
// assign a client-2 asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2c")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2c-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2c-foreign")
	user := f.AppUser(t, f.OrgA, "holder")

	repo := assignment.NewPGRepository(f.App)
	mk := func(assetID string) *assignment.Assignment {
		return &assignment.Assignment{OrganizationID: f.OrgA, AssetID: assetID, AssignedTo: user, AssignedBy: user, Status: "active", AssignmentType: "assignment"}
	}
	foreignA := mk(foreign)
	if err := repo.Create(f.OrgCtx(f.OrgA), foreignA); err != nil {
		t.Fatalf("org-wide assignment: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, mk(foreign)); err == nil {
		t.Fatal("client-1 principal assigned a client-2 asset")
	}
	ownA := mk(own)
	if err := repo.Create(ctx, ownA); err != nil {
		t.Fatalf("client-1 assignment of own asset: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, assignment.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != ownA.ID {
		t.Fatalf("client-1 assignments: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignA.ID); err == nil {
		t.Fatal("client-1 principal read the assignment of a client-2 asset")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignA.ID); err == nil {
		t.Fatal("client-1 principal deleted the assignment of a client-2 asset")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM assignment WHERE id = $1`, foreignA.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 assignment: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, assignment.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestAssignmentForeignClientReturnAndTransfer covers the writes on an
// existing assignment (TEN-06, AST-01, WP-537): a principal restricted to
// client 1 can neither return nor transfer the assignment of a client-2
// asset, through the handler (404) or through the repository's Update the
// handlers call (not found), and the client-2 row stays unchanged. Its own
// assignments it returns and transfers.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentForeignClientReturnAndTransfer(t *testing.T) {
	f := scopetest.Seed(t, "2c")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2c-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2c-foreign")
	holder := f.AppUser(t, f.OrgA, "holder")
	successor := f.AppUser(t, f.OrgA, "successor")

	repo := assignment.NewPGRepository(f.App)
	mk := func(assetID string) *assignment.Assignment {
		return &assignment.Assignment{OrganizationID: f.OrgA, AssetID: assetID, AssignedTo: holder, AssignedBy: holder, Status: "active", AssignmentType: "assignment", Notes: "original"}
	}
	foreignA, ownReturn, ownTransfer := mk(foreign), mk(own), mk(own)
	for _, a := range []*assignment.Assignment{foreignA, ownReturn, ownTransfer} {
		if err := repo.Create(f.OrgCtx(f.OrgA), a); err != nil {
			t.Fatalf("org-wide assignment of %s: %v", a.AssetID, err)
		}
	}

	mux := chi.NewRouter()
	assignment.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1, UserID: holder})
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	type row struct{ status, notes, condition, assetID string }
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(notes, ''), COALESCE(return_condition, ''), COALESCE(asset_id::text, '') FROM assignment WHERE id = $1`, id).Scan(&r.status, &r.notes, &r.condition, &r.assetID); err != nil {
			t.Fatalf("stored assignment %s: %v", id, err)
		}
		return r
	}
	transfers := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM assignment WHERE organization_id = $1 AND assignment_type = 'transfer'`, f.OrgA).Scan(&n); err != nil {
			t.Fatalf("count transfers: %v", err)
		}
		return n
	}
	want := row{status: "active", notes: "original", assetID: foreign}

	if w := post("/api/v1/assignments/"+foreignA.ID+"/return", `{"return_condition":"damaged","notes":"hijacked"}`); w.Code != http.StatusNotFound {
		t.Errorf("client-1 return of a client-2 assignment = %d %s, want 404", w.Code, w.Body)
	}
	if got := stored(foreignA.ID); got != want {
		t.Errorf("client-2 assignment after foreign return: %+v, want %+v", got, want)
	}
	if w := post("/api/v1/assignments/"+foreignA.ID+"/transfer", `{"new_assignee":"`+successor+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("client-1 transfer of a client-2 assignment = %d %s, want 404", w.Code, w.Body)
	}
	if got := stored(foreignA.ID); got != want {
		t.Errorf("client-2 assignment after foreign transfer: %+v, want %+v", got, want)
	}
	if n := transfers(); n != 0 {
		t.Errorf("foreign transfer created %d transfer assignments, want 0", n)
	}

	// The repository refuses the write of both handlers on its own, also
	// when the caller skipped the visibility check of GetByID.
	for _, status := range []string{"returned", "transferred"} {
		upd := *foreignA
		upd.Status, upd.Notes = status, "hijacked"
		if err := repo.Update(ctx, f.OrgA, foreignA.ID, &upd); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("client-1 repository update (%s) of a client-2 assignment: err=%v, want not found", status, err)
		}
		if got := stored(foreignA.ID); got != want {
			t.Errorf("client-2 assignment after repository update (%s): %+v, want %+v", status, got, want)
		}
	}

	// Positive control: the client-1 principal returns and transfers its own
	// assignments.
	if w := post("/api/v1/assignments/"+ownReturn.ID+"/return", `{"return_condition":"good","notes":"back"}`); w.Code != http.StatusOK {
		t.Fatalf("client-1 return of own assignment = %d %s, want 200", w.Code, w.Body)
	}
	if got := stored(ownReturn.ID); got.status != "returned" || got.condition != "good" || got.notes != "back" {
		t.Errorf("own assignment after return: %+v, want returned/good/back", got)
	}
	if w := post("/api/v1/assignments/"+ownTransfer.ID+"/transfer", `{"new_assignee":"`+successor+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("client-1 transfer of own assignment = %d %s, want 201", w.Code, w.Body)
	}
	if got := stored(ownTransfer.ID); got.status != "transferred" {
		t.Errorf("own assignment after transfer: %+v, want transferred", got)
	}
	if n := transfers(); n != 1 {
		t.Errorf("own transfer: %d transfer assignments, want 1", n)
	}
}
