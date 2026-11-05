package document_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// linkWorld seeds the targets of the link tests in organization A: two
// sites of client 1 and one of client 2 (each with a room), assets at
// those rooms with their disposal records, an assignment movement of a
// client-2 asset (the table assignment is gone, WP-843), two teams with
// one ticket each and an org-wide document author.
type linkWorld struct {
	f                        *scopetest.Fixture
	repo                     *document.PGRepository
	user                     string
	site1, site1b, site2     string
	room1, room1b, room2     string
	asset1, asset2           string
	disposal1, disposal2     string
	movement2                string
	teamA, teamB             string
	ticketA, ticketA2        string
	ticketB                  string
	orgCtx, client1Ctx       context.Context
	site1Ctx, teamACtx       context.Context
	teamAScope, client1Scope database.TenantScope
}

func newLinkWorld(t *testing.T) *linkWorld {
	t.Helper()
	f := scopetest.Seed(t, "34")
	w := &linkWorld{f: f, repo: document.NewPGRepository(f.App), user: f.AppUser(t, f.OrgA, "link-author")}
	siteOf := func(room string) string { return w.scalar(t, `SELECT site_id::text FROM location WHERE id = $1`, room) }
	w.room1, w.room1b, w.room2 = f.Room(t, f.Client1, "lk-room1"), f.Room(t, f.Client1, "lk-room1b"), f.Room(t, f.Client2, "lk-room2")
	w.site1, w.site1b, w.site2 = siteOf(w.room1), siteOf(w.room1b), siteOf(w.room2)
	asset := func(client, room, tag string) string {
		return w.scalar(t, `INSERT INTO asset (organization_id, client_id, location_id, asset_tag, name) VALUES ($1, $2, $3, $4, $4) RETURNING id::text`,
			f.OrgA, client, room, tag)
	}
	w.asset1, w.asset2 = asset(f.Client1, w.room1, "lk-asset1"), asset(f.Client2, w.room2, "lk-asset2")
	disposal := func(asset string) string {
		return w.scalar(t, `INSERT INTO disposal_record (organization_id, asset_id, method, performed_at) VALUES ($1, $2, 'recycling', now()) RETURNING id::text`,
			f.OrgA, asset)
	}
	w.disposal1, w.disposal2 = disposal(w.asset1), disposal(w.asset2)
	w.movement2 = w.scalar(t, `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, assignee_user_id)
		VALUES ($1, 'asset', $3, 'assignment', $2) RETURNING id::text`, f.OrgA, w.user, w.asset2)
	team := func(name string) string {
		return w.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, $2) RETURNING id::text`, f.OrgA, name)
	}
	w.teamA, w.teamB = team("lk-team-a"), team("lk-team-b")
	ticket := func(title, team string) string {
		return w.scalar(t, `INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, title, w.user, team)
	}
	w.ticketA, w.ticketA2, w.ticketB = ticket("lk-ticket-a", w.teamA), ticket("lk-ticket-a2", w.teamA), ticket("lk-ticket-b", w.teamB)

	w.orgCtx = f.OrgCtx(f.OrgA)
	w.client1Scope = database.OrgWideScope(f.OrgA, w.user)
	w.client1Scope.Clients = database.ScopeIDs(f.Client1)
	w.client1Ctx = database.ContextWithTenantScope(context.Background(), &w.client1Scope)
	site1 := database.OrgWideScope(f.OrgA, w.user)
	site1.Sites = database.ScopeIDs(w.site1)
	w.site1Ctx = database.ContextWithTenantScope(context.Background(), &site1)
	w.teamAScope = database.OrgWideScope(f.OrgA, w.user)
	w.teamAScope.Teams = database.ScopeIDs(w.teamA)
	w.teamACtx = database.ContextWithTenantScope(context.Background(), &w.teamAScope)
	return w
}

func (w *linkWorld) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

