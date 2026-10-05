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

// countingProvider records embedding calls.
type countingProvider struct {
	DisabledProvider
	embeds int
}

func (p *countingProvider) EmbeddingsEnabled() bool { return true }
func (p *countingProvider) Embed(string) ([]float64, error) {
	p.embeds++
	return []float64{1, 0}, nil
}

type optIns map[string]bool

func (o optIns) AIOptIn(_ context.Context, orgID string) (bool, error) { return o[orgID], nil }

type addons map[string]bool

func (a addons) IsEnabled(_ context.Context, orgID, feature string) bool {
	return feature == FeatureAI && a[orgID]
}

// TestCIChunkIndexerEmbedsOnlyWithOptInAndAddon covers WP-076 (AI-02): the
// embedding provider is called only for organizations that opted in and
// hold the ai add-on; without consent the chunk stays lexical; the
// air-gapped profile disables the provider entirely.
func TestCIChunkIndexerEmbedsOnlyWithOptInAndAddon(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	provider := &countingProvider{}
	consent := OrgConsent{OptIn: optIns{"opted": true, "opted-no-addon": true}, Entitlements: addons{"opted": true, "addon-only": true}}
	idx := NewCIChunkIndexer(repo, provider).WithConsent(consent)
	for _, org := range []string{"opted", "opted-no-addon", "addon-only", "neither"} {
		if err := idx.IndexDocument(ctx, ci.Document{OrganizationID: org, EntityType: "ci", EntityID: "c-" + org, Title: org}); err != nil {
			t.Fatal(err)
		}
		chunks, _ := repo.CandidateChunks(ctx, org, []string{"ci"}, []string{"c-" + org}, 1)
		embedded := len(chunks) == 1 && len(chunks[0].Embedding) > 0
		if want := org == "opted"; embedded != want || len(chunks) != 1 {
			t.Errorf("%s: %d chunks, embedded %v, want embedded %v", org, len(chunks), embedded, want)
		}
	}
	if provider.embeds != 1 {
		t.Errorf("provider called %d times, want 1 (only the opted-in organization with the add-on)", provider.embeds)
	}

	// Without a consent no embedding is requested at all.
	provider.embeds = 0
	if err := NewCIChunkIndexer(repo, provider).IndexDocument(ctx, ci.Document{OrganizationID: "opted", EntityType: "ci", EntityID: "c-x"}); err != nil {
		t.Fatal(err)
	}
	if provider.embeds != 0 {
		t.Errorf("provider called without consent: %d", provider.embeds)
	}

	airGapped := NewProvider(&ProviderConfig{BaseURL: "https://llm.example", ChatModel: "m", EmbeddingModel: "e"}, true, nil)
	if airGapped.Enabled() || airGapped.EmbeddingsEnabled() {
		t.Error("air-gapped provider is enabled")
	}
	if p := NewProvider(&ProviderConfig{BaseURL: "https://llm.example", ChatModel: "m", EmbeddingModel: "e"}, false, nil); !p.EmbeddingsEnabled() {
		t.Error("configured provider disabled outside the air-gapped profile")
	}
}
