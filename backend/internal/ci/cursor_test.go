package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func seedItems(t *testing.T, repo *MemoryRepository, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := repo.Create(context.Background(), &Item{
			OrganizationID: "org-1",
			CITypeID:       "type-server",
			Name:           fmt.Sprintf("srv-%02d", i),
			Status:         "active",
			Attributes:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func listPage(t *testing.T, mux chi.Router, url string) api.ListResponse[Item] {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d: %s", url, w.Code, w.Body.String())
	}
	var resp api.ListResponse[Item]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestListCursorPaginationWalksEveryRowExactlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	seedItems(t, repo, 25)

	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)

	seen := map[string]int{}
	url := "/api/v1/cis?limit=10&sort_by=name&sort_dir=asc"
	pages := 0
	for {
		resp := listPage(t, mux, url)
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
		url = "/api/v1/cis?limit=10&sort_by=name&sort_dir=asc&cursor=" + resp.NextCursor
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

func TestListCursorIsStableAcrossInserts(t *testing.T) {
	repo := NewMemoryRepository()
	seedItems(t, repo, 6)

	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)

	first := listPage(t, mux, "/api/v1/cis?limit=3&sort_by=name&sort_dir=asc")
	if first.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	// Inserting a row that sorts before the cursor must not shift the window.
	seedItems(t, repo, 1)

	second := listPage(t, mux, "/api/v1/cis?limit=3&sort_by=name&sort_dir=asc&cursor="+first.NextCursor)
	for _, item := range second.Data {
		for _, prev := range first.Data {
			if item.ID == prev.ID {
				t.Fatalf("item %s (%s) returned on both pages", item.ID, item.Name)
			}
		}
	}
}

func TestListRejectsMalformedCursor(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/cis?cursor=%21%21%21", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListRejectsCursorFromDifferentSortOrder(t *testing.T) {
	repo := NewMemoryRepository()
	seedItems(t, repo, 4)

	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)

	page := listPage(t, mux, "/api/v1/cis?limit=2&sort_by=name&sort_dir=asc")
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	req := httptest.NewRequest("GET", "/api/v1/cis?limit=2&sort_by=status&sort_dir=asc&cursor="+page.NextCursor, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mismatched cursor, got %d", w.Code)
	}
}

func TestNextCursorEmptyOnPartialPage(t *testing.T) {
	items := []Item{{ID: "a", Name: "a", CreatedAt: "2026-01-01T00:00:00Z"}}
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
