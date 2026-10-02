package document_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
)

// TestDocumentRepositoryClientScope runs the document repository with the
// principal's tenant scope (TEN-06, WP-019): a document linked only to a
// client-2 CI is invisible to a principal restricted to client 1, who also
// cannot link documents to client-2 objects.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "34")
	ownCI := f.CI(t, f.OrgA, f.Client1, "doc-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "doc-c2")
	user := f.AppUser(t, f.OrgA, "author")

	repo := document.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	doc := func(title string) *document.Document {
		return &document.Document{OrganizationID: f.OrgA, Title: title, FileName: title + ".pdf", StorageKey: "k/" + title, UploadedBy: user, Category: "general"}
	}
	ownDoc, foreignDoc := doc("own"), doc("foreign")
	for _, d := range []*document.Document{ownDoc, foreignDoc} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide document %s: %v", d.Title, err)
		}
	}
	if err := repo.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: ownDoc.ID, EntityType: "ci", EntityID: ownCI}); err != nil {
		t.Fatalf("link own document: %v", err)
	}
	if err := repo.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: foreignDoc.ID, EntityType: "ci", EntityID: foreignCI}); err != nil {
		t.Fatalf("link foreign document: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, total, err := repo.List(ctx, f.OrgA, document.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != ownDoc.ID {
		t.Fatalf("client-1 documents: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignDoc.ID); err == nil {
		t.Fatal("client-1 principal read a document of a client-2 CI")
	}
	if docs, linkErr := repo.GetLinksForEntity(ctx, f.OrgA, "ci", foreignCI); linkErr != nil || len(docs) != 0 {
		t.Fatalf("client-1 documents of client-2 CI: %+v err=%v, want none", docs, linkErr)
	}
	if err = repo.LinkDocument(ctx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: ownDoc.ID, EntityType: "ci", EntityID: foreignCI}); err == nil {
		t.Fatal("client-1 principal linked a document to a client-2 CI")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignDoc.ID); err == nil {
		t.Fatal("client-1 principal deleted a document of a client-2 CI")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM document WHERE id = $1`, foreignDoc.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 document: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, document.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
