package reservation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
)

// TestReservationRepositoryClientScope runs the reservation repository and
// the availability projection with the principal's tenant scope (TEN-06,
// WP-017): a principal restricted to client 1 neither sees, releases nor
// creates reservations of client-2 assets, and availability lists no client-2
// asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReservationRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "2d")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-2d-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-2d-foreign")

	repo := reservation.NewPGRepository(f.App)
	mk := func(assetID string) *reservation.Reservation {
		return &reservation.Reservation{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: assetID, Quantity: 1}
	}
	foreignR := mk(foreign)
	if err := repo.Create(f.OrgCtx(f.OrgA), foreignR); err != nil {
		t.Fatalf("org-wide reservation: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, mk(foreign)); err == nil {
		t.Fatal("client-1 principal reserved a client-2 asset")
	}
	if err := repo.Create(ctx, mk(own)); err != nil {
		t.Fatalf("client-1 reservation of own asset: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].AssetID != own {
		t.Fatalf("client-1 reservations: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.Transition(ctx, f.OrgA, foreignR.ID, "released"); err == nil {
		t.Fatal("client-1 principal released the reservation of a client-2 asset")
	}

	avail, err := reservation.NewPGAvailability(f.App).Availability(ctx, f.OrgA, reservation.AvailabilityFilter{ItemKind: "asset"})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	for _, a := range avail {
		if a.ItemID == foreign {
			t.Fatal("client-1 availability lists a client-2 asset")
		}
	}

	var state string
	if err = f.Admin.QueryRow(context.Background(), `SELECT state FROM reservation WHERE id = $1`, foreignR.ID).Scan(&state); err != nil || state != "active" {
		t.Fatalf("client-2 reservation: state=%q err=%v, want active", state, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
