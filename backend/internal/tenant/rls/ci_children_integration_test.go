package rls_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestCIChildTablesCarryClientScope covers migration 000061 (WP-025, TEN-02,
// TEN-05, IMP-07, SRC-01): the child rows of a CI derive client_id and
// site_id from it whatever the writer passes, follow the CI when it moves to
// another client, and the policies reject rows of a foreign client. A
// relationship is visible only when both endpoints are.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIChildTablesCarryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "42")
	bg := context.Background()
	own := f.CI(t, f.OrgA, f.Client1, "children-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "children-c2")

	orgWide := database.OrgWideScope(f.OrgA, "")
	var nicID, edgeID string
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		// The writer claims client 2; the trigger derives client 1 from the CI.
		if err := tx.QueryRow(ctx,
			`INSERT INTO network_interface (organization_id, ci_id, name, client_id) VALUES ($1, $2, 'eth0', $3) RETURNING id::text`,
			f.OrgA, own, f.Client2).Scan(&nicID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ip_address (organization_id, interface_id, address, type) VALUES ($1, $2, '10.42.0.1', 'static')`,
			f.OrgA, nicID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on') RETURNING id::text`,
			f.OrgA, own, foreign).Scan(&edgeID)
	}); err != nil {
		t.Fatalf("org-wide writes: %v", err)
	}

	var nicClient, ipClient, srcClient, dstClient string
	if err := f.Admin.QueryRow(bg, `
		SELECT (SELECT client_id::text FROM network_interface WHERE id = $1),
		       (SELECT client_id::text FROM ip_address WHERE interface_id = $1),
		       (SELECT source_client_id::text FROM ci_relationship WHERE id = $2),
		       (SELECT target_client_id::text FROM ci_relationship WHERE id = $2)`,
		nicID, edgeID).Scan(&nicClient, &ipClient, &srcClient, &dstClient); err != nil {
		t.Fatalf("read derived scope: %v", err)
	}
	if nicClient != f.Client1 || ipClient != f.Client1 || srcClient != f.Client1 || dstClient != f.Client2 {
		t.Fatalf("derived clients: interface=%s ip=%s edge=%s->%s, want client 1, client 1, client 1->client 2", nicClient, ipClient, srcClient, dstClient)
	}

	client1 := database.OrgWideScope(f.OrgA, "")
	client1.Clients = database.ScopeIDs(f.Client1)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&n)
		}); err != nil {
			t.Fatalf("client-1 query %q: %v", query, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM network_interface WHERE ci_id = $1`, own); n != 1 {
		t.Fatalf("client-1 interfaces of own CI: %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, edgeID); n != 0 {
		t.Fatal("client-1 principal sees an edge to a client-2 CI")
	}
	if err := database.WithTenant(bg, f.App, &client1, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth1')`, f.OrgA, foreign)
		return err
	}); err == nil {
		t.Fatal("client-1 principal created an interface on a client-2 CI")
	}

	// Moving the CI to client 2 moves its interface, address and edge.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, own, f.Client2); err != nil {
		t.Fatalf("move CI: %v", err)
	}
	if err := f.Admin.QueryRow(bg, `
		SELECT (SELECT client_id::text FROM network_interface WHERE id = $1),
		       (SELECT client_id::text FROM ip_address WHERE interface_id = $1),
		       (SELECT source_client_id::text FROM ci_relationship WHERE id = $2)`,
		nicID, edgeID).Scan(&nicClient, &ipClient, &srcClient); err != nil {
		t.Fatalf("read moved scope: %v", err)
	}
	if nicClient != f.Client2 || ipClient != f.Client2 || srcClient != f.Client2 {
		t.Fatalf("after move: interface=%s ip=%s edge source=%s, want client 2", nicClient, ipClient, srcClient)
	}
	if n := count(`SELECT count(*) FROM network_interface WHERE id = $1`, nicID); n != 0 {
		t.Fatal("client-1 principal still sees the interface of the moved CI")
	}
}

