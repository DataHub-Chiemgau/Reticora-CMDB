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
// hits whose entity type the caller may not read.
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
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET site_id = $2 WHERE id = $1`, id, site); err != nil {
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
	for _, c := range []struct {
		name string
		ctx  context.Context
		want map[string]bool
	}{
		{"client 1", f.ClientCtx(f.Client1), map[string]bool{atSite1: true, atSite2: true, ownAsset: true}},
		{"site 1", database.ContextWithTenantScope(bg, &siteScope), map[string]bool{atSite1: true, foreign: true, foreignAsset: true, ownAsset: true}},
	} {
		got := hits(c.ctx)
		for _, id := range []string{atSite1, atSite2, foreign, foreignAsset, ownAsset} {
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

	// A CI that moves to client 2 takes its search row along.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, atSite1, f.Client2); err != nil {
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
