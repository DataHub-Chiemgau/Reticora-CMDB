package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestComputeImpactFollowsDirectedEdges(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "pdu", TargetCIID: "server", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "server", TargetCIID: "vm", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "server", TargetCIID: "switch", RelType: "connected_to"},
	}

	all := computeImpact("pdu", edges, "")
	if len(all) != 3 {
		t.Fatalf("expected server, vm, switch impacted, got %v", all)
	}
	for _, id := range []string{"server", "vm", "switch"} {
		if _, ok := all[id]; !ok {
			t.Errorf("expected %s impacted", id)
		}
	}
}

func TestComputeImpactRelTypeFacet(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "pdu", TargetCIID: "server", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "pdu", TargetCIID: "switch", RelType: "connected_to"},
		{ID: "e3", SourceCIID: "server", TargetCIID: "vm", RelType: "hosted_on"},
	}
	// Only power path: switch (network) must not be impacted.
	power := computeImpact("pdu", edges, "powered_by")
	if _, ok := power["switch"]; ok {
		t.Errorf("network-only neighbor must not be impacted by power facet")
	}
	if _, ok := power["server"]; !ok {
		t.Errorf("server must be impacted by power facet")
	}
}

func TestComputeImpactNoBackward(t *testing.T) {
	// A dependency edge server->vm must not impact the server when the vm fails.
	edges := []Edge{{ID: "e1", SourceCIID: "server", TargetCIID: "vm", RelType: "hosted_on"}}
	got := computeImpact("vm", edges, "")
	if len(got) != 0 {
		t.Fatalf("failure of a dependent must not propagate upstream, got %v", got)
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
		mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+ids["sw1"]))
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
	if before := impacted(); !before[ids["srv1"]] || !before[ids["pdu1"]] {
		t.Fatalf("seed graph: impacted %v", before)
	}
	if err := ciRepo.Delete(context.Background(), "org-1", ids["srv1"]); err != nil {
		t.Fatal(err)
	}
	if after := impacted(); len(after) != 0 {
		t.Errorf("impact passes through a deleted CI: %v", after)
	}
}
