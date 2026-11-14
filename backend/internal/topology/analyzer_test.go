package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// analyzerFixture is the memory graph of the analyzer tests (WP-1025): a
// router, two switches uplink_to it, 20 CPEs uplink_to the switches, a
// server powered_by two PDUs and uplink_to switch 1, a vm hosted_on the
// server, an application runs_on the vm, a business service depends_on the
// application, a monitoring CI connected_to switch 2 (heuristic) and a CI
// without edges. The ids are deterministic (memory repositories).
type analyzerFixture struct {
	t     *testing.T
	cis   *ci.MemoryRepository
	rels  *relationship.MemoryRepository
	types *citype.MemoryRepository
	ids   map[string]string
	names map[string]string
}

func newAnalyzerFixture(t *testing.T) *analyzerFixture {
	t.Helper()
	f := &analyzerFixture{t: t, cis: ci.NewMemoryRepository(), types: citype.NewMemoryRepository(),
		ids: map[string]string{}, names: map[string]string{}}
	f.rels = relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(f.cis))
	for _, key := range []string{"business_service", "application", "vm", "server", "switch", "router", "cpe", "pdu", "monitor"} {
		f.types.SeedType(&citype.Type{ID: "type-" + key, Key: key, Name: key, IsSystem: true, IsActive: true})
	}
	f.add("router", "router", "client-a", "site-1")
	f.add("switch-1", "switch", "client-a", "site-1")
	f.add("switch-2", "switch", "client-a", "site-2")
	f.link("switch-1", "uplink_to", "router")
	f.link("switch-2", "uplink_to", "router")
	for i := range 20 {
		name := cpeName(i)
		sw := "switch-1"
		if i%2 == 1 {
			sw = "switch-2"
		}
		f.add(name, "cpe", "client-b", "site-3")
		f.link(name, "uplink_to", sw)
	}
	f.add("pdu-a", "pdu", "client-a", "site-1")
	f.add("pdu-b", "pdu", "client-a", "site-1")
	f.add("server", "server", "client-a", "site-1")
	f.add("vm", "vm", "client-a", "site-1")
	f.add("app", "application", "client-a", "site-1")
	f.add("service", "business_service", "client-c", "site-1")
	f.add("monitor", "monitor", "client-a", "site-2")
	f.add("lonely", "server", "client-a", "site-1")
	f.link("server", "powered_by", "pdu-a")
	f.link("server", "powered_by", "pdu-b")
	f.link("server", "uplink_to", "switch-1")
	f.link("vm", "hosted_on", "server")
	f.link("app", "runs_on", "vm")
	f.link("service", "depends_on", "app")
	f.link("monitor", "connected_to", "switch-2")
	return f
}

func cpeName(i int) string { return "cpe-" + string(rune('a'+i)) }

func (f *analyzerFixture) add(name, ciType, client, site string) {
	f.t.Helper()
	item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "type-" + ciType, Status: "active", ClientID: client, SiteID: site}
	if err := f.cis.Create(context.Background(), item); err != nil {
		f.t.Fatal(err)
	}
	f.ids[name], f.names[item.ID] = item.ID, name
}

func (f *analyzerFixture) link(source, relType, target string) {
	f.t.Helper()
	if err := f.rels.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1",
		SourceCIID: f.ids[source], TargetCIID: f.ids[target], RelType: relType, Source: "manual"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *analyzerFixture) mux() chi.Router {
	mux := chi.NewRouter()
	NewHandler(f.cis, f.rels).WithCITypes(f.types).RegisterRoutes(mux)
	return mux
}

