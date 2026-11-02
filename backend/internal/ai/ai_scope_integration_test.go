package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestAIChunkScope runs the RAG chunk store with the principal's tenant scope
// (TEN-06, WP-021): retrieval for a principal restricted to client 1 returns
// no chunk of a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAIChunkScope(t *testing.T) {
	f := scopetest.Seed(t, "3e")
	own := f.CI(t, f.OrgA, f.Client1, "ai-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "ai-c2")
	repo := ai.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for _, id := range []string{own, foreign} {
		if err := repo.UpsertChunk(orgCtx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, Title: id, Content: "server"}); err != nil {
			t.Fatalf("org-wide chunk %s: %v", id, err)
		}
	}

	chunks, err := repo.CandidateChunks(f.ClientCtx(f.Client1), f.OrgA, []string{}, []string{}, 100)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(chunks) != 1 || chunks[0].EntityID != own {
		t.Fatalf("client-1 chunks: %+v, want only the own CI", chunks)
	}
	if chunks, err = repo.CandidateChunks(orgCtx, f.OrgA, []string{}, []string{}, 100); err != nil || len(chunks) != 2 {
		t.Fatalf("org-wide chunks: %d err=%v, want 2", len(chunks), err)
	}
	if _, err = repo.CandidateChunks(context.Background(), f.OrgA, []string{}, []string{}, 10); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("candidates without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestAIChunkWritesClientScope covers the write side of the RAG chunk store
// (TEN-06, WP-021): a principal restricted to client 1 can neither create
// nor overwrite (UpsertChunk, UpsertChunks) nor delete (DeleteChunk) the
// chunk of a client-2 CI; the client-2 chunk stays unchanged. The chunk of
// its own CI it writes and deletes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAIChunkWritesClientScope(t *testing.T) {
	f := scopetest.Seed(t, "3e")
	own := f.CI(t, f.OrgA, f.Client1, "ai-w-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "ai-w-c2")
	foreignNew := f.CI(t, f.OrgA, f.Client2, "ai-w-c2-new")
	repo := ai.NewPGRepository(f.App)
	bg := context.Background()
	if err := repo.UpsertChunk(f.OrgCtx(f.OrgA), ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreign, Title: "c2", Content: "client 2 original"}); err != nil {
		t.Fatalf("org-wide chunk of the client-2 CI: %v", err)
	}
	chunkOf := func(entityID string) string {
		t.Helper()
		var state string
		err := f.Admin.QueryRow(bg, `SELECT title || '|' || content || '|' || COALESCE(client_id::text, '') || '|' || updated_at::text FROM ai_chunk WHERE entity_type = 'ci' AND entity_id = $1`, entityID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatalf("read chunk: %v", err)
		}
		return state
	}
	before := chunkOf(foreign)
	if before == "" || !strings.HasPrefix(before, "c2|client 2 original|"+f.Client2+"|") {
		t.Fatalf("client-2 chunk: %q", before)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.UpsertChunk(ctx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreign, Title: "hijacked", Content: "client 1"}); err == nil {
		t.Error("client-1 principal overwrote the chunk of a client-2 CI")
	}
	if err := repo.UpsertChunk(ctx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreignNew, Title: "planted", Content: "client 1"}); err == nil {
		t.Error("client-1 principal created a chunk for a client-2 CI")
	}
	if err := repo.UpsertChunks(ctx, []ai.Chunk{
		{OrganizationID: f.OrgA, EntityType: "ci", EntityID: own, Title: "batch", Content: "client 1"},
		{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreign, Title: "hijacked", Content: "client 1"},
	}); err == nil {
		t.Error("client-1 principal overwrote the chunk of a client-2 CI in a batch")
	}
	if err := repo.DeleteChunk(ctx, f.OrgA, "ci", foreign); err != nil {
		t.Errorf("client-1 delete of a client-2 chunk: %v, want a no-op", err)
	}
	if after := chunkOf(foreign); after != before {
		t.Errorf("client-2 chunk after client-1 writes: %q, want %q", after, before)
	}
	if got := chunkOf(foreignNew); got != "" {
		t.Errorf("chunk of the second client-2 CI: %q, want none", got)
	}
	if got := chunkOf(own); got != "" {
		t.Errorf("own chunk after the refused batch: %q, want none (the batch is atomic)", got)
	}

	// The own CI: write, overwrite, delete.
	if err := repo.UpsertChunk(ctx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: own, Title: "c1", Content: "first"}); err != nil {
		t.Fatalf("client-1 chunk of the own CI: %v", err)
	}
	if err := repo.UpsertChunks(ctx, []ai.Chunk{{OrganizationID: f.OrgA, EntityType: "ci", EntityID: own, Title: "c1", Content: "second"}}); err != nil {
		t.Fatalf("client-1 batch for the own CI: %v", err)
	}
	if got := chunkOf(own); !strings.HasPrefix(got, "c1|second|"+f.Client1+"|") {
		t.Errorf("own chunk: %q, want the second content for client 1", got)
	}
	if err := repo.DeleteChunk(ctx, f.OrgA, "ci", own); err != nil {
		t.Fatalf("client-1 delete of the own chunk: %v", err)
	}
	if got := chunkOf(own); got != "" {
		t.Errorf("own chunk after delete: %q, want none", got)
	}
	if after := chunkOf(foreign); after != before {
		t.Errorf("client-2 chunk at the end: %q, want %q", after, before)
	}
}
