package location_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
)

// TestLocationRepositoryClientScope runs the asset location history with the
// principal's tenant scope (TEN-06, WP-014): a principal restricted to client
// 1 neither reads nor extends the location history of a client-2 asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "24")
	bg := context.Background()
	asset := func(clientID, tag string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg,
			`INSERT INTO asset (organization_id, client_id, asset_tag, name) VALUES ($1, $2, $3, $3) RETURNING id::text`,
			f.OrgA, clientID, tag).Scan(&id); err != nil {
			t.Fatalf("seed asset %s: %v", tag, err)
		}
		return id
	}
	own := asset(f.Client1, "scopetest-24-c1")
	foreign := asset(f.Client2, "scopetest-24-c2")

	repo := location.NewPGRepository(f.App)
	if err := repo.Record(f.OrgCtx(f.OrgA), &location.Entry{OrganizationID: f.OrgA, AssetID: foreign, Lat: 47.8, Lon: 12.6}); err != nil {
		t.Fatalf("org-wide record: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	entries, total, err := repo.History(ctx, f.OrgA, foreign, page)
	if err != nil || total != 0 || len(entries) != 0 {
		t.Fatalf("client-1 history of client-2 asset: total=%d %+v err=%v, want none", total, entries, err)
	}
	if err = repo.Record(ctx, &location.Entry{OrganizationID: f.OrgA, AssetID: foreign, Lat: 1, Lon: 1}); !errors.Is(err, location.ErrAssetNotFound) {
		t.Fatalf("client-1 record for client-2 asset: got %v, want ErrAssetNotFound", err)
	}
	if err = repo.Record(ctx, &location.Entry{OrganizationID: f.OrgA, AssetID: own, Lat: 47.9, Lon: 12.1}); err != nil {
		t.Fatalf("client-1 record for own asset: %v", err)
	}
	if entries, total, err = repo.History(ctx, f.OrgA, own, page); err != nil || total != 1 || len(entries) != 1 {
		t.Fatalf("client-1 history of own asset: total=%d err=%v, want 1", total, err)
	}
	if _, _, err = repo.History(bg, f.OrgA, own, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("history without scope: got %v, want ErrNoTenantScope", err)
	}
}
