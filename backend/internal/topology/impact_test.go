package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// flags reduces an impact result to the heuristic flag per CI.
func flags(hits map[string]impactHit) map[string]bool {
	out := map[string]bool{}
	for id, hit := range hits {
		out[id] = hit.heuristic
	}
	return out
}

var testRules = func() map[string]impactRule {
	return (&Handler{}).impactRules(context.Background(), "org-1")
}()

// TestComputeImpactFollowsImpactDirection covers IMP-01 (WP-176): device
// powered_by pdu and vm hosted_on server read "source depends on target",
// so the failure of the pdu reaches the server and over it the vm, never
// the other way round.
func TestComputeImpactFollowsImpactDirection(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "app", TargetCIID: "vm", RelType: "runs_on"},
		{ID: "e4", SourceCIID: "server", TargetCIID: "monitor", RelType: "monitored_by"},
	}
	all := flags(computeImpact([]string{"pdu"}, edges, impactQuery{}, testRules))
	for _, id := range []string{"server", "vm", "app"} {
		if heuristic, ok := all[id]; !ok || heuristic {
			t.Errorf("%s: impacted %v, heuristic %v", id, ok, heuristic)
		}
	}
	if _, ok := all["monitor"]; ok || len(all) != 3 {
		t.Errorf("impact %v: the monitoring CI does not depend on the server", all)
	}
	// The failure of a dependent never reaches what it depends on.
	if got := flags(computeImpact([]string{"vm"}, edges, impactQuery{}, testRules)); len(got) != 1 {
		t.Errorf("failure of the vm: %v, want only the app", got)
	}
	if got := flags(computeImpact([]string{"server"}, edges, impactQuery{}, testRules)); len(got) != 2 {
		t.Errorf("failure of the server: %v, want vm and app", got)
	}
	// target_depends_on_source: the contained CI depends on its container.
	contains := []Edge{{ID: "c", SourceCIID: "chassis", TargetCIID: "blade", RelType: "contains"}}
	if got := flags(computeImpact([]string{"chassis"}, contains, impactQuery{}, testRules)); len(got) != 1 {
		t.Errorf("contains: %v", got)
	}
	if got := flags(computeImpact([]string{"blade"}, contains, impactQuery{}, testRules)); len(got) != 0 {
		t.Errorf("contains backwards: %v", got)
	}
}

// TestComputeImpactConnectedToIsHeuristic covers IMP-03: connected_to is
// undirected and propagates both ways, but only as a marked heuristic; a CI
// that a directed path reaches as well is not heuristic.
func TestComputeImpactConnectedToIsHeuristic(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "switch", TargetCIID: "server", RelType: "connected_to"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "router", TargetCIID: "switch", RelType: "connected_to"},
		{ID: "e4", SourceCIID: "server2", TargetCIID: "switch", RelType: "uplink_to"},
	}
	got := flags(computeImpact([]string{"switch"}, edges, impactQuery{}, testRules))
	want := map[string]bool{"server": true, "vm": true, "router": true, "server2": false}
	if len(got) != len(want) {
		t.Fatalf("impact %v, want %v", got, want)
	}
	for id, heuristic := range want {
		if h, ok := got[id]; !ok || h != heuristic {
			t.Errorf("%s: impacted %v heuristic %v, want heuristic %v", id, ok, h, heuristic)
		}
	}
	// Undirected: the failure of the server reaches the switch as well.
	if got = flags(computeImpact([]string{"server"}, edges, impactQuery{}, testRules)); !got["switch"] {
		t.Errorf("connected_to backwards: %v", got)
	}
	// The rel_type facet keeps one class.
	if got = flags(computeImpact([]string{"switch"}, edges, impactQuery{relType: "uplink_to"}, testRules)); len(got) != 1 || got["server2"] {
		t.Errorf("uplink facet: %v", got)
	}
}

// TestImpactRulesUseOrganizationTypes: a custom type follows its own
// impact_direction; none does not propagate.
func TestImpactRulesUseOrganizationTypes(t *testing.T) {
	types := relationshiptype.NewMemoryRepository()
	if err := types.Create(context.Background(), &relationshiptype.Type{OrganizationID: "org-1", Key: "feeds", DisplayName: "feeds",
		Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactTargetDependsOnSource}); err != nil {
		t.Fatal(err)
	}
	if err := types.Create(context.Background(), &relationshiptype.Type{OrganizationID: "org-1", Key: "documents", DisplayName: "documents",
		Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactNone}); err != nil {
		t.Fatal(err)
	}
	rules := (&Handler{types: types}).impactRules(context.Background(), "org-1")
	edges := []Edge{
		{ID: "e1", SourceCIID: "a", TargetCIID: "b", RelType: "feeds"},
		{ID: "e2", SourceCIID: "a", TargetCIID: "c", RelType: "documents"},
	}
	if got := flags(computeImpact([]string{"a"}, edges, impactQuery{}, rules)); len(got) != 1 || got["b"] {
		t.Errorf("custom types: %v", got)
	}
}

