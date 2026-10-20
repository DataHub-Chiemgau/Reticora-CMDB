package rls_test

// WP-342 (TEN-02, TEN-05, TEN-06, TEN-09; F-003): review items carry the
// client and site of what they are about (migration 000140). With candidate
// CIs both are derived from the candidates and follow a move of a candidate;
// without candidates the producer sets them (record, collector) and an item
// without a client is visible to org-wide principals only (E-08). A scoped
// principal writes and resolves no item with client_id NULL (E-09).
//
// Formerly the defect tests TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope
// (audit_t1_review_rack_scope_auditdefect_test.go) and
// TestAuditT1ReviewItemRowsCarryClientScope (audit_t1_team_scope_auditdefect_test.go).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^(TestReviewItemsWithoutCandidatesStayInClientScope|TestReviewItemRowsCarryClientScope)$' ./internal/tenant/rls/

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// countReviewItems counts the review items among ids that scope selects
// directly in WithTenant (no repository predicate).
func countReviewItems(t *testing.T, pool *pgxpool.Pool, scope *database.TenantScope, ids ...string) int {
	t.Helper()
	var n int
	if err := database.WithTenant(context.Background(), pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM review_item WHERE id = ANY ($1::uuid[])`, ids).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReviewItemsWithoutCandidatesStayInClientScope opens review items
// without candidate CIs through the product's own write paths for a device
// and a subnet of client 2 and checks that a client-1 principal can neither
// list, read nor resolve them (REST and GraphQL) nor select them directly,
// while the client-2 and the org-wide principal see them. An item without a
// derivable client (probe_review_no_candidate.sql) is visible to org-wide
// principals only.
func TestReviewItemsWithoutCandidatesStayInClientScope(t *testing.T) {
	w := newT1World(t, "d3")
	f := w.f
	bg := context.Background()
	client1 := w.as("client1", only(f.Client1), all())
	client2 := w.as("client2", only(f.Client2), all())
	clients12 := w.as("clients1+2", only(f.Client1, f.Client2), all())
	collector2 := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 't1rev-collector-c2', 'active') RETURNING id::text`,
		f.OrgA, f.Client2)

	// unclassified_device: a record of the client-2 discovery scope whose CI
	// type the server does not know (discovery.go openFrame).
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "t1rev-unknown-type", Name: "t1rev-c2-firewall", IP: "10.77.2.1", Serial: "T1REV-C2-SN"},
		time.Now().Add(-time.Minute))
	rec.ClientID = f.Client2
	if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/ingest/bulk", plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}}); code != http.StatusAccepted {
		t.Fatalf("ingest the client-2 record: %d %s", code, trim(body))
	}
	// scope_suggested: the client-2 collector reports an uncovered subnet
	// (scope_handler.go SuggestScopes, the NATS subnets report path).
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	if n, err := discovery.NewHandler(repos.Discovery, repos.CI).SuggestScopes(f.OrgCtx(f.OrgA), f.OrgA, collector2, []string{"10.77.9.0/24"}); err != nil || n != 1 {
		t.Fatalf("suggest the client-2 subnet: n=%d err=%v", n, err)
	}

	items := map[string]string{}
	rows, err := f.Admin.Query(bg, `SELECT kind, id::text FROM review_item WHERE organization_id = $1 AND cardinality(candidate_ci_ids) = 0
		AND kind IN ('unclassified_device', 'scope_suggested')`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatal(err)
		}
		items[kind] = id
	}
	rows.Close()
	if items["unclassified_device"] == "" || items["scope_suggested"] == "" {
		t.Fatalf("setup: review items without candidates %v, want unclassified_device and scope_suggested", items)
	}

	// probe_review_no_candidate.sql: an item without candidates and without
	// a derivable client (written directly, payload of a client-2 device).
	orphan := w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids)
		VALUES ($1, 'unclassified_device', '{"name":"t1rev-c2-secret-fw","management_ip":"10.77.2.9"}', '{}') RETURNING id::text`, f.OrgA)

	// Positive controls: the org-wide principal sees all three, the client-2
	// principal its two items.
	_, orgBody := w.do(w.orgA, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	for kind, id := range map[string]string{"unclassified_device": items["unclassified_device"], "scope_suggested": items["scope_suggested"], "orphan": orphan} {
		if !strings.Contains(orgBody, id) {
			t.Fatalf("org-wide principal does not see the %s item %s", kind, id)
		}
	}
	if _, gql := w.do(w.orgA, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ reviewInbox(first: 100) { id } }"}); !strings.Contains(gql, items["unclassified_device"]) {
		t.Fatalf("org-wide principal: GraphQL reviewInbox does not list the unclassified_device item: %s", trim(gql))
	}
	_, c2Body := w.do(client2, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	for kind, id := range items {
		if !strings.Contains(c2Body, id) {
			t.Errorf("client-2 principal does not see its %s item", kind)
		}
		if code, _ := w.do(client2, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusOK {
			t.Errorf("GET /review-items/{%s of client 2} as client 2: %d, want 200", kind, code)
		}
	}

	code, body := w.do(client1, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /review-items as client 1: %d %s", code, trim(body))
	}
	for _, marker := range []string{"t1rev-c2-firewall", "T1REV-C2-SN", "10.77.9.0/24", "t1rev-collector-c2", f.Client2, "t1rev-c2-secret-fw", orphan} {
		if strings.Contains(body, marker) {
			t.Errorf("TEN-06/TEN-09: GET /review-items as client 1 discloses %q of client 2", marker)
		}
	}
	if strings.Contains(c2Body, orphan) {
		t.Errorf("E-08: the client-2 principal sees the review item without a client")
	}
	targets := map[string]string{"unclassified_device": items["unclassified_device"], "scope_suggested": items["scope_suggested"]}
	for kind, id := range targets {
		if code, _ := w.do(client1, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusNotFound {
			t.Errorf("GET /review-items/{%s of client 2} as client 1: %d, want 404", kind, code)
		}
		if code, _ := w.do(client1, http.MethodPost, "/api/v1/review-items/"+id+"/resolve", map[string]any{"action": "dismiss"}); code != http.StatusNotFound {
			t.Errorf("POST /review-items/{%s of client 2}/resolve as client 1: %d, want 404", kind, code)
		}
		if st := w.scalar(`SELECT status FROM review_item WHERE id = $1`, id); st != "open" {
			t.Errorf("%s item of client 2 after the client-1 resolve: status %q, want open", kind, st)
		}
	}
	// E-09: a scoped principal resolves no item without a client, even one
	// restricted to every client of the organization.
	for _, p := range []*channelPrincipal{client1, clients12} {
		if code, _ := w.do(p, http.MethodGet, "/api/v1/review-items/"+orphan, nil); code != http.StatusNotFound {
			t.Errorf("GET /review-items/{without client} as %s: %d, want 404", p.kind, code)
		}
		if code, _ := w.do(p, http.MethodPost, "/api/v1/review-items/"+orphan+"/resolve", map[string]any{"action": "dismiss"}); code != http.StatusNotFound {
			t.Errorf("POST /review-items/{without client}/resolve as %s: %d, want 404", p.kind, code)
		}
	}
	if st := w.scalar(`SELECT status FROM review_item WHERE id = $1`, orphan); st != "open" {
		t.Errorf("review item without a client after the scoped resolves: status %q, want open", st)
	}
	// GraphQL reviewInbox is the same channel.
	if _, gql := w.do(client1, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ reviewInbox(first: 100) { id } }"}); strings.Contains(gql, items["unclassified_device"]) ||
		strings.Contains(gql, items["scope_suggested"]) || strings.Contains(gql, orphan) {
		t.Errorf("TEN-09: GraphQL reviewInbox as client 1 lists review items of client 2")
	}

	// The database enforces the same without the repository predicate.
	scope1 := database.OrgWideScope(f.OrgA, w.userA)
	scope1.Clients = database.ScopeIDs(f.Client1)
	scope2 := database.OrgWideScope(f.OrgA, w.userA)
	scope2.Clients = database.ScopeIDs(f.Client2)
	orgWide := database.OrgWideScope(f.OrgA, w.userA)
	if n := countReviewItems(t, f.App, &scope1, items["unclassified_device"], items["scope_suggested"], orphan); n != 0 {
		t.Errorf("TEN-05: a client-1 principal selects %d review items of client 2 or without a client, want 0", n)
	}
	if n := countReviewItems(t, f.App, &scope2, orphan); n != 0 {
		t.Errorf("E-08: a client-2 principal selects the review item without a client (%d rows), want 0", n)
	}
	if n := countReviewItems(t, f.App, &scope2, items["unclassified_device"], items["scope_suggested"]); n != 2 {
		t.Errorf("a client-2 principal selects %d of its 2 review items", n)
	}
	if n := countReviewItems(t, f.App, &orgWide, items["unclassified_device"], items["scope_suggested"], orphan); n != 3 {
		t.Errorf("the org-wide principal selects %d of the 3 review items", n)
	}

	// The producers set the client of the record and of the collector.
	for kind, id := range items {
		if got := w.scalar(`SELECT COALESCE(client_id::text, 'NULL') FROM review_item WHERE id = $1`, id); got != f.Client2 {
			t.Errorf("TEN-02: %s item of client 2 carries client_id %s, want %s", kind, got, f.Client2)
		}
	}
}

// TestReviewItemRowsCarryClientScope selects review items about client-1,
// client-2 and shared CIs directly in WithTenant: the client and site follow
// the candidate CIs, also when a candidate moves (WP-025), and the client
// and site a writer passes do not override the derivation. Without
// candidates only a client of the organization is kept, and a scoped
// principal writes no item outside its scope (E-09).
func TestReviewItemRowsCarryClientScope(t *testing.T) {
	w := newT1World(t, "de")
	f := w.f
	bg := context.Background()
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-rev-db-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-rev-db-c2")
	review := func(cis ...string) string {
		return w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids) VALUES ($1, 'probable_duplicate', $2, $3::uuid[]) RETURNING id::text`,
			f.OrgA, fmt.Sprintf(`{"name":"t1-rev-db","ci_id":%q}`, cis[0]), cis)
	}
	scopeOf := func(id string) string {
		t.Helper()
		return w.scalar(`SELECT COALESCE(client_id::text, 'NULL') || '|' || COALESCE(site_id::text, 'NULL') FROM review_item WHERE id = $1`, id)
	}
	own, foreign, shared := review(ci1), review(ci2), review(ci1, ci2)
	count := func(scope *database.TenantScope, id string) int {
		t.Helper()
		return countReviewItems(t, f.App, scope, id)
	}
	client1 := database.OrgWideScope(f.OrgA, w.userA)
	client1.Clients = database.ScopeIDs(f.Client1)
	client2 := database.OrgWideScope(f.OrgA, w.userA)
	client2.Clients = database.ScopeIDs(f.Client2)
	orgWide := database.OrgWideScope(f.OrgA, w.userA)
	// Controls: the client-1 principal sees the own item, the org-wide
	// principal both.
	if count(&client1, own) != 1 || count(&orgWide, foreign) != 1 {
		t.Fatalf("controls: client 1 own item %d, org-wide foreign item %d; want 1/1", count(&client1, own), count(&orgWide, foreign))
	}
	if n := count(&client1, foreign); n != 0 {
		t.Errorf("TEN-02/TEN-05 (WP-025 AC1): a client-1 principal selects %d review_item rows about the client-2 CI, want 0", n)
	}
	if got, want := scopeOf(own), f.Client1+"|NULL"; got != want {
		t.Errorf("TEN-02: item about the client-1 CI carries %s, want %s", got, want)
	}
	if got, want := scopeOf(foreign), f.Client2+"|NULL"; got != want {
		t.Errorf("TEN-02: item about the client-2 CI carries %s, want %s", got, want)
	}
	// Candidates of two clients: no common client, visible org-wide only.
	if got := scopeOf(shared); got != "NULL|NULL" {
		t.Errorf("item about CIs of client 1 and 2 carries %s, want NULL|NULL", got)
	}
	if count(&client1, shared) != 0 || count(&client2, shared) != 0 || count(&orgWide, shared) != 1 {
		t.Errorf("item about CIs of client 1 and 2: client 1 %d, client 2 %d, org-wide %d rows; want 0/0/1",
			count(&client1, shared), count(&client2, shared), count(&orgWide, shared))
	}
	// A passed client does not override the candidates; without candidates
	// a client of another organization is dropped.
	spoofed := w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids, client_id) VALUES ($1, 'probable_duplicate', '{}', ARRAY[$2::uuid], $3) RETURNING id::text`,
		f.OrgA, ci2, f.Client1)
	if got, want := scopeOf(spoofed), f.Client2+"|NULL"; got != want {
		t.Errorf("item about the client-2 CI written with client 1 carries %s, want %s", got, want)
	}
	otherOrgClient := w.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 't1-rev-orgb-client', 't1-rev-orgb-client') RETURNING id::text`, f.OrgB)
	alien := w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids, client_id) VALUES ($1, 'scope_suggested', '{}', '{}', $2) RETURNING id::text`,
		f.OrgA, otherOrgClient)
	if got := scopeOf(alien); got != "NULL|NULL" {
		t.Errorf("item without candidates written with a client of organization B carries %s, want NULL|NULL", got)
	}

	// E-09: a scoped principal inserts no item without a client or of
	// another client and resolves no item without a client.
	write := func(scope *database.TenantScope, sql string, args ...any) (int64, error) {
		t.Helper()
		var n int64
		err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	insert := `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids, client_id) VALUES ($1, 'scope_suggested', '{"cidr":"10.78.0.0/24"}', '{}', $2)`
	if _, err := write(&client1, insert, f.OrgA, nil); !rlsDenied(err) {
		t.Errorf("E-09: client-1 principal inserts an item without a client: err %v, want a row level security violation", err)
	}
	if _, err := write(&client1, insert, f.OrgA, f.Client2); !rlsDenied(err) {
		t.Errorf("TEN-05: client-1 principal inserts an item of client 2: err %v, want a row level security violation", err)
	}
	if _, err := write(&client1, insert, f.OrgA, f.Client1); err != nil {
		t.Errorf("client-1 principal inserts an item of client 1: %v", err)
	}
	if _, err := write(&client1, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids) VALUES ($1, 'probable_duplicate', '{}', ARRAY[$2::uuid, $3::uuid])`,
		f.OrgA, ci1, ci2); !rlsDenied(err) {
		t.Errorf("client-1 principal inserts an item about a client-2 CI: err %v, want a row level security violation", err)
	}
	for _, id := range []string{alien, shared} {
		if n, err := write(&client1, `UPDATE review_item SET status = 'dismissed' WHERE id = $1`, id); err != nil || n != 0 {
			t.Errorf("E-09: client-1 principal resolves an org-wide item: %d rows, err %v; want 0 rows", n, err)
		}
	}
	if n, err := write(&orgWide, `UPDATE review_item SET status = 'dismissed' WHERE id = $1`, alien); err != nil || n != 1 {
		t.Errorf("org-wide principal resolves the item without a client: %d rows, err %v; want 1", n, err)
	}

	// WP-025: the scope follows a candidate that moves. ciA and ciB are CIs
	// of client 1 in two sites; the mover is restricted to clients 1 and 2
	// and the sites of ciA before and after the move, so it does not see
	// ciB and the item about both: the propagation must not skip it.
	room1, room1b, room2 := f.Room(t, f.Client1, "t1-rev-room-c1"), f.Room(t, f.Client1, "t1-rev-room-c1b"), f.Room(t, f.Client2, "t1-rev-room-c2")
	site1, site1b, site2 := w.siteOfRoom(room1), w.siteOfRoom(room1b), w.siteOfRoom(room2)
	ciA, ciB := f.CI(t, f.OrgA, f.Client1, "t1-rev-move-a"), f.CI(t, f.OrgA, f.Client1, "t1-rev-move-b")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = CASE id WHEN $1::uuid THEN $3::uuid ELSE $4::uuid END WHERE id IN ($1, $2)`,
		ciA, ciB, room1, room1b); err != nil {
		t.Fatal(err)
	}
	onlyA, onlyB, split := review(ciA), review(ciB), review(ciA, ciB)
	if got, want := scopeOf(onlyA), f.Client1+"|"+site1; got != want {
		t.Errorf("item about ciA carries %s, want %s", got, want)
	}
	if got, want := scopeOf(split), f.Client1+"|NULL"; got != want {
		t.Errorf("item about ciA and ciB (two sites of client 1) carries %s, want %s", got, want)
	}
	mover := database.OrgWideScope(f.OrgA, w.userA)
	mover.Clients = database.ScopeIDs(f.Client1, f.Client2)
	mover.Sites = database.ScopeIDs(site1, site2)
	if count(&mover, split) != 0 {
		t.Fatalf("setup: the mover sees the item about ciB")
	}
	if n, err := write(&mover, `UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1`, ciA, f.Client2, room2); err != nil || n != 1 {
		t.Fatalf("move ciA to client 2 as the mover: %d rows, err %v", n, err)
	}
	if got, want := scopeOf(onlyA), f.Client2+"|"+site2; got != want {
		t.Errorf("WP-025: item about ciA after its move to client 2 carries %s, want %s", got, want)
	}
	if got := scopeOf(split); got != "NULL|NULL" {
		t.Errorf("WP-025: item about ciA and ciB after the move (invisible to the mover) carries %s, want NULL|NULL", got)
	}
	if count(&client2, onlyA) != 1 || count(&client1, onlyA) != 0 {
		t.Errorf("WP-025: item about the moved CI: client 2 %d, client 1 %d rows; want 1/0", count(&client2, onlyA), count(&client1, onlyA))
	}
	// A move to another site changes only the location (ci_derive_location
	// sets site_id): the site follows as well.
	if n, err := write(&orgWide, `UPDATE ci SET location_id = $2 WHERE id = $1`, ciB, room1); err != nil || n != 1 {
		t.Fatalf("move ciB to the site of room 1: %d rows, err %v", n, err)
	}
	if got, want := scopeOf(onlyB), f.Client1+"|"+site1; got != want {
		t.Errorf("item about ciB after its move to another site carries %s, want %s (was site %s)", got, want, site1b)
	}
}
