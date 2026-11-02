package composition_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/composition"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestCompositionRepositoryClientScope runs the composition repository with
// the principal's tenant scope (TEN-06, WP-017): a principal restricted to
// client 1 neither sees nor removes the composition of a client-2 asset and
// cannot attach a client-2 CI to its own asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCompositionRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	ownAsset := f.Asset(t, f.OrgA, f.Client1, "scopetest-2e-own")
	foreignAsset := f.Asset(t, f.OrgA, f.Client2, "scopetest-2e-foreign")
	ownCI := f.CI(t, f.OrgA, f.Client1, "composition-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "composition-c2")

	repo := composition.NewPGRepository(f.App)
	foreignC := &composition.Composition{OrganizationID: f.OrgA, ParentAssetID: foreignAsset, ChildCIID: foreignCI}
	if err := repo.Create(f.OrgCtx(f.OrgA), foreignC); err != nil {
		t.Fatalf("org-wide composition: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, &composition.Composition{OrganizationID: f.OrgA, ParentAssetID: ownAsset, ChildCIID: foreignCI}); err == nil {
		t.Fatal("client-1 principal attached a client-2 CI")
	}
	if err := repo.Create(ctx, &composition.Composition{OrganizationID: f.OrgA, ParentAssetID: ownAsset, ChildCIID: ownCI}); err != nil {
		t.Fatalf("client-1 composition of own objects: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ChildCIID != ownCI {
		t.Fatalf("client-1 compositions: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignC.ID); err == nil {
		t.Fatal("client-1 principal read the composition of a client-2 asset")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignC.ID); err == nil {
		t.Fatal("client-1 principal removed the composition of a client-2 asset")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM composition WHERE id = $1`, foreignC.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 composition: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestCompositionForeignClientUpdate covers the update of an existing link
// (TEN-06, AST-01, WP-537): a principal restricted to client 1 cannot change
// role, position or independence flags of the composition of a client-2
// asset (not found) and the client-2 row stays unchanged; its own link it
// updates.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCompositionForeignClientUpdate(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	ownAsset := f.Asset(t, f.OrgA, f.Client1, "scopetest-2e-own")
	foreignAsset := f.Asset(t, f.OrgA, f.Client2, "scopetest-2e-foreign")
	ownCI := f.CI(t, f.OrgA, f.Client1, "composition-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "composition-c2")

	repo := composition.NewPGRepository(f.App)
	role, position := "disk", "slot-1"
	foreignC := &composition.Composition{OrganizationID: f.OrgA, ParentAssetID: foreignAsset, ChildCIID: foreignCI, Role: role, Position: position}
	ownC := &composition.Composition{OrganizationID: f.OrgA, ParentAssetID: ownAsset, ChildCIID: ownCI, Role: role, Position: position}
	for _, c := range []*composition.Composition{foreignC, ownC} {
		if err := repo.Create(f.OrgCtx(f.OrgA), c); err != nil {
			t.Fatalf("org-wide composition: %v", err)
		}
	}
	type row struct {
		role, position string
		assignable     bool
	}
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(role, ''), COALESCE(position, ''), independently_assignable FROM composition WHERE id = $1`, id).Scan(&r.role, &r.position, &r.assignable); err != nil {
			t.Fatalf("stored composition %s: %v", id, err)
		}
		return r
	}
	want := stored(foreignC.ID)
	newRole, newPosition, assignable := "hijacked", "slot-9", !want.assignable
	req := composition.UpdateRequest{Role: &newRole, Position: &newPosition, IndependentlyAssignable: &assignable}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.Update(ctx, f.OrgA, foreignC.ID, req); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("client-1 update of a client-2 composition: err=%v, want not found", err)
	}
	if got := stored(foreignC.ID); got != want {
		t.Errorf("client-2 composition after foreign update: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates its own link.
	updated, err := repo.Update(ctx, f.OrgA, ownC.ID, req)
	if err != nil || updated == nil || updated.Role != newRole {
		t.Fatalf("client-1 update of own composition: %+v err=%v", updated, err)
	}
	if got := stored(ownC.ID); got != (row{role: newRole, position: newPosition, assignable: assignable}) {
		t.Errorf("own composition after update: %+v", got)
	}
}
