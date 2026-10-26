package savedview_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// grants is a PermissionChecker granting exactly the listed keys.
type grants map[string]bool

func (g grants) HasPermission(_ context.Context, _, _, key string) (bool, error) { return g[key], nil }

// TestStructuredSearchScopeDepthAndPermission covers WP-034 (SRC-03,
// SRC-04): the structured search checks the read permission of the queried
// entity kind, runs under the principal's full scope, stops upstream and
// downstream walks after five hops and never walks through an invisible or
// deleted CI. WP-460 adds a site-scoped principal, assets under the client
// scope and requests whose own permissions are narrower than the user's.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredSearchScopeDepthAndPermission(t *testing.T) {
	f := scopetest.Seed(t, "4e")
	bg := context.Background()

	// chain[0] -> chain[1] -> ... -> chain[6] (client 1)
	chain := make([]string, 7)
	for i := range chain {
		chain[i] = f.CI(t, f.OrgA, f.Client1, "chain-"+string(rune('a'+i)))
	}
	// root -> foreign (client 2) -> behindForeign (client 1)
	// root -> deleted            -> behindDeleted
	foreign := f.CI(t, f.OrgA, f.Client2, "foreign")
	behindForeign := f.CI(t, f.OrgA, f.Client1, "behind-foreign")
	deleted := f.CI(t, f.OrgA, f.Client1, "deleted")
	behindDeleted := f.CI(t, f.OrgA, f.Client1, "behind-deleted")
	edge := func(src, dst string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
			f.OrgA, src, dst); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}
	for i := 0; i+1 < len(chain); i++ {
		edge(chain[i], chain[i+1])
	}
	root := chain[0]
	edge(root, foreign)
	edge(foreign, behindForeign)
	edge(root, deleted)
	edge(deleted, behindDeleted)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatalf("delete ci: %v", err)
	}

	engine := savedview.NewPGQueryEngine(f.App)
	ids := func(ctx context.Context, spec savedview.FilterSpec) map[string]bool {
		t.Helper()
		res, _, err := engine.Query(ctx, f.OrgA, spec, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out := map[string]bool{}
		for _, r := range res {
			out[r.ID] = true
		}
		return out
	}

	// Depth 5 (SRC-04): chain[1..5] are downstream, chain[6] is six hops away.
	down := ids(f.OrgCtx(f.OrgA), savedview.FilterSpec{DownstreamOf: root})
	for i := 1; i <= 5; i++ {
		if !down[chain[i]] {
			t.Errorf("downstream: chain[%d] missing", i)
		}
	}
	if down[chain[6]] {
		t.Error("downstream reached chain[6], six hops away")
	}
	up := ids(f.OrgCtx(f.OrgA), savedview.FilterSpec{UpstreamOf: chain[6]})
	if up[chain[0]] || !up[chain[1]] {
		t.Errorf("upstream of chain[6]: chain[0]=%v chain[1]=%v, want false/true", up[chain[0]], up[chain[1]])
	}

	// A deleted CI is neither returned nor walked through.
	if down[deleted] || down[behindDeleted] {
		t.Errorf("downstream through a deleted CI: deleted=%v behind=%v", down[deleted], down[behindDeleted])
	}
	if !down[foreign] || !down[behindForeign] {
		t.Error("org-wide principal misses the CIs behind the client-2 CI")
	}

	// Client 1: the client-2 CI is invisible and connects nothing.
	c1 := ids(f.ClientCtx(f.Client1), savedview.FilterSpec{DownstreamOf: root})
	if c1[foreign] || c1[behindForeign] {
		t.Errorf("client 1 downstream through a client-2 CI: foreign=%v behind=%v", c1[foreign], c1[behindForeign])
	}
	if all := ids(f.ClientCtx(f.Client1), savedview.FilterSpec{EntityKind: "ci"}); all[foreign] || all[deleted] {
		t.Errorf("client 1 lists foreign=%v deleted=%v", all[foreign], all[deleted])
	}

	// Site scope (WP-460, open proof of WP-034): a principal restricted to
	// site 1 sees only the site-1 CIs of the two sites, and a walk ends at
	// the site border: s1a -> s1c stays in site 1, s1a -> s2 -> s1b passes
	// through a site-2 CI.
	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "site "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	ciAt := func(name, site string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, id, site); err != nil {
			t.Fatalf("place ci %s: %v", name, err)
		}
		return id
	}
	s1a, s1b, s1c, s2 := ciAt("site1-a", site1), ciAt("site1-b", site1), ciAt("site1-c", site1), ciAt("site2", site2)
	edge(s1a, s1c)
	edge(s1a, s2)
	edge(s2, s1b)
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	siteCtx := database.ContextWithTenantScope(bg, &siteScope)
	if org := ids(f.OrgCtx(f.OrgA), savedview.FilterSpec{DownstreamOf: s1a}); !org[s1c] || !org[s2] || !org[s1b] {
		t.Errorf("org-wide downstream of s1a: s1c=%v s2=%v s1b=%v, want all", org[s1c], org[s2], org[s1b])
	}
	siteDown := ids(siteCtx, savedview.FilterSpec{DownstreamOf: s1a})
	if !siteDown[s1c] || siteDown[s2] || siteDown[s1b] {
		t.Errorf("site-1 downstream of s1a: s1c=%v s2=%v s1b=%v, want true/false/false", siteDown[s1c], siteDown[s2], siteDown[s1b])
	}
	if up := ids(siteCtx, savedview.FilterSpec{UpstreamOf: s1b}); up[s2] || up[s1a] {
		t.Errorf("site-1 upstream of s1b: s2=%v s1a=%v, want false/false", up[s2], up[s1a])
	}
	siteAll := ids(siteCtx, savedview.FilterSpec{EntityKind: "ci"})
	if !siteAll[s1a] || !siteAll[s1b] || !siteAll[s1c] || siteAll[s2] {
		t.Errorf("site-1 list: s1a=%v s1b=%v s1c=%v s2=%v, want site-1 CIs only", siteAll[s1a], siteAll[s1b], siteAll[s1c], siteAll[s2])
	}
	if got := ids(siteCtx, savedview.FilterSpec{LocationID: site2}); len(got) != 0 {
		t.Errorf("site-1 principal filtering for site 2 got %d CIs, want none", len(got))
	}

	// Assets (WP-460, open proof of WP-034): entity_kind asset runs under
	// the client scope like CIs.
	asset1 := f.Asset(t, f.OrgA, f.Client1, "4e-asset-c1")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "4e-asset-c2")
	if org := ids(f.OrgCtx(f.OrgA), savedview.FilterSpec{EntityKind: "asset"}); !org[asset1] || !org[asset2] {
		t.Errorf("org-wide assets: client1=%v client2=%v, want both", org[asset1], org[asset2])
	}
	if c1 := ids(f.ClientCtx(f.Client1), savedview.FilterSpec{EntityKind: "asset"}); !c1[asset1] || c1[asset2] {
		t.Errorf("client-1 assets: client1=%v client2=%v, want true/false", c1[asset1], c1[asset2])
	}
	if c1 := ids(f.ClientCtx(f.Client1), savedview.FilterSpec{EntityKind: "asset", ClientID: f.Client2}); len(c1) != 0 {
		t.Errorf("client-1 principal filtering for client 2 got %d assets, want none", len(c1))
	}

	// The handler requires the read permission of the entity kind.
	// held, when not nil, are the request's own permissions (a token
	// narrowed below the user's roles, WP-460): they must hold the key too.
	status := func(granted grants, held []identity.Permission, body string) int {
		t.Helper()
		mux := chi.NewRouter()
		savedview.NewHandler(savedview.NewMemoryRepository()).WithQueryEngine(engine).WithPermissions(granted).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search/query", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		scope := database.OrgWideScope(f.OrgA, f.User)
		ctx = database.ContextWithTenantScope(ctx, &scope)
		if held != nil {
			ctx = identity.WithPermissions(ctx, held)
		}
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	for _, c := range []struct {
		name    string
		granted grants
		held    []identity.Permission
		body    string
		want    int
	}{
		{"ci with ci:read", grants{"ci:read": true}, nil, `{"entity_kind":"ci"}`, http.StatusOK},
		{"default kind with ci:read", grants{"ci:read": true}, nil, `{}`, http.StatusOK},
		{"asset with ci:read only", grants{"ci:read": true}, nil, `{"entity_kind":"asset"}`, http.StatusForbidden},
		{"asset with asset:read", grants{"asset:read": true}, nil, `{"entity_kind":"asset"}`, http.StatusOK},
		{"ci without permission", grants{}, nil, `{"entity_kind":"ci"}`, http.StatusForbidden},
		{"unknown kind", grants{"ci:read": true}, nil, `{"entity_kind":"credential"}`, http.StatusBadRequest},
		{"asset, request holds asset:read", grants{"ci:read": true, "asset:read": true},
			[]identity.Permission{identity.PermSearchRead, identity.PermAssetRead}, `{"entity_kind":"asset"}`, http.StatusOK},
		{"asset, request narrowed to ci:read", grants{"ci:read": true, "asset:read": true},
			[]identity.Permission{identity.PermSearchRead, identity.PermCIRead}, `{"entity_kind":"asset"}`, http.StatusForbidden},
		{"ci, request without permissions", grants{"ci:read": true},
			[]identity.Permission{}, `{"entity_kind":"ci"}`, http.StatusForbidden},
	} {
		if got := status(c.granted, c.held, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
