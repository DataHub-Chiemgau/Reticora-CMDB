package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantReq(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

// seed builds a small graph: sw1 -connected_to- srv1, srv1 -powered_by- pdu1,
// plus an isolated CI in another org that must never appear. The walks
// pass only through visible, not deleted CIs (IMP-07).
func seed(t *testing.T) (*ci.MemoryRepository, *relationship.MemoryRepository, map[string]string) {
	t.Helper()
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(ciRepo))
	ids := map[string]string{}

	add := func(name, ctype, client string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: ctype, ClientID: client, Status: "active"}
		ciRepo.Create(context.Background(), item)
		ids[name] = item.ID
		return item.ID
	}
	sw1 := add("sw1", "switch", "client-a")
	srv1 := add("srv1", "server", "client-a")
	pdu1 := add("pdu1", "pdu", "client-a")
	add("other", "server", "client-b")

	// CI belonging to another org, must be invisible.
	foreign := &ci.Item{OrganizationID: "org-2", Name: "foreign", CITypeID: "server"}
	ciRepo.Create(context.Background(), foreign)

	relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: sw1, TargetCIID: srv1, RelType: "connected_to", Source: "discovery"})
	relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: srv1, TargetCIID: pdu1, RelType: "powered_by", Source: "discovery"})
	return ciRepo, relRepo, ids
}

func TestGetTopologyFull(t *testing.T) {
	ciRepo, relRepo, _ := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var g Graph
	json.Unmarshal(w.Body.Bytes(), &g)
	// 4 org-1 CIs (sw1, srv1, pdu1, other); foreign org excluded.
	if len(g.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d (%+v)", len(g.Nodes), g.Nodes)
	}
	if len(g.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(g.Edges))
	}
}

func TestGetTopologyClientFilter(t *testing.T) {
	ciRepo, relRepo, _ := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology?client_id=client-b"))
	var g Graph
	json.Unmarshal(w.Body.Bytes(), &g)
	if len(g.Nodes) != 1 || g.Nodes[0].Name != "other" {
		t.Fatalf("expected only 'other', got %+v", g.Nodes)
	}
	if len(g.Edges) != 0 {
		t.Errorf("expected no edges, got %d", len(g.Edges))
	}
}

// batchEdges gives the memory repository the batch edge read
// (relationship.EdgeReader) and records its calls; the topology without a
// root must then not list the edges of each CI.
type batchEdges struct {
	*relationship.MemoryRepository
	t     *testing.T
	calls [][]string
}

func (b *batchEdges) List(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]relationship.Relationship, int, error) {
	b.t.Errorf("the topology without a root lists the edges of %q", ciID)
	return b.MemoryRepository.List(ctx, orgID, ciID, page)
}

// EdgesAmong answers every edge of the organization, so the handler's own
// check of the node set shows as well.
func (b *batchEdges) EdgesAmong(ctx context.Context, orgID string, ids []string) ([]relationship.Relationship, error) {
	b.calls = append(b.calls, slices.Clone(ids))
	rels, _, err := b.MemoryRepository.List(ctx, orgID, "", api.PaginationParams{Limit: 100})
	return rels, err
}

