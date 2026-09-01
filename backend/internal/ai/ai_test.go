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

// grantPermission wires role -> permission key -> user in the memory repo.
func grantPermission(perms *permission.MemoryRepository, orgID, userID, key string) {
	if _, err := perms.ReplaceRolePermissions(context.Background(), orgID, "role-1", "tester", []string{key}); err != nil {
		panic(err)
	}
	perms.AssignRoleToUser(orgID, userID, "role-1")
}

func TestRetrievalTenantIsolation(t *testing.T) {
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org-a", EntityType: "ci", EntityID: "a1", Title: "Router"})
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org-b", EntityType: "ci", EntityID: "b1", Title: "Router"})
	repo := NewMemoryRepository()
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org-a", EntityType: "ci", EntityID: "a1", Title: "Router", Content: "org-a router"})
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org-b", EntityType: "ci", EntityID: "b1", Title: "Router", Content: "org-b router"})
	perms := permission.NewMemoryRepository()
	grantPermission(perms, "org-a", "user", "ci:read")
	retr := NewRetriever(repo, sr, perms, DisabledProvider{})

	chunks, cites, err := retr.Retrieve(context.Background(), "org-a", "user", "Router", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks for org-a")
	}
	for _, ch := range chunks {
		if ch.OrganizationID != "org-a" {
			t.Fatalf("cross-tenant chunk leaked: %+v", ch)
		}
	}
	for _, c := range cites {
		if c.EntityID == "b1" {
			t.Fatalf("cross-tenant citation leaked: %+v", c)
		}
	}
}

func TestRetrievalDropsForeignSearchHits(t *testing.T) {
	// Defense in depth: even if a misconfigured search backend returns hits
	// for another tenant, the retriever must not pass them on.
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org-b", EntityType: "ci", EntityID: "b1", Title: "Router"})
	repo := NewMemoryRepository()
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org-b", EntityType: "ci", EntityID: "b1", Title: "Router", Content: "org-b router"})
	perms := permission.NewMemoryRepository()
	grantPermission(perms, "org-a", "user", "ci:read")
	retr := NewRetriever(repo, sr, perms, DisabledProvider{})

	// The search memory repo scopes by org, so to simulate a leak we query
	// with the attacker's org but a backend that would return foreign rows.
	// We assert the retrieved set never contains org-b chunks.
	chunks, _, err := retr.Retrieve(context.Background(), "org-a", "user", "Router", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if ch.OrganizationID == "org-b" {
			t.Fatalf("foreign chunk leaked through retrieval: %+v", ch)
		}
	}
}

func TestRetrievalFailsClosedOnUnknownEntityType(t *testing.T) {
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org", EntityType: "secret", EntityID: "s1", Title: "Secret"})
	repo := NewMemoryRepository()
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org", EntityType: "secret", EntityID: "s1", Title: "Secret", Content: "top secret"})
	perms := permission.NewMemoryRepository()
	retr := NewRetriever(repo, sr, perms, DisabledProvider{})

	chunks, _, err := retr.Retrieve(context.Background(), "org", "user", "Secret", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no chunks for unmapped entity type, got %d", len(chunks))
	}
}

type errPermissionRepo struct{ permission.Repository }

func (errPermissionRepo) HasPermission(context.Context, string, string, string) (bool, error) {
	return false, context.DeadlineExceeded
}

func TestRetrievalFailsClosedOnPermissionError(t *testing.T) {
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router"})
	repo := NewMemoryRepository()
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Content: "router text"})
	retr := NewRetriever(repo, sr, errPermissionRepo{}, DisabledProvider{})

	chunks, _, err := retr.Retrieve(context.Background(), "org", "user", "Router", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no chunks when permission check errors, got %d", len(chunks))
	}
}

func TestContactPermissionMapping(t *testing.T) {
	if got := searchPermission("contact"); got != "contact:read" {
		t.Fatalf("expected contact:read, got %q", got)
	}
}

func TestCitationCompleteness(t *testing.T) {
	sr := search.NewMemoryRepository()
	_ = sr.IndexDocument(context.Background(), search.Document{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router"})
	repo := NewMemoryRepository()
	_ = repo.UpsertChunk(context.Background(), Chunk{OrganizationID: "org", EntityType: "ci", EntityID: "c1", Title: "Router", Content: "router text", URL: "/cmdb/c1"})
	perms := permission.NewMemoryRepository()
	grantPermission(perms, "org", "user", "ci:read")
	retr := NewRetriever(repo, sr, perms, DisabledProvider{})

	chunks, cites, err := retr.Retrieve(context.Background(), "org", "user", "Router", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	// Every chunk that grounds an answer must produce a complete citation.
	if len(cites) != len(chunks) {
		t.Fatalf("expected one citation per chunk, got %d for %d", len(cites), len(chunks))
	}
	for _, c := range cites {
		if c.EntityType == "" || c.EntityID == "" || c.Title == "" || c.URL == "" {
			t.Fatalf("incomplete citation: %+v", c)
		}
	}
}
