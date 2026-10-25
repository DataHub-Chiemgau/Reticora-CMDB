package bootstrap_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// fakeAIRepository counts the chunk writes of the AI chunk indexer; the
// conversation methods are not called.
type fakeAIRepository struct {
	ai.Repository
	upserts, deletes int
}

func (r *fakeAIRepository) UpsertChunk(context.Context, ai.Chunk) error {
	r.upserts++
	return nil
}

func (r *fakeAIRepository) DeleteChunk(context.Context, string, string, string) error {
	r.deletes++
	return nil
}

// fakeSearchBackend counts the index writes of the search indexer.
type fakeSearchBackend struct {
	search.Backend
	indexed, deleted int
}

func (b *fakeSearchBackend) IndexDocument(context.Context, search.Document) error {
	b.indexed++
	return nil
}

func (b *fakeSearchBackend) Delete(context.Context, string, string, string) error {
	b.deleted++
	return nil
}

// embeddingProvider is an external provider with embeddings that counts
// them.
type embeddingProvider struct{ embeds int }

func (p *embeddingProvider) Enabled() bool           { return true }
func (p *embeddingProvider) EmbeddingsEnabled() bool { return true }
func (p *embeddingProvider) Chat([]ai.Message) (string, int, int, error) {
	return "", 0, 0, nil
}

func (p *embeddingProvider) Embed(string) ([]float64, error) {
	p.embeds++
	return []float64{1}, nil
}

// consenting admits every organization.
type consenting struct{}

func (consenting) AllowsExternalAI(context.Context, string) bool { return true }

// TestNewCIRepositoryWiresAIOnlyWithModule covers F-301 (E-03, WP-418):
// the AI chunk decorator is wired only with the late module ai. With the
// G1 default (RETICORA_MODULES empty), without modules or with other
// modules the AI repository receives no chunk write; with ai exactly one
// UpsertChunk per CI change (create, update) and one DeleteChunk per
// delete, embedded through the given consent. The search index is kept in
// sync in every case; a nil search backend or AI repository leaves its
// decorator out.
func TestNewCIRepositoryWiresAIOnlyWithModule(t *testing.T) {
	ctx := context.Background()
	parse := func(spec string) bootstrap.Modules {
		t.Helper()
		modules, err := server.ParseModules(spec)
		if err != nil {
			t.Fatal(err)
		}
		return modules
	}
	for _, tc := range []struct {
		name    string
		modules bootstrap.Modules
		consent ai.Consent
		// wantAI: one chunk write per CI change; wantEmbeds: embeddings.
		wantAI     bool
		wantEmbeds int
	}{
		{name: "G1 default", modules: parse(""), consent: consenting{}},
		{name: "no modules", modules: nil, consent: consenting{}},
		{name: "other modules", modules: parse("inventory,iga"), consent: consenting{}},
		{name: "module ai", modules: parse("ai"), consent: consenting{}, wantAI: true, wantEmbeds: 2},
		{name: "all modules", modules: parse("all"), consent: consenting{}, wantAI: true, wantEmbeds: 2},
		// Without a consent no embedding is requested (fail-closed).
		{name: "module ai without consent", modules: parse("ai"), consent: nil, wantAI: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aiRepo, backend, provider := &fakeAIRepository{}, &fakeSearchBackend{}, &embeddingProvider{}
			svc := ci.NewService(bootstrap.NewCIRepository(ci.NewMemoryRepository(), backend, aiRepo, provider, tc.consent, tc.modules))
			item := &ci.Item{OrganizationID: "org-1", CITypeID: "c1000000-0000-4000-8000-000000000001", Name: "srv-1"}
			if err := svc.Create(ctx, item); err != nil {
				t.Fatalf("create: %v", err)
			}
			name := "srv-1-renamed"
			if _, err := svc.Update(ctx, "org-1", item.ID, ci.UpdateRequest{Name: &name}); err != nil {
				t.Fatalf("update: %v", err)
			}
			if _, err := svc.Delete(ctx, "org-1", item.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			wantUpserts, wantDeletes := 0, 0
			if tc.wantAI {
				wantUpserts, wantDeletes = 2, 1
			}
			if aiRepo.upserts != wantUpserts || aiRepo.deletes != wantDeletes {
				t.Errorf("AI repository: %d UpsertChunk, %d DeleteChunk; want %d, %d", aiRepo.upserts, aiRepo.deletes, wantUpserts, wantDeletes)
			}
			if provider.embeds != tc.wantEmbeds {
				t.Errorf("provider: %d embeddings, want %d", provider.embeds, tc.wantEmbeds)
			}
			if backend.indexed != 2 || backend.deleted != 1 {
				t.Errorf("search index: %d indexed, %d deleted; want 2, 1", backend.indexed, backend.deleted)
			}
		})
	}

	t.Run("nil search backend and AI repository", func(t *testing.T) {
		base := ci.NewMemoryRepository()
		if repo := bootstrap.NewCIRepository(base, nil, nil, nil, nil, parse("ai")); repo != ci.Repository(base) {
			t.Fatalf("without search backend and AI repository the repository must stay undecorated, got %T", repo)
		}
		aiRepo := &fakeAIRepository{}
		svc := ci.NewService(bootstrap.NewCIRepository(base, nil, aiRepo, nil, nil, parse("ai")))
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "c1000000-0000-4000-8000-000000000001", Name: "srv-2"}
		if err := svc.Create(ctx, item); err != nil {
			t.Fatalf("create without search backend: %v", err)
		}
		if aiRepo.upserts != 1 {
			t.Errorf("AI repository without search backend: %d UpsertChunk, want 1", aiRepo.upserts)
		}
	})
}
