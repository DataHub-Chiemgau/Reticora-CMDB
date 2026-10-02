package assignment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
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
