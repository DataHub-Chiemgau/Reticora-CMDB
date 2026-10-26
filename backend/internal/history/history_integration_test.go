package history_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestUnifiedCIHistory covers CI-06 and AUD-03 (WP-171) against
// PostgreSQL: the history of a CI unites its ci_change rows (with the site
// at the time), its entity_change rows and its audit entries in one trail,
// newest first, paginated over all three; without the audit entries
// (no audit:read, E-78) the rest of the trail stays; another organization
// sees none of it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUnifiedCIHistory(t *testing.T) {
	f := scopetest.Seed(t, "b1")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	site, err := locations.NewPGRepository(f.App).Create(ctx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "history-site"})
	if err != nil {
		t.Fatal(err)
	}
	siteID := site.ID
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, LocationID: siteID, CITypeID: typeID, Name: "history-unified", Status: "active"}
	if err = cis.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	renamed := "history-unified-2"
	if _, err = cis.Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	repo := history.NewPGRepository(f.App)
	if err = repo.Record(ctx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: item.ID, ChangeType: "lifecycle_transition", Comment: "commissioned"}); err != nil {
		t.Fatal(err)
	}

	all, total, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 100}, true)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]int{}
	for i, c := range all {
		sources[c.Source]++
		if c.Source == history.SourceCIChange && c.SiteID != siteID {
			t.Errorf("ci_change %s without the site at the time: %q", c.ChangeType, c.SiteID)
		}
		if i > 0 && c.CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("not newest first at %d", i)
		}
	}
	if total != len(all) || sources[history.SourceCIChange] < 2 || sources[history.SourceAudit] < 2 || sources[history.SourceEntityChange] != 1 {
		t.Fatalf("total %d, sources %v", total, sources)
	}
	page, pageTotal, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 2, Offset: 1}, true)
	if err != nil || pageTotal != total || len(page) != 2 || page[0].ID != all[1].ID {
		t.Errorf("second page: %d/%d %v", len(page), pageTotal, err)
	}
	other, otherTotal, err := repo.List(f.OrgCtx(f.OrgB), f.OrgB, "ci", item.ID, api.PaginationParams{Limit: 100}, true)
	if !errors.Is(err, api.ErrNotFound) || otherTotal != 0 || len(other) != 0 {
		t.Errorf("another organization reads %d entries (%v), want api.ErrNotFound", otherTotal, err)
	}
	changes, changesTotal, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 100}, false)
	if err != nil || changesTotal != total-sources[history.SourceAudit] || len(changes) != changesTotal {
		t.Fatalf("history without audit entries: %d/%d entries (%v), want %d", len(changes), changesTotal, err, total-sources[history.SourceAudit])
	}
	for _, c := range changes {
		if c.Source == history.SourceAudit {
			t.Errorf("history without audit:read lists the audit entry %s", c.ChangeType)
		}
	}
}

// locationTree creates and moves location nodes of one client through the
// location repository with the given request context.
type locationTree struct {
	t        *testing.T
	ctx      context.Context
	org      string
	clientID string
	repo     *locations.PGRepository
}

func (l locationTree) mk(kind locations.Kind, parent, name string) string {
	l.t.Helper()
	loc, err := l.repo.Create(l.ctx, l.org, locations.CreateRequest{Kind: kind, ParentID: parent, ClientID: l.clientID, Name: name})
	if err != nil {
		l.t.Fatalf("create %s: %v", name, err)
	}
	return loc.ID
}

func (l locationTree) move(id, parent string) {
	l.t.Helper()
	if _, err := l.repo.Move(l.ctx, l.org, id, parent); err != nil {
		l.t.Fatalf("move %s: %v", id, err)
	}
}

// locationMoves returns the location_change entries of a history keyed by
// the moved location.
func locationMoves(entries []history.Change) map[string][]history.Change {
	out := map[string][]history.Change{}
	for _, c := range entries {
		if c.Source != history.SourceLocationChange {
			continue
		}
		v, _ := c.NewValue.(map[string]any)
		id, _ := v["location_id"].(string)
		out[id] = append(out[id], c)
	}
	return out
}