// TestImpactSkipsDeletedCIs covers CI-05: a deleted CI is no node of the
// impact walk, so what is only reachable through it is not impacted
// either.
func TestImpactSkipsDeletedCIs(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	mux := chi.NewRouter()
	NewHandler(ciRepo, relRepo).RegisterRoutes(mux)
	impacted := func() map[string]bool {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+ids["pdu1"]))
		var body struct {
			Impacted []Node `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("impact: %d %s", w.Code, w.Body.String())
		}
		out := map[string]bool{}
		for _, n := range body.Impacted {
			out[n.ID] = true
		}
		return out
	}
	// srv1 powered_by pdu1, sw1 connected_to srv1 (heuristic).
	if before := impacted(); !before[ids["srv1"]] || !before[ids["sw1"]] {
		t.Fatalf("seed graph: impacted %v", before)
	}
	if err := ciRepo.Delete(context.Background(), "org-1", ids["srv1"]); err != nil {
		t.Fatal(err)
	}
	if after := impacted(); len(after) != 0 {
		t.Errorf("impact passes through a deleted CI: %v", after)
	}
}

// TestComputeImpactPathsAndCategories covers IMP-02 (WP-177): every
// impacted CI carries its shortest path with the type sequence; categories
// narrow the walk; the walk stops after the depth.
func TestComputeImpactPathsAndCategories(t *testing.T) {
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "app", TargetCIID: "vm", RelType: "runs_on"},
		// A longer way to the app: app depends on the server directly too.
		{ID: "e4", SourceCIID: "app", TargetCIID: "server", RelType: "depends_on"},
		{ID: "e5", SourceCIID: "pdu", TargetCIID: "blade", RelType: "contains"},
	}
	hits := computeImpact([]string{"pdu"}, edges, impactQuery{depth: maxDepth}, testRules)
	app := impactPath(hits, "app")
	if hits["app"].hops != 2 || len(app) != 2 || app[0].RelType != "powered_by" || app[1].RelType != "depends_on" ||
		app[0].FromCIID != "pdu" || app[1].ToCIID != "app" {
		t.Errorf("shortest path to the app: %+v", app)
	}
	if blade := impactPath(hits, "blade"); len(blade) != 1 || blade[0].Category != "physical" {
		t.Errorf("projected containment: %+v", blade)
	}
	power := computeImpact([]string{"pdu"}, edges, impactQuery{categories: map[string]bool{"power": true}, depth: maxDepth}, testRules)
	if len(power) != 1 || power["server"].hops != 1 || len(impactPath(power, "server")) != 1 {
		t.Errorf("power category: %v", power)
	}
	physical := computeImpact([]string{"pdu"}, edges, impactQuery{categories: map[string]bool{"physical": true}, depth: maxDepth}, testRules)
	if _, ok := physical["blade"]; !ok || len(physical) != 1 {
		t.Errorf("physical category: %v", physical)
	}
	if one := computeImpact([]string{"pdu"}, edges, impactQuery{depth: 1}, testRules); len(one) != 2 {
		t.Errorf("depth 1: %v, want server and blade", one)
	}
	if _, err := parseCategories("power, physical"); err != nil {
		t.Error(err)
	}
	if _, err := parseCategories("power,cooling"); err == nil {
		t.Error("unknown category accepted")
	}
}

// TestComputeImpactTwentyHops: the walk reaches 20 hops, not 21 (IMP-02).
func TestComputeImpactTwentyHops(t *testing.T) {
	var edges []Edge
	for i := 0; i < 21; i++ {
		edges = append(edges, Edge{ID: fmt.Sprintf("e%02d", i), SourceCIID: fmt.Sprintf("n%02d", i+1), TargetCIID: fmt.Sprintf("n%02d", i), RelType: "hosted_on"})
	}
	hits := computeImpact([]string{"n00"}, edges, impactQuery{depth: maxDepth}, testRules)
	if path := impactPath(hits, "n20"); len(hits) != 20 || hits["n20"].hops != 20 || len(path) != 20 || path[0].FromCIID != "n00" {
		t.Errorf("impact over 21 hops: %d CIs, path to n20 %+v", len(hits), path)
	}
	if _, ok := hits["n21"]; ok {
		t.Error("the 21st hop was reached")
	}
}

// countingCIs counts the reads of the CI repository during a graph walk.
type countingCIs struct {
	*ci.MemoryRepository
	single, batch, nodes int
}

func (c *countingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.single++
	return c.MemoryRepository.GetByID(ctx, orgID, id)
}

func (c *countingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.batch++
	return c.MemoryRepository.GetByIDs(ctx, orgID, ids)
}

func (c *countingCIs) ListNodes(ctx context.Context, orgID string, ids []string) ([]ci.Node, error) {
	c.nodes++
	return c.MemoryRepository.ListNodes(ctx, orgID, ids)
}

// TestImpactReadsNodesInOneQuery covers NFR-02: the impact of a CI with
// many dependents reads the root and then all nodes in one read of the
// node projection (WP-335), not one query per node and not the full CIs.
func TestImpactReadsNodesInOneQuery(t *testing.T) {
	cis := &countingCIs{MemoryRepository: ci.NewMemoryRepository()}
	rels := relationship.NewMemoryRepository()
	add := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active"}
		if err := cis.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	pdu := add("pdu")
	for i := 0; i < 30; i++ {
		srv := add(fmt.Sprintf("srv-%02d", i))
		if err := rels.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1", SourceCIID: srv, TargetCIID: pdu, RelType: "powered_by"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	NewHandler(cis, rels).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+pdu+"?categories=power"))
	var body struct {
		Count    int          `json:"count"`
		Impacted []ImpactNode `json:"impacted"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Count != 30 {
		t.Fatalf("impact: %d %s", w.Code, w.Body.String())
	}
	if first := body.Impacted[0]; first.Hops != 1 || len(first.Path) != 1 || first.Path[0].RelType != "powered_by" ||
		first.Via != (Via{CIID: pdu, RelType: "powered_by"}) || first.Relay != nil {
		t.Errorf("path: %+v", first)
	}
	if cis.single > 2 || cis.batch != 0 || cis.nodes != 1 {
		t.Errorf("CI reads: %d single, %d batch, %d node reads; want the root and one node read", cis.single, cis.batch, cis.nodes)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", "/api/v1/impact/"+pdu+"?categories=cooling"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown category: %d", w.Code)
	}
}

