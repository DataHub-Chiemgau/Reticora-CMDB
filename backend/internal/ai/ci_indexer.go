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
	// The provider is called only for organizations that opted in and hold
	// the ai add-on (AI-02).
	if x.provider != nil && x.provider.EmbeddingsEnabled() && x.consent != nil &&
		x.consent.AllowsExternalAI(ctx, doc.OrganizationID) {
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
