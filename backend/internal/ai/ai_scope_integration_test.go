package ai_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
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
