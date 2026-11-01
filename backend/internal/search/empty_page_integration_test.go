package search_test

// WP-672 (API-02, SRC-01), finding F-401: a search without a visible hit
// answers "data": [] as SearchResponse requires, not null: no matching
// text, a type without a hit, and a client-2 principal whose hits all lie
// in client 1.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestSearchWithoutHitsAnswersAnEmptyArray$' ./internal/search/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestSearchWithoutHitsAnswersAnEmptyArray runs GET /api/v1/search over
// the PostgreSQL index under the caller's tenant scope.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchWithoutHitsAnswersAnEmptyArray(t *testing.T) {
	f := scopetest.Seed(t, "25")
	own := f.CI(t, f.OrgA, f.Client1, "emptypage-client1")
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	mux := chi.NewRouter()
	search.NewHandler(repo, permissionStub{granted: map[string]bool{"ci:read": true, "asset:read": true}}).RegisterRoutes(mux)
	searchAs := func(scope database.TenantScope, path string) (data json.RawMessage, hits []search.Hit) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		req = req.WithContext(database.ContextWithTenantScope(ctx, &scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(body.Data, &hits); err != nil {
			t.Fatalf("GET %s: data %s: %v", path, body.Data, err)
		}
		return body.Data, hits
	}

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client2 := database.OrgWideScope(f.OrgA, f.User)
	client2.Clients = database.ScopeIDs(f.Client2)
	// Control: the org-wide caller finds the client-1 CI.
	if _, hits := searchAs(orgWide, "/api/v1/search?q=emptypage"); len(hits) != 1 || hits[0].EntityID != own {
		t.Fatalf("org-wide search: %d hits %v, want the client-1 CI", len(hits), hits)
	}
	for _, c := range []struct {
		name  string
		scope database.TenantScope
		path  string
	}{
		{"no matching text", orgWide, "/api/v1/search?q=no-such-text"},
		{"no matching text of a type", orgWide, "/api/v1/search?q=no-such-text&type=ci"},
		{"a type without a hit", orgWide, "/api/v1/search?q=emptypage&type=asset"},
		{"client 2, every hit in client 1", client2, "/api/v1/search?q=emptypage"},
		{"client 2, every hit of the type in client 1", client2, "/api/v1/search?q=emptypage&type=ci"},
	} {
		if data, _ := searchAs(c.scope, c.path); string(data) != "[]" {
			t.Errorf("%s: GET %s answers \"data\": %s, want []", c.name, c.path, data)
		}
	}
}