// doc creates an org-wide document linked to the given targets
// (entity type, id pairs) by the org-wide principal.
func (w *linkWorld) doc(t *testing.T, title string, targets ...string) string {
	t.Helper()
	d := &document.Document{OrganizationID: w.f.OrgA, Title: title, FileName: title + ".pdf", UploadedBy: w.user, Category: "general"}
	if err := w.repo.Create(w.orgCtx, d); err != nil {
		t.Fatalf("create %s: %v", title, err)
	}
	for i := 0; i+1 < len(targets); i += 2 {
		if _, err := w.link(w.orgCtx, d.ID, targets[i], targets[i+1]); err != nil {
			t.Fatalf("link %s to %s %s: %v", title, targets[i], targets[i+1], err)
		}
	}
	return d.ID
}

func (w *linkWorld) link(ctx context.Context, docID, entityType, entityID string) (string, error) {
	l := &document.DocumentLink{OrganizationID: w.f.OrgA, DocumentID: docID, EntityType: entityType, EntityID: entityID}
	err := w.repo.LinkDocument(ctx, l)
	return l.ID, err
}

// scope returns client, site and team of the link of docID to entityID.
func (w *linkWorld) scope(t *testing.T, docID, entityID string) string {
	t.Helper()
	return w.scalar(t, `SELECT COALESCE(client_id::text, '-') || '|' || COALESCE(site_id::text, '-') || '|' || COALESCE(team_id::text, '-')
		FROM document_link WHERE document_id = $1 AND entity_id = $2`, docID, entityID)
}

func (w *linkWorld) linkCount(t *testing.T, docID string) string {
	t.Helper()
	return w.scalar(t, `SELECT link_count::text || '/' || (SELECT count(*) FROM document_link WHERE document_id = $1)::text FROM document WHERE id = $1`, docID)
}

