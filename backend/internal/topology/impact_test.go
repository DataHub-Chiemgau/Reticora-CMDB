package topology

import "testing"

// Relationship edges point from the dependent to its dependency:
// A —runs_on/depends_on/powered_by→ B means A depends on B. When B fails,
// impact must therefore walk edges in reverse (target→source).

func TestComputeImpactFindsDependents(t *testing.T) {
	// server powered_by pdu; vm hosted_on server; switch connected_to server.
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "switch", TargetCIID: "server", RelType: "connected_to"},
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

func TestComputeImpactStandardDependencyChain(t *testing.T) {
	// Regression for audit finding H2: BS -> App -> DB -> VM -> Hypervisor
	// (each depends_on the next). A hypervisor failure must impact the whole
	// chain up to the business service.
	edges := []Edge{
		{ID: "e1", SourceCIID: "bs", TargetCIID: "app", RelType: "depends_on"},
		{ID: "e2", SourceCIID: "app", TargetCIID: "db", RelType: "depends_on"},
		{ID: "e3", SourceCIID: "db", TargetCIID: "vm", RelType: "runs_on"},
		{ID: "e4", SourceCIID: "vm", TargetCIID: "hypervisor", RelType: "runs_on"},
	}
	got := computeImpact("hypervisor", edges, "")
	if len(got) != 4 {
		t.Fatalf("expected 4 impacted (vm, db, app, bs), got %v", got)
	}
	for _, id := range []string{"vm", "db", "app", "bs"} {
		if _, ok := got[id]; !ok {
			t.Errorf("expected %s impacted", id)
		}
	}
}

func TestComputeImpactRelTypeFacet(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "switch", TargetCIID: "pdu", RelType: "connected_to"},
		{ID: "e3", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
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

func TestComputeImpactNoDownwardPropagation(t *testing.T) {
	// vm runs_on server: a server failure impacts the vm, but a vm failure
	// must not impact the server it runs on.
	edges := []Edge{{ID: "e1", SourceCIID: "vm", TargetCIID: "server", RelType: "runs_on"}}
	got := computeImpact("server", edges, "")
	if _, ok := got["vm"]; !ok {
		t.Fatalf("vm depends on server, so a server failure must impact the vm, got %v", got)
	}
	got = computeImpact("vm", edges, "")
	if len(got) != 0 {
		t.Fatalf("failure of a dependent must not propagate to its dependency, got %v", got)
	}
}

func TestComputeImpactHandlesCycles(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "a", TargetCIID: "b", RelType: "depends_on"},
		{ID: "e2", SourceCIID: "b", TargetCIID: "c", RelType: "depends_on"},
		{ID: "e3", SourceCIID: "c", TargetCIID: "a", RelType: "depends_on"},
	}
	got := computeImpact("c", edges, "")
	if len(got) != 2 {
		t.Fatalf("expected a and b impacted (cycle must terminate), got %v", got)
	}
}

func TestWalkDirectedUpstreamAndDownstream(t *testing.T) {
	// app depends_on db; db runs_on vm.
	edges := []Edge{
		{ID: "e1", SourceCIID: "app", TargetCIID: "db", RelType: "depends_on"},
		{ID: "e2", SourceCIID: "db", TargetCIID: "vm", RelType: "runs_on"},
	}

	// Upstream of app = its dependencies (db, vm): follow source→target.
	up := walkDirected("app", edges, false)
	for _, id := range []string{"db", "vm"} {
		if _, ok := up[id]; !ok {
			t.Errorf("expected %s in upstream (dependencies) of app, got %v", id, up)
		}
	}

	// Downstream of vm = its dependents (db, app): follow target→source.
	down := walkDirected("vm", edges, true)
	for _, id := range []string{"db", "app"} {
		if _, ok := down[id]; !ok {
			t.Errorf("expected %s in downstream (dependents) of vm, got %v", id, down)
		}
	}
}

func TestFindSPOFsWithRedundancy(t *testing.T) {
	// server has redundant power feeds pdu1 + pdu2; storage only feeds off pdu1.
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu1", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "server", TargetCIID: "pdu2", RelType: "powered_by"},
		{ID: "e3", SourceCIID: "storage", TargetCIID: "pdu1", RelType: "powered_by"},
	}
	affected := []Node{{ID: "server"}, {ID: "storage"}}
	spofs := findSPOFs("pdu1", edges, affected)
	ids := map[string]bool{}
	for _, n := range spofs {
		ids[n.ID] = true
	}
	if ids["server"] {
		t.Error("server has a redundant feed and must not be a SPOF")
	}
	if !ids["storage"] {
		t.Error("storage has no redundancy and must be a SPOF")
	}
}
