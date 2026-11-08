package ticket_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The tests of this file run only against PostgreSQL (TEST_DATABASE_URL,
// see scopetest.Seed). They cover TKT-01 of WP-764: object links of a
// ticket (ci, asset, location, contract, review_item) and ticket tags from
// the tag catalog (E-127).

// linkTargets seeds one target per entity type in organization A (client
// clientID) through the maintenance pool.
func linkTargets(t *testing.T, f *scopetest.Fixture, clientID, name string) map[string]string {
	t.Helper()
	ctx := context.Background()
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
		return id
	}
	ciID := f.CI(t, f.OrgA, clientID, "link-ci-"+name)
	return map[string]string{
		ticket.LinkCI:    ciID,
		ticket.LinkAsset: f.Asset(t, f.OrgA, clientID, "link-asset-"+name),
		// A site row brings its location row (location_sync, 000062).
		ticket.LinkLocation: scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, clientID, "link-site-"+name),
		ticket.LinkContract: scalar(`INSERT INTO contract (organization_id, client_id, number, currency) VALUES ($1, $2, $3, 'EUR') RETURNING id::text`,
			f.OrgA, clientID, "link-contract-"+name),
		ticket.LinkReviewItem: scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids)
			VALUES ($1, 'ambiguous_identity', '{}', ARRAY[$2::uuid]) RETURNING id::text`, f.OrgA, ciID),
	}
}

// linkScenario runs link, list and unlink through h and returns the status
// codes, so the PostgreSQL and the memory repository can be compared.
func linkScenario(t *testing.T, e *ticketEnv, h http.Handler, ticketID string, targets map[string]string) []int {
	t.Helper()
	url := "/api/v1/tickets/" + ticketID + "/links"
	var codes []int
	var created []ticket.Link
	for _, entityType := range ticket.LinkEntityTypes {
		w := e.request(t, h, http.MethodPost, url, map[string]any{"entity_type": entityType, "entity_id": targets[entityType]})
		codes = append(codes, w.Code)
		var l ticket.Link
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		created = append(created, l)
	}
	// A second link of the same object, an unknown entity type, a missing
	// ticket.
	codes = append(codes,
		e.request(t, h, http.MethodPost, url, map[string]any{"entity_type": "ci", "entity_id": targets[ticket.LinkCI]}).Code,
		e.request(t, h, http.MethodPost, url, map[string]any{"entity_type": "stocktake", "entity_id": targets[ticket.LinkCI]}).Code,
		e.request(t, h, http.MethodPost, "/api/v1/tickets/"+e.f.ID()+"/links", map[string]any{"entity_type": "ci", "entity_id": targets[ticket.LinkCI]}).Code,
	)

	// Pages of two with the cursor return every link once, oldest first.
	var listed []ticket.Link
	next := ""
	for range 5 {
		w := e.request(t, h, http.MethodGet, url+"?limit=2"+next, nil)
		codes = append(codes, w.Code)
		var page struct {
			Data       []ticket.Link `json:"data"`
			Total      int           `json:"total"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != len(ticket.LinkEntityTypes) {
			t.Errorf("total %d, want %d", page.Total, len(ticket.LinkEntityTypes))
		}
		listed = append(listed, page.Data...)
		if page.NextCursor == "" {
			break
		}
		next = "&cursor=" + page.NextCursor
	}
	if len(listed) != len(created) {
		t.Fatalf("listed %d links, want %d: %+v", len(listed), len(created), listed)
	}
	for i := range listed {
		if listed[i].ID != created[i].ID || listed[i].EntityType != created[i].EntityType || listed[i].EntityID != created[i].EntityID {
			t.Errorf("link %d: %+v, want %+v", i, listed[i], created[i])
		}
	}
	codes = append(codes, e.request(t, h, http.MethodGet, url+"?cursor=forged", nil).Code)

	codes = append(codes,
		e.request(t, h, http.MethodDelete, url+"/"+created[0].ID, nil).Code,
		e.request(t, h, http.MethodDelete, url+"/"+created[0].ID, nil).Code,
	)
	return codes
}

