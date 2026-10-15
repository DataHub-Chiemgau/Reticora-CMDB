package ai

import (
	"context"
	"log/slog"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
	consent  Consent
}

// Consent decides whether an organization's data may be sent to the
// external AI provider (AI-02): the organization opted in and holds the ai
// add-on.
type Consent interface {
	AllowsExternalAI(ctx context.Context, orgID string) bool
}

// WithConsent attaches the per-organization consent. Without one, no
// embedding is requested (fail-closed); chunks stay lexical.
func (x *CIChunkIndexer) WithConsent(consent Consent) *CIChunkIndexer {
	if x != nil {
		x.consent = consent
	}
	return x
}

// OptInSource reports an organization's AI opt-in.
type OptInSource interface {
	AIOptIn(ctx context.Context, orgID string) (bool, error)
}

// EntitlementChecker reports whether an organization holds a feature.
type EntitlementChecker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// FeatureAI is the AI add-on entitlement (CH14).
const FeatureAI = "ai"

// OrgConsent grants external AI processing to organizations that opted in
// and hold the ai add-on; a failing lookup denies.
type OrgConsent struct {
	OptIn        OptInSource
	Entitlements EntitlementChecker
}

// AllowsExternalAI implements Consent.
func (c OrgConsent) AllowsExternalAI(ctx context.Context, orgID string) bool {
	if c.OptIn == nil || c.Entitlements == nil || !c.Entitlements.IsEnabled(ctx, orgID, FeatureAI) {
		return false
	}
	ok, err := c.OptIn.AIOptIn(ctx, orgID)
	return err == nil && ok
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

// mayEmbed reports whether embeddings can be requested at all: a provider
// with embeddings and a consent to ask. It reads no external state.
func (x *CIChunkIndexer) mayEmbed() bool {
	return x.provider != nil && x.provider.EmbeddingsEnabled() && x.consent != nil
}

// IndexDocument upserts the retrieval chunk for a CI document.
func (x *CIChunkIndexer) IndexDocument(ctx context.Context, doc ci.Document) error {
	chunk := chunkOf(&doc)
	if !x.mayEmbed() {
		return x.repo.UpsertChunk(ctx, chunk)
	}
	if !database.InTx(ctx) {
		// The provider is called only for organizations that opted in and
		// hold the ai add-on (AI-02).
		if x.consent.AllowsExternalAI(ctx, doc.OrganizationID) {
			x.embed(&chunk)
		}
		return x.repo.UpsertChunk(ctx, chunk)
	}
	// Inside a context transaction the chunk is written lexically with the
	// CI; the consent (entitlement cache, opt-in) and the embedding, an
	// external HTTP call, follow after the commit: the transaction never
	// holds its locks while Redis or the provider answers (NFR-03).
	if err := x.repo.UpsertChunk(ctx, chunk); err != nil {
		return err
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if !x.consent.AllowsExternalAI(ctx, chunk.OrganizationID) || !x.embed(&chunk) {
			return
		}
		if err := x.repo.UpsertChunk(ctx, chunk); err != nil {
			slog.WarnContext(ctx, "AI chunk embedding not stored", "entity_id", chunk.EntityID, "error", err)
		}
	})
	return nil
}

// chunkOf is the retrieval chunk of a CI document.
func chunkOf(doc *ci.Document) Chunk {
	return Chunk{
		OrganizationID: doc.OrganizationID,
		EntityType:     doc.EntityType,
		EntityID:       doc.EntityID,
		Title:          doc.Title,
		Content:        doc.Summary,
		URL:            doc.URL,
	}
}

// ChunkBatchWriter upserts several chunks of one organization at once
// (NFR-03, WP-333); the PostgreSQL repository implements it.
type ChunkBatchWriter interface {
	UpsertChunks(ctx context.Context, chunks []Chunk) error
}

// IndexDocuments implements ci.BatchIndexer: the chunks of the documents of
// one organization with one statement where the repository can, else chunk
// by chunk. The consent is checked once for the batch; inside a context
// transaction the consent and the embeddings follow after the commit, as in
// IndexDocument.
func (x *CIChunkIndexer) IndexDocuments(ctx context.Context, docs []ci.Document) error {
	batch, ok := x.repo.(ChunkBatchWriter)
	if !ok || len(docs) == 0 {
		for i := range docs {
			if err := x.IndexDocument(ctx, docs[i]); err != nil {
				return err
			}
		}
		return nil
	}
	chunks := make([]Chunk, len(docs))
	for i := range docs {
		chunks[i] = chunkOf(&docs[i])
	}
	if !x.mayEmbed() {
		return batch.UpsertChunks(ctx, chunks)
	}
	orgID := docs[0].OrganizationID
	if !database.InTx(ctx) {
		if x.consent.AllowsExternalAI(ctx, orgID) {
			for i := range chunks {
				x.embed(&chunks[i])
			}
		}
		return batch.UpsertChunks(ctx, chunks)
	}
	if err := batch.UpsertChunks(ctx, chunks); err != nil {
		return err
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if !x.consent.AllowsExternalAI(ctx, orgID) {
			return
		}
		var embedded []Chunk
		for i := range chunks {
			if x.embed(&chunks[i]) {
				embedded = append(embedded, chunks[i])
			}
		}
		if err := batch.UpsertChunks(ctx, embedded); err != nil {
			slog.WarnContext(ctx, "AI chunk embeddings not stored", "chunks", len(embedded), "error", err)
		}
	})
	return nil
}

// embed adds the embedding of the chunk and reports whether it got one.
// Embedding failures degrade gracefully to lexical scoring; the chunk is
// still upserted without a vector so retrieval keeps working.
func (x *CIChunkIndexer) embed(chunk *Chunk) bool {
	emb, err := x.provider.Embed(chunk.Title + " " + chunk.Content)
	if err != nil {
		return false
	}
	chunk.Embedding = emb
	return true
}

// DeleteDocument removes the retrieval chunk for a CI document.
func (x *CIChunkIndexer) DeleteDocument(ctx context.Context, orgID, entityType, entityID string) error {
	return x.repo.DeleteChunk(ctx, orgID, entityType, entityID)
}
