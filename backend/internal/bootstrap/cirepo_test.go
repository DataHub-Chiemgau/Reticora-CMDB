package bootstrap_test

import (
	"context"
	"reflect"
	"sort"
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

func (r *fakeAIRepository) UpsertChunk(context.Context, *ai.Chunk) error {
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

func (b *fakeSearchBackend) IndexDocument(context.Context, *search.Document) error {
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

// ciRestorer is the reactivation interface the discovery review expects of
// the CI repository (discovery.ciRestorer, unexported there).
type ciRestorer interface {
	Restore(ctx context.Context, orgID, id string) (*ci.Item, error)
}

// chainExceptions names the exported methods of *ci.PGRepository the
// decorator chain does not offer, with the reason. Every other method must
// reach the callers through the chain.
var chainExceptions = map[string]string{
	"WithEvents":   "builder used while wiring the repositories (server.PostgresRepositories), before the chain is built",
	"WithComputed": "builder used while wiring the repositories (server.PostgresRepositories, WP-717), before the chain is built",
}

// TestCIRepositoryChainForwardsOptionalInterfaces is the regression guard of
// SR-11 (WP-833, CI-13, MET-15): the CI repository chain cmd/server builds
// with bootstrap.NewCIRepository (search and AI decorator, module ai) on
// *ci.PGRepository offers every optional interface callers find by type
// assertion and every exported method of *ci.PGRepository, with the same
// signature. Only the method set is checked; no database is used. A Phase-2
// WP that gives *ci.PGRepository a method forwards it in
// internal/ci/indexing_repository.go or names an exception in
// chainExceptions.
func TestCIRepositoryChainForwardsOptionalInterfaces(t *testing.T) {
	modules, err := server.ParseModules("ai")
	if err != nil {
		t.Fatal(err)
	}
	base := ci.NewPGRepository(nil)
	chain := bootstrap.NewCIRepository(base, &fakeSearchBackend{}, &fakeAIRepository{}, &embeddingProvider{}, consenting{}, modules)

	// The chain holds both decorators around the PostgreSQL repository:
	// the search decorator inside, the AI decorator outermost.
	if depth, inner := decoratorDepth(chain); depth != 2 || inner != reflect.TypeOf(base) {
		t.Fatalf("chain: %d decorators around %v, want 2 around %T", depth, inner, base)
	}

	for _, tc := range []struct {
		name string
		has  func(ci.Repository) bool
	}{
		{"Restore (discovery.ciRestorer)", func(r ci.Repository) bool { _, ok := r.(ciRestorer); return ok }},
		{"ci.UniqueChecker", func(r ci.Repository) bool { _, ok := r.(ci.UniqueChecker); return ok }},
		{"ci.BatchCreator", func(r ci.Repository) bool { _, ok := r.(ci.BatchCreator); return ok }},
		{"ci.NodeReader", func(r ci.Repository) bool { _, ok := r.(ci.NodeReader); return ok }},
		{"ci.BatchReader", func(r ci.Repository) bool { _, ok := r.(ci.BatchReader); return ok }},
		{"ci.TypeChanger", func(r ci.Repository) bool { _, ok := r.(ci.TypeChanger); return ok }},
		{"ci.RecordUpdater", func(r ci.Repository) bool { _, ok := r.(ci.RecordUpdater); return ok }},
		{"ci.Merger", func(r ci.Repository) bool { _, ok := r.(ci.Merger); return ok }},
		{"ci.Unmerger", func(r ci.Repository) bool { _, ok := r.(ci.Unmerger); return ok }},
		{"ci.TombstoneReader", func(r ci.Repository) bool { _, ok := r.(ci.TombstoneReader); return ok }},
		{"ci.Restorer", func(r ci.Repository) bool { _, ok := r.(ci.Restorer); return ok }},
		{"ci.ChangeReader", func(r ci.Repository) bool { _, ok := r.(ci.ChangeReader); return ok }},
		{"ci.ActiveCounter", func(r ci.Repository) bool { _, ok := r.(ci.ActiveCounter); return ok }},
	} {
		if !tc.has(base) {
			t.Errorf("%s: *ci.PGRepository does not implement it; update this test", tc.name)
			continue
		}
		if !tc.has(chain) {
			t.Errorf("%s: the chain of bootstrap.NewCIRepository hides it (SR-11); forward it in internal/ci/indexing_repository.go", tc.name)
		}
	}

	baseValue, chainValue := reflect.ValueOf(base), reflect.ValueOf(chain)
	baseType := baseValue.Type()
	var missing []string
	for i := range baseType.NumMethod() {
		name := baseType.Method(i).Name
		if _, excepted := chainExceptions[name]; excepted {
			if chainValue.MethodByName(name).IsValid() {
				t.Errorf("%s: the chain offers it; remove the exception from chainExceptions", name)
			}
			continue
		}
		forwarded := chainValue.MethodByName(name)
		if !forwarded.IsValid() {
			missing = append(missing, name)
			continue
		}
		if want, got := baseValue.Method(i).Type(), forwarded.Type(); want != got {
			t.Errorf("%s: the chain offers %v, *ci.PGRepository %v", name, got, want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the chain of bootstrap.NewCIRepository hides methods of *ci.PGRepository (SR-11): %v; "+
			"forward them in internal/ci/indexing_repository.go or name an exception in chainExceptions", missing)
	}
	var stale []string
	for name := range chainExceptions {
		if _, ok := baseType.MethodByName(name); !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("chainExceptions names methods *ci.PGRepository no longer has: %v", stale)
	}
}

// decoratorDepth unwraps the decorators of the chain through their embedded
// ci.Repository (first field) and returns their number and the type of the
// innermost repository.
func decoratorDepth(repo ci.Repository) (int, reflect.Type) {
	depth := 0
	v := reflect.ValueOf(repo)
	for v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Struct && v.Elem().NumField() > 0 {
		field := v.Elem().Type().Field(0)
		if !field.Anonymous || field.Type != reflect.TypeOf((*ci.Repository)(nil)).Elem() {
			break
		}
		v = v.Elem().Field(0).Elem()
		depth++
	}
	return depth, v.Type()
}
