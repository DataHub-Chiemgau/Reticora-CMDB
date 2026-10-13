package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// flags reduces an impact result to the heuristic flag per CI.
func flags(hits map[string]impactHit) map[string]bool {
	out := map[string]bool{}
	for id, hit := range hits {
		out[id] = hit.heuristic
	}
	return out
}

var testRules = func() map[string]impactRule {
	return (&Handler{}).impactRules(context.Background(), "org-1")
}()

// TestComputeImpactFollowsImpactDirection covers IMP-01 (WP-176): device
// powered_by pdu and vm hosted_on server read "source depends on target",
// so the failure of the pdu reaches the server and over it the vm, never
// the other way round.
func TestComputeImpactFollowsImpactDirection(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "app", TargetCIID: "vm", RelType: "runs_on"},
		{ID: "e4", SourceCIID: "server", TargetCIID: "monitor", RelType: "monitored_by"},
	}
	all := flags(computeImpact("pdu", edges, impactQuery{}, testRules))
	for _, id := range []string{"server", "vm", "app"} {
		if heuristic, ok := all[id]; !ok || heuristic {
			t.Errorf("%s: impacted %v, heuristic %v", id, ok, heuristic)
		}
	}
	if _, ok := all["monitor"]; ok || len(all) != 3 {
		t.Errorf("impact %v: the monitoring CI does not depend on the server", all)
	}
	// The failure of a dependent never reaches what it depends on.
	if got := flags(computeImpact("vm", edges, impactQuery{}, testRules)); len(got) != 1 {
		t.Errorf("failure of the vm: %v, want only the app", got)
	}
	if got := flags(computeImpact("server", edges, impactQuery{}, testRules)); len(got) != 2 {
		t.Errorf("failure of the server: %v, want vm and app", got)
	}
	// target_depends_on_source: the contained CI depends on its container.
	contains := []Edge{{ID: "c", SourceCIID: "chassis", TargetCIID: "blade", RelType: "contains"}}
	if got := flags(computeImpact("chassis", contains, impactQuery{}, testRules)); len(got) != 1 {
		t.Errorf("contains: %v", got)
	}
	if got := flags(computeImpact("blade", contains, impactQuery{}, testRules)); len(got) != 0 {
		t.Errorf("contains backwards: %v", got)
	}
}

// TestComputeImpactConnectedToIsHeuristic covers IMP-03: connected_to is
// undirected and propagates both ways, but only as a marked heuristic; a CI
// that a directed path reaches as well is not heuristic.
func TestComputeImpactConnectedToIsHeuristic(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "switch", TargetCIID: "server", RelType: "connected_to"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "router", TargetCIID: "switch", RelType: "connected_to"},
		{ID: "e4", SourceCIID: "server2", TargetCIID: "switch", RelType: "uplink_to"},
	}
	got := flags(computeImpact("switch", edges, impactQuery{}, testRules))
	want := map[string]bool{"server": true, "vm": true, "router": true, "server2": false}
	if len(got) != len(want) {
		t.Fatalf("impact %v, want %v", got, want)
	}
	for id, heuristic := range want {
		if h, ok := got[id]; !ok || h != heuristic {
			t.Errorf("%s: impacted %v heuristic %v, want heuristic %v", id, ok, h, heuristic)
		}
	}
	// Undirected: the failure of the server reaches the switch as well.
	if got = flags(computeImpact("server", edges, impactQuery{}, testRules)); !got["switch"] {
		t.Errorf("connected_to backwards: %v", got)
	}
	// The rel_type facet keeps one class.
	if got = flags(computeImpact("switch", edges, impactQuery{relType: "uplink_to"}, testRules)); len(got) != 1 || got["server2"] {
		t.Errorf("uplink facet: %v", got)
	}
}

// TestImpactRulesUseOrganizationTypes: a custom type follows its own
// impact_direction; none does not propagate.
func TestImpactRulesUseOrganizationTypes(t *testing.T) {
	types := relationshiptype.NewMemoryRepository()
	if err := types.Create(context.Background(), &relationshiptype.Type{OrganizationID: "org-1", Key: "feeds", DisplayName: "feeds",
		Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactTargetDependsOnSource}); err != nil {
		t.Fatal(err)
	}
	if err := types.Create(context.Background(), &relationshiptype.Type{OrganizationID: "org-1", Key: "documents", DisplayName: "documents",
		Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactNone}); err != nil {
		t.Fatal(err)
	}
	rules := (&Handler{types: types}).impactRules(context.Background(), "org-1")
	edges := []Edge{
		{ID: "e1", SourceCIID: "a", TargetCIID: "b", RelType: "feeds"},
		{ID: "e2", SourceCIID: "a", TargetCIID: "c", RelType: "documents"},
	}
	if got := flags(computeImpact("a", edges, impactQuery{}, rules)); len(got) != 1 || got["b"] {
		t.Errorf("custom types: %v", got)
	}
}

