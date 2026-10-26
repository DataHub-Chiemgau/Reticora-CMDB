package relationship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandler_CreateAndList(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"connected_to"}`
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Relationship
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.RelType != "connected_to" {
		t.Errorf("expected connected_to, got %s", created.RelType)
	}

	// List relationships for ci-1
	req = httptest.NewRequest("GET", "/api/v1/cis/ci-1/relationships", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Relationship `json:"data"`
		Total int            `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_InvalidRelType(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"invalid_type"}`
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	rel := &Relationship{
		OrganizationID: "org-1",
		SourceCIID:     "ci-1",
		TargetCIID:     "ci-2",
		RelType:        "depends_on",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), rel)

	req := httptest.NewRequest("DELETE", "/api/v1/relationships/"+rel.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

// TestListAllRelationships covers GET /api/v1/relationships, which lists every
// relationship in the organization rather than those of a single CI.
func TestListAllRelationships(t *testing.T) {
	repo := NewMemoryRepository()
	for _, rel := range []*Relationship{
		{OrganizationID: "org-1", SourceCIID: "ci-1", TargetCIID: "ci-2", RelType: "depends_on"},
		{OrganizationID: "org-1", SourceCIID: "ci-3", TargetCIID: "ci-4", RelType: "hosts"},
		{OrganizationID: "org-2", SourceCIID: "ci-5", TargetCIID: "ci-6", RelType: "hosts"},
	} {
		if err := repo.Create(context.Background(), rel); err != nil {
			t.Fatalf("create relationship: %v", err)
		}
	}

	router := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(router)

	req := tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/relationships", nil))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListResponse[Relationship]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("expected the 2 relationships of org-1, got %d", resp.Total)
	}
}

// createRel posts a relationship and returns it.
func createRel(t *testing.T, mux http.Handler, body string) Relationship {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rel Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
		t.Fatal(err)
	}
	return rel
}

func patchRel(t *testing.T, mux http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/v1/relationships/"+id, bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestHandler_UpdateVerifiesAndEdits covers spec §13, which requires manual
// relationships to be editable and verifiable, not just creatable and
// deletable.
func TestHandler_UpdateVerifiesAndEdits(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	rel := createRel(t, mux, `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"depends_on","source":"discovery"}`)
	if rel.VerificationState != "unverified" {
		t.Fatalf("discovered relationship should start unverified, got %q", rel.VerificationState)
	}

	conf := 0.9
	body, _ := json.Marshal(UpdateRequest{
		VerificationState: strPtr("verified"),
		Notes:             strPtr("checked on site"),
		Confidence:        &conf,
		SourceSystem:      strPtr("netbox"),
		Attributes:        map[string]any{"port": "eth0"},
	})
	w := patchRel(t, mux, rel.ID, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.VerificationState != "verified" {
		t.Fatalf("expected verified, got %q", updated.VerificationState)
	}
	if updated.Notes != "checked on site" {
		t.Fatalf("expected notes persisted, got %q", updated.Notes)
	}
	if updated.Confidence == nil || *updated.Confidence != 0.9 {
		t.Fatalf("expected confidence 0.9, got %v", updated.Confidence)
	}
	if updated.SourceSystem != "netbox" {
		t.Fatalf("expected source_system persisted, got %q", updated.SourceSystem)
	}
	if updated.Attributes["port"] != "eth0" {
		t.Fatalf("expected attributes merged, got %#v", updated.Attributes)
	}
	// The edge itself must not have moved.
	if updated.SourceCIID != "ci-1" || updated.TargetCIID != "ci-2" || updated.RelType != "depends_on" {
		t.Fatalf("edge endpoints/type must be immutable, got %+v", updated)
	}
}

func TestHandler_UpdateRejectsInvalidInput(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	rel := createRel(t, mux, `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"depends_on"}`)

	if w := patchRel(t, mux, rel.ID, `{"verification_state":"bogus"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid verification_state, got %d", w.Code)
	}
	if w := patchRel(t, mux, rel.ID, `{"confidence":1.5}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range confidence, got %d", w.Code)
	}
}

// TestHandler_UpdateIsTenantScoped ensures a relationship belonging to another
// organization cannot be edited through a guessed identifier.
func TestHandler_UpdateIsTenantScoped(t *testing.T) {
	repo := NewMemoryRepository()
	other := &Relationship{OrganizationID: "org-2", SourceCIID: "x", TargetCIID: "y", RelType: "depends_on"}
	if err := repo.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	if w := patchRel(t, mux, other.ID, `{"verification_state":"verified"}`); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant update, got %d", w.Code)
	}
	if other.VerificationState == "verified" {
		t.Fatal("cross-tenant update must not mutate the row")
	}
}

func strPtr(s string) *string { return &s }

// TestVisibleEdgesFailClosed covers IMP-07 for the organization read
// (WP-335): an edge is kept only when both endpoints are visible; an
// endpoint missing from the set or empty drops it.
func TestVisibleEdgesFailClosed(t *testing.T) {
	edges := []edgeRow{
		{id: "e1", sourceCIID: "a", targetCIID: "b"},
		{id: "e2", sourceCIID: "b", targetCIID: "hidden"},
		{id: "e3", sourceCIID: "unknown", targetCIID: "a"},
		{id: "e4", sourceCIID: "a", targetCIID: ""},
		{id: "e5", sourceCIID: "b", targetCIID: "b"},
	}
	kept := visibleEdges(edges, edgeRowEnds, map[string]bool{"a": true, "b": true, "hidden": false})
	if len(kept) != 2 || kept[0].id != "e1" || kept[1].id != "e5" {
		t.Errorf("kept %+v, want e1 and e5", kept)
	}
}

// TestSelectWalkMatchesSelectTraversal: the selection of a walk on CI
// numbers (WP-611) keeps the same edges as the shared semantics, node limit
// and depth included, on random graphs with hidden CIs, self-loops and
// parallel edges; ties of the node limit go by id as in the text form. The
// CIs are numbered from the root and each one but the root ends an edge; a
// hidden root keeps nothing.
func TestSelectWalkMatchesSelectTraversal(t *testing.T) {
	rng := rand.New(rand.NewPCG(611, 1))
	randomID := func() uuid {
		var id uuid
		for i := range id {
			id[i] = byte(rng.IntN(256))
		}
		return id
	}
	types, origins := []string{"connected_to", "powered_by", "contains"}, []string{"discovery", "manual", ProjectionSource}
	compared := 0
	for graph := 0; graph < 300; graph++ {
		n := 2 + rng.IntN(15)
		ids := make([]uuid, n)
		for i := range ids {
			ids[i] = randomID()
		}
		hidden := map[uuid]bool{}
		for i := 0; i < rng.IntN(3); i++ {
			hidden[ids[rng.IntN(n)]] = true
		}
		g := newWalkGraph(ids[0])
		for _, i := range rng.Perm(n) {
			if number := g.ci(ids[i]); i == 0 && number != 0 {
				t.Fatalf("the root is number %d", number)
			}
		}
		var visible []Relationship
		for e := 0; e < n+rng.IntN(3*n); e++ {
			id := randomID()
			row := walkRow{id: keyOf(&id), source: g.ci(ids[rng.IntN(n)]), target: g.ci(ids[rng.IntN(n)]),
				relType: g.intern([]byte(types[rng.IntN(len(types))])), origin: g.intern([]byte(origins[rng.IntN(len(origins))]))}
			g.edges = append(g.edges, row)
			if !hidden[g.ids[row.source]] && !hidden[g.ids[row.target]] {
				visible = append(visible, Relationship{ID: formatUUID(&id), SourceCIID: formatUUID(&g.ids[row.source]),
					TargetCIID: formatUUID(&g.ids[row.target]), RelType: g.text[row.relType], Source: g.text[row.origin]})
			}
		}
		edges := slices.Clone(g.edges)
		root := formatUUID(&ids[0])
		for _, depth := range []int{1, 2, 3, 20} {
			for _, maxNodes := range []int{1, 2, 4, 8, MaxTraversalNodes} {
				for v := range g.state {
					g.state[v] = ciVisible
					if hidden[g.ids[v]] {
						g.state[v] = ciHidden
					}
				}
				g.edges = slices.Clone(edges)
				walk := g.selectWalk(depth, maxNodes)
				got := make([]Relationship, 0, len(walk.Edges))
				ends := map[int32]bool{0: true}
				for _, e := range walk.Edges {
					got = append(got, Relationship{ID: e.ID, SourceCIID: walk.CIs[e.SourceCI], TargetCIID: walk.CIs[e.TargetCI],
						RelType: e.RelType, Source: e.Source})
					ends[e.SourceCI], ends[e.TargetCI] = true, true
				}
				want := SelectTraversal(root, visible, depth, maxNodes)
				if hidden[ids[0]] {
					want = []Relationship{}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("graph %d, depth %d, %d nodes: %+v, want %+v", graph, depth, maxNodes, got, want)
				}
				if walk.CIs[0] != root || len(ends) != len(walk.CIs) || len(walk.CIs) > max(maxNodes, 1) {
					t.Fatalf("graph %d, depth %d, %d nodes: CIs %v for %d edges", graph, depth, maxNodes, walk.CIs, len(got))
				}
				compared += len(got)
			}
		}
	}
	t.Logf("%d edges compared", compared)
}

// TestMemoryTraversalVisibility covers IMP-07 in the memory profile
// (WP-335): with a NodeVisibility an invisible intermediate CI connects
// nothing, and an error of the check fails the walk.
func TestMemoryTraversalVisibility(t *testing.T) {
	ctx := context.Background()
	hidden := map[string]bool{"b": true}
	var asked []string
	repo := NewMemoryRepository().WithNodeVisibility(func(_ context.Context, orgID string, ids []string) (map[string]bool, error) {
		if orgID != "org-1" {
			return nil, errors.New("wrong organization")
		}
		asked = ids
		out := map[string]bool{}
		for _, id := range ids {
			out[id] = !hidden[id]
		}
		return out, nil
	})
	for _, e := range [][2]string{{"a", "b"}, {"b", "c"}, {"a", "d"}} {
		if err := repo.Create(ctx, &Relationship{OrganizationID: "org-1", SourceCIID: e[0], TargetCIID: e[1], RelType: "connected_to"}); err != nil {
			t.Fatal(err)
		}
	}
	rels, err := repo.TraverseFrom(ctx, "org-1", "a", 5, MaxTraversalNodes)
	if err != nil || len(rels) != 1 || rels[0].TargetCIID != "d" || len(asked) != 4 {
		t.Errorf("walk past an invisible CI: %+v %v (asked for %v)", rels, err, asked)
	}
	if _, err := repo.TraverseFrom(ctx, "org-2", "a", 5, MaxTraversalNodes); err != nil {
		t.Errorf("an organization without edges asks nothing: %v", err)
	}
	repo.WithNodeVisibility(func(context.Context, string, []string) (map[string]bool, error) {
		return nil, errors.New("visibility unavailable")
	})
	if _, err := repo.TraverseFrom(ctx, "org-1", "a", 5, MaxTraversalNodes); err == nil {
		t.Error("a failed visibility check returned a walk")
	}
}
