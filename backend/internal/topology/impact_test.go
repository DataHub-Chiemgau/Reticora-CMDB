package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

var testRules = func() map[string]string {
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
	all := computeImpact("pdu", edges, "", testRules)
	for _, id := range []string{"server", "vm", "app"} {
		if heuristic, ok := all[id]; !ok || heuristic {
			t.Errorf("%s: impacted %v, heuristic %v", id, ok, heuristic)
		}
	}
	if _, ok := all["monitor"]; ok || len(all) != 3 {
		t.Errorf("impact %v: the monitoring CI does not depend on the server", all)
	}
	// The failure of a dependent never reaches what it depends on.
	if got := computeImpact("vm", edges, "", testRules); len(got) != 1 {
		t.Errorf("failure of the vm: %v, want only the app", got)
	}
	if got := computeImpact("server", edges, "", testRules); len(got) != 2 {
		t.Errorf("failure of the server: %v, want vm and app", got)
	}
	// target_depends_on_source: the contained CI depends on its container.
	contains := []Edge{{ID: "c", SourceCIID: "chassis", TargetCIID: "blade", RelType: "contains"}}
	if got := computeImpact("chassis", contains, "", testRules); len(got) != 1 {
		t.Errorf("contains: %v", got)
	}
	if got := computeImpact("blade", contains, "", testRules); len(got) != 0 {
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
	got := computeImpact("switch", edges, "", testRules)
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
	if got = computeImpact("server", edges, "", testRules); !got["switch"] {
		t.Errorf("connected_to backwards: %v", got)
	}
	// The rel_type facet keeps one class.
	if got = computeImpact("switch", edges, "uplink_to", testRules); len(got) != 1 || got["server2"] {
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
	if got := computeImpact("a", edges, "", rules); len(got) != 1 || got["b"] {
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
