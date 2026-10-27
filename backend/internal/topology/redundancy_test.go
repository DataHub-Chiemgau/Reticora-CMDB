package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
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

// TestRedundancyProviderOutsideTheEdges covers IMP-04 (WP-513, F-160): a
// provider of a redundant type that is not among the edges of the
// analysis (a feed read beyond its depth) keeps the dependent up. Without
// the feed the server and the vm go down.
func TestRedundancyProviderOutsideTheEdges(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu-a", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
	}
	impacted := func(feeds func(g *ciGraph, number func(string) int32) []ciEdge, failedIDs ...string) string {
		t.Helper()
		g, failed := edgeGraph(failedIDs, edges)
		number := func(id string) int32 {
			for n, known := range g.ids {
				if known == id {
					return int32(n)
				}
			}
			t.Fatalf("no CI %s", id)
			return outside
		}
		if feeds != nil {
			g.feeds = feeds(g, number)
		}
		hits := impactOf(g, failed, impactQuery{depth: maxDepth}, testRules)
		var got []string
		for n := range hits {
			if hits[n].impacted {
				got = append(got, g.ids[n])
			}
		}
		sort.Strings(got)
		return strings.Join(got, ",")
	}
	powered := func(g *ciGraph) int32 { return int32(slices.Index(g.relTypes, "powered_by")) }
	beyond := func(g *ciGraph, number func(string) int32) []ciEdge {
		return []ciEdge{{id: "f1", source: number("server"), target: outside, relType: powered(g)}}
	}
	if got := impacted(nil, "pdu-a"); got != "server,vm" {
		t.Errorf("without the feed: %s, want server,vm", got)
	}
	if got := impacted(beyond, "pdu-a"); got != "" {
		t.Errorf("provider beyond the edges: %s, want nothing", got)
	}
}

