package relationship_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