func TestTicketLinksInPostgreSQL(t *testing.T) {
	e := newTicketEnv(t)
	targets := linkTargets(t, e.f, e.f.Client1, "pg")
	tk := e.create(t, map[string]any{"title": "Objektbezug"})

	got := linkScenario(t, e, e.mux, tk.ID, targets)
	want := []int{201, 201, 201, 201, 201, 409, 422, 404, 200, 200, 200, 400, 204, 404}
	if !slices.Equal(got, want) {
		t.Fatalf("PostgreSQL status codes %v, want %v", got, want)
	}

	// Parity: the memory repository answers alike (it checks no target).
	memory := ticket.NewMemoryRepository()
	memMux := chi.NewRouter()
	ticket.NewHandler(ticket.NewService(memory, nil)).RegisterRoutes(memMux)
	w := e.request(t, memMux, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Objektbezug"})
	var memTicket ticket.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &memTicket); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("memory ticket: %d %s", w.Code, w.Body)
	}
	if mem := linkScenario(t, e, memMux, memTicket.ID, targets); !slices.Equal(mem, want) {
		t.Fatalf("memory status codes %v, want %v", mem, want)
	}

	// One ticket.updated per link and unlink, with link, entity type and id.
	var changes []string
	for _, ev := range e.ticketEvents(t, tk.ID) {
		change, _ := ev["change"].(string)
		if change == "link" || change == "unlink" {
			if ev["link_id"] == "" || ev["entity_type"] == "" || ev["entity_id"] == "" {
				t.Errorf("event %v lacks the link", ev)
			}
			changes = append(changes, change)
		}
	}
	if wantChanges := []string{"link", "link", "link", "link", "link", "unlink"}; !slices.Equal(changes, wantChanges) {
		t.Errorf("link events %v, want %v", changes, wantChanges)
	}

	// Each link carries exactly one target column, the one of its type.
	var bad int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ticket_link WHERE ticket_id = $1 AND NOT (
		num_nonnulls(ci_id, asset_id, location_id, contract_id, review_item_id) = 1)`, tk.ID).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("links with more than one target: %d err=%v", bad, err)
	}
}

func TestTicketLinksScope(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	bg := context.Background()
	own := linkTargets(t, f, f.Client1, "own")
	foreign := linkTargets(t, f, f.Client2, "foreign")
	ownTicket := e.create(t, map[string]any{"title": "Client 1", "related_ci_id": own[ticket.LinkCI]})
	foreignTicket := e.create(t, map[string]any{"title": "Client 2", "related_ci_id": foreign[ticket.LinkCI]})
	// The org-wide principal links the client-1 ticket to a client-2 asset.
	hidden := &ticket.Link{OrganizationID: f.OrgA, TicketID: ownTicket.ID, EntityType: ticket.LinkAsset, EntityID: foreign[ticket.LinkAsset]}
	if err := e.repo.CreateLink(f.OrgCtx(f.OrgA), hidden); err != nil {
		t.Fatalf("org-wide link: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	link := func(ticketID, entityType, entityID string) error {
		return e.repo.CreateLink(ctx, &ticket.Link{OrganizationID: f.OrgA, TicketID: ticketID, EntityType: entityType, EntityID: entityID})
	}
	for _, entityType := range ticket.LinkEntityTypes {
		if err := link(ownTicket.ID, entityType, foreign[entityType]); !errors.Is(err, ticket.ErrNotFound) {
			t.Errorf("client 1 links a client-2 %s: %v, want ErrNotFound", entityType, err)
		}
	}
	if err := link(foreignTicket.ID, ticket.LinkCI, own[ticket.LinkCI]); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("client 1 links a client-2 ticket: %v, want ErrNotFound", err)
	}
	if err := link(ownTicket.ID, ticket.LinkReviewItem, own[ticket.LinkReviewItem]); err != nil {
		t.Fatalf("client 1 links its review item: %v", err)
	}
	// A review item with a candidate CI of client 2 is invisible.
	var mixed string
	if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', '{}', ARRAY[$2::uuid, $3::uuid]) RETURNING id::text`,
		f.OrgA, own[ticket.LinkCI], foreign[ticket.LinkCI]).Scan(&mixed); err != nil {
		t.Fatal(err)
	}
	if err := link(ownTicket.ID, ticket.LinkReviewItem, mixed); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("review item with a client-2 candidate: %v, want ErrNotFound", err)
	}

	// The link on the client-2 asset is hidden from client 1, and so is
	// the client-2 ticket's list.
	links, total, err := e.repo.ListLinks(ctx, f.OrgA, ownTicket.ID, api.PaginationParams{Limit: 50})
	if err != nil || total != 1 || len(links) != 1 || links[0].EntityType != ticket.LinkReviewItem {
		t.Fatalf("client-1 links: total=%d %+v err=%v, want only the review item", total, links, err)
	}
	if _, _, err = e.repo.ListLinks(ctx, f.OrgA, foreignTicket.ID, api.PaginationParams{Limit: 50}); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("client-2 ticket links: %v, want ErrNotFound", err)
	}
	if err = e.repo.DeleteLink(ctx, f.OrgA, ownTicket.ID, hidden.ID); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("client 1 deletes the hidden link: %v, want ErrNotFound", err)
	}

	// Client and team of the ticket are derived into the links: a change
	// of the primary object (client) and of the team re-derives them.
	scope := func() (client, team string) {
		t.Helper()
		if scanErr := f.Admin.QueryRow(bg, `SELECT COALESCE(min(client_id::text), ''), COALESCE(min(team_id::text), '')
			FROM ticket_link WHERE ticket_id = $1`, ownTicket.ID).Scan(&client, &team); scanErr != nil {
			t.Fatal(scanErr)
		}
		return client, team
	}
	if client, _ := scope(); client != f.Client1 {
		t.Fatalf("link client %q, want client 1", client)
	}
	team := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'links')`, team, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if w := e.request(t, e.mux, http.MethodPatch, "/api/v1/tickets/"+ownTicket.ID,
		map[string]any{"related_ci_id": foreign[ticket.LinkCI], "team_id": team}); w.Code != http.StatusOK {
		t.Fatalf("move ticket: %d %s", w.Code, w.Body)
	}
	if client, gotTeam := scope(); client != f.Client2 || gotTeam != team {
		t.Fatalf("link scope after the move: client %q team %q, want client 2 and the team", client, gotTeam)
	}
}

func TestTicketLinksGoWithTheirTarget(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	bg := context.Background()
	targets := linkTargets(t, f, f.Client1, "del")
	tk := e.create(t, map[string]any{"title": "Löschen"})
	for _, entityType := range ticket.LinkEntityTypes {
		if err := e.repo.CreateLink(f.OrgCtx(f.OrgA), &ticket.Link{OrganizationID: f.OrgA, TicketID: tk.ID, EntityType: entityType, EntityID: targets[entityType]}); err != nil {
			t.Fatalf("link %s: %v", entityType, err)
		}
	}
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ticket_link WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The hard delete of the CI (JOB-02) and the delete of the asset
	// remove their links.
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, targets[ticket.LinkCI]); err != nil {
		_ = tx.Rollback(bg)
		t.Fatalf("soft delete: %v", err)
	}
	if err = jobs.HardDeleteCI(bg, tx, f.OrgA, targets[ticket.LinkCI]); err != nil {
		_ = tx.Rollback(bg)
		t.Fatalf("hard delete: %v", err)
	}
	if err = tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `DELETE FROM asset WHERE id = $1`, targets[ticket.LinkAsset]); err != nil {
		t.Fatal(err)
	}
	if n := count(`ticket_id = $1 AND entity_type IN ('ci', 'asset')`, tk.ID); n != 0 {
		t.Errorf("%d links on deleted CI or asset remain", n)
	}
	if n := count(`ticket_id = $1`, tk.ID); n != 3 {
		t.Errorf("%d links remain, want 3", n)
	}
	if w := e.request(t, e.mux, http.MethodDelete, "/api/v1/tickets/"+tk.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete ticket: %d %s", w.Code, w.Body)
	}
	if n := count(`ticket_id = $1`, tk.ID); n != 0 {
		t.Errorf("%d links of the deleted ticket remain", n)
	}
}

func TestTicketTagsFromTheCatalog(t *testing.T) {
	e := newTicketEnv(t)
	f := e.f
	bg := context.Background()
	tag := func(key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO tag (organization_id, key, display_name) VALUES ($1, $2, '{"de": "x", "en": "x"}') RETURNING id::text`,
			f.OrgA, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tag("netz")
	tag("server")
	src := tag("drucker")
	into := tag("printer")

	// An unknown key is 422 with the unknown keys; nothing is created.
	w := e.request(t, e.mux, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "x", "tags": []string{"netz", "gibtsnicht", "auch-nicht"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "gibtsnicht") || !strings.Contains(w.Body.String(), "auch-nicht") {
		t.Fatalf("unknown tag: %d %s, want 422 naming the keys", w.Code, w.Body)
	}
	a := e.create(t, map[string]any{"title": "A", "tags": []string{"server", "netz", "netz"}})
	if !slices.Equal(a.Tags, []string{"netz", "server"}) {
		t.Fatalf("tags of A %v, want [netz server]", a.Tags)
	}
	b := e.create(t, map[string]any{"title": "B", "tags": []string{"netz", "drucker", "printer"}})
	c := e.create(t, map[string]any{"title": "C"})
	if c.Tags == nil || len(c.Tags) != 0 {
		t.Fatalf("tags of C %#v, want []", c.Tags)
	}
	if w = e.request(t, e.mux, http.MethodPatch, "/api/v1/tickets/"+c.ID, map[string]any{"tags": []string{"drucker"}}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"tags":["drucker"]`) {
		t.Fatalf("update tags: %d %s", w.Code, w.Body)
	}
	if w = e.request(t, e.mux, http.MethodPatch, "/api/v1/tickets/"+c.ID, map[string]any{"tags": []string{"nope"}}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("update unknown tag: %d %s, want 422", w.Code, w.Body)
	}
	last := e.ticketEvents(t, c.ID)
	if ev := last[len(last)-1]; ev["change"] != "updated" || !slices.Equal(anyStrings(ev["changed_fields"]), []string{"tags"}) {
		t.Fatalf("last event of C %v, want updated with tags", ev)
	}

	// The filter tags: every key.
	list := func(tags string) []string {
		t.Helper()
		items, _, err := e.repo.List(f.OrgCtx(f.OrgA), f.OrgA, &ticket.FilterParams{Tags: strings.Split(tags, ",")}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for i := range items {
			titles = append(titles, items[i].Title)
		}
		slices.Sort(titles)
		return titles
	}
	if got := list("netz"); !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("tags=netz: %v", got)
	}
	if got := list("netz,server"); !slices.Equal(got, []string{"A"}) {
		t.Errorf("tags=netz,server: %v", got)
	}

	// Scope of ticket_tag follows the ticket's client and team.
	team := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'tags')`, team, f.OrgA); err != nil {
		t.Fatal(err)
	}
	ciID := f.CI(t, f.OrgA, f.Client2, "tag-ci")
	if w = e.request(t, e.mux, http.MethodPatch, "/api/v1/tickets/"+a.ID, map[string]any{"related_ci_id": ciID, "team_id": team}); w.Code != http.StatusOK {
		t.Fatalf("move A: %d %s", w.Code, w.Body)
	}
	var wrong int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ticket_tag WHERE ticket_id = $1
		AND (client_id IS DISTINCT FROM $2::uuid OR team_id IS DISTINCT FROM $3::uuid)`, a.ID, f.Client2, team).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("ticket_tag rows of A with stale scope: %d err=%v", wrong, err)
	}

	// The search finds a ticket by a tag key.
	searchRepo := search.NewPGRepository(f.App)
	if _, err := searchRepo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	var summary string
	if err := f.Admin.QueryRow(bg, `SELECT summary FROM search_document WHERE entity_type = 'ticket' AND entity_id = $1`, b.ID).Scan(&summary); err != nil ||
		!strings.Contains(summary, "drucker netz printer") {
		t.Fatalf("search summary of B %q err=%v, want the tag keys", summary, err)
	}

	// Merging drucker into printer moves the ticket assignments (B keeps
	// printer once, C gets printer), one ticket.updated per ticket.
	cis := ci.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	before := map[string]int{b.ID: len(e.ticketEvents(t, b.ID)), c.ID: len(e.ticketEvents(t, c.ID))}
	if _, err := cis.MergeTag(f.OrgCtx(f.OrgA), f.OrgA, src, into); err != nil {
		t.Fatalf("merge: %v", err)
	}
	for id, want := range map[string][]string{b.ID: {"netz", "printer"}, c.ID: {"printer"}} {
		got, err := e.repo.GetByID(f.OrgCtx(f.OrgA), f.OrgA, id)
		if err != nil || !slices.Equal(got.Tags, want) {
			t.Errorf("tags after merge %v err=%v, want %v", got, err, want)
		}
		evs := e.ticketEvents(t, id)
		if len(evs) != before[id]+1 || evs[len(evs)-1]["change"] != "updated" || !slices.Equal(anyStrings(evs[len(evs)-1]["changed_fields"]), []string{"tags"}) {
			t.Errorf("events of %s after merge: %v", id, evs[before[id]:])
		}
	}
	// A tag assigned to a ticket cannot be deleted.
	if err := cis.DeleteTag(f.OrgCtx(f.OrgA), f.OrgA, into); !errors.Is(err, ci.ErrTagInUse) {
		t.Fatalf("delete assigned tag: %v, want ErrTagInUse", err)
	}
	if _, _, err := e.repo.List(context.Background(), f.OrgA, &ticket.FilterParams{}, api.PaginationParams{Limit: 1}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: %v", err)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, s := range list {
		str, _ := s.(string)
		out = append(out, str)
	}
	return out
}

// TestTicketTagsMigration runs the down and the up migration 000230 in a
// transaction that is rolled back: the down brings ticket.tags back from
// ticket_tag, the up moves the values to catalog tags with normalized keys
// (missing ones created, empty keys dropped) and drops the column.
func TestTicketTagsMigration(t *testing.T) {
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/000230_ticket_links." + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	user := f.AppUser(t, f.OrgA, "migration")
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(step, sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", step, execErr)
		}
	}
	exec("tag", `INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'netz', '{"de": "Netz", "en": "Netz"}')`, f.OrgA)
	var kept string
	if err = tx.QueryRow(bg, `INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 'kept', $2) RETURNING id::text`,
		f.OrgA, user).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	exec("ticket tag", `INSERT INTO ticket_tag (organization_id, ticket_id, tag_id) SELECT $1, $2, id FROM tag WHERE organization_id = $1 AND key = 'netz'`, f.OrgA, kept)

	// 000275 hangs reservation.ticket_id on the key the down drops; the
	// later migration goes first, as a real rollback would order it.
	exec("later fk", `ALTER TABLE reservation DROP CONSTRAINT reservation_ticket_id_fkey`)
	exec("down", read("down"))
	var tags []string
	if err = tx.QueryRow(bg, `SELECT tags FROM ticket WHERE id = $1`, kept).Scan(&tags); err != nil || !slices.Equal(tags, []string{"netz"}) {
		t.Fatalf("down: tags %v err=%v, want [netz]", tags, err)
	}
	var legacy string
	if err = tx.QueryRow(bg, `INSERT INTO ticket (organization_id, title, reporter_id, tags) VALUES ($1, 'legacy', $2, ARRAY['Netz', 'Kern Switch', '--', 'kern switch'])
		RETURNING id::text`, f.OrgA, user).Scan(&legacy); err != nil {
		t.Fatal(err)
	}

	exec("up", read("up"))
	keys := func(id string) []string {
		t.Helper()
		rows, qErr := tx.Query(bg, `SELECT g.key FROM ticket_tag tt JOIN tag g ON g.id = tt.tag_id WHERE tt.ticket_id = $1 ORDER BY g.key`, id)
		if qErr != nil {
			t.Fatal(qErr)
		}
		out, qErr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qErr != nil {
			t.Fatal(qErr)
		}
		return out
	}
	if got := keys(legacy); !slices.Equal(got, []string{"kern_switch", "netz"}) {
		t.Errorf("legacy keys %v, want [kern_switch netz]", got)
	}
	if got := keys(kept); !slices.Equal(got, []string{"netz"}) {
		t.Errorf("kept keys %v, want [netz]", got)
	}
	var name string
	if err = tx.QueryRow(bg, `SELECT display_name ->> 'de' FROM tag WHERE organization_id = $1 AND key = 'kern_switch'`, f.OrgA).Scan(&name); err != nil || name != "Kern Switch" {
		t.Errorf("display name %q err=%v, want Kern Switch", name, err)
	}
	var column bool
	if err = tx.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'ticket' AND column_name = 'tags')`).Scan(&column); err != nil || column {
		t.Errorf("ticket.tags after up: %v err=%v, want dropped", column, err)
	}
}
