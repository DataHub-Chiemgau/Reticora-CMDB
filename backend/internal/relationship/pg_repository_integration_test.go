package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// These tests run against the migrated CI PostgreSQL (see the migrations CI
// job). They are skipped locally unless TEST_DATABASE_URL is set.
func testPool(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// TestTraverseFromProjectsRelTypePG is the regression test for the verified
// defect: the recursive walk CTE did not project rel_type/source, so every
// neighbors/impact query failed with SQLSTATE 42703. It builds a 3-node chain
// with a cycle and asserts a multi-hop traversal returns typed edges.
func TestTraverseFromProjectsRelTypePG(t *testing.T) {
	dsn := testPool(t)
	ctx := context.Background()
	// The fixture creates its own organization, which the tenant-scoped
	// application role cannot do before app.org_id exists; the repository under
	// test sets the tenant itself on every call.
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgID := "11111111-1111-1111-1111-111111111111"
	if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'traverse-org', 'traverse-org') ON CONFLICT (id) DO NOTHING`, orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM organization WHERE id = $1`, orgID)
	})
	// Repeat runs share the org and its CIs; clear the graph first.
	if _, err := pool.Exec(ctx, `DELETE FROM ci_relationship WHERE organization_id = $1`, orgID); err != nil {
		t.Fatalf("reset relationships: %v", err)
	}

	ciIDs := []string{
		"22222222-2222-2222-2222-222222222221",
		"22222222-2222-2222-2222-222222222222",
		"22222222-2222-2222-2222-222222222223",
	}
	var typeID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatalf("lookup server ci_type: %v", err)
	}
	for _, id := range ciIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO ci (id, organization_id, ci_type_id, name, status) VALUES ($1, $2, $3, $4, 'active') ON CONFLICT (id) DO NOTHING`, id, orgID, typeID, "ci-"+id[len(id)-2:]); err != nil {
			t.Fatalf("insert ci: %v", err)
		}
	}

	// The repository takes the tenant scope from the request context.
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	repo := relationship.NewPGRepository(pool)
	mk := func(src, dst, typ string) {
		r := &relationship.Relationship{
			OrganizationID: orgID,
			SourceCIID:     src,
			TargetCIID:     dst,
			RelType:        typ,
		}
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("create relationship %s->%s: %v", src, dst, err)
		}
	}
	mk(ciIDs[0], ciIDs[1], "connected_to")
	mk(ciIDs[1], ciIDs[2], "connected_to")
	mk(ciIDs[2], ciIDs[0], "depends_on") // cycle back to the root

	rels, err := repo.TraverseFrom(ctx, orgID, ciIDs[0], 3, 100)
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("expected 3 relationships within depth 3 incl. cycle, got %d: %+v", len(rels), rels)
	}
	types := map[string]bool{}
	for _, rel := range rels {
		if rel.RelType == "" {
			t.Errorf("relationship %s has empty rel_type", rel.ID)
		}
		types[rel.RelType] = true
	}
	if !types["connected_to"] || !types["depends_on"] {
		t.Errorf("expected both rel_types projected, got %v", types)
	}

	// Depth 1 from the root must only return the directly attached edges.
	relsD1, err := repo.TraverseFrom(ctx, orgID, ciIDs[0], 1, 100)
	if err != nil {
		t.Fatalf("traverse depth 1: %v", err)
	}
	if len(relsD1) != 2 {
		t.Fatalf("expected 2 edges at depth 1 (chain start + cycle edge), got %d", len(relsD1))
	}
}

// TestRelationshipListOfACIListsEachEdgeOnce covers List for one CI
// (WP-609, one arm per end of the edge) against PostgreSQL: the stored
// edges from and to the CI and its edge to itself, the projected contains
// to its parent and from its child, located_in and mounted_in, each once,
// with total equal to the edges; pages of three partition them in the
// order of the list. A principal of client 1 sees neither the edges to a
// CI of client 2 nor them in the total. Other edges are not listed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipListOfACIListsEachEdgeOnce(t *testing.T) {
	f := scopetest.Seed(t, "06")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := relationship.NewPGRepository(f.App)
	names := map[string]string{}
	newCI := func(clientID, name string) string {
		id := f.CI(t, f.OrgA, clientID, name)
		names[id] = name
		return id
	}
	self, other, parent, child, mounted := newCI(f.Client1, "self"), newCI(f.Client1, "other"),
		newCI(f.Client1, "parent"), newCI(f.Client1, "child"), newCI(f.Client1, "mounted")
	foreign := newCI(f.Client2, "client2")
	room := f.Room(t, f.Client1, "list-room")
	names[room] = "room"
	var rack string
	if err := f.Admin.QueryRow(bg, `INSERT INTO rack (organization_id, room_id, name, height_units) VALUES ($1, $2, 'list-rack', 42)
		RETURNING id::text`, f.OrgA, room).Scan(&rack); err != nil {
		t.Fatal(err)
	}
	names[rack] = "rack"
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE ci SET parent_ci_id = $1, location_id = $2 WHERE id = $3`, []any{parent, room, self}},
		{`UPDATE ci SET parent_ci_id = $1 WHERE id = $2`, []any{self, child}},
		{`UPDATE ci SET location_id = $1 WHERE id = $2`, []any{rack, mounted}},
		{`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u) VALUES ($1, $2, $3, 1, 1)`, []any{f.OrgA, rack, mounted}},
	} {
		if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
			t.Fatalf("%s: %v", s.sql, err)
		}
	}
	for _, e := range [][3]string{
		{self, other, "depends_on"}, {other, self, "depends_on"}, {self, self, "runs_on"},
		{foreign, self, "connected_to"}, {self, foreign, "depends_on"}, {other, foreign, "depends_on"},
	} {
		if err := repo.Create(ctx, &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: e[0], TargetCIID: e[1], RelType: e[2]}); err != nil {
			t.Fatalf("create %s %s -> %s: %v", e[2], names[e[0]], names[e[1]], err)
		}
	}
	edge := func(r relationship.Relationship) string {
		target := names[r.TargetCIID]
		if r.TargetCIID == "" {
			target = "@" + names[r.TargetLocationID]
		}
		return r.RelType + " " + names[r.SourceCIID] + "->" + target
	}
	list := func(lctx context.Context, ciID string, page api.PaginationParams) ([]string, int) {
		t.Helper()
		rels, total, err := repo.List(lctx, f.OrgA, ciID, page)
		if err != nil {
			t.Fatalf("list %s: %v", names[ciID], err)
		}
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = edge(r)
		}
		return out, total
	}

	for _, c := range []struct {
		name string
		ctx  context.Context
		ci   string
		want []string
	}{
		{"org-wide", ctx, self, []string{"depends_on self->other", "depends_on other->self", "runs_on self->self",
			"connected_to client2->self", "depends_on self->client2", "contains parent->self", "contains self->child", "located_in self->@room"}},
		{"client 1", f.ClientCtx(f.Client1), self, []string{"depends_on self->other", "depends_on other->self", "runs_on self->self",
			"contains parent->self", "contains self->child", "located_in self->@room"}},
		{"contains: source", ctx, parent, []string{"contains parent->self"}},
		{"contains: target", ctx, child, []string{"contains self->child"}},
		{"mounted CI", ctx, mounted, []string{"mounted_in mounted->@rack", "located_in mounted->@rack"}},
	} {
		got, total := list(c.ctx, c.ci, api.PaginationParams{Limit: 50})
		sorted, want := slices.Clone(got), slices.Clone(c.want)
		slices.Sort(sorted)
		slices.Sort(want)
		if !slices.Equal(sorted, want) || total != len(want) {
			t.Errorf("%s: relationships of %s: total %d, %q; want %d, %q", c.name, names[c.ci], total, got, len(want), c.want)
		}
		var paged []string
		for offset := 0; offset < len(got)+3; offset += 3 {
			page, pageTotal := list(c.ctx, c.ci, api.PaginationParams{Limit: 3, Offset: offset})
			if pageTotal != total {
				t.Errorf("%s: total %d at offset %d, want %d", c.name, pageTotal, offset, total)
			}
			paged = append(paged, page...)
		}
		if !slices.Equal(paged, got) {
			t.Errorf("%s: pages of three %q, want the list %q", c.name, paged, got)
		}
	}
}

