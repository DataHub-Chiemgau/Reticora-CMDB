package topology_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestTopologyRootAndLimitAgainstPostgres covers F-161 and IMP-06 (WP-403)
// against PostgreSQL, through the production repositories under RLS:
//   - A root_ci_id the caller does not see answers 404 "ci not found": an
//     unknown UUID, a CI of the other organization, a CI of client 2 for a
//     client-1 principal, a CI at site 2 for a site-1 principal and a
//     deleted CI, each with the same problem and without the id; the own
//     CI answers 200, a root that is no UUID 400 on root_ci_id.
//   - Probe TestProbeV25TopologyLimit as a repository test: without a root,
//     exactly 2,000 CIs answer 200 with 2,000 nodes and 2,001 answer 422 on
//     root_ci_id, naming root_ci_id, site_id, client_id and ci_type; a root
//     answers 200 at 2,001 CIs; site_id, client_id and ci_type return
//     exactly the matching CIs; a filter that is no UUID is 400.
//
// It shares its fixture tag with TestMultiImpactAgainstPostgres: the tests
// of a package run one after another.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTopologyRootAndLimitAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b8")
	bg := context.Background()
	as := func(scope *database.TenantScope) context.Context {
		return tenant.WithTenant(database.ContextWithTenantScope(bg, scope),
			tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: f.User})
	}
	orgA, orgB := database.OrgWideScope(f.OrgA, f.User), database.OrgWideScope(f.OrgB, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)

	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "limit "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	site1Scope := database.OrgWideScope(f.OrgA, f.User)
	site1Scope.Sites = database.ScopeIDs(site1)
	var serverType, switchType string
	if err := f.Admin.QueryRow(bg, `SELECT (SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL),
		(SELECT id::text FROM ci_type WHERE key = 'switch' AND organization_id IS NULL)`).Scan(&serverType, &switchType); err != nil {
		t.Fatal(err)
	}
	// insert adds n active CIs of organization A; client, site and type
	// may be nil (org-wide, no site, server).
	insert := func(n int, name string, client, site, typeID any) []string {
		t.Helper()
		if typeID == nil {
			typeID = serverType
		}
		rows, err := f.Admin.Query(bg, `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status)
			SELECT $1, $2::uuid, $3::uuid, $4::uuid, $5::text || '-' || g, 'active' FROM generate_series(1, $6::int) g RETURNING id::text`,
			f.OrgA, client, site, typeID, name, n)
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil || len(ids) != n {
			t.Fatalf("insert %s: %d of %d, %v", name, len(ids), n, err)
		}
		return ids
	}

	// Organization A: 10 CIs of client 1 at site 1 (own, its neighbour and
	// 8 more), 11 CIs of client 2, one at site 2, one switch, one deleted.
	atSite1 := insert(10, "limit-site1", f.Client1, site1, nil)
	own, neighbour := atSite1[0], atSite1[1]
	ofClient2 := insert(11, "limit-client2", f.Client2, nil, nil)
	atSite2 := insert(1, "limit-site2", f.Client1, site2, nil)[0]
	switches := insert(1, "limit-switch", nil, nil, switchType)
	deleted := insert(1, "limit-deleted", f.Client1, site1, nil)[0]
	ciB := f.CI(t, f.OrgB, "", "limit-b")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'depends_on'), ($1, $2, $4, 'depends_on')`, f.OrgA, own, neighbour, deleted); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(ctx context.Context, path string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w.Code, w.Body.String()
	}
	nodes := func(ctx context.Context, path string) []string {
		t.Helper()
		code, body := call(ctx, path)
		var g topology.Graph
		if code != http.StatusOK || json.Unmarshal([]byte(body), &g) != nil {
			t.Fatalf("%s: %d %.300s", path, code, body)
		}
		ids := make([]string, 0, len(g.Nodes))
		for _, n := range g.Nodes {
			ids = append(ids, n.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string {
		out := slices.Clone(ids)
		slices.Sort(out)
		return out
	}

	t.Run("root not visible", func(t *testing.T) {
		// Each answer is the same problem but for the trace id of the
		// request, and none names the CI.
		answers := map[string]string{}
		for _, c := range []struct {
			name  string
			ctx   context.Context
			root  string
			hints []string
		}{
			{"unknown UUID", as(&orgA), "00000000-0000-4000-8000-0000000000ff", nil},
			{"CI of organization B", as(&orgA), ciB, []string{"limit-b"}},
			{"CI of organization A for organization B", as(&orgB), own, []string{"limit-site1"}},
			{"CI of client 2 for client 1", as(&client1), ofClient2[0], []string{"limit-client2"}},
			{"CI at site 2 for site 1", as(&site1Scope), atSite2, []string{"limit-site2"}},
			{"deleted CI", as(&orgA), deleted, []string{"limit-deleted"}},
		} {
			code, body := call(c.ctx, "/api/v1/topology?root_ci_id="+c.root+"&depth=3")
			var p map[string]any
			if code != http.StatusNotFound || json.Unmarshal([]byte(body), &p) != nil || p["detail"] != "ci not found" || p["status"] != float64(404) {
				t.Errorf("%s: %d %s, want 404 ci not found", c.name, code, body)
			}
			for _, hint := range append(c.hints, c.root) {
				if strings.Contains(body, hint) {
					t.Errorf("%s: the answer names %q: %s", c.name, hint, body)
				}
			}
			delete(p, "trace_id")
			answers[c.name] = fmt.Sprint(p)
		}
		for name, answer := range answers {
			if answer != answers["unknown UUID"] {
				t.Errorf("%s answers %s, an unknown UUID %s", name, answer, answers["unknown UUID"])
			}
		}

		// Visible roots answer; the deleted neighbour stays out.
		for _, c := range []struct {
			name string
			ctx  context.Context
			root string
			want []string
		}{
			{"own CI", as(&orgA), own, sorted(own, neighbour)},
			{"own CI for client 1", as(&client1), own, sorted(own, neighbour)},
			{"own CI for site 1", as(&site1Scope), own, sorted(own, neighbour)},
			{"CI of client 2 org-wide", as(&orgA), ofClient2[0], []string{ofClient2[0]}},
			{"CI at site 2 org-wide", as(&orgA), atSite2, []string{atSite2}},
			{"CI of organization B for organization B", as(&orgB), ciB, []string{ciB}},
		} {
			if got := nodes(c.ctx, "/api/v1/topology?root_ci_id="+c.root); !slices.Equal(got, c.want) {
				t.Errorf("%s: nodes %v, want %v", c.name, got, c.want)
			}
		}

		code, body := call(as(&orgA), "/api/v1/topology?root_ci_id=not-a-uuid")
		var fp struct {
			Field string `json:"field"`
		}
		if code != http.StatusBadRequest || json.Unmarshal([]byte(body), &fp) != nil || fp.Field != "root_ci_id" {
			t.Errorf("root not-a-uuid: %d %s, want 400 on root_ci_id", code, body)
		}
	})

	t.Run("limit and filters", func(t *testing.T) {
		// Org-wide servers fill organization A up to exactly 2,000 CIs.
		seeded := slices.Concat(atSite1, ofClient2, []string{atSite2}, switches)
		all := sorted(slices.Concat(seeded, insert(topology.MaxTopologyNodes-len(seeded), "limit-bulk", nil, nil, nil))...)
		// Statistics as after autoanalyze. Before WP-602 the answer read the
		// relationships of each of the 2,000 CIs and took about 50 s with the
		// statistics of the nearly empty table; checkTopologyWithoutRoot
		// covers the answer without ANALYZE.
		if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
			t.Fatal(err)
		}
		if got := nodes(as(&orgA), "/api/v1/topology"); !slices.Equal(got, all) || len(got) != topology.MaxTopologyNodes {
			t.Fatalf("topology of exactly 2000 CIs: %d nodes, want the %d CIs of organization A", len(got), len(all))
		}

		insert(1, "limit-over", nil, nil, nil)
		code, body := call(as(&orgA), "/api/v1/topology")
		var fp struct {
			Field  string `json:"field"`
			Detail string `json:"detail"`
		}
		if code != http.StatusUnprocessableEntity || json.Unmarshal([]byte(body), &fp) != nil || fp.Field != "root_ci_id" {
			t.Fatalf("topology of 2001 CIs: %d %s, want 422 on root_ci_id", code, body)
		}
		for _, hint := range []string{"2001", "root_ci_id", "site_id", "client_id", "ci_type"} {
			if !strings.Contains(fp.Detail, hint) {
				t.Errorf("422 detail %q does not name %s", fp.Detail, hint)
			}
		}

		for _, c := range []struct {
			name, path string
			want       []string
		}{
			{"root at 2001 CIs", "/api/v1/topology?root_ci_id=" + own, sorted(own, neighbour)},
			{"site 1", "/api/v1/topology?site_id=" + site1, sorted(atSite1...)},
			{"site 2", "/api/v1/topology?site_id=" + site2, []string{atSite2}},
			{"client 2", "/api/v1/topology?client_id=" + f.Client2, sorted(ofClient2...)},
			{"switch type", "/api/v1/topology?ci_type=" + switchType, switches},
			{"client 1 at site 1", "/api/v1/topology?client_id=" + f.Client1 + "&site_id=" + site1, sorted(atSite1...)},
		} {
			if got := nodes(as(&orgA), c.path); !slices.Equal(got, c.want) {
				t.Errorf("%s: nodes %v, want %v", c.name, got, c.want)
			}
		}
		// The edge between own and its neighbour is in the site view.
		code, body = call(as(&orgA), "/api/v1/topology?site_id="+site1)
		var g topology.Graph
		if code != http.StatusOK || json.Unmarshal([]byte(body), &g) != nil || len(g.Edges) != 1 ||
			g.Edges[0].SourceCIID != own || g.Edges[0].TargetCIID != neighbour {
			t.Errorf("site 1 edges: %d %+v", code, g.Edges)
		}

		for _, path := range []string{
			"/api/v1/topology?site_id=not-a-uuid",
			"/api/v1/topology?client_id=not-a-uuid",
			"/api/v1/topology?ci_type=not-a-uuid",
			"/api/v1/topology?root_ci_id=not-a-uuid&ci_type=" + switchType,
		} {
			if code, body := call(as(&orgA), path); code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", path, code, body)
			}
		}
	})
}