func (f *analyzerFixture) get(mux chi.Router, path string) []byte {
	f.t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq(http.MethodGet, path))
	if w.Code != http.StatusOK {
		f.t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// goldenRoutes are the answers of the impact, blast radius and dependency
// routes the analyzer must keep byte-equal (WP-1025 AK5); the files were
// written before the routes moved onto the analyzer.
func (f *analyzerFixture) goldenRoutes() map[string]string {
	id := f.ids
	return map[string]string{
		"impact-single":            "/api/v1/impact/" + id["router"],
		"impact-single-deprecated": "/api/v1/topology/cis/" + id["switch-2"] + "/impact?depth=3",
		"impact-multi":             "/api/v1/impact?ci_ids=" + id["router"] + "," + id["pdu-a"],
		"impact-multi-pdus-chain": "/api/v1/impact?ci_ids=" + id["pdu-a"] + "," + id["pdu-b"] +
			"&include=services,clients,sites,spof&fields=hops,via,heuristic,path,name",
		"impact-categories":    "/api/v1/impact?ci_ids=" + id["switch-1"] + "&categories=network&rel_type=uplink_to",
		"blast-radius-server":  "/api/v1/cis/" + id["server"] + "/blast-radius",
		"blast-radius-pdu":     "/api/v1/cis/" + id["pdu-a"] + "/blast-radius",
		"blast-radius-router":  "/api/v1/cis/" + id["router"] + "/blast-radius",
		"dependencies-up":      "/api/v1/cis/" + id["app"] + "/dependencies?direction=upstream",
		"dependencies-down":    "/api/v1/cis/" + id["router"] + "/dependencies?direction=downstream&depth=1",
		"dependencies-default": "/api/v1/cis/" + id["server"] + "/dependencies",
	}
}

// TestTopologyRoutesGolden covers WP-1025 AK5 (API-01): the answers of the
// impact (single, multiple, deprecated path, service chain, categories),
// blast radius and dependency routes are byte-equal to the golden files
// written before the switch to the analyzer. RETICORA_UPDATE_TOPOLOGY_GOLDEN=1
// rewrites them.
func TestTopologyRoutesGolden(t *testing.T) {
	f := newAnalyzerFixture(t)
	mux := f.mux()
	update := os.Getenv("RETICORA_UPDATE_TOPOLOGY_GOLDEN") == "1"
	for name, path := range f.goldenRoutes() {
		got := f.get(mux, path)
		file := filepath.Join("testdata", "analyzer", name+".json")
		if update {
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v (RETICORA_UPDATE_TOPOLOGY_GOLDEN=1 writes it)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s (%s): answer differs from the golden file\n got %s\nwant %s", name, path, got, want)
		}
	}
}

// decodeJSON is a test helper.
func decodeJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func (f *analyzerFixture) analyzer() *Analyzer {
	return NewAnalyzer(f.cis, f.rels).WithCITypes(f.types)
}

func (f *analyzerFixture) idsOf(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = f.ids[n]
	}
	return out
}

// TestAnalyzerImpactMatchesHandler covers WP-1025 AK1 and AK2: Impact gives
// the failed and impacted CIs, the summary and the service chain of GET
// /api/v1/impact?ci_ids=… for the same fixture (the golden answers).
func TestAnalyzerImpactMatchesHandler(t *testing.T) {
	f := newAnalyzerFixture(t)
	a := f.analyzer()
	for _, tc := range []struct {
		golden string
		failed []string
		opts   ImpactOptions
	}{
		{"impact-multi", f.idsOf("router", "pdu-a"), ImpactOptions{Paths: true}},
		{"impact-categories", f.idsOf("switch-1"), ImpactOptions{Categories: []string{"network"}, RelType: "uplink_to", Paths: true}},
		{"impact-multi-pdus-chain", f.idsOf("pdu-a", "pdu-b"), ImpactOptions{Paths: true, ServiceChain: true}},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "analyzer", tc.golden+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var want struct {
			FailedCIs []Node          `json:"failed_cis"`
			Impacted  []ImpactNode    `json:"impacted"`
			Summary   ImpactSummary   `json:"summary"`
			Services  []BlastRadiusCI `json:"affected_services"`
			ClientIDs []string        `json:"client_ids"`
			SiteIDs   []string        `json:"site_ids"`
		}
		decodeJSON(t, raw, &want)
		got, err := a.Impact(context.Background(), "org-1", tc.failed, tc.opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.golden, err)
		}
		if !reflect.DeepEqual(got.Failed, want.FailedCIs) || !reflect.DeepEqual(got.Summary, want.Summary) {
			t.Errorf("%s: failed/summary differ\n got %+v %+v\nwant %+v %+v", tc.golden, got.Failed, got.Summary, want.FailedCIs, want.Summary)
		}
		gotImpacted, _ := json.Marshal(got.Impacted)
		if tc.golden == "impact-multi-pdus-chain" {
			// The golden answer narrows the fields (fields=); compare ids,
			// hops, via, heuristic and path.
			if len(got.Impacted) != len(want.Impacted) {
				t.Fatalf("%s: %d impacted, want %d", tc.golden, len(got.Impacted), len(want.Impacted))
			}
			for i := range want.Impacted {
				w, g := want.Impacted[i], got.Impacted[i]
				if w.ID != g.ID || w.Hops != g.Hops || w.Via != g.Via || w.Heuristic != g.Heuristic || !reflect.DeepEqual(w.Path, g.Path) {
					t.Errorf("%s: impacted %d: got %+v, want %+v", tc.golden, i, g, w)
				}
			}
			// The services by content: id, name, type and heuristic mark
			// (the fields the golden answer carries).
			type service struct {
				ID, Name, CIType string
				Heuristic        bool
			}
			services := func(list []BlastRadiusCI) []service {
				out := make([]service, len(list))
				for i, s := range list {
					out[i] = service{ID: s.ID, Name: s.Name, CIType: s.CIType, Heuristic: s.Heuristic}
				}
				return out
			}
			if len(want.Services) == 0 {
				t.Fatalf("%s: the golden answer names no service", tc.golden)
			}
			if !reflect.DeepEqual(services(got.Services), services(want.Services)) || !reflect.DeepEqual(got.ClientIDs, want.ClientIDs) || !reflect.DeepEqual(got.SiteIDs, want.SiteIDs) {
				t.Errorf("%s: service chain %v %v %v, want %v %v %v", tc.golden, got.Services, got.ClientIDs, got.SiteIDs, want.Services, want.ClientIDs, want.SiteIDs)
			}
			continue
		}
		wantImpacted, _ := json.Marshal(want.Impacted)
		if !bytes.Equal(gotImpacted, wantImpacted) {
			t.Errorf("%s: impacted differ\n got %s\nwant %s", tc.golden, gotImpacted, wantImpacted)
		}
	}
	// An unknown id is ErrNotFound naming the first missing id.
	missing := "00000999-0000-0000-0000-000000000999"
	_, err := a.Impact(context.Background(), "org-1", []string{f.ids["router"], missing, "x"}, ImpactOptions{})
	var nf *NotFoundError
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &nf) || nf.ID != missing {
		t.Fatalf("unknown id: %v, want ErrNotFound for %s", err, missing)
	}
	if _, err := a.Impact(context.Background(), "org-2", f.idsOf("router"), ImpactOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CI of another organization: %v, want ErrNotFound", err)
	}
}

