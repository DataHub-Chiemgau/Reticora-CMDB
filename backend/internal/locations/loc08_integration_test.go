package locations_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestStorageHierarchyThroughAPI covers WP-282 (LOC-08): the storage chain
// site > warehouse > zone > shelf > bin is enforced by the canonical location
// tree through the HTTP layer against PostgreSQL. Creating the chain
// succeeds, every other parent is a 422 problem naming parent_id, on create
// and on a move, and a valid move re-derives site, client and path of the
// subtree, as the tree endpoint shows.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStorageHierarchyThroughAPI(t *testing.T) {
	f := scopetest.Seed(t, "43")
	repo := locations.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	locations.NewHandler(repo).RegisterRoutes(mux)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	create := func(body string) locations.Location {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/locations", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d %s", body, w.Code, w.Body.String())
		}
		var loc locations.Location
		if err := json.Unmarshal(w.Body.Bytes(), &loc); err != nil {
			t.Fatal(err)
		}
		return loc
	}
	expectParentProblem := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: status %d type %q body %s, want a 422 problem", name, w.Code, w.Header().Get("Content-Type"), w.Body.String())
			return
		}
		var p struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.Violations) == 0 || p.Violations[0].Field != "parent_id" {
			t.Errorf("%s: problem %s, want violations[0].field = parent_id (%v)", name, w.Body.String(), err)
		}
	}
	child := func(kind locations.Kind, parentID, name string) string {
		return `{"kind":"` + string(kind) + `","parent_id":"` + parentID + `","name":"` + name + `"}`
	}

	// The storage chain, each node with 201.
	site := create(`{"kind":"site","client_id":"` + f.Client1 + `","name":"l8"}`)
	wh := create(child(locations.KindWarehouse, site.ID, "l8-w"))
	zone := create(child(locations.KindZone, wh.ID, "l8-z"))
	shelf := create(child(locations.KindShelf, zone.ID, "l8-s"))
	bin := create(child(locations.KindBin, shelf.ID, "l8-n"))
	building := create(child(locations.KindBuilding, site.ID, "l8-b"))

	// Every other parent of a storage kind, and a building below a warehouse,
	// is refused.
	for _, c := range []struct{ name, body string }{
		{"zone below site", child(locations.KindZone, site.ID, "x")},
		{"shelf below warehouse", child(locations.KindShelf, wh.ID, "x")},
		{"bin below zone", child(locations.KindBin, zone.ID, "x")},
		{"warehouse below building", child(locations.KindWarehouse, building.ID, "x")},
		{"warehouse without parent", `{"kind":"warehouse","name":"x"}`},
		{"building below warehouse", child(locations.KindBuilding, wh.ID, "x")},
	} {
		expectParentProblem(c.name, call(http.MethodPost, "/api/v1/locations", c.body))
	}

	// The tree, read in a request of its own, nests the chain below the site
	// with the site's id and the parent path plus the own label.
	treeOf := func() []*locations.TreeNode {
		t.Helper()
		w := call(http.MethodGet, "/api/v1/locations/tree", "")
		if w.Code != http.StatusOK {
			t.Fatalf("tree: status %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Data []*locations.TreeNode `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	find := func(nodes []*locations.TreeNode, id string) *locations.TreeNode {
		for _, n := range nodes {
			if n.ID == id {
				return n
			}
		}
		return nil
	}
	// chain follows ids from the roots down and checks site_id and path.
	chain := func(roots []*locations.TreeNode, siteID string, ids ...string) {
		t.Helper()
		nodes, parentPath := roots, ""
		for i, id := range ids {
			n := find(nodes, id)
			if n == nil {
				t.Errorf("tree: node %d (%s) not below %s", i, id, parentPath)
				return
			}
			want := label(id)
			if parentPath != "" {
				want = parentPath + "." + want
			}
			if n.SiteID != siteID || n.Path != want {
				t.Errorf("tree: %s site %s path %s, want %s %s", n.Kind, n.SiteID, n.Path, siteID, want)
			}
			nodes, parentPath = n.Children, n.Path
		}
	}
	chain(treeOf(), site.ID, site.ID, wh.ID, zone.ID, shelf.ID, bin.ID)

	// Moving the zone below a site is refused, below the warehouse of a
	// second site (another client) re-derives the subtree.
	expectParentProblem("move zone below site", call(http.MethodPatch, "/api/v1/locations/"+zone.ID, `{"parent_id":"`+site.ID+`"}`))
	other := buildTree(t, repo, ctx, f.OrgA, f.Client2, "l8b")
	if w := call(http.MethodPatch, "/api/v1/locations/"+zone.ID, `{"parent_id":"`+other.warehouse.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("move zone to the second warehouse: status %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{shelf.ID, bin.ID} {
		w := call(http.MethodGet, "/api/v1/locations/"+id, "")
		var loc locations.Location
		if err := json.Unmarshal(w.Body.Bytes(), &loc); err != nil || w.Code != http.StatusOK {
			t.Fatalf("get %s: status %d %v", id, w.Code, err)
		}
		if loc.SiteID != other.site.ID || loc.ClientID != f.Client2 || !strings.HasPrefix(loc.Path, other.site.Path+".") {
			t.Errorf("%s after the move: site %s client %s path %s, want %s %s %s.*", loc.Kind, loc.SiteID, loc.ClientID, loc.Path, other.site.ID, f.Client2, other.site.Path)
		}
	}
	roots := treeOf()
	chain(roots, other.site.ID, other.site.ID, other.warehouse.ID, zone.ID, shelf.ID, bin.ID)
	if old := find(roots, site.ID); old == nil || find(old.Children, wh.ID) == nil || len(find(old.Children, wh.ID).Children) != 0 {
		t.Errorf("tree: the first warehouse should remain without children after the move")
	}
}
