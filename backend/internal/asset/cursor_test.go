package asset

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func seedAssets(t *testing.T, repo *MemoryRepository, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := repo.Create(context.Background(), &Asset{
			OrganizationID: "org-1",
			AssetTag:       fmt.Sprintf("tag-%02d", i),
			Name:           fmt.Sprintf("srv-%02d", i),
			Category:       "hardware",
			Status:         "in_stock",
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func listAssetPage(t *testing.T, mux chi.Router, url string) api.ListResponse[Asset] {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d: %s", url, w.Code, w.Body.String())
	}
	var resp api.ListResponse[Asset]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestListCursorPaginationWalksEveryRowExactlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	seedAssets(t, repo, 25)

	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	seen := map[string]int{}
	url := "/api/v1/assets?limit=10&sort_by=name&sort_dir=asc"
	pages := 0
	for {
		resp := listAssetPage(t, mux, url)
		pages++
		for _, item := range resp.Data {
			seen[item.ID]++
		}
		if resp.Total != 25 {
			t.Fatalf("total: got %d, want 25", resp.Total)
		}
		if resp.NextCursor == "" {
			break
		}
		if pages > 10 {
			t.Fatal("cursor pagination did not terminate")
		}
		url = "/api/v1/assets?limit=10&sort_by=name&sort_dir=asc&cursor=" + resp.NextCursor
	}

	if pages != 3 {
		t.Errorf("pages: got %d, want 3", pages)
	}
	if len(seen) != 25 {
		t.Errorf("distinct items: got %d, want 25", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("item %s returned %d times", id, count)
		}
	}
}

func TestListRejectsMalformedCursor(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/assets?cursor=%21%21%21", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListRejectsCursorFromDifferentSortOrder(t *testing.T) {
	repo := NewMemoryRepository()
	seedAssets(t, repo, 4)

	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	page := listAssetPage(t, mux, "/api/v1/assets?limit=2&sort_by=name&sort_dir=asc")
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	req := httptest.NewRequest("GET", "/api/v1/assets?limit=2&sort_by=status&sort_dir=asc&cursor="+page.NextCursor, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mismatched cursor, got %d", w.Code)
	}
}

func TestNextCursorEmptyOnPartialPage(t *testing.T) {
	items := []Asset{{ID: "a", Name: "a", CreatedAt: time.Now().UTC()}}
	if got := NextCursor(items, &FilterParams{}, 10); got != "" {
		t.Errorf("expected no cursor for a partial page, got %q", got)
	}
	if got := NextCursor(nil, &FilterParams{}, 10); got != "" {
		t.Errorf("expected no cursor for an empty page, got %q", got)
	}
	if got := NextCursor(items, &FilterParams{}, 1); got == "" {
		t.Error("expected a cursor for a full page")
	}
}

// listStatus requests url as organization org-1 and returns the recorder.
func listStatus(t *testing.T, mux chi.Router, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestListQueryAPI02 covers the list of WP-740 (API-02): filter[field][op]
// on the whitelist, the sort whitelist (sort and the alias sort_by), fields
// with id always kept, 422 for an unknown filter, sort or response field
// instead of the silent fallback, the aliases status, category, search and
// q, and a stable cursor under a filter and a sort on a nullable field.
func TestListQueryAPI02(t *testing.T) {
	repo := NewMemoryRepository()
	seedAssets(t, repo, 6)
	ctx := context.Background()
	for i, id := range []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003"} {
		maker, warranty := "Dell", fmt.Sprintf("2027-0%d-01", i+1)
		if i == 2 {
			maker = "Lenovo"
		}
		if _, err := repo.Update(ctx, "org-1", id, &UpdateRequest{Manufacturer: &maker, WarrantyEnd: &warranty}); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	tags := func(resp api.ListResponse[Asset]) []string {
		out := make([]string, len(resp.Data))
		for i := range resp.Data {
			out[i] = resp.Data[i].AssetTag
		}
		return out
	}
	for _, c := range []struct {
		url  string
		want []string
	}{
		{"/api/v1/assets?filter[manufacturer]=Dell&sort=inventory_no", []string{"tag-00", "tag-01"}},
		{"/api/v1/assets?filter[manufacturer][in]=Dell,Lenovo&sort=-inventory_no", []string{"tag-02", "tag-01", "tag-00"}},
		{"/api/v1/assets?filter[warranty_until][gte]=2027-02-01&sort=warranty_until", []string{"tag-01", "tag-02"}},
		{"/api/v1/assets?filter[inventory_no][like]=tag-0*&filter[lifecycle_state]=ordered&sort_by=name&sort_dir=asc&limit=2",
			[]string{"tag-00", "tag-01"}},
		{"/api/v1/assets?search=srv-05", []string{"tag-05"}},
		{"/api/v1/assets?q=srv-04", []string{"tag-04"}},
		{"/api/v1/assets?status=retired", []string{}},
		{"/api/v1/assets?category=hardware&filter[manufacturer]=Lenovo", []string{"tag-02"}},
	} {
		if got := tags(listAssetPage(t, mux, c.url)); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.url, got, c.want)
		}
	}

	for _, url := range []string{
		"/api/v1/assets?filter[location]=x",
		"/api/v1/assets?filter[category]=x",
		"/api/v1/assets?sort=notes",
		"/api/v1/assets?sort_by=bogus",
		"/api/v1/assets?fields=name,secret",
	} {
		if w := listStatus(t, mux, url); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d %s, want 422", url, w.Code, w.Body.String())
		}
	}
	for _, url := range []string{"/api/v1/assets?filter[supplier_id]=no-uuid", "/api/v1/assets?filter[model][near]=x",
		"/api/v1/assets?sort_dir=up"} {
		if w := listStatus(t, mux, url); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", url, w.Code, w.Body.String())
		}
	}

	// fields selects members; id stays.
	w := listStatus(t, mux, "/api/v1/assets?fields=inventory_no,version&sort=inventory_no&limit=1")
	var sparse api.ListResponse[map[string]any]
	if err := json.Unmarshal(w.Body.Bytes(), &sparse); err != nil || len(sparse.Data) != 1 {
		t.Fatalf("fields: %d %s", w.Code, w.Body.String())
	}
	if row := sparse.Data[0]; len(row) != 3 || row["inventory_no"] != "tag-00" || row["version"] != float64(2) || row["id"] == nil {
		t.Errorf("fields row %v", row)
	}

	// A stable cursor under a filter and a sort on a nullable field.
	var seen []string
	url := "/api/v1/assets?filter[lifecycle_state]=ordered&sort=manufacturer&limit=2"
	for pages := 0; ; pages++ {
		resp := listAssetPage(t, mux, url)
		seen = append(seen, tags(resp)...)
		if resp.NextCursor == "" || pages > 5 {
			break
		}
		url = "/api/v1/assets?filter[lifecycle_state]=ordered&sort=manufacturer&limit=2&cursor=" + resp.NextCursor
	}
	if want := []string{"tag-03", "tag-04", "tag-05", "tag-00", "tag-01", "tag-02"}; !slices.Equal(seen, want) {
		t.Errorf("cursor walk %v, want %v", seen, want)
	}
}
