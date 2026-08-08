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

func TestParseDepth(t *testing.T) {
	cases := map[string]int{"": defaultDepth, "0": defaultDepth, "-1": defaultDepth, "abc": defaultDepth, "3": 3, "999": maxDepth}
	for in, want := range cases {
		if got := parseDepth(in); got != want {
			t.Errorf("parseDepth(%q)=%d want %d", in, got, want)
		}
	}
}
