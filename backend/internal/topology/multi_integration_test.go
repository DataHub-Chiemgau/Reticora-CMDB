package topology_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// TestMultiImpactAgainstPostgres covers IMP-05 and IMP-06 (WP-180) against
// PostgreSQL: GET /impact?ci_ids= analyses several failing CIs together
// (one of two supplies keeps the server up, both take it down) with the
// failed CIs, the impacted CIs with paths and a summary; /impact/{ci_id}
// stays the short form; GET /topology without a root over 2,000 CIs is
// 422. WP-403: 100 CIs answer, 101 are 422 on ci_ids; ci_ids with a CI of
// the other organization or, for a client-1 principal, of client 2 is 404
// like an unknown id, only own CIs answer 200. WP-840: the analysis of
// several CIs is one walk from all of them (WalkFromMany); its answer
// equals the one of a walk per failed CI merged afterwards, the path
// before WP-840, byte for byte (truncated only appears for a walk the
// node limit cut), for the organization and a client-1
// principal, over depths, categories, a relationship type and fields=
// (checkMultiImpactEqualsPerRootWalks).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMultiImpactAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b8")
	bg := context.Background()
	ctxOf := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	pduA := f.CI(t, f.OrgA, f.Client1, "multi-pdu-a")
	pduB := f.CI(t, f.OrgA, f.Client1, "multi-pdu-b")
	server := f.CI(t, f.OrgA, f.Client1, "multi-server")
	vm := f.CI(t, f.OrgA, f.Client1, "multi-vm")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'powered_by'), ($1, $2, $4, 'powered_by'), ($1, $5, $2, 'hosted_on')`, f.OrgA, server, pduA, pduB, vm); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(org, path string) (int, []byte) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctxOf(org)))
		return w.Code, w.Body.Bytes()
	}
	type answer struct {
		FailedCIs []topology.Node        `json:"failed_cis"`
		Impacted  []topology.ImpactNode  `json:"impacted"`
		Summary   topology.ImpactSummary `json:"summary"`
	}
	impact := func(path string) answer {
		t.Helper()
		code, body := call(f.OrgA, path)
		var a answer
		if code != http.StatusOK || json.Unmarshal(body, &a) != nil {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		return a
	}
	if one := impact("/api/v1/impact?ci_ids=" + pduA); len(one.Impacted) != 0 || one.Summary.Failed != 1 {
		t.Errorf("one supply: %+v", one)
	}
	both := impact("/api/v1/impact?ci_ids=" + pduA + "," + pduB)
	if len(both.FailedCIs) != 2 || len(both.Impacted) != 2 || both.Summary.Impacted != 2 || both.Summary.Certain != 2 ||
		both.Summary.ByCategory["power"] != 1 || both.Summary.ByCategory["hosting"] != 1 {
		t.Errorf("both supplies: %+v", both)
	}
	for i := range both.Impacted {
		if both.Impacted[i].ID == vm && (both.Impacted[i].Hops != 2 || both.Impacted[i].Path[1].RelType != "hosted_on") {
			t.Errorf("path to the vm: %+v", both.Impacted[i])
		}
	}
	if short := impact("/api/v1/impact/" + server); len(short.Impacted) != 1 || short.Impacted[0].ID != vm || len(short.FailedCIs) != 1 {
		t.Errorf("short form: %+v", short)
	}
	for path, want := range map[string]int{
		"/api/v1/impact":                                  http.StatusBadRequest,
		"/api/v1/impact?ci_ids=" + f.ID():                 http.StatusNotFound,
		"/api/v1/impact?ci_ids=" + pduA + "&categories=x": http.StatusUnprocessableEntity,
	} {
		if code, body := call(f.OrgA, path); code != want {
			t.Errorf("%s: %d, want %d (%s)", path, code, want, body)
		}
	}

	// WP-403: at most 100 CIs per analysis; a CI of organization B or, for
	// a client-1 principal, of client 2 among them is not found, like an
	// unknown id; the own CIs answer.
	ofClient2 := f.CI(t, f.OrgA, f.Client2, "multi-client2")
	ofOrgB := f.CI(t, f.OrgB, "", "multi-org-b")
	clientScope := database.OrgWideScope(f.OrgA, f.User)
	clientScope.Clients = database.ScopeIDs(f.Client1)
	client1 := tenant.WithTenant(database.ContextWithTenantScope(bg, &clientScope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	callAs := func(ctx context.Context, path string) (int, []byte) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w.Code, w.Body.Bytes()
	}
	own := []string{pduA, pduB, server, vm}
	rows, err := f.Admin.Query(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, ci_type_id, 'multi-many-' || g, 'active' FROM ci, generate_series(1, 97) g WHERE ci.id = $3
		RETURNING id::text`, f.OrgA, f.Client1, pduA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		own = append(own, id)
	}
	rows.Close()
	if len(own) != topology.MaxImpactCIs+1 {
		t.Fatalf("seeded %d own CIs, want %d", len(own), topology.MaxImpactCIs+1)
	}
	if hundred := impact("/api/v1/impact?ci_ids=" + strings.Join(own[:topology.MaxImpactCIs], ",")); len(hundred.FailedCIs) != topology.MaxImpactCIs ||
		hundred.Summary.Failed != topology.MaxImpactCIs {
		t.Errorf("100 CIs: %d failed, summary %+v", len(hundred.FailedCIs), hundred.Summary)
	}
	var fp struct {
		Field string `json:"field"`
	}
	if code, body := call(f.OrgA, "/api/v1/impact?ci_ids="+strings.Join(own, ",")); code != http.StatusUnprocessableEntity ||
		json.Unmarshal(body, &fp) != nil || fp.Field != "ci_ids" {
		t.Errorf("101 CIs: %d %s, want 422 on ci_ids", code, body)
	}
	for _, c := range []struct {
		name    string
		ctx     context.Context
		ids     []string
		missing string
	}{
		{"organization A with a CI of organization B", ctxOf(f.OrgA), []string{pduA, ofOrgB}, ofOrgB},
		{"organization B with a CI of organization A", ctxOf(f.OrgB), []string{ofOrgB, pduA}, pduA},
		{"client 1 with a CI of client 2", client1, []string{pduA, ofClient2}, ofClient2},
		{"client 1 with only a CI of client 2", client1, []string{ofClient2}, ofClient2},
	} {
		code, body := callAs(c.ctx, "/api/v1/impact?ci_ids="+strings.Join(c.ids, ","))
		var p struct {
			Detail string `json:"detail"`
		}
		if code != http.StatusNotFound || json.Unmarshal(body, &p) != nil || p.Detail != "ci not found: "+c.missing {
			t.Errorf("%s: %d %s, want 404 ci not found: %s", c.name, code, body, c.missing)
		}
		if strings.Contains(string(body), "multi-") {
			t.Errorf("%s: the answer names a CI: %s", c.name, body)
		}
	}
	for _, c := range []struct {
		name string
		ctx  context.Context
		ids  []string
	}{
		{"client 1 with its own CIs", client1, []string{pduA, pduB}},
		{"organization A with a CI of client 2", ctxOf(f.OrgA), []string{pduA, ofClient2}},
		{"organization B with its own CI", ctxOf(f.OrgB), []string{ofOrgB}},
	} {
		code, body := callAs(c.ctx, "/api/v1/impact?ci_ids="+strings.Join(c.ids, ","))
		var a answer
		if code != http.StatusOK || json.Unmarshal(body, &a) != nil || len(a.FailedCIs) != len(c.ids) {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}

	t.Run("equals the walks per root", func(t *testing.T) {
		checkMultiImpactEqualsPerRootWalks(t, f, ctxOf(f.OrgA), client1)
	})

	// Over 2,000 CIs the topology without a root is 422; a type filter
	// that narrows below the limit answers.
	var serverType, switchType string
	if err := f.Admin.QueryRow(bg, `SELECT (SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL),
		(SELECT id::text FROM ci_type WHERE key = 'switch' AND organization_id IS NULL)`).Scan(&serverType, &switchType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, $2, 'bulk-' || g, 'active' FROM generate_series(1, 2001) g`, f.OrgB, serverType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status) VALUES ($1, $2, 'bulk-switch', 'active')`,
		f.OrgB, switchType); err != nil {
		t.Fatal(err)
	}
	if code, body := call(f.OrgB, "/api/v1/topology"); code != http.StatusUnprocessableEntity {
		t.Errorf("topology over 2000 CIs: %d %s", code, body)
	}
	code, body := call(f.OrgB, "/api/v1/topology?ci_type="+switchType)
	var g topology.Graph
	if code != http.StatusOK || json.Unmarshal(body, &g) != nil || len(g.Nodes) != 1 {
		t.Errorf("topology by type id: %d %s", code, body)
	}
}

