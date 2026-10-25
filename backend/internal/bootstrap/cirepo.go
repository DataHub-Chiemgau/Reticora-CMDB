// Package bootstrap holds wiring of cmd/server that tests build the same
// way, so a test of the wiring tests what runs in production.
package bootstrap

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// moduleAI is the late module of the AI assistant (add-on, server.LateModules).
const moduleAI = "ai"

// Modules reports whether a late module is enabled; server.ModuleSet from
// RETICORA_MODULES implements it.
type Modules interface {
	Enabled(key string) bool
}

// NewCIRepository decorates the CI repository as cmd/server runs it. The
// decorators keep every optional interface of the repository (reactivation,
// unique check, merge, …; SR-11).
//
//   - Every CI write (REST handler, collector bulk ingest, workflow
//     executor) keeps the tenant search index in sync. Indexing is
//     best-effort: a failing search backend never breaks CI persistence, and
//     the index can always be rebuilt via POST /api/v1/search/reindex.
//   - Only with the late module ai (E-03, F-301) are CI writes mirrored into
//     the retrieval chunk store of the governed RAG assistant. Chunks are
//     embedded only for organizations the consent admits (opt-in and ai
//     add-on, AI-02); otherwise they stay lexical. cmd/server passes
//     ai.NewOrgConsent, the consent the router wires into the Ask path.
//
// A nil search backend or AI repository leaves its decorator out; nil
// modules enable no late module (the G1 default).
func NewCIRepository(base ci.Repository, backend search.Backend, aiRepo ai.Repository, provider ai.Provider,
	consent ai.Consent, modules Modules) ci.Repository {
	repo := base
	if backend != nil {
		repo = ci.NewIndexingRepository(repo, search.NewCIIndexer(backend))
	}
	if aiRepo != nil && modules != nil && modules.Enabled(moduleAI) {
		repo = ci.NewIndexingRepository(repo, ai.NewCIChunkIndexer(aiRepo, provider).WithConsent(consent))
	}
	return repo
}
