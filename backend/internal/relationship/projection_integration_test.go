package relationship_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestStructureIsProjected covers WP-079 (DB-05, REL-01, AST-05, CH27)
// against PostgreSQL:
//   - containment is ci.parent_ci_id; a CI cannot contain itself, directly
//     or through its parents;
//   - contains, mounted_in and located_in are listed as projected
//     relationships (from parent_ci_id, rack_mount, location) and cannot be
//     stored through the relationship API (422);
//   - topology and impact traverse the projected containment;
//   - migration 000089 turns stored contains rows into parent_ci_id and
//     quarantines the structural rows.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructureIsProjected(t *testing.T) {
	f := scopetest.Seed(t, "70")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	cis := ci.NewPGRepository(f.App)
	rels := relationship.NewPGRepository(f.App)

	chassis := f.CI(t, f.OrgA, f.Client1, "struct-chassis")
	blade1 := f.CI(t, f.OrgA, f.Client1, "struct-blade-1")
	blade2 := f.CI(t, f.OrgA, f.Client1, "struct-blade-2")
	setParent := func(child, parent string) error {
		_, err := cis.Update(ctx, f.OrgA, child, ci.UpdateRequest{ParentCIID: &parent})
		return err
	}
	for _, blade := range []string{blade1, blade2} {
		if err := setParent(blade, chassis); err != nil {
			t.Fatal(err)
		}
	}
	isParentViolation := func(err error) bool {
		var v *ci.ValidationError
		return errors.As(err, &v) && v.Violations[0].Field == "parent_ci_id"
	}
	if err := setParent(chassis, blade1); !isParentViolation(err) {
		t.Errorf("cycle chassis → blade → chassis: %v, want 422 on parent_ci_id", err)
	}
	if err := setParent(chassis, chassis); !isParentViolation(err) {
		t.Errorf("self containment: %v, want 422 on parent_ci_id", err)
	}

	// Location and rack mount of the chassis: the parent of a composition
	// is mounted, its children are not (RCK-01).
	tree := locations.NewPGRepository(f.App)
	newLocation := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "struct-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "struct-b1"})
	room := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "struct-r1"})
	rackID := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "struct-k1"})
	if _, err := f.Admin.Exec(bg, `INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u)
		VALUES ($1, $2, $3, 1, 1)`, f.OrgA, rackID, chassis); err != nil {
		t.Fatal(err)
	}
	if _, err := cis.Update(ctx, f.OrgA, chassis, ci.UpdateRequest{LocationID: &rackID}); err != nil {
		t.Fatal(err)
	}

	list, total, err := rels.List(ctx, f.OrgA, chassis, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	projected := map[string]relationship.Relationship{}
	contained := map[string]bool{}
	for _, r := range list {
		if !r.Projected {
			continue
		}
		projected[r.RelType] = r
		if r.RelType == "contains" && r.SourceCIID == chassis && r.Source == relationship.ProjectionSource {
			contained[r.TargetCIID] = true
		}
	}
	if !contained[blade1] || !contained[blade2] {
		t.Errorf("projected contains of the chassis: %+v", list)
	}
	if m := projected["mounted_in"]; m.SourceCIID != chassis || m.TargetLocationID != rackID || m.TargetCIID != "" {
		t.Errorf("projected mounted_in %+v", m)
	}
	if l := projected["located_in"]; l.SourceCIID != chassis || l.TargetLocationID != rackID {
		t.Errorf("projected located_in %+v", l)
	}
	if total != len(list) || len(list) != 4 {
		t.Errorf("relationships of the chassis: total %d, %+v", total, list)
	}
	again, _, _ := rels.List(ctx, f.OrgA, chassis, api.PaginationParams{Limit: 50})
	if len(again) != len(list) || again[0].ID != list[0].ID {
		t.Error("projected relationship ids are not stable")
	}

	// The relationship API refuses structural types.
	mux := chi.NewRouter()
	relationship.NewHandler(rels).RegisterRoutes(mux)
	topology.NewHandler(cis, rels).RegisterRoutes(mux)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rctx := tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(rctx))
		return w
	}
	for _, relType := range []string{"contains", "contained_by", "parent_of", "child_of", "mounted_in", "located_in"} {
		body := `{"source_ci_id":"` + chassis + `","target_ci_id":"` + blade2 + `","rel_type":"` + relType + `"}`
		if w := serve(http.MethodPost, "/api/v1/relationships", body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("POST %s: %d, want 422", relType, w.Code)
		}
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		VALUES ($1, $2, $3, 'contains')`, f.OrgA, chassis, blade2); err == nil {
		t.Error("the database stored a contains row")
	}

	// Topology and impact follow the projected containment.
	edges, err := rels.TraverseFrom(ctx, f.OrgA, chassis, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	reached := map[string]bool{}
	for _, e := range edges {
		if e.RelType == "contains" && e.SourceCIID == chassis {
			reached[e.TargetCIID] = true
		}
	}
	if !reached[blade1] || !reached[blade2] {
		t.Errorf("traversal from the chassis: %+v", edges)
	}
	w := serve(http.MethodGet, "/api/v1/cis/"+chassis+"/dependencies?direction=downstream", "")
	var deps topology.DependencyResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &deps) != nil {
		t.Fatalf("dependencies: %d %s", w.Code, w.Body.String())
	}
	impacted := map[string]bool{}
	for _, n := range deps.Nodes {
		impacted[n.ID] = true
	}
	if !impacted[blade1] || !impacted[blade2] {
		t.Errorf("downstream of the chassis: %+v, want both blades", deps.Nodes)
	}
}

// TestMigration89MovesContainment runs the data section of migration 000089
// on stored structural rows inside a rolled-back transaction.
func TestMigration89MovesContainment(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "71")
	bg := context.Background()
	parent := f.CI(t, f.OrgA, "", "mig89-parent")
	child := f.CI(t, f.OrgA, "", "mig89-child")
	other := f.CI(t, f.OrgA, "", "mig89-other")

	raw, err := os.ReadFile("../../migrations/000089_ci_parent_projection.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "WITH structural AS")
	end := strings.Index(text, "ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_not_structural")
	if start < 0 || end < start {
		t.Fatal("data section of 000089 not found")
	}

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", sql[:40], execErr)
		}
	}
	exec(`ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_not_structural`)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'contains'), ($1, $4, $2, 'located_in')`, f.OrgA, parent, child, other)
	exec(text[start:end])

	var got string
	if err = tx.QueryRow(bg, `SELECT COALESCE(parent_ci_id::text, '') FROM ci WHERE id = $1`, child).Scan(&got); err != nil || got != parent {
		t.Errorf("parent of the contained CI: %q, %v; want %s", got, err, parent)
	}
	var stored, quarantined int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1
		AND rel_type IN ('contains', 'located_in')`, f.OrgA).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE organization_id = $1 AND migration = '000089'`,
		f.OrgA).Scan(&quarantined); err != nil {
		t.Fatal(err)
	}
	if stored != 0 || quarantined != 2 {
		t.Errorf("structural rows: %d stored, %d quarantined; want 0 and 2", stored, quarantined)
	}
}

