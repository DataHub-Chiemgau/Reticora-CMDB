package document

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func seedDocuments(t *testing.T, repo *MemoryRepository, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := repo.Create(context.Background(), &Document{
			OrganizationID: "org-1",
			Title:          fmt.Sprintf("doc-%02d", i),
			FileName:       fmt.Sprintf("doc-%02d.pdf", i),
			Category:       "general",
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func listDocumentPage(t *testing.T, mux chi.Router, url string) api.ListResponse[Document] {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d: %s", url, w.Code, w.Body.String())
	}
	var resp api.ListResponse[Document]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestListCursorPaginationWalksEveryRowExactlyOnce(t *testing.T) {
	repo := NewMemoryRepository()
	seedDocuments(t, repo, 25)

	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)

	seen := map[string]int{}
	url := "/api/v1/documents?limit=10&sort_by=title&sort_dir=asc"
	pages := 0
	for {
		resp := listDocumentPage(t, mux, url)
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
		url = "/api/v1/documents?limit=10&sort_by=title&sort_dir=asc&cursor=" + resp.NextCursor
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
	NewHandler(repo, nil).RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/documents?cursor=%21%21%21", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListRejectsCursorFromDifferentSortOrder(t *testing.T) {
	repo := NewMemoryRepository()
	seedDocuments(t, repo, 4)

	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)

	page := listDocumentPage(t, mux, "/api/v1/documents?limit=2&sort_by=title&sort_dir=asc")
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	req := httptest.NewRequest("GET", "/api/v1/documents?limit=2&sort_by=category&sort_dir=asc&cursor="+page.NextCursor, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mismatched cursor, got %d", w.Code)
	}
}

func TestNextCursorEmptyOnPartialPage(t *testing.T) {
	items := []Document{{ID: "a", Title: "a", CreatedAt: time.Now().UTC()}}
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

// TestGetLinksIsCursorPaged: with three links of a document,
// GET /documents/{id}/links?limit=1 answers one entry and a next_cursor;
// following it returns every link once, newest first; a forged cursor is
// 400 (WP-467, API-02, F-075).
func TestGetLinksIsCursorPaged(t *testing.T) {
	repo := NewMemoryRepository()
	seedDocuments(t, repo, 2)
	docs, _, err := repo.List(context.Background(), "org-1", FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil || len(docs) != 2 {
		t.Fatalf("seeded documents: %d, %v", len(docs), err)
	}
	doc, other := docs[0], docs[1]
	created := time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC)
	for i, entityID := range []string{"ci-1", "ci-2", "ci-3"} {
		if err := repo.LinkDocument(context.Background(), &DocumentLink{OrganizationID: "org-1", DocumentID: doc.ID, EntityType: "ci", EntityID: entityID}); err != nil {
			t.Fatal(err)
		}
		// Two links share an instant: id breaks the tie.
		repo.links[fmt.Sprintf("link-%d", i+1)].CreatedAt = created.Add(time.Duration(min(i, 1)) * time.Second)
	}
	if err := repo.LinkDocument(context.Background(), &DocumentLink{OrganizationID: "org-1", DocumentID: other.ID, EntityType: "ci", EntityID: "ci-9"}); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)
	get := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", url, nil)
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	path := "/api/v1/documents/" + doc.ID + "/links"
	var entities []string
	for next, pages := path+"?limit=1", 0; next != ""; pages++ {
		if pages > 5 {
			t.Fatal("cursor pagination did not terminate")
		}
		w := get(next)
		var page api.ListResponse[DocumentLink]
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Data) != 1 || page.Total != 3 {
			t.Fatalf("GET %s: %d %s; want {data: [one link], total: 3, next_cursor}", next, w.Code, w.Body.String())
		}
		entities = append(entities, page.Data[0].EntityID)
		next = ""
		if page.NextCursor != "" {
			next = path + "?limit=1&cursor=" + page.NextCursor
		}
	}
	if fmt.Sprint(entities) != "[ci-3 ci-2 ci-1]" {
		t.Errorf("links over the pages: %v; want [ci-3 ci-2 ci-1] (newest first, then id)", entities)
	}

	if w := get(path + "?cursor=eyJvIjoxfQ.AAAAAAAAAAAAAAAAAAAAAA"); w.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d %s, want 400", w.Code, w.Body.String())
	}
	if w := get("/api/v1/documents/" + other.ID + "/links"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ci-9"`) || strings.Contains(w.Body.String(), `"ci-1"`) {
		t.Errorf("links of the other document: %d %s", w.Code, w.Body.String())
	}
}
