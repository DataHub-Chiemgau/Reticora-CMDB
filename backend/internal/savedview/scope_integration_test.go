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
// deleted CI.
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

	// The handler requires the read permission of the entity kind.
	status := func(granted grants, body string) int {
		t.Helper()
		mux := chi.NewRouter()
		savedview.NewHandler(savedview.NewMemoryRepository()).WithQueryEngine(engine).WithPermissions(granted).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search/query", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		scope := database.OrgWideScope(f.OrgA, f.User)
		req = req.WithContext(database.ContextWithTenantScope(ctx, &scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	for _, c := range []struct {
		name    string
		granted grants
		body    string
		want    int
	}{
		{"ci with ci:read", grants{"ci:read": true}, `{"entity_kind":"ci"}`, http.StatusOK},
		{"default kind with ci:read", grants{"ci:read": true}, `{}`, http.StatusOK},
		{"asset with ci:read only", grants{"ci:read": true}, `{"entity_kind":"asset"}`, http.StatusForbidden},
		{"asset with asset:read", grants{"asset:read": true}, `{"entity_kind":"asset"}`, http.StatusOK},
		{"ci without permission", grants{}, `{"entity_kind":"ci"}`, http.StatusForbidden},
		{"unknown kind", grants{"ci:read": true}, `{"entity_kind":"credential"}`, http.StatusBadRequest},
	} {
		if got := status(c.granted, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