// TestGetTopologyFullEdgeOrder covers NFR-02 and ABN-02 (WP-602) in the
// memory profile: without a root, a repository that reads the edges among a
// set of CIs in batches (relationship.EdgeReader) is asked once for the
// node set and never per CI; the memory repository keeps the read per CI.
// Both answer the same graph: the edges between two nodes, each once,
// ordered by id, the same on every request.
func TestGetTopologyFullEdgeOrder(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	// More edges than the seed, one of them to the CI of client-b.
	for _, e := range [][3]string{
		{ids["pdu1"], ids["sw1"], "depends_on"},
		{ids["other"], ids["sw1"], "connected_to"},
		{ids["sw1"], ids["pdu1"], "connected_to"},
	} {
		if err := relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: e[0], TargetCIID: e[1], RelType: e[2]}); err != nil {
			t.Fatal(err)
		}
	}
	batch := &batchEdges{MemoryRepository: relRepo, t: t}
	for _, c := range []struct {
		name  string
		rels  relationship.Repository
		path  string
		nodes int
		edges int
	}{
		{"per CI", relRepo, "/api/v1/topology", 4, 5},
		{"per CI, client-a", relRepo, "/api/v1/topology?client_id=client-a", 3, 4},
		{"batch", batch, "/api/v1/topology", 4, 5},
		{"batch, client-a", batch, "/api/v1/topology?client_id=client-a", 3, 4},
	} {
		mux := chi.NewRouter()
		NewHandler(ciRepo, c.rels).RegisterRoutes(mux)
		batch.calls = nil
		var first string
		for i := 0; i < 5; i++ {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, tenantReq("GET", c.path))
			var g Graph
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &g) != nil {
				t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
			}
			if i == 0 {
				first = w.Body.String()
			} else if w.Body.String() != first {
				t.Errorf("%s: answer %d differs from the first:\n%s\n%s", c.name, i, w.Body.String(), first)
			}
			if len(g.Nodes) != c.nodes || len(g.Edges) != c.edges {
				t.Fatalf("%s: %d nodes and %d edges, want %d and %d", c.name, len(g.Nodes), len(g.Edges), c.nodes, c.edges)
			}
			nodes := map[string]bool{}
			for _, n := range g.Nodes {
				nodes[n.ID] = true
			}
			for j, e := range g.Edges {
				if !nodes[e.SourceCIID] || !nodes[e.TargetCIID] {
					t.Errorf("%s: edge %+v leaves the node set", c.name, e)
				}
				if j > 0 && g.Edges[j-1].ID >= e.ID {
					t.Errorf("%s: edges not ordered by id: %+v", c.name, g.Edges)
				}
			}
			if c.rels == batch {
				ids := make([]string, len(g.Nodes))
				for j, n := range g.Nodes {
					ids[j] = n.ID
				}
				if len(batch.calls) != i+1 || !slices.Equal(batch.calls[i], ids) {
					t.Errorf("%s: EdgesAmong calls %v, want one per request with the nodes %v", c.name, batch.calls, ids)
				}
			}
		}
	}
}

func TestGetTopologyRootDepth(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Depth 1 from sw1 reaches only srv1.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology?root_ci_id="+ids["sw1"]+"&depth=1"))
	var g Graph
	json.Unmarshal(w.Body.Bytes(), &g)
	if len(g.Nodes) != 2 {
		t.Fatalf("depth 1: expected 2 nodes, got %d (%+v)", len(g.Nodes), g.Nodes)
	}

	// Depth 2 from sw1 reaches srv1 and pdu1.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology?root_ci_id="+ids["sw1"]+"&depth=2"))
	json.Unmarshal(w.Body.Bytes(), &g)
	if len(g.Nodes) != 3 {
		t.Fatalf("depth 2: expected 3 nodes, got %d (%+v)", len(g.Nodes), g.Nodes)
	}
	if len(g.Edges) != 2 {
		t.Errorf("depth 2: expected 2 edges, got %d", len(g.Edges))
	}
}

func TestGetNeighbors(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology/cis/"+ids["srv1"]+"/neighbors"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var g Graph
	json.Unmarshal(w.Body.Bytes(), &g)
	// srv1 neighbors: sw1 and pdu1, plus srv1 itself = 3 nodes.
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d (%+v)", len(g.Nodes), g.Nodes)
	}
	if len(g.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(g.Edges))
	}
}

