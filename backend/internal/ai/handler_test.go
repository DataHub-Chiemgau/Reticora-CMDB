package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	consent := OrgConsent{OptIn: optIns{"org": true}, Entitlements: addons{"org": true}}
	retr := NewRetriever(repo, sr, perms, stubProvider{answer: "Antwort"}).WithConsent(consent)
	h := NewHandler(repo, stubProvider{answer: "Antwort"}, retr).WithConsent(consent)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

func ask(t *testing.T, mux chi.Router, question string) AskResponse {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", bytes.NewBufferString(`{"question":`+jsonQuote(question)+`}`))
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

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestAskWithoutChunksSkipsProviderAndCitesNothing(t *testing.T) {
	mux := setupAsk(t, nil)
	out := ask(t, mux, "Was ist der Status?")
	// The stub provider reports one token per call: zero tokens prove that
	// the provider was not asked (AI-01).
	if out.Answer != noGroundingAnswer || out.PromptTokens != 0 || out.CompletionTokens != 0 {
		t.Fatalf("expected the fixed no-grounding answer without provider call, got %q (%d/%d tokens)", out.Answer, out.PromptTokens, out.CompletionTokens)
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

// recordingProvider counts every chat and embedding request.
type recordingProvider struct{ chats, embeds int }

func (p *recordingProvider) Enabled() bool           { return true }
func (p *recordingProvider) EmbeddingsEnabled() bool { return true }
func (p *recordingProvider) Chat([]Message) (string, int, int, error) {
	p.chats++
	return "Antwort", 1, 1, nil
}

func (p *recordingProvider) Embed(string) ([]float64, error) {
	p.embeds++
	return []float64{1, 0}, nil
}

// TestAskWithoutConsentIsForbidden covers WP-360 (AI-02): without the
// organization's opt-in, without the ai add-on or without any consent
// wired, Ask answers 403 ai-opt-in-required, creates no conversation and
// sends nothing to the provider (neither the question embedding nor the
// chat); with both the question is embedded and answered.
func TestAskWithoutConsentIsForbidden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		consent Consent
		want    int
	}{
		{"no consent wired", nil, http.StatusForbidden},
		{"neither", OrgConsent{OptIn: optIns{}, Entitlements: addons{}}, http.StatusForbidden},
		{"opt-in without add-on", OrgConsent{OptIn: optIns{"org": true}, Entitlements: addons{}}, http.StatusForbidden},
		{"add-on without opt-in", OrgConsent{OptIn: optIns{}, Entitlements: addons{"org": true}}, http.StatusForbidden},
		{"opt-in of another organization", OrgConsent{OptIn: optIns{"other": true}, Entitlements: addons{"org": true, "other": true}}, http.StatusForbidden},
		{"opt-in and add-on", OrgConsent{OptIn: optIns{"org": true}, Entitlements: addons{"org": true}}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := NewMemoryRepository()
			sr := search.NewMemoryRepository()
			_ = sr.IndexDocument(ctx, search.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router"})
			_ = repo.UpsertChunk(ctx, Chunk{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Content: "core router", URL: "/cmdb/c1"})
			perms := permission.NewMemoryRepository()
			grantPermission(perms, "org", "user", "ci:read")
			provider := &recordingProvider{}
			retr := NewRetriever(repo, sr, perms, provider)
			h := NewHandler(repo, provider, retr)
			if tc.consent != nil {
				retr.WithConsent(tc.consent)
				h.WithConsent(tc.consent)
			}
			mux := chi.NewRouter()
			h.RegisterRoutes(mux)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", bytes.NewBufferString(`{"question":"Router"}`))
			r = r.WithContext(tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			convs, _ := repo.ListConversations(ctx, "org", "user")
			if tc.want == http.StatusOK {
				if w.Code != http.StatusOK || provider.embeds != 1 || provider.chats != 1 || len(convs) != 1 {
					t.Fatalf("ask with consent: %d %s, %d embedding and %d chat call(s), %d conversation(s); want 200, one each",
						w.Code, w.Body.String(), provider.embeds, provider.chats, len(convs))
				}
				return
			}
			var problem struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &problem)
			if w.Code != http.StatusForbidden || problem.Type != ProblemAIOptInRequired || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("ask: %d %q %s; want 403 problem+json %s", w.Code, w.Header().Get("Content-Type"), w.Body.String(), ProblemAIOptInRequired)
			}
			if provider.embeds != 0 || provider.chats != 0 || len(convs) != 0 {
				t.Errorf("ask refused: %d embedding and %d chat call(s), %d conversation(s); want none", provider.embeds, provider.chats, len(convs))
			}
		})
	}
}

// TestRetrieverEmbedsQuestionOnlyWithConsent covers WP-360 (AI-02): the
// retriever ranks lexically and sends the question to the provider only
// with the organization's consent.
func TestRetrieverEmbedsQuestionOnlyWithConsent(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(ctx, search.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router"})
	_ = repo.UpsertChunk(ctx, Chunk{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Content: "core router"})
	perms := permission.NewMemoryRepository()
	grantPermission(perms, "org", "user", "ci:read")
	provider := &recordingProvider{}
	retr := NewRetriever(repo, sr, perms, provider)
	for _, consent := range []Consent{nil, OrgConsent{OptIn: optIns{"org": true}, Entitlements: addons{}}} {
		retr.consent = consent
		chunks, _, err := retr.Retrieve(ctx, "org", "user", "Router", 5)
		if err != nil || len(chunks) != 1 {
			t.Fatalf("retrieve without consent %v: %d chunk(s), %v; want the lexical match", consent, len(chunks), err)
		}
	}
	if provider.embeds != 0 {
		t.Fatalf("retriever without consent embedded the question %d time(s)", provider.embeds)
	}
	retr.WithConsent(OrgConsent{OptIn: optIns{"org": true}, Entitlements: addons{"org": true}})
	if _, _, err := retr.Retrieve(ctx, "org", "user", "Router", 5); err != nil || provider.embeds != 1 {
		t.Fatalf("retriever with consent: %d embedding(s), %v; want 1", provider.embeds, err)
	}
}