// TestMigration179RepairsParentCycles covers WP-574 (AST-05, DB-05; F-243)
// inside a rolled-back transaction. Migration 000089 moved stored contains
// rows into ci.parent_ci_id before it created the cycle trigger, so cyclic
// legacy rows became a parent_ci_id cycle. Migration 000179 breaks every
// cycle at its youngest CI: the parent_ci_id moves to migration_quarantine
// and is cleared. Acyclic containment, also below a cycle, stays unchanged.
func TestMigration179RepairsParentCycles(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "71")
	bg := context.Background()
	// A 2-cycle and a CI below it in org A, a 3-cycle in org B and an
	// acyclic chain in org A.
	names := map[string]string{}
	newCI := func(org, name string) string {
		id := f.CI(t, org, "", name)
		names[id] = name
		return id
	}
	a1, a2, tail := newCI(f.OrgA, "mig179-a1"), newCI(f.OrgA, "mig179-a2"), newCI(f.OrgA, "mig179-tail")
	b1, b2, b3 := newCI(f.OrgB, "mig179-b1"), newCI(f.OrgB, "mig179-b2"), newCI(f.OrgB, "mig179-b3")
	p, q, r := newCI(f.OrgA, "mig179-p"), newCI(f.OrgA, "mig179-q"), newCI(f.OrgA, "mig179-r")

	read := func(file string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	text89 := read("000089_ci_parent_projection.up.sql")
	start := strings.Index(text89, "WITH structural AS")
	end := strings.Index(text89, "ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_not_structural")
	if start < 0 || end < start {
		t.Fatal("data section of 000089 not found")
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.60s: %v", sql, execErr)
		}
	}
	// a2 and b2 are the youngest CIs of their cycles.
	for i, id := range []string{a1, b1, b3, tail, p, q, r, a2, b2} {
		exec(`UPDATE ci SET created_at = now() - make_interval(days => 10 - $2::int) WHERE id = $1`, id, i)
	}

	// Legacy rows as 000089 found them: its UPDATE ran before the trigger
	// existed.
	exec(`ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_not_structural`)
	exec(`ALTER TABLE ci DISABLE TRIGGER trg_ci_parent_cycle`)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'contains'), ($1, $3, $2, 'contains'), ($1, $2, $4, 'contains'),
		($1, $5, $6, 'contains'), ($1, $6, $7, 'contains')`, f.OrgA, a1, a2, tail, p, q, r)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'contains'), ($1, $4, $3, 'child_of'), ($1, $4, $2, 'contains')`, f.OrgB, b1, b2, b3)
	exec(text89[start:end])
	exec(`ALTER TABLE ci ENABLE TRIGGER trg_ci_parent_cycle`)

	ids := []string{a1, a2, tail, b1, b2, b3, p, q, r}
	parents := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, id := range ids {
			var parent string
			if scanErr := tx.QueryRow(bg, `SELECT COALESCE(parent_ci_id::text, '') FROM ci WHERE id = $1`, id).Scan(&parent); scanErr != nil {
				t.Fatal(scanErr)
			}
			out[names[id]] = names[parent]
		}
		return out
	}
	// F-243: the legacy rows became parent_ci_id cycles.
	legacy := map[string]string{
		"mig179-a1": "mig179-a2", "mig179-a2": "mig179-a1", "mig179-tail": "mig179-a1",
		"mig179-b1": "mig179-b3", "mig179-b2": "mig179-b1", "mig179-b3": "mig179-b2",
		"mig179-p": "", "mig179-q": "mig179-p", "mig179-r": "mig179-q",
	}
	if got := parents(); !maps.Equal(got, legacy) {
		t.Fatalf("parents after 000089 = %v, want %v", got, legacy)
	}

	repairUp := read("000179_ci_parent_cycle_repair.up.sql")
	repairDown := read("000179_ci_parent_cycle_repair.down.sql")
	exec(repairUp)

	repaired := maps.Clone(legacy)
	repaired["mig179-a2"], repaired["mig179-b2"] = "", ""
	if got := parents(); !maps.Equal(got, repaired) {
		t.Errorf("parents after 000179 = %v, want %v", got, repaired)
	}
	// No CI reaches itself through its parents any more.
	var onCycle int
	if err = tx.QueryRow(bg, `
		WITH RECURSIVE up (start_id, id, depth) AS (
			SELECT id, parent_ci_id, 1 FROM ci WHERE id = ANY ($1::uuid[]) AND parent_ci_id IS NOT NULL
			UNION ALL
			SELECT up.start_id, c.parent_ci_id, up.depth + 1 FROM up JOIN ci c ON c.id = up.id
			WHERE c.parent_ci_id IS NOT NULL AND up.id <> up.start_id AND up.depth < 20
		)
		SELECT count(DISTINCT start_id) FROM up WHERE id = start_id`, ids).Scan(&onCycle); err != nil {
		t.Fatal(err)
	}
	if onCycle != 0 {
		t.Errorf("%d CIs still on a parent_ci_id cycle after 000179", onCycle)
	}

	quarantine := func() []string {
		t.Helper()
		rows, qErr := tx.Query(bg, `
			SELECT organization_id::text || ' ' || (row_data->>'id') || ' ' || (row_data->>'parent_ci_id')
			       || ' ' || jsonb_array_length(row_data->'cycle') || ' ' || (row_data->'cycle'->>0)
			FROM migration_quarantine
			WHERE migration = '000179' AND source_table = 'ci' AND organization_id IN ($1, $2)
			ORDER BY 1`, f.OrgA, f.OrgB)
		if qErr != nil {
			t.Fatal(qErr)
		}
		got, qErr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qErr != nil {
			t.Fatal(qErr)
		}
		return got
	}
	wantQuarantine := []string{
		f.OrgA + " " + a2 + " " + a1 + " 2 " + a2,
		f.OrgB + " " + b2 + " " + b1 + " 3 " + b2,
	}
	slices.Sort(wantQuarantine)
	if got := quarantine(); !slices.Equal(got, wantQuarantine) {
		t.Errorf("quarantine of 000179 = %v, want %v", got, wantQuarantine)
	}

	// The down migration keeps the repair and the quarantined rows; a
	// second up finds no cycle.
	exec(repairDown)
	exec(repairUp)
	if got := parents(); !maps.Equal(got, repaired) {
		t.Errorf("parents after down and up again = %v, want %v", got, repaired)
	}
	if got := quarantine(); !slices.Equal(got, wantQuarantine) {
		t.Errorf("quarantine after down and up again = %v, want %v", got, wantQuarantine)
	}
}