func TestGetNeighborsNotFound(t *testing.T) {
	ciRepo, relRepo, _ := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology/cis/missing/neighbors"))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// relationsOnly hides the Traverser of the memory repository, so the
// handler walks the graph hop by hop.
type relationsOnly struct{ relationship.Repository }

// TestGetTopologyRootNotFound covers F-161 (WP-403) in the memory profile: a
// root_ci_id the caller does not see (unknown, of another organization,
// deleted) answers 404 "ci not found" like GetNeighbors, the same answer for
// each and without the id; a root that is no UUID is 400 on root_ci_id.
// Both walks (recursive traversal and hop by hop) answer alike.
func TestGetTopologyRootNotFound(t *testing.T) {
	for _, walk := range []string{"traversal", "iterative"} {
		t.Run(walk, func(t *testing.T) {
			ciRepo, relRepo, ids := seed(t)
			var rels relationship.Repository = relRepo
			if walk == "iterative" {
				rels = relationsOnly{relRepo}
			}
			h := NewHandler(ciRepo, rels)
			if (h.traverser != nil) != (walk == "traversal") {
				t.Fatalf("walk %s: traverser %v", walk, h.traverser != nil)
			}
			mux := chi.NewRouter()
			h.RegisterRoutes(mux)
			get := func(root string) (int, string) {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology?root_ci_id="+root))
				return w.Code, w.Body.String()
			}

			foreign := &ci.Item{OrganizationID: "org-2", Name: "foreign-root", CITypeID: "server"}
			deleted := &ci.Item{OrganizationID: "org-1", Name: "deleted-root", CITypeID: "server"}
			for _, item := range []*ci.Item{foreign, deleted} {
				if err := ciRepo.Create(context.Background(), item); err != nil {
					t.Fatal(err)
				}
			}
			if err := ciRepo.Delete(context.Background(), "org-1", deleted.ID); err != nil {
				t.Fatal(err)
			}
			// The problems are equal but for the trace id of the request.
			answers := map[string]string{}
			for name, root := range map[string]string{
				"unknown":            "00000000-0000-4000-8000-0000000000ff",
				"other organization": foreign.ID,
				"deleted":            deleted.ID,
			} {
				code, body := get(root)
				var p map[string]any
				if code != http.StatusNotFound || json.Unmarshal([]byte(body), &p) != nil || p["detail"] != "ci not found" {
					t.Errorf("%s root: %d %s, want 404 ci not found", name, code, body)
				}
				if strings.Contains(body, root) || strings.Contains(body, "foreign-root") {
					t.Errorf("%s root: the answer names the CI: %s", name, body)
				}
				delete(p, "trace_id")
				answers[name] = fmt.Sprint(p)
			}
			for name, answer := range answers {
				if answer != answers["unknown"] {
					t.Errorf("%s root answers %s, an unknown root %s", name, answer, answers["unknown"])
				}
			}

			code, body := get("not-a-uuid")
			var fp struct {
				Field string `json:"field"`
			}
			if code != http.StatusBadRequest || json.Unmarshal([]byte(body), &fp) != nil || fp.Field != "root_ci_id" {
				t.Errorf("root not-a-uuid: %d %s, want 400 on root_ci_id", code, body)
			}
			if code, body := get(ids["sw1"]); code != http.StatusOK {
				t.Errorf("own root: %d %s", code, body)
			}
		})
	}
}

func TestTopologyUnauthorized(t *testing.T) {
	ciRepo, relRepo, _ := seed(t)
	h := NewHandler(ciRepo, relRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/topology", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestTopologySkipsDeletedCIs covers IMP-07 and CI-05 in the memory
// profile (WP-335): a deleted CI does not connect the CIs behind it, so a
// CI reachable within the depth only through it stays out of the topology.
func TestTopologySkipsDeletedCIs(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	add := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		if err := ciRepo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	// sw1 - gone - far (two hops), and the long way sw1 - srv1 - mid - far.
	gone, mid, far := add("gone"), add("mid"), add("far")
	for _, e := range [][2]string{{ids["sw1"], gone}, {gone, far}, {ids["srv1"], mid}, {mid, far}} {
		if err := relRepo.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: e[0], TargetCIID: e[1], RelType: "connected_to"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	NewHandler(ciRepo, relRepo).RegisterRoutes(mux)
	nodes := func() map[string]bool {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", "/api/v1/topology?root_ci_id="+ids["sw1"]+"&depth=2"))
		var g Graph
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &g) != nil {
			t.Fatalf("topology: %d %s", w.Code, w.Body.String())
		}
		out := map[string]bool{}
		for _, n := range g.Nodes {
			out[n.ID] = true
		}
		return out
	}
	if before := nodes(); !before[gone] || !before[far] {
		t.Fatalf("before the delete: %v", before)
	}
	if err := ciRepo.Delete(context.Background(), "org-1", gone); err != nil {
		t.Fatal(err)
	}
	if after := nodes(); after[gone] || after[far] || !after[mid] {
		t.Errorf("after the delete: gone %v, far %v (behind the deleted CI), mid %v", after[gone], after[far], after[mid])
	}
}

// TestTraversalCycleGuard seeds a cyclic graph and asserts the traversal
// terminates with every relationship reported exactly once.
func TestTraversalCycleGuard(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository()

	mkCI := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		ciRepo.Create(context.Background(), item)
		return item.ID
	}
	a, b, c := mkCI("a"), mkCI("b"), mkCI("c")

	mkRel := func(src, dst string) {
		relRepo.Create(context.Background(), &relationship.Relationship{
			OrganizationID: "org-1", SourceCIID: src, TargetCIID: dst, RelType: "connected_to", Source: "manual",
		})
	}
	// Cycle a -> b -> c -> a, a self-loop on the root a, and a self-loop on b.
	// Every edge between reached nodes is reported once
	// (relationship.SelectTraversal), self-loops included.
	mkRel(a, b)
	mkRel(b, c)
	mkRel(c, a)
	mkRel(a, a)
	mkRel(b, b)

	rels, err := relRepo.TraverseFrom(context.Background(), "org-1", a, maxDepth, relationship.MaxTraversalNodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 5 {
		t.Fatalf("expected 5 relationships in cyclic graph, got %d (%+v)", len(rels), rels)
	}
	seen := map[string]int{}
	for _, rel := range rels {
		seen[rel.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("relationship %s reported %d times", id, n)
		}
	}
}

// TestTraversalDepthLimit ensures the memory traverser honours maxDepth.
func TestTraversalDepthLimit(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository()

	mkCI := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		ciRepo.Create(context.Background(), item)
		return item.ID
	}
	// Chain: n0 - n1 - n2 - n3.
	ids := []string{mkCI("n0"), mkCI("n1"), mkCI("n2"), mkCI("n3")}
	for i := 0; i+1 < len(ids); i++ {
		relRepo.Create(context.Background(), &relationship.Relationship{
			OrganizationID: "org-1", SourceCIID: ids[i], TargetCIID: ids[i+1], RelType: "connected_to", Source: "manual",
		})
	}

	rels, err := relRepo.TraverseFrom(context.Background(), "org-1", ids[0], 2, relationship.MaxTraversalNodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("depth 2: expected 2 relationships, got %d", len(rels))
	}

	// Cross-org relationships must never be traversed.
	rels, err = relRepo.TraverseFrom(context.Background(), "org-2", ids[0], maxDepth, relationship.MaxTraversalNodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected no relationships for foreign org, got %d", len(rels))
	}
}

func TestParseDepth(t *testing.T) {
	cases := map[string]int{"": defaultDepth, "0": defaultDepth, "-1": defaultDepth, "abc": defaultDepth, "3": 3, "999": maxDepth}
	for in, want := range cases {
		if got := parseDepth(in); got != want {
			t.Errorf("parseDepth(%q)=%d want %d", in, got, want)
		}
	}
}

// TestImpactCatalogPath covers API-05: /impact/{ci_id} answers like the
// legacy /topology/cis/{id}/impact.
func TestImpactCatalogPath(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	mux := chi.NewRouter()
	NewHandler(ciRepo, relRepo).RegisterRoutes(mux)
	bodies := map[string]string{}
	for _, path := range []string{"/api/v1/impact/" + ids["pdu1"], "/api/v1/topology/cis/" + ids["pdu1"] + "/impact"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", path))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		bodies[path] = w.Body.String()
	}
	if bodies["/api/v1/impact/"+ids["pdu1"]] != bodies["/api/v1/topology/cis/"+ids["pdu1"]+"/impact"] {
		t.Errorf("answers differ: %v", bodies)
	}
}
