package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

func TestCosine(t *testing.T) {
	if got := Cosine([]float64{1, 0}, []float64{1, 0}); got != 1 {
		t.Fatalf("got %v", got)
	}
}
func TestDisabledProvider(t *testing.T) {
	p := NewOpenAIProvider(ProviderConfig{}, nil)
	if p.Enabled() {
		t.Fatal("provider should be disabled")
	}
	if _, _, _, err := p.Chat(nil); err == nil {
		t.Fatal("expected disabled error")
	}
}
func TestOpenAIProviderHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
		case "/embeddings":
			w.Write([]byte(`{"data":[{"embedding":[1,0]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{BaseURL: srv.URL, ChatModel: "chat", EmbeddingModel: "emb"}, srv.Client())
	ans, pt, ct, err := p.Chat([]Message{{Role: "user", Content: "hi"}})
	if err != nil || ans != "ok" || pt != 3 || ct != 2 {
		t.Fatalf("%q %d %d %v", ans, pt, ct, err)
	}
	emb, err := p.Embed("x")
	if err != nil || len(emb) != 2 {
		t.Fatalf("%v %v", emb, err)
	}
}
func TestRetrievalPermissionScoping(t *testing.T) {
	sr := search.NewMemoryRepository()
	sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org", EntityType: "ticket", EntityID: "t1", Title: "Secret"})
	repo := NewMemoryRepository()
	repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org", EntityType: "ticket", EntityID: "t1", Title: "Secret", Content: "secret text"})
	perms := permission.NewMemoryRepository()
	retr := NewRetriever(repo, sr, perms, DisabledProvider{})
	chunks, _, err := retr.Retrieve(context.Background(), "org", "user", "Secret", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no chunks without ticket:read, got %d", len(chunks))
	}
}
func TestBuildPromptUsesOnlyChunks(t *testing.T) {
	prompt := buildPrompt("q", []Chunk{{Title: "Owned", Content: "tenant data"}})
	if !strings.Contains(prompt, "tenant data") || strings.Contains(prompt, "other tenant") {
		t.Fatal(prompt)
	}
}
