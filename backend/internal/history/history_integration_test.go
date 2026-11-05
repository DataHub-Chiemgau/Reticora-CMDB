package history_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
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

	all, total, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 100}, history.ListOptions{IncludeAudit: true})
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
	page, pageTotal, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 2, Offset: 1}, history.ListOptions{IncludeAudit: true})
	if err != nil || pageTotal != total || len(page) != 2 || page[0].ID != all[1].ID {
		t.Errorf("second page: %d/%d %v", len(page), pageTotal, err)
	}
	other, otherTotal, err := repo.List(f.OrgCtx(f.OrgB), f.OrgB, "ci", item.ID, api.PaginationParams{Limit: 100}, history.ListOptions{IncludeAudit: true})
	if !errors.Is(err, api.ErrNotFound) || otherTotal != 0 || len(other) != 0 {
		t.Errorf("another organization reads %d entries (%v), want api.ErrNotFound", otherTotal, err)
	}
	changes, changesTotal, err := repo.List(ctx, f.OrgA, "ci", item.ID, api.PaginationParams{Limit: 100}, history.ListOptions{})
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
	_, ciBefore, err := repo.List(ctx, f.OrgA, "ci", ciID, page, history.ListOptions{IncludeAudit: true})
	if err != nil {
		t.Fatal(err)
	}
	_, roomBefore, err := repo.List(ctx, f.OrgA, "location", room2, page, history.ListOptions{IncludeAudit: true})
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

	roomHist, roomAfter, err := repo.List(ctx, f.OrgA, "location", room2, page, history.ListOptions{IncludeAudit: true})
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
	rackHist, _, err := repo.List(ctx, f.OrgA, "location", rack, page, history.ListOptions{IncludeAudit: true})
	if err != nil {
		t.Fatal(err)
	}
	if moves := locationMoves(rackHist); len(moves[rack]) != 1 || len(moves[room2]) != 1 || len(moves) != 2 {
		t.Errorf("rack history: moves %v, want the rack's and the room's", moves)
	}

	ciHist, ciAfter, err := repo.List(ctx, f.OrgA, "ci", ciID, page, history.ListOptions{IncludeAudit: true})
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

// ciChangeEntries returns the ci_change entries of a history.
func ciChangeEntries(entries []history.Change) []history.Change {
	var out []history.Change
	for _, c := range entries {
		if c.Source == history.SourceCIChange {
			out = append(out, c)
		}
	}
	return out
}

