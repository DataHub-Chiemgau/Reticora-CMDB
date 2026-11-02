package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSearchTotalForm covers WP-839 (API-01, NFR-02; E-104, E-17): the
// PostgreSQL index counts the hits of a search capped. With 10,000 hits
// Total is 10,000 and not capped; with 10,001 Total is 10,000 and
// TotalCapped is set. has_more stays right next to the cap (a full page
// before it) and on the last page, whose count reaches past the cap. The
// single scan of search_document (E-133) stays the check of
// TestSearchPlanUnderRLS. GET /api/v1/search answers
// total as the hits of the page the caller may read (handler, unchanged):
// it never reaches the cap and carries no total_capped.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestSearchTotalForm$' ./internal/search/
func TestSearchTotalForm(t *testing.T) {
	f := scopetest.Seed(t, "39")
	bg := context.Background()
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, CASE WHEN i % 2 = 0 THEN $2::uuid END, `+server+`, 'wp839 host ' || i, 'active'
		FROM generate_series(1, $3::int) AS i`, f.OrgA, f.Client1, api.TotalCap); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}
	repo := search.NewPGRepository(f.App)
	reindex := func() {
		t.Helper()
		if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		if _, err := f.Admin.Exec(bg, `ANALYZE search_document`); err != nil {
			t.Fatal(err)
		}
	}
	reindex()
	query := func(offset int) search.Result {
		t.Helper()
		res, err := repo.Query(f.OrgCtx(f.OrgA), search.Query{OrganizationID: f.OrgA, UserID: f.User, Text: "wp839", Limit: 50, Offset: offset})
		if err != nil {
			t.Fatalf("search at offset %d: %v", offset, err)
		}
		return res
	}

	// At the cap: exact.
	if res := query(0); res.Total != api.TotalCap || res.TotalCapped || !res.HasMore || len(res.Data) != 50 {
		t.Errorf("%d hits: %d on the page, total %d, capped %v, has_more %v; want 50, %d, false, true",
			api.TotalCap, len(res.Data), res.Total, res.TotalCapped, res.HasMore, api.TotalCap)
	}
	if res := query(api.TotalCap - 10); res.Total != api.TotalCap || res.TotalCapped || res.HasMore || len(res.Data) != 10 {
		t.Errorf("%d hits, last page: %d on the page, total %d, capped %v, has_more %v; want 10, %d, false, false",
			api.TotalCap, len(res.Data), res.Total, res.TotalCapped, res.HasMore, api.TotalCap)
	}

	// Beyond the cap.
	f.CI(t, f.OrgA, f.Client1, "wp839 host beyond")
	reindex()
	for _, c := range []struct {
		offset, rows int
		hasMore      bool
	}{{0, 50, true}, {api.TotalCap - 50, 50, true}, {api.TotalCap - 9, 10, false}} {
		res := query(c.offset)
		if res.Total != api.TotalCap || !res.TotalCapped || res.HasMore != c.hasMore || len(res.Data) != c.rows {
			t.Errorf("%d hits, offset %d: %d on the page, total %d, capped %v, has_more %v; want %d, %d, true, %v",
				api.TotalCountLimit, c.offset, len(res.Data), res.Total, res.TotalCapped, res.HasMore, c.rows, api.TotalCap, c.hasMore)
		}
	}

	// The response: total is the page, without total_capped.
	mux := chi.NewRouter()
	search.NewHandler(repo, permissionStub{granted: map[string]bool{"ci:read": true}}).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=wp839&limit=50", nil)
	req = req.WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var body map[string]json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("GET /api/v1/search: %d %.300s", w.Code, w.Body.String())
	}
	if _, ok := body["total_capped"]; ok || string(body["total"]) != "50" || string(body["has_more"]) != "true" {
		t.Errorf("GET /api/v1/search: total %s, has_more %s, total_capped present %v; want 50, true, absent", body["total"], body["has_more"], ok)
	}
}