// TestImpactSkipsDeletedCIs covers CI-05: a deleted CI is no node of the
// impact walk, so what is only reachable through it is not impacted
// either.
func TestImpactSkipsDeletedCIs(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	mux := chi.NewRouter()
	NewHandler(ciRepo, relRepo).RegisterRoutes(mux)
	impacted := func() map[string]bool {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+ids["pdu1"]))
		var body struct {
			Impacted []Node `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("impact: %d %s", w.Code, w.Body.String())
		}
		out := map[string]bool{}
		for _, n := range body.Impacted {
			out[n.ID] = true
		}
		return out
	}
	// srv1 powered_by pdu1, sw1 connected_to srv1 (heuristic).
	if before := impacted(); !before[ids["srv1"]] || !before[ids["sw1"]] {
		t.Fatalf("seed graph: impacted %v", before)
	}
	if err := ciRepo.Delete(context.Background(), "org-1", ids["srv1"]); err != nil {
		t.Fatal(err)
	}
	if after := impacted(); len(after) != 0 {
		t.Errorf("impact passes through a deleted CI: %v", after)
	}
}

// TestComputeImpactPathsAndCategories covers IMP-02 (WP-177): every
// impacted CI carries its shortest path with the type sequence; categories
// narrow the walk; the walk stops after the depth.
func TestComputeImpactPathsAndCategories(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "app", TargetCIID: "vm", RelType: "runs_on"},
		// A longer way to the app: app depends on the server directly too.
		{ID: "e4", SourceCIID: "app", TargetCIID: "server", RelType: "depends_on"},
		{ID: "e5", SourceCIID: "pdu", TargetCIID: "blade", RelType: "contains"},
	}
	hits := computeImpact("pdu", edges, impactQuery{depth: maxDepth}, testRules)
	app := hits["app"]
	if len(app.path) != 2 || app.path[0].RelType != "powered_by" || app.path[1].RelType != "depends_on" ||
		app.path[0].FromCIID != "pdu" || app.path[1].ToCIID != "app" {
		t.Errorf("shortest path to the app: %+v", app.path)
	}
	if blade := hits["blade"]; len(blade.path) != 1 || blade.path[0].Category != "physical" {
		t.Errorf("projected containment: %+v", blade.path)
	}
	power := computeImpact("pdu", edges, impactQuery{categories: map[string]bool{"power": true}, depth: maxDepth}, testRules)
	if len(power) != 1 || len(power["server"].path) != 1 {
		t.Errorf("power category: %v", power)
	}
	physical := computeImpact("pdu", edges, impactQuery{categories: map[string]bool{"physical": true}, depth: maxDepth}, testRules)
	if _, ok := physical["blade"]; !ok || len(physical) != 1 {
		t.Errorf("physical category: %v", physical)
	}
	if one := computeImpact("pdu", edges, impactQuery{depth: 1}, testRules); len(one) != 2 {
		t.Errorf("depth 1: %v, want server and blade", one)
	}
	if _, err := parseCategories("power, physical"); err != nil {
		t.Error(err)
	}
	if _, err := parseCategories("power,cooling"); err == nil {
		t.Error("unknown category accepted")
	}
}

// TestComputeImpactTwentyHops: the walk reaches 20 hops, not 21 (IMP-02).
func TestComputeImpactTwentyHops(t *testing.T) {
	var edges []Edge
	for i := 0; i < 21; i++ {
		edges = append(edges, Edge{ID: fmt.Sprintf("e%02d", i), SourceCIID: fmt.Sprintf("n%02d", i+1), TargetCIID: fmt.Sprintf("n%02d", i), RelType: "hosted_on"})
	}
	hits := computeImpact("n00", edges, impactQuery{depth: maxDepth}, testRules)
	if len(hits) != 20 || len(hits["n20"].path) != 20 {
		t.Errorf("impact over 21 hops: %d CIs, path to n20 %d", len(hits), len(hits["n20"].path))
	}
	if _, ok := hits["n21"]; ok {
		t.Error("the 21st hop was reached")
	}
}

// countingCIs counts the reads of the CI repository during a graph walk.
type countingCIs struct {
	*ci.MemoryRepository
	single, batch int
}

func (c *countingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.single++
	return c.MemoryRepository.GetByID(ctx, orgID, id)
}

func (c *countingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.batch++
	return c.MemoryRepository.GetByIDs(ctx, orgID, ids)
}

// TestImpactReadsNodesInOneQuery covers NFR-02: the impact of a CI with
// many dependents reads the root and then all nodes in one batch, not one
// query per node.
func TestImpactReadsNodesInOneQuery(t *testing.T) {
	cis := &countingCIs{MemoryRepository: ci.NewMemoryRepository()}
	rels := relationship.NewMemoryRepository()
	add := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		if err := cis.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	pdu := add("pdu")
	for i := 0; i < 30; i++ {
		srv := add(fmt.Sprintf("srv-%02d", i))
		if err := rels.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: srv, TargetCIID: pdu, RelType: "powered_by"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	NewHandler(cis, rels).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+pdu+"?categories=power"))
	var body struct {
		Count    int          `json:"count"`
		Impacted []ImpactNode `json:"impacted"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Count != 30 {
		t.Fatalf("impact: %d %s", w.Code, w.Body.String())
	}
	if body.Impacted[0].Hops != 1 || len(body.Impacted[0].Path) != 1 || body.Impacted[0].Path[0].RelType != "powered_by" {
		t.Errorf("path: %+v", body.Impacted[0])
	}
	if cis.single > 2 || cis.batch != 1 {
		t.Errorf("CI reads: %d single, %d batch; want the root and one batch", cis.single, cis.batch)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+pdu+"?categories=cooling"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown category: %d", w.Code)
	}
}
