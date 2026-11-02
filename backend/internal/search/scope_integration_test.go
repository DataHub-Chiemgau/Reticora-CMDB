package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// permissionStub grants exactly the listed permission keys.
type permissionStub struct {
	permission.Repository
	granted map[string]bool
}

func (p permissionStub) HasPermission(_ context.Context, _, _, key string) (bool, error) {
	return p.granted[key], nil
}

// TestSearchHitsRespectClientSiteAndReadPermission covers WP-032 (SRC-01):
// search_document derives client and site from its entity (migration 000067),
// its policy hides hits of foreign clients and sites even without the
// query-time check, the rows follow a moved entity, and the handler drops
// hits whose entity type the caller may not read. Assets and their
// reservations carry the site of the asset's location, reservations of
// quantity items client and site of the item (WP-393, migration 000152;
// asset site: WP-392): a site-1 principal finds no asset or reservation of
// site 2.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchHitsRespectClientSiteAndReadPermission(t *testing.T) {
	f := scopetest.Seed(t, "4d")
	bg := context.Background()
	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "search "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	place := func(id, site string) {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, id, site); err != nil {
			t.Fatalf("place ci: %v", err)
		}
	}
	atSite1 := f.CI(t, f.OrgA, f.Client1, "scopehit-site1")
	place(atSite1, site1)
	atSite2 := f.CI(t, f.OrgA, f.Client1, "scopehit-site2")
	place(atSite2, site2)
	foreign := f.CI(t, f.OrgA, f.Client2, "scopehit-client2")
	foreignAsset := f.Asset(t, f.OrgA, f.Client2, "scopehit-asset2")
	ownAsset := f.Asset(t, f.OrgA, f.Client1, "scopehit-asset1")
	assetAt := func(name, site string) string {
		id := f.Asset(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, id, site); err != nil {
			t.Fatalf("place asset: %v", err)
		}
		return id
	}
	assetSite1 := assetAt("scopehit-asset-site1", site1)
	assetSite2 := assetAt("scopehit-asset-site2", site2)
	itemAt := func(client, name string, site any) string {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO quantity_item (organization_id, client_id, name, location_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, client, name, site).Scan(&id); err != nil {
			t.Fatalf("seed quantity item: %v", err)
		}
		return id
	}
	itemSite2 := itemAt(f.Client1, "scopehit-item-site2", site2)
	itemClient2 := itemAt(f.Client2, "scopehit-item-client2", nil)
	reserve := func(column, item, ref string) string {
		kind := "asset"
		if column == "quantity_item_id" {
			kind = "quantity_item"
		}
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO reservation (organization_id, item_kind, `+column+`, project_ref) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, kind, item, ref).Scan(&id); err != nil {
			t.Fatalf("seed reservation: %v", err)
		}
		return id
	}
	resSite2 := reserve("asset_id", assetSite2, "scopehit-res-site2")
	resItemSite2 := reserve("quantity_item_id", itemSite2, "scopehit-res-item-site2")
	resItemClient2 := reserve("quantity_item_id", itemClient2, "scopehit-res-item-client2")

	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	derived := func(entityID string) (client, site string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM search_document WHERE entity_id = $1`,
			entityID).Scan(&client, &site); err != nil {
			t.Fatalf("read search row of %s: %v", entityID, err)
		}
		return client, site
	}
	if c, s := derived(atSite2); c != f.Client1 || s != site2 {
		t.Errorf("search row of the site-2 CI: client %s site %s", c, s)
	}
	if c, _ := derived(foreignAsset); c != f.Client2 {
		t.Errorf("search row of the client-2 asset: client %s", c)
	}
	for _, c := range []struct{ name, id, client, site string }{
		{"site-2 asset", assetSite2, f.Client1, site2},
		{"reservation of the site-2 asset", resSite2, f.Client1, site2},
		{"reservation of the site-2 quantity item", resItemSite2, f.Client1, site2},
		{"reservation of the client-2 quantity item", resItemClient2, f.Client2, ""},
	} {
		if gotClient, gotSite := derived(c.id); gotClient != c.client || gotSite != c.site {
			t.Errorf("search row of the %s: client %q site %q, want %q %q", c.name, gotClient, gotSite, c.client, c.site)
		}
	}

	hits := func(ctx context.Context) map[string]bool {
		t.Helper()
		res, err := repo.Query(ctx, search.Query{OrganizationID: f.OrgA, Text: "scopehit", Limit: 50})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out := map[string]bool{}
		for _, h := range res.Data {
			out[h.EntityID] = true
		}
		return out
	}
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	all := []string{atSite1, atSite2, foreign, foreignAsset, ownAsset, assetSite1, assetSite2, resSite2, resItemSite2, resItemClient2}
	for _, c := range []struct {
		name string
		ctx  context.Context
		want map[string]bool
	}{
		{"client 1", f.ClientCtx(f.Client1), map[string]bool{atSite1: true, atSite2: true, ownAsset: true,
			assetSite1: true, assetSite2: true, resSite2: true, resItemSite2: true}},
		// Rows without site (the unplaced assets, the CI and the quantity
		// item of client 2) stay visible; nothing of site 2.
		{"site 1", database.ContextWithTenantScope(bg, &siteScope), map[string]bool{atSite1: true, foreign: true, foreignAsset: true, ownAsset: true,
			assetSite1: true, resItemClient2: true}},
	} {
		got := hits(c.ctx)
		for _, id := range all {
			if got[id] != c.want[id] {
				t.Errorf("%s: hit %s present=%v, want %v", c.name, id, got[id], c.want[id])
			}
		}
	}

	// The policy alone hides foreign rows, without the query-time check.
	c1 := database.OrgWideScope(f.OrgA, f.User)
	c1.Clients = database.ScopeIDs(f.Client1)
	var n int
	if err := database.WithTenant(bg, f.App, &c1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM search_document WHERE entity_id = ANY($1::uuid[])`,
			[]string{foreign, foreignAsset}).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("client 1 reads %d foreign search rows directly, %v", n, err)
	}
	if err := database.WithTenant(bg, f.App, &siteScope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM search_document WHERE entity_id = ANY($1::uuid[])`,
			[]string{atSite2, assetSite2, resSite2, resItemSite2}).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("site 1 reads %d search rows of site 2 directly, %v", n, err)
	}

	// An asset or quantity item that moves to site 2 takes its search rows
	// and those of its reservations along.
	resSite1 := reserve("asset_id", assetSite1, "scopehit-res-site1")
	itemSite1 := itemAt(f.Client1, "scopehit-item-site1", site1)
	resItemSite1 := reserve("quantity_item_id", itemSite1, "scopehit-res-item-site1")
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	for _, c := range []struct{ table, id string }{{"asset", assetSite1}, {"quantity_item", itemSite1}} {
		if _, err := f.Admin.Exec(bg, `UPDATE `+c.table+` SET location_id = $2 WHERE id = $1`, c.id, site2); err != nil {
			t.Fatalf("move %s: %v", c.table, err)
		}
	}
	for _, id := range []string{assetSite1, resSite1, resItemSite1} {
		if _, s := derived(id); s != site2 {
			t.Errorf("search row of %s after the move: site %q, want site 2", id, s)
		}
	}
	if got := hits(database.ContextWithTenantScope(bg, &siteScope)); got[assetSite1] || got[resSite1] || got[resItemSite1] {
		t.Error("site 1 still finds the asset, quantity item reservation or asset reservation that moved to site 2")
	}

	// A CI that moves to client 2 takes its search row along. It leaves its
	// location, which belongs to client 1 (WP-054).
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2, location_id = NULL WHERE id = $1`, atSite1, f.Client2); err != nil {
		t.Fatalf("move ci: %v", err)
	}
	if c, _ := derived(atSite1); c != f.Client2 {
		t.Errorf("search row after the CI moved: client %s, want client 2", c)
	}
	if hits(f.ClientCtx(f.Client1))[atSite1] {
		t.Error("client 1 still finds the CI that moved to client 2")
	}

	// The handler checks the read permission of each hit's entity type.
	searchAs := func(userID string, granted map[string]bool) map[string]bool {
		t.Helper()
		mux := chi.NewRouter()
		search.NewHandler(repo, permissionStub{granted: granted}).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=scopehit", nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID})
		scope := database.OrgWideScope(f.OrgA, f.User)
		req = req.WithContext(database.ContextWithTenantScope(ctx, &scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("search: status %d: %s", w.Code, w.Body.String())
		}
		var res search.Result
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		types := map[string]bool{}
		for _, h := range res.Data {
			types[h.EntityType] = true
		}
		return types
	}
	if got := searchAs(f.User, map[string]bool{"ci:read": true}); !got["ci"] || got["asset"] {
		t.Errorf("ci:read only: entity types %v, want ci without asset", got)
	}
	if got := searchAs(f.User, map[string]bool{"asset:read": true}); got["ci"] || !got["asset"] {
		t.Errorf("asset:read only: entity types %v, want asset without ci", got)
	}
	if got := searchAs("", map[string]bool{"ci:read": true, "asset:read": true}); len(got) != 0 {
		t.Errorf("without user: entity types %v, want none", got)
	}
}
