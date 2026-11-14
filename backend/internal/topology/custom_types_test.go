package topology

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// TestImpactOfCustomRelationshipTypes covers IMP-10 (WP-294, REL-06): the
// impact follows the impact_direction of an organization's own types for
// both edge directions (source_depends_on_target, target_depends_on_source,
// none, undirected in network as a heuristic), a custom redundant type
// keeps a CI with a second provider up (IMP-04, redundancyFindings), and
// the seeds monitored_by (no impact) and belongs_to (the source depends on
// the target) read as REL-06 defines them. The rules come from the
// relationship type repository (ListAll) as in the handler.
func TestImpactOfCustomRelationshipTypes(t *testing.T) {
	bg := context.Background()
	types := relationshiptype.NewMemoryRepository()
	for _, typ := range []relationshiptype.Type{
		{Key: "c_needs", Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactSourceDependsOnTarget},
		{Key: "c_feeds", Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactTargetDependsOnSource},
		{Key: "c_notes", Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactNone},
		{Key: "c_peers", Directed: false, Category: "network", ImpactDirection: relationshiptype.ImpactUndirected},
		{Key: "c_supplied_by", Directed: true, Category: "power", ImpactDirection: relationshiptype.ImpactSourceDependsOnTarget, Redundant: true},
	} {
		typ.OrganizationID, typ.DisplayName, typ.ReverseName = "org-1", typ.Key, typ.Key
		if err := types.Create(bg, &typ); err != nil {
			t.Fatalf("%s: %v", typ.Key, err)
		}
	}
	rules := (&Analyzer{types: types}).impactRules(bg, "org-1")

	// Matrix: the edge src → dst of each type; the failure of src or dst.
	type reach struct {
		impacted, heuristic bool
	}
	for _, c := range []struct {
		relType            string
		srcFails, dstFails reach // what the failure reaches at the other end
	}{
		{"c_needs", reach{}, reach{impacted: true}},
		{"c_feeds", reach{impacted: true}, reach{}},
		{"c_notes", reach{}, reach{}},
		{"c_peers", reach{impacted: true, heuristic: true}, reach{impacted: true, heuristic: true}},
	} {
		edges := []Edge{{ID: "e1", SourceCIID: "src", TargetCIID: "dst", RelType: c.relType}}
		for _, run := range []struct {
			failed, other string
			want          reach
		}{
			{"src", "dst", c.srcFails},
			{"dst", "src", c.dstFails},
		} {
			hits := flags(computeImpact([]string{run.failed}, edges, impactQuery{depth: maxDepth}, rules))
			heuristic, impacted := hits[run.other]
			if impacted != run.want.impacted || heuristic != run.want.heuristic || len(hits) > 1 {
				t.Errorf("%s, %s fails: impact %v, want %s %+v", c.relType, run.failed, hits, run.other, run.want)
			}
		}
	}
	if rule := ruleOf(rules, "c_peers"); rule.category != "network" {
		t.Errorf("undirected custom type: %+v, want network", rule)
	}

	// A custom redundant type: server has two supplies, single one.
	graph := Graph{
		Nodes: []Node{{ID: "ups-a"}, {ID: "ups-b"}, {ID: "server"}, {ID: "single"}},
		Edges: []Edge{
			{ID: "e1", SourceCIID: "server", TargetCIID: "ups-a", RelType: "c_supplied_by"},
			{ID: "e2", SourceCIID: "server", TargetCIID: "ups-b", RelType: "c_supplied_by"},
			{ID: "e3", SourceCIID: "single", TargetCIID: "ups-a", RelType: "c_supplied_by"},
		},
	}
	hits := computeImpact([]string{"ups-a"}, graph.Edges, impactQuery{depth: maxDepth}, rules)
	if _, down := hits["server"]; down || len(hits) != 1 {
		t.Errorf("failure of one of two supplies: %v, want only single", flags(hits))
	}
	spof, protected := redundancyFindings("ups-a", graph, hits, rules)
	if len(spof) != 1 || spof[0].ID != "single" || len(protected) != 1 || protected[0].ID != "server" {
		t.Errorf("single points of failure %v, protected %v", spof, protected)
	}
	if both := computeImpact([]string{"ups-a", "ups-b"}, graph.Edges, impactQuery{depth: maxDepth}, rules); len(both) != 2 {
		t.Errorf("failure of both supplies: %v, want server and single", flags(both))
	}

	// The seeds of REL-06: server monitored_by monitor, member belongs_to
	// group.
	seeds := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "monitor", RelType: "monitored_by"},
		{ID: "e2", SourceCIID: "member", TargetCIID: "group", RelType: "belongs_to"},
	}
	if got := computeImpact([]string{"monitor"}, seeds, impactQuery{depth: maxDepth}, rules); len(got) != 0 {
		t.Errorf("failure of the monitoring CI: %v, want nothing", flags(got))
	}
	got := flags(computeImpact([]string{"group"}, seeds, impactQuery{depth: maxDepth}, rules))
	if heuristic, ok := got["member"]; !ok || heuristic || len(got) != 1 {
		t.Errorf("failure of the group: %v, want the member, certain", got)
	}
	if got := computeImpact([]string{"member"}, seeds, impactQuery{depth: maxDepth}, rules); len(got) != 0 {
		t.Errorf("failure of the member: %v, want nothing", flags(got))
	}
}