// TestImpactFields covers API-02 on the impact (WP-335): fields= narrows
// every impacted CI to its id and the selected fields and builds no path
// unless it is selected; without fields the answer keeps every field of
// before plus via (API-01, additive only); an unknown field is 422; the
// deprecated topology path answers in the full form.
func TestImpactFields(t *testing.T) {
	ciRepo, relRepo, ids := seed(t)
	mux := chi.NewRouter()
	NewHandler(ciRepo, relRepo).RegisterRoutes(mux)
	impacted := func(path string) []map[string]json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", path))
		var body struct {
			Impacted []map[string]json.RawMessage `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Impacted) != 2 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return body.Impacted
	}
	keys := func(entry map[string]json.RawMessage) string {
		out := make([]string, 0, len(entry))
		for k := range entry {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	full := "ci_type,client_id,heuristic,hops,id,name,path,status,via"
	for _, path := range []string{"/api/v1/impact/" + ids["pdu1"], "/api/v1/impact?ci_ids=" + ids["pdu1"],
		"/api/v1/topology/cis/" + ids["pdu1"] + "/impact?fields=hops"} {
		for _, entry := range impacted(path) {
			if got := keys(entry); got != full {
				t.Errorf("%s: fields %s, want %s", path, got, full)
			}
		}
	}
	for _, path := range []string{"/api/v1/impact/" + ids["pdu1"] + "?fields=hops,via,heuristic", "/api/v1/impact?fields=hops,,via,%20heuristic&ci_ids=" + ids["pdu1"]} {
		for _, entry := range impacted(path) {
			if got := keys(entry); got != "heuristic,hops,id,via" {
				t.Errorf("%s: fields %s", path, got)
			}
			if string(entry["id"]) == `"`+ids["srv1"]+`"` && string(entry["via"]) != `{"ci_id":"`+ids["pdu1"]+`","rel_type":"powered_by"}` {
				t.Errorf("%s: via of srv1 %s", path, entry["via"])
			}
		}
	}
	// A selected field keeps the omission rule of the full form: client_id
	// is left out where it is empty, heuristic false is returned.
	for _, entry := range impacted("/api/v1/impact/" + ids["pdu1"] + "?fields=id,path,client_id,heuristic") {
		if got := keys(entry); got != "client_id,heuristic,id,path" {
			t.Errorf("fields with path: %s", got)
		}
	}
	for _, path := range []string{"/api/v1/impact/" + ids["pdu1"] + "?fields=hops,paths", "/api/v1/impact?fields=x&ci_ids=" + ids["pdu1"]} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantReq("GET", path))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"fields"`) {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// TestImpactFieldsMatchSpec: the fields fields= selects are the properties
// of ImpactNode (the full entry) and of ImpactNodeFields (the sparse entry)
// in api/openapi.yaml, and the sparse entry requires its id only, because
// fields= leaves every other property out (API-02, WP-335).
func TestImpactFieldsMatchSpec(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Ref        string         `yaml:"$ref"`
		Required   []string       `yaml:"required"`
		Properties map[string]any `yaml:"properties"`
		AllOf      []schema       `yaml:"allOf"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]schema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var properties func(s schema) []string
	properties = func(s schema) []string {
		out := slices.Collect(maps.Keys(s.Properties))
		if s.Ref != "" {
			out = append(out, properties(doc.Components.Schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")])...)
		}
		for _, sub := range s.AllOf {
			out = append(out, properties(sub)...)
		}
		slices.Sort(out)
		return out
	}
	want := slices.Sorted(slices.Values(impactFields))
	for _, name := range []string{"ImpactNode", "ImpactNodeFields"} {
		if got := properties(doc.Components.Schemas[name]); !slices.Equal(got, want) {
			t.Errorf("%s documents %v, fields= selects %v", name, got, want)
		}
	}
	if got := doc.Components.Schemas["ImpactNodeFields"].Required; !slices.Equal(got, []string{"id"}) {
		t.Errorf("ImpactNodeFields requires %v, want only the id", got)
	}
}

// TestImpactShortcutKeepsPaths: a heuristic CI whose shortest path passes a
// certain CI over an undirected shortcut keeps that path and its hops; the
// certain CI keeps its directed path and names the shortcut as relay
// (IMP-02, IMP-03, WP-335).
func TestImpactShortcutKeepsPaths(t *testing.T) {
	// a fails; b hosted_on a, p hosted_on b (certain, two hops); a
	// connected_to p (one heuristic hop); x connected_to p.
	edges := []Edge{
		{ID: "e1", SourceCIID: "b", TargetCIID: "a", RelType: "hosted_on"},
		{ID: "e2", SourceCIID: "p", TargetCIID: "b", RelType: "hosted_on"},
		{ID: "e3", SourceCIID: "a", TargetCIID: "p", RelType: "connected_to"},
		{ID: "e4", SourceCIID: "x", TargetCIID: "p", RelType: "connected_to"},
	}
	for _, depth := range []int{2, maxDepth} {
		hits := computeImpact([]string{"a"}, edges, impactQuery{depth: depth}, testRules)
		p, x := hits["p"], hits["x"]
		if p.heuristic || p.hops != 2 || p.via.via() != (Via{CIID: "b", RelType: "hosted_on"}) ||
			p.relay == nil || p.relay.via() != (Via{CIID: "a", RelType: "connected_to"}) {
			t.Errorf("depth %d: certain p %+v", depth, p)
		}
		if !x.heuristic || x.hops != 2 || x.via.via() != (Via{CIID: "p", RelType: "connected_to"}) {
			t.Errorf("depth %d: heuristic x %+v", depth, x)
		}
		want := []PathStep{{FromCIID: "a", ToCIID: "p", RelType: "connected_to", Category: "network", Heuristic: true},
			{FromCIID: "p", ToCIID: "x", RelType: "connected_to", Category: "network", Heuristic: true}}
		if got := impactPath(hits, "x"); !reflect.DeepEqual(got, want) {
			t.Errorf("depth %d: path to x %+v", depth, got)
		}
		if got := impactPath(hits, "p"); len(got) != 2 || got[0].ToCIID != "b" || got[1].RelType != "hosted_on" {
			t.Errorf("depth %d: path to p %+v", depth, got)
		}
	}
}

// TestComputeImpactViaRebuildsPaths covers the via encoding of WP-335: on
// the graphs of these tests and on random graphs with every facet and
// depth, the impacted CIs and their heuristic flags equal the former
// computation (legacyComputeImpact), and the path a client rebuilds from
// via, relay and the type rules alone equals the former path step by step,
// as does impactPath.
func TestComputeImpactViaRebuildsPaths(t *testing.T) {
	rules := maps.Clone(testRules)
	// An undirected redundant type and a reverse type exercise the
	// redundancy check on heuristic steps.
	rules["mesh"] = impactRule{direction: relationshiptype.ImpactUndirected, category: "network", redundant: true}
	rules["feeds"] = impactRule{direction: relationshiptype.ImpactTargetDependsOnSource, category: "logical"}
	types := []string{"powered_by", "hosted_on", "connected_to", "contains", "depends_on", "member_of_cluster",
		"uplink_to", "runs_on", "mesh", "feeds", "monitors", "unknown_type"}
	type graphCase struct {
		name   string
		edges  []Edge
		failed []string
	}
	cases := []graphCase{
		{"direction", []Edge{
			{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
			{ID: "e2", SourceCIID: "vm", TargetCIID: "server", RelType: "hosted_on"},
			{ID: "e3", SourceCIID: "app", TargetCIID: "vm", RelType: "runs_on"},
			{ID: "e4", SourceCIID: "app", TargetCIID: "server", RelType: "depends_on"},
			{ID: "e5", SourceCIID: "pdu", TargetCIID: "blade", RelType: "contains"},
		}, []string{"pdu"}},
		{"shortcut", []Edge{
			{ID: "e1", SourceCIID: "b", TargetCIID: "a", RelType: "hosted_on"},
			{ID: "e2", SourceCIID: "p", TargetCIID: "b", RelType: "hosted_on"},
			{ID: "e3", SourceCIID: "a", TargetCIID: "p", RelType: "connected_to"},
			{ID: "e4", SourceCIID: "x", TargetCIID: "p", RelType: "connected_to"},
		}, []string{"a"}},
	}
	rng := rand.New(rand.NewPCG(335, 2))
	for g := 0; g < 500; g++ {
		n := 4 + rng.IntN(11)
		var edges []Edge
		for e := 0; e < n+rng.IntN(2*n); e++ {
			edges = append(edges, Edge{ID: fmt.Sprintf("e%03d", e), SourceCIID: fmt.Sprintf("n%02d", rng.IntN(n)),
				TargetCIID: fmt.Sprintf("n%02d", rng.IntN(n)), RelType: types[rng.IntN(len(types))]})
		}
		var failed []string
		for f := 0; f < 1+rng.IntN(3); f++ {
			failed = append(failed, fmt.Sprintf("n%02d", rng.IntN(n)))
		}
		cases = append(cases, graphCase{fmt.Sprintf("random %d", g), edges, failed})
	}
	queries := []impactQuery{{}, {relType: "connected_to"}, {relType: "powered_by"},
		{categories: map[string]bool{"power": true, "hosting": true}}, {categories: map[string]bool{"network": true}}}
	compared, relays := 0, 0
	for _, c := range cases {
		for _, base := range queries {
			for _, depth := range []int{1, 2, 3, maxDepth} {
				q := base
				q.depth = depth
				legacy := legacyComputeImpact(c.failed, c.edges, q, rules)
				hits := computeImpact(c.failed, c.edges, q, rules)
				if len(hits) != len(legacy) {
					t.Fatalf("%s %+v: %d impacted, former %d", c.name, q, len(hits), len(legacy))
				}
				// What a client receives: id, heuristic, hops, via, relay.
				answer := map[string]ImpactNode{}
				for id, hit := range hits {
					entry := ImpactNode{Node: Node{ID: id}, Heuristic: hit.heuristic, Hops: hit.hops, Via: hit.via.via()}
					if hit.relay != nil {
						relay := hit.relay.via()
						entry.Relay = &relay
						relays++
					}
					answer[id] = entry
				}
				for id, old := range legacy {
					hit, ok := hits[id]
					if !ok || hit.heuristic != old.heuristic || hit.hops != len(old.path) {
						t.Fatalf("%s %+v: %s impacted %v %+v, former %+v", c.name, q, id, ok, hit, old)
					}
					if got := rebuildFromVia(answer, id, rules); !reflect.DeepEqual(got, old.path) {
						t.Fatalf("%s %+v: path to %s rebuilt from via %+v, former %+v", c.name, q, id, got, old.path)
					}
					if got := impactPath(hits, id); !reflect.DeepEqual(got, old.path) {
						t.Fatalf("%s %+v: impactPath to %s %+v, former %+v", c.name, q, id, got, old.path)
					}
					compared++
				}
			}
		}
	}
	t.Logf("%d impacted CIs compared on %d graphs, %d of them with a relay", compared, len(cases), relays)
}

// rebuildFromVia rebuilds the path to id the way a client does from the
// answer: back along via to a failed CI; on the path of a heuristic CI
// over the relay of a certain CI where it has one. Category and heuristic
// flag of a step come from the type's rule.
func rebuildFromVia(answer map[string]ImpactNode, id string, rules map[string]impactRule) []PathStep {
	target := answer[id]
	path := make([]PathStep, target.Hops)
	to := id
	for i := target.Hops - 1; i >= 0; i-- {
		at := answer[to]
		step := at.Via
		if target.Heuristic && at.Relay != nil {
			step = *at.Relay
		}
		rule := ruleOf(rules, step.RelType)
		path[i] = PathStep{FromCIID: step.CIID, ToCIID: to, RelType: step.RelType, Category: rule.category,
			Heuristic: rule.direction == relationshiptype.ImpactUndirected}
		to = step.CIID
	}
	return path
}

// TestComputeImpactClosesDownOnce: one analysis computes the downward
// closure once for both walks (WP-335), and its levels equal the former
// closures without and with undirected steps on random graphs.
func TestComputeImpactClosesDownOnce(t *testing.T) {
	calls := 0
	t.Cleanup(func() { closeDown = downLevels })
	closeDown = func(g *ciGraph, failed []int32, steps []stepRule) []downLevel {
		calls++
		return downLevels(g, failed, steps)
	}
	edges := []Edge{
		{ID: "e1", SourceCIID: "server", TargetCIID: "pdu", RelType: "powered_by"},
		{ID: "e2", SourceCIID: "sw", TargetCIID: "server", RelType: "connected_to"},
	}
	if hits := computeImpact([]string{"pdu"}, edges, impactQuery{depth: maxDepth}, testRules); len(hits) != 2 || calls != 1 {
		t.Errorf("%d impacted, %d closures; want 2 and 1", len(hits), calls)
	}

	types := []string{"powered_by", "hosted_on", "connected_to", "member_of_cluster", "contains", "depends_on", "mesh"}
	rules := maps.Clone(testRules)
	rules["mesh"] = impactRule{direction: relationshiptype.ImpactUndirected, category: "network", redundant: true}
	rng := rand.New(rand.NewPCG(335, 4))
	for g := 0; g < 500; g++ {
		n := 3 + rng.IntN(10)
		var edges []Edge
		for e := 0; e < n+rng.IntN(2*n); e++ {
			edges = append(edges, Edge{ID: fmt.Sprintf("e%03d", e), SourceCIID: fmt.Sprintf("n%02d", rng.IntN(n)),
				TargetCIID: fmt.Sprintf("n%02d", rng.IntN(n)), RelType: types[rng.IntN(len(types))]})
		}
		failed := map[string]bool{fmt.Sprintf("n%02d", rng.IntN(n)): true, fmt.Sprintf("n%02d", rng.IntN(n)): true}
		level := downClosure(failed, edges, rules)
		certain := legacyDownSet(failed, edges, rules, func(_ *Edge, _ impactRule, heuristic bool) bool { return !heuristic })
		all := legacyDownSet(failed, edges, rules, func(*Edge, impactRule, bool) bool { return true })
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("n%02d", i)
			want := levelUp
			switch {
			case certain[id]:
				want = levelCertain
			case all[id]:
				want = levelHeuristic
			}
			if level[id] != want {
				t.Fatalf("graph %d %v failed %v: %s level %d, former closures %d", g, edges, failed, id, level[id], want)
			}
		}
	}
}

// legacyHit, legacyComputeImpact, legacyStepFilter and legacyDownSet are
// the impact computation before WP-335 (two closures, a copied path per
// CI), kept as the oracle of the tests above; impactStep and providerKey
// are its step and provider key on ids.
type legacyHit struct {
	heuristic bool
	path      []PathStep
}

type legacyStepFilter func(e *Edge, rule impactRule, heuristic bool) bool

func legacyComputeImpact(failedIDs []string, edges []Edge, q impactQuery, rules map[string]impactRule) map[string]legacyHit {
	failed := map[string]bool{}
	for _, id := range failedIDs {
		failed[id] = true
	}
	incident := make(map[string][]int, len(edges))
	for i := range edges {
		incident[edges[i].SourceCIID] = append(incident[edges[i].SourceCIID], i)
		if edges[i].TargetCIID != edges[i].SourceCIID {
			incident[edges[i].TargetCIID] = append(incident[edges[i].TargetCIID], i)
		}
	}
	walk := func(withHeuristic bool) map[string][]PathStep {
		follow := func(e *Edge, rule impactRule) bool {
			if q.relType != "" && e.RelType != q.relType {
				return false
			}
			return len(q.categories) == 0 || q.categories[rule.category]
		}
		down := legacyDownSet(failed, edges, rules, func(e *Edge, rule impactRule, heuristic bool) bool {
			return follow(e, rule) && (withHeuristic || !heuristic)
		})
		paths := map[string][]PathStep{}
		queue := make([]string, 0, len(failedIDs))
		for _, id := range failedIDs {
			if _, ok := paths[id]; !ok {
				paths[id] = []PathStep{}
				queue = append(queue, id)
			}
		}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if q.depth > 0 && len(paths[current]) >= q.depth {
				continue
			}
			for _, i := range incident[current] {
				rule := ruleOf(rules, edges[i].RelType)
				if !follow(&edges[i], rule) {
					continue
				}
				to, heuristic, ok := impactStep(&edges[i], current, rule)
				if !ok || (heuristic && !withHeuristic) || !down[to] {
					continue
				}
				if _, seen := paths[to]; seen {
					continue
				}
				prev := paths[current]
				path := make([]PathStep, len(prev), len(prev)+1)
				copy(path, prev)
				paths[to] = append(path, PathStep{FromCIID: current, ToCIID: to, RelType: edges[i].RelType,
					Category: rule.category, Heuristic: heuristic})
				queue = append(queue, to)
			}
		}
		for id := range failed {
			delete(paths, id)
		}
		return paths
	}
	certain := walk(false)
	impacted := map[string]legacyHit{}
	for id, path := range walk(true) {
		if sure, ok := certain[id]; ok {
			impacted[id] = legacyHit{path: sure}
			continue
		}
		impacted[id] = legacyHit{heuristic: true, path: path}
	}
	return impacted
}

func legacyDownSet(failed map[string]bool, edges []Edge, rules map[string]impactRule, follow legacyStepFilter) map[string]bool {
	providers := map[providerKey][]string{}
	for i := range edges {
		rule := ruleOf(rules, edges[i].RelType)
		if !rule.redundant {
			continue
		}
		for _, from := range []string{edges[i].SourceCIID, edges[i].TargetCIID} {
			if to, _, ok := impactStep(&edges[i], from, rule); ok {
				key := providerKey{to, edges[i].RelType}
				providers[key] = append(providers[key], from)
			}
		}
	}
	incident := make(map[string][]int, len(edges))
	for i := range edges {
		incident[edges[i].SourceCIID] = append(incident[edges[i].SourceCIID], i)
		if edges[i].TargetCIID != edges[i].SourceCIID {
			incident[edges[i].TargetCIID] = append(incident[edges[i].TargetCIID], i)
		}
	}
	down := map[string]bool{}
	queue := make([]string, 0, len(failed))
	for id := range failed {
		down[id] = true
		queue = append(queue, id)
	}
	allDown := func(ids []string) bool {
		for _, id := range ids {
			if !down[id] {
				return false
			}
		}
		return true
	}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, i := range incident[from] {
			rule := ruleOf(rules, edges[i].RelType)
			to, heuristic, ok := impactStep(&edges[i], from, rule)
			if !ok || down[to] || !follow(&edges[i], rule, heuristic) {
				continue
			}
			if rule.redundant && !allDown(providers[providerKey{to, edges[i].RelType}]) {
				continue
			}
			down[to] = true
			queue = append(queue, to)
		}
	}
	return down
}

// impactStep returns the CI a failure of from reaches over edge, if any.
// heuristic is true for an undirected edge.
func impactStep(edge *Edge, from string, rule impactRule) (to string, heuristic, ok bool) {
	switch rule.direction {
	case relationshiptype.ImpactSourceDependsOnTarget:
		if edge.TargetCIID == from {
			return edge.SourceCIID, false, true
		}
	case relationshiptype.ImpactTargetDependsOnSource:
		if edge.SourceCIID == from {
			return edge.TargetCIID, false, true
		}
	case relationshiptype.ImpactUndirected:
		switch from {
		case edge.SourceCIID:
			return edge.TargetCIID, true, true
		case edge.TargetCIID:
			return edge.SourceCIID, true, true
		}
	}
	return "", false, false
}

// providerKey identifies the providers of one dependent over one type.
type providerKey struct{ dependent, relType string }

// The tests state the impact on graphs given by id strings. computeImpact,
// impactPath, downClosure and redundancyFindings number the CIs of such a
// graph and run the analysis of the handler on the numbers (WP-611):
// impactOf, ciGraph.path, downLevels and redundancyOf.

// impactHit is a ciHit by id; it keeps its analysis for impactPath.
type impactHit struct {
	heuristic bool
	hops      int
	via       impactLink
	relay     *impactLink
	graph     *ciGraph
	hits      []ciHit
	n         int32
}

// impactLink is a ciLink by id.
type impactLink struct {
	from, relType, category string
	heuristic               bool
}

func (l impactLink) via() Via { return Via{CIID: l.from, RelType: l.relType} }

// edgeGraph numbers the failed CIs and then the ends of the edges in their
// order, each a known node; the edges keep their order.
func edgeGraph(failedIDs []string, edges []Edge) (*ciGraph, []int32) {
	g := &ciGraph{}
	number := map[string]int32{}
	add := func(id string) int32 {
		if n, ok := number[id]; ok {
			return n
		}
		number[id] = int32(len(g.ids))
		g.ids, g.nodes, g.known = append(g.ids, id), append(g.nodes, Node{ID: id}), append(g.known, true)
		return number[id]
	}
	failed := make([]int32, len(failedIDs))
	for i, id := range failedIDs {
		failed[i] = add(id)
	}
	types := map[string]int32{}
	for _, e := range edges {
		g.edges = append(g.edges, ciEdge{id: e.ID, source: add(e.SourceCIID), target: add(e.TargetCIID),
			relType: g.typeNumber(types, e.RelType), origin: e.Source})
	}
	return g, failed
}

// computeImpact is impactOf on a graph given by its edges, by id.
func computeImpact(failedIDs []string, edges []Edge, q impactQuery, rules map[string]impactRule) map[string]impactHit {
	g, failed := edgeGraph(failedIDs, edges)
	hits := impactOf(g, failed, q, rules)
	link := func(l *ciLink) impactLink {
		return impactLink{from: g.ids[l.from], relType: g.relTypes[l.relType], category: l.category, heuristic: l.heuristic}
	}
	out := map[string]impactHit{}
	for n := range hits {
		if !hits[n].impacted {
			continue
		}
		hit := impactHit{heuristic: hits[n].heuristic, hops: hits[n].hops, via: link(&hits[n].via), graph: g, hits: hits, n: int32(n)}
		if hits[n].relay != nil {
			relay := link(hits[n].relay)
			hit.relay = &relay
		}
		out[g.ids[n]] = hit
	}
	return out
}

// impactPath is ciGraph.path to the impacted CI id.
func impactPath(hits map[string]impactHit, id string) []PathStep {
	hit, ok := hits[id]
	if !ok {
		return []PathStep{}
	}
	return hit.graph.path(hit.hits, hit.n)
}

// downClosure is downLevels on a graph given by its edges, every step
// followed, by id.
func downClosure(failed map[string]bool, edges []Edge, rules map[string]impactRule) map[string]downLevel {
	g, numbers := edgeGraph(slices.Sorted(maps.Keys(failed)), edges)
	level := downLevels(g, numbers, g.stepRules(rules, impactQuery{}))
	out := make(map[string]downLevel, len(level))
	for n := range level {
		out[g.ids[n]] = level[n]
	}
	return out
}

// redundancyFindings is redundancyOf on graph with the hits of
// computeImpact.
func redundancyFindings(failedID string, graph Graph, hits map[string]impactHit, rules map[string]impactRule) (spof, protected []Node) {
	nodes := map[string]Node{}
	for _, n := range graph.Nodes {
		nodes[n.ID] = n
	}
	g, number := graphOf(failedID, nodes, graph.Edges)
	impacted := make([]ciHit, len(g.ids))
	for id, hit := range hits {
		if n, ok := number[id]; ok {
			impacted[n] = ciHit{impacted: true, heuristic: hit.heuristic}
		}
	}
	return redundancyOf(g, number[failedID], impacted, rules)
}