// checkMove asserts one location_change entry: time, actor, site, the moved
// location with its name and the old and new parent and path.
func checkMove(t *testing.T, c history.Change, actor, location, name, oldParent, newParent string) {
	t.Helper()
	oldV, _ := c.OldValue.(map[string]any)
	newV, _ := c.NewValue.(map[string]any)
	if c.ChangeType != "location_change" || c.CreatedAt.IsZero() || c.ActorID != actor || c.SiteID == "" {
		t.Errorf("move of %s: type %q, time %v, actor %q (want %q), site %q", name, c.ChangeType, c.CreatedAt, c.ActorID, actor, c.SiteID)
	}
	if oldV["location_id"] != location || newV["location_id"] != location || oldV["location_name"] != name || newV["location_name"] != name ||
		oldV["parent_id"] != oldParent || newV["parent_id"] != newParent {
		t.Errorf("move of %s: old %v, new %v; want location %s, parent %s -> %s", name, oldV, newV, location, oldParent, newParent)
	}
	oldPath, _ := oldV["path"].(string)
	newPath, _ := newV["path"].(string)
	if oldPath == "" || newPath == "" || oldPath == newPath {
		t.Errorf("move of %s: path %q -> %q", name, oldPath, newPath)
	}
}

// TestAuditT3HistoryIncludesLocationChange covers CI-06, LOC-10 and LOC-11
// (WP-171, WP-473; F-093): a location_change of the CI's location and of
// each of its ancestors (ltree path) appears in the unified CI history with
// time, actor, old and new parent and path, newest first among the other
// sources; the move of a location outside that path does not. The history
// of a location (type location) shows its own move and those of its
// ancestors.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditT3HistoryIncludesLocationChange(t *testing.T) {
	f := scopetest.Seed(t, "d4")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	tree := locationTree{t: t, ctx: ctx, org: f.OrgA, clientID: f.Client1, repo: locations.NewPGRepository(f.App)}
	site := tree.mk(locations.KindSite, "", "audit-t3-hist-site")
	b1 := tree.mk(locations.KindBuilding, site, "audit-t3-hist-b1")
	b2 := tree.mk(locations.KindBuilding, site, "audit-t3-hist-b2")
	room := tree.mk(locations.KindRoom, b1, "audit-t3-hist-room")
	room2 := tree.mk(locations.KindRoom, b1, "audit-t3-hist-room2")
	other := tree.mk(locations.KindRoom, b1, "audit-t3-hist-other")
	rack := tree.mk(locations.KindRack, room, "audit-t3-hist-rack")
	ciID := f.CI(t, f.OrgA, f.Client1, "audit-t3-hist-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ciID, rack); err != nil {
		t.Fatal(err)
	}
	repo := history.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 100}
	_, ciBefore, err := repo.List(ctx, f.OrgA, "ci", ciID, page, true)
	if err != nil {
		t.Fatal(err)
	}
	_, roomBefore, err := repo.List(ctx, f.OrgA, "location", room2, page, true)
	if err != nil {
		t.Fatal(err)
	}

	tree.move(rack, room2) // the CI's own location
	tree.move(room2, b2)   // an ancestor of it
	tree.move(other, b2)   // outside the CI's path
	var rows int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM location_change WHERE location_id IN ($1, $2, $3)`, rack, room2, other).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("precondition: location_change rows = %d (%v), want 3", rows, err)
	}

	roomHist, roomAfter, err := repo.List(ctx, f.OrgA, "location", room2, page, true)
	if err != nil {
		t.Fatal(err)
	}
	if roomAfter <= roomBefore {
		t.Errorf("history of the moved room: %d entries before, %d after the move; the location_change row is missing", roomBefore, roomAfter)
	}
	if moves := locationMoves(roomHist); len(moves[room2]) != 1 || len(moves) != 1 {
		t.Errorf("room history: moves %v, want the room's own move only", moves)
	} else {
		checkMove(t, moves[room2][0], f.User, room2, "audit-t3-hist-room2", b1, b2)
	}
	rackHist, _, err := repo.List(ctx, f.OrgA, "location", rack, page, true)
	if err != nil {
		t.Fatal(err)
	}
	if moves := locationMoves(rackHist); len(moves[rack]) != 1 || len(moves[room2]) != 1 || len(moves) != 2 {
		t.Errorf("rack history: moves %v, want the rack's and the room's", moves)
	}

	ciHist, ciAfter, err := repo.List(ctx, f.OrgA, "ci", ciID, page, true)
	if err != nil {
		t.Fatal(err)
	}
	if ciAfter <= ciBefore || ciAfter != len(ciHist) {
		t.Errorf("unified history of the CI in the moved room: %d entries before, %d after the move; the location_change is missing", ciBefore, ciAfter)
	}
	moves := locationMoves(ciHist)
	if len(moves[rack]) != 1 || len(moves[room2]) != 1 || len(moves[other]) != 0 {
		t.Fatalf("CI history: moves %v, want the rack's and the room's, not the other room's", moves)
	}
	checkMove(t, moves[rack][0], f.User, rack, "audit-t3-hist-rack", room, room2)
	checkMove(t, moves[room2][0], f.User, room2, "audit-t3-hist-room2", b1, b2)
	if c := moves[room2][0]; c.EntityType != "ci" || c.EntityID != ciID {
		t.Errorf("CI history entry of the room move: entity %s/%s", c.EntityType, c.EntityID)
	}
	rackAt, roomAt := -1, -1
	for i, c := range ciHist {
		if i > 0 && c.CreatedAt.After(ciHist[i-1].CreatedAt) {
			t.Errorf("CI history not newest first at %d", i)
		}
		switch c.ID {
		case moves[rack][0].ID:
			rackAt = i
		case moves[room2][0].ID:
			roomAt = i
		}
	}
	if roomAt > rackAt {
		t.Errorf("CI history: the later room move at %d, the rack move at %d", roomAt, rackAt)
	}
}

// getHistory serves GET path through the history handler with ctx, as a
// principal with every permission unless ctx carries one.
func getHistory(ctx context.Context, t *testing.T, h *history.Handler, path string) (*httptest.ResponseRecorder, history.ListResponse) {
	t.Helper()
	if _, ok := identity.PrincipalFromContext(ctx); !ok {
		ctx = identity.WithPrincipal(ctx, identity.Principal{OrganizationID: tenant.FromContext(ctx).OrganizationID, Permissions: identity.AllPermissions()})
	}
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
	var body history.ListResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
	}
	return rec, body
}

// TestLocationHistoryAfterDeleteAndRetiredType covers LOC-11 and F-226
// (WP-473): GET /api/v1/history/location/{id} answers 200 with the move of
// the location, also after the location was deleted (location_change keeps
// location_id and location_name, WP-411); an unknown location is 404; the
// retired type location_node is 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationHistoryAfterDeleteAndRetiredType(t *testing.T) {
	f := scopetest.Seed(t, "d5")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	tree := locationTree{t: t, ctx: ctx, org: f.OrgA, clientID: f.Client1, repo: locations.NewPGRepository(f.App)}
	site := tree.mk(locations.KindSite, "", "hist-del-site")
	b1 := tree.mk(locations.KindBuilding, site, "hist-del-b1")
	b2 := tree.mk(locations.KindBuilding, site, "hist-del-b2")
	room := tree.mk(locations.KindRoom, b1, "hist-del-room")
	tree.move(room, b2)
	h := history.NewHandler(history.NewPGRepository(f.App))

	rec, live := getHistory(ctx, t, h, "/api/v1/history/location/"+room)
	if rec.Code != http.StatusOK || live.Total != 1 || len(live.Data) != 1 {
		t.Fatalf("history of the moved room: %d %s", rec.Code, rec.Body)
	}
	checkMove(t, live.Data[0], f.User, room, "hist-del-room", b1, b2)

	if err := tree.repo.Delete(ctx, f.OrgA, room); err != nil {
		t.Fatalf("delete room: %v", err)
	}
	rec, gone := getHistory(ctx, t, h, "/api/v1/history/location/"+room)
	if rec.Code != http.StatusOK || gone.Total != 1 || len(gone.Data) != 1 {
		t.Fatalf("history of the deleted room: %d %s", rec.Code, rec.Body)
	}
	checkMove(t, gone.Data[0], f.User, room, "hist-del-room", b1, b2)
	if gone.Data[0].ID != live.Data[0].ID {
		t.Errorf("history entry changed with the deletion: %s -> %s", live.Data[0].ID, gone.Data[0].ID)
	}

	if rec, _ = getHistory(ctx, t, h, "/api/v1/history/location/"+f.ID()); rec.Code != http.StatusNotFound {
		t.Errorf("unknown location: %d %s, want 404", rec.Code, rec.Body)
	}
	if rec, _ = getHistory(ctx, t, h, "/api/v1/history/location_node/"+b2); rec.Code != http.StatusBadRequest {
		t.Errorf("retired type location_node: %d %s, want 400", rec.Code, rec.Body)
	}
	if rec, _ = getHistory(ctx, t, h, "/api/v1/history/location/"+b2); rec.Code != http.StatusOK {
		t.Errorf("building without a move of its own: %d %s, want 200", rec.Code, rec.Body)
	}
}
