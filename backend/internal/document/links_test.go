package document_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestEntityTypesRegistry: the registry holds exactly the link targets of
// WP-267 and contract (WP-737); contact (a dead branch of the former
// linkVisible) is not one.
func TestEntityTypesRegistry(t *testing.T) {
	want := []string{"asset", "assignment", "ci", "contract", "disposal_record", "location", "stocktake", "ticket"}
	if got := document.EntityTypes(); !slices.Equal(got, want) {
		t.Errorf("EntityTypes() = %v, want %v", got, want)
	}
}

// TestLinkUnknownEntityType: POST /api/v1/documents/{id}/links with an
// entity type outside the registry answers 422 on entity_type and creates
// no link; a registered type answers 201.
func TestLinkUnknownEntityType(t *testing.T) {
	repo := document.NewMemoryRepository()
	ctx := context.Background()
	doc := &document.Document{OrganizationID: "org-1", Title: "t", FileName: "t.pdf"}
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	document.NewHandler(repo, nil).RegisterRoutes(mux)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+doc.ID+"/links", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	for _, entityType := range []string{"contact", "bogus", "CI"} {
		w := post(`{"entity_type":"` + entityType + `","entity_id":"x-1"}`)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "entity_type") {
			t.Errorf("entity_type %q: %d %s, want 422 on entity_type", entityType, w.Code, w.Body.String())
		}
	}
	if links, _ := repo.GetLinks(ctx, "org-1", doc.ID); len(links) != 0 {
		t.Fatalf("refused links were stored: %+v", links)
	}
	if w := post(`{"entity_type":"location","entity_id":"loc-1"}`); w.Code != http.StatusCreated {
		t.Errorf("link to a location: %d %s, want 201", w.Code, w.Body.String())
	}
}

// TestMemoryRepositoryUnlink: the memory repository (profile without a
// database) removes a link of the document and organization only, checks
// the entity type against the registry and answers api.ErrNotFound for
// anything else.
func TestMemoryRepositoryUnlink(t *testing.T) {
	repo := document.NewMemoryRepository()
	ctx := context.Background()
	newDoc := func(org string) string {
		d := &document.Document{OrganizationID: org, Title: "t", FileName: "t.pdf"}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	doc, other := newDoc("org-1"), newDoc("org-1")
	link := &document.DocumentLink{OrganizationID: "org-1", DocumentID: doc, EntityType: "ticket", EntityID: "t-1"}
	if err := repo.LinkDocument(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkDocument(ctx, &document.DocumentLink{OrganizationID: "org-1", DocumentID: doc, EntityType: "contact", EntityID: "c-1"}); err == nil {
		t.Error("memory repository linked an entity type outside the registry")
	}

	for name, call := range map[string]func() error{
		"other organization": func() error { return repo.UnlinkDocument(ctx, "org-2", doc, link.ID) },
		"other document":     func() error { return repo.UnlinkDocument(ctx, "org-1", other, link.ID) },
		"unknown link":       func() error { return repo.UnlinkDocument(ctx, "org-1", doc, "link-404") },
	} {
		if err := call(); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("unlink with %s: %v, want api.ErrNotFound", name, err)
		}
	}
	if err := repo.UnlinkDocument(ctx, "org-1", doc, link.ID); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if links, _ := repo.GetLinks(ctx, "org-1", doc); len(links) != 0 {
		t.Errorf("links after the unlink: %+v", links)
	}
	if err := repo.UnlinkDocument(ctx, "org-1", doc, link.ID); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("second unlink: %v, want api.ErrNotFound", err)
	}

	// Through the route: 204, then 404.
	again := &document.DocumentLink{OrganizationID: "org-1", DocumentID: doc, EntityType: "location", EntityID: "l-1"}
	if err := repo.LinkDocument(ctx, again); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	document.NewHandler(repo, nil).RegisterRoutes(mux)
	del := func() int {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/documents/"+doc+"/links/"+again.ID, nil)
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if code := del(); code != http.StatusNoContent {
		t.Errorf("DELETE the link: %d, want 204", code)
	}
	if code := del(); code != http.StatusNotFound {
		t.Errorf("DELETE the removed link: %d, want 404", code)
	}
}
