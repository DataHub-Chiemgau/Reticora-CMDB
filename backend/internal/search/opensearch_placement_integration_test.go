package search_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// fakeOpenSearch is an httptest OpenSearch that stores the documents the
// backend PUTs, counts the PUTs per document and returns all documents on
// _search: the backend's own scope post-filter and the PostgreSQL check
// decide, as with a real index whose documents carry a stale site.
type fakeOpenSearch struct {
	*httptest.Server
	mu   sync.Mutex
	docs map[string]search.Document
	puts map[string]int
}

func newFakeOpenSearch(t *testing.T) *fakeOpenSearch {
	t.Helper()
	f := &fakeOpenSearch{docs: map[string]search.Document{}, puts: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/_doc/"):
			var d search.Document
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &d)
			f.docs[d.EntityType+"/"+d.EntityID] = d
			f.puts[d.EntityType+"/"+d.EntityID]++
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/_doc/"):
			for k := range f.docs {
				if strings.HasSuffix(r.URL.Path, "-"+strings.Replace(k, "/", "-", 1)) {
					delete(f.docs, k)
				}
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/_search"):
			hits := []map[string]any{}
			for _, d := range f.docs {
				hits = append(hits, map[string]any{"_score": 1, "_source": d})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": len(hits)}, "hits": hits}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// doc returns the stored document and the number of PUTs it received.
func (f *fakeOpenSearch) doc(entityType, id string) (search.Document, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.docs[entityType+"/"+id], f.puts[entityType+"/"+id]
}

// TestOpenSearchFollowsRackPlacement covers WP-346 (TEC-12; F-054) against
// PostgreSQL with an OpenSearch fake: a rack mount, move and unmount index
// the placed CI again (for a CI bound to an asset also the asset and all its
// CIs), so its remote document carries the new site; a client change
// through the CI service does the same. A move no indexer sees (a room moved
// to another site) is caught by the PostgreSQL check of the hybrid backend:
// a principal of the old site gets no hit, while asset and ticket hits stay
// for the principals entitled to them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOpenSearchFollowsRackPlacement(t *testing.T) {
	f := scopetest.Seed(t, "df")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	roomA, roomB := f.Room(t, f.Client1, "t1-os-a"), f.Room(t, f.Client1, "t1-os-b")
	siteOf := func(room string) string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, room).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	siteA, siteB := siteOf(roomA), siteOf(roomB)

	remote := newFakeOpenSearch(t)
	hybrid := &search.HybridBackend{
		Remote: search.NewOpenSearchBackend(search.OpenSearchConfig{URL: remote.URL, Index: "t1-idx"}, remote.Client()),
		Source: search.NewPGRepository(f.App),
	}
	// The wiring of cmd/server/main.go.
	cis := ci.NewIndexingRepository(ci.NewPGRepository(f.App), search.NewCIIndexer(hybrid))
	racks := rack.NewIndexingRepository(rack.NewPGRepository(f.App), hybrid)

	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	newCI := func(name, client, location string) *ci.Item {
		t.Helper()
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typeID, LocationID: location, Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatalf("create CI %s: %v", name, err)
		}
		return item
	}
	query := func(scope *database.TenantScope, text string) map[string]bool {
		t.Helper()
		res, err := hybrid.Query(database.ContextWithTenantScope(bg, scope), search.Query{OrganizationID: f.OrgA, Text: text})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		found := map[string]bool{}
		for _, h := range res.Data {
			found[h.EntityID] = true
		}
		return found
	}
	siteScope := func(site string) *database.TenantScope {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(site)
		return &scope
	}
	clientScope := func(client string) *database.TenantScope {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Clients = database.ScopeIDs(client)
		return &scope
	}
	dbSite := func(ciID string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM ci WHERE id = $1`, ciID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// expectSite checks the remote document, the database and the hits of
	// both site principals for a CI that lies in site want.
	expectSite := func(step string, item *ci.Item, want string) {
		t.Helper()
		if got := dbSite(item.ID); got != want {
			t.Fatalf("%s: setup: ci.site_id = %q, want %q", step, got, want)
		}
		if d, _ := remote.doc("ci", item.ID); d.SiteID != want {
			t.Errorf("%s: remote document of %s carries site %q, want %q", step, item.Name, d.SiteID, want)
		}
		other := siteA
		if want == siteA {
			other = siteB
		}
		if !query(siteScope(want), item.Name)[item.ID] {
			t.Errorf("%s: a principal of the CI's site does not find %s through OpenSearch", step, item.Name)
		}
		if query(siteScope(other), item.Name)[item.ID] {
			t.Errorf("%s: CH25/TEN-05: a principal of the other site still finds %s through OpenSearch", step, item.Name)
		}
	}

	rackA := &rack.Rack{OrganizationID: f.OrgA, RoomID: roomA, Name: "t1os-rack-a", HeightUnits: 42}
	rackB := &rack.Rack{OrganizationID: f.OrgA, RoomID: roomB, Name: "t1os-rack-b", HeightUnits: 42}
	for _, rk := range []*rack.Rack{rackA, rackB} {
		if err := racks.CreateRack(ctx, rk); err != nil {
			t.Fatalf("create rack: %v", err)
		}
	}

	t.Run("mount, move and unmount", func(t *testing.T) {
		item := newCI("t1os-moved-ci", f.Client1, roomA)
		// Control before the move: the fake index and the post-filter work.
		expectSite("control", item, siteA)

		// POST /api/v1/racks/{id}/mounts: the CI moves into a rack of site B.
		m := &rack.RackMount{OrganizationID: f.OrgA, RackID: rackB.ID, CIID: item.ID, PositionU: 1, HeightU: 1, Face: "front"}
		if err := racks.CreateMount(ctx, m); err != nil {
			t.Fatalf("mount CI: %v", err)
		}
		expectSite("mount into site B", item, siteB)

		// PUT /api/v1/racks/{id}/mounts/{ci_id}: back into a rack of site A.
		if _, err := racks.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &rackA.ID}); err != nil {
			t.Fatalf("move mount: %v", err)
		}
		expectSite("move into site A", item, siteA)

		// DELETE /api/v1/racks/{id}/mounts/{ci_id}: the CI returns to the
		// rack's room and is indexed again.
		_, before := remote.doc("ci", item.ID)
		if err := racks.DeleteMount(ctx, f.OrgA, m.ID); err != nil {
			t.Fatalf("unmount CI: %v", err)
		}
		if _, after := remote.doc("ci", item.ID); after != before+1 {
			t.Errorf("unmount: remote document indexed %d times, want once more than %d", after, before)
		}
		expectSite("unmount", item, siteA)
	})

	t.Run("CI bound to an asset", func(t *testing.T) {
		primary := newCI("t1os-asset-ci-1", f.Client1, roomA)
		second := newCI("t1os-asset-ci-2", f.Client1, roomA)
		assetID := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO asset (id, organization_id, client_id, asset_tag, name, location_id, ci_id)
			VALUES ($1, $2, $3, 'T1OS-ASSET', 't1os-asset', $4, $5)`, assetID, f.OrgA, f.Client1, roomA, primary.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET asset_id = $2 WHERE id = $1`, second.ID, assetID); err != nil {
			t.Fatal(err)
		}
		// The asset document reaches both indexes through the reindex.
		if _, err := hybrid.ReindexTenant(ctx, f.OrgA); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		_, assetPuts := remote.doc("asset", assetID)
		if err := racks.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rackB.ID, CIID: primary.ID, PositionU: 10, HeightU: 1, Face: "front"}); err != nil {
			t.Fatalf("mount asset-bound CI: %v", err)
		}
		expectSite("asset-bound CI", primary, siteB)
		expectSite("other CI of the asset", second, siteB)
		if d, puts := remote.doc("asset", assetID); puts != assetPuts+1 || d.ClientID != f.Client1 {
			t.Errorf("asset document: %d PUTs (client %q), want %d (client %s)", puts, d.ClientID, assetPuts+1, f.Client1)
		}
	})

	t.Run("client change through the CI service", func(t *testing.T) {
		item := newCI("t1os-client-ci", f.Client1, "")
		if !query(clientScope(f.Client1), item.Name)[item.ID] || query(clientScope(f.Client2), item.Name)[item.ID] {
			t.Fatal("control: the client-1 principal must find the CI, the client-2 principal must not")
		}
		if _, err := ci.NewService(cis).Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{ClientID: &f.Client2}); err != nil {
			t.Fatalf("change client: %v", err)
		}
		if d, _ := remote.doc("ci", item.ID); d.ClientID != f.Client2 {
			t.Errorf("remote document after the client change carries client %q, want %s", d.ClientID, f.Client2)
		}
		if query(clientScope(f.Client1), item.Name)[item.ID] {
			t.Error("TEN-05: the client-1 principal still finds the CI that moved to client 2")
		}
		if !query(clientScope(f.Client2), item.Name)[item.ID] {
			t.Error("the client-2 principal does not find the CI that moved to client 2")
		}
	})

	t.Run("location moved without reindex", func(t *testing.T) {
		roomC := f.Room(t, f.Client1, "t1-os-c")
		siteC := siteOf(roomC)
		item := newCI("t1os-room-ci", f.Client1, roomC)
		assetID := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO asset (id, organization_id, client_id, asset_tag, name)
			VALUES ($1, $2, $3, 'T1OS-KEEP', 't1os-keep-asset')`, assetID, f.OrgA, f.Client1); err != nil {
			t.Fatal(err)
		}
		reporter := f.AppUser(t, f.OrgA, "t1os-reporter")
		var ticketID string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ticket (organization_id, client_id, title, reporter_id)
			VALUES ($1, $2, 't1os-keep-ticket', $3) RETURNING id::text`, f.OrgA, f.Client1, reporter).Scan(&ticketID); err != nil {
			t.Fatal(err)
		}
		if _, err := hybrid.ReindexTenant(ctx, f.OrgA); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		if !query(siteScope(siteC), item.Name)[item.ID] {
			t.Fatal("control: the site principal must find the CI before the room moves")
		}

		// The room moves into a building of site B; no indexer sees it, the
		// remote document keeps site C.
		if _, err := f.Admin.Exec(bg, `UPDATE room SET building_id = (SELECT building_id FROM room WHERE id = $2) WHERE id = $1`, roomC, roomB); err != nil {
			t.Fatalf("move room: %v", err)
		}
		if got := dbSite(item.ID); got != siteB {
			t.Fatalf("setup: ci.site_id after the room move = %q, want site B", got)
		}
		if d, _ := remote.doc("ci", item.ID); d.SiteID != siteC {
			t.Fatalf("setup: remote document carries site %q, want the stale site %s", d.SiteID, siteC)
		}
		if query(siteScope(siteC), item.Name)[item.ID] {
			t.Error("CH25/TEN-05: a principal of the old site finds the CI whose room moved to site B")
		}

		// Asset and ticket hits stay for the principals entitled to them.
		orgWide := database.OrgWideScope(f.OrgA, f.User)
		for _, p := range []struct {
			name  string
			scope *database.TenantScope
		}{
			{"org-wide", &orgWide},
			{"client 1", clientScope(f.Client1)},
			{"site B", siteScope(siteB)},
		} {
			found := query(p.scope, "t1os-keep-asset")
			if !found[assetID] {
				t.Errorf("%s principal: the asset hit is dropped", p.name)
			}
			if found = query(p.scope, "t1os-keep-ticket"); !found[ticketID] {
				t.Errorf("%s principal: the ticket hit is dropped", p.name)
			}
		}
		if query(clientScope(f.Client2), "t1os-keep-asset")[assetID] {
			t.Error("the client-2 principal finds the asset of client 1")
		}
	})
}
