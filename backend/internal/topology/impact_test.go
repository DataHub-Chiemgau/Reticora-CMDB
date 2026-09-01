package topology

import "testing"

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
