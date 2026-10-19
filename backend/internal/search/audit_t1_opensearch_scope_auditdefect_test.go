//go:build auditdefect

package search_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (fails until the
// product is fixed).
//
// WP-033 (SRC-01; TEN-05, CH25) P2 "OpenSearch documents keep a stale site
// after a CI moves via rack placement; site-restricted users still find the
// moved CI": the remote index is written only by ci.NewIndexingRepository
// and ReindexTenant; rack/placement.go updates ci.location_id in SQL, the
// trigger changes ci.site_id, and no indexer call follows. HybridBackend's
// visibility check re-checks only the stale document fields.
//
// The remote OpenSearch is an httptest fake that stores the documents the
// backend PUTs and returns all of them on _search (the backend's own scope
// post-filter decides, as with a real index without site filter).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1OpenSearchFollowsRackPlacement' ./internal/search/

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

func TestAuditT1OpenSearchFollowsRackPlacement(t *testing.T) {
	f := scopetest.Seed(t, "df")
	bg := context.Background()
	roomA, roomB := f.Room(t, f.Client1, "t1-os-a"), f.Room(t, f.Client1, "t1-os-b")
	siteOf := func(room string) string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, room).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	siteA, siteB := siteOf(roomA), siteOf(roomB)

	var mu sync.Mutex
	docs := map[string]search.Document{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/_doc/"):
			var d search.Document
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &d)
			docs[r.URL.Path] = d
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/_search"):
			hits := []map[string]any{}
			for _, d := range docs {
				hits = append(hits, map[string]any{"_score": 1, "_source": d})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": len(hits)}, "hits": hits}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	hybrid := &search.HybridBackend{
		Remote: search.NewOpenSearchBackend(search.OpenSearchConfig{URL: srv.URL, Index: "t1-idx"}, srv.Client()),
		Source: search.NewPGRepository(f.App),
	}
	cis := ci.NewIndexingRepository(ci.NewPGRepository(f.App), search.NewCIIndexer(hybrid))

	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeID, LocationID: roomA, Name: "t1os-moved-ci", Status: "active"}
	if err := cis.Create(f.OrgCtx(f.OrgA), item); err != nil {
		t.Fatalf("create CI: %v", err)
	}

	hits := func(site string, backend search.Backend) bool {
		t.Helper()
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(site)
		res, err := backend.Query(database.ContextWithTenantScope(bg, &scope), search.Query{OrganizationID: f.OrgA, Text: "t1os"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, h := range res.Data {
			if h.EntityID == item.ID {
				return true
			}
		}
		return false
	}
	// Control before the move: OpenSearch serves the CI to site A, not to
	// site B, so the fake index and the scope post-filter work.
	if !hits(siteA, hybrid) || hits(siteB, hybrid) {
		t.Fatalf("control before the move: site A hit=%v site B hit=%v, want true/false", hits(siteA, hybrid), hits(siteB, hybrid))
	}

	// The product path of POST /api/v1/racks/{id}/mounts: the CI moves into
	// a rack of site B.
	racks := rack.NewPGRepository(f.App)
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: roomB, Name: "t1os-rack", HeightUnits: 42}
	if err := racks.CreateRack(f.OrgCtx(f.OrgA), rk); err != nil {
		t.Fatalf("create rack: %v", err)
	}
	if err := racks.CreateMount(f.OrgCtx(f.OrgA), &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: item.ID, PositionU: 1, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("mount CI: %v", err)
	}
	var dbSite string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM ci WHERE id = $1`, item.ID).Scan(&dbSite); err != nil || dbSite != siteB {
		t.Fatalf("setup: ci.site_id after the mount = %q (%v), want site B", dbSite, err)
	}

	mu.Lock()
	for path, d := range docs {
		if strings.HasSuffix(path, item.ID) && d.SiteID != siteB {
			t.Errorf("remote document of the moved CI keeps site %s, want site B %s", d.SiteID, siteB)
		}
	}
	mu.Unlock()

	if hits(siteA, hybrid) {
		t.Errorf("CH25/TEN-05: a site-A principal still finds the CI that moved to site B through OpenSearch")
	}
	if !hits(siteB, hybrid) {
		t.Errorf("a site-B principal does not find the CI that moved into site B through OpenSearch")
	}
}
