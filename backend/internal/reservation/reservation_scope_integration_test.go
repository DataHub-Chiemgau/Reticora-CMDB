package reservation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
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

// TestReservationScopeColumns covers WP-750 (TEN-02, E-09, E-40): the
// trigger reservation_scope derives client_id and site_id from the asset
// or quantity item whatever the writer passes, they follow a change of
// client or site of the asset, the policy reservation_isolation scopes
// the rows without the former subquery reservationVisible, a scoped
// principal cannot reserve an org-wide item, and the reservation.* events
// carry client and site from the columns.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReservationScopeColumns(t *testing.T) {
	f := scopetest.Seed(t, "2d")
	bg := context.Background()
	scalar := func(t *testing.T, sql string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	siteOf := func(room string) string {
		return scalar(t, `SELECT site_id::text FROM location WHERE id = $1`, room)
	}
	site1, site2 := siteOf(f.Room(t, f.Client1, "wp750-s1")), siteOf(f.Room(t, f.Client1, "wp750-s2"))
	located := func(tag, site string) string {
		return scalar(t, `INSERT INTO asset (organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, $3, $3, $4) RETURNING id::text`,
			f.OrgA, f.Client1, tag, site)
	}
	own := f.Asset(t, f.OrgA, f.Client1, "wp750-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "wp750-foreign")
	orgWide := f.Asset(t, f.OrgA, "", "wp750-orgwide")
	atSite1, atSite2 := located("wp750-site1", site1), located("wp750-site2", site2)
	orgItem := scalar(t, `INSERT INTO quantity_item (organization_id, name) VALUES ($1, 'wp750-item') RETURNING id::text`, f.OrgA)

	repo := reservation.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	reserve := func(t *testing.T, ctx context.Context, assetID string) *reservation.Reservation {
		t.Helper()
		res := &reservation.Reservation{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: assetID, Quantity: 1}
		if err := repo.Create(ctx, res); err != nil {
			t.Fatalf("reserve %s: %v", assetID, err)
		}
		return res
	}
	org := f.OrgCtx(f.OrgA)
	siteCtx := func(site string) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(site)
		return database.ContextWithTenantScope(bg, &scope)
	}
	visible := func(t *testing.T, ctx context.Context) map[string]bool {
		t.Helper()
		list, _, err := repo.List(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := map[string]bool{}
		for i := range list {
			out[list[i].AssetID] = true
		}
		return out
	}
	scopeOf := func(t *testing.T, id string) (client, site string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM reservation WHERE id = $1`, id).
			Scan(&client, &site); err != nil {
			t.Fatal(err)
		}
		return client, site
	}

	ownR := reserve(t, f.ClientCtx(f.Client1), own)
	foreignR := reserve(t, org, foreign)
	reserve(t, org, orgWide)
	site1R, site2R := reserve(t, org, atSite1), reserve(t, org, atSite2)

	t.Run("derivation", func(t *testing.T) {
		if ownR.ClientID != f.Client1 || ownR.SiteID != "" || site2R.ClientID != f.Client1 || site2R.SiteID != site2 {
			t.Fatalf("derived scope: own %q/%q, site 2 %q/%q", ownR.ClientID, ownR.SiteID, site2R.ClientID, site2R.SiteID)
		}
		// A written client or site is overwritten, also on update.
		id := scalar(t, `INSERT INTO reservation (organization_id, item_kind, asset_id, state, client_id, site_id)
			VALUES ($1, 'asset', $2, 'released', $3, $4) RETURNING id::text`, f.OrgA, atSite1, f.Client2, site2)
		if client, site := scopeOf(t, id); client != f.Client1 || site != site1 {
			t.Errorf("written scope client 2/site 2 stored as %q/%q, want client 1/site 1", client, site)
		}
		exec(t, `UPDATE reservation SET client_id = $2, site_id = NULL WHERE id = $1`, id, f.Client2)
		if client, site := scopeOf(t, id); client != f.Client1 || site != site1 {
			t.Errorf("updated scope stored as %q/%q, want client 1/site 1", client, site)
		}
		if client, _ := scopeOf(t, scalar(t, `INSERT INTO reservation (organization_id, item_kind, quantity_item_id, client_id)
			VALUES ($1, 'quantity_item', $2, $3) RETURNING id::text`, f.OrgA, orgItem, f.Client1)); client != "" {
			t.Errorf("reservation of an org-wide quantity item stored client %q, want none", client)
		}
	})

	t.Run("client scope by the policy", func(t *testing.T) {
		got := visible(t, f.ClientCtx(f.Client1))
		if !got[own] || !got[orgWide] || got[foreign] {
			t.Fatalf("client-1 principal sees %v, want own and org-wide, not client 2", got)
		}
		if _, err := repo.GetByID(f.ClientCtx(f.Client1), f.OrgA, foreignR.ID); !errors.Is(err, reservation.ErrNotFound) {
			t.Errorf("client-1 read of a client-2 reservation: %v, want ErrNotFound", err)
		}
		active, err := repo.ActiveForItem(f.ClientCtx(f.Client1), f.OrgA, "asset", foreign)
		if err != nil || len(active) != 0 {
			t.Errorf("client-1 active reservations of a client-2 asset: %d (%v), want 0", len(active), err)
		}
	})

	t.Run("site scope by the policy", func(t *testing.T) {
		got := visible(t, siteCtx(site1))
		if !got[atSite1] || got[atSite2] {
			t.Fatalf("site-1 principal sees %v, want site 1 and not site 2", got)
		}
		// The asset moves to site 2: the reservation follows (E-40).
		exec(t, `UPDATE asset SET location_id = $2 WHERE id = $1`, atSite1, site2)
		if _, site := scopeOf(t, site1R.ID); site != site2 {
			t.Fatalf("reservation site after the move: %q, want site 2", site)
		}
		if got = visible(t, siteCtx(site1)); got[atSite1] {
			t.Error("site-1 principal still sees the reservation of the asset moved to site 2")
		}
		if got = visible(t, siteCtx(site2)); !got[atSite1] || !got[atSite2] {
			t.Errorf("site-2 principal sees %v, want both assets of site 2", got)
		}
	})

	t.Run("client change follows", func(t *testing.T) {
		exec(t, `UPDATE asset SET client_id = $2 WHERE id = $1`, own, f.Client2)
		if client, _ := scopeOf(t, ownR.ID); client != f.Client2 {
			t.Fatalf("reservation client after the asset changed client: %q, want client 2", client)
		}
		if visible(t, f.ClientCtx(f.Client1))[own] {
			t.Error("client-1 principal still sees the reservation of an asset now of client 2")
		}
		released, err := repo.Transition(org, f.OrgA, ownR.ID, "released")
		if err != nil {
			t.Fatalf("release: %v", err)
		}
		if released.ClientID != f.Client2 {
			t.Errorf("released reservation client %q, want client 2", released.ClientID)
		}
	})

	t.Run("E-09: scoped principals do not reserve org-wide items", func(t *testing.T) {
		exec(t, `UPDATE reservation SET state = 'released' WHERE asset_id = $1`, orgWide)
		res := &reservation.Reservation{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: orgWide, Quantity: 1}
		if err := repo.Create(f.ClientCtx(f.Client1), res); !errors.Is(err, reservation.ErrAssetNotFound) {
			t.Errorf("client-1 reservation of an org-wide asset: %v, want ErrAssetNotFound", err)
		}
		item := &reservation.Reservation{OrganizationID: f.OrgA, ItemKind: "quantity_item", QuantityItemID: orgItem, Quantity: 1}
		if err := repo.Create(f.ClientCtx(f.Client1), item); !errors.Is(err, reservation.ErrItemNotFound) {
			t.Errorf("client-1 reservation of an org-wide quantity item: %v, want ErrItemNotFound", err)
		}
		if err := repo.Create(siteCtx(site2), &reservation.Reservation{OrganizationID: f.OrgA, ItemKind: "quantity_item",
			QuantityItemID: orgItem, Quantity: 1}); !errors.Is(err, reservation.ErrItemNotFound) {
			t.Errorf("site-2 reservation of a quantity item without site: %v, want ErrItemNotFound", err)
		}
	})

	t.Run("event payloads from the columns", func(t *testing.T) {
		for _, c := range []struct {
			id, typ, client, site string
		}{
			{site2R.ID, events.ReservationCreated, f.Client1, site2},
			// Released after the asset changed client: the column decides.
			{ownR.ID, events.ReservationReleased, f.Client2, ""},
		} {
			var client, site string
			if err := f.Admin.QueryRow(bg, `SELECT COALESCE(data->>'client_id', ''), COALESCE(data->>'site_id', '') FROM event_outbox
				WHERE entity_id::text = $1 AND type = $2`, c.id, c.typ).Scan(&client, &site); err != nil {
				t.Fatalf("%s of %s: %v", c.typ, c.id, err)
			}
			if client != c.client || site != c.site {
				t.Errorf("%s payload client/site %q/%q, want %q/%q", c.typ, client, site, c.client, c.site)
			}
		}
	})
}