// TestCIChildEdgeToOrgWideCI covers decision E-62, option 2 (WP-345
// follow-up, migration 000142; F-182, probe_edge_orgwide.sql): a client- or
// site-scoped principal creates and changes an edge, a suppression and an
// interface-level edge between its own CI and a visible org-wide CI (client
// or site NULL). The edge carries the client (site) of the own endpoint and
// is visible only there: a client-2 principal sees it in neither the
// database, the REST API, GraphQL, topology nor impact; it follows a move of
// the own CI. Edges between two org-wide CIs stay reserved to org-wide
// principals (E-09), an endpoint outside the scope is refused as before, an
// interface on an org-wide CI stays an org-wide row and is refused, and a
// suppression naming a CI of another organization is refused.
func TestCIChildEdgeToOrgWideCI(t *testing.T) {
	w := newT1World(t, "05")
	f := w.f
	bg := context.Background()
	own := f.CI(t, f.OrgA, f.Client1, "e62-own")
	orgWideCI := f.CI(t, f.OrgA, "", "e62-org-wide")
	orgWide2 := f.CI(t, f.OrgA, "", "e62-org-wide-2")
	foreign := f.CI(t, f.OrgA, f.Client2, "e62-client-2")
	otherOrg := f.CI(t, f.OrgB, "", "e62-org-b")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatalf("relationship type: %v", err)
	}

	scope := func(clients, sites []string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, w.userA)
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return &s
	}
	client1, client2, orgWide := scope([]string{f.Client1}, nil), scope([]string{f.Client2}, nil), scope(nil, nil)
	exec := func(s *database.TenantScope, query string, args ...any) (int64, error) {
		var n int64
		err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, query, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	insert := func(s *database.TenantScope, query string, args ...any) (string, error) {
		var id string
		err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query+` RETURNING id::text`, args...).Scan(&id)
		})
		return id, err
	}
	count := func(s *database.TenantScope, query string, args ...any) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&n)
		}); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		return n
	}
	edgeScope := func(table, id string) string {
		return w.scalar(`SELECT COALESCE(source_client_id::text, '-') || '>' || COALESCE(target_client_id::text, '-') FROM `+table+` WHERE id = $1`, id)
	}
	if n := count(client1, `SELECT count(*) FROM ci WHERE id IN ($1, $2)`, orgWideCI, orgWide2); n != 2 {
		t.Fatalf("setup: client-1 principal sees %d of the 2 org-wide CIs", n)
	}
	const insertEdge = `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'connected_to')`
	const insertSuppression = `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id) VALUES ($1, $2, $3, $4)`

	// Database level: own CI <-> org-wide CI in both directions, a
	// suppression and an interface-level edge to an interface of the
	// org-wide CI (created by the org-wide principal).
	var dbEdges []string
	for _, tc := range []struct{ name, src, dst, want string }{
		{"edge own -> org-wide", own, orgWideCI, f.Client1 + ">-"},
		{"edge org-wide -> own", orgWideCI, own, "->" + f.Client1},
	} {
		id, err := insert(client1, insertEdge, f.OrgA, tc.src, tc.dst)
		if err != nil {
			t.Errorf("E-62 option 2: client-1 principal, %s: %v, want success", tc.name, err)
			continue
		}
		if got := edgeScope("ci_relationship", id); got != tc.want {
			t.Errorf("%s: derived client scope %s, want %s", tc.name, got, tc.want)
		}
		dbEdges = append(dbEdges, id)
	}
	suppression, err := insert(client1, insertSuppression, f.OrgA, own, orgWideCI, typeID)
	if err != nil {
		t.Errorf("E-62 option 2: client-1 principal, suppression own -> org-wide: %v, want success", err)
	} else if got := edgeScope("relationship_suppression", suppression); got != f.Client1+">-" {
		t.Errorf("suppression: derived client scope %s, want client 1 > NULL", got)
	}
	nicOrgWide, err := insert(orgWide, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'e62-ow-eth0')`, f.OrgA, orgWideCI)
	if err != nil {
		t.Fatalf("setup: org-wide interface: %v", err)
	}
	nicOwn, err := insert(client1, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'e62-own-eth0')`, f.OrgA, own)
	if err != nil {
		t.Fatalf("setup: own interface as client 1: %v", err)
	}
	ifEdge, err := insert(client1, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source_interface_id, target_interface_id)
		VALUES ($1, $2, $3, 'connected_to', $4, $5)`, f.OrgA, own, orgWideCI, nicOwn, nicOrgWide)
	if err != nil {
		t.Errorf("E-62 option 2: client-1 principal, interface edge own eth0 -> org-wide eth0: %v, want success", err)
	} else {
		dbEdges = append(dbEdges, ifEdge)
	}
	if len(dbEdges) > 0 {
		if n, err := exec(client1, `UPDATE ci_relationship SET notes = 'e62' WHERE id = $1`, dbEdges[0]); err != nil || n != 1 {
			t.Errorf("client-1 principal updates its edge to the org-wide CI: %d rows (err %v), want 1", n, err)
		}
	}

	// Refused: two org-wide endpoints (E-09), an endpoint of client 2, the
	// mirror case for a client-2 principal, an interface on the org-wide CI
	// (an org-wide row) and a suppression naming a CI of organization B.
	for _, tc := range []struct {
		name  string
		scope *database.TenantScope
		query string
		args  []any
	}{
		{"client 1, edge org-wide -> org-wide", client1, insertEdge, []any{f.OrgA, orgWideCI, orgWide2}},
		{"client 1, suppression org-wide -> org-wide", client1, insertSuppression, []any{f.OrgA, orgWideCI, orgWide2, typeID}},
		{"client 1, edge own -> client 2", client1, insertEdge, []any{f.OrgA, own, foreign}},
		{"client 1, edge client 2 -> org-wide", client1, insertEdge, []any{f.OrgA, foreign, orgWideCI}},
		{"client 2, edge client 2 -> client 1", client2, insertEdge, []any{f.OrgA, foreign, own}},
		{"client 1, interface on the org-wide CI", client1, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'e62-eth1')`, []any{f.OrgA, orgWideCI}},
	} {
		if _, err := exec(tc.scope, tc.query, tc.args...); !rlsDenied(err) {
			t.Errorf("%s: err %v, want 42501 row-level security", tc.name, err)
		}
	}
	if _, err := exec(client1, insertSuppression, f.OrgA, own, otherOrg, typeID); err == nil {
		t.Error("client-1 principal stored a suppression naming a CI of organization B")
	}
	orgWideEdge := w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": orgWideCI, "target_ci_id": orgWide2, "rel_type": "depends_on"})
	for _, s := range []*database.TenantScope{client1, client2} {
		if n, err := exec(s, `UPDATE ci_relationship SET notes = 'e62' WHERE id = $1`, orgWideEdge); err != nil || n != 0 {
			t.Errorf("E-09: scoped principal %v updates the org-wide edge: %d rows (err %v), want 0", s.Clients, n, err)
		}
	}

	// Visibility: client 1 and the org-wide principal see the edges and the
	// suppression, client 2 none of them, nor can it change or delete them.
	written := dbEdges
	if suppression != "" {
		written = append([]string{suppression}, dbEdges...)
	}
	visible := `SELECT (SELECT count(*) FROM ci_relationship WHERE id = ANY ($1::uuid[])) + (SELECT count(*) FROM relationship_suppression WHERE id = ANY ($1::uuid[]))`
	if n := count(client1, visible, written); n != len(written) {
		t.Errorf("client-1 principal sees %d of its %d edges/suppressions to the org-wide CI", n, len(written))
	}
	if n := count(orgWide, visible, written); n != len(written) {
		t.Errorf("org-wide principal sees %d of the %d edges/suppressions", n, len(written))
	}
	if n := count(client2, visible, written); n != 0 {
		t.Errorf("TEN-05: client-2 principal sees %d edges/suppressions of client 1 to the org-wide CI, want 0", n)
	}
	for _, q := range []string{`UPDATE ci_relationship SET notes = 'c2' WHERE id = ANY ($1::uuid[])`, `DELETE FROM ci_relationship WHERE id = ANY ($1::uuid[])`,
		`DELETE FROM relationship_suppression WHERE id = ANY ($1::uuid[])`} {
		if n, err := exec(client2, q, written); err != nil || n != 0 {
			t.Errorf("client-2 principal, %s: %d rows (err %v), want 0", q, n, err)
		}
	}

	// HTTP: POST /api/v1/relationships in both directions answers 201, a
	// forced delete of a discovery edge stores the suppression; refusals as
	// on the database level.
	scoped1 := w.as("client1", only(f.Client1), all())
	scoped2 := w.as("client2", only(f.Client2), all())
	var apiEdges []string
	for _, body := range []map[string]any{
		{"source_ci_id": own, "target_ci_id": orgWideCI, "rel_type": "depends_on"},
		{"source_ci_id": orgWideCI, "target_ci_id": own, "rel_type": "depends_on"},
	} {
		code, resp := w.do(scoped1, http.MethodPost, "/api/v1/relationships", body)
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(resp), &out)
		if code != http.StatusCreated || out.ID == "" {
			t.Errorf("E-62 option 2: POST /api/v1/relationships %v as client1: %d %s, want 201", body, code, trim(resp))
			continue
		}
		apiEdges = append(apiEdges, out.ID)
	}
	// A user POST cannot claim the discovery source (F-271, WP-583); the
	// discovery edge is written as the discovery path writes it.
	if code, resp := w.do(scoped1, http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": own, "target_ci_id": orgWideCI, "rel_type": "runs_on", "source": "discovery"}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(resp, `"field":"source"`) {
		t.Errorf("user POST of a discovery edge own -> org-wide as client1: %d %s, want 422 on source", code, trim(resp))
	}
	if discovered, err := insert(client1, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		VALUES ($1, $2, $3, 'runs_on', 'discovery')`, f.OrgA, own, orgWideCI); err != nil {
		t.Errorf("discovery edge own -> org-wide as client1: %v", err)
	} else if code, resp := w.do(scoped1, http.MethodDelete, "/api/v1/relationships/"+discovered+"?force=true", nil); code != http.StatusNoContent {
		t.Errorf("forced DELETE of the discovery edge as client1: %d %s, want 204", code, trim(resp))
	} else if got := w.scalar(`SELECT count(*)::text FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
		WHERE s.source_ci_id = $1 AND s.target_ci_id = $2 AND rt.key = 'runs_on' AND s.source_client_id = $3 AND s.target_client_id IS NULL`, own, orgWideCI, f.Client1); got != "1" {
		t.Errorf("suppression of the deleted discovery edge with client 1 > NULL: %s rows, want 1", got)
	}
	if code, resp := w.do(scoped1, http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": orgWideCI, "target_ci_id": orgWide2, "rel_type": "runs_on"}); !refused(code) {
		t.Errorf("E-09: POST /api/v1/relationships between two org-wide CIs as client1: %d %s, want 403/404", code, trim(resp))
	}
	// An endpoint outside the scope is refused like an unknown CI: 422 on
	// its field (F-145, WP-401).
	for _, tc := range []struct {
		p    *channelPrincipal
		body map[string]any
	}{
		{scoped1, map[string]any{"source_ci_id": own, "target_ci_id": foreign, "rel_type": "runs_on"}},
		{scoped2, map[string]any{"source_ci_id": foreign, "target_ci_id": own, "rel_type": "runs_on"}},
	} {
		if code, resp := w.do(tc.p, http.MethodPost, "/api/v1/relationships", tc.body); code != http.StatusUnprocessableEntity ||
			!strings.Contains(resp, `"field":"target_ci_id"`) {
			t.Errorf("POST /api/v1/relationships %v as %s: %d %s, want 422 on target_ci_id", tc.body, tc.p.kind, code, trim(resp))
		}
	}
	if n := w.scalar(`SELECT count(*)::text FROM ci_relationship WHERE rel_type = 'runs_on' AND (source_ci_id IN ($1, $2, $3) OR target_ci_id IN ($1, $2, $3))`, own, orgWide2, foreign); n != "0" {
		t.Errorf("refused writes left %s edges", n)
	}

	// Reading channels rooted at the org-wide CI (visible to both clients):
	// client 1 finds its own CI and the edges, client 2 neither.
	for _, ch := range []struct {
		name, method, path string
		body               any
	}{
		{"REST relationships", http.MethodGet, "/api/v1/cis/" + orgWideCI + "/relationships", nil},
		{"REST topology", http.MethodGet, "/api/v1/topology?root_ci_id=" + orgWideCI, nil},
		{"REST impact", http.MethodGet, "/api/v1/impact/" + orgWideCI, nil},
		{"GraphQL relationships", http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ relationships(ciId: %q) { id sourceCiId targetCiId } }`, orgWideCI)}},
		// Nodes and edges apart: from a root of depth 3 a topology may hold
		// 1,111 nodes, both together exceed the complexity budget (F-333).
		{"GraphQL topology nodes", http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ topology(rootCiId: %q, depth: 3) { nodes { id name } } }`, orgWideCI)}},
		{"GraphQL topology edges", http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ topology(rootCiId: %q, depth: 3) { edges { sourceCiId targetCiId } } }`, orgWideCI)}},
	} {
		for _, p := range []*channelPrincipal{scoped1, scoped2} {
			code, body := w.do(p, ch.method, ch.path, ch.body)
			if code != http.StatusOK {
				t.Errorf("%s as %s: %d %s", ch.name, p.kind, code, trim(body))
				continue
			}
			if p == scoped1 && !strings.Contains(body, own) {
				t.Errorf("%s as client1 does not show the own CI behind the edge: %s", ch.name, trim(body))
			}
			if p == scoped2 {
				for _, m := range append([]string{own, "e62-own"}, apiEdges...) {
					if strings.Contains(body, m) {
						t.Errorf("TEN-05/IMP-07: %s as client2 discloses %q of the client-1 edge: %s", ch.name, m, trim(body))
					}
				}
			}
		}
	}

	// A move of the own CI by a client-1/client-2 principal takes the edges
	// along (000141): afterwards client 2 sees them, client 1 no longer.
	mover := w.as("clients-1-2", only(f.Client1, f.Client2), all())
	if code, body := w.do(mover, http.MethodPatch, "/api/v1/cis/"+own, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
		t.Fatalf("move the own CI to client 2: %d %s", code, trim(body))
	}
	for _, id := range append(dbEdges, apiEdges...) {
		if got := edgeScope("ci_relationship", id); got != f.Client2+">-" && got != "->"+f.Client2 {
			t.Errorf("edge %s after the move: client scope %s, want client 2 at the moved endpoint", id, got)
		}
	}
	moved := append(written, apiEdges...)
	if n := count(client1, visible, moved); n != 0 {
		t.Errorf("client-1 principal still sees %d edges/suppressions of the moved CI", n)
	}
	if n := count(client2, visible, moved); n != len(moved) {
		t.Errorf("client-2 principal sees %d of the %d edges/suppressions of the moved CI", n, len(moved))
	}
}

