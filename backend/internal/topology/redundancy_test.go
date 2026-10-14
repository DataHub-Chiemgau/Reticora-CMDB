package topology

import (
	"sort"
	"strings"
	"testing"
)

// TestRedundancyPostfilter covers IMP-04 (WP-179) as a table: over a
// redundant type (powered_by, member_of_cluster) a CI fails only when all
// its providers failed; hosted_on has no redundancy; what depends on a CI
// kept up stays up as well.
func TestRedundancyPostfilter(t *testing.T) {
	// server is powered by pdu-a and pdu-b, the vm is hosted on the
	// server, node-1 and node-2 are members of two clusters each, the app
	// runs on node-1.
	edges := []Edge{
		{ID: "e01", SourceCIID: "server", TargetCIID: "pdu-a", RelType: "powered_by"},
		{ID: "e02", SourceCIID: "server", TargetCIID: "pdu-b", RelType: "powered_by"},
		{ID: "e03", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e04", SourceCIID: "single", TargetCIID: "pdu-a", RelType: "powered_by"},
		{ID: "e05", SourceCIID: "node-1", TargetCIID: "cluster-x", RelType: "member_of_cluster"},
		{ID: "e06", SourceCIID: "node-1", TargetCIID: "cluster-y", RelType: "member_of_cluster"},
		{ID: "e07", SourceCIID: "app", TargetCIID: "node-1", RelType: "runs_on"},
		{ID: "e08", SourceCIID: "vm2", TargetCIID: "hv-a", RelType: "hosted_on"},
		{ID: "e09", SourceCIID: "vm2", TargetCIID: "hv-b", RelType: "hosted_on"},
	}
	cases := []struct {
		name   string
		failed []string
		want   string
	}{
		{"one of two supplies", []string{"pdu-a"}, "single"},
		{"the other supply", []string{"pdu-b"}, ""},
		{"both supplies", []string{"pdu-a", "pdu-b"}, "server,single,vm"},
		{"the server itself", []string{"server"}, "vm"},
		{"one cluster", []string{"cluster-x"}, ""},
		{"both clusters", []string{"cluster-x", "cluster-y"}, "app,node-1"},
		{"hosted_on without redundancy", []string{"hv-a"}, "vm2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := computeImpact(c.failed, edges, impactQuery{depth: maxDepth}, testRules)
			got := make([]string, 0, len(hits))
			for id := range hits {
				got = append(got, id)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != c.want {
				t.Errorf("impacted %v, want %s", got, c.want)
			}
		})
	}
}

// TestRedundancyFindings: the direct dependents of a failed supply are a
// single point of failure without an alternative and protected with one.
func TestRedundancyFindings(t *testing.T) {
	graph := Graph{
		Nodes: []Node{{ID: "pdu-a"}, {ID: "pdu-b"}, {ID: "server"}, {ID: "single"}},
		Edges: []Edge{
			{ID: "e1", SourceCIID: "server", TargetCIID: "pdu-a", RelType: "powered_by"},
			{ID: "e2", SourceCIID: "server", TargetCIID: "pdu-b", RelType: "powered_by"},
			{ID: "e3", SourceCIID: "single", TargetCIID: "pdu-a", RelType: "powered_by"},
		},
	}
	hits := computeImpact([]string{"pdu-a"}, graph.Edges, impactQuery{depth: maxDepth}, testRules)
	spof, protected := redundancyFindings("pdu-a", graph, hits, testRules)
	if len(spof) != 1 || spof[0].ID != "single" || len(protected) != 1 || protected[0].ID != "server" {
		t.Errorf("single points of failure %v, protected %v", spof, protected)
	}
}