// TestAnalyzerProtectedNamesProvider covers AK2: a server on two PDUs
// (powered_by), one fails; Protected names the server with the second PDU.
func TestAnalyzerProtectedNamesProvider(t *testing.T) {
	f := newAnalyzerFixture(t)
	got, err := f.analyzer().Impact(context.Background(), "org-1", f.idsOf("pdu-a"), ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Impacted) != 0 {
		t.Errorf("one PDU of two: impacted %v, want none", got.Impacted)
	}
	want := []ProtectedCI{{Node: Node{ID: f.ids["server"], Name: "server", CIType: "type-server", Status: "active", ClientID: "client-a", SiteID: "site-1"},
		Providers: []Provider{{CIID: f.ids["pdu-b"], RelType: "powered_by"}}}}
	if !reflect.DeepEqual(got.Protected, want) {
		t.Fatalf("protected %+v, want %+v", got.Protected, want)
	}
	// Both PDUs: the server goes down, nobody is protected.
	both, err := f.analyzer().Impact(context.Background(), "org-1", f.idsOf("pdu-a", "pdu-b"), ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Protected) != 0 || both.Summary.Certain != 4 {
		t.Fatalf("both PDUs: protected %v, certain %d", both.Protected, both.Summary.Certain)
	}
}

// rootNames runs Roots and maps the answer to names.
func (f *analyzerFixture) rootNames(t *testing.T, a *Analyzer, failed []string) map[string]string {
	t.Helper()
	roots, err := a.Roots(context.Background(), "org-1", f.idsOf(failed...))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for ciID, root := range roots {
		out[f.names[ciID]] = f.names[root]
	}
	return out
}

// routerOutage is the failed set of a router outage: router, both
// switches and the 20 CPEs.
func routerOutage() []string {
	failed := []string{"router", "switch-1", "switch-2"}
	for i := range 20 {
		failed = append(failed, cpeName(i))
	}
	return failed
}

// TestAnalyzerRootsRouterSwitchCPE covers AK4 (ALR-03): router, two
// switches and 20 CPEs failed: all switches and CPEs have the router as
// root; an unrelated failed CI is its own root.
func TestAnalyzerRootsRouterSwitchCPE(t *testing.T) {
	f := newAnalyzerFixture(t)
	got := f.rootNames(t, f.analyzer(), append(routerOutage(), "lonely"))
	if len(got) != 24 {
		t.Fatalf("%d assignments, want 24: %v", len(got), got)
	}
	for _, name := range routerOutage() {
		if got[name] != "router" {
			t.Errorf("%s: root %q, want router", name, got[name])
		}
	}
	if got["lonely"] != "lonely" {
		t.Errorf("lonely: root %q, want itself", got["lonely"])
	}
	// Without the router the switches are roots of their CPEs.
	without := f.rootNames(t, f.analyzer(), routerOutage()[1:])
	for i := range 20 {
		want := "switch-1"
		if i%2 == 1 {
			want = "switch-2"
		}
		if without[cpeName(i)] != want {
			t.Errorf("%s: root %q, want %s", cpeName(i), without[cpeName(i)], want)
		}
	}
}