// TestRelationshipEdgesFollowREL02 covers REL-02 (WP-128) against
// PostgreSQL: interface endpoints (interfaces of the edge's CIs), the edge
// is unique per (org, source, target, type, interfaces), confidence is a
// column limited to 0–1 and takes over attributes.confidence (migration
// 000114), last_confirmed_at is set, verifying records verified_by and
// verified_at; indexes (org, source) and (org, target) exist.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipEdgesFollowREL02(t *testing.T) {
	f := scopetest.Seed(t, "91")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "rel02-verifier")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	sw1, sw2 := f.CI(t, f.OrgA, "", "rel02-sw1"), f.CI(t, f.OrgA, "", "rel02-sw2")
	iface := func(ciID, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, ciID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sw1p1, sw1p2, sw2p1 := iface(sw1, "Gi1/0/1"), iface(sw1, "Gi1/0/2"), iface(sw2, "Gi1/0/1")

	var defs []string
	if err := f.Admin.QueryRow(bg, `SELECT array_agg(d) FROM (
			SELECT indexdef AS d FROM pg_indexes WHERE tablename = 'ci_relationship'
			UNION ALL SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'ci_relationship'::regclass) x`).Scan(&defs); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(defs, "\n")
	for _, want := range []string{
		"(organization_id, source_ci_id, target_ci_id, rel_type, COALESCE(source_interface_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(target_interface_id, '00000000-0000-0000-0000-000000000000'::uuid))",
		"USING btree (organization_id, source_ci_id)",
		"USING btree (organization_id, target_ci_id)",
		"CHECK (((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric))))",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}

	mux := chi.NewRouter()
	relationship.NewHandler(relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	edge := func(src, dst string) string {
		body := `{"source_ci_id":"` + sw1 + `","target_ci_id":"` + sw2 + `","rel_type":"connected_to","confidence":0.9`
		if src != "" {
			body += `,"source_interface_id":"` + src + `"`
		}
		if dst != "" {
			body += `,"target_interface_id":"` + dst + `"`
		}
		return body + "}"
	}
	w := call(http.MethodPost, "/api/v1/relationships", edge(sw1p1, sw2p1))
	var first relationship.Relationship
	_ = json.Unmarshal(w.Body.Bytes(), &first)
	if w.Code != http.StatusCreated || first.SourceInterfaceID != sw1p1 || first.TargetInterfaceID != sw2p1 ||
		first.VerifiedBy != user || first.VerifiedAt == "" || first.LastConfirmedAt == "" || first.Confidence == nil || *first.Confidence != 0.9 {
		t.Fatalf("manual edge with ports: %d %s", w.Code, w.Body.String())
	}
	// Ordered: "again" cases depend on the case before them.
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"other port pair", edge(sw1p2, sw2p1), http.StatusCreated},
		{"same ports again", edge(sw1p1, sw2p1), http.StatusConflict},
		{"without ports", edge("", ""), http.StatusCreated},
		{"without ports again", edge("", ""), http.StatusConflict},
		{"port of another CI", edge(sw2p1, ""), http.StatusUnprocessableEntity},
		{"confidence above 1", strings.Replace(edge(sw1p2, ""), "0.9", "1.5", 1), http.StatusBadRequest},
	} {
		if got := call(http.MethodPost, "/api/v1/relationships", tc.body); got.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, got.Code, got.Body.String(), tc.status)
		}
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci_relationship SET confidence = 1.5 WHERE id = $1`, first.ID); err == nil {
		t.Error("the database accepted confidence 1.5")
	}

	// Verification: leaving the verified state clears the actor, verifying
	// records it again.
	w = call(http.MethodPatch, "/api/v1/relationships/"+first.ID, `{"verification_state":"disputed"}`)
	var disputed relationship.Relationship
	_ = json.Unmarshal(w.Body.Bytes(), &disputed)
	if w.Code != http.StatusOK || disputed.VerifiedBy != "" || disputed.VerifiedAt != "" {
		t.Errorf("disputed: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPatch, "/api/v1/relationships/"+first.ID, `{"verification_state":"verified"}`)
	var verified relationship.Relationship
	_ = json.Unmarshal(w.Body.Bytes(), &verified)
	if verified.VerifiedBy != user || verified.VerifiedAt == "" || verified.SourceInterfaceID != sw1p1 {
		t.Errorf("verified again: %s", w.Body.String())
	}

	// Data transfer of migration 000114: attributes.confidence becomes the
	// column.
	t.Run("data transfer", func(t *testing.T) {
		down, err := os.ReadFile("../../migrations/000114_relationship_rel02.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		up, err := os.ReadFile("../../migrations/000114_relationship_rel02.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err = tx.Exec(bg, `DELETE FROM ci_relationship WHERE organization_id = $1`, f.OrgA); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(bg, string(down)); err != nil {
			t.Fatalf("down: %v", err)
		}
		if _, err = tx.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, attributes)
			VALUES ($1, $2, $3, 'uplink_to', 'discovery', '{"confidence": 0.6, "port": "x"}')`, f.OrgA, sw1, sw2); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(bg, string(up)); err != nil {
			t.Fatalf("up: %v", err)
		}
		var confidence float64
		var attrs string
		if err = tx.QueryRow(bg, `SELECT confidence, attributes::text FROM ci_relationship WHERE organization_id = $1 AND rel_type = 'uplink_to'`,
			f.OrgA).Scan(&confidence, &attrs); err != nil || confidence != 0.6 || attrs != `{"port": "x"}` {
			t.Errorf("after the transfer: confidence %v attributes %s (%v)", confidence, attrs, err)
		}
	})
}