// perRootWalks hides the walk of the PostgreSQL repository
// (relationship.Walker): the impact of several CIs then walks from each
// failed CI with TraverseFrom, the walk of WalkFrom, and merges the graphs
// (mergeGraphs), the path before WP-840.
type perRootWalks struct {
	relationship.Repository
	relationship.Traverser
	relationship.IncidentEdgeReader
	relationship.HiddenEdgeReader
}

// checkMultiImpactEqualsPerRootWalks seeds 80 CIs of organization A with
// overlapping neighbourhoods (WP-840): two PDUs each powered by one of four
// supplies, every third by a second one (a redundant type, IMP-04), chains
// of hosted_on, connected_to (heuristic, IMP-03), depends_on and the
// projected containment; every fifth CI belongs to client 2, every seventh
// to no client. GET /impact?ci_ids= with one walk from all failed CIs
// answers as the walks per failed CI, for the organization and for a
// client-1 principal (IMP-07), without truncated: the walk was not cut.
func checkMultiImpactEqualsPerRootWalks(t *testing.T, f *scopetest.Fixture, org, client1 context.Context) {
	bg := context.Background()
	const n = 80
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
		SELECT md5($1 || '/cmp/' || i)::uuid, $1::uuid,
			CASE WHEN i % 7 = 0 THEN NULL WHEN i % 5 = 0 THEN $3::uuid ELSE $2::uuid END,
			t.id, 'cmp-' || lpad(i::text, 2, '0'), 'active'
		FROM generate_series(1, $4::int) AS i JOIN ci_type t ON t.organization_id IS NULL AND t.key = 'server'`,
		f.OrgA, f.Client1, f.Client2, n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		SELECT $1::uuid, md5($1 || '/cmp/' || e.src)::uuid, md5($1 || '/cmp/' || e.dst)::uuid, e.rel FROM (
			SELECT i AS src, 1 + i % 4 AS dst, 'powered_by' AS rel FROM generate_series(5, $2::int) AS i
			UNION ALL SELECT i, 1 + (i + 1) % 4, 'powered_by' FROM generate_series(5, $2::int) AS i WHERE i % 3 = 0
			UNION ALL SELECT i, i - 4, 'hosted_on' FROM generate_series(9, $2::int) AS i WHERE i % 2 = 0
			UNION ALL SELECT i, i + 1, 'connected_to' FROM generate_series(1, $2::int - 1) AS i WHERE i % 6 = 0
			UNION ALL SELECT i, i + 10, 'depends_on' FROM generate_series(1, $2::int - 10) AS i WHERE i % 9 = 0) e`,
		f.OrgA, n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = md5($1 || '/cmp/' || (g.i - 1))::uuid
		FROM generate_series(2, $2::int) AS g(i) WHERE g.i % 11 = 0 AND ci.id = md5($1 || '/cmp/' || g.i)::uuid`, f.OrgA, n); err != nil {
		t.Fatal(err)
	}
	ids := map[int]string{}
	rows, err := f.Admin.Query(bg, `SELECT i, md5($1 || '/cmp/' || i)::uuid::text FROM generate_series(1, $2::int) AS i`, f.OrgA, n)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var i int
		var id string
		if err := rows.Scan(&i, &id); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	rows.Close()

	routes := func(rels relationship.Repository) *chi.Mux {
		mux := chi.NewRouter()
		topology.NewHandler(ci.NewPGRepository(f.App), rels).
			WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
		return mux
	}
	repo := relationship.NewPGRepository(f.App)
	oneWalk, perRoot := routes(repo), routes(perRootWalks{repo, repo, repo, repo})
	call := func(mux *chi.Mux, ctx context.Context, path string) (int, string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w.Code, w.Body.String()
	}
	// Client 1 sees every CI but those of client 2 (i % 5 = 0, i % 7 != 0).
	sets := [][]int{{1, 2}, {1, 2, 3, 4}, {6, 17, 33}, {12, 24, 36, 48, 66}, {9, 16, 49, 70, 77}, {8, 12, 7, 21, 63, 2}}
	orgSets := append(slices.Clone(sets), []int{5, 10, 15, 22}, []int{20, 40, 61})
	queries := []string{"", "&depth=1", "&depth=2", "&depth=3", "&categories=power", "&categories=hosting,network",
		"&rel_type=hosted_on", "&fields=name,status,heuristic,hops,via,relay", "&fields=path&depth=4"}
	compared := 0
	for _, c := range []struct {
		name string
		ctx  context.Context
		sets [][]int
	}{{"organization", org, orgSets}, {"client 1", client1, sets}} {
		for _, set := range c.sets {
			failed := make([]string, len(set))
			for i, k := range set {
				failed[i] = ids[k]
			}
			for _, q := range queries {
				path := "/api/v1/impact?ci_ids=" + strings.Join(failed, ",") + q
				code, got := call(oneWalk, c.ctx, path)
				wantCode, want := call(perRoot, c.ctx, path)
				if code != http.StatusOK || wantCode != http.StatusOK {
					t.Fatalf("%s, CIs %v%s: %d %.300s, per root %d", c.name, set, q, code, got, wantCode)
				}
				if got != want {
					t.Errorf("%s, CIs %v%s: one walk\n%s\nper root\n%s", c.name, set, q, got, want)
				}
				var body struct {
					Count     int   `json:"count"`
					Truncated *bool `json:"truncated"`
				}
				if err := json.Unmarshal([]byte(got), &body); err != nil || body.Truncated != nil {
					t.Errorf("%s, CIs %v%s: truncated %v, want none (%v)", c.name, set, q, body.Truncated, err)
				}
				if q == "" && body.Count == 0 {
					t.Errorf("%s, CIs %v: nothing impacted, the comparison is empty", c.name, set)
				}
				compared++
			}
		}
	}
	t.Logf("%d answers of one walk equal the walks per failed CI", compared)
}