// TestHistoryChangeSetsAUD03 covers AUD-03 and CI-11 (WP-488, E-82 option
// 2) against PostgreSQL as reticora_app: GET /api/v1/history/ci/{id} gives
// one entry per change set with change_kind and field_changes [{field, old,
// new}], the source, request id, correlation id and user of the request and
// the collector and observation time of a discovery change; a PATCH of three
// fields is exactly one entry with three field changes. Rows without change
// set (before 000167) stay entries of their own. change_kind=type_changed
// finds the type change and nothing else; an unknown kind, and a kind for
// another entity type, is 400 naming the field.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHistoryChangeSetsAUD03(t *testing.T) {
	f := scopetest.Seed(t, "e8")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: identity.AllPermissions()})
	// The request chain of the server: request and correlation id, then the
	// request context of the audit (source, AUD-07).
	mux := chi.NewRouter()
	// The field policy source of the server router (WP-265).
	mux.Use(fieldPolicySource())
	mux.Use(middleware.RequestID, middleware.AuditRoute(http.MethodPatch, "/api/v1/cis/{id}", nil, middleware.AuditNone, ""))
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	history.NewHandler(history.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(method, path, body string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	list := func(path string) history.ListResponse {
		t.Helper()
		w := call(http.MethodGet, path, "")
		var body history.ListResponse
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return body
	}

	id := f.CI(t, f.OrgA, f.Client1, "h8-aud03")
	// Two rows of one write before 000167: no change set, entries of their own.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, old_value, new_value, created_at)
		VALUES ($1, $2, 'status_change', 'status', '"planned"', '"active"', now() - interval '2 hours'),
		       ($1, $2, 'update', 'description', '"a"', '"b"', now() - interval '2 hours')`, f.OrgA, id); err != nil {
		t.Fatal(err)
	}
	// A discovery change set of two fields with collector and observation time.
	collector, observed := f.ID(), time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, old_value, new_value, created_at,
			change_kind, change_set_id, source, collector_id, observed_at, correlation_id)
		SELECT $1, $2, 'update', f, '"x"', to_jsonb(f || '-new'), now() - interval '1 hour', 'updated', $3, 'collector', $4, $5, 'record-1'
		FROM unnest(ARRAY['serial_number', 'os_version']) AS f`, f.OrgA, id, f.ID(), collector, observed); err != nil {
		t.Fatal(err)
	}

	// PATCH of three fields: exactly one entry with three field changes.
	w := call(http.MethodPatch, "/api/v1/cis/"+id, `{"name":"h8-aud03-b","model":"M-488","os_name":"Linux"}`,
		"X-Correlation-ID", "corr-wp488")
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	requestID := w.Header().Get("X-Request-ID")
	all := list("/api/v1/history/ci/" + id)
	entries := ciChangeEntries(all.Data)
	if all.Total != len(all.Data) || len(entries) != 4 {
		t.Fatalf("history: total %d, %d entries, %d ci_change entries, want 4 (PATCH, discovery, two rows without change set): %+v",
			all.Total, len(all.Data), len(entries), entries)
	}
	patch := entries[0]
	want := []history.FieldChange{
		{Field: "model", New: "M-488"},
		{Field: "name", Old: "h8-aud03", New: "h8-aud03-b"},
		{Field: "os_name", New: "Linux"},
	}
	if len(patch.FieldChanges) != 3 {
		t.Fatalf("PATCH of three fields: %d field changes, want 3: %+v", len(patch.FieldChanges), patch)
	}
	for i, fc := range patch.FieldChanges {
		if fc.Field != want[i].Field || fc.New != want[i].New || (want[i].Old != nil && fc.Old != want[i].Old) {
			t.Errorf("field change %d: %+v, want %+v", i, fc, want[i])
		}
	}
	if patch.ChangeKind != "updated" || patch.ChangeSetID == "" || patch.ChangeType != "update" || patch.ChangeSource != audit.SourceUI ||
		requestID == "" || patch.RequestID != requestID || patch.CorrelationID != "corr-wp488" || patch.ActorUserID != f.User ||
		patch.ActorID != f.User || patch.ID == "" || patch.EntityType != "ci" || patch.EntityID != id || patch.CreatedAt.IsZero() ||
		patch.OrganizationID != f.OrgA {
		t.Errorf("PATCH entry: %+v, want kind updated, a change set, source ui, request_id %q, correlation corr-wp488, user %s",
			patch, requestID, f.User)
	}
	if patch.FieldName != "" || patch.OldValue != nil || patch.NewValue != nil {
		t.Errorf("PATCH entry of several fields keeps field_name %q, old %v, new %v", patch.FieldName, patch.OldValue, patch.NewValue)
	}
	discovery := entries[1]
	if discovery.ChangeKind != "updated" || discovery.ChangeSource != audit.SourceCollector || discovery.CollectorID != collector ||
		discovery.ObservedAt == nil || !discovery.ObservedAt.Equal(observed) || discovery.CorrelationID != "record-1" ||
		len(discovery.FieldChanges) != 2 || discovery.FieldChanges[0].Field != "os_version" || discovery.FieldChanges[1].Field != "serial_number" ||
		discovery.FieldChanges[1].Old != "x" || discovery.FieldChanges[1].New != "serial_number-new" {
		t.Errorf("discovery entry: %+v, want collector %s, observed_at %v and two field changes", discovery, collector, observed)
	}
	for _, legacy := range entries[2:] {
		if legacy.ChangeSetID != "" || len(legacy.FieldChanges) != 1 || legacy.FieldName == "" ||
			legacy.FieldChanges[0].Field != legacy.FieldName || legacy.FieldChanges[0].Old != legacy.OldValue ||
			legacy.FieldChanges[0].New != legacy.NewValue || legacy.ChangeKind == "" {
			t.Errorf("row without change set: %+v, want an entry of its own with its field and kind", legacy)
		}
	}

	// Filter: type_changed finds the type change and nothing else.
	var router string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'router' AND organization_id IS NULL`).Scan(&router); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+id+"/type", `{"ci_type_id":"`+router+`"}`); w.Code != http.StatusOK {
		t.Fatalf("type change: %d %s", w.Code, w.Body)
	}
	typed := list("/api/v1/history/ci/" + id + "?change_kind=type_changed")
	newType := func(c history.Change) any {
		v, _ := c.FieldChanges[0].New.(map[string]any)
		return v["ci_type_id"]
	}
	if typed.Total != 1 || len(typed.Data) != 1 || typed.Data[0].ChangeKind != "type_changed" || typed.Data[0].ChangeType != "type_changed" ||
		typed.Data[0].FieldName != "ci_type_id" || len(typed.Data[0].FieldChanges) != 1 || newType(typed.Data[0]) != router {
		t.Errorf("change_kind=type_changed: total %d, %+v, want the type change only", typed.Total, typed.Data)
	}
	updated := list("/api/v1/history/ci/" + id + "?change_kind=updated&limit=2")
	if updated.Total != 3 || len(updated.Data) != 2 || updated.Data[0].ChangeSetID != patch.ChangeSetID || !updated.HasMore {
		t.Errorf("change_kind=updated: total %d, %+v, want 3 entries (PATCH, discovery, the description row) paged by 2, the PATCH first",
			updated.Total, updated.Data)
	}
	for _, c := range updated.Data {
		if c.ChangeKind != "updated" || c.Source != history.SourceCIChange {
			t.Errorf("change_kind=updated lists %+v", c)
		}
	}

	// An unknown kind, and a kind for another entity type, is 400 with the field.
	for _, path := range []string{
		"/api/v1/history/ci/" + id + "?change_kind=type_change",
		"/api/v1/history/ci/" + id + "?change_kind=",
		"/api/v1/history/location/" + f.ID() + "?change_kind=updated",
	} {
		w = call(http.MethodGet, path, "")
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusBadRequest || problem.Field != "change_kind" {
			t.Errorf("GET %s: %d %s, want 400 naming change_kind", path, w.Code, w.Body)
		}
	}
}

// phase1Resolver resolves every principal to the policy without field
// permissions (an organization without grants).
type phase1Resolver struct{}

func (phase1Resolver) Resolve(_ context.Context, p identity.Principal) (*permission.FieldPolicy, error) { //nolint:gocritic // interface of permission.PolicyResolver
	return permission.UnrestrictedPolicy(p.Permissions), nil
}

// fieldPolicySource is the policy source server.NewRouter puts behind the
// route permission (permission.FieldPolicyMiddleware): a router of the test
// that mounts the CI handler needs it, since a principal without policy
// source is answered 500 (fail-closed, WP-265).
func fieldPolicySource() func(http.Handler) http.Handler {
	return permission.FieldPolicyMiddleware(phase1Resolver{})
}

// TestCITimeline covers GQL-03 and CI-06 (WP-792) against PostgreSQL: the
// timeline of a CI (history.CITimeline, the source of the BFF field
// ciHistory) holds its unified history and the relationship_change entries
// of its edges, also of a deleted edge, at the source and at the target
// CI; the window bounds the time of recording; the relationship entries of
// an edge to a CI outside the client or site scope are invisible, as is
// the timeline of a CI outside it; List stays without relationship entries.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCITimeline(t *testing.T) {
	f := scopetest.Seed(t, "b1")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	locs := locations.NewPGRepository(f.App)
	site := func(name string) string {
		t.Helper()
		loc, err := locs.Create(ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return loc.ID
	}
	site1, site2 := site("timeline-site-1"), site("timeline-site-2")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mk := func(name, clientID, locationID string) string {
		t.Helper()
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: clientID, LocationID: locationID, CITypeID: typeID, Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return item.ID
	}
	src, dst := mk("timeline-src", f.Client1, site1), mk("timeline-dst", f.Client1, site1)
	foreign, otherSite := mk("timeline-client-2", f.Client2, ""), mk("timeline-site-2", f.Client1, site2)
	rels := relationship.NewPGRepository(f.App)
	edge := func(target string) string {
		t.Helper()
		rel := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: src, TargetCIID: target, RelType: "depends_on"}
		if err := rels.Create(ctx, rel); err != nil {
			t.Fatalf("create edge to %s: %v", target, err)
		}
		return rel.ID
	}
	removed := edge(dst)
	if err := rels.Delete(ctx, f.OrgA, removed); err != nil {
		t.Fatal(err)
	}
	toForeign, toOtherSite := edge(foreign), edge(otherSite)
	// An entity change of ten days ago, before the window.
	if _, err := f.Admin.Exec(bg, `INSERT INTO entity_change (organization_id, entity_type, entity_id, change_type, comment, created_at)
		VALUES ($1, 'ci', $2, 'lifecycle_transition', 'timeline-old', now() - interval '10 days')`, f.OrgA, src); err != nil {
		t.Fatal(err)
	}

	repo := history.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 100}
	all := history.TimelineOptions{IncludeAudit: true, IncludeRelationships: true}
	timeline := func(ctx context.Context, id string, opts history.TimelineOptions) []history.Change {
		t.Helper()
		entries, total, err := repo.CITimeline(ctx, f.OrgA, id, page, opts)
		if err != nil {
			t.Fatalf("timeline of %s: %v", id, err)
		}
		if total != len(entries) {
			t.Fatalf("timeline of %s: total %d, %d entries", id, total, len(entries))
		}
		for i := 1; i < len(entries); i++ {
			if entries[i].CreatedAt.After(entries[i-1].CreatedAt) {
				t.Errorf("timeline of %s not newest first at %d", id, i)
			}
		}
		return entries
	}
	// edgeKinds are the change kinds of the relationship entries per edge.
	edgeKinds := func(entries []history.Change) map[string][]string {
		out := map[string][]string{}
		for _, e := range entries {
			if e.Source != history.SourceRelationshipChange {
				continue
			}
			if e.EntityType != "relationship" || e.ObservedAt == nil || !e.ObservedAt.Equal(e.CreatedAt) {
				t.Errorf("relationship entry %s: %+v", e.ID, e)
			}
			out[e.EntityID] = append(out[e.EntityID], e.ChangeType)
		}
		for id := range out {
			slices.Sort(out[id])
		}
		return out
	}
	sources := func(entries []history.Change) map[string]int {
		out := map[string]int{}
		for _, e := range entries {
			out[e.Source]++
		}
		return out
	}

	window := all
	window.From = time.Now().Add(-24 * time.Hour)
	entries := timeline(ctx, src, window)
	kinds := edgeKinds(entries)
	want := map[string][]string{removed: {relationship.ChangeCreated, relationship.ChangeRemoved},
		toForeign: {relationship.ChangeCreated}, toOtherSite: {relationship.ChangeCreated}}
	if !maps.EqualFunc(kinds, want, slices.Equal[[]string]) {
		t.Errorf("relationship entries of the source: %v, want %v", kinds, want)
	}
	for _, e := range entries {
		if e.EntityID == removed && e.ChangeType == relationship.ChangeRemoved && e.OldValue == nil {
			t.Errorf("the removal carries no edge before it: %+v", e)
		}
	}
	got := sources(entries)
	if got[history.SourceCIChange] < 1 || got[history.SourceAudit] < 1 || got[history.SourceEntityChange] != 0 {
		t.Errorf("sources in the window: %v", got)
	}
	whole := timeline(ctx, src, all)
	if len(whole) != len(entries)+1 || sources(whole)[history.SourceEntityChange] != 1 {
		t.Errorf("without a window: %d entries, %v", len(whole), sources(whole))
	}
	before := all
	before.To = time.Now().Add(-5 * 24 * time.Hour)
	if old := timeline(ctx, src, before); len(old) != 1 || old[0].Comment != "timeline-old" {
		t.Errorf("before five days: %+v", old)
	}
	if kinds = edgeKinds(timeline(ctx, dst, all)); !maps.EqualFunc(kinds, map[string][]string{removed: want[removed]}, slices.Equal[[]string]) {
		t.Errorf("relationship entries of the target: %v", kinds)
	}
	if got = sources(timeline(ctx, src, history.TimelineOptions{})); got[history.SourceRelationshipChange] != 0 || got[history.SourceAudit] != 0 {
		t.Errorf("without audit and relationship entries: %v", got)
	}

	// Pages of two over the whole timeline.
	first, total, err := repo.CITimeline(ctx, f.OrgA, src, api.PaginationParams{Limit: 2}, all)
	if err != nil || total != len(whole) || len(first) != 2 || first[0].ID != whole[0].ID {
		t.Errorf("first page: %d/%d %v", len(first), total, err)
	}
	second, _, err := repo.CITimeline(ctx, f.OrgA, src, api.PaginationParams{Limit: 2, Offset: 2}, all)
	if err != nil || len(second) != 2 || second[0].ID != whole[2].ID {
		t.Errorf("second page: %d %v", len(second), err)
	}

	// The scope: an edge to a CI outside it is invisible, a CI outside it
	// has no timeline.
	siteCtx := func(siteID string) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(siteID)
		return database.ContextWithTenantScope(bg, &scope)
	}
	if kinds = edgeKinds(timeline(f.ClientCtx(f.Client1), src, all)); len(kinds) != 2 || kinds[toForeign] != nil {
		t.Errorf("client 1 sees the edges %v, want the ones without client 2", kinds)
	}
	if kinds = edgeKinds(timeline(siteCtx(site1), src, all)); len(kinds) != 2 || kinds[toOtherSite] != nil {
		t.Errorf("site 1 sees the edges %v, want the ones without site 2", kinds)
	}
	for name, scoped := range map[string]context.Context{"client 2": f.ClientCtx(f.Client2), "site 2": siteCtx(site2),
		"organization B": f.OrgCtx(f.OrgB)} {
		orgID := f.OrgA
		if name == "organization B" {
			orgID = f.OrgB
		}
		if out, n, terr := repo.CITimeline(scoped, orgID, src, page, all); !errors.Is(terr, api.ErrNotFound) || n != 0 || len(out) != 0 {
			t.Errorf("%s reads the timeline: %d entries (%v), want api.ErrNotFound", name, n, terr)
		}
	}

	// List is unchanged: ci_change, entity_change, location_change and
	// audit, no relationship entries.
	listed, listedTotal, err := repo.List(ctx, f.OrgA, "ci", src, page, history.ListOptions{IncludeAudit: true})
	if err != nil {
		t.Fatal(err)
	}
	withoutEdges := all
	withoutEdges.IncludeRelationships = false
	if n := len(timeline(ctx, src, withoutEdges)); listedTotal != n || len(listed) != n {
		t.Errorf("List: %d/%d entries, the timeline without edges %d", len(listed), listedTotal, n)
	}
	for _, e := range listed {
		switch e.Source {
		case history.SourceCIChange, history.SourceEntityChange, history.SourceLocationChange, history.SourceAudit:
		default:
			t.Errorf("List holds an entry of %s", e.Source)
		}
	}
}
