package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
// plus an isolated CI in another org that must never appear.
func seed(t *testing.T) (*ci.MemoryRepository, *relationship.MemoryRepository, map[string]string) {
	t.Helper()
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository()
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
	// Cycle a -> b -> c -> a, a self-loop on the root a, and a self-loop on b
	// (self-loops away from the root are dropped by the traversal guard, which
	// matches the recursive-CTE implementation).
	mkRel(a, b)
	mkRel(b, c)
	mkRel(c, a)
	mkRel(a, a)
	mkRel(b, b)

	rels, err := relRepo.TraverseFrom(context.Background(), "org-1", a, maxDepth, maxFetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 4 {
		t.Fatalf("expected 4 relationships in cyclic graph, got %d (%+v)", len(rels), rels)
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

	rels, err := relRepo.TraverseFrom(context.Background(), "org-1", ids[0], 2, maxFetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("depth 2: expected 2 relationships, got %d", len(rels))
	}

	// Cross-org relationships must never be traversed.
	rels, err = relRepo.TraverseFrom(context.Background(), "org-2", ids[0], maxDepth, maxFetch)
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
