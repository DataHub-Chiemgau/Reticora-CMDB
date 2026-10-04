package disposal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
)

// TestDisposalRepositoryClientScope runs the disposal register with the
// principal's tenant scope (TEN-06, WP-018): a principal restricted to client
// 1 neither sees nor records the disposal of a client-2 asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDisposalRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "32")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-32-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-32-foreign")
	repo := disposal.NewPGRepository(f.App)
	rec := func(assetID string) *disposal.Record {
		return &disposal.Record{OrganizationID: f.OrgA, AssetID: assetID, Method: "recycling"}
	}
	foreignRec := rec(foreign)
	if err := repo.Create(f.OrgCtx(f.OrgA), foreignRec); err != nil {
		t.Fatalf("org-wide disposal: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, rec(foreign)); err == nil {
		t.Fatal("client-1 principal recorded the disposal of a client-2 asset")
	}
	if err := repo.Create(ctx, rec(own)); err != nil {
		t.Fatalf("client-1 disposal of own asset: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, disposal.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].AssetID != own {
		t.Fatalf("client-1 disposals: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignRec.ID); err == nil {
		t.Fatal("client-1 principal read the disposal of a client-2 asset")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM disposal_record WHERE asset_id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 disposals: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, disposal.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
