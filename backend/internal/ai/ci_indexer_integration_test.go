package ai_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// embedProvider counts the embedding calls.
type embedProvider struct {
	ai.DisabledProvider
	calls atomic.Int32
}

func (p *embedProvider) EmbeddingsEnabled() bool { return true }
func (p *embedProvider) Embed(string) ([]float64, error) {
	p.calls.Add(1)
	return []float64{0.5, 0.5}, nil
}

// consentAll grants every organization and counts the checks made inside a
// context transaction.
type consentAll struct{ calls, inTx atomic.Int32 }

func (c *consentAll) AllowsExternalAI(ctx context.Context, _ string) bool {
	c.calls.Add(1)
	if database.InTx(ctx) {
		c.inTx.Add(1)
	}
	return true
}

// consentNone denies every organization.
type consentNone struct{}

func (consentNone) AllowsExternalAI(context.Context, string) bool { return false }

// TestCIChunkIndexerEmbedsAfterCommit covers WP-332 (NFR-03, AI-02) against
// PostgreSQL: inside a transaction in the context (the ingest's record
// transaction) the chunk is written lexically with the CI, and the consent
// check (entitlement cache, opt-in) and the embedding, an external HTTP call,
// follow only after the commit, also for a batch of chunks; a rolled back CI
// requests none, a denied consent none either. Outside a transaction the
// embedding is requested at once.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIChunkIndexerEmbedsAfterCommit(t *testing.T) {
	f := scopetest.Seed(t, "e0")
	ctx := f.OrgCtx(f.OrgA)
	repo := ai.NewPGRepository(f.App)
	provider := &embedProvider{}
	consent := &consentAll{}
	indexer := ai.NewCIChunkIndexer(repo, provider).WithConsent(consent)
	doc := func(name string) ci.Document {
		return ci.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: f.CI(t, f.OrgA, "", name), Title: name, Summary: "core router"}
	}
	chunk := func(ctx context.Context, id string) (found, embedded bool) {
		t.Helper()
		chunks, err := repo.CandidateChunks(ctx, f.OrgA, []string{"ci"}, []string{id}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return len(chunks) == 1, len(chunks) == 1 && len(chunks[0].Embedding) > 0
	}

	committed := doc("embed-committed")
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := indexer.IndexDocument(ctx, committed); err != nil {
			return err
		}
		if provider.calls.Load() != 0 {
			t.Error("embedding requested inside the transaction")
		}
		if found, embedded := chunk(ctx, committed.EntityID); !found || embedded {
			t.Errorf("chunk in the transaction: found %v, embedded %v; want the lexical chunk", found, embedded)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found, embedded := chunk(ctx, committed.EntityID); provider.calls.Load() != 1 || !found || !embedded {
		t.Errorf("after the commit: %d embedding calls, chunk found %v, embedded %v; want 1 embedded chunk",
			provider.calls.Load(), found, embedded)
	}

	rolledBack := doc("embed-rolled-back")
	errAbort := errors.New("abort")
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := indexer.IndexDocument(ctx, rolledBack); err != nil {
			return err
		}
		return errAbort
	}); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if found, _ := chunk(ctx, rolledBack.EntityID); provider.calls.Load() != 1 || found {
		t.Errorf("after the rollback: %d embedding calls, chunk found %v; want none", provider.calls.Load(), found)
	}

	direct := doc("embed-direct")
	if err := indexer.IndexDocument(ctx, direct); err != nil {
		t.Fatal(err)
	}
	if found, embedded := chunk(ctx, direct.EntityID); provider.calls.Load() != 2 || !embedded || !found {
		t.Errorf("outside a transaction: %d embedding calls, embedded %v; want the embedding at once", provider.calls.Load(), embedded)
	}

	// A batch of chunks (WP-333) checks the consent once, after the commit.
	batch := []ci.Document{doc("embed-batch-1"), doc("embed-batch-2")}
	checks := consent.calls.Load()
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		return indexer.IndexDocuments(ctx, batch)
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range batch {
		if found, embedded := chunk(ctx, d.EntityID); !found || !embedded {
			t.Errorf("batch chunk %s: found %v, embedded %v; want the embedding after the commit", d.Title, found, embedded)
		}
	}
	if provider.calls.Load() != 4 || consent.calls.Load() != checks+1 {
		t.Errorf("batch: %d embedding calls, %d consent checks; want 4 and 1 more", provider.calls.Load(), consent.calls.Load()-checks)
	}
	if n := consent.inTx.Load(); n != 0 {
		t.Errorf("%d consent checks inside the transaction, want none", n)
	}

	// Without consent the chunk stays lexical.
	denied := doc("embed-denied")
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		return ai.NewCIChunkIndexer(repo, provider).WithConsent(consentNone{}).IndexDocument(ctx, denied)
	}); err != nil {
		t.Fatal(err)
	}
	if found, embedded := chunk(ctx, denied.EntityID); provider.calls.Load() != 4 || !found || embedded {
		t.Errorf("denied consent: %d embedding calls, chunk found %v, embedded %v; want the lexical chunk only",
			provider.calls.Load(), found, embedded)
	}
}
