// Phase-1 acceptance audit, group T2-auth-authz: regression test of the AI
// opt-in on the assistant's Ask path, through the production router over
// PostgreSQL.
//
//   - WP-076 (AI-02), finding F-073 "The AI assistant Ask path sends tenant
//     data to the external provider without org opt-in" (WP-360): external
//     AI processing only with the organization's opt-in
//     (organization.ai_opt_in) and the ai add-on. The router wires the same
//     consent as the CI chunk indexer of cmd/server/main.go
//     (ai.NewOrgConsent) into the handler and the retriever: without it
//     POST /api/v1/ai/ask answers 403 ai-opt-in-required, creates no
//     conversation and calls the provider neither for the embedding nor for
//     the chat.
//
// Both organizations hold the ai add-on (as granted by the operator) unless
// stated otherwise, each user holds ci:read through the org_admin role, a CI
// is indexed for search and as an AI chunk in each organization.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestAuditT2AIAskRequiresOrgOptIn$' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// t2CountingProvider is an enabled external provider that counts what it is
// sent.
type t2CountingProvider struct{ chats, embeds atomic.Int32 }

func (p *t2CountingProvider) Enabled() bool           { return true }
func (p *t2CountingProvider) EmbeddingsEnabled() bool { return true }
func (p *t2CountingProvider) Chat([]ai.Message) (string, int, int, error) {
	p.chats.Add(1)
	return "fake answer", 1, 1, nil
}

func (p *t2CountingProvider) Embed(string) ([]float64, error) {
	p.embeds.Add(1)
	return []float64{1, 0}, nil
}

// TestAuditT2AIAskRequiresOrgOptIn: without the organization's opt-in or
// without the ai add-on the Ask path refuses with 403, creates no
// conversation and the provider receives nothing; with both the question
// is embedded once and answered once, while the other organization stays
// refused.
func TestAuditT2AIAskRequiresOrgOptIn(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	f := scopetest.Seed(t, "ed")
	bg := context.Background()
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})

	// seedOrg gives the organization an org_admin user, an indexed CI with
	// its AI chunk and the plan's entitlements; it returns the user.
	seedOrg := func(orgID, name string) string {
		t.Helper()
		orgCtx := f.OrgCtx(orgID)
		userID := f.AppUser(t, orgID, "t2-ai-user-"+name)
		if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, orgID, userID); err != nil {
			t.Fatal(err)
		}
		title := "t2aioptinprobe " + name
		ciID := f.CI(t, orgID, "", title)
		if err := search.NewPGRepository(f.App).IndexDocument(orgCtx, search.Document{OrganizationID: orgID, EntityType: "ci", EntityID: ciID,
			Title: title, URL: "/cis/" + ciID}); err != nil {
			t.Fatalf("index search document: %v", err)
		}
		if err := ai.NewPGRepository(f.App).UpsertChunk(orgCtx, ai.Chunk{OrganizationID: orgID, EntityType: "ci", EntityID: ciID,
			Title: title, Content: title + " runs the billing database", URL: "/cis/" + ciID}); err != nil {
			t.Fatalf("upsert chunk: %v", err)
		}
		if _, err := entitlements.Provision(orgCtx, orgID, entitlement.PlanEssential); err != nil {
			t.Fatalf("provision: %v", err)
		}
		return userID
	}
	grantAI := func(orgID string) {
		t.Helper()
		if _, err := entitlements.Grant(f.OrgCtx(orgID), entitlement.Entitlement{OrganizationID: orgID, FeatureKey: entitlement.FeatureAI,
			Plan: entitlement.PlanEssential, Enabled: true, Source: "manual"}); err != nil {
			t.Fatalf("grant ai add-on: %v", err)
		}
	}
	setOptIn := func(orgID string, v bool) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE organization SET ai_opt_in = $2 WHERE id = $1`, orgID, v); err != nil {
			t.Fatal(err)
		}
	}
	conversations := func(orgID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ai_conversation WHERE organization_id = $1`, orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	userA, userB := seedOrg(f.OrgA, "alpha"), seedOrg(f.OrgB, "bravo")
	grantAI(f.OrgA)

	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	provider := &t2CountingProvider{}
	// The router builds the consent itself (ai.NewOrgConsent over repos.AI
	// and the entitlements), as for the indexer of cmd/server/main.go.
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "t2-ai", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		AIProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.Chain(middleware.TenantMiddleware, entitlements.Middleware)(mux)
	ask := func(orgID, userID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", strings.NewReader(`{"question":"t2aioptinprobe"}`))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: identity.AllPermissions(), Credential: "t2-ai"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	// refused asserts a 403 problem+json of the given type, no provider call
	// and no conversation of the organization.
	refused := func(label, orgID, userID, problemType string) {
		t.Helper()
		chats, embeds, convs := provider.chats.Load(), provider.embeds.Load(), conversations(orgID)
		w := ask(orgID, userID)
		var problem struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") || problem.Type != problemType {
			t.Errorf("%s: POST /api/v1/ai/ask: %d %q %s; want 403 problem+json of type %s (AI-02)",
				label, w.Code, w.Header().Get("Content-Type"), strings.TrimSpace(w.Body.String()), problemType)
		}
		if provider.chats.Load() != chats || provider.embeds.Load() != embeds {
			t.Errorf("%s: provider got %d chat and %d embedding call(s); want none (AI-02)",
				label, provider.chats.Load()-chats, provider.embeds.Load()-embeds)
		}
		if n := conversations(orgID); n != convs {
			t.Errorf("%s: %d conversation(s) created; want none", label, n-convs)
		}
	}

	// Without opt-in, with the ai add-on: refused by the Ask handler.
	setOptIn(f.OrgA, false)
	refused("organization A without opt-in", f.OrgA, userA, ai.ProblemAIOptInRequired)

	// With opt-in, without the ai add-on: refused before the provider (the
	// entitlement gate of /api/v1/ai answers first).
	setOptIn(f.OrgB, true)
	refused("organization B with opt-in, without the ai add-on", f.OrgB, userB, api.ProblemFeatureNotEntitled)
	// The ai add-on without opt-in: refused by the Ask handler.
	grantAI(f.OrgB)
	setOptIn(f.OrgB, false)
	refused("organization B with the ai add-on, without opt-in", f.OrgB, userB, ai.ProblemAIOptInRequired)

	// Opt-in and add-on: exactly one embedding of the question and one chat.
	setOptIn(f.OrgA, true)
	convs := conversations(f.OrgA)
	if w := ask(f.OrgA, userA); w.Code != http.StatusOK || provider.embeds.Load() != 1 || provider.chats.Load() != 1 {
		t.Errorf("organization A with opt-in and add-on: %d %s; provider got %d embedding and %d chat call(s); want 200, one each",
			w.Code, strings.TrimSpace(w.Body.String()), provider.embeds.Load(), provider.chats.Load())
	}
	if n := conversations(f.OrgA); n != convs+1 {
		t.Errorf("organization A with opt-in: %d conversation(s) created; want 1", n-convs)
	}
	// The opt-in of A does not open B.
	refused("organization B while A opted in", f.OrgB, userB, ai.ProblemAIOptInRequired)
}
