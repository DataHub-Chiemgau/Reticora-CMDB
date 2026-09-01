package ai

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

func TestNewCIChunkIndexer_NilRepoReturnsNil(t *testing.T) {
	if got := NewCIChunkIndexer(nil, DisabledProvider{}); got != nil {
		t.Fatalf("expected nil adapter for nil repo, got %#v", got)
	}
}

func TestCIChunkIndexerMirrorsDocument(t *testing.T) {
	repo := NewMemoryRepository()
	idx := NewCIChunkIndexer(repo, DisabledProvider{})
	doc := ci.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Summary: "core router acme", URL: "/cmdb/c1"}
	if err := idx.IndexDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	chunks, err := repo.CandidateChunks(context.Background(), "org", []string{"ci"}, []string{"c1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Title != "Router" || chunks[0].Content != "core router acme" || chunks[0].URL != "/cmdb/c1" {
		t.Fatalf("unexpected chunk: %+v", chunks[0])
	}
	// Lexical fallback: without an embedding provider no vector is stored.
	if len(chunks[0].Embedding) != 0 {
		t.Fatalf("expected no embedding without provider, got %d values", len(chunks[0].Embedding))
	}
}

func TestCIChunkIndexerEmbeddingDegradesGracefully(t *testing.T) {
	repo := NewMemoryRepository()
	// DisabledProvider has EmbeddingsEnabled() == false, so no embed call is
	// made; a provider with a failing embed path must still persist the chunk.
	idx := NewCIChunkIndexer(repo, DisabledProvider{})
	doc := ci.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c2", Title: "Switch", Summary: "access switch"}
	if err := idx.IndexDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	chunks, _ := repo.CandidateChunks(context.Background(), "org", []string{"ci"}, []string{"c2"}, 1)
	if len(chunks) != 1 {
		t.Fatalf("expected chunk to be persisted despite missing embeddings, got %d", len(chunks))
	}
}

func TestCIChunkIndexerDelete(t *testing.T) {
	repo := NewMemoryRepository()
	idx := NewCIChunkIndexer(repo, DisabledProvider{})
	doc := ci.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c3", Title: "Server", Summary: "db server"}
	if err := idx.IndexDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	// A chunk with an explicitly set ID must be found by DeleteChunk too.
	if err := repo.UpsertChunk(context.Background(), Chunk{ID: "custom-id", OrganizationID: "org", EntityType: "ci", EntityID: "c4", Title: "X", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.DeleteDocument(context.Background(), "org", "ci", "c3"); err != nil {
		t.Fatal(err)
	}
	if err := idx.DeleteDocument(context.Background(), "org", "ci", "c4"); err != nil {
		t.Fatal(err)
	}
	chunks, _ := repo.CandidateChunks(context.Background(), "org", []string{"ci"}, []string{"c3", "c4"}, 10)
	if len(chunks) != 0 {
		t.Fatalf("expected chunks to be deleted, got %d", len(chunks))
	}
}