// TestAnalyzerRootsOrderIndependent: 200 random permutations of failed
// give the same assignment.
func TestAnalyzerRootsOrderIndependent(t *testing.T) {
	f := newAnalyzerFixture(t)
	a := f.analyzer()
	failed := append(routerOutage(), "pdu-a", "pdu-b", "server", "vm", "app", "service", "monitor", "lonely")
	want := f.rootNames(t, a, failed)
	rng := rand.New(rand.NewPCG(1025, 7))
	for i := range 200 {
		perm := slices.Clone(failed)
		rng.Shuffle(len(perm), func(x, y int) { perm[x], perm[y] = perm[y], perm[x] })
		if got := f.rootNames(t, a, perm); !maps.Equal(got, want) {
			t.Fatalf("permutation %d %v: %v, want %v", i, perm, got, want)
		}
	}
	// The server hangs on switch 1 (uplink_to): the router is its root.
	if want["server"] != "router" || want["service"] != "router" || want["pdu-a"] != "pdu-a" || want["pdu-b"] != "pdu-b" {
		t.Errorf("server %q, service %q, pdu-a %q, pdu-b %q", want["server"], want["service"], want["pdu-a"], want["pdu-b"])
	}
}

// TestAnalyzerRootsJointRedundancy: the joint failure of both PDUs takes
// the server down; it follows from neither alone, so it and its dependents
// take the smallest root of the failed CIs the failure comes from.
func TestAnalyzerRootsJointRedundancy(t *testing.T) {
	f := newAnalyzerFixture(t)
	got := f.rootNames(t, f.analyzer(), []string{"vm", "pdu-b", "server", "pdu-a"})
	want := map[string]string{"pdu-a": "pdu-a", "pdu-b": "pdu-b", "server": "pdu-a", "vm": "pdu-a"}
	if !maps.Equal(got, want) {
		t.Fatalf("roots %v, want %v", got, want)
	}
	// One PDU alone does not take the server down: it is its own root.
	if got := f.rootNames(t, f.analyzer(), []string{"pdu-a", "server"}); got["server"] != "server" {
		t.Fatalf("one PDU: %v", got)
	}
}

// TestAnalyzerRootsHeuristicIsNoRoot: over connected_to (undirected,
// heuristic) no assignment; the monitoring CI stays its own root.
func TestAnalyzerRootsHeuristicIsNoRoot(t *testing.T) {
	f := newAnalyzerFixture(t)
	got := f.rootNames(t, f.analyzer(), []string{"switch-2", "monitor"})
	if got["monitor"] != "monitor" || got["switch-2"] != "switch-2" {
		t.Fatalf("heuristic path assigned a root: %v", got)
	}
}

// TestAnalyzerRootsCycle: two CIs that follow from each other (a directed
// cycle) have the smaller id as root, their dependents too.
func TestAnalyzerRootsCycle(t *testing.T) {
	f := newAnalyzerFixture(t)
	f.add("ring-a", "server", "client-a", "site-1")
	f.add("ring-b", "server", "client-a", "site-1")
	f.add("ring-leaf", "vm", "client-a", "site-1")
	f.link("ring-a", "depends_on", "ring-b")
	f.link("ring-b", "depends_on", "ring-a")
	f.link("ring-leaf", "hosted_on", "ring-b")
	got := f.rootNames(t, f.analyzer(), []string{"ring-b", "ring-leaf", "ring-a"})
	smaller := "ring-a"
	if f.ids["ring-b"] < f.ids["ring-a"] {
		smaller = "ring-b"
	}
	for _, name := range []string{"ring-a", "ring-b", "ring-leaf"} {
		if got[name] != smaller {
			t.Errorf("%s: root %q, want %s", name, got[name], smaller)
		}
	}
}