// TestRelationshipEventsCommitWithTheEdge covers EVT-01/EVT-02 for the
// relationship repository (WP-400, F-043): Create, Delete and
// DeleteWithPolicy write relationship.created and relationship.removed in
// the transaction of the edge, with the edge as entity (entity_seq per
// edge) and its CIs in the data. A rolled back context transaction (the
// discovery ingest), a refused write (duplicate, CI of organization B,
// unknown CI, CI outside the client scope, discovery edge without force,
// organization B) and a repository without outbox write no event.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipEventsCommitWithTheEdge(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	c1a, c1b := f.CI(t, f.OrgA, f.Client1, "evt-c1a"), f.CI(t, f.OrgA, f.Client1, "evt-c1b")
	c2 := f.CI(t, f.OrgA, f.Client2, "evt-c2")
	foreign := f.CI(t, f.OrgB, "", "evt-foreign")
	outbox := func(org string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT type || ' ' || entity_id || ' ' || entity_seq || ' ' ||
				COALESCE(data->>'source_ci_id', '') || '->' || COALESCE(data->>'target_ci_id', '')
			FROM event_outbox WHERE organization_id = $1 ORDER BY sequence`, org)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	event := func(typ string, rel *relationship.Relationship, seq int) string {
		return fmt.Sprintf("%s %s %d %s->%s", typ, rel.ID, seq, rel.SourceCIID, rel.TargetCIID)
	}
	exists := func(id string) bool {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id::text = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	var want []string
	check := func(step string) {
		t.Helper()
		if got := outbox(f.OrgA); !slices.Equal(got, want) {
			t.Fatalf("%s: events of organization A\n got %q\nwant %q", step, got, want)
		}
	}

	committed := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1a, TargetCIID: c1b, RelType: "depends_on"}
	if err := repo.Create(ctx, committed); err != nil {
		t.Fatal(err)
	}
	want = append(want, event(events.RelationshipCreated, committed, 1))
	check("create")

	// A rolled back context transaction takes the edge and its event.
	errRollback := errors.New("roll back")
	rolledBack := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1b, TargetCIID: c1a, RelType: "depends_on"}
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := repo.Create(ctx, rolledBack); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("rolled back create: %v", err)
	}
	if rolledBack.ID == "" || exists(rolledBack.ID) {
		t.Fatalf("rolled back edge %q is stored", rolledBack.ID)
	}
	check("rolled back create")
	// A committed context transaction writes the event with the edge.
	joined := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1b, TargetCIID: c1a, RelType: "depends_on"}
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		return repo.Create(ctx, joined)
	}); err != nil {
		t.Fatal(err)
	}
	want = append(want, event(events.RelationshipCreated, joined, 1))
	check("create in a context transaction")

	// Refused writes.
	for _, tc := range []struct {
		name string
		ctx  context.Context
		rel  relationship.Relationship
	}{
		{"duplicate", ctx, relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1a, TargetCIID: c1b, RelType: "depends_on"}},
		{"CI of organization B", ctx, relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1a, TargetCIID: foreign, RelType: "depends_on"}},
		{"unknown CI", ctx, relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: f.ID(), TargetCIID: c1a, RelType: "depends_on"}},
		{"CI outside the client scope", f.ClientCtx(f.Client1), relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1a, TargetCIID: c2, RelType: "depends_on"}},
	} {
		if err := repo.Create(tc.ctx, &tc.rel); err == nil {
			t.Errorf("create with %s succeeded", tc.name)
		}
	}
	if err := repo.Delete(f.OrgCtx(f.OrgB), f.OrgB, committed.ID); err == nil {
		t.Error("organization B deleted A's edge")
	}
	if err := repo.Delete(f.ClientCtx(f.Client2), f.OrgA, committed.ID); err == nil {
		t.Error("a principal of client 2 deleted an edge of client 1")
	}
	if err := repo.DeleteWithPolicy(f.OrgCtx(f.OrgB), f.OrgB, committed.ID, true); err == nil {
		t.Error("organization B deleted A's edge by policy")
	}
	var discovered relationship.Relationship
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		VALUES ($1, $2, $3, 'connected_to', 'discovery') RETURNING id::text, source_ci_id::text, target_ci_id::text`,
		f.OrgA, c1a, c1b).Scan(&discovered.ID, &discovered.SourceCIID, &discovered.TargetCIID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteWithPolicy(ctx, f.OrgA, discovered.ID, false); !errors.Is(err, relationship.ErrForceRequired) {
		t.Errorf("discovery edge without force: %v", err)
	}
	// A delete in a rolled back context transaction keeps edge and silence.
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := repo.Delete(ctx, f.OrgA, committed.ID); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) || !exists(committed.ID) {
		t.Fatalf("rolled back delete: %v, edge kept %v", err, exists(committed.ID))
	}
	check("refused and rolled back writes")
	if n := len(outbox(f.OrgB)); n != 0 {
		t.Errorf("organization B has %d events", n)
	}

	// Deletes: the discovery edge with force (and its suppression), the
	// manual edges by policy and directly; the removal is the second event
	// of an edge created through the repository.
	if err := repo.DeleteWithPolicy(ctx, f.OrgA, discovered.ID, true); err != nil {
		t.Fatal(err)
	}
	want = append(want, event(events.RelationshipRemoved, &discovered, 1))
	if err := repo.DeleteWithPolicy(ctx, f.OrgA, joined.ID, false); err != nil {
		t.Fatal(err)
	}
	want = append(want, event(events.RelationshipRemoved, joined, 2))
	if err := repo.Delete(ctx, f.OrgA, committed.ID); err != nil {
		t.Fatal(err)
	}
	want = append(want, event(events.RelationshipRemoved, committed, 2))
	check("deletes")
	var payload struct {
		ID, RelType, Source string
	}
	if err := f.Admin.QueryRow(bg, `SELECT data->>'id', data->>'rel_type', data->>'source' FROM event_outbox
		WHERE organization_id = $1 AND type = 'relationship.removed' AND entity_id = $2`, f.OrgA, discovered.ID).Scan(&payload.ID, &payload.RelType, &payload.Source); err != nil ||
		payload != (struct{ ID, RelType, Source string }{discovered.ID, "connected_to", "discovery"}) {
		t.Errorf("relationship.removed data of the discovery edge: %+v, %v", payload, err)
	}

	// Without an outbox the repository writes no event.
	plain := relationship.NewPGRepository(f.App)
	silent := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: c1a, TargetCIID: c1b, RelType: "runs_on"}
	if err := plain.Create(ctx, silent); err != nil {
		t.Fatal(err)
	}
	if err := plain.Delete(ctx, f.OrgA, silent.ID); err != nil {
		t.Fatal(err)
	}
	check("repository without outbox")
}

