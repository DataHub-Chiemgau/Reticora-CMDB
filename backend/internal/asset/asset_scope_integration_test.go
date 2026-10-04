package asset_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestAssetRepositoryClientScope runs the asset repository with the
// principal's tenant scope (TEN-06, WP-017): a principal restricted to client
// 1 neither sees nor changes a client-2 asset and cannot create one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2b")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2b-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2b-foreign")

	repo := asset.NewPGRepository(f.App)
	ctx := f.ClientCtx(f.Client1)
	assets, _, err := repo.List(ctx, f.OrgA, asset.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range assets {
		seen[a.ID] = true
	}
	if !seen[own] || seen[foreign] {
		t.Fatalf("client-1 assets: own=%v foreign=%v, want true/false", seen[own], seen[foreign])
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal read a client-2 asset")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal deleted a client-2 asset")
	}
	if err = repo.Create(ctx, &asset.Asset{OrganizationID: f.OrgA, ClientID: f.Client2, AssetTag: "scopetest-2b-intruder", Name: "intruder"}); err == nil {
		t.Fatal("client-1 principal created a client-2 asset")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM asset WHERE id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 asset: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, asset.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