// TestCIChildEdgeToOrgWideCISite is the site variant of E-62 option 2: a
// site-1 principal writes an edge between a CI of site 1 and a CI without
// site, a site-2 principal does not see it; an edge between two CIs without
// site and an edge to a CI of site 2 are refused.
func TestCIChildEdgeToOrgWideCISite(t *testing.T) {
	w := newT1World(t, "06")
	f := w.f
	bg := context.Background()
	s1, s2 := w.siteOfRoom(f.Room(t, f.Client1, "e62-s1")), w.siteOfRoom(f.Room(t, f.Client1, "e62-s2"))
	own := f.CI(t, f.OrgA, f.Client1, "e62s-own")
	other := f.CI(t, f.OrgA, f.Client1, "e62s-site-2")
	noSite := f.CI(t, f.OrgA, f.Client1, "e62s-no-site")
	noSite2 := f.CI(t, f.OrgA, f.Client1, "e62s-no-site-2")
	for ci, site := range map[string]string{own: s1, other: s2} {
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+ci, map[string]any{"location_id": site}); code != http.StatusOK {
			t.Fatalf("place CI: %d %s", code, trim(body))
		}
	}
	site1 := database.OrgWideScope(f.OrgA, w.userA)
	site1.Sites = database.ScopeIDs(s1)
	site2 := database.OrgWideScope(f.OrgA, w.userA)
	site2.Sites = database.ScopeIDs(s2)
	const insertEdge = `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'connected_to')`
	run := func(s *database.TenantScope, query string, args ...any) error {
		return database.WithTenant(bg, f.App, s, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		})
	}

	if err := run(&site1, insertEdge, f.OrgA, noSite, own); err != nil {
		t.Errorf("E-62 option 2: site-1 principal, edge without site -> site 1: %v, want success", err)
	}
	p1 := w.as("site1", all(), only(s1))
	code, resp := w.do(p1, http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": own, "target_ci_id": noSite, "rel_type": "depends_on"})
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(resp), &out)
	if code != http.StatusCreated || out.ID == "" {
		t.Errorf("E-62 option 2: POST /api/v1/relationships site 1 -> without site as site1: %d %s, want 201", code, trim(resp))
	} else if got := w.scalar(`SELECT COALESCE(source_site_id::text, '-') || '>' || COALESCE(target_site_id::text, '-') FROM ci_relationship WHERE id = $1`, out.ID); got != s1+">-" {
		t.Errorf("site edge: derived site scope %s, want site 1 > NULL", got)
	}
	for name, args := range map[string][]any{
		"edge without site -> without site": {f.OrgA, noSite, noSite2},
		"edge site 1 -> site 2":             {f.OrgA, own, other},
	} {
		if err := run(&site1, insertEdge, args...); !rlsDenied(err) {
			t.Errorf("site-1 principal, %s: err %v, want 42501 row-level security", name, err)
		}
	}
	if code, resp := w.do(p1, http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": noSite, "target_ci_id": noSite2, "rel_type": "depends_on"}); !refused(code) {
		t.Errorf("POST /api/v1/relationships between two CIs without site as site1: %d %s, want 403/404", code, trim(resp))
	}
	var seen int
	if err := database.WithTenant(bg, f.App, &site2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 OR target_ci_id = $1`, own).Scan(&seen)
	}); err != nil || seen != 0 {
		t.Errorf("site-2 principal sees %d edges of the site-1 CI (err %v), want 0", seen, err)
	}
	if err := database.WithTenant(bg, f.App, &site1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 OR target_ci_id = $1`, own).Scan(&seen)
	}); err != nil || seen != 2 {
		t.Errorf("site-1 principal sees %d of its 2 edges to the CI without site (err %v)", seen, err)
	}
}