// TestRedundancyBeyondDepthOverHTTP covers IMP-04 (WP-513, F-160) on the
// memory repository: at depth 1 the walk from pdu-a does not keep the edge
// server powered_by pdu-b; the handler reads it (EdgesOf) and keeps the
// server and the vm up in /impact, /impact?ci_ids=, the topology path and
// the blast radius. Both supplies take them down; a dropped edge between
// two CIs of the walk is followed like any other once its provider goes
// down.
func TestRedundancyBeyondDepthOverHTTP(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(ciRepo))
	ids := map[string]string{}
	for _, name := range []string{"pdu-a", "pdu-b", "server", "vm", "ups", "ups-2", "server-2", "pdu-c"} {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		if err := ciRepo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		ids[name] = item.ID
	}
	for _, e := range [][3]string{
		{"server", "pdu-a", "powered_by"}, {"server", "pdu-b", "powered_by"},
		{"vm", "server", "hosted_on"}, {"pdu-a", "ups", "powered_by"},
		// server-2 and pdu-c hang on ups-2; at depth 1 the walk drops
		// server-2 powered_by pdu-c (both ends at the depth).
		{"server-2", "ups-2", "powered_by"}, {"pdu-c", "ups-2", "powered_by"}, {"server-2", "pdu-c", "powered_by"},
	} {
		if err := relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1",
			SourceCIID: ids[e[0]], TargetCIID: ids[e[1]], RelType: e[2]}); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	counted := &countingFeeds{MemoryRepository: relRepo}
	NewHandler(ciRepo, counted).RegisterRoutes(mux)
	names := map[string]string{}
	for name, id := range ids {
		names[id] = name
	}
	impacted := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq(http.MethodGet, path))
		var body struct {
			Impacted []ImpactNode `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var got []string
		for _, n := range body.Impacted {
			got = append(got, names[n.ID])
		}
		sort.Strings(got)
		return strings.Join(got, ",")
	}
	for path, want := range map[string]string{
		"/api/v1/impact/" + ids["pdu-a"] + "?depth=1":                             "",
		"/api/v1/impact?ci_ids=" + ids["pdu-a"] + "&depth=1":                      "",
		"/api/v1/topology/cis/" + ids["pdu-a"] + "/impact?depth=1":                "",
		"/api/v1/impact/" + ids["ups"] + "?depth=2":                               "pdu-a",
		"/api/v1/impact?ci_ids=" + ids["pdu-a"] + "," + ids["pdu-b"] + "&depth=1": "server",
		"/api/v1/impact?ci_ids=" + ids["pdu-a"] + "," + ids["pdu-b"] + "&depth=2": "server,vm",
		// The edge of server-2 to pdu-c joins the graph: pdu-c goes down
		// with ups-2, so does server-2.
		"/api/v1/impact/" + ids["ups-2"] + "?depth=1": "pdu-c,server-2",
	} {
		if got := impacted(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq(http.MethodGet, "/api/v1/cis/"+ids["pdu-a"]+"/blast-radius"))
	var blast BlastRadius
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &blast) != nil {
		t.Fatalf("blast radius: %d %s", w.Code, w.Body.String())
	}
	if blast.AffectedCount != 0 || len(blast.SinglePointsOfFailure) != 0 || len(blast.ProtectedByRedundancy) != 1 ||
		blast.ProtectedByRedundancy[0].ID != ids["server"] {
		t.Errorf("blast radius of pdu-a: %+v", blast)
	}

	// One read of the feeds per analysis, of several failed CIs as well;
	// none when every dependent lies within the depth.
	for path, want := range map[string]int{
		"/api/v1/impact?ci_ids=" + ids["pdu-a"] + "," + ids["pdu-b"] + "," + ids["ups"] + "&depth=1": 1,
		"/api/v1/impact/" + ids["pdu-a"] + "?depth=20":                                               0,
	} {
		counted.calls = 0
		impacted(path)
		if counted.calls != want {
			t.Errorf("%s: %d reads of the feeds, want %d", path, counted.calls, want)
		}
	}
}

// countingFeeds counts the reads of the feeds (EdgesOf).
type countingFeeds struct {
	*relationship.MemoryRepository
	calls int
}

func (c *countingFeeds) EdgesOf(ctx context.Context, orgID string, ciIDs, relTypes []string) ([]relationship.Relationship, error) {
	c.calls++
	return c.MemoryRepository.EdgesOf(ctx, orgID, ciIDs, relTypes)
}

// TestReadFeedsOfATruncatedWalk covers IMP-04 (WP-513): a dependent within
// the depth has every edge in the walk, so its feeds are not read; once
// the node limit cut the walk they are, and the provider beyond keeps it
// up.
func TestReadFeedsOfATruncatedWalk(t *testing.T) {
	relRepo := relationship.NewMemoryRepository()
	for _, e := range [][2]string{{"server", "pdu-a"}, {"server", "pdu-b"}} {
		if err := relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1",
			SourceCIID: e[0], TargetCIID: e[1], RelType: "powered_by"}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(ci.NewMemoryRepository(), relRepo)
	for _, truncated := range []bool{false, true} {
		// The walk from pdu-a holds the server but not its edge to pdu-b.
		g, failed := edgeGraph([]string{"pdu-a"}, []Edge{{ID: "x1", SourceCIID: "server", TargetCIID: "pdu-a", RelType: "powered_by"}})
		g.truncated = truncated
		if err := h.readFeeds(context.Background(), "org-1", g, failed, maxDepth, testRules); err != nil {
			t.Fatal(err)
		}
		hits := impactOf(g, failed, impactQuery{depth: maxDepth}, testRules)
		want := 0
		if truncated {
			want = 1
		}
		if len(g.feeds) != want {
			t.Errorf("truncated %v: feeds %+v, want %d", truncated, g.feeds, want)
		}
		if server := hits[slices.Index(g.ids, "server")].impacted; server == truncated {
			t.Errorf("truncated %v: server impacted %v", truncated, server)
		}
	}
}