// TestEdgesOfStaysInsideScope covers IMP-04 and IMP-07 (WP-513, F-160)
// for the read of the providers of a redundant type beyond the depth of a
// walk: EdgesOf returns the stored edges of the given types with an end
// among the given CIs, ordered by id, under the caller's scope only. An
// edge to a CI of another client is missing for a client-1 principal, an
// edge to a deleted CI for everyone; another organization reads nothing;
// without a tenant scope the read fails. WalkFrom reports when the node
// limit cut the walk (Walk.Truncated).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEdgesOfStaysInsideScope(t *testing.T) {
	f := scopetest.Seed(t, "06")
	bg := context.Background()
	server := f.CI(t, f.OrgA, f.Client1, "edges-of-server")
	pduA := f.CI(t, f.OrgA, f.Client1, "edges-of-pdu-a")
	pduB := f.CI(t, f.OrgA, f.Client1, "edges-of-pdu-b")
	foreign := f.CI(t, f.OrgA, f.Client2, "edges-of-pdu-client2")
	deleted := f.CI(t, f.OrgA, f.Client1, "edges-of-pdu-deleted")
	other := f.CI(t, f.OrgA, f.Client1, "edges-of-other")
	edge := map[string]string{}
	for name, e := range map[string][3]string{
		"a":       {server, pduA, "powered_by"},
		"b":       {server, pduB, "powered_by"},
		"foreign": {server, foreign, "powered_by"},
		"deleted": {server, deleted, "powered_by"},
		"hosted":  {server, other, "hosted_on"},
		"unrel":   {other, pduA, "powered_by"},
	} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, $4) RETURNING id::text`, f.OrgA, e[0], e[1], e[2]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		edge[name] = id
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	repo := relationship.NewPGRepository(f.App)
	read := func(ctx context.Context, org string) []string {
		t.Helper()
		rels, err := repo.EdgesOf(ctx, org, []string{server}, []string{"powered_by", "member_of_cluster"})
		if err != nil {
			t.Fatalf("edges of: %v", err)
		}
		out := make([]string, 0, len(rels))
		for i := range rels {
			if rels[i].SourceCIID != server || rels[i].RelType != "powered_by" {
				t.Errorf("edge %+v", rels[i])
			}
			out = append(out, rels[i].ID)
		}
		return out
	}
	want := func(names ...string) []string {
		ids := make([]string, 0, len(names))
		for _, n := range names {
			ids = append(ids, edge[n])
		}
		slices.Sort(ids)
		return ids
	}
	if got := read(f.OrgCtx(f.OrgA), f.OrgA); !slices.Equal(got, want("a", "b", "foreign")) {
		t.Errorf("org-wide: %v, want %v", got, want("a", "b", "foreign"))
	}
	if got := read(f.ClientCtx(f.Client1), f.OrgA); !slices.Equal(got, want("a", "b")) {
		t.Errorf("client 1: %v, want %v (no edge to client 2)", got, want("a", "b"))
	}
	if got := read(f.OrgCtx(f.OrgB), f.OrgB); len(got) != 0 {
		t.Errorf("organization B: %v", got)
	}
	// The walk reports whether the node limit cut it: the impact then
	// reads the feeds of every dependent.
	for maxNodes, want := range map[int]bool{2: true, relationship.MaxTraversalNodes: false} {
		walk, err := repo.WalkFrom(f.ClientCtx(f.Client1), f.OrgA, server, 2, maxNodes)
		if err != nil || walk.Truncated != want {
			t.Errorf("walk of at most %d CIs: truncated %v (%v), want %v", maxNodes, walk != nil && walk.Truncated, err, want)
		}
	}
	if rels, err := repo.EdgesOf(f.OrgCtx(f.OrgA), f.OrgA, nil, []string{"powered_by"}); err != nil || len(rels) != 0 {
		t.Errorf("no CIs: %v %v", rels, err)
	}
	if _, err := repo.EdgesOf(bg, f.OrgA, []string{server}, []string{"powered_by"}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("without scope: %v, want ErrNoTenantScope", err)
	}
}