// TestDocumentLinkTargetsDeriveScope (WP-267, MGT-01): links to locations
// and disposal records take client and site of their target, a link to an
// asset also its site, a link to a ticket its team; moves of the target
// re-derive them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentLinkTargetsDeriveScope(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	d := w.doc(t, "lk-derive", "location", w.room1, "disposal_record", w.disposal1, "asset", w.asset1, "ticket", w.ticketB,
		"location", w.site1b)
	for _, c := range []struct{ name, entity, want string }{
		{"location", w.room1, f.Client1 + "|" + w.site1 + "|-"},
		{"disposal record", w.disposal1, f.Client1 + "|" + w.site1 + "|-"},
		{"asset", w.asset1, f.Client1 + "|" + w.site1 + "|-"},
		{"ticket", w.ticketB, "-|-|" + w.teamB},
		{"site location", w.site1b, f.Client1 + "|" + w.site1b + "|-"},
	} {
		if got := w.scope(t, d, c.entity); got != c.want {
			t.Errorf("link to the %s: client|site|team %s, want %s", c.name, got, c.want)
		}
	}

	bg := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// A site moving to client 2 takes its location rows and their links along.
	exec(`UPDATE site SET client_id = $2 WHERE id = $1`, w.site1b, f.Client2)
	if got := w.scope(t, d, w.site1b); got != f.Client2+"|"+w.site1b+"|-" {
		t.Errorf("link to the location after the client change: %s, want client 2", got)
	}
	exec(`UPDATE site SET client_id = $2 WHERE id = $1`, w.site1b, f.Client1)

	// The asset changes its site, then its client: the links to the asset
	// and to its disposal record follow.
	exec(`UPDATE asset SET location_id = $2 WHERE id = $1`, w.asset1, w.room1b)
	for name, entity := range map[string]string{"asset": w.asset1, "disposal record": w.disposal1} {
		if got := w.scope(t, d, entity); got != f.Client1+"|"+w.site1b+"|-" {
			t.Errorf("link to the %s after the site change: %s, want site %s", name, got, w.site1b)
		}
	}
	exec(`UPDATE asset SET client_id = $2 WHERE id = $1`, w.asset1, f.Client2)
	for name, entity := range map[string]string{"asset": w.asset1, "disposal record": w.disposal1} {
		if got := w.scope(t, d, entity); got != f.Client2+"|"+w.site1b+"|-" {
			t.Errorf("link to the %s after the client change: %s, want client 2", name, got)
		}
	}

	// A ticket moving to another team takes its links along.
	exec(`UPDATE ticket SET team_id = $2 WHERE id = $1`, w.ticketB, w.teamA)
	if got := w.scope(t, d, w.ticketB); got != "-|-|"+w.teamA {
		t.Errorf("link to the ticket after the team change: %s, want team A", got)
	}

	// Registry and doc_link_entity_type_check hold the same values; the
	// check admits stock_movement since WP-744, the registry entry follows
	// with WP-276.
	def := w.scalar(t, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'doc_link_entity_type_check'`)
	var inCheck []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(def, -1) {
		if m[1] != "stock_movement" || slices.Contains(document.EntityTypes(), "stock_movement") {
			inCheck = append(inCheck, m[1])
		}
	}
	slices.Sort(inCheck)
	if !slices.Equal(inCheck, document.EntityTypes()) {
		t.Errorf("doc_link_entity_type_check %v, registry %v", inCheck, document.EntityTypes())
	}
}

// TestDocumentLinkTargetsIsolation (WP-267, MGT-01): a principal restricted
// to client 1 neither links nor sees targets of client 2, a site-scoped
// principal no document of an asset at another site, a team-restricted
// principal no document hanging only at a ticket of another team.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentLinkTargetsIsolation(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	own := w.doc(t, "lk-own", "asset", w.asset1)
	foreign := map[string]string{
		"location":        w.doc(t, "lk-foreign-location", "location", w.room2),
		"disposal_record": w.doc(t, "lk-foreign-disposal", "disposal_record", w.disposal2),
		"stock_movement":  w.doc(t, "lk-foreign-movement", "stock_movement", w.movement2),
	}
	targets := map[string]string{"location": w.room2, "disposal_record": w.disposal2, "stock_movement": w.movement2}
	for entityType, target := range targets {
		if _, err := w.link(w.client1Ctx, own, entityType, target); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("client 1 links a %s of client 2: %v, want not found", entityType, err)
		}
		if _, err := w.repo.GetByID(w.client1Ctx, f.OrgA, foreign[entityType]); err == nil {
			t.Errorf("client 1 reads the document of a %s of client 2", entityType)
		}
	}
	if got := w.linkCount(t, own); got != "1/1" {
		t.Errorf("own document after the refused links: link_count/links %s, want 1/1", got)
	}
	// A missing target is not found, also for an org-wide principal.
	for _, entityType := range []string{"stock_movement", "stocktake", "location", "disposal_record"} {
		if _, err := w.link(w.orgCtx, own, entityType, f.ID()); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("link to a missing %s: %v, want not found", entityType, err)
		}
	}
	stocktake := w.scalar(t, `INSERT INTO stocktake (organization_id, title) VALUES ($1, 'lk-stocktake') RETURNING id::text`, f.OrgA)
	if _, err := w.link(w.orgCtx, own, "stocktake", stocktake); err != nil {
		t.Errorf("link to an existing stocktake: %v", err)
	}
	if _, err := w.link(w.orgCtx, own, "contact", f.ID()); err == nil || errors.Is(err, api.ErrNotFound) {
		t.Errorf("link to an entity type outside the registry: %v, want a field error", err)
	}

	// Site scope: the link to an asset carries its site.
	atSite1, atSite2 := w.doc(t, "lk-asset-site1", "asset", w.asset1), w.doc(t, "lk-asset-site2", "asset", w.asset2)
	if _, err := w.repo.GetByID(w.site1Ctx, f.OrgA, atSite1); err != nil {
		t.Errorf("site-1 principal reads the document of an asset at site 1: %v", err)
	}
	if _, err := w.repo.GetByID(w.site1Ctx, f.OrgA, atSite2); err == nil {
		t.Error("site-1 principal reads the document of an asset at another site")
	}

	// Team scope: a document hanging only at a ticket of team B.
	teamB := w.doc(t, "lk-team-b", "ticket", w.ticketB)
	if _, err := w.repo.GetByID(w.teamACtx, f.OrgA, teamB); err == nil {
		t.Error("team-A principal reads a document of a team-B ticket through the repository")
	}
	var visible int
	if err := database.WithRequestTenant(w.teamACtx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM document WHERE id = $1`, teamB).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Errorf("team-A principal SELECTs the team-B document: %d rows, err %v; want 0 (hitVisible of the search)", visible, err)
	}
	title := "lk-renamed-by-team-a"
	if _, err := w.repo.Update(w.teamACtx, f.OrgA, teamB, document.UpdateRequest{Title: &title}); err == nil {
		t.Error("team-A principal renamed the team-B document")
	}
	if err := w.repo.Delete(w.teamACtx, f.OrgA, teamB); err == nil {
		t.Error("team-A principal deleted the team-B document")
	}
	// Even unrestricted in client and site, the row policies refuse it.
	if err := database.WithRequestTenant(w.teamACtx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE document SET title = 'x' WHERE id = $1`, teamB)
		visible = int(tag.RowsAffected())
		return err
	}); err != nil || visible != 0 {
		t.Errorf("team-A principal UPDATEs the team-B document: %d rows, err %v; want 0", visible, err)
	}
	if got := w.scalar(t, `SELECT title FROM document WHERE id = $1`, teamB); got != "lk-team-b" {
		t.Errorf("team-B document title %q, want unchanged", got)
	}

	// A document of team A: the team-A principal links it to another
	// ticket of its team and removes the link again.
	teamA := w.doc(t, "lk-team-a", "ticket", w.ticketA)
	linkID, err := w.link(w.teamACtx, teamA, "ticket", w.ticketA2)
	if err != nil {
		t.Fatalf("team-A principal links its document to a team-A ticket: %v", err)
	}
	if got := w.linkCount(t, teamA); got != "2/2" {
		t.Errorf("after the link: link_count/links %s, want 2/2", got)
	}
	if _, err = w.link(w.teamACtx, teamA, "ticket", w.ticketB); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("team-A principal links a team-B ticket: %v, want not found", err)
	}
	if _, err = w.link(w.teamACtx, teamA, "asset", w.asset1); err == nil {
		t.Error("team-A principal linked a target without a team (E-09, E-11)")
	}
	if err = w.repo.UnlinkDocument(w.teamACtx, f.OrgA, teamA, linkID); err != nil {
		t.Fatalf("team-A principal removes the link: %v", err)
	}
	if got := w.linkCount(t, teamA); got != "1/1" {
		t.Errorf("after the unlink: link_count/links %s, want 1/1", got)
	}
	title = "lk-team-a-renamed"
	if _, err = w.repo.Update(w.teamACtx, f.OrgA, teamA, document.UpdateRequest{Title: &title}); err != nil {
		t.Errorf("team-A principal renames its document: %v", err)
	}

	// The team change of the ticket is propagated: team A now sees the
	// document of the former team-B ticket.
	if _, err = f.Admin.Exec(context.Background(), `UPDATE ticket SET team_id = $2 WHERE id = $1`, w.ticketB, w.teamA); err != nil {
		t.Fatal(err)
	}
	if _, err = w.repo.GetByID(w.teamACtx, f.OrgA, teamB); err != nil {
		t.Errorf("team-A principal after the team change of the ticket: %v", err)
	}
}

// TestDocumentUnlinkRouteAndAudit (WP-267, MGT-01, API-06): DELETE
// /api/v1/documents/{id}/links/{link_id} removes a link in the caller's
// scope (204) and answers 404 for any other; POST …/links answers 422 for
// an unknown entity type. Both writes leave document.linked and
// document.unlinked with link id and target.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentUnlinkRouteAndAudit(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	shared := w.doc(t, "lk-shared", "asset", w.asset1, "asset", w.asset2)
	linkOf := func(entity string) string {
		return w.scalar(t, `SELECT id::text FROM document_link WHERE document_id = $1 AND entity_id = $2`, shared, entity)
	}
	link1, link2 := linkOf(w.asset1), linkOf(w.asset2)

	mux := chi.NewRouter()
	document.NewHandler(w.repo, nil).RegisterRoutes(mux)
	orgWide := database.OrgWideScope(f.OrgA, w.user)
	call := func(scope *database.TenantScope, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: w.user})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return rec
	}
	links := "/api/v1/documents/" + shared + "/links"

	for _, c := range []struct {
		name, path string
		scope      *database.TenantScope
		want       int
	}{
		{"link of client 2 as client 1", links + "/" + link2, &w.client1Scope, http.StatusNotFound},
		{"link of another document", "/api/v1/documents/" + w.doc(t, "lk-other", "asset", w.asset1) + "/links/" + link1, &orgWide, http.StatusNotFound},
		{"unknown link", links + "/" + f.ID(), &orgWide, http.StatusNotFound},
		{"malformed link id", links + "/not-a-uuid", &orgWide, http.StatusNotFound},
		{"own link as client 1", links + "/" + link1, &w.client1Scope, http.StatusNoContent},
		{"removed link again", links + "/" + link1, &orgWide, http.StatusNotFound},
	} {
		rec := call(c.scope, http.MethodDelete, c.path, "")
		if rec.Code != c.want {
			t.Errorf("DELETE %s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
		if dbLeak.MatchString(rec.Body.String()) {
			t.Errorf("DELETE %s: body discloses database internals: %s", c.name, rec.Body.String())
		}
	}
	if got := w.linkCount(t, shared); got != "1/1" {
		t.Errorf("shared document: link_count/links %s, want 1/1 (only the client-2 link)", got)
	}

	rec := call(&orgWide, http.MethodPost, links, `{"entity_type":"contact","entity_id":"`+f.ID()+`"}`)
	if rec.Code != http.StatusUnprocessableEntity || dbLeak.MatchString(rec.Body.String()) {
		t.Errorf("POST a link to an unknown entity type: %d %s, want 422", rec.Code, rec.Body.String())
	}
	rec = call(&orgWide, http.MethodPost, links, `{"entity_type":"location","entity_id":"`+w.room1+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST a link to a location: %d %s", rec.Code, rec.Body.String())
	}
	var created document.DocumentLink
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// The audit entries of the routes: actor, document and link.
	entry := func(action, linkID string) string {
		t.Helper()
		var out string
		err := f.Admin.QueryRow(context.Background(), `SELECT resource_type || '|' || COALESCE(actor_id::text, '-') || '|' ||
				(changes->>'entity_type') || '|' || (changes->>'entity_id')
			FROM audit_log WHERE organization_id = $1 AND action = $2 AND resource_id = $3 AND changes->>'link_id' = $4`,
			f.OrgA, action, shared, linkID).Scan(&out)
		if err != nil {
			t.Errorf("audit %s of link %s: %v", action, linkID, err)
		}
		return out
	}
	if got, want := entry(document.ActionUnlinked, link1), "document|"+w.user+"|asset|"+w.asset1; got != want {
		t.Errorf("document.unlinked: %s, want %s", got, want)
	}
	if got, want := entry(document.ActionLinked, created.ID), "document|"+w.user+"|location|"+w.room1; got != want {
		t.Errorf("document.linked: %s, want %s", got, want)
	}
	// The repository link of the fixture has no user in its context.
	if got, want := entry(document.ActionLinked, link2), "document|-|asset|"+w.asset2; got != want {
		t.Errorf("document.linked of the fixture: %s, want %s", got, want)
	}
}