// TestAnalyzerUpstreamMatchesDependencies covers AK3: Upstream equals GET
// /cis/{id}/dependencies?direction=upstream.
func TestAnalyzerUpstreamMatchesDependencies(t *testing.T) {
	f := newAnalyzerFixture(t)
	mux := f.mux()
	for _, name := range []string{"app", "cpe-a", "server", "router"} {
		for _, depth := range []int{1, 2, 20} {
			var want DependencyResult
			decodeJSON(t, f.get(mux, "/api/v1/cis/"+f.ids[name]+"/dependencies?direction=upstream&depth="+strconv.Itoa(depth)), &want)
			got, err := f.analyzer().Upstream(context.Background(), "org-1", f.ids[name], depth)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want.Nodes) {
				t.Errorf("%s depth %d: %v, want %v", name, depth, got, want.Nodes)
			}
		}
	}
	if _, err := f.analyzer().Upstream(context.Background(), "org-1", "nope", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown CI: %v", err)
	}
}

// TestAnalyzerHasNoHTTPDependency covers AK1: analyzer.go imports neither
// net/http nor the api package of the handlers.
func TestAnalyzerHasNoHTTPDependency(t *testing.T) {
	src, err := os.ReadFile("analyzer.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"net/http"`, `internal/api"`} {
		if bytes.Contains(src, []byte(forbidden)) {
			t.Errorf("analyzer.go imports %s", forbidden)
		}
	}
}

// TestAnalyzerImpactEmptySet: a worker with an empty failure set (nil, or
// only empty ids) gets an empty result, no panic and no error.
func TestAnalyzerImpactEmptySet(t *testing.T) {
	a := newAnalyzerFixture(t).analyzer()
	for _, failed := range [][]string{nil, {}, {" "}, {"", "  "}} {
		got, err := a.Impact(context.Background(), "org-1", failed, ImpactOptions{Paths: true, ServiceChain: true})
		if err != nil {
			t.Fatalf("%q: %v", failed, err)
		}
		if len(got.Failed) != 0 || len(got.Impacted) != 0 || got.Summary.Impacted != 0 || len(got.Services) != 0 || len(got.Protected) != 0 {
			t.Errorf("%q: %+v, want an empty result", failed, got)
		}
		roots, err := a.Roots(context.Background(), "org-1", failed)
		if err != nil || len(roots) != 0 {
			t.Errorf("%q: roots %v %v, want none", failed, roots, err)
		}
	}
}

// failingCIs answers GetByID with err (a lost connection, a timeout).
type failingCIs struct {
	*ci.MemoryRepository
	err error
}

func (r failingCIs) GetByID(context.Context, string, string) (*ci.Item, error) { return nil, r.err }

func (r failingCIs) GetByIDs(context.Context, string, []string) ([]ci.Item, error) { return nil, r.err }

// TestAnalyzerPassesRepositoryErrors: a repository error other than not
// found reaches the worker as is, never as ErrNotFound (a worker would drop
// the CI instead of retrying); not found stays ErrNotFound.
func TestAnalyzerPassesRepositoryErrors(t *testing.T) {
	f := newAnalyzerFixture(t)
	id := "00000001-0000-0000-0000-000000000001"
	for _, cause := range []error{context.DeadlineExceeded, errors.New("get ci by id: conn closed")} {
		a := NewAnalyzer(failingCIs{f.cis, cause}, f.rels).WithCITypes(f.types)
		ctx := context.Background()
		if _, err := a.Impact(ctx, "org-1", []string{id}, ImpactOptions{}); !errors.Is(err, cause) || errors.Is(err, ErrNotFound) {
			t.Errorf("Impact one CI with %v: %v", cause, err)
		}
		if _, err := a.Impact(ctx, "org-1", []string{id, f.ids["router"]}, ImpactOptions{}); !errors.Is(err, cause) || errors.Is(err, ErrNotFound) {
			t.Errorf("Impact two CIs with %v: %v", cause, err)
		}
		if _, err := a.Upstream(ctx, "org-1", id, 0); !errors.Is(err, cause) || errors.Is(err, ErrNotFound) {
			t.Errorf("Upstream with %v: %v", cause, err)
		}
		if _, err := a.Roots(ctx, "org-1", []string{id}); !errors.Is(err, cause) || errors.Is(err, ErrNotFound) {
			t.Errorf("Roots with %v: %v", cause, err)
		}
	}
	a := NewAnalyzer(failingCIs{f.cis, fmt.Errorf("get: %w", ci.ErrNotFound)}, f.rels).WithCITypes(f.types)
	if _, err := a.Upstream(context.Background(), "org-1", id, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Upstream with not found: %v, want ErrNotFound", err)
	}
	if _, err := a.Roots(context.Background(), "org-1", []string{id}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Roots with not found: %v, want ErrNotFound", err)
	}
}
