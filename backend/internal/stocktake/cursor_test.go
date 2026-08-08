package stocktake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func seedStocktakes(t *testing.T, repo *MemoryRepository, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := repo.Create(context.Background(), &Stocktake{
			OrganizationID: "org-1",
			Title:          fmt.Sprintf("count-%02d", i),
			Status:         "planned",
			Scope:          "full",
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func listStocktakePage(t *testing.T, mux chi.Router, url string) api.ListResponse[Stocktake] {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d: %s", url, w.Code, w.Body.String())
	}
	var resp api.ListResponse[Stocktake]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestListCursorPaginationWalksEveryRowExactlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	seedStocktakes(t, repo, 25)

	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	seen := map[string]int{}
	url := "/api/v1/stocktakes?limit=10&sort_by=title&sort_dir=asc"
	pages := 0
	for {
		resp := listStocktakePage(t, mux, url)
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
		url = "/api/v1/stocktakes?limit=10&sort_by=title&sort_dir=asc&cursor=" + resp.NextCursor
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

	req := httptest.NewRequest("GET", "/api/v1/stocktakes?cursor=%21%21%21", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListRejectsCursorFromDifferentSortOrder(t *testing.T) {
	repo := NewMemoryRepository()
	seedStocktakes(t, repo, 4)

	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	page := listStocktakePage(t, mux, "/api/v1/stocktakes?limit=2&sort_by=title&sort_dir=asc")
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	req := httptest.NewRequest("GET", "/api/v1/stocktakes?limit=2&sort_by=status&sort_dir=asc&cursor="+page.NextCursor, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mismatched cursor, got %d", w.Code)
	}
}

func TestNextCursorEmptyOnPartialPage(t *testing.T) {
	items := []Stocktake{{ID: "a", Title: "a", CreatedAt: time.Now().UTC()}}
	if got := NextCursor(items, FilterParams{}, 10); got != "" {
		t.Errorf("expected no cursor for a partial page, got %q", got)
	}
	if got := NextCursor(nil, FilterParams{}, 10); got != "" {
		t.Errorf("expected no cursor for an empty page, got %q", got)
	}
	if got := NextCursor(items, FilterParams{}, 1); got == "" {
		t.Error("expected a cursor for a full page")
	}
}
