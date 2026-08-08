package ci

import (
	"context"
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type recordingIndexer struct {
	indexed []Document
	deleted []string
	err     error
}

func (r *recordingIndexer) IndexDocument(_ context.Context, doc Document) error {
	if r.err != nil {
		return r.err
	}
	r.indexed = append(r.indexed, doc)
	return nil
}

func (r *recordingIndexer) DeleteDocument(_ context.Context, orgID, entityType, entityID string) error {
	if r.err != nil {
		return r.err
	}
	r.deleted = append(r.deleted, orgID+":"+entityType+":"+entityID)
	return nil
}

func TestIndexingRepository_CreateIndexesDocument(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       "type-switch",
		Name:           "sw-core-01",
		Manufacturer:   "Cisco",
		Model:          "C9300",
		SerialNumber:   "SN-123",
		Hostname:       "sw-core-01.example.net",
	}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create: %v", err)
	}

	if len(indexer.indexed) != 1 {
		t.Fatalf("expected 1 indexed document, got %d", len(indexer.indexed))
	}
	doc := indexer.indexed[0]
	if doc.EntityType != EntityTypeCI || doc.EntityID != item.ID || doc.OrganizationID != "org-1" {
		t.Errorf("unexpected document identity: %+v", doc)
	}
	if doc.Title != "sw-core-01" {
		t.Errorf("expected title sw-core-01, got %q", doc.Title)
	}
	if doc.URL != "/cmdb/"+item.ID {
		t.Errorf("unexpected url %q", doc.URL)
	}
	wantSummary := "sw-core-01.example.net Cisco C9300 SN-123"
	if doc.Summary != wantSummary {
		t.Errorf("expected summary %q, got %q", wantSummary, doc.Summary)
	}
}

func TestIndexingRepository_UpdateAndDelete(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-01"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create: %v", err)
	}

	name := "srv-01-renamed"
	if _, err := svc.Update(context.Background(), "org-1", item.ID, UpdateRequest{Name: &name}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(indexer.indexed) != 2 {
		t.Fatalf("expected 2 indexed documents (create+update), got %d", len(indexer.indexed))
	}
	if indexer.indexed[1].Title != "srv-01-renamed" {
		t.Errorf("expected reindexed title srv-01-renamed, got %q", indexer.indexed[1].Title)
	}

	if _, err := svc.Delete(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(indexer.deleted) != 1 || indexer.deleted[0] != "org-1:ci:"+item.ID {
		t.Errorf("unexpected deletes: %v", indexer.deleted)
	}
}

func TestIndexingRepository_IndexFailureDoesNotFailMutation(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{err: fmt.Errorf("search backend down")}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-02"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create must succeed despite index failure: %v", err)
	}
	if _, err := svc.GetByID(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("CI must be persisted despite index failure: %v", err)
	}
	if _, err := svc.Delete(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("delete must succeed despite index failure: %v", err)
	}
}

func TestIndexingRepository_NilIndexerPassesThrough(t *testing.T) {
	repo := NewMemoryRepository()
	if got := NewIndexingRepository(repo, nil); got != repo {
		t.Fatal("nil indexer must return the repository unchanged")
	}
}

func TestIndexingRepository_ForwardsChangeHistory(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	decorated := NewIndexingRepository(repo, indexer)

	reader, ok := decorated.(ChangeReader)
	if !ok {
		t.Fatal("decorated repository must keep implementing ChangeReader")
	}
	changes, total, err := reader.ListChanges(context.Background(), "org-1", "any-id", api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatalf("list changes through decorator: %v", err)
	}
	if total != 0 || len(changes) != 0 {
		t.Errorf("expected empty forwarded history from memory repo, got %d rows (total %d)", len(changes), total)
	}
}
