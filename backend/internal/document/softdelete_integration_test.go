package document_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// TestDocumentSoftDelete covers WP-732 (MGT-01): DELETE sets deleted_at
// (204, a second call 404) and writes document.deleted. The deleted
// document is missing from list, detail, PATCH, content (current and
// earlier version), versions (list and upload, no blob left), links (read,
// create, unlink) and GetLinksForEntity; versions, links and the usage stay
// until the purge. The search drops an index entry that is out of date
// (hitVisible), and ReindexTenant indexes the document no more.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentSoftDelete(t *testing.T) {
	w := newVersionWorld(t, false)
	f := w.f
	d := w.doc(t, "softdeletewp", "asset", w.asset1)
	kept := w.doc(t, "softdeletekept", "asset", w.asset1)
	base := "/api/v1/documents/" + d
	for _, body := range []string{"%PDF-one", "%PDF-two"} {
		if rec := w.call(w.orgScope(), http.MethodPost, base+"/versions", "application/pdf", body); rec.Code != http.StatusCreated {
			t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
		}
	}
	linkID := w.scalar(t, `SELECT id::text FROM document_link WHERE document_id = $1`, d)
	repoSearch := search.NewPGRepository(f.App)
	hits := func() map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, text := range []string{"softdeletewp", "softdeletekept"} {
			res, err := repoSearch.Query(w.orgCtx, search.Query{OrganizationID: f.OrgA, Text: text, Limit: 50})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			for _, h := range res.Data {
				out[h.EntityID] = true
			}
		}
		return out
	}
	if _, err := repoSearch.ReindexTenant(w.orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if !hits()[d] {
		t.Fatal("the document is no search hit before the delete")
	}
	usageBefore := w.usage(t).StorageBytes
	blobsBefore := w.blobs(t)

	if rec := w.call(w.orgScope(), http.MethodDelete, base, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(w.orgScope(), http.MethodDelete, base, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: %d, want 404", rec.Code)
	}
	if n := w.scalar(t, `SELECT count(*)::text FROM document WHERE id = $1 AND deleted_at IS NOT NULL`, d); n != "1" {
		t.Fatalf("soft-deleted rows %s, want the document with deleted_at", n)
	}
	if n := w.scalar(t, `SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		AND action = 'document.deleted'`, f.OrgA, d); n != "1" {
		t.Fatalf("document.deleted entries %s, want 1", n)
	}

	// Every route treats the document as missing.
	for _, c := range []struct{ method, path, contentType, body string }{
		{http.MethodGet, base, "", ""},
		{http.MethodPatch, base, "application/json", `{"title":"renamed"}`},
		{http.MethodGet, base + "/content", "", ""},
		{http.MethodGet, base + "/content?version=1", "", ""},
		{http.MethodGet, base + "/versions", "", ""},
		{http.MethodPost, base + "/versions", "application/pdf", "%PDF-three"},
		{http.MethodPut, base + "/content", "application/pdf", "%PDF-three"},
		{http.MethodGet, base + "/links", "", ""},
		{http.MethodPost, base + "/links", "application/json", `{"entity_type":"asset","entity_id":"` + w.asset2 + `"}`},
		{http.MethodDelete, base + "/links/" + linkID, "", ""},
	} {
		if rec := w.call(w.orgScope(), c.method, c.path, c.contentType, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s, want 404", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents?limit=100", "", "")
	var list api.ListResponse[document.Document]
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	listed := map[string]bool{}
	for _, doc := range list.Data {
		listed[doc.ID] = true
	}
	if listed[d] || !listed[kept] {
		t.Errorf("list: deleted %v, kept %v", listed[d], listed[kept])
	}
	docs, err := w.repo.GetLinksForEntity(w.orgCtx, f.OrgA, "asset", w.asset1)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if doc.ID == d {
			t.Error("GetLinksForEntity returns the deleted document")
		}
	}
	if _, err = w.repo.GetLinks(w.orgCtx, f.OrgA, d); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("GetLinks of the deleted document: %v, want api.ErrNotFound", err)
	}
	if err = w.repo.UnlinkDocument(w.orgCtx, f.OrgA, d, linkID); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unlink of the deleted document: %v, want api.ErrNotFound", err)
	}

	// Versions, link, content and usage stay until the purge; a refused
	// upload leaves no blob.
	if n := w.scalar(t, `SELECT ((SELECT count(*) FROM document_version WHERE document_id = $1) * 10 +
		(SELECT count(*) FROM document_link WHERE document_id = $1))::text`, d); n != "21" {
		t.Errorf("versions*10 + links of the deleted document: %s, want 21", n)
	}
	if got := w.usage(t).StorageBytes; got != usageBefore {
		t.Errorf("usage after the soft delete: %d, want %d", got, usageBefore)
	}
	if n := w.blobs(t); n != blobsBefore {
		t.Errorf("blobs after the soft delete: %d, want %d", n, blobsBefore)
	}

	// Search: the entry out of date is filtered, the reindex leaves it out.
	if got := hits(); got[d] || !got[kept] {
		t.Errorf("search hits after the delete: %v, want the kept document only", got)
	}
	if _, err = repoSearch.ReindexTenant(w.orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if n := w.scalar(t, `SELECT count(*)::text FROM search_document WHERE entity_type = 'document' AND entity_id = $1`, d); n != "0" {
		t.Errorf("index entries of the deleted document after the reindex: %s", n)
	}
	if got := hits(); got[d] || !got[kept] {
		t.Errorf("search hits after the reindex: %v, want the kept document only", got)
	}

}

// TestMemoryRepositorySoftDelete: the memory repository (profile without a
// database) soft-deletes like PGRepository (WP-732).
func TestMemoryRepositorySoftDelete(t *testing.T) {
	ctx := context.Background()
	mem := document.NewMemoryRepository()
	var err error
	md := &document.Document{OrganizationID: "org-1", Title: "m", FileName: "m.pdf"}
	if err = mem.Create(ctx, md); err != nil {
		t.Fatal(err)
	}
	if err = mem.Delete(ctx, "org-1", md.ID); err != nil {
		t.Fatal(err)
	}
	if err = mem.Delete(ctx, "org-1", md.ID); err == nil {
		t.Error("memory: second delete succeeded")
	}
	if _, err = mem.GetByID(ctx, "org-1", md.ID); err == nil {
		t.Error("memory: the deleted document is found")
	}
	if items, _, _ := mem.List(ctx, "org-1", document.FilterParams{}, api.PaginationParams{Limit: 10}); len(items) != 0 {
		t.Errorf("memory: list %v", items)
	}
	if err = mem.LinkDocument(ctx, &document.DocumentLink{OrganizationID: "org-1", DocumentID: md.ID, EntityType: "asset", EntityID: "a"}); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("memory: link to the deleted document: %v", err)
	}
	if _, _, err = mem.ListVersions(ctx, "org-1", md.ID, api.PaginationParams{Limit: 10}); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("memory: versions of the deleted document: %v", err)
	}
}
