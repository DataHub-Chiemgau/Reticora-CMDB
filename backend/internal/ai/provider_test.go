package ai

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestNewProviderAirGappedMakesNoExternalCall covers WP-076/WP-360 (AI-02):
// in the air-gapped profile the configured provider is replaced by the
// disabled one; chat and embeddings are off and nothing reaches the
// configured endpoint, even when called.
func TestNewProviderAirGappedMakesNoExternalCall(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}],"data":[{"embedding":[1]}]}`))
	}))
	defer srv.Close()
	cfg := &ProviderConfig{BaseURL: srv.URL, ChatModel: "chat", EmbeddingModel: "emb"}

	p := NewProvider(cfg, true, srv.Client())
	if p.Enabled() || p.EmbeddingsEnabled() {
		t.Fatalf("air-gapped provider: chat %v, embeddings %v; want both disabled", p.Enabled(), p.EmbeddingsEnabled())
	}
	if _, _, _, err := p.Chat([]Message{{Role: "user", Content: "q"}}); err == nil {
		t.Error("air-gapped provider answered a chat request")
	}
	if _, err := p.Embed("q"); err == nil {
		t.Error("air-gapped provider answered an embedding request")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("air-gapped provider made %d HTTP call(s) to the configured endpoint; want none", n)
	}

	// Control: the same configuration outside the air gap reaches the
	// endpoint, so the fake server would have seen an air-gapped call.
	p = NewProvider(cfg, false, srv.Client())
	if !p.Enabled() || !p.EmbeddingsEnabled() {
		t.Fatalf("connected provider: chat %v, embeddings %v; want both enabled", p.Enabled(), p.EmbeddingsEnabled())
	}
	if _, _, _, err := p.Chat([]Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("connected chat: %v", err)
	}
	if _, err := p.Embed("q"); err != nil {
		t.Fatalf("connected embed: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("connected provider made %d HTTP call(s); want 2", n)
	}
}
