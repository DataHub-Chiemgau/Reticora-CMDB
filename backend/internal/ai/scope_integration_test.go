package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// grantAll grants exactly the listed permission keys.
type grantAll struct {
	permission.Repository
	keys map[string]bool
}

func (g grantAll) HasPermission(_ context.Context, _, _, key string) (bool, error) {
	return g.keys[key], nil
}

// countingProvider records every chat call.
type countingProvider struct{ calls *int }

func (p countingProvider) Enabled() bool { return true }
func (p countingProvider) Chat([]ai.Message) (string, int, int, error) {
	*p.calls++
	return "grounded answer", 3, 4, nil
}
func (p countingProvider) Embed(string) ([]float64, error) { return nil, nil }
func (p countingProvider) EmbeddingsEnabled() bool         { return false }

// TestRAGChunksFollowSourceScope covers WP-037 (AI-01): chunks derive client
// and site from their source object (migration 000069), the policy hides
// chunks outside the principal's scope, chunks follow a moved source, and an
// answer is only generated from permitted, cited sources: without one the
// provider is not called.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRAGChunksFollowSourceScope(t *testing.T) {
	f := scopetest.Seed(t, "50")
	bg := context.Background()
	own := f.CI(t, f.OrgA, f.Client1, "rag-own-firewall")
	foreign := f.CI(t, f.OrgA, f.Client2, "rag-foreign-loadbalancer")
	user := f.AppUser(t, f.OrgA, "rag-user")

	repo := ai.NewPGRepository(f.App)
	searchRepo := search.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for id, title := range map[string]string{own: "rag-own-firewall", foreign: "rag-foreign-loadbalancer"} {
		if err := repo.UpsertChunk(orgCtx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, Title: title, Content: title, URL: "/cmdb/" + id}); err != nil {
			t.Fatalf("chunk %s: %v", title, err)
		}
		if err := searchRepo.IndexDocument(orgCtx, search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, Title: title, URL: "/cmdb/" + id}); err != nil {
			t.Fatalf("index %s: %v", title, err)
		}
	}

	clientOf := func(entityID string) string {
		t.Helper()
		var client string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM ai_chunk WHERE entity_id = $1`, entityID).Scan(&client); err != nil {
			t.Fatalf("read chunk: %v", err)
		}
		return client
	}
	if clientOf(foreign) != f.Client2 {
		t.Errorf("chunk of the client-2 CI: client %s", clientOf(foreign))
	}

	// The policy alone hides the foreign chunk.
	c1 := database.OrgWideScope(f.OrgA, user)
	c1.Clients = database.ScopeIDs(f.Client1)
	var n int
	if err := database.WithTenant(bg, f.App, &c1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ai_chunk WHERE entity_id = $1`, foreign).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("client 1 reads %d foreign chunks directly, %v", n, err)
	}

	calls := 0
	provider := countingProvider{calls: &calls}
	retriever := ai.NewRetriever(repo, searchRepo, grantAll{keys: map[string]bool{"ci:read": true}}, provider)
	mux := chi.NewRouter()
	ai.NewHandler(repo, provider, retriever).RegisterRoutes(mux)
	ask := func(question string) ai.AskResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", bytes.NewBufferString(`{"question":"`+question+`"}`))
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		req = req.WithContext(database.ContextWithTenantScope(ctx, &c1))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ask %q: status %d: %s", question, w.Code, w.Body.String())
		}
		var out ai.AskResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// Only the foreign CI matches: no permitted source, no provider call.
	out := ask("loadbalancer")
	if calls != 0 || len(out.Citations) != 0 || out.PromptTokens != 0 {
		t.Errorf("question about a foreign CI: %d provider calls, citations %v", calls, out.Citations)
	}

	// The own CI matches: the answer cites exactly it.
	out = ask("firewall")
	if calls != 1 || len(out.Citations) != 1 || out.Citations[0].EntityID != own {
		t.Errorf("question about the own CI: %d provider calls, citations %+v", calls, out.Citations)
	}

	// The foreign CI moves to client 1 and its chunk follows.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, foreign, f.Client1); err != nil {
		t.Fatalf("move ci: %v", err)
	}
	if clientOf(foreign) != f.Client1 {
		t.Errorf("chunk after the CI moved: client %s, want client 1", clientOf(foreign))
	}
}
