package ai

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// CIChunkIndexer adapts the AI chunk store to the ci.Indexer interface so
// every CI mutation is mirrored into `ai_chunk` for the governed RAG
// assistant. It mirrors ci.IndexDocumentFor into a retrieval chunk: the title
// and URL match the search index, and the content is the textual summary so
// the assistant can rank chunks lexically even when no embedding provider is
// configured.
//
// The adapter lives in the ai package (not ci) so the ci domain stays free
// of AI dependencies; wiring happens in cmd/server.
type CIChunkIndexer struct {
	repo     Repository
	provider Provider
}

// NewCIChunkIndexer wraps the AI repository for use with
// ci.NewIndexingRepository. A nil repo returns nil so the CI repository is
// used undecorated; the provider may be nil (lexical scoring only).
func NewCIChunkIndexer(repo Repository, provider Provider) *CIChunkIndexer {
	if repo == nil {
		return nil
	}
	return &CIChunkIndexer{repo: repo, provider: provider}
}

// IndexDocument upserts the retrieval chunk for a CI document.
func (x *CIChunkIndexer) IndexDocument(ctx context.Context, doc ci.Document) error {
	chunk := Chunk{
		OrganizationID: doc.OrganizationID,
		EntityType:     doc.EntityType,
		EntityID:       doc.EntityID,
		Title:          doc.Title,
		Content:        doc.Summary,
		URL:            doc.URL,
	}
	if x.provider != nil && x.provider.EmbeddingsEnabled() {
		// Embedding failures degrade gracefully to lexical scoring; the chunk
		// is still upserted without a vector so retrieval keeps working.
		if emb, err := x.provider.Embed(doc.Title + " " + doc.Summary); err == nil {
			chunk.Embedding = emb
		}
	}
	return x.repo.UpsertChunk(ctx, chunk)
}

// DeleteDocument removes the retrieval chunk for a CI document.
func (x *CIChunkIndexer) DeleteDocument(ctx context.Context, orgID, entityType, entityID string) error {
	return x.repo.DeleteChunk(ctx, orgID, entityType, entityID)
}
