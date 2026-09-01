package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// stubProvider answers every chat request with a fixed message.
type stubProvider struct{ answer string }

func (p stubProvider) Enabled() bool { return true }
func (p stubProvider) Chat([]Message) (string, int, int, error) {
	return p.answer, 1, 1, nil
}
func (p stubProvider) Embed(string) ([]float64, error) { return nil, nil }
func (p stubProvider) EmbeddingsEnabled() bool         { return false }

func setupAsk(t *testing.T, indexSeed func(sr *search.MemoryRepository, repo *MemoryRepository)) chi.Router {
	t.Helper()
	repo := NewMemoryRepository()
	sr := search.NewMemoryRepository()
	if indexSeed != nil {
		indexSeed(sr, repo)
	}
	perms := permission.NewMemoryRepository()
	retr := NewRetriever(repo, sr, perms, stubProvider{answer: "Antwort"})
	h := NewHandler(repo, stubProvider{answer: "Antwort"}, retr)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

func ask(t *testing.T, mux chi.Router, question string) AskResponse {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", bytes.NewBufferString(`{"question":`+strconv(question)+`}`))
	r = r.WithContext(tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ask: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var out AskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func strconv(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestAskWithoutChunksReturnsUngroundedNoticeAndNoCitations(t *testing.T) {
	mux := setupAsk(t, nil)
	out := ask(t, mux, "Was ist der Status?")
	if !strings.Contains(out.Answer, "keine passenden tenant-eigenen Daten") {
		t.Fatalf("expected ungrounded notice, got %q", out.Answer)
	}
	if len(out.Citations) != 0 {
		t.Fatalf("expected no citations without chunks, got %d", len(out.Citations))
	}
}

func TestAskWithChunksReturnsCitations(t *testing.T) {
	mux := setupAsk(t, func(sr *search.MemoryRepository, repo *MemoryRepository) {
		_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router"})
		_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Content: "core router", URL: "/cmdb/c1"})
	})
	// No permission granted -> the retriever filters the chunk out, so the
	// answer is ungrounded and carries no citations.
	out := ask(t, mux, "Router?")
	if len(out.Citations) != 0 {
		t.Fatalf("expected citations to be filtered without ci:read, got %d", len(out.Citations))
	}
}
